package boa

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

/*
 * #444: OpenWrt's hostapd answers FAIL to `SET bss_load_test`, because the
 * wpad build does not compile the testing option in. That reply came back as a
 * successful exchange, so boa reported the correction as applied, logged
 * nothing, and re-sent it every 15 s -- 738 hostapd complaints in 49 minutes on
 * target 5, enough to roll logread over.
 *
 * captureHostapd can only fail the TRANSPORT; hostapd's own answer is always
 * "OK" there, which is exactly the blind spot. replyHostapd lets a test choose
 * what hostapd says.
 */

func replyHostapd(t *testing.T, reply func(iface, cmd string) string) (sent func() [][2]string) {
	t.Helper()
	var mu sync.Mutex
	var log [][2]string
	origSend, origReach := hostapdSend, hostapdReachable
	t.Cleanup(func() { hostapdSend, hostapdReachable = origSend, origReach })
	hostapdReachable = func(string) bool { return true }
	hostapdSend = func(iface, cmd string) (string, error) {
		mu.Lock()
		log = append(log, [2]string{iface, cmd})
		mu.Unlock()
		return reply(iface, cmd), nil
	}
	return func() [][2]string {
		mu.Lock()
		defer mu.Unlock()
		out := log
		log = nil
		return out
	}
}

// openWrtHostapd answers as the wpad on target 5 did: FAIL to the testing
// option, OK to everything else.
func openWrtHostapd(_, cmd string) string {
	if strings.HasPrefix(cmd, "SET bss_load_test") {
		return "FAIL\n"
	}
	return "OK\n"
}

func bssEngine(t *testing.T) *Engine {
	t.Helper()
	origFloor := bssFloorFor
	t.Cleanup(func() { bssFloorFor = origFloor })
	bssFloorFor = func(*Engine, string) BSSLoadState { return BSSLoadState{} }
	origNb := neighbourUtilFor
	t.Cleanup(func() { neighbourUtilFor = origNb })
	neighbourUtilFor = func(*Engine, string) (float64, bool) { return 0, false }
	return &Engine{
		cfg:     Config{Demo: true, WlanPorts: []string{"phy1-ap0"}},
		bssLoad: bssLoadStore{path: filepath.Join(t.TempDir(), "bssload.json")},
	}
}

func warnings(e *Engine, substr string) int {
	n := 0
	for _, ev := range e.Events(0, 1000) {
		if strings.Contains(ev.Text, substr) {
			n++
		}
	}
	return n
}

// A FAIL reply is a refusal, not a success, and says why.
func TestAFailReplyIsARefusalNotASuccess(t *testing.T) {
	replyHostapd(t, openWrtHostapd)
	e := bssEngine(t)

	err := e.applyBSSLoad("phy1-ap0")
	if !errors.Is(err, errBSSLoadRefused) {
		t.Fatalf("hostapd answered FAIL and applyBSSLoad returned %v; it reported success on a value that never reached the beacon", err)
	}
	if why := e.bssLoadRefusal("phy1-ap0"); !strings.Contains(why, "FAIL") {
		t.Errorf("the refusal was not remembered with hostapd's answer: %q", why)
	}
	if n := warnings(e, "cannot advertise a BSS Load value"); n != 1 {
		t.Errorf("the refusal was reported %d time(s), want exactly once", n)
	}

	// Pressing a BSS Load control on the radio asks again (SetBSSLoad calls
	// applyBSSLoad directly), and is refused again. The operator gets the
	// error from that request; the activity log does not get a second copy.
	if err := e.applyBSSLoad("phy1-ap0"); !errors.Is(err, errBSSLoadRefused) {
		t.Fatalf("second attempt returned %v", err)
	}
	if n := warnings(e, "cannot advertise a BSS Load value"); n != 1 {
		t.Errorf("a second refusal was reported again (%d in all); once is the point", n)
	}
}

// A refused radio is not asked again: the 15 s refresh is what flooded
// logread. One send to learn the answer, then silence -- and one warning.
func TestARefusedRadioIsNotAskedAgain(t *testing.T) {
	sent := replyHostapd(t, openWrtHostapd)
	e := bssEngine(t)

	e.assertBSSLoad() // startup: the one attempt that learns the answer
	if got := sent(); len(got) != 1 || got[0][1] != "SET bss_load_test 0:0:0" {
		t.Fatalf("startup sent %v, want one SET that is refused (and no UPDATE_BEACON after it)", got)
	}
	for i := 0; i < 5; i++ {
		e.reapplyBSSLoad() // five refresh rounds, 75 s of a real box
	}
	if got := sent(); len(got) != 0 {
		t.Errorf("a radio that refused was asked again %d time(s): %v", len(got), got)
	}
	if n := warnings(e, "cannot advertise a BSS Load value"); n != 1 {
		t.Errorf("reported %d time(s) across startup and five refreshes, want once", n)
	}
	if n := warnings(e, "could not restore"); n != 0 {
		t.Errorf("the refusal was also reported as a failure to restore (%d), so it is said twice", n)
	}
}

// The API tells the interface why, so it can say so instead of drawing
// controls that cannot reach the air.
func TestTheInventorySaysWhyBSSLoadIsUnavailable(t *testing.T) {
	replyHostapd(t, openWrtHostapd)
	e := bssEngine(t)
	_ = e.applyBSSLoad("phy1-ap0")

	st := e.BSSLoadStates([]string{"phy1-ap0"}, nil)["phy1-ap0"]
	if st.Unavailable == "" {
		t.Error("a radio whose hostapd refused BSS Load is reported as available")
	}
}

// The ordinary case is untouched: a hostapd that answers OK is sent both
// commands, and nothing is marked unavailable.
func TestAnOKReplyStillAppliesAndRebuildsTheBeacon(t *testing.T) {
	sent := replyHostapd(t, func(string, string) string { return "OK\n" })
	e := bssEngine(t)

	if err := e.applyBSSLoad("phy1-ap0"); err != nil {
		t.Fatalf("an accepted value returned %v", err)
	}
	want := [][2]string{{"phy1-ap0", "SET bss_load_test 0:0:0"}, {"phy1-ap0", "UPDATE_BEACON"}}
	if got := sent(); !sameCalls(got, want) {
		t.Errorf("sent %v, want %v", got, want)
	}
	if e.bssLoadRefusal("phy1-ap0") != "" {
		t.Error("a hostapd that accepted the value is marked as refusing it")
	}
}

// UPDATE_BEACON's reply is read too: an accepted value that is never rebuilt
// into the beacon changed nothing on the air.
func TestAFailedBeaconRebuildIsAnError(t *testing.T) {
	replyHostapd(t, func(_, cmd string) string {
		if cmd == "UPDATE_BEACON" {
			return "FAIL\n"
		}
		return "OK\n"
	})
	e := bssEngine(t)
	if err := e.applyBSSLoad("phy1-ap0"); err == nil {
		t.Error("UPDATE_BEACON answered FAIL and applyBSSLoad reported success")
	}
}
