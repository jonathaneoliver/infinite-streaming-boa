package pifi

import (
	"math"
	"path/filepath"
	"sort"
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
	// 6.89 rather than the top rung's measured 6.41: see topRungHeadroom.
	eq(t, rates(p), []float64{0.45, 1.20, 3.34, 6.89, 3.34, 1.20, 0.45})
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
	eq(t, rates(p), []float64{6.89, 3.34, 1.20, 0.45, 1.20, 3.34, 6.89})
}

// The top rung does not get the cap the sweep recorded for it.
//
// Its up_at is not a measurement of the same kind as the others: nothing was
// ever observed climbing INTO the top, because the sweep starts there and
// descends, so that figure is bounded by where the sweep began. Here it is
// 1.40x the variant's cost, under the 1.5x floor a player was ever seen to
// need, which would make the top of every pattern a marginal state rather than
// the unconstrained baseline it is supposed to be.
func TestTopRungGetsHeadroomOverItsMeasuredCap(t *testing.T) {
	l := testLadder()
	p, err := LadderPattern(PatternValley, l, 30)
	if err != nil {
		t.Fatal(err)
	}
	top := rates(p)[0]
	if want := round2(4.59 * topRungHeadroom); top != want {
		t.Errorf("top cap %v, want %v (1.5x the 4.59 top variant)", top, want)
	}
	if top <= 6.41 {
		t.Errorf("top cap %v is not above the measured 6.41 it replaces", top)
	}
	// Every rung BELOW the top keeps its measurement: those up_at figures were
	// each observed by a player actually climbing into that rendition, and
	// widening them would over-select the rung above.
	for i, want := range []float64{3.34, 1.20, 0.45} {
		if got := rates(p)[i+1]; got != want {
			t.Errorf("rung %d cap %v, want the measured %v", i+1, got, want)
		}
	}
}

// ramp_down walks the ladder once, top to bottom, and ramp_up is its mirror.
// Neither returns along itself: the seam is a real jump, which is what a
// repeated ramp is.
func TestRampsWalkTheLadderOnce(t *testing.T) {
	down, err := LadderPattern(PatternRampDown, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, rates(down), []float64{6.89, 3.34, 1.20, 0.45, 6.89})

	up, err := LadderPattern(PatternRampUp, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, rates(up), []float64{0.45, 1.20, 3.34, 6.89, 0.45})
}

// A square wave is the extremes and nothing else -- no intermediate rung to
// land on, so a player either crosses the whole ladder or thrashes.
func TestSquareWaveUsesOnlyTheExtremes(t *testing.T) {
	p, err := LadderPattern(PatternSquareWave, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, rates(p), []float64{0.45, 6.89, 0.45})
	if got := p.DurSec(); got != 60 {
		t.Fatalf("duration %v, want 60", got)
	}
}

