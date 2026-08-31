package pifi

import (
	"path/filepath"
	"strings"
	"testing"
)

// A four-rung ladder with measured caps, deliberately out of order so the
// generator has to sort it.
func testLadder() Ladder {
	return Ladder{
		Service: "infinite-stream", Provenance: LadderMeasured, MeasuredAt: 100,
		Rungs: []Rung{
			{Mbps: 4.59, UpAtMbps: 6.41},
			{Mbps: 0.25, UpAtMbps: 0.45},
			{Mbps: 2.08, UpAtMbps: 3.34},
			{Mbps: 0.82, UpAtMbps: 1.20},
		},
	}
}

func rates(p Pattern) []float64 {
	out := make([]float64, 0, len(p.Keys))
	for _, k := range p.Keys {
		out = append(out, k.Down.RateMbps)
	}
	return out
}

func eq(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %v, want %v", i, got, want)
		}
	}
}

// A pyramid climbs every rung and comes back down, and it uses the MEASURED cap
// for each rung rather than the rung's own rate. Capping at what a rendition
// costs does not select it -- a player wants headroom -- so a pattern built
// from the rung rates would sit one rendition below its own description.
func TestPyramidWalksEveryRungUsingMeasuredCaps(t *testing.T) {
	p, err := LadderPattern(PatternPyramid, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, rates(p), []float64{0.45, 1.20, 3.34, 6.41, 3.34, 1.20, 0.45})
	if p.Name != PatternPyramid || !p.Loop {
		t.Fatalf("name %q loop %v", p.Name, p.Loop)
	}
	if got := p.DurSec(); got != 180 {
		t.Fatalf("duration %v, want 180", got)
	}
}

// A valley is the inverse: the squeeze, then the recovery.
func TestValleyDescendsThenClimbsBack(t *testing.T) {
	p, err := LadderPattern(PatternValley, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, rates(p), []float64{6.41, 3.34, 1.20, 0.45, 1.20, 3.34, 6.41})
}

// The last keyframe repeats the first, so a looping run rejoins its own start
// without a step change at the seam -- which would otherwise read as a cap
// event the operator never asked for.
func TestLadderPatternLoopsSeamlessly(t *testing.T) {
	for _, name := range BuiltinNames {
		p, err := LadderPattern(name, testLadder(), 30)
		if err != nil {
			t.Fatal(err)
		}
		r := rates(p)
		if r[0] != r[len(r)-1] {
			t.Fatalf("%s: seam %v -> %v", name, r[len(r)-1], r[0])
		}
	}
}

// Every segment holds. An interpolated cap means "somewhere between two values
// for thirty seconds", and no player reaction can be lined up against that.
func TestLadderPatternHoldsEverySegment(t *testing.T) {
	p, _ := LadderPattern(PatternValley, testLadder(), 30)
	for i, k := range p.Keys {
		if k.Ease != EaseHold {
			t.Fatalf("key %d ease %q, want %q", i, k.Ease, EaseHold)
		}
	}
}

// Without a measured cap there is nothing better than arithmetic, but it must
// still be above the rung or the pattern cannot select it.
func TestLadderPatternFallsBackToHeadroomWhenUpAtIsMissing(t *testing.T) {
	l := Ladder{Service: "typed", Rungs: []Rung{{Mbps: 1}, {Mbps: 2}}}
	p, err := LadderPattern(PatternPyramid, l, 30)
	if err != nil {
		t.Fatal(err)
	}
	r := rates(p)
	if r[0] <= 1 || r[1] <= 2 {
		t.Fatalf("caps %v do not clear their rungs", r)
	}
}

// One rung has no shape to traverse, and the pattern engine requires two
// keyframes. Refused with a reason rather than emitting a degenerate pattern.
func TestLadderPatternNeedsTwoRungs(t *testing.T) {
	l := Ladder{Service: "x", Rungs: []Rung{{Mbps: 1, UpAtMbps: 2}}}
	if _, err := LadderPattern(PatternPyramid, l, 30); err == nil {
		t.Fatal("accepted a one-rung ladder")
	}
	if _, err := LadderPattern("nonsense", testLadder(), 30); err == nil {
		t.Fatal("accepted an unknown built-in name")
	}
}

