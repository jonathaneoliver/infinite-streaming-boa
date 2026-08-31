package pifi

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// A Pattern drives a device's conditioning along a timeline instead of holding
// it still.
//
// A fixed cap only ever tests steady state, and steady state is not where
// adaptive players are interesting: what a player does *through* a transition
// -- how long it takes to notice, whether it overshoots, whether it recovers --
// is the question this box exists to answer. Commercial impairment boxes call
// these scenarios; this is the same idea with the authoring surface the
// operator already has, because a keyframe here is simply the whole policy at
// one instant.
//
// # Why full-state keyframes rather than per-parameter lanes
//
// Every keyframe carries BOTH directions and all four parameters. Separate
// automation lanes per parameter are more expressive and much worse to reason
// about: three lanes can disagree about what second 30 means, and the operator
// reading a surprising result then has to reconstruct the effective policy in
// their head. A keyframe that is just a Policy is one that can be read.
//
// # Why nothing is ever written to the Store
//
// A run overrides stored policy for its duration and never persists a thing,
// exactly as Sweeper does (see sweep.go). Nothing needs unwinding: a daemon
// that dies mid-pattern comes back conditioning the device precisely as the
// operator left it, and "stop" is implemented by forgetting rather than by
// restoring. Restore-on-stop is the version of this feature that leaves a
// device throttled at 0.5 Mbps after a crash and no record of why.

// Ease is how a segment arrives at its closing keyframe.
const (
	// EaseHold keeps the previous keyframe's values until this one lands, then
	// steps. A cell handover, a lift door, a device switching AP.
	EaseHold = "hold"
	// EaseRamp interpolates across the segment. Walking out of range, a cell
	// filling up, weather closing in on a satellite link.
	EaseRamp = "ramp"
)

// Keyframe is the whole conditioning policy at one instant on the timeline.
type Keyframe struct {
	// AtSec is the offset from the start of the pattern. Quantised to half a
	// second by the API -- see validPattern for why the limit is there rather
	// than in the millisecond range the kernel would accept.
	AtSec float64 `json:"at_sec"`
	Down  Shape   `json:"down"`
	Up    Shape   `json:"up"`
	// Ease governs how the run gets HERE from the preceding keyframe, so it is
	// meaningless on the first one. Empty means EaseHold.
	Ease string `json:"ease,omitempty"`
}

// Pattern is an ordered list of keyframes plus how to leave the end of it.
//
// There is deliberately no "wrap" mode. A loop restarts at the first keyframe,
// so a pattern that should loop smoothly ends with a keyframe holding the same
// values it began with -- which the operator can see on the timeline, unlike a
// flag whose effect is invisible until playback. The UI offers to append that
// closing keyframe; the runtime needs no concept for it.
type Pattern struct {
	Name string     `json:"name"`
	Keys []Keyframe `json:"keys"`
	Loop bool       `json:"loop"`
}

// DurSec is the position of the last keyframe: the loop point, and the moment a
// one-shot run ends.
func (p Pattern) DurSec() float64 {
	if len(p.Keys) == 0 {
		return 0
	}
	return p.Keys[len(p.Keys)-1].AtSec
}

// At resolves the pattern at one position, returning the shapes to enforce and
// the index of the keyframe currently in force.
func (p Pattern) At(sec float64) (down, up Shape, idx int) {
	if len(p.Keys) == 0 {
		return Shape{}, Shape{}, 0
	}
	if sec <= p.Keys[0].AtSec {
		return p.Keys[0].Down, p.Keys[0].Up, 0
	}
	last := len(p.Keys) - 1
	if sec >= p.Keys[last].AtSec {
		return p.Keys[last].Down, p.Keys[last].Up, last
	}
	for i := 0; i < last; i++ {
		a, b := p.Keys[i], p.Keys[i+1]
		if sec < a.AtSec || sec >= b.AtSec {
			continue
		}
		if b.Ease != EaseRamp || b.AtSec <= a.AtSec {
			return a.Down, a.Up, i
		}
		f := (sec - a.AtSec) / (b.AtSec - a.AtSec)
		return lerpShape(a.Down, b.Down, f), lerpShape(a.Up, b.Up, f), i
	}
	return p.Keys[last].Down, p.Keys[last].Up, last
}

