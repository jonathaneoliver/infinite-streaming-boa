package boa

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

/*
 * ADVERTISING A CLAIM IS ONLY SAFE IN ONE DIRECTION.
 *
 * This is the only control on the box that can state something untrue about the
 * radio, so the tests here are about the two ways that goes wrong: claiming
 * LESS load than there is, which would pull devices onto equipment nobody here
 * owns; and getting the wire encoding wrong, which would make every claim a
 * different number from the one on screen.
 *
 * The 0-255 conversion in particular is exactly the class of mistake
 * docs/DATA-CONTRACT.md was written to stop -- 60 on the wire is 23.5%, not
 * 60%, and both readings look plausible in a log line.
 */

func TestBSSLoadOnlyEverClaimsMoreThanIsThere(t *testing.T) {
	floor := BSSLoadState{FloorStations: 4, FloorUtilPct: 37.5}

	for _, tc := range []struct {
		name         string
		stations     int
		util         float64
		wantStations int
		wantUtil     float64
	}{
		{"below both floors snaps up to them", 0, 0, 4, 37.5},
		{"above both is left alone", 20, 80, 20, 80},
		{"one below, one above", 1, 90, 4, 90},
		{"exactly on the floor stays", 4, 37.5, 4, 37.5},
		// The caps are the FIELD, not a policy: utilisation is a percentage and
		// hostapd takes the station count as a byte, so a larger number would be
		// truncated into a SMALLER claim than was asked for -- a lie in the one
		// direction this control must never tell.
		{"utilisation cannot exceed the whole channel", 5, 400, 5, 100},
		{"station count cannot exceed the byte", 9000, 50, 255, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotS, gotU := raiseToFloor(tc.stations, tc.util, floor)
			if gotS != tc.wantStations || gotU != tc.wantUtil {
				t.Fatalf("raiseToFloor(%d, %v) = %d, %v; want %d, %v",
					tc.stations, tc.util, gotS, gotU, tc.wantStations, tc.wantUtil)
			}
		})
	}
}

// A percentage on screen and a byte on the wire are different numbers, and the
// conversion happens in exactly one place. If it ever happens twice, or not at
// all, this is what says so.
func TestBSSLoadWireEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		ov   bssLoadOverride
		want string
	}{
		// All-zero is how hostapd is told to stop overriding. It does NOT remove
		// the element -- measured 2026-09-08, the beacon still carries a BSS Load
		// reading 0/255 afterwards -- so what this asserts is that switching off
		// stops the CLAIM, whatever hostapd then chooses to put in the beacon.
		{"off stops overriding", bssLoadOverride{On: false, Stations: 9, UtilPct: 80}, "0:0:0"},
		{"zero is zero", bssLoadOverride{On: true}, "0:0:0"},
		{"half the channel", bssLoadOverride{On: true, Stations: 3, UtilPct: 50}, "3:128:0"},
		{"the whole channel", bssLoadOverride{On: true, Stations: 12, UtilPct: 100}, "12:255:0"},
		// The value measured on hardware on 2026-09-08, kept as the case a reader
		// can check against a real beacon: 40/255 reads back as 15.7%.
		{"the measured case", bssLoadOverride{On: true, Stations: 3, UtilPct: 15.7}, "3:40:0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bssLoadArg(tc.ov); got != tc.want {
				t.Fatalf("bssLoadArg(%+v) = %q, want %q", tc.ov, got, tc.want)
			}
		})
	}
}

// SET alone returns OK and changes nothing -- the beacon is only rebuilt when
// something asks it to be. Measured 2026-09-08, and it is the whole reason this
// works without a restart, so the pair is asserted rather than assumed.
func TestBSSLoadRebuildsTheBeacon(t *testing.T) {
	sent := captureHostapd(t, nil)

	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}
	if _, err := e.SetBSSLoad("wlan-usb", true, false, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad: %v", err)
	}

	want := [][2]string{
		{"wlan-usb", "SET bss_load_test 12:153:0"},
		{"wlan-usb", "UPDATE_BEACON"},
	}
	if got := sent(); !sameCalls(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
}