// transient_shock returns to the top between dips, so each dip starts from a
// refilled buffer and is an independent probe. The dips deepen.
func TestTransientShockRecoversBetweenDeepeningDips(t *testing.T) {
	p, err := LadderPattern(PatternTransientShock, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	top := round2(4.59 * topRungHeadroom)
	eq(t, rates(p), []float64{top, 3.34, top, 1.20, top, 0.45, top})

	// Every other keyframe is the top: that alternation IS the pattern, and
	// without it the dips would compound instead of each being measured from
	// the same starting condition.
	r := rates(p)
	for i := 0; i < len(r); i += 2 {
		if r[i] != top {
			t.Fatalf("keyframe %d is %v, want the top cap %v", i, r[i], top)
		}
	}
	// And the dips descend.
	for i := 1; i+2 < len(r); i += 2 {
		if r[i] <= r[i+2] {
			t.Fatalf("dip %v at %d is not deeper than the previous", r[i+2], i+2)
		}
	}
}

// blackhole is a minute with the last ten seconds at total loss.
func TestBlackholeIsAMinuteWithTenSecondsDark(t *testing.T) {
	p, err := LadderPattern(PatternBlackhole, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.DurSec(); got != 60 {
		t.Fatalf("duration %v, want 60", got)
	}
	if len(p.Keys) != 3 {
		t.Fatalf("got %d keyframes, want 3", len(p.Keys))
	}
	for i, want := range []struct {
		at   float64
		loss float64
	}{{0, 0}, {50, 100}, {60, 0}} {
		k := p.Keys[i]
		if k.AtSec != want.at || k.Down.LossPct != want.loss {
			t.Errorf("key %d: at %v loss %v, want at %v loss %v",
				i, k.AtSec, k.Down.LossPct, want.at, want.loss)
		}
	}
	// The dark stretch is the LAST ten seconds, not the first.
	if p.Keys[1].AtSec != p.DurSec()-10 {
		t.Errorf("outage starts at %v, want %v", p.Keys[1].AtSec, p.DurSec()-10)
	}
}

// blackhole touches the loss axis and NOTHING else.
//
// This is what lets it be layered over a pattern that drives the rate: zero on
// a Shape means "no conditioning of this kind", so every field it leaves alone
// is a field another pattern can own. Asserting a rate here -- even the
// ladder's top -- would collide with every other pattern in the library, since
// they all drive one.
func TestBlackholeSetsLossAndNothingElse(t *testing.T) {
	p, err := LadderPattern(PatternBlackhole, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range p.Keys {
		for _, f := range []struct {
			name string
			v    float64
		}{
			{"rate_mbps", k.Down.RateMbps},
			{"delay_ms", k.Down.DelayMs},
			{"jitter_ms", k.Down.JitterMs},
			{"reorder_pct", k.Down.ReorderPct},
			{"corrupt_pct", k.Down.CorruptPct},
		} {
			if f.v != 0 {
				t.Errorf("key %d sets %s=%v; blackhole must leave every axis but loss at zero",
					i, f.name, f.v)
			}
		}
		if k.Up != (Shape{}) {
			t.Errorf("key %d sets an uplink shape %+v; blackhole is downlink-only", i, k.Up)
		}
	}
}

// It needs nothing from the ladder, so it does not care how many rungs there
// are -- including fewer than the two a rung walk requires.
func TestBlackholeNeedsNoLadder(t *testing.T) {
	one := Ladder{Service: "x", Rungs: []Rung{{Mbps: 4.59, UpAtMbps: 6.41}}}
	if _, err := LadderPattern(PatternBlackhole, one, 30); err == nil {
		t.Log("builds from a single rung, which is fine: it reads none of them")
	}
}

// The dwell applies to rung walks and not to blackhole, whose cycle is fixed
// because the question it asks has nothing to do with the ladder.
func TestBlackholeIgnoresTheRungDwell(t *testing.T) {
	for _, dwell := range []float64{5, 30, 120} {
		p, err := LadderPattern(PatternBlackhole, testLadder(), dwell)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.DurSec(); got != 60 {
			t.Errorf("dwell %v gave duration %v, want 60", dwell, got)
		}
	}
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

// The merge this feature exists for: a ladder walk that periodically goes dark.
// transient_shock owns the rate, blackhole owns the loss, and neither was
// written knowing about the other.
func TestMergeLaysBlackholeOverTransientShock(t *testing.T) {
	shock, err := LadderPattern(PatternTransientShock, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	dark, err := LadderPattern(PatternBlackhole, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	m, err := MergePatterns("shock+dark", []Pattern{shock, dark})
	if err != nil {
		t.Fatal(err)
	}
	// 60 divides 180 exactly, so nothing is stretched and the merge is as long
	// as the longer source.
	if got, want := m.DurSec(), shock.DurSec(); got != want {
		t.Fatalf("duration %v, want %v (blackhole fits exactly, nothing stretches)", got, want)
	}
	// The rate comes from the shock at every instant...
	for _, k := range m.Keys {
		if k.Down.RateMbps == 0 {
			t.Fatalf("keyframe at %vs has no rate; the shock should own that axis", k.AtSec)
		}
	}
	// ...and the loss from blackhole, dark for the last 10s of each minute.
	darkAt := map[float64]bool{50: true, 110: true, 170: true}
	for _, k := range m.Keys {
		if darkAt[k.AtSec] && k.Down.LossPct != 100 {
			t.Errorf("at %vs loss is %v, want 100", k.AtSec, k.Down.LossPct)
		}
	}
	var sawDark bool
	for _, k := range m.Keys {
		if k.Down.LossPct == 100 {
			sawDark = true
			if k.Down.RateMbps == 0 {
				t.Errorf("at %vs the link is dark but has no cap; both axes should be set", k.AtSec)
			}
		}
	}
	if !sawDark {
		t.Error("no keyframe carries the blackhole's loss")
	}
}

// Where two sources drive the same axis, the FIRST selected wins -- not the
// lowest, and not an error. Selection order is a choice the operator made and
// can see.
func TestMergeGivesACollidingFieldToTheFirstSelected(t *testing.T) {
	a, err := LadderPattern(PatternValley, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LadderPattern(PatternRampUp, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	// Both drive the rate at every instant, so the second contributes nothing.
	m, err := MergePatterns("v+r", []Pattern{a, b})
	if err != nil {
		t.Fatalf("a collision must merge, not fail: %v", err)
	}
	// Valley starts at the top; ramp_up starts at the bottom. First wins.
	if got, want := m.Keys[0].Down.RateMbps, a.Keys[0].Down.RateMbps; got != want {
		t.Errorf("first keyframe rate %v, want valley's %v", got, want)
	}
	rev, err := MergePatterns("r+v", []Pattern{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rev.Keys[0].Down.RateMbps, b.Keys[0].Down.RateMbps; got != want {
		t.Errorf("reversed, first keyframe rate %v, want ramp_up's %v", got, want)
	}
	if rev.Keys[0].Down.RateMbps == m.Keys[0].Down.RateMbps {
		t.Error("selection order made no difference; first-wins is not being applied")
	}
}

// Stretching may only ENLARGE. Effects have minimum durations -- an outage has
// to outlast a segment fetch, a rung has to be held long enough to provoke a
// switch -- and shrinking one silently breaks what the pattern is for.
func TestMergeNeverShortensASource(t *testing.T) {
	for _, durs := range [][]float64{
		{660, 60}, {660, 360}, {660, 360, 60}, {360, 60}, {180, 50}, {97, 31},
	} {
		total, reps := mergeLength(durs)
		for i, d := range durs {
			f := total / (float64(reps[i]) * d)
			if f < 1-1e-9 {
				t.Errorf("durs %v: source %v stretched by %.4f, which SHRINKS it", durs, d, f)
			}
		}
		if total < durs[0] {
			for _, d := range durs {
				if total < d {
					t.Errorf("durs %v: merged length %v is shorter than source %v", durs, total, d)
				}
			}
		}
	}
}

// The merged length is a whole multiple of at least one source's period, so no
// cycle is cut off mid-shape -- which is how a blackhole ends up in a pattern
// that never goes dark.
func TestMergeLengthCutsNoCycleShort(t *testing.T) {
	cases := [][]float64{{660, 60}, {660, 360}, {660, 360, 60}, {97, 31}}
	for _, durs := range cases {
		total, reps := mergeLength(durs)
		whole := false
		for i, d := range durs {
			if math.Abs(total-float64(reps[i])*d*(total/(float64(reps[i])*d))) < 1e-6 &&
				math.Abs(total/d-math.Round(total/d)) < 1e-6 {
				whole = true
			}
		}
		if !whole {
			t.Errorf("durs %v: merged length %v is a whole multiple of no source period", durs, total)
		}
	}
}

// The worst stretch across the built-in library, stated so a regression in the
// search shows up as a number rather than as a pattern that feels wrong.
func TestMergeWorstStretchAcrossTheLibrary(t *testing.T) {
	lib := map[string]float64{
		"valley": 660, "pyramid": 660, "transient_shock": 660,
		"ramp_down": 360, "ramp_up": 360, "square_wave": 60, "blackhole": 60,
	}
	worst, worstPair := 1.0, ""
	names := make([]string, 0, len(lib))
	for n := range lib {
		names = append(names, n)
	}
	sort.Strings(names)
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			durs := []float64{lib[names[i]], lib[names[j]]}
			total, reps := mergeLength(durs)
			for k, d := range durs {
				if f := total / (float64(reps[k]) * d); f > worst {
					worst, worstPair = f, names[i]+"+"+names[j]
				}
			}
		}
	}
	if worst > 1.10 {
		t.Errorf("worst stretch across the library is %.3f on %s, want <= 1.10", worst, worstPair)
	}
	t.Logf("worst stretch %.3f on %s", worst, worstPair)
}

// A merge loops, so its last keyframe must repeat its first, exactly as every
// generated pattern does.
func TestMergeLoopsSeamlessly(t *testing.T) {
	shock, _ := LadderPattern(PatternTransientShock, testLadder(), 30)
	dark, _ := LadderPattern(PatternBlackhole, testLadder(), 30)
	m, err := MergePatterns("m", []Pattern{shock, dark})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Loop {
		t.Error("a merge should loop")
	}
	first, last := m.Keys[0], m.Keys[len(m.Keys)-1]
	if first.Down != last.Down || first.Up != last.Up {
		t.Errorf("seam: first %+v, last %+v", first.Down, last.Down)
	}
}

func TestMergeNeedsTwoPatterns(t *testing.T) {
	one, _ := LadderPattern(PatternValley, testLadder(), 30)
	if _, err := MergePatterns("x", []Pattern{one}); err == nil {
		t.Error("merging one pattern should fail")
	}
}

// 660 and 360 merge at 720s: the shorter runs twice untouched and the LONGER
// takes the stretch.
//
// The alternative at 660s would have run the 360 once at 1.833x -- the same
// distortion, applied to the pattern that did not need it. And the alternative
// at 2640s reduces the stretch to 1.048 by running for 44 minutes, which is the
// trade maxMergeRun exists to refuse: length is a cost the operator feels, a
// rung held 32.7s instead of 30 is not.
func TestMergeLengthPrefersAShortRunOverASmallStretch(t *testing.T) {
	total, reps := mergeLength([]float64{660, 360})
	if total != 720 {
		t.Fatalf("merged length %v, want 720", total)
	}
	if reps[1] != 2 {
		t.Errorf("the 360 runs %d time(s), want 2", reps[1])
	}
	if f := total / (float64(reps[1]) * 360); f != 1 {
		t.Errorf("the 360 was stretched %v, want 1 (the shorter should be untouched)", f)
	}
	if f := total / (float64(reps[0]) * 660); math.Abs(f-720.0/660.0) > 1e-9 {
		t.Errorf("the 660 was stretched %v, want %v", f, 720.0/660.0)
	}
}

// An overlay owns its axis and asserts NO RATE, which is the whole reason a
// merge has anything to lay over a ladder walk.
func TestImpairmentOverlaysAssertNoRate(t *testing.T) {
	for _, name := range []string{
		PatternDelayClimb, PatternLossClimb, PatternReorderClimb, PatternCorruptClimb,
	} {
		p, err := LadderPattern(name, testLadder(), 30)
		if err != nil {
			t.Fatal(err)
		}
		var touched bool
		for i, k := range p.Keys {
			if k.Down.RateMbps != 0 {
				t.Errorf("%s key %d sets rate %v; an overlay must leave the cap alone",
					name, i, k.Down.RateMbps)
			}
			if k.Down != (Shape{}) {
				touched = true
			}
		}
		if !touched {
			t.Errorf("%s sets nothing at all", name)
		}
		// Out and back, so the way down is measured too, and the seam repeats.
		if p.Keys[0].Down != p.Keys[len(p.Keys)-1].Down {
			t.Errorf("%s does not return to its start", name)
		}
		if !p.Loop {
			t.Errorf("%s should loop", name)
		}
	}
}

// Each overlay touches only the axis it is named for, so two of them can be
// merged as readily as one can be merged with a ladder walk. Delay carries
// jitter and loss carries its burst length because each pair is one physical
// phenomenon; both are documented as such.
func TestEachOverlayOwnsOneAxis(t *testing.T) {
	axes := map[string]func(Shape) bool{
		PatternDelayClimb: func(s Shape) bool { return s.LossPct != 0 || s.ReorderPct != 0 || s.CorruptPct != 0 },
		PatternLossClimb:  func(s Shape) bool { return s.DelayMs != 0 || s.JitterMs != 0 || s.ReorderPct != 0 || s.CorruptPct != 0 },
		// Delay is not a stray here: netem reorders by letting packets skip the
		// delay queue, so reorder without delay is rejected outright. This
		// overlay owns both by necessity, which is documented where it is
		// built and is why it cannot be layered under delay_climb.
		PatternReorderClimb: func(s Shape) bool { return s.JitterMs != 0 || s.LossPct != 0 || s.CorruptPct != 0 },
		PatternCorruptClimb: func(s Shape) bool { return s.DelayMs != 0 || s.JitterMs != 0 || s.LossPct != 0 || s.ReorderPct != 0 },
	}
	for name, strays := range axes {
		p, err := LadderPattern(name, testLadder(), 30)
		if err != nil {
			t.Fatal(err)
		}
		for i, k := range p.Keys {
			if strays(k.Down) {
				t.Errorf("%s key %d strays onto another axis: %+v", name, i, k.Down)
			}
		}
	}
}

// The point of the overlays: a ladder walk and an impairment climb merge into
// one pattern where each still owns what it drove.
func TestMergeLadderWalkWithAnImpairmentOverlay(t *testing.T) {
	walk, err := LadderPattern(PatternValley, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	climb, err := LadderPattern(PatternLossClimb, testLadder(), 30)
	if err != nil {
		t.Fatal(err)
	}
	m, err := MergePatterns("valley+loss", []Pattern{walk, climb})
	if err != nil {
		t.Fatal(err)
	}
	var rate, loss bool
	for _, k := range m.Keys {
		if k.Down.RateMbps != 0 {
			rate = true
		}
		if k.Down.LossPct != 0 {
			loss = true
		}
	}
	if !rate {
		t.Error("merged pattern lost the ladder walk's rate")
	}
	if !loss {
		t.Error("merged pattern lost the overlay's loss")
	}
}

// One ladder for the box, chosen as the most recently measured anywhere on it.
func TestGlobalLadderTakesTheNewestMeasurement(t *testing.T) {
	older := Ladder{Service: "a", Provenance: LadderMeasured, MeasuredAt: 100,
		Rungs: []Rung{{Mbps: 1, UpAtMbps: 1.5}, {Mbps: 2, UpAtMbps: 3}}}
	newer := Ladder{Service: "b", Provenance: LadderMeasured, MeasuredAt: 200,
		Rungs: []Rung{{Mbps: 4, UpAtMbps: 6}, {Mbps: 8, UpAtMbps: 12}}}
	all := map[string]Policy{
		"aa:bb": {Ladders: []Ladder{older}},
		"cc:dd": {Ladders: []Ladder{newer}},
	}
	got, ok := GlobalLadder(all)
	if !ok || got.Service != "b" {
		t.Fatalf("got %q (ok=%v), want the newer ladder %q", got.Service, ok, "b")
	}
	// Which device it was measured on is not part of the answer: the same
	// pattern must come out whichever card the operator is looking at.
	got2, _ := GlobalLadder(map[string]Policy{"zz:zz": {Ladders: []Ladder{newer, older}}})
	if got2.Service != got.Service {
		t.Errorf("the ladder changed with the device it was found on: %q vs %q",
			got2.Service, got.Service)
	}
}

// With nothing swept it falls back to the synthesised ladder, and says so, so
// the interface can render a guess differently from a measurement.
func TestGlobalLadderFallsBackAndAdmitsIt(t *testing.T) {
	l, ok := GlobalLadder(map[string]Policy{"aa:bb": {}})
	if ok {
		t.Error("reported a measured ladder when nothing was measured")
	}
	if l.Provenance == LadderMeasured {
		t.Errorf("fallback claims provenance %q", l.Provenance)
	}
	if len(l.Rungs) < 2 {
		t.Error("fallback is not usable as a ladder")
	}
}

// A one-rung ladder cannot drive a walk, so it must not be chosen as the box's
// ladder just for being newest.
func TestGlobalLadderIgnoresLaddersTooShortToWalk(t *testing.T) {
	stub := Ladder{Service: "stub", Provenance: LadderMeasured, MeasuredAt: 999,
		Rungs: []Rung{{Mbps: 1, UpAtMbps: 1.5}}}
	real := Ladder{Service: "real", Provenance: LadderMeasured, MeasuredAt: 1,
		Rungs: []Rung{{Mbps: 1, UpAtMbps: 1.5}, {Mbps: 2, UpAtMbps: 3}}}
	got, ok := GlobalLadder(map[string]Policy{"aa:bb": {Ladders: []Ladder{stub, real}}})
	if !ok || got.Service != "real" {
		t.Errorf("got %q, want the usable ladder", got.Service)
	}
}