// lerpShape interpolates one direction's conditioning across a ramp.
//
// Every impairment but rate is linear, which also keeps the netem invariant
// that jitter may not exceed delay: both endpoints satisfy it, and a linear
// blend of two values that each satisfy an inequality satisfies it too. Rate is
// not linear -- see lerpRate.
//
// The reorder invariant does NOT survive the same argument, and this is where
// that matters. Reorder needs a delay to reorder against, and a ramp from
// {delay 10ms, reorder 25%} to {delay 0, reorder 25%} arrives at exactly the
// combination netem refuses -- with no keyframe an operator could look at to
// see it coming. That is why writeNetem drops reorder when delay is zero
// rather than trusting its input: this function is the caller that can produce
// an invalid pair out of two valid ones.
//
// Every field is listed rather than starting from `a` and overwriting, so
// adding an impairment to Shape breaks the build here instead of silently
// ramping it to zero -- which is what a struct literal missing a field would
// otherwise do, mid-segment, in a way no test of the endpoints would catch.
func lerpShape(a, b Shape, f float64) Shape {
	lin := func(x, y float64) float64 { return round2(x + (y-x)*f) }
	return Shape{
		RateMbps:   lerpRate(a.RateMbps, b.RateMbps, f),
		DelayMs:    lin(a.DelayMs, b.DelayMs),
		JitterMs:   lin(a.JitterMs, b.JitterMs),
		LossPct:    lin(a.LossPct, b.LossPct),
		ReorderPct: lin(a.ReorderPct, b.ReorderPct),
		CorruptPct: lin(a.CorruptPct, b.CorruptPct),
	}
}

// lerpRate interpolates a cap in LOG space.
//
// The rate control is exponential for a documented reason: every interesting
// mobile rate lives between 0.5 and 5 Mbps, and a linear scale buries all of
// them at one end. A ramp has the same problem in time rather than in pixels --
// a linear 1 -> 10 Mbps ramp is already past 5 Mbps at the halfway mark and
// spends the rest of its run in the part of the range nobody was testing.
// Geometric interpolation gives equal time to each doubling.
//
// Zero is the exception: it means unlimited, which has no position on a log
// axis and no meaningful midpoint with any finite rate. A ramp to or from
// unlimited is therefore a step, at the end of the segment.
func lerpRate(a, b, f float64) float64 {
	if a <= 0 || b <= 0 {
		if f >= 1 {
			return b
		}
		return a
	}
	return round2(a * math.Pow(b/a, f))
}

// maxKeys bounds a pattern. Well below anything a person would author by hand,
// and high enough for a generated one; the point is that a malformed request
// cannot make the tick loop walk a million keyframes.
const maxKeys = 256

// maxPatternSec is an hour. History holds 300 samples at 1 Hz and the longest
// chart range is an hour, so a pattern longer than its own chart could never be
// read back whole.
const maxPatternSec = 3600

// validPattern rejects what the runtime cannot honestly play.
func validPattern(p Pattern) error {
	if len(p.Keys) < 2 {
		return fmt.Errorf("a pattern needs at least two keyframes; one keyframe is just a policy")
	}
	if len(p.Keys) > maxKeys {
		return fmt.Errorf("a pattern may have at most %d keyframes", maxKeys)
	}
	if p.Keys[0].AtSec != 0 {
		return fmt.Errorf("the first keyframe must be at 0s")
	}
	for i, k := range p.Keys {
		if i > 0 && k.AtSec <= p.Keys[i-1].AtSec {
			return fmt.Errorf("keyframe %d is at %gs, not after the one before it", i, k.AtSec)
		}
		// Half a second is not an arbitrary grid. Throughput is sampled once a
		// second, so a transition finer than that cannot be OBSERVED even
		// though the kernel would happily accept it -- and a control offering
		// precision the box cannot report against is a lie with a spinner on
		// it.
		if math.Abs(k.AtSec*2-math.Round(k.AtSec*2)) > 1e-6 {
			return fmt.Errorf("keyframe %d is at %gs; times land on half seconds, "+
				"because throughput is only sampled once a second", i, k.AtSec)
		}
		if k.AtSec > maxPatternSec {
			return fmt.Errorf("a pattern may run for at most %d seconds", maxPatternSec)
		}
		switch k.Ease {
		case "", EaseHold, EaseRamp:
		default:
			return fmt.Errorf("keyframe %d: ease must be %q or %q", i, EaseHold, EaseRamp)
		}
		if err := validShape(k.Down); err != nil {
			return fmt.Errorf("keyframe %d down: %w", i, err)
		}
		if err := validShape(k.Up); err != nil {
			return fmt.Errorf("keyframe %d up: %w", i, err)
		}
	}
	return nil
}

// Player run states.
const (
	PatternRunning = "running"
	PatternPaused  = "paused"
	PatternDone    = "done"
)

// Player advances devices along their patterns and reports what it is
// enforcing.
//
// Structurally this is Sweeper's twin: it holds its own lock, never reaches
// back into the Engine, is stepped once per tick and exposes an Override the
// desired-state builder consults. It differs in one way that matters -- a
// sweep is exclusive because Wi-Fi airtime is shared and two sweeps would
// measure each other, while patterns are per device precisely BECAUSE they
// contend: "everything degrades at once" is a scenario people come here to
// reproduce.
type Player struct {
	mu   sync.Mutex
	runs map[string]*patternRun
}

type patternRun struct {
	pat   Pattern
	state string
	// pos is the playhead in seconds, advanced by wall clock rather than by
	// counting ticks. A tick that arrives late or not at all would otherwise
	// stretch a 30-second pattern into a 34-second one, and the whole value of
	// a pattern is that the same run happens the same way twice.
	pos       float64
	lastAt    time.Time
	startedAt time.Time
	laps      int
	idx       int
	down, up  Shape
	reason    string
}

