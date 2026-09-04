package boa

import "testing"

// mergeFixture builds the two built-ins the bug was found with: one that owns
// the rate and asserts nothing on the link lane, and one that does the reverse.
func mergeFixture(t *testing.T) (rate, link Pattern) {
	t.Helper()
	var err error
	if rate, err = LadderPattern(PatternBlackhole, DefaultLadder(), 30); err != nil {
		t.Fatalf("%s: %v", PatternBlackhole, err)
	}
	if link, err = LadderPattern(PatternDropEveryMin, DefaultLadder(), 30); err != nil {
		t.Fatalf("%s: %v", PatternDropEveryMin, err)
	}
	return rate, link
}

// TestMergeCarriesTheLinkLane pins issue #207.
//
// MergePatterns built its result from keyframes alone, so the link lane was
// dropped. Because linkPattern deliberately uses CLEAN keyframes so it can
// layer, a merged link pattern contributed nothing in either lane: the merge
// returned the other pattern unchanged and reported success.
func TestMergeCarriesTheLinkLane(t *testing.T) {
	rate, link := mergeFixture(t)
	if len(link.Links) == 0 {
		t.Fatalf("fixture is wrong: %s has no link events", PatternDropEveryMin)
	}

	for _, order := range [][]Pattern{{link, rate}, {rate, link}} {
		got, err := MergePatterns("merged", order)
		if err != nil {
			t.Fatalf("merge(%s, %s): %v", order[0].Name, order[1].Name, err)
		}
		if len(got.Links) == 0 {
			t.Errorf("merge(%s, %s) produced %d link events, want at least 1",
				order[0].Name, order[1].Name, len(got.Links))
		}
		// The rate lane must still be there: carrying links is not allowed to
		// cost the thing the merge already did.
		if len(got.Keys) < 2 {
			t.Errorf("merge(%s, %s) produced %d keyframes",
				order[0].Name, order[1].Name, len(got.Keys))
		}
	}
}

// TestMergeRepeatsAndStretchesLinkEvents checks the link lane gets the same
// treatment as the keyframes rather than merely being copied across: a shorter
// source repeats to fill the merged length, and every event stays inside it.
func TestMergeRepeatsAndStretchesLinkEvents(t *testing.T) {
	rate, link := mergeFixture(t)

	merged, err := MergePatterns("merged", []Pattern{link, rate})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	total := merged.DurSec()
	if total <= 0 {
		t.Fatalf("merged duration %v", total)
	}

	// One event per lap of the source, and none on or past the seam, which the
	// loop already replays as the start.
	laps := int(total/link.DurSec() + 0.5)
	if want := laps * len(link.Links); len(merged.Links) != want {
		t.Errorf("got %d link events over %.0fs, want %d (%d laps x %d)",
			len(merged.Links), total, want, laps, len(link.Links))
	}

	last := -1.0
	for i, ev := range merged.Links {
		if ev.AtSec < 0 || ev.AtSec >= total {
			t.Errorf("link event %d at %.2fs is outside [0, %.2fs)", i, ev.AtSec, total)
		}
		if ev.AtSec < last {
			t.Errorf("link events are not sorted: %.2fs after %.2fs", ev.AtSec, last)
		}
		last = ev.AtSec
		if ev.Kind != LinkDrop {
			t.Errorf("link event %d kind = %q, want %q", i, ev.Kind, LinkDrop)
		}
	}

	if err := validPattern(merged); err != nil {
		t.Errorf("merged pattern does not validate: %v", err)
	}
}

// TestMergeStretchesDeadzoneDuration checks a held event grows with the pattern
// around it. An outage that stays 10s while everything else is stretched is a
// different test than the one that was merged.
func TestMergeStretchesDeadzoneDuration(t *testing.T) {
	rate, err := LadderPattern(PatternBlackhole, DefaultLadder(), 30)
	if err != nil {
		t.Fatalf("blackhole: %v", err)
	}
	// A 70s source against a 60s one, so something must stretch.
	dead := Pattern{
		Name: "dz",
		Keys: []Keyframe{{AtSec: 0, Ease: EaseHold}, {AtSec: 70, Ease: EaseHold}},
		Links: []LinkEvent{
			{AtSec: 10, Kind: LinkDeadzone, DurSec: 10},
		},
		Loop: true,
	}

	merged, err := MergePatterns("merged", []Pattern{dead, rate})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged.Links) == 0 {
		t.Fatalf("no link events survived the merge")
	}
	for _, ev := range merged.Links {
		if ev.Kind != LinkDeadzone {
			continue
		}
		if ev.DurSec < 10 {
			t.Errorf("deadzone shrank to %.2fs; a stretch may only enlarge", ev.DurSec)
		}
	}
}
