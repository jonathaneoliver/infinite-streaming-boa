package boa

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

/*
 * The wedge from issue #182, and the one signal that reveals it.
 *
 * A radio comes back from an rfkill outage with hostapd reporting state=ENABLED
 * and no BSS on the air. Six instruments were measured on the box and every one
 * reads identically wedged and healthy, so the only thing left to test against
 * is the CONTRADICTION reenableAP can observe: it asked for an ENABLE, which it
 * only does when the access point did not look enabled, and hostapd refused on
 * the grounds that it already was.
 *
 * These tests drive that contradiction through the control-socket seam, because
 * the sequence is the whole behaviour -- a rebuild that fires when it should not
 * drops clients for nothing, and one that does not fire leaves a dead radio.
 */

// fakeAP is a scriptable hostapd. It answers STATUS from its own idea of
// whether the BSS is up, so a test states the radio's condition rather than
// scripting each reply in order.
type fakeAP struct {
	mu sync.Mutex

	// enabled is what STATUS reports. The wedge is enabled=false paired with
	// refuseEnable=true: nothing can see a BSS, and hostapd insists there is one.
	enabled bool
	// mute makes STATUS fail outright, which is what the control socket
	// actually does on mt7921u for the first 10-25s after an unblock.
	mute bool
	// refuseEnable answers FAIL to ENABLE, as hostapd does when it believes the
	// interface is already enabled.
	refuseEnable bool

	sent []string
}

func (f *fakeAP) send(iface, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, cmd)

	switch {
	case cmd == "STATUS":
		if f.mute {
			return "", fmt.Errorf("hostapd read: i/o timeout")
		}
		if f.enabled {
			return "state=ENABLED\nssid[0]=x\n", nil
		}
		return "state=DISABLED\n", nil

	case cmd == "DISABLE":
		// A real DISABLE clears the stuck state, which is exactly why it is the
		// half of the rebuild that matters.
		f.enabled = false
		f.mute = false
		f.refuseEnable = false
		return "OK\n", nil

	case cmd == "ENABLE":
		if f.refuseEnable {
			return "FAIL\n", nil
		}
		f.enabled = true
		return "OK\n", nil
	}
	return "OK\n", nil
}

func (f *fakeAP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeAP) count(cmd string) int {
	n := 0
	for _, c := range f.commands() {
		if c == cmd {
			n++
		}
	}
	return n
}

func (f *fakeAP) isEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

// withFakeAP points the control-socket seams at a scripted hostapd.
func withFakeAP(t *testing.T, f *fakeAP) {
	t.Helper()
	oS, oR := hostapdSend, hostapdReachable
	t.Cleanup(func() { hostapdSend, hostapdReachable = oS, oR })
	hostapdSend = f.send
	hostapdReachable = func(string) bool { return true }
}

func wedgeEngine() *Engine {
	return &Engine{cfg: Config{WlanPorts: []string{"wlan-usb"}}}
}

// The failure this issue is about: hostapd claims the interface is already
// enabled, while the check that just ran could not find a BSS. The radio must
// be rebuilt, not accepted.
func TestReenableAPRebuildsWhenHostapdContradictsItself(t *testing.T) {
	f := &fakeAP{mute: true, refuseEnable: true}
	withFakeAP(t, f)

	wedgeEngine().reenableAP("wlan-usb")

	if f.count("DISABLE") != 1 {
		t.Fatalf("wedged radio was not rebuilt: commands %v", f.commands())
	}
	if !f.isEnabled() {
		t.Fatal("radio was left down after the rebuild")
	}
	// DISABLE must come before the ENABLE that revives it: ENABLE alone is what
	// hostapd already refused, so a rebuild that skipped the teardown would
	// spend the only remedy available and change nothing.
	cmds := strings.Join(f.commands(), ",")
	di, ei := strings.Index(cmds, "DISABLE"), strings.LastIndex(cmds, "ENABLE")
	if di < 0 || ei < di {
		t.Fatalf("ENABLE did not follow DISABLE: %v", f.commands())
	}
}

