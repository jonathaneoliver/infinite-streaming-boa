package boa

import (
	"errors"
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
			gotS, gotU := clampToFloor(tc.stations, tc.util, floor)
			if gotS != tc.wantStations || gotU != tc.wantUtil {
				t.Fatalf("clampToFloor(%d, %v) = %d, %v; want %d, %v",
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
	if _, err := e.SetBSSLoad("wlan-usb", true, 12, 60); err != nil {
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
	_, err := e.SetBSSLoad("wlan-usb", true, 12, 60)
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
 * restart it, and each one silently drops the element. reapplyBSSLoad is the
 * one place that is put back, so what it does and does NOT touch is the whole
 * behaviour: re-asserting a radio the operator switched off would be this
 * control turning itself back on.
 */
func TestReapplyOnlyRestoresRadiosStillClaiming(t *testing.T) {
	sent := captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb", "wlan-usb2", "wlan0"}}}

	if _, err := e.SetBSSLoad("wlan-usb", true, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad wlan-usb: %v", err)
	}
	if _, err := e.SetBSSLoad("wlan-usb2", false, 3, 20); err != nil {
		t.Fatalf("SetBSSLoad wlan-usb2: %v", err)
	}
	_ = sent() // the setup traffic; what matters is what the restart replays.

	e.reapplyBSSLoad()

	want := [][2]string{
		{"wlan-usb", "SET bss_load_test 12:153:0"},
		{"wlan-usb", "UPDATE_BEACON"},
	}
	if got := sent(); !sameCalls(got, want) {
		t.Fatalf("reapply sent %v, want only the radio still claiming: %v", got, want)
	}
}

// Switching the element off must not lose the numbers behind it: an operator
// toggling a claim to see whether a phone moves would otherwise find the
// sliders back on the floor every time.
func TestBSSLoadKeepsItsNumbersWhileSwitchedOff(t *testing.T) {
	captureHostapd(t, nil)
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan-usb"}}}

	if _, err := e.SetBSSLoad("wlan-usb", true, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad on: %v", err)
	}
	if _, err := e.SetBSSLoad("wlan-usb", false, 12, 60); err != nil {
		t.Fatalf("SetBSSLoad off: %v", err)
	}

	st := e.BSSLoadStates([]string{"wlan-usb"})["wlan-usb"]
	if st.On {
		t.Fatal("still advertising after being switched off")
	}
	if st.Stations != 12 || st.UtilPct != 60 {
		t.Fatalf("switching off lost the settings: %d station(s), %v%%", st.Stations, st.UtilPct)
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
