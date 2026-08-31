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
	// The names below match the templates in the streaming project's
	// go-proxy/pkg/ladder/pattern.go exactly. That is deliberate: "valley"
	// already means the same experiment in both places, and a shared vocabulary
	// is what lets a run here and a run of the harness there be compared
	// without a translation table.
	PatternRampUp         = "ramp_up"
	PatternRampDown       = "ramp_down"
	PatternSquareWave     = "square_wave"
	PatternTransientShock = "transient_shock"
	// Blackhole has no counterpart there. It is not a ladder walk at all.
	PatternBlackhole = "blackhole"
)

// BuiltinNames is every generated pattern, in display order.
var BuiltinNames = []string{
	PatternValley, PatternPyramid,
	PatternRampDown, PatternRampUp,
	PatternSquareWave, PatternTransientShock,
	PatternBlackhole,
}

// The blackhole cycle: a minute, with the last ten seconds dark.
//
// Fixed rather than derived from the rung dwell, because the question it asks
// has nothing to do with the ladder. Ten seconds is longer than a segment fetch
// and longer than most players' retry backoff, so it outlasts the buffer of a
// player running thin without being so long that every run ends in a fatal
// error -- and fifty seconds of clear air is enough for the buffer to refill
// before the next one, which is what makes each outage an independent probe
// rather than a slow starvation.
const (
	blackholeClearSec  = 50
	blackholeOutageSec = 10
)

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

// topRungHeadroom is the margin over the TOP variant's cost, which is not the
// same problem as the rungs below it.
//
// Every other rung's up_at is a real observation: the player climbed into that
// rendition when the cap reached that value. The top rung's is not. A sweep
// descends from its own starting cap, so nothing was ever observed climbing
// INTO the top -- its up_at is bounded by where the sweep began rather than by
// the player, which is also why it is the one rung with no down_at. On the two
// ladders measured on this box it lands at 1.34x and 1.39x of cost, BELOW the
// 1.5x floor this file records as the least a player was ever seen to need.
//
// It matters because the top is where a pattern starts and returns to. A cap
// that only just selects the top variant makes the baseline a marginal state
// instead of an unconstrained one, and then a valley's recovery leg cannot be
// told apart from a cap that was too tight the whole time. Nothing sits above
// the top to over-select into, so the headroom costs nothing.
const topRungHeadroom = 1.5

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

// topCapFor is capFor for the highest rung: never below what was measured, and
// never below topRungHeadroom over the variant's cost. See that constant for
// why the top is treated differently from every rung under it.
func topCapFor(r Rung) float64 {
	generous := round2(r.Mbps * topRungHeadroom)
	if measured := capFor(r); measured > generous {
		return measured
	}
	return generous
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
//   - ramp_down walks top to bottom once and stops. The same descent as a
//     valley with the recovery removed, which is what makes it the clearer read
//     of the descent alone: nothing after it to confuse a late switch with an
//     early recovery.
//   - ramp_up is the mirror, and asks the harder question. Not whether a player
//     downshifts -- they all do -- but how long it waits before trusting
//     bandwidth that has appeared. That hesitation is invisible in a valley,
//     where the descent has already primed it.
//   - square_wave is the extremes and nothing in between. With no intermediate
//     rung to land on, a player either crosses the whole ladder in one decision
//     or thrashes at the boundary.
//   - transient_shock holds the top and dips to each lower rung in turn,
//     shallowest first, returning to the top between dips. Because the buffer
//     refills in between, every dip starts from the same condition and is an
//     independent probe at increasing severity: it looks for where a player
//     BREAKS, where valley watches one glide.
//   - blackhole is the odd one out and walks no rungs at all: a minute of clear
//     air with the last ten seconds at total loss. See blackhole().
//
// Every rung walk traverses EVERY rung rather than just the extremes. A
// two-level dip tells you a player fell and got up; walking the ladder tells you
// which renditions it actually stopped at on the way, and the ones it skipped
// are usually the finding. square_wave is the deliberate exception, and exists
// to ask what happens when there is nothing to stop at.
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
	// The top rung is capped differently from the rest; see topRungHeadroom.
	caps[len(caps)-1] = topCapFor(rungs[len(rungs)-1])

	// Blackhole is not a walk of the ladder at all -- it only borrows its top
	// so the clear phase is unconstrained -- so it is built and returned here
	// rather than being expressed as a sequence of caps.
	if name == PatternBlackhole {
		return blackhole(caps[len(caps)-1]), nil
	}

	// Every sequence below ends on the value it began with, so a looping run
	// rejoins its own start without a step change at the seam, and the final
	// keyframe's time is the pattern's duration.
	var seq []float64
	switch name {
	case PatternPyramid:
		seq = outAndBack(caps)
	case PatternValley:
		seq = outAndBack(reversedCaps(caps))
	case PatternRampUp:
		// One-way, so the seam is a genuine jump from top back to bottom: a
		// sawtooth, which is what a repeated ramp is. Asked of a player, it is
		// the harder question of the two -- not whether it downshifts, but how
		// long it waits before trusting bandwidth that has appeared.
		seq = append(append([]float64{}, caps...), caps[0])
	case PatternRampDown:
		d := reversedCaps(caps)
		seq = append(append([]float64{}, d...), d[0])
	case PatternSquareWave:
		// Only the extremes, no rungs between. It asks whether a player can
		// cross the whole ladder in one step or thrashes trying.
		seq = []float64{caps[0], caps[len(caps)-1], caps[0]}
	case PatternTransientShock:
		// Hold the top, then dip to each lower rung in turn, shallowest first,
		// returning to the top between dips. Because the buffer refills in
		// between, each dip is an independent probe at increasing severity --
		// it looks for where a player BREAKS, where valley watches it glide.
		top := caps[len(caps)-1]
		seq = append(seq, top)
		for i := len(caps) - 2; i >= 0; i-- {
			seq = append(seq, caps[i], top)
		}
	default:
		return Pattern{}, fmt.Errorf("no built-in pattern named %q", name)
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

// outAndBack walks a sequence and returns along it, without holding the far end
// twice. The result ends on the value it started from.
func outAndBack(c []float64) []float64 {
	seq := make([]float64, 0, 2*len(c)-1)
	seq = append(seq, c...)
	for i := len(c) - 2; i >= 0; i-- {
		seq = append(seq, c[i])
	}
	return seq
}

func reversedCaps(c []float64) []float64 {
	out := make([]float64, len(c))
	for i, v := range c {
		out[len(c)-1-i] = v
	}
	return out
}

// blackhole is a minute of clear air with the last ten seconds dark.
//
// The cap is held across BOTH phases rather than dropped to zero for the
// outage, because those are different faults and a player tells them apart. A
// rate of zero is a link that got slow; 100% loss is a link that stopped
// answering, with segment requests timing out rather than trickling. Tunnels,
// lift shafts and handovers are the second kind, and testing the first in their
// name would flatter a player that handles only slowness.
func blackhole(topCap float64) Pattern {
	clear := Shape{RateMbps: topCap}
	dark := Shape{RateMbps: topCap, LossPct: 100}
	return Pattern{
		Name: PatternBlackhole,
		Keys: []Keyframe{
			{AtSec: 0, Down: clear, Ease: EaseHold},
			{AtSec: blackholeClearSec, Down: dark, Ease: EaseHold},
			{AtSec: blackholeClearSec + blackholeOutageSec, Down: clear, Ease: EaseHold},
		},
		Loop: true,
	}
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