// Generated patterns must survive the validator they will be stored through.
func TestGeneratedPatternsAreValid(t *testing.T) {
	for _, name := range BuiltinNames {
		p, err := LadderPattern(name, DefaultLadder(), 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := validPattern(p); err != nil {
			t.Fatalf("%s: generated an invalid pattern: %v", name, err)
		}
	}
}

// A device holds one ladder per service and they share nothing. Naming a
// service picks it; not naming one takes the most recently measured, which is
// the one the operator was last working on.
func TestPickLadderPrefersServiceThenRecency(t *testing.T) {
	p := Policy{Ladders: []Ladder{
		{Service: "youtube", MeasuredAt: 10, Rungs: []Rung{{Mbps: 1}}},
		{Service: "avplayer", MeasuredAt: 99, Rungs: []Rung{{Mbps: 2}}},
	}}
	if l, ok := pickLadder(p, "youtube"); !ok || l.Service != "youtube" {
		t.Fatalf("by service: %v %v", l.Service, ok)
	}
	if l, ok := pickLadder(p, ""); !ok || l.Service != "avplayer" {
		t.Fatalf("by recency: %v %v", l.Service, ok)
	}
	if _, ok := pickLadder(p, "netflix"); ok {
		t.Fatal("found a service the device has no ladder for")
	}
	if _, ok := pickLadder(Policy{}, ""); ok {
		t.Fatal("found a ladder on a device with none")
	}
}

// A built-in is derived and reshapes itself when the ladder is re-swept. A
// saved pattern wearing its name would be a frozen copy under the same label,
// so "valley" would mean different things on different boxes.
func TestSavedPatternsCannotShadowABuiltin(t *testing.T) {
	s := NewPatternStore(filepath.Join(t.TempDir(), "patterns.json"))
	two := []Keyframe{kf(0, 8, EaseHold), kf(30, 2, EaseHold)}
	for _, name := range []string{"valley", "Valley", " PYRAMID "} {
		if err := s.Put(Pattern{Name: name, Keys: two}); err == nil {
			t.Fatalf("%q: accepted, want refused", name)
		}
	}
	if err := s.Put(Pattern{Name: "morning-peak", Keys: two}); err != nil {
		t.Fatalf("refused a legitimate name: %v", err)
	}
	if err := s.Put(Pattern{Name: "  ", Keys: two}); err == nil {
		t.Fatal("accepted a blank name")
	}
}

// Names are the storage key, so two spellings of one name must be one pattern
// rather than two rows that look identical in a list.
func TestSavedPatternNamesAreCaseAndSpaceInsensitive(t *testing.T) {
	s := NewPatternStore(filepath.Join(t.TempDir(), "patterns.json"))
	two := []Keyframe{kf(0, 8, EaseHold), kf(30, 2, EaseHold)}
	if err := s.Put(Pattern{Name: "Morning Peak", Keys: two}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Pattern{Name: "  morning peak  ", Keys: two, Loop: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.All(); len(got) != 1 {
		t.Fatalf("got %d patterns, want 1: %v", len(got), got)
	}
	if p, ok := s.Get("MORNING PEAK"); !ok || !p.Loop {
		t.Fatalf("lookup failed or did not overwrite: %+v %v", p, ok)
	}
}

// The library outlives a restart, or "saved" means nothing.
func TestSavedPatternsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "patterns.json")
	s := NewPatternStore(path)
	if err := s.Put(Pattern{Name: "flaky-lift",
		Keys: []Keyframe{kf(0, 8, EaseHold), kf(30, 2, EaseHold)}}); err != nil {
		t.Fatal(err)
	}
	again := NewPatternStore(path)
	if _, ok := again.Get("flaky-lift"); !ok {
		t.Fatal("pattern did not survive a reload")
	}
}

// Built-ins are generated from whichever ladder a device has, so exporting one
// would freeze a shape meant to track the content -- and restoring it onto
// another box would describe that box's ladders wrongly.
func TestConfigExportOmitsBuiltinsAndImportRefusesThem(t *testing.T) {
	two := []Keyframe{kf(0, 8, EaseHold), kf(30, 2, EaseHold)}
	doc := ExportConfig(
		map[string]Policy{"aa:aa:aa:aa:aa:aa": devPolicy("aa:aa:aa:aa:aa:aa", 1)},
		[]Pattern{{Name: "valley", Keys: two}, {Name: "morning-peak", Keys: two}},
	)
	if len(doc.Patterns) != 1 || doc.Patterns[0].Name != "morning-peak" {
		t.Fatalf("exported %v", doc.Patterns)
	}
	doc.Patterns = append(doc.Patterns, Pattern{Name: "pyramid", Keys: two})
	if err := doc.Validate(); err == nil {
		t.Fatal("import accepted a built-in name")
	}
}

// Stretching changes time and nothing else. Touching the rates would ask a
// different question and silently invalidate the ladder the pattern came from.
func TestStretchScalesTimeAndLeavesRatesAlone(t *testing.T) {
	base, _ := LadderPattern(PatternValley, testLadder(), 30)
	got, err := StretchPattern(base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.DurSec() != base.DurSec()*2 {
		t.Fatalf("duration %v, want %v", got.DurSec(), base.DurSec()*2)
	}
	eq(t, rates(got), rates(base))
	for i := range got.Keys {
		if got.Keys[i].AtSec != base.Keys[i].AtSec*2 {
			t.Fatalf("key %d at %v, want %v",
				i, got.Keys[i].AtSec, base.Keys[i].AtSec*2)
		}
	}
	if got.Name != base.Name || got.Loop != base.Loop {
		t.Fatal("stretching changed the pattern's identity")
	}
}

// Shape is preserved, not flattened. An authored timeline whose steps are
// uneven on purpose must stay uneven -- imposing one duration on every step
// would destroy the thing that was authored.
func TestStretchPreservesUnevenSpacing(t *testing.T) {
	p := Pattern{Name: "authored", Keys: []Keyframe{
		kf(0, 8, EaseHold), kf(20, 2, EaseHold), kf(25, 8, EaseHold)}}
	got, err := StretchPattern(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0, 40, 50}
	for i, w := range want {
		if got.Keys[i].AtSec != w {
			t.Fatalf("key %d at %v, want %v", i, got.Keys[i].AtSec, w)
		}
	}
}

// Every stretched keyframe must still land on the half-second grid, because
// throughput is sampled once a second and a finer transition cannot be seen.
func TestStretchStaysOnTheHalfSecondGrid(t *testing.T) {
	base, _ := LadderPattern(PatternPyramid, testLadder(), 30)
	for _, f := range []float64{0.35, 0.5, 1.1, 1.7, 3.3} {
		got, err := StretchPattern(base, f)
		if err != nil {
			t.Fatalf("%gx: %v", f, err)
		}
		if err := validPattern(got); err != nil {
			t.Fatalf("%gx produced an invalid pattern: %v", f, err)
		}
	}
}

// A stretch that collapses two steps into one instant, or runs past what the
// engine will play, is refused with a reason naming the slider -- not with the
// validator's message about grids, which says nothing about the cause.
func TestStretchRefusesWhatItCannotPlay(t *testing.T) {
	base, _ := LadderPattern(PatternValley, testLadder(), 30)
	if _, err := StretchPattern(base, 0.001); err == nil {
		t.Fatal("accepted a stretch below the minimum")
	}
	if _, err := StretchPattern(base, 100); err == nil {
		t.Fatal("accepted a stretch above the maximum")
	}
	if _, err := StretchPattern(base, 19); err != nil {
		t.Fatalf("refused a stretch that fits: %v", err)
	}
	long := Pattern{Name: "long", Keys: []Keyframe{
		kf(0, 8, EaseHold), kf(3000, 2, EaseHold)}}
	if _, err := StretchPattern(long, 2); err == nil {
		t.Fatal("accepted a stretch past the maximum pattern length")
	}
	tight := Pattern{Name: "tight", Keys: []Keyframe{
		kf(0, 8, EaseHold), kf(0.5, 2, EaseHold), kf(1, 8, EaseHold)}}
	if _, err := StretchPattern(tight, 0.2); err == nil {
		t.Fatal("accepted a stretch that collapses adjacent keyframes")
	}
}

// A stretch of 1 is the identity.
func TestStretchOfOneIsTheIdentity(t *testing.T) {
	base, _ := LadderPattern(PatternValley, testLadder(), 30)
	got, err := StretchPattern(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.DurSec() != base.DurSec() {
		t.Fatalf("1x changed the duration to %v", got.DurSec())
	}
}

// Zero is not "unset" and not a very fast pattern: it puts every keyframe at
// the same instant, which is a constant rate, which is a policy. Refused with
// that explanation rather than silently coerced to 1x -- the coercion is the
// same trap policyPatch uses pointers to avoid, and it would answer a request
// the operator did not make.
func TestStretchOfZeroIsRefusedAsAConstant(t *testing.T) {
	base, _ := LadderPattern(PatternValley, testLadder(), 30)
	_, err := StretchPattern(base, 0)
	if err == nil {
		t.Fatal("a stretch of 0 was accepted")
	}
	if !strings.Contains(err.Error(), "constant rate") {
		t.Fatalf("error does not explain why: %v", err)
	}
	if _, err := StretchPattern(base, -2); err == nil {
		t.Fatal("a negative stretch was accepted")
	}
}

// Seconds-per-step must be recoverable from a stored pattern, so a slider
// labelled in seconds shows the right value after a reload.
func TestGeneratedDwellIsRecoverableFromTheStoredPattern(t *testing.T) {
	base, _ := LadderPattern(PatternPyramid, testLadder(), 30)
	got, err := StretchPattern(base, 2.5)
	if err != nil {
		t.Fatal(err)
	}
	if dwell := got.DurSec() / float64(len(got.Keys)-1); dwell != 75 {
		t.Fatalf("recovered dwell %v, want 75", dwell)
	}
}

// Cloning a built-in needs its concrete rates, and reading them must not apply
// them: selecting a pattern onto a device to find out what it contains would
// change what that device is set to.
func TestBuiltinResolvesToTheSameTimelineTheSelectionWouldStore(t *testing.T) {
	l := testLadder()
	for _, name := range BuiltinNames {
		want, err := LadderPattern(name, l, 0)
		if err != nil {
			t.Fatal(err)
		}
		want, err = StretchPattern(want, 2)
		if err != nil {
			t.Fatal(err)
		}
		// What the endpoint composes: generate at the default dwell, then
		// stretch -- the same two steps, in the same order, as selection.
		got, err := LadderPattern(name, l, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err = StretchPattern(got, 2)
		if err != nil {
			t.Fatal(err)
		}
		eq(t, rates(got), rates(want))
		if got.DurSec() != want.DurSec() {
			t.Fatalf("%s: %v vs %v", name, got.DurSec(), want.DurSec())
		}
	}
}

// A clone is a saved pattern like any other, so it must not take a built-in's
// name -- which is exactly the case a clone runs into, since the thing being
// cloned is usually a built-in.
func TestACloneMustBeSavedUnderItsOwnName(t *testing.T) {
	s := NewPatternStore(filepath.Join(t.TempDir(), "patterns.json"))
	src, err := LadderPattern(PatternValley, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(src); err == nil {
		t.Fatal("a clone kept the built-in name and was accepted")
	}
	clone := src
	clone.Name = "valley-slow"
	if err := s.Put(clone); err != nil {
		t.Fatalf("refused a renamed clone: %v", err)
	}
	got, ok := s.Get("valley-slow")
	if !ok {
		t.Fatal("clone not stored")
	}
	// The clone is a snapshot: it keeps the rates it was taken from and does
	// NOT track the ladder afterwards, which is the point of cloning one.
	eq(t, rates(got), rates(src))
}
