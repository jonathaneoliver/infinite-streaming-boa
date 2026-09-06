package boa

import (
	"testing"
	"time"
)

// An explicit departure is recorded and survives being read.
//
// assocSeen cannot serve this: assocTime consumes it so that one transition
// stamps one event, while the tick has to keep asking "has this client left?"
// for as long as it is deciding whether to list it.
func TestDepartureIsRecordedDurably(t *testing.T) {
	e := &Engine{}
	mac := "fc:9c:a7:93:7f:ed"

	e.noteAssoc("wlan-usb", mac, false)

	if _, ok := e.assocGone[mac]; !ok {
		t.Fatal("a reported departure was not recorded")
	}
	// Reading the one-shot record must not erase the durable one.
	_ = e.assocTime(mac, false)
	if _, ok := e.assocGone[mac]; !ok {
		t.Fatal("the departure was consumed by reading assocTime")
	}
}

// Coming back clears it, or a client that returned would be held out by a
// departure it has already reversed.
func TestReturningClearsTheDeparture(t *testing.T) {
	e := &Engine{}
	mac := "fc:9c:a7:93:7f:ed"

	e.noteAssoc("wlan-usb", mac, false)
	e.noteAssoc("wlan0", mac, true)

	if _, ok := e.assocGone[mac]; ok {
		t.Fatal("a client that re-associated is still marked as gone")
	}
}

// The rule the tick applies: a departure NEWER than the last time the station
// was seen means the client has left, and no grace is owed. Older means it has
// been seen since, and the departure is spent.
func TestDepartureBeatsTheGraceOnlyWhenItIsNewer(t *testing.T) {
	e := &Engine{}
	mac := "fc:9c:a7:93:7f:ed"
	e.lastAssoc = map[string]int64{}

	// Seen, then told it left: the departure wins.
	e.lastAssoc[mac] = time.Now().Add(-2 * time.Second).UnixMilli()
	e.noteAssoc("wlan-usb", mac, false)
	if gone := e.assocGone[mac]; !gone.After(time.UnixMilli(e.lastAssoc[mac])) {
		t.Fatal("a departure after the last sighting did not win")
	}

	// Told it left, then seen again: the sighting wins, and the grace applies
	// as it did before.
	e.lastAssoc[mac] = time.Now().Add(time.Second).UnixMilli()
	if gone := e.assocGone[mac]; gone.After(time.UnixMilli(e.lastAssoc[mac])) {
		t.Fatal("a stale departure outranked a later sighting")
	}
}
