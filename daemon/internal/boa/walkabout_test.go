package boa

import (
	"math"
	"testing"
)

// walkModel is the device the generator models; the tests reproduce it rather
// than exporting it, so a change to the generator's choice shows up here as a
// failure rather than being followed silently.
func walkModel() RssiModel {
	return RssiModel{
		N: DefaultExponent, RxDb: DefaultRxDb, TxDb: DefaultTxDb, AutoBand: true,
	}
}

/*
 * The far end is the CLIFF: the last level where the link still holds.
 *
 * Both halves matter and each has failed. Stopping short leaves a walk whose
 * worst moment still streams comfortably -- the first draft ended delivering
 * 16.9 Mbit/s, which is 4K with room to spare. Stopping past it spends the last
 * steps repeating "nothing gets through" at three different distances.
 */
func TestWalkaboutEndsAtTheCliff(t *testing.T) {
	m := walkModel()
	out := walkOutLevels(m)
	if len(out) < 4 {
		t.Fatalf("a walk of %d steps is not a walk", len(out))
	}

	far := out[len(out)-1]
	down, up, _, _ := AutoShapesAt(far, m)
	if down.LossPct >= 100 || up.LossPct >= 100 {
		t.Errorf("the walk ends at %.0f dBm with a dead link (down %.0f%% up %.0f%% loss); "+
			"it should stop at the last level that still holds",
			far, down.LossPct, up.LossPct)
	}

	// One step further must be dead, or the walk stopped short of the edge.
	beyond := far - walkStepDb
	dn, u, _, _ := AutoShapesAt(beyond, m)
	if dn.LossPct < 100 && u.LossPct < 100 {
		t.Errorf("the walk stops at %.0f dBm but %.0f dBm still holds "+
			"(down %.1f Mbps, up %.1f Mbps): it is ending before the cliff",
			far, beyond, dn.RateMbps, u.RateMbps)
	}
}

/*
 * The far end must actually be bad enough to break a stream.
 *
 * TestWalkaboutEndsAtTheCliff pins the walk to the model's floor, but a model
 * whose floor is comfortable would satisfy it and still be useless: the whole
 * premise is walking until the stream falls apart. This asserts the OUTCOME
 * rather than the mechanism, so it fails if a future change to the ladder makes
 * the edge survivable.
 */
func TestWalkaboutReachesSomethingAStreamCannotSurvive(t *testing.T) {
	m := walkModel()
	out := walkOutLevels(m)
	_, up, _, _ := AutoShapesAt(out[len(out)-1], m)

	// The uplink is the direction that fails first, and it carries the
	// downlink's ACKs -- see DATA-CONTRACT Source S.
	if up.LossPct < 1 && up.CorruptPct < 1 {
		t.Errorf("the far end of the walk has an uplink at %.1f Mbps with %.2f%% loss "+
			"and %.2f%% corruption, which no player would notice; the walk never "+
			"reaches the point of the test",
			up.RateMbps, up.LossPct, up.CorruptPct)
	}
}

/*
 * ONE CROSSING EACH WAY, plus the opening one that establishes the band.
 *
 * Three is the whole budget: open on 5GHz, cross to 2.4GHz on the way out,
 * cross back on the way in. More than that means the two MCS ladders are
 * interleaving again and the client is being asked to roam repeatedly, which no
 * real walk does -- the failure BestBandFor's clear-win margin exists to stop.
 */
func TestWalkaboutCrossesBandOnceEachWay(t *testing.T) {
	p := walkabout(PatternWalkabout, 30)
	if len(p.Links) != 3 {
		for _, ev := range p.Links {
			t.Logf("  %s at %.1fs -> %d MHz", ev.Kind, ev.AtSec, ev.ToBandMHz)
		}
		t.Fatalf("want 3 band moves (open, out, back), got %d", len(p.Links))
	}
	if p.Links[0].ToBandMHz != ModelBands[0].FreqMHz {
		t.Errorf("the walk should open on the reference band %d MHz, not %d",
			ModelBands[0].FreqMHz, p.Links[0].ToBandMHz)
	}
	if p.Links[1].ToBandMHz == p.Links[0].ToBandMHz {
		t.Error("the outbound crossing does not change band")
	}
	if p.Links[2].ToBandMHz != p.Links[0].ToBandMHz {
		t.Error("the walk does not come back to the band it started on, so a " +
			"looping run would start its second lap on the wrong radio")
	}
}