// The common case must stay untouched. A radio whose access point came back on
// its own is not commanded at all -- hostapd watches rfkill itself, and an
// ENABLE aimed at a BSS it had already restored took switch-on from about a
// second to 25 on mt7921u.
func TestReenableAPLeavesAHealthyRecoveryAlone(t *testing.T) {
	f := &fakeAP{enabled: true}
	withFakeAP(t, f)

	wedgeEngine().reenableAP("wlan-usb")

	if n := f.count("ENABLE"); n != 0 {
		t.Fatalf("healthy radio was sent %d ENABLE commands", n)
	}
	if n := f.count("DISABLE"); n != 0 {
		t.Fatalf("healthy radio was rebuilt %d times, dropping its clients for nothing", n)
	}
}

// The case ENABLE actually exists for: the access point genuinely had not come
// back, hostapd accepts the command and brings it up. That is a success, and it
// must NOT be followed by a rebuild -- the clients would be dropped a second
// time, moments after reconnecting.
func TestReenableAPDoesNotRebuildWhenEnableWorks(t *testing.T) {
	f := &fakeAP{mute: false, enabled: false}
	withFakeAP(t, f)

	wedgeEngine().reenableAP("wlan-usb")

	if n := f.count("ENABLE"); n != 1 {
		t.Fatalf("expected exactly one ENABLE, got %d: %v", n, f.commands())
	}
	if n := f.count("DISABLE"); n != 0 {
		t.Fatalf("a working ENABLE was followed by %d rebuilds", n)
	}
	if !f.isEnabled() {
		t.Fatal("radio did not come up")
	}
}

// rebuildBSS waits for the teardown to finish before building back up. An
// ENABLE sent while hostapd is still tearing down is refused, which would leave
// the radio down having spent the one action that could have fixed it.
func TestRebuildBSSWaitsForTheTeardown(t *testing.T) {
	f := &fakeAP{enabled: true, refuseEnable: true}
	withFakeAP(t, f)

	wedgeEngine().rebuildBSS("wlan-usb")

	cmds := f.commands()
	// The hush comes FIRST, before the teardown it is meant to silence: a
	// broadcast deauth sent on the way down would announce the recovery to
	// clients still working out that the outage happened (#224).
	if len(cmds) < 3 || cmds[0] != "SET broadcast_deauth 0" {
		t.Fatalf("rebuild did not silence the teardown first: %v", cmds)
	}
	if cmds[1] != "DISABLE" {
		t.Fatalf("rebuild did not tear down after hushing: %v", cmds)
	}
	// A STATUS between the two proves it waited on the answer rather than on a
	// sleep chosen to look long enough.
	var sawStatusBetween bool
	for _, c := range cmds[2:] {
		if c == "ENABLE" {
			break
		}
		if c == "STATUS" {
			sawStatusBetween = true
		}
	}
	if !sawStatusBetween {
		t.Fatalf("rebuild did not wait for the BSS to go down: %v", cmds)
	}
	if !f.isEnabled() {
		t.Fatal("rebuild left the radio down")
	}
}

// An unanswerable question must not be reported as a bad answer.
//
// This is the bug the first hardware run exposed: hostapd's socket stops
// answering for minutes while mt7921u re-initialises, and every STATUS in that
// window fails. Treating those failures as "not serving" made a rebuild that
// WORKED announce itself as "could not be rebuilt ... serving nobody".
func TestAPStateSeparatesDownFromUnreachable(t *testing.T) {
	f := &fakeAP{enabled: false, mute: true}
	withFakeAP(t, f)

	if up, known := apState("wlan-usb"); up || known {
		t.Fatalf("an unanswered STATUS reported a known state: up=%v known=%v", up, known)
	}

	f.mu.Lock()
	f.mute = false
	f.mu.Unlock()

	if up, known := apState("wlan-usb"); up || !known {
		t.Fatalf("a genuine DISABLED was not reported as known: up=%v known=%v", up, known)
	}

	f.mu.Lock()
	f.enabled = true
	f.mu.Unlock()

	if up, known := apState("wlan-usb"); !up || !known {
		t.Fatalf("a serving AP was not reported: up=%v known=%v", up, known)
	}
}

// reenableAP tells its caller whether it rebuilt, so one recovery is not
// announced twice -- once by the rebuild and again by the watch above it.
func TestReenableAPReportsWhetherItRebuilt(t *testing.T) {
	healthy := &fakeAP{enabled: true}
	withFakeAP(t, healthy)
	if wedgeEngine().reenableAP("wlan-usb") {
		t.Fatal("a healthy recovery claimed to have rebuilt the BSS")
	}

	wedged := &fakeAP{mute: true, refuseEnable: true}
	withFakeAP(t, wedged)
	if !wedgeEngine().reenableAP("wlan-usb") {
		t.Fatal("a rebuilt BSS was not reported as rebuilt")
	}
}

