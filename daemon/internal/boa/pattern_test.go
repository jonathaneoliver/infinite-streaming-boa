package boa

import (
	"math"
	"testing"
	"time"
)

func kf(at float64, rate float64, ease string) Keyframe {
	return Keyframe{AtSec: at, Down: Shape{RateMbps: rate}, Ease: ease}
}

// A hold segment must not creep. This is the whole difference between "the
// cap dropped at 30s" and "the cap was somewhere between two values for 30s",
// and only the first can be lined up against a player's reaction.
func TestAtHoldsUntilTheNextKeyframe(t *testing.T) {
	p := Pattern{Keys: []Keyframe{kf(0, 8, ""), kf(30, 2, EaseHold), kf(60, 8, EaseHold)}}
	for _, at := range []float64{0, 5, 29.5} {
		if d, _, _ := p.At(at); d.RateMbps != 8 {
			t.Fatalf("at %gs: got %g Mbps, want 8", at, d.RateMbps)
		}
	}
	if d, _, idx := p.At(30); d.RateMbps != 2 || idx != 1 {
		t.Fatalf("at 30s: got %g Mbps idx %d, want 2 idx 1", d.RateMbps, idx)
	}
	if d, _, _ := p.At(59.9); d.RateMbps != 2 {
		t.Fatalf("at 59.9s: got %g Mbps, want 2", d.RateMbps)
	}
}

// A ramp's midpoint is the GEOMETRIC mean, not the arithmetic one. Linear
// interpolation from 1 to 100 would already be past 50 halfway through, so the
// run would spend its second half in a range the operator was not testing.
func TestRampIsGeometricInRate(t *testing.T) {
	p := Pattern{Keys: []Keyframe{kf(0, 1, ""), kf(10, 100, EaseRamp)}}
	d, _, _ := p.At(5)
	if math.Abs(d.RateMbps-10) > 0.01 {
		t.Fatalf("midpoint of a 1->100 ramp: got %g Mbps, want 10", d.RateMbps)
	}
}

// Unlimited has no position on a log axis, so a ramp involving it is a step.
// Interpolating towards 0 would otherwise sweep the cap down to nothing on the
// way to "no cap at all" -- the exact opposite of what was asked for.
func TestRampToUnlimitedIsAStep(t *testing.T) {
	p := Pattern{Keys: []Keyframe{kf(0, 5, ""), kf(10, 0, EaseRamp)}}
	if d, _, _ := p.At(9.9); d.RateMbps != 5 {
		t.Fatalf("before the step: got %g Mbps, want 5", d.RateMbps)
	}
	if d, _, _ := p.At(10); d.RateMbps != 0 {
		t.Fatalf("at the step: got %g Mbps, want 0 (unlimited)", d.RateMbps)
	}
}

// netem subtracts jitter from delay per packet, so a ramp that produced jitter
// above delay would ask the kernel for negative latency and be silently
// clamped into a distribution nobody configured.
func TestRampNeverPutsJitterAboveDelay(t *testing.T) {
	p := Pattern{Keys: []Keyframe{
		{AtSec: 0, Down: Shape{DelayMs: 200, JitterMs: 200}},
		{AtSec: 10, Down: Shape{DelayMs: 20, JitterMs: 5}, Ease: EaseRamp},
	}}
	for at := 0.0; at <= 10; at += 0.5 {
		d, _, _ := p.At(at)
		if d.JitterMs > d.DelayMs {
			t.Fatalf("at %gs: jitter %g exceeds delay %g", at, d.JitterMs, d.DelayMs)
		}
	}
}

// A ramp must carry every impairment, not just the four it was written for. A
// struct literal that forgets a field ramps it to zero mid-segment while both
// endpoints still read correctly, so testing the keyframes alone would miss it.
func TestRampCarriesEveryImpairment(t *testing.T) {
	p := Pattern{Keys: []Keyframe{
		{AtSec: 0, Down: Shape{DelayMs: 100, ReorderPct: 10, CorruptPct: 1}},
		{AtSec: 10, Down: Shape{DelayMs: 100, ReorderPct: 20, CorruptPct: 3},
			Ease: EaseRamp},
	}}
	mid, _, _ := p.At(5)
	for _, c := range []struct {
		name      string
		got, a, b float64
	}{
		{"reorder", mid.ReorderPct, 10, 20},
		{"corrupt", mid.CorruptPct, 1, 3},
	} {
		if c.got <= c.a || c.got >= c.b {
			t.Errorf("%s halfway through the ramp = %g, want between %g and %g",
				c.name, c.got, c.a, c.b)
		}
	}
}

// netem refuses `reorder` with no delay, and refuses the WHOLE command -- so an
// invalid pair takes the device's rate and loss down with it. A ramp reaches
// that pair from two perfectly valid keyframes, which is why the shaper drops
// reorder rather than trusting what it is handed. This records that the pair is
// reachable, so the guard cannot be removed as redundant.
func TestRampCanReachReorderWithoutDelay(t *testing.T) {
	p := Pattern{Keys: []Keyframe{
		{AtSec: 0, Down: Shape{DelayMs: 50, ReorderPct: 25}},
		{AtSec: 10, Down: Shape{DelayMs: 0, ReorderPct: 25}, Ease: EaseRamp},
	}}
	end, _, _ := p.At(10)
	if end.DelayMs != 0 || end.ReorderPct == 0 {
		t.Fatalf("expected the ramp to end at delay 0 with reorder still set, "+
			"got delay=%g reorder=%g", end.DelayMs, end.ReorderPct)
	}
}