/*
 * The opening gather cannot sit at zero.
 *
 * linkFires asks whether an event lies in (prev, pos], and a run starts with
 * the playhead at 0 -- so an event at 0 is never crossed and never fires. It
 * would look right in the stored pattern and do nothing at runtime, leaving the
 * walk to start on whichever band the client happened to be on.
 */
func TestWalkaboutOpeningGatherCanActuallyFire(t *testing.T) {
	p := walkabout(PatternWalkabout, 30)
	open := p.Links[0]
	if open.AtSec <= 0 {
		t.Fatalf("the opening gather is at %gs, which can never fire", open.AtSec)
	}
	if !crossed(0, 1, false, p.DurSec(), open.AtSec) {
		t.Errorf("the opening gather at %gs is not crossed by the first one-second tick",
			open.AtSec)
	}
}

/*
 * A gather is emitted only where the band actually changes.
 *
 * Every event asks a real client to roam, which costs auth, assoc and a 4-way
 * handshake -- 100-300ms of nothing. Emitting one per keyframe would make the
 * walk a roaming test with some impairment attached rather than the reverse.
 */
func TestWalkaboutEmitsNoRedundantGathers(t *testing.T) {
	m := walkModel()
	p := walkabout(PatternWalkabout, 30)
	levels := walkLevels(m)

	// Rebuild the band at each keyframe and count the changes.
	changes := 1 // the opening one
	prev := 0
	for i, lvl := range levels {
		_, _, freq, _ := AutoShapesAt(lvl, m)
		if i > 0 && freq != prev {
			changes++
		}
		prev = freq
	}
	if len(p.Links) != changes {
		t.Errorf("the pattern carries %d band moves but the levels change band %d times",
			len(p.Links), changes)
	}
	for i := 1; i < len(p.Links); i++ {
		if p.Links[i].ToBandMHz == p.Links[i-1].ToBandMHz {
			t.Errorf("band move %d repeats the previous destination (%d MHz)",
				i, p.Links[i].ToBandMHz)
		}
	}
}

/*
 * THE RETURN LEG IS SLOWER. Recovery is not degradation reversed: rate control
 * drops on a few failed frames and climbs back only after sustained success,
 * and a player has a drained buffer to refill before it dares go up. A
 * symmetric walk reports a recovery that never happened.
 */
func TestWalkaboutReturnLegIsSlowerThanTheWayOut(t *testing.T) {
	m := walkModel()
	p := walkabout(PatternWalkabout, 30)
	apex := len(walkOutLevels(m)) - 1

	outStep := p.Keys[1].AtSec - p.Keys[0].AtSec
	backStep := p.Keys[apex+1].AtSec - p.Keys[apex].AtSec
	if backStep <= outStep {
		t.Errorf("the way back steps every %gs against %gs on the way out; "+
			"recovery must be given more time, not the same", backStep, outStep)
	}

	// And the levels themselves are symmetric -- same places, different pace.
	levels := walkLevels(m)
	for i := range levels {
		mirror := len(levels) - 1 - i
		if levels[i] != levels[mirror] {
			t.Fatalf("level %d (%.0f dBm) does not mirror level %d (%.0f dBm)",
				i, levels[i], mirror, levels[mirror])
		}
	}
}

/*
 * Every keyframe holds; none ramps.
 *
 * Two reasons that happen to agree. The MCS ladder steps and does not glide, so
 * an interpolated segment describes a link that does not exist. And lerpShape
 * does not interpolate LossBurst -- a ramped segment would silently zero it,
 * turning correlated loss into the uniform loss netem defaults to and that real
 * links essentially never produce.
 */
func TestWalkaboutHoldsEveryKeyframe(t *testing.T) {
	p := walkabout(PatternWalkabout, 30)
	for i, k := range p.Keys {
		if i == 0 {
			continue // ease is meaningless on the first
		}
		if k.Ease != EaseHold {
			t.Fatalf("keyframe %d eases with %q; a ramp here would drop LossBurst",
				i, k.Ease)
		}
	}
}