// A beacon hostapd would not rebuild is a claim that never reached the air, and
// reporting success for it would leave an operator watching a client that has
// been told nothing. The SET succeeding is not the outcome.
func TestBSSLoadFailsLoudlyWhenTheBeaconIsNotRebuilt(t *testing.T) {
	captureHostapd(t, func(_, cmd string) error {
		if cmd == "UPDATE_BEACON" {
			return errors.New("FAIL")
		}
		return nil
	})

	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}
	_, err := e.SetBSSLoad("wlan-usb", true, false, 12, 60)
	if err == nil {
		t.Fatal("SetBSSLoad reported success after hostapd refused to rebuild the beacon")
	}
	// The error has to name the consequence, not just the verb that failed: an
	// operator reading "UPDATE_BEACON failed" cannot tell whether the claim is
	// on the air or not.
	if !strings.Contains(err.Error(), "nothing changed on the air") {
		t.Fatalf("error does not say the claim never reached the air: %v", err)
	}
}

/*
 * THE OVERRIDE LIVES IN THE RUNNING hostapd AND NOWHERE ELSE.
 *
 * A width change, a profile, a USB re-enumeration and a driver reload all
 * restart it, and each one silently drops the element. reapplyBSSLoad is where
 * it is put back, so what it does and does NOT touch is the whole behaviour.
 *
 * Three radios, three states, and the distinction that matters is between a
 * radio nobody has touched and one an operator switched OFF:
 *
 *	wlan-usb   a standing claim          -> replayed
 *	wlan-usb2  explicitly switched off   -> LEFT ALONE. Re-asserting here would
 *	                                        be the control turning itself back
 *	                                        on, which no amount of good
 *	                                        intention makes acceptable.
 *	wlan0      never touched             -> the default, asserted. With nothing
 *	                                        to correct with that is a 0:0:0,
 *	                                        which is how a stale claim from a
 *	                                        previous daemon gets cleared.
 */
func TestReapplyRespectsAnOptOutButAssertsTheDefault(t *testing.T) {
	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState { return BSSLoadState{} }
	origNb := neighbourUtilFor
	t.Cleanup(func() { neighbourUtilFor = origNb })
	neighbourUtilFor = func(*Engine, string) (float64, bool) { return 0, false }

	sent := captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb", "wlan-usb2", "wlan0"}}}

	if _, err := e.SetBSSLoad("wlan-usb", true, false, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad wlan-usb: %v", err)
	}
	if _, err := e.SetBSSLoad("wlan-usb2", false, false, 3, 20); err != nil {
		t.Fatalf("SetBSSLoad wlan-usb2: %v", err)
	}
	_ = sent() // the setup traffic; what matters is what the restart replays.

	e.reapplyBSSLoad()

	want := [][2]string{
		{"wlan-usb", "SET bss_load_test 12:153:0"},
		{"wlan-usb", "UPDATE_BEACON"},
		{"wlan0", "SET bss_load_test 0:0:0"},
		{"wlan0", "UPDATE_BEACON"},
	}
	if got := sent(); !sameCalls(got, want) {
		t.Fatalf("reapply sent %v, want the claim replayed and the untouched radio "+
			"asserted, with the opted-out one left alone: %v", got, want)
	}
}

// Switching the element off must not lose the numbers behind it: an operator
// toggling a claim to see whether a phone moves would otherwise find the
// sliders back on the floor every time.
func TestBSSLoadKeepsItsNumbersWhileSwitchedOff(t *testing.T) {
	captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}

	if _, err := e.SetBSSLoad("wlan-usb", true, false, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad on: %v", err)
	}
	if _, err := e.SetBSSLoad("wlan-usb", false, false, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad off: %v", err)
	}

	st := e.BSSLoadStates([]string{"wlan-usb"}, nil)["wlan-usb"]
	if st.On {
		t.Fatal("still advertising after being switched off")
	}
	if st.Stations != 12 || st.UtilPct != 60 {
		t.Fatalf("switching off lost the settings: %d station(s), %v%%", st.Stations, st.UtilPct)
	}
}

