package pifi

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Built-in pattern names. These are RESERVED: a saved pattern may not take one,
// because the whole point of a built-in is that its shape is derived rather
// than authored, and a saved pattern shadowing the name would make "valley"
// mean different things on different boxes.
const (
	PatternValley  = "valley"
	PatternPyramid = "pyramid"
)

// BuiltinNames is every generated pattern, in display order.
var BuiltinNames = []string{PatternValley, PatternPyramid}

// IsBuiltin reports whether a name belongs to a generated pattern.
func IsBuiltin(name string) bool {
	for _, n := range BuiltinNames {
		if strings.EqualFold(strings.TrimSpace(name), n) {
			return true
		}
	}
	return false
}

// defaultRungDwellSec is how long a ladder pattern sits on each rung.
//
// It has to exceed the time a player needs to notice a cap change and act on
// it, or the pattern walks the ladder faster than the client can follow and
// every rung reads as a transient. Sweeps on real hardware needed roughly 75s
// of dwell before a switch could be relied on, but a sweep must be certain and
// a pattern only has to provoke: 30s is long enough that a player which is
// going to move has moved, and short enough that traversing a 12-rung ladder
// is minutes rather than a quarter of an hour.
const defaultRungDwellSec = 30

// ladderHeadroom converts a rendition's cost into a cap that will actually
// select it, when the ladder has no measured cap to use instead.
//
// A player will not choose a variant costing everything it has; it wants
// margin. Measured on one iPhone across twelve switches the cap that produced a
// rendition sat between 1.1x and 1.5x its cost -- so this is a midpoint of a
// measured RANGE, not a constant anybody observed. A measured ladder carries
// up_at per rung and should always be preferred to it.
const ladderHeadroom = 1.35

// capFor is the cap to hold a player on one rung.
//
// up_at is the cap OBSERVED to put this client on this rendition, so it beats
// any arithmetic: it already contains whatever headroom this player wanted,
// measured rather than assumed. Note it is a SUFFICIENT cap and not a minimal
// one -- sweeps step 20-45% at a time, so the true threshold is somewhere below
// it. That is the right error to have here: too much headroom keeps the player
// on the rung, too little drops it to the one below and the pattern is
// exercising a rung it did not mean to.
func capFor(r Rung) float64 {
	if r.UpAtMbps > 0 {
		return r.UpAtMbps
	}
	return round2(r.Mbps * ladderHeadroom)
}

// DefaultLadder stands in when a device has no measured or typed ladder.
//
// Rungs 1.5x apart, which is the spacing a ladder is usually built to and close
// to the middle of what has actually been measured here (1.30x to 1.87x). It is
// a placeholder that produces a plausibly-shaped test rather than a description
// of anyone's content -- so it is marked as such, and any pattern built from it
// says so.
func DefaultLadder() Ladder {
	rungs := []Rung{}
	for mbps := 0.4; mbps <= 9; mbps *= 1.5 {
		rungs = append(rungs, Rung{Mbps: round2(mbps)})
	}
	return Ladder{
		Service:    "default",
		Provenance: LadderTyped,
		Rungs:      rungs,
		Note: "generic 1.5x-spaced ladder, not measured from any content: " +
			"sweep the device to replace it",
	}
}

// LadderPattern builds one of the generated patterns from a ladder.
//
// The rates are ladder-relative, which is the entire point. A step at "4 Mbps"
// tells you a player got 4 Mbps; a step at "the cap that holds rung 6" tells you
// which rendition it should have been able to sustain, which is the question
// being asked. Re-sweep the device and the pattern reshapes itself, because
// nothing here is stored.
//
//   - pyramid climbs every rung bottom to top, then descends. It asks whether a
//     player takes the quality that is offered, and how quickly.
//   - valley descends top to bottom, then climbs back. It asks the more
//     interesting question: what a player does under a squeeze, and whether it
//     recovers to where it started.
//
// Both traverse EVERY rung rather than just the extremes. A two-level dip tells
// you a player fell and got up; walking the ladder tells you which renditions it
// actually stopped at on the way, and the ones it skipped are usually the
// finding.
func LadderPattern(name string, l Ladder, dwellSec float64) (Pattern, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !IsBuiltin(name) {
		return Pattern{}, fmt.Errorf("no built-in pattern named %q", name)
	}
	if len(l.Rungs) < 2 {
		// One rung has no shape to traverse, and a pattern needs two keyframes
		// to be a pattern rather than a policy.
		return Pattern{}, fmt.Errorf(
			"a ladder pattern needs at least 2 rungs, %q has %d",
			l.Service, len(l.Rungs))
	}
	if dwellSec <= 0 {
		dwellSec = defaultRungDwellSec
	}

	rungs := append([]Rung(nil), l.Rungs...)
	sort.Slice(rungs, func(i, j int) bool { return rungs[i].Mbps < rungs[j].Mbps })

	caps := make([]float64, len(rungs))
	for i, r := range rungs {
		caps[i] = capFor(r)
	}
	if name == PatternValley {
		// Top first, then down and back up.
		for i, j := 0, len(caps)-1; i < j; i, j = i+1, j-1 {
			caps[i], caps[j] = caps[j], caps[i]
		}
	}

	// Out and back. The last keyframe repeats the first so a looping run
	// rejoins its own start without a step change at the seam.
	seq := make([]float64, 0, 2*len(caps)-1)
	seq = append(seq, caps...)
	for i := len(caps) - 2; i >= 0; i-- {
		seq = append(seq, caps[i])
	}

	keys := make([]Keyframe, 0, len(seq))
	for i, c := range seq {
		keys = append(keys, Keyframe{
			AtSec: float64(i) * dwellSec,
			Down:  Shape{RateMbps: c},
			// Hold, never ramp. A pattern is only diagnostic if you can say
			// what the cap was at a given moment; an interpolated segment means
			// the cap was somewhere between two values for 30 seconds, and no
			// player reaction can be lined up against that.
			Ease: EaseHold,
		})
	}
	return Pattern{Name: name, Keys: keys, Loop: true}, nil
}