// Cycling a radio must not set rebuilds fighting each other.
//
// Every power-on starts a watch, and a watch can run for minutes while a wedged
// mt7921u re-initialises. Before the guard, an operator switching a radio off
// and on a few times had several watches alive at once, each firing its own
// DISABLE/ENABLE and undoing the last -- seen on the box as seventeen "rebuilt
// and serving again" lines at the same second, and a recovery that took 97
// seconds because the rebuilds were tearing down each other's work.
func TestOnlyOneRecoveryPerRadioAtATime(t *testing.T) {
	f := &fakeAP{mute: true, refuseEnable: true}
	withFakeAP(t, f)
	e := wedgeEngine()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.confirmAPBack("wlan-usb")
		}()
	}
	wg.Wait()

	// Exactly one teardown. Five would mean five concurrent rebuilds, which is
	// the bug: each DISABLE drops the access point another had just brought up.
	if n := f.count("DISABLE"); n != 1 {
		t.Fatalf("expected one rebuild across five concurrent watches, got %d: %v",
			n, f.commands())
	}
	if !f.isEnabled() {
		t.Fatal("the radio was left down")
	}
}

// The guard must be released, or a radio recovers once and never again.
func TestRecoveryGuardIsReleased(t *testing.T) {
	e := wedgeEngine()
	if !e.startRecovery("wlan-usb") {
		t.Fatal("could not claim a free radio")
	}
	if e.startRecovery("wlan-usb") {
		t.Fatal("claimed a radio that was already being recovered")
	}
	// A second radio is unaffected: the guard is per radio, not global.
	if !e.startRecovery("wlan0") {
		t.Fatal("one radio's recovery blocked another's")
	}
	e.endRecovery("wlan-usb")
	if !e.startRecovery("wlan-usb") {
		t.Fatal("the guard was not released")
	}
}

// A deadzone must survive the restart that recovers a wedged radio.
//
// Deny lists are runtime state: set over the control socket, backed by no
// deny_mac_file, and erased when hostapd is replaced. The wedge recovery
// replaces hostapd, so without this a ban set for sixty seconds would just end
// early -- the client quietly allowed back mid-outage, and the measurement it
// belonged to wrong in a way nothing on screen could show.
func TestDeadzonesAreRestoredAfterARestart(t *testing.T) {
	f := &fakeAP{enabled: true}
	withFakeAP(t, f)
	e := wedgeEngine()

	e.noteDeadzone("aa:bb:cc:dd:ee:ff", []string{"wlan-usb"}, 60*time.Second)
	e.reapplyDeadzones("wlan-usb")

	var added bool
	for _, c := range f.commands() {
		if strings.HasPrefix(c, "DENY_ACL ADD_MAC aa:bb:cc:dd:ee:ff") {
			added = true
		}
	}
	if !added {
		t.Fatalf("the ban was not put back: %v", f.commands())
	}
}

// A ban that has already run its course must NOT be reinstated: putting it back
// would strand a client that was due to be let in.
func TestExpiredDeadzonesAreNotRestored(t *testing.T) {
	f := &fakeAP{enabled: true}
	withFakeAP(t, f)
	e := wedgeEngine()

	e.noteDeadzone("aa:bb:cc:dd:ee:ff", []string{"wlan-usb"}, -1*time.Second)
	e.reapplyDeadzones("wlan-usb")

	for _, c := range f.commands() {
		if strings.HasPrefix(c, "DENY_ACL ADD_MAC") {
			t.Fatalf("an expired ban was reinstated: %v", f.commands())
		}
	}
}

// A ban covering only the OTHER radio is left alone when this one restarts.
func TestDeadzonesOnOtherRadiosAreLeftAlone(t *testing.T) {
	f := &fakeAP{enabled: true}
	withFakeAP(t, f)
	e := wedgeEngine()

	e.noteDeadzone("aa:bb:cc:dd:ee:ff", []string{"wlan0"}, 60*time.Second)
	e.reapplyDeadzones("wlan-usb")

	for _, c := range f.commands() {
		if strings.HasPrefix(c, "DENY_ACL ADD_MAC") {
			t.Fatalf("another radio's ban was applied here: %v", f.commands())
		}
	}
}