/*
 * THE FLOOR IS A MOVING MEASUREMENT, SO IT IS APPLIED AT SEND TIME.
 *
 * Measured 2026-09-08: our own airtime went 0% to 80% within four seconds of an
 * iperf3 starting. A claim checked only when the operator made it is therefore
 * stale almost immediately -- and the interface was already drawing the raised
 * figure, so the screen and the air disagreed exactly when traffic outran the
 * claim.
 *
 * The other half of that, and the reason the raised value is NOT stored: the
 * floor is re-asserted every 15s, so storing it would ratchet. One burst would
 * lift a 20% claim to 80% and nothing would bring it back, until every radio
 * permanently advertised its own worst moment.
 */
func TestTheFloorIsAppliedAtSendTimeAndNeverRatchets(t *testing.T) {
	sent := captureHostapd(t, nil)

	var floor float64
	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState {
		return BSSLoadState{FloorUtilPct: floor, FloorKnown: true}
	}

	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}

	// Claimed while the radio is quiet: the ask reaches the air unchanged.
	if _, err := e.SetBSSLoad("wlan-usb", true, false, 4, 20); err != nil {
		t.Fatalf("SetBSSLoad: %v", err)
	}
	if got := onlyLoadArg(t, sent()); got != "4:51:0" {
		t.Fatalf("quiet radio advertised %q, want the ask 4:51:0", got)
	}

	// Traffic starts. The next send must carry the FLOOR, not the stale ask.
	floor = 80
	e.reapplyBSSLoad()
	if got := onlyLoadArg(t, sent()); got != "4:204:0" {
		t.Fatalf("busy radio advertised %q, want the 80%% floor 4:204:0", got)
	}

	// Traffic stops. The claim must come back to what the operator chose, or the
	// floor has quietly become the setting.
	floor = 0
	e.reapplyBSSLoad()
	if got := onlyLoadArg(t, sent()); got != "4:51:0" {
		t.Fatalf("after the traffic stopped the radio advertised %q, want the "+
			"operator's 4:51:0 back — the floor has ratcheted", got)
	}

	// And the stored ask is still the ask, so the interface draws the handle
	// where it was put rather than where the traffic left it.
	if st := e.BSSLoadStates([]string{"wlan-usb"}, nil)["wlan-usb"]; st.UtilPct != 20 {
		t.Fatalf("stored claim is %v%%, want the operator's 20%%", st.UtilPct)
	}
}

// onlyLoadArg picks the bss_load_test parameter out of one send, asserting the
// UPDATE_BEACON went with it -- a SET on its own reaches no beacon at all.
func onlyLoadArg(t *testing.T, calls [][2]string) string {
	t.Helper()
	var arg string
	var beaconed bool
	for _, c := range calls {
		if a, ok := strings.CutPrefix(c[1], "SET bss_load_test "); ok {
			arg = a
		}
		if c[1] == "UPDATE_BEACON" {
			beaconed = true
		}
	}
	if arg == "" {
		t.Fatalf("no bss_load_test in %v", calls)
	}
	if !beaconed {
		t.Fatalf("bss_load_test %q was set but no UPDATE_BEACON followed, so it "+
			"never reached the air", arg)
	}
	return arg
}

/*
 * A DAEMON RESTART MUST NOT LEAVE A CLAIM ON THE AIR THAT NOTHING KNOWS ABOUT.
 *
 * MEASURED 2026-09-08 and this is the bug, not a hypothetical: a `deploy.sh
 * --ui-only` restarted the daemon, the in-memory store went with it, and the
 * box carried on beaconing 39 stations at 85% while the API and the interface
 * both reported it was claiming nothing. hostapd keeps bss_load_test across a
 * daemon restart and will not report it back -- GET bss_load_test returns FAIL
 * -- so the value cannot be adopted, only asserted.
 *
 * Which makes this the worst failure the feature can have. The whole safety
 * argument is that the truth is drawn beside the claim, and that is void the
 * moment the interface does not know what the claim IS.
 */
