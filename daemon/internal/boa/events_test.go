package boa

import "testing"

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