/*
 * The generated keyframes ARE the slider's own output.
 *
 * The walkabout must not become a second implementation of the model that
 * drifts from the first. patternlib.go carries the scar: its climb steps were
 * lifted from the UI presets "so the two agree", after they had not.
 */
func TestWalkaboutKeyframesMatchTheModel(t *testing.T) {
	m := walkModel()
	p := walkabout(PatternWalkabout, 30)
	levels := walkLevels(m)
	if len(p.Keys) != len(levels) {
		t.Fatalf("%d keyframes for %d levels", len(p.Keys), len(levels))
	}
	for i, lvl := range levels {
		down, up, _, _ := AutoShapesAt(lvl, m)
		if p.Keys[i].Down != down || p.Keys[i].Up != up {
			t.Errorf("keyframe %d at %.0f dBm does not match the model:\n  down %+v\n  want %+v\n  up   %+v\n  want %+v",
				i, lvl, p.Keys[i].Down, down, p.Keys[i].Up, up)
		}
	}
}

// Keyframe times must be snapped and strictly increasing, or a band move lands
// a half second off the curve it belongs to.
func TestWalkaboutTimesAreSnappedAndOrdered(t *testing.T) {
	p := walkabout(PatternWalkabout, 30)
	for i, k := range p.Keys {
		if k.AtSec != snapHalf(k.AtSec) {
			t.Errorf("keyframe %d at %gs is not on a half second", i, k.AtSec)
		}
		if i > 0 && k.AtSec <= p.Keys[i-1].AtSec {
			t.Errorf("keyframe %d at %gs does not follow %gs", i, k.AtSec, p.Keys[i-1].AtSec)
		}
	}
	for i, ev := range p.Links {
		if ev.AtSec != snapHalf(ev.AtSec) {
			t.Errorf("band move %d at %gs is not on a half second", i, ev.AtSec)
		}
	}
}

/*
 * The walk begins with the signal unchanged for long enough that a player can
 * reach its top rendition before anything happens to it.
 *
 * The repeated opening keyframes are that settle period, not redundancy: the
 * ladder saturates above about -50 dBm, so the first few steps are identical by
 * construction. Removing them would start the walk while the player is still
 * climbing, and its first downshift would be indistinguishable from never
 * having got up there.
 */
func TestWalkaboutSettlesBeforeItDegrades(t *testing.T) {
	p := walkabout(PatternWalkabout, 30)
	settle := 0.0
	for i := 1; i < len(p.Keys); i++ {
		if p.Keys[i].Down != p.Keys[0].Down || p.Keys[i].Up != p.Keys[0].Up {
			break
		}
		settle = p.Keys[i].AtSec
	}
	if settle < 60 {
		t.Errorf("only %gs of unchanged signal before the walk starts moving; "+
			"a player has not reached its ceiling by then", settle)
	}
}

/*
 * A dead uplink disqualifies a band. Found by generating a walkabout and
 * reading it: between -76 and -80 dBm the model kept a phone on 5GHz carrying
 * 38 Mbit/s down with its uplink at 100% loss, then moved it to 2.4GHz at -80
 * where both directions were healthy -- a link that was already over, held for
 * two more steps because the band choice scored the downlink alone.
 */
func TestBandChoiceWillNotPickADeadUplink(t *testing.T) {
	m := walkModel()
	for lvl := walkNearDbm; lvl >= -100; lvl -= 1 {
		freq, width := BestBandFor(lvl, m)
		dn, up := LevelsFor(PathAtBand(lvl, m, freq), m)
		chosen := shapeAtLevel(up, freq, width)
		if chosen.LossPct < 100 {
			continue
		}
		// The chosen band's uplink is dead. That is only acceptable if every
		// band's is -- past the range of the box entirely.
		for _, b := range ModelBands {
			d2, u2 := LevelsFor(PathAtBand(lvl, m, b.FreqMHz), m)
			if shapeAtLevel(u2, b.FreqMHz, b.WidthMHz).LossPct < 100 {
				t.Fatalf("at %.0f dBm the model chose %d MHz, whose uplink is dead "+
					"(down level %.1f), while %d MHz has a live one (down level %.1f)",
					lvl, freq, dn, b.FreqMHz, d2)
			}
		}
	}
}