func TestAClaimSurvivesADaemonRestart(t *testing.T) {
	sent := captureHostapd(t, nil)
	path := filepath.Join(t.TempDir(), "bssload.json")

	first := &Engine{
		cfg:     Config{Demo: true, WlanPorts: []string{"wlan-usb"}},
		bssLoad: bssLoadStore{path: path},
	}
	if _, err := first.SetBSSLoad("wlan-usb", true, false, 39, 85); err != nil {
		t.Fatalf("SetBSSLoad: %v", err)
	}
	_ = sent()

	// The daemon dies and comes back. Nothing is carried over but the file.
	second := &Engine{
		cfg:     Config{Demo: true, WlanPorts: []string{"wlan-usb"}},
		bssLoad: bssLoadStore{path: path},
	}
	second.bssLoad.load()
	second.assertBSSLoad()

	if got := onlyLoadArg(t, sent()); got != "39:217:0" {
		t.Fatalf("after a restart the radio was told %q, want the claim back as "+
			"39:217:0 — the interface would be reporting nothing while the beacon "+
			"carried 85%%", got)
	}
	if st := second.BSSLoadStates([]string{"wlan-usb"}, nil)["wlan-usb"]; !st.On || st.UtilPct != 85 {
		t.Fatalf("restarted daemon reports on=%v at %v%%, want on at 85%%",
			st.On, st.UtilPct)
	}
}

// The other half, and the one a stored claim cannot cover: hostapd is holding a
// claim from a daemon whose state file is gone. Silence would leave the box
// lying with nothing on the box aware of it, so every radio is told something.
//
// This is also what keeps the correction being the DEFAULT from re-opening that
// bug. A radio with nothing to correct with must still be sent 0:0:0, because
// "no correction to make" and "leave the previous daemon's claim alone" are the
// same code path unless standing down is made an instruction.
func TestStartupClearsAClaimItHasNoRecordOf(t *testing.T) {
	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState { return BSSLoadState{} }
	origNb := neighbourUtilFor
	t.Cleanup(func() { neighbourUtilFor = origNb })
	neighbourUtilFor = func(*Engine, string) (float64, bool) { return 0, false }

	sent := captureHostapd(t, nil)

	e := &Engine{
		cfg:     Config{Demo: true, WlanPorts: []string{"wlan-usb", "wlan0"}},
		bssLoad: bssLoadStore{path: filepath.Join(t.TempDir(), "bssload.json")},
	}
	e.assertBSSLoad()

	want := [][2]string{
		{"wlan-usb", "SET bss_load_test 0:0:0"},
		{"wlan-usb", "UPDATE_BEACON"},
		{"wlan0", "SET bss_load_test 0:0:0"},
		{"wlan0", "UPDATE_BEACON"},
	}
	if got := sent(); !sameCalls(got, want) {
		t.Fatalf("startup sent %v, want every radio explicitly cleared: %v", got, want)
	}
}

/*
 * THE CORRECTION: ADVERTISING WHAT THIS BOX ACTUALLY MEASURES.
 *
 * hostapd fills the element in from the survey counter, and on this hardware
 * that counter is broken in a way one command's own output contradicts.
 * MEASURED 2026-09-08 over a 22.3s window at 494 Mbit/s:
 *
 *	survey 'channel busy time'      11.9%   <- what hostapd advertises
 *	survey receive + transmit       71.6%   <- same command, same window
 *	our own per-station tx+rx       78.9%   <- verified against iperf3
 *
 * So the correction is not an opinion about hostapd's number, it is a
 * replacement for one the driver disagrees with itself about.
 */
func TestTheCorrectionAdvertisesTheLargerLowerBound(t *testing.T) {
	var floor float64
	var neighbour float64
	var neighbourKnown bool

	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState {
		return BSSLoadState{FloorStations: 2, FloorUtilPct: floor, FloorKnown: true}
	}
	origNb := neighbourUtilFor
	t.Cleanup(func() { neighbourUtilFor = origNb })
	neighbourUtilFor = func(*Engine, string) (float64, bool) { return neighbour, neighbourKnown }

	sent := captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}

	// Our own airtime is the bigger of the two lower bounds.
	floor, neighbour, neighbourKnown = 78.9, 74.5, true
	if _, err := e.SetBSSLoad("wlan-usb", false, true, 0, 0); err != nil {
		t.Fatalf("SetBSSLoad: %v", err)
	}
	// 78.9% -> 201/255, and the station count is the REAL one, not a claim.
	if got := onlyLoadArg(t, sent()); got != "2:201:0" {
		t.Fatalf("advertised %q, want our own 78.9%% as 2:201:0", got)
	}

	// The neighbours are the bigger one: an idle radio on a channel somebody
	// else is filling. Adding them would double-count the same medium.
	floor, neighbour = 0, 30
	e.reapplyBSSLoad()
	if got := onlyLoadArg(t, sent()); got != "2:77:0" {
		t.Fatalf("advertised %q, want the neighbours' 30%% as 2:77:0", got)
	}
}

