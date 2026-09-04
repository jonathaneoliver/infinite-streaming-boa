package boa

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

	// Impairment overlays. Each owns ONE axis and asserts no rate, so it layers
	// over any pattern that drives one -- which is every other pattern here.
	// They are the reason a merge has anything to merge: without them the
	// library was seven patterns all fighting over rate_mbps.
	PatternDelayClimb   = "delay_climb"
	PatternLossClimb    = "loss_climb"
	PatternReorderClimb = "reorder_climb"
	PatternCorruptClimb = "corrupt_climb"

	// Group A link-event patterns (#135). Like the overlays, they assert no
	// rate -- they drive the LINK lane, not the rate lane -- so they layer over
	// any pattern that owns the rate. drop_1m/nudge_1m fire one pulse a minute;
	// deadzone_1m runs clear for 50s then holds a 10s outage, the link-level
	// sibling of blackhole (which does the same shape with netem loss).
	PatternDropEveryMin     = "drop_1m"
	PatternNudgeEveryMin    = "nudge_1m"
	PatternDeadzoneEveryMin = "deadzone_1m"
)

// BuiltinNames is every generated pattern, in display order.
var BuiltinNames = []string{
	PatternValley, PatternPyramid,
	PatternRampDown, PatternRampUp,
	PatternSquareWave, PatternTransientShock,
	PatternBlackhole,
	PatternDelayClimb, PatternLossClimb,
	PatternReorderClimb, PatternCorruptClimb,
	PatternDropEveryMin, PatternNudgeEveryMin, PatternDeadzoneEveryMin,
}