// The walk is a plausible length: long enough to be a walk, short enough that
// someone will run it. maxPatternSec is the hard limit; this is the useful one.
func TestWalkaboutIsAReasonableLength(t *testing.T) {
	p := walkabout(PatternWalkabout, 0) // 0 = the default dwell
	if d := p.DurSec(); d < 300 || d > 1800 {
		t.Errorf("a walkabout of %.0fs (%.0f min) is outside the range anyone will run",
			d, d/60)
	}
	if len(p.Keys) > maxKeys {
		t.Errorf("%d keyframes exceeds the %d the validator allows", len(p.Keys), maxKeys)
	}
}

// Rounding guard: the levels must land on exact multiples of the step, so the
// mirror comparison above is not comparing accumulated float error.
func TestWalkaboutLevelsAreExact(t *testing.T) {
	for _, l := range walkOutLevels(walkModel()) {
		if off := math.Mod(walkNearDbm-l, walkStepDb); math.Abs(off) > 1e-9 {
			t.Errorf("level %.6f is not a whole number of %g dB steps from %g",
				l, walkStepDb, walkNearDbm)
		}
	}
}

/*
 * THE PATH-LOSS EXPONENT HAS NO EFFECT ON A GENERATED WALK.
 *
 * Counter-intuitive enough that DATA-CONTRACT Source S states it, so it is
 * pinned here rather than left as an assertion. Converting a level between bands
 * goes through the distance, and the exponent cancels exactly:
 *
 *   RssiAt(DistanceFor(r, f0, n), f1, n)
 *     = 20 - FSPL(f1) - 10n * (20 - FSPL(f0) - r)/(10n)
 *     = r + FSPL(f0) - FSPL(f1)
 *
 * So n moves the distance LABEL and nothing else -- not the impairments, not the
 * band choice, not the timing. It matters because it says where NOT to look when
 * a walk comes out wrong, and because it means the walkabout needs no opinion
 * about the building it runs in.
 *
 * The one caveat is real: RssiAt clamps distance to a 1 m floor, so the identity
 * holds only beyond that. On the reference band that is everything below about
 * -27.6 dBm, and the walk starts at -40.
 */
func TestExponentCancelsInBandConversion(t *testing.T) {
	for _, n := range []float64{2.0, 2.8, 3.1, 3.8, 5.0} {
		m := RssiModel{N: n, RxDb: DefaultRxDb, TxDb: DefaultTxDb, AutoBand: true}
		want0 := freeSpaceAt1m(ModelBands[0].FreqMHz)
		for lvl := walkNearDbm; lvl >= -100; lvl -= 0.5 {
			for _, b := range ModelBands {
				got := PathAtBand(lvl, m, b.FreqMHz)
				want := lvl + want0 - freeSpaceAt1m(b.FreqMHz)
				if math.Abs(got-want) > 1e-9 {
					t.Fatalf("n=%g at %.1f dBm on %d MHz: got %.6f, want %.6f -- the exponent did not cancel",
						n, lvl, b.FreqMHz, got, want)
				}
			}
		}
	}
}

// And the consequence, stated as the property that actually matters: the same
// walk comes out of any building.
func TestWalkaboutIsTheSameAtEveryExponent(t *testing.T) {
	base := walkabout(PatternWalkabout, 30)
	for _, n := range []float64{2.0, 3.1, 3.8, 5.0} {
		m := RssiModel{N: n, RxDb: DefaultRxDb, TxDb: DefaultTxDb, AutoBand: true}
		levels := walkLevels(m)
		if len(levels) != len(base.Keys) {
			t.Fatalf("n=%g gives a walk of %d steps against %d", n, len(levels), len(base.Keys))
		}
		for i, lvl := range levels {
			down, up, _, _ := AutoShapesAt(lvl, m)
			if down != base.Keys[i].Down || up != base.Keys[i].Up {
				t.Fatalf("n=%g changes keyframe %d at %.0f dBm", n, i, lvl)
			}
		}
	}
}
