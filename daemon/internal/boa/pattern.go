package boa

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

// Link event kinds. drop and nudge are instant pulses; deadzone is a held
// outage with a duration. See issue #135.
const (
	LinkDrop     = "drop"     // deauthenticate: hard link-down pulse
	LinkNudge    = "nudge"    // disassociate: softer link-down pulse
	LinkDeadzone = "deadzone" // deauth held for DurSec: the client cannot stay on
)

// Deadzone scope: which radios a deadzone holds a client off.
//
// The distinction only exists because this box can serve two radios from one
// SSID onto one bridge, which turns a deny on one of them into an open door on
// the other -- measured at under a second on the bench. Naming the scope is the
// difference between a test that says what it does and one that reads as an
// outage while delivering a roam. See issue #206.
const (
	// ScopeCurrent denies on the radio the client is associated to, and only
	// that one. The client lands on another radio if the box is serving one, so
	// this is a forced roam -- a useful primitive precisely because, unlike a
	// steer, the client cannot decline it.
	ScopeCurrent = "current"
	// ScopeAll denies on every radio serving the AP, which is the sustained
	// outage the deadzone was always described as. Refused rather than
	// half-applied when a radio cannot be reached; see LinkDeadzone.
	ScopeAll = "all"
)

// LinkEvent is one entry on a pattern's link lane. A pulse (drop/nudge) fires
// once as the playhead crosses AtSec; a deadzone re-fires for DurSec, holding
// the client off long enough to drain a player's buffer.
type LinkEvent struct {
	AtSec  float64 `json:"at_sec"`
	Kind   string  `json:"kind"`
	DurSec float64 `json:"dur_sec,omitempty"` // deadzone only
	// Scope is deadzone only, and empty means ScopeCurrent -- so a pattern
	// saved before this field existed keeps doing exactly what it did.
	Scope string `json:"scope,omitempty"`
}

// LinkFire is a link action the Player determined should happen this tick,
// handed back to the Engine to execute against hostapd (outside the Player
// lock, since it does network I/O).
type LinkFire struct {
	MAC    string
	Kind   string
	DurSec float64 // deadzone only
	Scope  string  // deadzone only; empty means ScopeCurrent
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

	// Links are Group A per-client link events on the same clock as the
	// keyframes: association-level impairments (drop/nudge) and held outages
	// (deadzone), as distinct from the rate/loss keyframes which condition
	// packets. They fire as the playhead crosses them. See issue #135.
	Links []LinkEvent `json:"links,omitempty"`

	// Recipe records how a merged pattern was made, when it was made that way.
	// Absent on anything hand-built, which stays entirely legal. See
	// PatternRecipe.
	Recipe *PatternRecipe `json:"recipe,omitempty"`
}

