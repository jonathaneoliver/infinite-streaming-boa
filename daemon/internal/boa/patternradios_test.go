package boa

import (
	"strings"
	"testing"
)

func radioPattern(ifaces ...string) Pattern {
	var evs []RadioEvent
	for i, w := range ifaces {
		evs = append(evs, RadioEvent{AtSec: float64(10 * (i + 1)), Iface: w, Kind: RadioGather})
	}
	return Pattern{Name: adapterPatternName, Radios: evs}
}

func ifacesOf(p Pattern) []string {
	var out []string
	for _, ev := range p.Radios {
		out = append(out, ev.Iface)
	}
	return out
}

// Rule 1: a dongle moved to another USB socket comes back under a new name, and
// the timeline drawn for it should follow rather than be stranded.
func TestASwappedRadioInheritsTheOldOnesEvents(t *testing.T) {
	p := radioPattern("wlan-usb", "wlan-usb2", "wlan-usb2")
	got, notes, changed := reconcileAdapterPattern(p, []string{"wlan-usb", "wlan-usb3"})
	if !changed {
		t.Fatal("wlan-usb2 is gone and wlan-usb3 is new, but nothing was remapped")
	}
	for i, w := range ifacesOf(got) {
		want := map[int]string{0: "wlan-usb", 1: "wlan-usb3", 2: "wlan-usb3"}[i]
		if w != want {
			t.Errorf("event %d on %s, want %s", i, w, want)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "wlan-usb2") ||
		!strings.Contains(notes[0], "wlan-usb3") {
		t.Errorf("the rename must say what moved where, got %q", notes)
	}
	// And it must say HOW MANY events moved, or the line is unfalsifiable.
	if !strings.Contains(notes[0], "2 event(s)") {
		t.Errorf("the rename should count the events it moved: %q", notes[0])
	}
}

// Rule 2: nothing to pair with, so the events stay exactly as they are and the
// editor is told to grey the lane. Silently dropping them would delete an
// operator's work on a hotplug; silently keeping them invisible is the bug this
// whole file exists for.
func TestALostRadioWithNoReplacementKeepsItsEvents(t *testing.T) {
	p := radioPattern("wlan-usb", "wlan-usb2")
	got, _, changed := reconcileAdapterPattern(p, []string{"wlan-usb"})
	if changed {
		t.Error("there was nothing to remap onto, so the pattern should be untouched")
	}
	if n := len(got.Radios); n != 2 {
		t.Fatalf("events were dropped: %d left, want 2", n)
	}
	orph := OrphanedRadios(got, []string{"wlan-usb"})
	if len(orph) != 1 || orph[0] != "wlan-usb2" {
		t.Errorf("wlan-usb2 should be reported as orphaned, got %v", orph)
	}
}

// Rule 3: a radio that arrives with nothing to inherit is simply a new empty
// lane, which needs no rewriting at all.
func TestANewRadioAloneChangesNothing(t *testing.T) {
	p := radioPattern("wlan-usb", "wlan-usb2")
	got, notes, changed := reconcileAdapterPattern(p, []string{"wlan-usb", "wlan-usb2", "wlan-usb3"})
	if changed || len(notes) != 0 {
		t.Errorf("gaining a radio should rewrite nothing, got %v", notes)
	}
	if len(OrphanedRadios(got, []string{"wlan-usb", "wlan-usb2", "wlan-usb3"})) != 0 {
		t.Error("nothing is orphaned when every named radio is present")
	}
}

// Two gone and one arrived: the pairing is by sorted order so the same swap
// always maps the same way, and the loser keeps its events rather than being
// merged into the winner.
func TestPairingIsDeterministicAndPartial(t *testing.T) {
	p := radioPattern("wlan-a", "wlan-b", "wlan-keep")
	have := []string{"wlan-keep", "wlan-new"}
	got, notes, changed := reconcileAdapterPattern(p, have)
	if !changed || len(notes) != 1 {
		t.Fatalf("expected exactly one rename, got %v", notes)
	}
	// wlan-a sorts before wlan-b, so it takes the single spare.
	if ifacesOf(got)[0] != "wlan-new" {
		t.Errorf("wlan-a should have taken the spare, got %s", ifacesOf(got)[0])
	}
	if ifacesOf(got)[1] != "wlan-b" {
		t.Errorf("wlan-b had nothing to move to and should be untouched, got %s",
			ifacesOf(got)[1])
	}
	if orph := OrphanedRadios(got, have); len(orph) != 1 || orph[0] != "wlan-b" {
		t.Errorf("wlan-b should be the orphan, got %v", orph)
	}
	// Repeating it must be stable: the same input maps the same way, and a
	// second pass finds nothing left to do for the radio it already moved.
	again, _, _ := reconcileAdapterPattern(got, have)
	if ifacesOf(again)[0] != "wlan-new" {
		t.Error("reconciling twice moved the events again")
	}
}

// A pattern with no radio events, or a box with no radios, must not be touched:
// there is nothing to reconcile and rewriting it would be inventing intent.
func TestNothingToReconcileIsLeftAlone(t *testing.T) {
	if _, _, changed := reconcileAdapterPattern(Pattern{}, []string{"wlan0"}); changed {
		t.Error("an empty pattern was rewritten")
	}
	p := radioPattern("wlan-usb")
	if _, _, changed := reconcileAdapterPattern(p, nil); changed {
		t.Error("a box with no radios rewrote the pattern")
	}
}