// Nothing measured and nobody to ask. Sending 0 would be indistinguishable from
// hostapd's own zero while claiming to be a correction, so the correction stands
// down rather than dressing an absence up as a measurement -- the same rule the
// air view follows in refusing to render "no scan yet" as 0%.
func TestTheCorrectionStandsDownWithNothingToCorrectWith(t *testing.T) {
	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState {
		return BSSLoadState{FloorStations: 0} // FloorKnown false: brcmfmac
	}
	origNb := neighbourUtilFor
	t.Cleanup(func() { neighbourUtilFor = origNb })
	neighbourUtilFor = func(*Engine, string) (float64, bool) { return 0, false }

	sent := captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan0"}}}
	if _, err := e.SetBSSLoad("wlan0", false, true, 0, 0); err != nil {
		t.Fatalf("SetBSSLoad: %v", err)
	}
	// 0:0:0, not nothing. Skipping the send would leave a previous daemon's
	// claim on the air, which is the bug assertBSSLoad exists to close.
	want := [][2]string{
		{"wlan0", "SET bss_load_test 0:0:0"},
		{"wlan0", "UPDATE_BEACON"},
	}
	if got := sent(); !sameCalls(got, want) {
		t.Fatalf("with nothing to correct with the radio was sent %v, want an "+
			"explicit stand-down: %v", got, want)
	}
}

// A deliberate claim outranks the correction. The operator asked for something
// specific, and it is floored at the truth anyway -- so there is no state in
// which having both switches on advertises LESS than the correction would.
func TestADeliberateClaimOutranksTheCorrection(t *testing.T) {
	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState {
		return BSSLoadState{FloorStations: 2, FloorUtilPct: 10, FloorKnown: true}
	}
	origNb := neighbourUtilFor
	t.Cleanup(func() { neighbourUtilFor = origNb })
	neighbourUtilFor = func(*Engine, string) (float64, bool) { return 30, true }

	sent := captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}
	if _, err := e.SetBSSLoad("wlan-usb", true, true, 40, 90); err != nil {
		t.Fatalf("SetBSSLoad: %v", err)
	}
	if got := onlyLoadArg(t, sent()); got != "40:230:0" {
		t.Fatalf("advertised %q, want the operator's 40 stations at 90%% as 40:230:0", got)
	}
}

// --- helpers -------------------------------------------------------------

// captureHostapd replaces the control-socket seam and returns a drain: each
// call reports what has been sent since the last one.
//
// Through the seam rather than the dialler, for the reason hostapd.go gives at
// length -- #205 was a control message addressed to the wrong radio, and no
// test could catch it while nothing in the package could observe WHICH radio a
// message went to. So the interface is asserted alongside every command.
func captureHostapd(t *testing.T, fail func(iface, cmd string) error) func() [][2]string {
	t.Helper()
	var mu sync.Mutex
	var sent [][2]string
	origSend, origReach := hostapdSend, hostapdReachable
	t.Cleanup(func() { hostapdSend, hostapdReachable = origSend, origReach })
	hostapdReachable = func(string) bool { return true }
	hostapdSend = func(iface, cmd string) (string, error) {
		mu.Lock()
		sent = append(sent, [2]string{iface, cmd})
		mu.Unlock()
		if fail != nil {
			if err := fail(iface, cmd); err != nil {
				return "", err
			}
		}
		return "OK\n", nil
	}
	return func() [][2]string {
		mu.Lock()
		defer mu.Unlock()
		out := sent
		sent = nil
		return out
	}
}

// sameCalls compares two sets of control messages ignoring order, since
// reapplyBSSLoad walks a map.
func sameCalls(got, want [][2]string) bool {
	if len(got) != len(want) {
		return false
	}
	cp := func(in [][2]string) [][2]string {
		out := append([][2]string(nil), in...)
		sort.Slice(out, func(i, j int) bool {
			if out[i][0] != out[j][0] {
				return out[i][0] < out[j][0]
			}
			return out[i][1] < out[j][1]
		})
		return out
	}
	a, b := cp(got), cp(want)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