// Start begins a run, or restarts one that has finished.
func (p *Player) Start(mac string, pat Pattern, now time.Time) error {
	if err := validPattern(pat); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runs == nil {
		p.runs = map[string]*patternRun{}
	}
	if r, ok := p.runs[mac]; ok && r.state == PatternRunning {
		return fmt.Errorf("a pattern is already running on this device")
	}
	d, u, i := pat.At(0)
	p.runs[mac] = &patternRun{
		pat: pat, state: PatternRunning, lastAt: now, startedAt: now,
		down: d, up: u, idx: i,
	}
	return nil
}

// Stop forgets a run. Stored policy takes over on the next tick; there is
// nothing to unwind because nothing was ever written.
func (p *Player) Stop(mac string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.runs[mac]; !ok {
		return fmt.Errorf("no pattern is loaded on this device")
	}
	delete(p.runs, mac)
	return nil
}

// Pause halts a run where it stands and hands the device back to stored policy.
//
// Called when the operator moves a slider on a device that is mid-pattern. The
// alternatives are both worse: overwriting their drag on the next tick makes
// the controls feel broken, and letting the drag win means a pattern is running
// that no longer describes what is being enforced. Stopping and saying so is
// the only version the operator can reason about.
func (p *Player) Pause(mac, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.runs[mac]
	if !ok || r.state != PatternRunning {
		return
	}
	r.state = PatternPaused
	r.reason = reason
}

// Resume picks a paused run up from where it stopped.
func (p *Player) Resume(mac string, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.runs[mac]
	if !ok || r.state != PatternPaused {
		return fmt.Errorf("no paused pattern on this device")
	}
	r.state = PatternRunning
	r.lastAt = now
	r.reason = ""
	return nil
}

// Running reports whether this device is being driven right now, so callers
// that must not collide with a run -- a sweep, which also wants the cap -- can
// refuse rather than fight over it.
func (p *Player) Running(mac string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.runs[mac]
	return ok && r.state == PatternRunning
}

// Override is what the desired-state builder consults: the conditioning this
// device should have right now, or false to leave stored policy alone.
//
// Both directions, unlike a sweep's cap-only override: a pattern is a whole
// policy over time, and a scenario that raises latency without touching rate is
// a normal thing to want.
func (p *Player) Override(mac string) (down, up Shape, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, found := p.runs[mac]
	if !found || r.state != PatternRunning {
		return Shape{}, Shape{}, false
	}
	return r.down, r.up, true
}

// Advance moves every running playhead to now and recomputes what it enforces.
//
// Called from the engine tick before reconciliation, so a keyframe boundary
// reaches the kernel on the tick it was crossed rather than a second later.
func (p *Player) Advance(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.runs {
		if r.state != PatternRunning {
			continue
		}
		dt := now.Sub(r.lastAt).Seconds()
		r.lastAt = now
		if dt <= 0 {
			continue
		}
		dur := r.pat.DurSec()
		r.pos += dt
		if dur > 0 && r.pos >= dur {
			if r.pat.Loop {
				r.laps += int(r.pos / dur)
				r.pos = math.Mod(r.pos, dur)
			} else {
				r.pos = dur
				r.state = PatternDone
				r.reason = "the pattern reached its end; the device is back on its stored policy"
			}
		}
		r.down, r.up, r.idx = r.pat.At(r.pos)
	}
}

// PatternView is a run's progress, carried in every snapshot so the UI can draw
// a moving playhead without polling a second endpoint.
//
// Live only. A pattern DEFINITION is operator intent and lives on the policy; a
// run is transient and a daemon restart cancels it.
type PatternView struct {
	// State is "running", "paused" or "done".
	State string `json:"state"`
	Name  string `json:"name"`
	// PosSec is the playhead, and DurSec the position of the last keyframe.
	PosSec float64 `json:"pos_sec"`
	DurSec float64 `json:"dur_sec"`
	Loop   bool    `json:"loop"`
	// Laps counts completed passes, so a long soak shows evidence of having
	// looped rather than just a playhead that keeps resetting.
	Laps int `json:"laps"`
	// Index is the keyframe currently in force -- the one the run has passed
	// most recently, not the one it is heading for.
	Index int `json:"index"`
	// Down and Up are what is being ENFORCED at this instant, which during a
	// ramp is not equal to any keyframe.
	Down      Shape  `json:"down"`
	Up        Shape  `json:"up"`
	Reason    string `json:"reason,omitempty"`
	StartedAt int64  `json:"started_at"`
}

// View returns this device's run, or nil if it has none this daemon run.
func (p *Player) View(mac string) *PatternView {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.runs[mac]
	if !ok {
		return nil
	}
	return &PatternView{
		State: r.state, Name: r.pat.Name,
		PosSec: round2(r.pos), DurSec: r.pat.DurSec(), Loop: r.pat.Loop,
		Laps: r.laps, Index: r.idx, Down: r.down, Up: r.up,
		Reason: r.reason, StartedAt: r.startedAt.UnixMilli(),
	}
}
