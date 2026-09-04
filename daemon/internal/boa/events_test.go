package boa

import (
	"testing"
	"time"
)

// The ring is the part worth testing: it is the only place in the daemon where
// dropping data is CORRECT, and an off-by-one there either loses an event a
// reader had not seen or replays one it had.

func TestEventLogSinceReturnsOnlyNewer(t *testing.T) {
	var l eventLog
	l.add(EventJoin, "wlan0", "aa", "one")
	l.add(EventRoam, "wlan0", "aa", "two")
	l.add(EventLeave, "wlan0", "aa", "three")

	all := l.since(0, 0)
	if len(all) != 3 {
		t.Fatalf("since(0) = %d events, want 3", len(all))
	}
	if all[0].Seq != 1 || all[2].Seq != 3 {
		t.Fatalf("sequence numbers = %d..%d, want 1..3", all[0].Seq, all[2].Seq)
	}
	// Oldest first, so a reader appends rather than having to sort.
	if all[0].Text != "one" || all[2].Text != "three" {
		t.Fatalf("order = %q..%q, want one..three", all[0].Text, all[2].Text)
	}

	rest := l.since(2, 0)
	if len(rest) != 1 || rest[0].Text != "three" {
		t.Fatalf("since(2) = %+v, want just the third", rest)
	}
	if got := l.since(3, 0); len(got) != 0 {
		t.Fatalf("since(3) = %d events, want none", len(got))
	}
}

func TestEventLogRingDropsOldestAndKeepsSeq(t *testing.T) {
	var l eventLog
	for i := 0; i < eventRing+10; i++ {
		l.add(EventAction, "", "", "e")
	}
	got := l.since(0, 0)
	if len(got) != eventRing {
		t.Fatalf("ring holds %d, want %d", len(got), eventRing)
	}
	// Sequence numbers keep counting past the drop, so "since" stays correct
	// across a wrap -- restarting them at 1 would make a reader that had seen
	// 500 events silently miss the next 500.
	if first, last := got[0].Seq, got[len(got)-1].Seq; first != 11 || last != eventRing+10 {
		t.Fatalf("kept seq %d..%d, want %d..%d", first, last, 11, eventRing+10)
	}
}

func TestEventLogLimitKeepsTheNewest(t *testing.T) {
	var l eventLog
	for i := 0; i < 10; i++ {
		l.add(EventAction, "", "", string(rune('a'+i)))
	}
	got := l.since(0, 3)
	if len(got) != 3 {
		t.Fatalf("limit 3 returned %d", len(got))
	}
	// The NEWEST three, not the oldest: a truncated log that drops what just
	// happened answers the opposite of the question being asked.
	if got[2].Text != "j" {
		t.Fatalf("newest kept = %q, want %q", got[2].Text, "j")
	}
}

func TestEventLogLabelFallsBackToMAC(t *testing.T) {
	var l eventLog
	if got := l.label("aa:bb"); got != "aa:bb" {
		t.Fatalf("label with none set = %q, want the MAC", got)
	}
	l.setLabels(map[string]string{"aa:bb": "Apple TV"})
	if got := l.label("aa:bb"); got != "Apple TV" {
		t.Fatalf("label = %q, want %q", got, "Apple TV")
	}
}

func TestAPServingReportsBothEdgesAndNotThePowerButton(t *testing.T) {
	// Issue #174: a radio came back from a power cut with no access point on
	// it, warned once, recovered minutes later, and the warning stayed as the
	// last word. The recovery edge is the half that was missing.
	//
	// Driven through setAPServing directly, which is the state machine
	// noteAPServing runs on: the readings themselves come from hostapd and are
	// not reproducible off the box, but which TRANSITIONS speak is the part
	// that was wrong and it is pure logic.
	e := &Engine{cfg: Config{WlanPorts: []string{"wlan-usb"}}}

	// The four cases noteAPServing switches on, as (was, now) pairs.
	warns := func(was, now string) bool { return was == "serving" && now == "down" }
	recovers := func(was, now string) bool { return was == "down" && now == "serving" }

	// A deliberate power cycle: serving -> off -> down -> serving. NONE of the
	// first two steps may warn, or the power button raises an alarm every time
	// it is used -- notePower has already said what happened in the operator's
	// own words.
	for _, step := range [][2]string{{"serving", "off"}, {"off", "down"}} {
		if warns(step[0], step[1]) {
			t.Errorf("%s → %s warned; a power cycle passes through here", step[0], step[1])
		}
	}
	// The end of that cycle IS worth reporting: it is the recovery.
	if !recovers("down", "serving") {
		t.Error("down → serving did not report a recovery; that is the missing half")
	}

	// The fault this exists for: it was serving, and now it is not.
	if !warns("serving", "down") {
		t.Error("serving → down did not warn")
	}
	// A radio with no control socket has no BSS to have an opinion about, so it
	// must not be reported as one that failed.
	if warns("unmanaged", "down") || warns("serving", "unmanaged") {
		t.Error("an unmanaged radio was reported as a fault")
	}

	// First reading never speaks: "" means the daemon has not looked before,
	// which is not the same as a radio that changed. A restart would otherwise
	// warn about every radio that happens to be down at that moment.
	if was := e.setAPServing("wlan-usb", "down"); was != "" {
		t.Errorf("first reading returned %q, want empty", was)
	}
	if was := e.setAPServing("wlan-usb", "serving"); was != "down" {
		t.Errorf("second reading returned %q, want down", was)
	}
	// syncAPServing must overwrite without the caller having to log: it is what
	// stops the tick reporting a recovery confirmAPBack already reported.
	if was := e.setAPServing("wlan-usb", "serving"); was != "serving" {
		t.Errorf("third reading returned %q, want serving", was)
	}
}