func TestValidPattern(t *testing.T) {
	ok := Pattern{Keys: []Keyframe{kf(0, 8, ""), kf(30, 2, EaseRamp)}}
	if err := validPattern(ok); err != nil {
		t.Fatalf("valid pattern rejected: %v", err)
	}
	bad := map[string]Pattern{
		"one keyframe":      {Keys: []Keyframe{kf(0, 8, "")}},
		"not starting at 0": {Keys: []Keyframe{kf(5, 8, ""), kf(10, 2, "")}},
		"out of order":      {Keys: []Keyframe{kf(0, 8, ""), kf(30, 2, ""), kf(10, 4, "")}},
		// Throughput is sampled once a second; a keyframe at 0.3s configures a
		// transition that can never be observed.
		"finer than half a second": {Keys: []Keyframe{kf(0, 8, ""), kf(0.3, 2, "")}},
		"unknown ease":             {Keys: []Keyframe{kf(0, 8, ""), kf(10, 2, "glide")}},
		"jitter above delay": {Keys: []Keyframe{
			{AtSec: 0}, {AtSec: 10, Down: Shape{DelayMs: 5, JitterMs: 50}},
		}},
	}
	for name, p := range bad {
		if err := validPattern(p); err == nil {
			t.Errorf("%s: accepted, want rejection", name)
		}
	}
}

func TestPlayerOverridesOnlyWhileRunning(t *testing.T) {
	p := &Player{}
	pat := Pattern{Keys: []Keyframe{kf(0, 8, ""), kf(30, 2, EaseHold), kf(60, 1, EaseHold)}}
	t0 := time.Unix(1000, 0)
	if err := p.Start("aa", pat, t0); err != nil {
		t.Fatalf("start: %v", err)
	}
	d, _, ok := p.Override("aa")
	if !ok || d.RateMbps != 8 {
		t.Fatalf("at the start: got %v ok=%v, want 8 Mbps", d, ok)
	}
	if _, _, ok := p.Override("bb"); ok {
		t.Fatal("a device with no run must not be overridden")
	}

	p.Advance(t0.Add(31 * time.Second))
	d, _, ok = p.Override("aa")
	if !ok || d.RateMbps != 2 {
		t.Fatalf("past the second keyframe: got %v ok=%v, want 2 Mbps", d, ok)
	}

	p.Advance(t0.Add(61 * time.Second))
	// A one-shot run releases the device rather than holding the last keyframe
	// forever: stored policy is what the operator configured, and a pattern
	// that has ended should not keep conditioning in its own name.
	if v := p.View("aa"); v == nil || v.State != PatternDone {
		t.Fatalf("a finished one-shot run should be done, got %+v", v)
	}
	if _, _, ok := p.Override("aa"); ok {
		t.Fatal("a finished run must stop overriding")
	}
}

func TestPlayerLoopsByWallClock(t *testing.T) {
	p := &Player{}
	pat := Pattern{Loop: true, Keys: []Keyframe{kf(0, 8, ""), kf(10, 2, EaseHold), kf(20, 8, EaseHold)}}
	t0 := time.Unix(1000, 0)
	if err := p.Start("aa", pat, t0); err != nil {
		t.Fatalf("start: %v", err)
	}
	// One tick that swallows two whole laps plus five seconds: the playhead
	// must land on phase, not on however many ticks happened to arrive.
	p.Advance(t0.Add(45 * time.Second))
	v := p.View("aa")
	if v.State != PatternRunning {
		t.Fatalf("a looping run should still be running, got %q", v.State)
	}
	if math.Abs(v.PosSec-5) > 1e-6 {
		t.Fatalf("playhead at %gs, want 5s (45s into a 20s loop)", v.PosSec)
	}
	if v.Laps != 2 {
		t.Fatalf("laps %d, want 2", v.Laps)
	}
	if v.Down.RateMbps != 8 {
		t.Fatalf("at 5s of the loop: %g Mbps, want 8", v.Down.RateMbps)
	}
}

func TestPauseHandsTheDeviceBackAndResumeKeepsPosition(t *testing.T) {
	p := &Player{}
	pat := Pattern{Loop: true, Keys: []Keyframe{kf(0, 8, ""), kf(60, 2, EaseHold)}}
	t0 := time.Unix(1000, 0)
	_ = p.Start("aa", pat, t0)
	p.Advance(t0.Add(10 * time.Second))

	p.Pause("aa", "operator moved a slider")
	if _, _, ok := p.Override("aa"); ok {
		t.Fatal("a paused run must stop overriding, or the slider it paused for is overwritten")
	}
	// Time passing while paused must not move the playhead.
	p.Advance(t0.Add(40 * time.Second))
	if v := p.View("aa"); math.Abs(v.PosSec-10) > 1e-6 {
		t.Fatalf("playhead moved while paused: %gs, want 10s", v.PosSec)
	}
	if err := p.Resume("aa", t0.Add(40*time.Second)); err != nil {
		t.Fatalf("resume: %v", err)
	}
	p.Advance(t0.Add(45 * time.Second))
	if v := p.View("aa"); math.Abs(v.PosSec-15) > 1e-6 {
		t.Fatalf("after resuming: %gs, want 15s", v.PosSec)
	}
}

func TestStopForgetsTheRun(t *testing.T) {
	p := &Player{}
	pat := Pattern{Keys: []Keyframe{kf(0, 8, ""), kf(30, 2, EaseHold)}}
	_ = p.Start("aa", pat, time.Unix(1000, 0))
	if err := p.Stop("aa"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if v := p.View("aa"); v != nil {
		t.Fatalf("a stopped run should leave nothing behind, got %+v", v)
	}
	if err := p.Stop("aa"); err == nil {
		t.Fatal("stopping nothing should report that there was nothing to stop")
	}
}
