package boa

import (
	"strings"
	"testing"
)

// A pattern of the given length whose only content is its radio lane, shaped
// the way AdapterPatternPanel saves one: two clean keyframes bracketing the
// timeline so the validator sees a pattern, and the events themselves.
func adapterPat(name string, dur float64, evs ...RadioEvent) Pattern {
	return Pattern{
		Name: name,
		Keys: []Keyframe{
			{AtSec: 0, Ease: EaseHold},
			{AtSec: dur, Ease: EaseHold},
		},
		Radios: evs,
	}
}

// The bug this file exists for: a merge returned Keys and Links and dropped
// every radio event, then handed the result back as a merge. Silent, and it
// looked like it worked -- the same fault as #207 one lane over. See #230.
func TestAMergeKeepsTheRadioLane(t *testing.T) {
	a := adapterPat("gather-a", 60,
		RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioGather},
		RadioEvent{AtSec: 40, Iface: "wlan-usb2", Kind: RadioGather},
	)
	b := adapterPat("scan-b", 60,
		RadioEvent{AtSec: 20, Iface: "wlan-usb", Kind: RadioScan},
	)
	got, err := MergePatterns("merged", []Pattern{a, b})
	if err != nil {
		t.Fatalf("MergePatterns: %v", err)
	}
	if len(got.Radios) != 3 {
		t.Fatalf("merged pattern has %d radio events, want 3 (2 from a, 1 from b)",
			len(got.Radios))
	}
	kinds := map[string]int{}
	for _, ev := range got.Radios {
		kinds[ev.Kind]++
	}
	if kinds[RadioGather] != 2 || kinds[RadioScan] != 1 {
		t.Errorf("wrong events survived: %v", kinds)
	}
	// Sorted by time, because a lane the player walks out of order is a lane
	// that fires out of order.
	for i := 1; i < len(got.Radios); i++ {
		if got.Radios[i].AtSec < got.Radios[i-1].AtSec {
			t.Errorf("radio events are not in time order: %v", got.Radios)
			break
		}
	}
}

// Same repeat-to-fill as the link lane: a merged pattern loops, so an event
// that fired only in the first of several cycles would be invisible for the
// rest of the run.
func TestARadioEventRepeatsWithItsSource(t *testing.T) {
	short := adapterPat("short", 30, RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioDeauth})
	long := adapterPat("long", 60, RadioEvent{AtSec: 5, Iface: "wlan-usb2", Kind: RadioDeauth})
	got, err := MergePatterns("merged", []Pattern{short, long})
	if err != nil {
		t.Fatalf("MergePatterns: %v", err)
	}
	var onUSB, onUSB2 int
	for _, ev := range got.Radios {
		switch ev.Iface {
		case "wlan-usb":
			onUSB++
		case "wlan-usb2":
			onUSB2++
		}
	}
	// The 30s source runs twice inside a 60s merge, so its event appears twice.
	if onUSB != 2 {
		t.Errorf("the 30s source's event appears %d time(s) in a 60s merge, want 2", onUSB)
	}
	if onUSB2 != 1 {
		t.Errorf("the 60s source's event appears %d time(s), want 1", onUSB2)
	}
}

// A block's duration stretches with the pattern around it, in the same
// direction and for the same reason a deadzone's does.
func TestABlockStretchesWithTheMerge(t *testing.T) {
	a := adapterPat("blocky", 55,
		RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioAPDown, DurSec: 10})
	b := adapterPat("plain", 60,
		RadioEvent{AtSec: 30, Iface: "wlan-usb2", Kind: RadioScan})
	got, err := MergePatterns("merged", []Pattern{a, b})
	if err != nil {
		t.Fatalf("MergePatterns: %v", err)
	}
	for _, ev := range got.Radios {
		if ev.Kind != RadioAPDown {
			continue
		}
		if ev.DurSec < 10 {
			t.Errorf("the block shrank to %gs; enlarging is the safe direction "+
				"because these effects have minimum durations, not maximum ones",
				ev.DurSec)
		}
		return
	}
	t.Fatal("the block did not survive the merge at all")
}

// Two blocks over one radio at one moment is not a busier timeline, it is an
// incoherent one: each owns a restore timer, so the first to expire brings the
// access point back while the second still believes it is holding it down.
func TestOverlappingBlocksOnOneRadioAreRefused(t *testing.T) {
	a := adapterPat("first", 60,
		RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioAPDown, DurSec: 20})
	b := adapterPat("second", 60,
		RadioEvent{AtSec: 15, Iface: "wlan-usb", Kind: RadioAPDown, DurSec: 20})
	_, err := MergePatterns("merged", []Pattern{a, b})
	if err == nil {
		t.Fatal("two overlapping blocks on one radio were merged without complaint")
	}
	// The message has to name the radio and both blocks, or an operator cannot
	// tell which two of their patterns are the problem.
	for _, want := range []string{"wlan-usb", RadioAPDown} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should mention %q: %s", want, err)
		}
	}
}

// The same two blocks on DIFFERENT radios are fine -- that is a box doing two
// things at once, which is the point of having several radios.
func TestOverlappingBlocksOnDifferentRadiosAreFine(t *testing.T) {
	a := adapterPat("first", 60,
		RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioAPDown, DurSec: 20})
	b := adapterPat("second", 60,
		RadioEvent{AtSec: 15, Iface: "wlan-usb2", Kind: RadioAPDown, DurSec: 20})
	if _, err := MergePatterns("merged", []Pattern{a, b}); err != nil {
		t.Errorf("blocks on two different radios should merge: %v", err)
	}
}

// Pulses do not hold the radio, so two at the same instant are two things
// happening at once rather than two things disagreeing.
func TestPulsesAtTheSameMomentAreNotAConflict(t *testing.T) {
	a := adapterPat("g", 60, RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioGather})
	b := adapterPat("d", 60, RadioEvent{AtSec: 10, Iface: "wlan-usb", Kind: RadioDeauth})
	got, err := MergePatterns("merged", []Pattern{a, b})
	if err != nil {
		t.Fatalf("two pulses at one instant should merge: %v", err)
	}
	if len(got.Radios) != 2 {
		t.Errorf("both pulses should survive, got %d", len(got.Radios))
	}
}

// A merge of two patterns that have no radio lane must not grow one, and must
// still carry the keyframes and links it always did.
func TestAMergeWithoutRadioLanesIsUnchanged(t *testing.T) {
	a := Pattern{Name: "a", Keys: []Keyframe{{AtSec: 0}, {AtSec: 30}},
		Links: []LinkEvent{{AtSec: 5, Kind: LinkDeauth}}}
	b := Pattern{Name: "b", Keys: []Keyframe{{AtSec: 0}, {AtSec: 30}}}
	got, err := MergePatterns("merged", []Pattern{a, b})
	if err != nil {
		t.Fatalf("MergePatterns: %v", err)
	}
	if len(got.Radios) != 0 {
		t.Errorf("a merge invented %d radio event(s)", len(got.Radios))
	}
	if len(got.Links) == 0 {
		t.Error("the link lane was lost, which is the bug #207 already fixed")
	}
}