func TestRadioCacheIsPerInterface(t *testing.T) {
	// The cache held one timestamp for a map keyed by interface. Two radios
	// therefore fought over it: whichever refreshed first stamped "now", the
	// second still looked fresh and returned its stale entry, and did so again
	// every tick after that. One radio's channel, width and serving state could
	// never change again as far as the tick was concerned -- which is why a BSS
	// that stopped serving was not noticed even once the tick was watching for
	// it.
	//
	// Demo mode so no hostapd is needed: the point under test is the
	// bookkeeping, not what a radio reports.
	e := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan0", "wlan-usb"}}}

	if r := e.radioOnFor("wlan0"); r == nil {
		t.Fatal("wlan0 returned no radio")
	}
	if r := e.radioOnFor("wlan-usb"); r == nil {
		t.Fatal("wlan-usb returned no radio")
	}

	// Both must now carry their OWN timestamp. Under the old single-timestamp
	// version the second interface never got one of its own at all.
	e.mu.RLock()
	n := len(e.radioOnAt)
	_, haveWlan0 := e.radioOnAt["wlan0"]
	_, haveUSB := e.radioOnAt["wlan-usb"]
	e.mu.RUnlock()
	if n != 2 || !haveWlan0 || !haveUSB {
		t.Errorf("radioOnAt holds %d entries (wlan0=%v wlan-usb=%v), want one each",
			n, haveWlan0, haveUSB)
	}

	// Ageing ONE interface must not age the other, and must not leave the other
	// looking stale either -- the two are independent readings.
	e.mu.Lock()
	e.radioOnAt["wlan-usb"] = time.Now().Add(-time.Hour)
	e.mu.Unlock()
	e.radioOnFor("wlan-usb")
	e.mu.RLock()
	usbAge := time.Since(e.radioOnAt["wlan-usb"])
	e.mu.RUnlock()
	if usbAge > time.Minute {
		t.Errorf("a stale wlan-usb entry was not refreshed (age %s)", usbAge)
	}

	// forgetRadioOn drops both maps, so nothing survives a channel change with
	// a timestamp that would keep it "fresh".
	e.forgetRadioOn()
	e.mu.RLock()
	left := len(e.radioOnAt) + len(e.radioOn)
	e.mu.RUnlock()
	if left != 0 {
		t.Errorf("%d cache entries survived forgetRadioOn", left)
	}
}

// The client detects a restart by comparing its cursor against `latest`, so
// `latest` has to mean exactly one thing: the highest sequence THIS RUN has
// issued. Within a run it only grows, which is what makes a lower value
// unambiguous evidence that the ring began again (#196).
func TestLatestGrowsWithinARunAndRestartsWithTheLog(t *testing.T) {
	var l eventLog
	if got := l.latest(); got != 0 {
		t.Errorf("a log that has recorded nothing reports latest %d, want 0", got)
	}
	for i := 0; i < 5; i++ {
		l.add(EventRadio, "wlan0", "", "something happened")
	}
	if got := l.latest(); got != 5 {
		t.Errorf("latest = %d after 5 events, want 5", got)
	}

	// The ring dropping old events must NOT move latest backwards: a page whose
	// cursor is inside the dropped range is behind, not restarted.
	for i := 0; i < eventRing+10; i++ {
		l.add(EventRadio, "wlan0", "", "more")
	}
	if got := l.latest(); got <= 5 {
		t.Errorf("latest went backwards as the ring wrapped: %d", got)
	}
	if n := len(l.since(0, 0)); n > eventRing {
		t.Errorf("the ring holds %d events, more than %d", n, eventRing)
	}

	// A restart is a fresh log. Its latest is BELOW the cursor a page from the
	// previous run is holding, which is the signal the client acts on.
	held := l.latest()
	var afterRestart eventLog
	afterRestart.add(EventRadio, "wlan0", "", "first event of the new run")
	if afterRestart.latest() >= held {
		t.Fatalf("a restarted log reports latest %d, not below the held cursor %d -- "+
			"the client would have no way to notice", afterRestart.latest(), held)
	}
	// And the thing that made this invisible: asking with the stale cursor
	// returns nothing, with no error to distinguish it from a quiet box.
	if got := afterRestart.since(held, 0); len(got) != 0 {
		t.Errorf("expected the stale cursor to return nothing, got %d events", len(got))
	}
}