// PatternRecipe is a merge expressed as its ingredients rather than its result.
//
// # Why keep both
//
// The keyframes are what plays, and they have to be: a pattern may be
// hand-built, and one that is has no recipe to resolve. But a merge of
// transient_shock and blackhole is 34 keyframes and 8.6 KB of absolute rates,
// and that is a poor description of a thing whose actual identity is "those two
// patterns, over this ladder". The recipe is 1.7 KB and says so.
//
// # Why it carries no ladder
//
// Because there is only one. The caps a pattern is built from are the sweep's
// grid rather than the content's bitrates, and that grid is the same for every
// device and every service on a box -- see GlobalLadder for the measurement
// that establishes it.
//
// So a recipe is just its ingredients: which patterns, in what order, at what
// dwell. Naming a ladder would be recording a dependency that does not exist,
// and it is the difference between a 1.7 KB object and a 200 byte one that a
// person can read.
type PatternRecipe struct {
	// Sources in the order they were selected, which IS the precedence rule:
	// the first to drive an axis owns it. See MergePatterns.
	Sources []string `json:"sources"`
	// DwellSec and Stretch as they were when the merge was made, so a rebuild
	// reproduces it exactly rather than approximately.
	DwellSec float64 `json:"dwell_sec,omitempty"`
	Stretch  float64 `json:"stretch,omitempty"`
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
	for i, ev := range p.Links {
		switch ev.Kind {
		case LinkDrop, LinkNudge:
			// 0 = a single pulse; >0 = flap (repeat) for that many seconds.
			if ev.DurSec < 0 || ev.DurSec > maxPatternSec {
				return fmt.Errorf("link event %d: %s duration must be 0 (a pulse) to %ds", i, ev.Kind, maxPatternSec)
			}
		case LinkDeadzone:
			if ev.DurSec <= 0 || ev.DurSec > maxPatternSec {
				return fmt.Errorf("link event %d: deadzone needs a duration of 1-%ds", i, maxPatternSec)
			}
			switch ev.Scope {
			case "", ScopeCurrent, ScopeAll:
			default:
				return fmt.Errorf("link event %d: deadzone scope must be %q or %q (got %q)",
					i, ScopeCurrent, ScopeAll, ev.Scope)
			}
		default:
			return fmt.Errorf("link event %d: unknown kind %q (want drop, nudge or deadzone)", i, ev.Kind)
		}
		if ev.Scope != "" && ev.Kind != LinkDeadzone {
			return fmt.Errorf("link event %d: scope is deadzone only, not %s", i, ev.Kind)
		}
		if ev.AtSec < 0 || ev.AtSec > maxPatternSec {
			return fmt.Errorf("link event %d: at %gs is out of range", i, ev.AtSec)
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
	// group ties this run to the others started with it. Empty for a lone
	// per-device run, which is the common case and must stay exactly what it
	// was: a group of one is not a concept the ordinary path pays for.
	group string
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

// StartGroup begins several runs on ONE clock.
//
// # Why a group at all
//
// Each run already advances by wall clock so that "the same run happens the
// same way twice". That holds for one device and stops holding the moment two
// runs are meant to relate to each other, because nothing made them start
// together: two devices given the same pattern are offset by however long the
// operator took to click the second one, and a comparison between different
// moments of the same pattern is not a comparison.
//
// Members are given the same lastAt, and Advance moves every run against a
// single `now`, so they stay in lockstep for the life of the run rather than
// merely starting together.
//
// # Why the group is stopped as a whole
//
// A scenario is one thing, so stopping or pausing any member ends it for all
// of them -- see Stop and Pause. A member that carried on after a sibling was
// interrupted would be measuring against a premise that no longer holds, and
// reporting that as a result is worse than reporting nothing.
//
// A member reaching its OWN last keyframe is not that: it is the pattern doing
// what it says, and a scenario pairing a 60s ladder walk with a 180s radio
// outage is a normal thing to want. Done ends that member only.
//
// All or nothing: if any member is refused, none of them start.
func (p *Player) StartGroup(id string, members map[string]Pattern, now time.Time) error {
	if id == "" {
		return fmt.Errorf("a group needs an id")
	}
	if len(members) == 0 {
		return fmt.Errorf("a group needs at least one member")
	}
	for _, pat := range members {
		if err := validPattern(pat); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for mac := range members {
		if r, ok := p.runs[mac]; ok && r.state == PatternRunning {
			return fmt.Errorf("a pattern is already running on %s", mac)
		}
	}
	if p.runs == nil {
		p.runs = map[string]*patternRun{}
	}
	for mac, pat := range members {
		d, u, i := pat.At(0)
		p.runs[mac] = &patternRun{
			pat: pat, state: PatternRunning, lastAt: now, startedAt: now,
			down: d, up: u, idx: i, group: id,
		}
	}
	return nil
}

// GroupOf names the group a device's run belongs to, or "" if it is a lone run.
func (p *Player) GroupOf(mac string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.runs[mac]
	if !ok {
		return ""
	}
	return r.group
}

// groupMembers lists the MACs in a group. Caller holds the lock.
func (p *Player) groupMembers(id string) []string {
	if id == "" {
		return nil
	}
	var out []string
	for mac, r := range p.runs {
		if r.group == id {
			out = append(out, mac)
		}
	}
	return out
}

// Stop forgets a run. Stored policy takes over on the next tick; there is
// nothing to unwind because nothing was ever written.
//
// A run in a group takes its group with it: the others were only meaningful
// alongside it.
func (p *Player) Stop(mac string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.runs[mac]
	if !ok {
		return fmt.Errorf("no pattern is loaded on this device")
	}
	for _, m := range p.groupMembers(r.group) {
		delete(p.runs, m)
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
	// The rest of the scenario goes with it. The reasoning above is about one
	// device, but it applies harder to several: a run that continues after a
	// sibling's premise changed is still moving its playhead and still looks
	// like a measurement, and it is not one.
	for _, m := range p.groupMembers(r.group) {
		if o := p.runs[m]; o.state == PatternRunning {
			o.state = PatternPaused
			o.reason = reason + " (paused with the rest of the scenario)"
		}
	}
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
	// Every member gets the SAME now, so a scenario resumes in the lockstep it
	// was paused in rather than fanning out by however long the loop took.
	for _, m := range p.groupMembers(r.group) {
		if o := p.runs[m]; o.state == PatternPaused {
			o.state = PatternRunning
			o.lastAt = now
			o.reason = ""
		}
	}
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
func (p *Player) Advance(now time.Time) []LinkFire {
	p.mu.Lock()
	defer p.mu.Unlock()
	var fires []LinkFire
	for mac, r := range p.runs {
		if r.state != PatternRunning {
			continue
		}
		dt := now.Sub(r.lastAt).Seconds()
		r.lastAt = now
		if dt <= 0 {
			continue
		}
		dur := r.pat.DurSec()
		prev := r.pos
		looped := false
		r.pos += dt
		if dur > 0 && r.pos >= dur {
			if r.pat.Loop {
				r.laps += int(r.pos / dur)
				r.pos = math.Mod(r.pos, dur)
				looped = true
			} else {
				r.pos = dur
				r.state = PatternDone
				r.reason = "the pattern reached its end; the device is back on its stored policy"
			}
		}
		fires = append(fires, r.pat.linkFires(mac, prev, r.pos, looped, dur)...)
		r.down, r.up, r.idx = r.pat.At(r.pos)
	}
	return fires
}

// linkFires reports the link actions the playhead triggers moving from prev to
// pos this tick. A pulse (drop/nudge) fires once when its time is crossed; a
// deadzone fires (a deauth) on every tick the playhead is inside its span, so
// the client keeps being kicked for the whole window.
func (p Pattern) linkFires(mac string, prev, pos float64, looped bool, dur float64) []LinkFire {
	var out []LinkFire
	for _, ev := range p.Links {
		if crossed(prev, pos, looped, dur, ev.AtSec) {
			// deadzone carries its duration; the Engine holds the client off
			// with a deny-ACL ban rather than re-firing each tick.
			out = append(out, LinkFire{
				MAC: mac, Kind: ev.Kind, DurSec: ev.DurSec, Scope: ev.Scope,
			})
		}
	}
	return out
}

// crossed reports whether time `at` fell in the interval the playhead covered
// this tick -- (prev, pos] normally, or (prev, dur] plus [0, pos] when the tick
// wrapped the loop.
func crossed(prev, pos float64, looped bool, dur, at float64) bool {
	if !looped {
		return at > prev && at <= pos
	}
	return (at > prev && at <= dur) || (at >= 0 && at <= pos)
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
	// Group is the scenario this run belongs to, or empty for a lone run. The
	// interface uses it to draw ONE transport for several devices rather than
	// one per card, and to say which devices a stop is about to take with it.
	Group string `json:"group,omitempty"`
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
		Group: r.group,
		State: r.state, Name: r.pat.Name,
		PosSec: round2(r.pos), DurSec: r.pat.DurSec(), Loop: r.pat.Loop,
		Laps: r.laps, Index: r.idx, Down: r.down, Up: r.up,
		Reason: r.reason, StartedAt: r.startedAt.UnixMilli(),
	}
}
