package boa

import (
	"strings"
	"testing"
	"time"
)

/*
 * A WATCHED RADIO THAT STOPS SERVING MUST SAY SO, AND KEEP SAYING SO (#164).
 *
 * hostapd stays running when an ENABLE fails, so systemd reads "active" and
 * nothing restarts it. Measured 2026-09-04: the 5GHz access point down for
 * minutes, three clients gone, and every health signal the box had reading
 * normal. noteAPServing logs the edge, but a log line scrolls away; these pin
 * the standing note -- that it waits out the deliberate transients, stays
 * quiet when the outage was chosen, and ends when the radio recovers.
 */

func TestAnAPDownPastTheGraceIsReportedUntilItRecovers(t *testing.T) {
	e := &Engine{}
	t0 := time.Unix(1_800_000_000, 0)

	// Inside the grace -- a scan, a channel move -- nothing is reported.
	if d := e.noteAPDown("wlan-usb", true, false, t0); d != 0 {
		t.Fatalf("reported a %s outage the moment it began", d)
	}
	if d := e.noteAPDown("wlan-usb", true, false, t0.Add(apDownGrace-time.Second)); d != 0 {
		t.Fatalf("reported %s, inside the %s grace", d, apDownGrace)
	}

	// Past it: reported on every rebuild, timed from when it went down.
	for i := 0; i < 3; i++ {
		at := t0.Add(apDownGrace + time.Duration(i)*time.Minute)
		if d := e.noteAPDown("wlan-usb", true, false, at); d != at.Sub(t0) {
			t.Fatalf("rebuild %d: down for %s, want %s", i, d, at.Sub(t0))
		}
	}

	// Recovery ends it, and a later outage is timed afresh.
	later := t0.Add(5 * time.Minute)
	if d := e.noteAPDown("wlan-usb", false, false, later); d != 0 {
		t.Errorf("still reported down after it recovered: %s", d)
	}
	if d := e.noteAPDown("wlan-usb", true, false, later.Add(time.Second)); d != 0 {
		t.Errorf("a fresh outage inherited the old clock: %s", d)
	}

	// The activity log is noteAPServing's job; this must not add to it.
	if evs := e.Events(0, 0); len(evs) != 0 {
		t.Errorf("noteAPDown logged %d event(s); the edges are already logged: %+v",
			len(evs), evs)
	}
}

// Switched off, or taken down by the operator or a timed outage: the same
// "no BSS" from hostapd, and not a fault. And when the intent ends with the
// access point still down, the clock starts THEN -- not from the original
// deliberate outage, which would report a long outage the moment it was
// released.
func TestADeliberateOutageIsNotReported(t *testing.T) {
	e := &Engine{}
	t0 := time.Unix(1_800_000_000, 0)

	for _, at := range []time.Duration{0, time.Minute, 10 * time.Minute} {
		if d := e.noteAPDown("wlan-usb", true, true, t0.Add(at)); d != 0 {
			t.Fatalf("a deliberate outage was reported as %s", d)
		}
	}

	released := t0.Add(10 * time.Minute)
	if d := e.noteAPDown("wlan-usb", true, false, released); d != 0 {
		t.Errorf("reported %s the moment the deliberate outage ended", d)
	}
	if d := e.noteAPDown("wlan-usb", true, false, released.Add(apDownGrace)); d != apDownGrace {
		t.Errorf("timed %s from the release, want %s", d, apDownGrace)
	}
}

func TestAWatchedRadioDownPastTheGraceRaisesAnError(t *testing.T) {
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: false,
			AP: &APStatus{Enabled: false}},
	}}

	if notes := bridgeNotes(bi, testCfg, nil); len(notes) != 0 {
		t.Fatalf("inside the grace (nothing in apDownFor) still noted: %+v", notes)
	}

	notes := bridgeNotes(bi, testCfg, map[string]time.Duration{"wlan-usb": 4 * time.Minute})
	if len(notes) != 1 {
		t.Fatalf("want one note, got %d: %+v", len(notes), notes)
	}
	if notes[0].Level != "error" {
		t.Errorf("a dead radio is an error, not %q", notes[0].Level)
	}
	for _, want := range []string{"wlan-usb", "4m0s", "disabled", "cannot associate"} {
		if !strings.Contains(notes[0].Text, want) {
			t.Errorf("note does not mention %q: %s", want, notes[0].Text)
		}
	}
}

// hostapd ENABLED over a downed interface is the other way to be off the air,
// and needs a different remedy -- the note must say which one it is.
func TestTheNoteSaysWhenTheInterfaceIsWhatWentDown(t *testing.T) {
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: false,
			AP: &APStatus{Enabled: false, LinkDown: true}},
	}}
	notes := bridgeNotes(bi, testCfg, map[string]time.Duration{"wlan-usb": 2 * time.Minute})
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "interface is down") {
		t.Fatalf("want a note naming the downed interface, got %+v", notes)
	}
}