// pickLadder chooses which of a device's ladders a generated pattern is built
// from.
//
// A device holds one ladder per service and they share nothing -- Netflix,
// YouTube and a self-hosted stream have different rungs, different segment
// durations and different adaptation logic -- so "the ladder" is only
// meaningful once a service is named. When one is not, the most recently
// measured wins: it is the one the operator was last working on, and it beats
// both alphabetical order and map order, neither of which mean anything.
func pickLadder(p Policy, service string) (Ladder, bool) {
	if service = strings.TrimSpace(service); service != "" {
		for _, l := range p.Ladders {
			if strings.EqualFold(l.Service, service) {
				return l, true
			}
		}
		return Ladder{}, false
	}
	best, ok := Ladder{}, false
	for _, l := range p.Ladders {
		if !ok || l.MeasuredAt > best.MeasuredAt {
			best, ok = l, true
		}
	}
	return best, ok
}

// Stretch limits. A pattern that runs longer than the engine will play, or
// whose steps are shorter than throughput is sampled, is not a pattern anyone
// can read a result off.
const (
	minStretch = 0.1
	maxStretch = 20.0
)

// snapHalf puts a time on the half-second grid validPattern requires.
//
// Throughput is sampled once a second, so a transition finer than half a second
// cannot be OBSERVED even though the kernel would accept it. Stretching has to
// respect that or it produces patterns the validator then rejects with a
// message about grids that the operator never asked to think about.
func snapHalf(sec float64) float64 { return math.Round(sec*2) / 2 }

// StretchPattern scales a pattern in time, keeping its shape.
//
// The knob is a MULTIPLIER rather than a per-step duration because a saved
// pattern's steps need not be uniform -- an authored timeline might hold for
// 20s, dip for 5 and recover over 60 -- and scaling preserves those
// proportions, where imposing one duration on every step would destroy the very
// thing that was authored. For the generated ladder patterns, whose steps ARE
// uniform, seconds-per-step is simply DurSec/(len(Keys)-1), so a UI can still
// label the slider in seconds and recover it after a reload.
//
// Rates are untouched. Stretching asks "how long does the player get at each
// rung", which is a question about time; changing the rates would ask a
// different question and silently invalidate the ladder the pattern was built
// from.
func StretchPattern(p Pattern, factor float64) (Pattern, error) {
	// Zero is refused rather than treated as "unset". Collapsing the time axis
	// to nothing puts every keyframe at the same instant, which is not a fast
	// pattern -- it is a single constant rate, and that is a policy. Clearing
	// the pattern and setting a rate already expresses it, honestly and
	// visibly. Callers pass 1 for "no stretch"; absent must be decided by the
	// caller, because a float cannot tell absent from zero.
	if factor == 0 {
		return Pattern{}, fmt.Errorf(
			"a stretch of 0 collapses every step into one instant, which is a " +
				"constant rate rather than a pattern; clear the pattern and " +
				"set a rate instead")
	}
	if factor < minStretch || factor > maxStretch {
		return Pattern{}, fmt.Errorf("stretch must be between %g and %g",
			minStretch, maxStretch)
	}
	if factor == 1 {
		return p, nil
	}
	out := p
	out.Keys = make([]Keyframe, len(p.Keys))
	copy(out.Keys, p.Keys)
	for i := range out.Keys {
		out.Keys[i].AtSec = snapHalf(p.Keys[i].AtSec * factor)
	}
	// Snapping can collapse two adjacent keyframes into one instant when the
	// factor is small. Caught here with a reason, rather than surfacing as the
	// validator's "not after the one before it", which says nothing about the
	// slider that caused it.
	for i := 1; i < len(out.Keys); i++ {
		if out.Keys[i].AtSec <= out.Keys[i-1].AtSec {
			return Pattern{}, fmt.Errorf(
				"stretching by %gx puts steps closer together than the half "+
					"second throughput is sampled at; use a larger stretch",
				factor)
		}
	}
	if d := out.DurSec(); d > maxPatternSec {
		return Pattern{}, fmt.Errorf(
			"stretching by %gx runs %.0fs, over the %ds a pattern may run; "+
				"the most this pattern will take is %.2gx",
			factor, d, maxPatternSec, float64(maxPatternSec)/p.DurSec())
	}
	return out, nil
}
