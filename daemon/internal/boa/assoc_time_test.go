package boa

import (
	"testing"
	"time"
)

// TestMonitorRecordsAssociationTimes checks the two AP-STA messages are parsed
// and stored, and that nothing else on the line confuses the MAC out of them.
func TestMonitorRecordsAssociationTimes(t *testing.T) {
	e := &Engine{}
	const mac = "aa:bb:cc:dd:ee:ff"

	e.handleHostapdEvent("wlan0", "<3>AP-STA-CONNECTED "+mac)
	obs, ok := e.assocSeen[mac]
	if !ok {
		t.Fatal("AP-STA-CONNECTED was not recorded")
	}
	if !obs.connected || obs.iface != "wlan0" {
		t.Errorf("recorded %+v, want connected on wlan0", obs)
	}

	// hostapd builds differ in what follows the MAC; the first field is the one
	// that matters.
	e.handleHostapdEvent("wlan-usb", "<3>AP-STA-DISCONNECTED "+mac+" reason=3")
	obs = e.assocSeen[mac]
	if obs.connected || obs.iface != "wlan-usb" {
		t.Errorf("recorded %+v, want disconnected on wlan-usb", obs)
	}
}

// TestMonitorRaisesNoEventItself pins the design decision. hostapd speaks only
// for radios it serves, and this box can run one radio under hostapd and
// another under NetworkManager -- so if the monitor raised the log line, a
// client on the second radio would vanish from the log entirely. The tick stays
// the single thing that decides an event happened.
func TestMonitorRaisesNoEventItself(t *testing.T) {
	e := &Engine{}
	e.handleHostapdEvent("wlan0", "<3>AP-STA-CONNECTED aa:bb:cc:dd:ee:ff")
	e.handleHostapdEvent("wlan0", "<3>AP-STA-DISCONNECTED aa:bb:cc:dd:ee:ff")

	if got := len(e.events.since(0, 100)); got != 0 {
		t.Errorf("the monitor raised %d events; association events are the "+
			"tick's to raise, so a radio without a control socket is not "+
			"silently dropped from the log", got)
	}
}

// TestAssocTimeUsesTheObservation is the point of the change: an event carries
// the moment hostapd saw the transition, not the moment the poll noticed it.
func TestAssocTimeUsesTheObservation(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	seen := time.Now().Add(-800 * time.Millisecond)
	e := &Engine{assocSeen: map[string]assocObs{
		mac: {iface: "wlan0", at: seen, connected: true},
	}}

	got := e.assocTime(mac, true)
	if !got.Equal(seen) {
		t.Errorf("assocTime = %v, want the observed %v -- otherwise two clients "+
			"reassociating 800ms apart both read as 'now'", got, seen)
	}
	// Consumed, so one transition cannot stamp a second event.
	if again := e.assocTime(mac, true); again.Equal(seen) {
		t.Error("the observation was used twice; a later event would carry a " +
			"time it has no claim to")
	}
}

func TestAssocTimeFallsBackToNow(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	old := time.Now().Add(-time.Hour)

	cases := []struct {
		name string
		seen map[string]assocObs
		want bool // true if the stored time should be used
	}{
		{"nothing seen", map[string]assocObs{}, false},
		{"stale", map[string]assocObs{
			mac: {at: old, connected: true},
		}, false},
		{"wrong direction", map[string]assocObs{
			mac: {at: time.Now(), connected: false},
		}, false},
	}
	for _, c := range cases {
		e := &Engine{assocSeen: c.seen}
		got := e.assocTime(mac, true)
		if used := got.Equal(old); used != c.want {
			t.Errorf("%s: assocTime used the stored time = %v, want %v", c.name, used, c.want)
		}
		if time.Since(got) > time.Second {
			t.Errorf("%s: fell back to %v, which is not now", c.name, got)
		}
	}
}

// TestEventCarriesTheGivenTime checks the plumbing all the way to the ring.
func TestEventCarriesTheGivenTime(t *testing.T) {
	e := &Engine{}
	at := time.Now().Add(-2 * time.Second)
	e.logEventAt(at, EventJoin, "wlan0", "aa:bb:cc:dd:ee:ff", "joined")

	evs := e.events.since(0, 100)
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	if evs[0].At != at.UnixMilli() {
		t.Errorf("event At = %d, want %d", evs[0].At, at.UnixMilli())
	}
}
