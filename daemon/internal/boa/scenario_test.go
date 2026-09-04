package boa

import (
	"testing"
	"time"
)

func twoKey(dur float64) Pattern {
	return Pattern{
		Name: "p",
		Keys: []Keyframe{
			{AtSec: 0, Down: Shape{RateMbps: 8}, Ease: EaseHold},
			{AtSec: dur, Down: Shape{RateMbps: 8}, Ease: EaseHold},
		},
		Loop: true,
	}
}

const macA = "aa:bb:cc:dd:ee:01"
const macB = "aa:bb:cc:dd:ee:02"

// TestGroupMembersShareOneClock is the whole point: two devices started as a
// scenario stay in lockstep, where two separate Starts are offset by however
// long the operator took between them.
func TestGroupMembersShareOneClock(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	if err := p.StartGroup("scn", map[string]Pattern{
		macA: twoKey(60), macB: twoKey(60),
	}, t0); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}

	p.Advance(t0.Add(10 * time.Second))
	p.Advance(t0.Add(25 * time.Second))

	a, b := p.View(macA), p.View(macB)
	if a == nil || b == nil {
		t.Fatal("both members should have a run")
	}
	if a.PosSec != b.PosSec {
		t.Errorf("playheads diverged: %v vs %v -- members of a scenario must "+
			"advance against the same now", a.PosSec, b.PosSec)
	}
	if a.StartedAt != b.StartedAt {
		t.Errorf("started_at differs: %d vs %d", a.StartedAt, b.StartedAt)
	}
	if a.Group != "scn" || b.Group != "scn" {
		t.Errorf("group not reported: %q / %q", a.Group, b.Group)
	}
}

// TestLoneRunIsUnchanged guards the thing most likely to be broken by adding a
// concept: the ordinary per-device play must not grow one.
func TestLoneRunIsUnchanged(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	if err := p.Start(macA, twoKey(60), t0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	v := p.View(macA)
	if v.Group != "" {
		t.Errorf("a lone run reported group %q, want empty", v.Group)
	}
	if p.GroupOf(macA) != "" {
		t.Errorf("GroupOf on a lone run = %q, want empty", p.GroupOf(macA))
	}
	// And stopping it takes nothing else with it.
	if err := p.Start(macB, twoKey(60), t0); err != nil {
		t.Fatalf("Start B: %v", err)
	}
	if err := p.Stop(macA); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.View(macB) == nil {
		t.Error("stopping one lone run stopped another; groups must not leak")
	}
}

// TestStoppingOneMemberStopsTheScenario -- a member left running after a
// sibling was interrupted is measuring against a premise that no longer holds.
func TestStoppingOneMemberStopsTheScenario(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	if err := p.StartGroup("scn", map[string]Pattern{
		macA: twoKey(60), macB: twoKey(60),
	}, t0); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	if err := p.Stop(macA); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.View(macA) != nil || p.View(macB) != nil {
		t.Error("stopping one member left the scenario running on the other")
	}
}

func TestPausingAndResumingMovesTheWholeScenario(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	if err := p.StartGroup("scn", map[string]Pattern{
		macA: twoKey(60), macB: twoKey(60),
	}, t0); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	p.Advance(t0.Add(5 * time.Second))

	p.Pause(macA, "a slider moved")
	if p.View(macB).State != PatternPaused {
		t.Error("pausing one member left the other running")
	}

	// Resume gives every member the same now, so they come back in lockstep
	// rather than fanning out by however long the loop took.
	if err := p.Resume(macA, t0.Add(30*time.Second)); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	p.Advance(t0.Add(40 * time.Second))
	if a, b := p.View(macA), p.View(macB); a.PosSec != b.PosSec {
		t.Errorf("playheads diverged across a pause: %v vs %v", a.PosSec, b.PosSec)
	}
}

// TestStartGroupIsAllOrNothing -- a scenario that began on some of its devices
// is not the scenario that was asked for.
func TestStartGroupIsAllOrNothing(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	if err := p.Start(macB, twoKey(60), t0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := p.StartGroup("scn", map[string]Pattern{
		macA: twoKey(60), macB: twoKey(60), // B is already running
	}, t0)
	if err == nil {
		t.Fatal("StartGroup succeeded over a running run, want a refusal")
	}
	if p.View(macA) != nil {
		t.Error("a refused StartGroup still started a member")
	}
	if g := p.GroupOf(macB); g != "" {
		t.Errorf("the existing lone run was captured into group %q", g)
	}
}

// TestGroupMemberMayFinishAlone -- a member reaching its own last keyframe is
// the pattern doing what it says, not a premise change. A scenario pairing a
// short ladder walk with a long radio outage is a normal thing to want.
func TestGroupMemberMayFinishAlone(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	short, long := twoKey(10), twoKey(600)
	short.Loop, long.Loop = false, false
	if err := p.StartGroup("scn", map[string]Pattern{
		macA: short, macB: long,
	}, t0); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	p.Advance(t0.Add(20 * time.Second))

	if got := p.View(macA).State; got != PatternDone {
		t.Errorf("short member state = %q, want %q", got, PatternDone)
	}
	if got := p.View(macB).State; got != PatternRunning {
		t.Errorf("long member state = %q, want %q -- one member ending must not "+
			"end the others", got, PatternRunning)
	}
}