// The impairment ladders each overlay walks.
//
// The delay and loss figures are lifted from this box's own PRESETS rather than
// invented: Fibre 4ms, Cable 15, 4G good 25, 4G weak 60, 3G 100, Satellite 300,
// and the loss pairs likewise. Those were chosen once, for links people
// actually use, and a second set of numbers meaning the same thing would only
// be a second thing to keep true.
//
// Delay carries jitter and loss carries its burst length because each pair is
// ONE physical phenomenon, not two axes that happen to co-occur: a delay with
// no jitter is not a link anyone has, and uniform loss is netem's default and
// essentially never happens on a real one (see Shape.LossBurst). Splitting them
// to gain composability would buy a merge the ability to build links that do
// not exist.
//
// reorder and corrupt have no preset to borrow from and nothing measured here,
// so their ladders are plausible rather than observed, and shallow: both are
// pathologies rather than conditions, and a player meeting 3% corruption has
// already told you what you needed to know.
var (
	delayClimbSteps = []Shape{
		{}, // start clean, so the overlay's first step changes nothing
		{DelayMs: 4, JitterMs: 1},
		{DelayMs: 15, JitterMs: 4},
		{DelayMs: 25, JitterMs: 8},
		{DelayMs: 60, JitterMs: 25},
		{DelayMs: 100, JitterMs: 40},
		{DelayMs: 300, JitterMs: 20},
	}
	lossClimbSteps = []Shape{
		{},
		{LossPct: 0.05, LossBurst: 2},
		{LossPct: 0.1, LossBurst: 4},
		{LossPct: 1, LossBurst: 10},
		{LossPct: 1.5, LossBurst: 10},
		{LossPct: 5, LossBurst: 20},
	}
	// Reorder carries a delay, and not by choice: netem reorders by letting a
	// fraction of packets SKIP the delay queue, so with no queue there is
	// nothing to skip and the shape is rejected (see the validator). 20ms is
	// small enough not to be the thing under test and large enough for the
	// skip to mean something.
	//
	// The consequence for merging is worth knowing: this overlay owns delay as
	// well as reorder, so laying it under delay_climb -- which starts at zero
	// delay -- produces an invalid keyframe at that instant. The validator says
	// so rather than silently dropping the reorder.
	reorderClimbSteps = []Shape{
		{},
		{DelayMs: 20, ReorderPct: 0.1},
		{DelayMs: 20, ReorderPct: 0.5},
		{DelayMs: 20, ReorderPct: 1},
		{DelayMs: 20, ReorderPct: 3},
	}
	corruptClimbSteps = []Shape{{}, {CorruptPct: 0.1}, {CorruptPct: 0.5}, {CorruptPct: 1}, {CorruptPct: 3}}
)

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
//     air with the last ten seconds at total loss, and it sets loss ONLY, so it
//     can be layered over a pattern that drives the rate. See blackhole().
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

	// The overlays walk no rungs and assert no rate, so they need nothing from
	// the ladder and are built and returned here rather than being expressed as
	// a sequence of caps.
	switch name {
	case PatternBlackhole:
		return blackhole(), nil
	case PatternDropEveryMin:
		return linkPattern(name, LinkEvent{AtSec: 30, Kind: LinkDrop}), nil
	case PatternNudgeEveryMin:
		return linkPattern(name, LinkEvent{AtSec: 30, Kind: LinkNudge}), nil
	case PatternDeadzoneEveryMin:
		return linkPattern(name, LinkEvent{AtSec: 50, Kind: LinkDeadzone, DurSec: 10}), nil
	case PatternDelayClimb:
		return impairmentClimb(name, delayClimbSteps, dwellSec), nil
	case PatternLossClimb:
		return impairmentClimb(name, lossClimbSteps, dwellSec), nil
	case PatternReorderClimb:
		return impairmentClimb(name, reorderClimbSteps, dwellSec), nil
	case PatternCorruptClimb:
		return impairmentClimb(name, corruptClimbSteps, dwellSec), nil
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
// It sets LOSS AND NOTHING ELSE. Every other field stays zero, and zero on a
// Shape means "no conditioning of this kind" (model.go) -- so the rate is left
// unlimited rather than pinned to the ladder's top.
//
// Two reasons, and the second is the one that matters. First, it is the more
// honest description: a tunnel does not slow a link, it removes it, and holding
// a cap through the outage describes a fault nobody has. Second, a pattern that
// touches only the axis it is about can be LAYERED on one that owns a different
// axis. transient_shock drives the rate; this drives the loss; together they
// are a ladder walk that periodically goes dark, and neither had to know about
// the other. A blackhole that also asserted a rate would collide with any
// pattern that drives one, which is every other pattern here.
func blackhole() Pattern {
	clear := Shape{}
	dark := Shape{LossPct: 100}
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

// linkPattern is a one-minute loop that conditions the LINK, not the rate: two
// clean keyframes bracket the minute (so the rate stays unlimited and the
// pattern can layer over one that drives the rate), and a single link event
// fires within it. See issue #135.
func linkPattern(name string, ev LinkEvent) Pattern {
	clean := Shape{}
	return Pattern{
		Name: name,
		Keys: []Keyframe{
			{AtSec: 0, Down: clean, Ease: EaseHold},
			{AtSec: 60, Down: clean, Ease: EaseHold},
		},
		Links: []LinkEvent{ev},
		Loop:  true,
	}
}

// GlobalLadder is the one ladder every generated pattern is built from.
//
// # Why one, when ladders are measured per device and per service
//
// Because the numbers a pattern uses are already global, and the data says so.
// Both ladders measured on this box -- a native player and a self-hosted stream,
// different content, different codecs -- produced IDENTICAL up_at values:
//
//	0.45 0.7 1.2 1.79 3.34 4.58 6.41 9.41 12.13 24.05 27.34 38.21
//
// while their costs differed throughout (0.29 vs 0.251 at the bottom, 28.44 vs
// 27.478 at the top). That is not a coincidence. up_at is the cap at which the
// SWEEP observed a switch, and a sweep climbs a fixed geometric schedule, so
// up_at lands on the sweep's own grid. It is a property of the measuring
// instrument. The cost is the property of the content.
//
// capFor prefers up_at, so patterns were already built entirely from that grid.
// Keying them by device and service was describing a dependency they did not
// have, and it cost the two things that matter here: a pattern could not be
// shared without carrying a ladder with it, and the same named pattern was a
// different length on every box.
//
// # The invariant this actually rests on
//
// Not "content does not matter" but "the sweep schedule is the same". Two boxes
// sweeping with a different start cap or climb ratio would produce different
// grids and disagree again. That is the thing to watch, and the thing that
// would silently stop being true.
//
// # How the one is chosen
//
// The most recently measured ladder anywhere on the box. Sweeps still write per
// device and per service -- those records are the measurement history and are
// worth keeping -- but pattern generation reads whichever is newest, because it
// is the one the operator was last working on. Falling back to DefaultLadder
// when nothing has been swept, which is marked as synthesised so the interface
// can say so.
func GlobalLadder(stored Ladder, storedOK bool, all map[string]Policy) (Ladder, bool) {
	// The box's own ladder wins when it has one. The scan below is the
	// migration path: a box swept before the ladder moved out of Policy still
	// has one under a device, and should keep working without a re-sweep.
	if storedOK && len(stored.Rungs) >= 2 {
		return stored, true
	}
	var best Ladder
	var found bool
	for _, p := range all {
		for _, l := range p.Ladders {
			if len(l.Rungs) < 2 {
				continue
			}
			if !found || l.MeasuredAt > best.MeasuredAt {
				best, found = l, true
			}
		}
	}
	if !found {
		return DefaultLadder(), false
	}
	return best, true
}

// pickLadder chooses which of a device's ladders a generated pattern is built
// from.
//
// Retained for the per-device view, which still shows what was measured where.
// Pattern generation uses GlobalLadder instead; see there for why.
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

// MergePatterns lays patterns over one another on a single timeline.
//
// # What a merge is
//
// Not a concatenation. The sources run CONCURRENTLY, and each contributes the
// axes it actually drives: transient_shock owns the rate, blackhole owns the
// loss, and together they are a ladder walk that periodically goes dark without
// either having been written with the other in mind. Zero on a Shape means "no
// conditioning of this kind" (model.go), so a field a pattern leaves alone is a
// field another can fill.
//
// # Which value wins
//
// The first non-zero one, in the order the operator selected them. Not the
// lowest, not the most restrictive, and never a refusal: selection order is a
// choice the operator already made and can see, where "most restrictive" is a
// rule they would have to remember. Two patterns that both drive the rate
// therefore merge to the first one's rate, which is a merge that did nothing
// rather than an error to dismiss.
//
// # Length, and why it only ever grows
//
// A shorter pattern repeats to fill a longer one. Where the lengths do not
// divide, something must stretch, and stretching may only ENLARGE: effects have
// minimum durations to work at all -- an outage has to outlast a segment fetch,
// a rung has to be held long enough to provoke a switch -- and shrinking one
// silently breaks the thing the pattern exists to test. Enlarging cannot: a
// longer dwell provokes a switch at least as reliably, a longer outage is at
// least as total.
//
// So the merged length is a whole multiple of some pattern's period, chosen by
// a short search to minimise the LARGEST stretch any source has to take. Across
// the built-in library the worst case is 1.09x, and most pairs need none: a 60s
// blackhole fits a 660s shock exactly eleven times, untouched.
func MergePatterns(name string, pats []Pattern) (Pattern, error) {
	if len(pats) < 2 {
		return Pattern{}, fmt.Errorf("a merge needs at least 2 patterns, got %d", len(pats))
	}
	durs := make([]float64, len(pats))
	for i, p := range pats {
		if durs[i] = p.DurSec(); durs[i] <= 0 {
			return Pattern{}, fmt.Errorf("pattern %q has no duration", p.Name)
		}
	}
	total, reps := mergeLength(durs)

	// Every instant at which ANY source changes, in merged time. A keyframe
	// anywhere else would be a sample of a step function between its steps,
	// which is a value nothing ever held.
	at := map[float64]bool{0: true, round2(total): true}
	for i, p := range pats {
		f := total / (float64(reps[i]) * durs[i])
		for r := 0; r < reps[i]; r++ {
			for _, k := range p.Keys {
				t := round2((float64(r)*durs[i] + k.AtSec) * f)
				if t > 0 && t < total {
					at[t] = true
				}
			}
		}
	}
	times := make([]float64, 0, len(at))
	for t := range at {
		times = append(times, t)
	}
	sort.Float64s(times)

	keys := make([]Keyframe, 0, len(times))
	for _, t := range times {
		var down, up Shape
		for i, p := range pats {
			f := total / (float64(reps[i]) * durs[i])
			// Its own phase: merged time undone by this source's stretch, then
			// wrapped into one of its cycles.
			phase := math.Mod(t/f, durs[i])
			if t >= total {
				phase = 0 // the seam repeats the start, as every pattern does
			}
			d, u, _ := p.At(phase)
			fillZeroFields(&down, d)
			fillZeroFields(&up, u)
		}
		keys = append(keys, Keyframe{AtSec: t, Down: down, Up: up, Ease: EaseHold})
	}

	// The link lane, carried through the same repeat and stretch as the
	// keyframes above.
	//
	// Leaving it behind was issue #207, and it was worse than a partial merge:
	// linkPattern gives the Group A patterns two CLEAN keyframes on purpose, so
	// they can layer over whatever owns the rate. A merge that kept only
	// keyframes therefore took drop_1m, contributed nothing from it in either
	// lane, and handed back the other pattern unchanged while calling it a
	// merge. Silent, and it looked like it worked.
	//
	// DurSec stretches with everything else. A deadzone that stays 10s while
	// the pattern around it grows by 9% is not the effect that was merged, and
	// enlarging is the safe direction for exactly the reason the length note
	// above gives: these effects have minimum durations, not maximum ones.
	var links []LinkEvent
	for i, p := range pats {
		if len(p.Links) == 0 {
			continue
		}
		f := total / (float64(reps[i]) * durs[i])
		for r := 0; r < reps[i]; r++ {
			for _, ev := range p.Links {
				at := round2((float64(r)*durs[i] + ev.AtSec) * f)
				// The seam repeats the start, as every pattern does, so an
				// event landing exactly on it would fire twice a lap.
				if at < 0 || at >= total {
					continue
				}
				ev.AtSec = at
				if ev.DurSec > 0 {
					ev.DurSec = round2(ev.DurSec * f)
				}
				links = append(links, ev)
			}
		}
	}
	sort.Slice(links, func(a, b int) bool { return links[a].AtSec < links[b].AtSec })

	return Pattern{Name: name, Keys: keys, Links: links, Loop: true}, nil
}

// maxMergeRun bounds how much longer a merge may run than its longest source.
//
// Without it, minimising the stretch always wins by running longer: 660 and 360
// merge with a stretch of only 1.048 if you are willing to run for 2640s, four
// cycles of one against seven of the other. That is 44 minutes to spare 4% of
// distortion, and nobody asked for a 44-minute test -- they asked for those two
// patterns. Length is the cost the operator feels; a rung held 32.7s instead of
// 30 is one they will not notice.
//
// 1.5x leaves room to round up to the next whole cycle of any source without
// leaving room to run away.
const maxMergeRun = 1.5

// mergeLength picks the merged duration and how many times each source repeats.
//
// Candidates are whole multiples of each source's own period -- a length that is
// not a whole multiple of SOMETHING would cut a cycle off mid-shape, which is
// how a blackhole ends up in a pattern that never goes dark -- from the longest
// source up to maxMergeRun times it. Among those the smallest worst-case
// stretch wins, and a tie goes to the shorter.
//
// For 660 and 360 that is 720s: the 360 runs twice untouched and the 660
// stretches by 1.091. The alternative at this length, 660s, would have made the
// 360 run once at 1.833x, which is the same distortion the bound exists to
// avoid, applied to the wrong pattern.
func mergeLength(durs []float64) (float64, []int) {
	longest, shortest := durs[0], durs[0]
	for _, d := range durs {
		if d > longest {
			longest = d
		}
		if d < shortest {
			shortest = d
		}
	}
	var bestTotal float64
	var bestReps []int
	bestWorst := math.Inf(1)
	limit := longest * maxMergeRun
	for _, unit := range durs {
		for k := 1; float64(k)*unit <= limit+1e-9; k++ {
			total := round2(float64(k) * unit)
			if total < longest {
				continue
			}
			reps := make([]int, len(durs))
			worst := 1.0
			for i, d := range durs {
				n := int(math.Floor(total/d + 1e-9))
				if n < 1 {
					n = 1
				}
				reps[i] = n
				if f := total / (float64(n) * d); f > worst {
					worst = f
				}
			}
			if worst < bestWorst-1e-9 || (math.Abs(worst-bestWorst) < 1e-9 && total < bestTotal) {
				bestWorst, bestTotal, bestReps = worst, total, reps
			}
		}
	}
	return bestTotal, bestReps
}

// fillZeroFields copies each field of src into dst only where dst is still
// zero, so the first source to set an axis owns it. Zero is "unset" throughout
// this model, which is what makes first-wins expressible at all.
func fillZeroFields(dst *Shape, src Shape) {
	for _, f := range []struct{ d, s *float64 }{
		{&dst.RateMbps, &src.RateMbps},
		{&dst.DelayMs, &src.DelayMs},
		{&dst.JitterMs, &src.JitterMs},
		{&dst.LossPct, &src.LossPct},
		{&dst.LossBurst, &src.LossBurst},
		{&dst.ReorderPct, &src.ReorderPct},
		{&dst.CorruptPct, &src.CorruptPct},
	} {
		if *f.d == 0 {
			*f.d = *f.s
		}
	}
}

// impairmentClimb walks one impairment axis up and back down, the same
// out-and-back shape a pyramid walks on the rate axis and for the same reason:
// the way back tells you whether a player recovers, which the way up cannot.
//
// It sets NO RATE. That is the whole purpose -- an overlay owns its axis and
// leaves every other one at zero, so a merge can lay it over a pattern that
// drives the cap and neither has to know about the other.
func impairmentClimb(name string, steps []Shape, dwellSec float64) Pattern {
	if dwellSec <= 0 {
		dwellSec = defaultRungDwellSec
	}
	seq := make([]Shape, 0, 2*len(steps)-1)
	seq = append(seq, steps...)
	for i := len(steps) - 2; i >= 0; i-- {
		seq = append(seq, steps[i])
	}
	keys := make([]Keyframe, 0, len(seq))
	for i, sh := range seq {
		keys = append(keys, Keyframe{
			AtSec: float64(i) * dwellSec,
			Down:  sh,
			Ease:  EaseHold,
		})
	}
	return Pattern{Name: name, Keys: keys, Loop: true}
}
