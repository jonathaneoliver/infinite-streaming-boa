package boa

import "testing"

// Every name this box has ever written must still be readable, because these
// strings are on disk. A missed one is not an error anybody sees: the readers
// skip an unknown kind rather than failing, so the lane simply stops doing
// anything.
//
// Walks legacyKinds rather than repeating the pairs, so a name added to the map
// without a home is caught here rather than at the next power cut.
func TestEveryLegacyKindStillLoads(t *testing.T) {
	if len(legacyKinds) == 0 {
		t.Fatal("no legacy names are being migrated, which cannot be right")
	}
	for old, want := range legacyKinds {
		if got := normaliseKind(old); got != want {
			t.Errorf("normaliseKind(%q) = %q, want %q", old, got, want)
		}
		// And the target must be stable: normalising twice is normalising once,
		// or the store would rewrite and re-save itself on every boot.
		if got := normaliseKind(want); got != want {
			t.Errorf("normaliseKind(%q) = %q; current names must be fixed points",
				want, got)
		}
	}
	// The names actually in use today, spelled out rather than derived, so a
	// constant renamed by accident fails here.
	for _, k := range []string{
		LinkDeauth, LinkDisassoc, LinkDeadzone,
		RadioOff, RadioAPDown, RadioGather, RadioEvict, RadioDeauth, RadioScan,
	} {
		if got := normaliseKind(k); got != k {
			t.Errorf("current kind %q was rewritten to %q", k, got)
		}
	}
}

// An unknown kind passes through. This is a rename, not a validator: refusing
// here would turn a pattern written by a newer build into a hard error rather
// than something a reader declines on its own terms.
func TestAnUnknownKindIsLeftAlone(t *testing.T) {
	if got := normaliseKind("something-from-the-future"); got != "something-from-the-future" {
		t.Errorf("an unknown kind was rewritten to %q", got)
	}
}

// A pattern saved before #229 must come back with current names on BOTH lanes,
// and must report that it changed so the store saves the upgrade rather than
// redoing it every boot.
func TestAPatternSavedUnderTheOldNamesIsUpgraded(t *testing.T) {
	old := Pattern{
		Name:  "saved-yesterday",
		Links: []LinkEvent{{AtSec: 5, Kind: "drop"}, {AtSec: 9, Kind: "nudge"}, {AtSec: 12, Kind: "deadzone", DurSec: 20}},
		Radios: []RadioEvent{
			{AtSec: 2, Iface: "wlan0", Kind: "off", DurSec: 30},
			{AtSec: 40, Iface: "wlan0", Kind: "apdown", DurSec: 15},
			{AtSec: 60, Iface: "wlan-usb", Kind: "gather"},
		},
	}
	got, changed := NormalisePattern(old)
	if !changed {
		t.Fatal("a pattern full of legacy names reported no change")
	}
	wantLinks := []string{LinkDeauth, LinkDisassoc, LinkDeadzone}
	for i, w := range wantLinks {
		if got.Links[i].Kind != w {
			t.Errorf("link %d is %q, want %q", i, got.Links[i].Kind, w)
		}
	}
	wantRadios := []string{RadioOff, RadioAPDown, RadioGather}
	for i, w := range wantRadios {
		if got.Radios[i].Kind != w {
			t.Errorf("radio %d is %q, want %q", i, got.Radios[i].Kind, w)
		}
	}
	// Everything else survives untouched -- a rename must not quietly reshape
	// the timeline it is renaming.
	if got.Radios[0].DurSec != 30 || got.Links[2].DurSec != 20 {
		t.Error("durations were altered by a rename")
	}
	if got.Radios[2].Iface != "wlan-usb" || got.Name != "saved-yesterday" {
		t.Error("a rename changed something that was not a kind")
	}
	// The ORIGINAL must not have been mutated: the store hands out patterns by
	// value and a caller holding one should not see it change underneath.
	if old.Links[0].Kind != "drop" || old.Radios[0].Kind != "off" {
		t.Error("NormalisePattern mutated its input")
	}
	// Idempotent.
	again, changedAgain := NormalisePattern(got)
	if changedAgain {
		t.Error("normalising an already-current pattern reported a change")
	}
	if again.Links[0].Kind != LinkDeauth {
		t.Error("a second pass altered a current name")
	}
}

// A pattern that is already current must report no change, or the store saves
// itself on every single load.
func TestACurrentPatternIsNotRewritten(t *testing.T) {
	p := Pattern{
		Links:  []LinkEvent{{AtSec: 1, Kind: LinkDeauth}},
		Radios: []RadioEvent{{AtSec: 2, Iface: "wlan0", Kind: RadioScan}},
	}
	if _, changed := NormalisePattern(p); changed {
		t.Error("a current pattern was reported as changed")
	}
	if _, changed := NormalisePattern(Pattern{}); changed {
		t.Error("an empty pattern was reported as changed")
	}
}
