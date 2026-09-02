package boa

import "testing"

func TestCrossedPulse(t *testing.T) {
	// non-looping: fires when at is in (prev, pos]
	if !crossed(29, 31, false, 60, 30) {
		t.Error("30 should fire crossing 29->31")
	}
	if crossed(31, 33, false, 60, 30) {
		t.Error("30 should not fire after it is passed")
	}
	if crossed(0, 0.5, false, 60, 0) {
		t.Error("at=0 should not fire on the first tick from prev=0")
	}
	// looping across the wrap: prev=59, pos=1 (dur 60) should fire at=30? no; at=0.5 yes
	if !crossed(59, 1, true, 60, 0.5) {
		t.Error("0.5 should fire on the tick that wrapped past 60")
	}
	if crossed(59, 1, true, 60, 30) {
		t.Error("30 should not fire on a wrap that did not reach it")
	}
}

func TestLinkFiresDeadzoneSpan(t *testing.T) {
	p := Pattern{Links: []LinkEvent{{AtSec: 50, Kind: LinkDeadzone, DurSec: 10}}}
	// inside the span -> a deauth (mapped to drop) fires
	if got := p.linkFires("aa", 50, 51, false, 60); len(got) != 1 || got[0].Kind != LinkDrop {
		t.Errorf("inside deadzone: got %+v, want one drop", got)
	}
	// before and after the span -> nothing
	if got := p.linkFires("aa", 40, 41, false, 60); len(got) != 0 {
		t.Errorf("before deadzone: got %+v, want none", got)
	}
	if got := p.linkFires("aa", 60, 61, false, 60); len(got) != 0 {
		t.Errorf("after deadzone: got %+v, want none", got)
	}
}

func TestBuiltinLinkPatternsValidate(t *testing.T) {
	l := Ladder{Service: "t", Rungs: []Rung{{Mbps: 1}, {Mbps: 3}}}
	for _, name := range []string{PatternDropEveryMin, PatternNudgeEveryMin, PatternDeadzoneEveryMin} {
		pat, err := LadderPattern(name, l, 0)
		if err != nil {
			t.Fatalf("%s: build: %v", name, err)
		}
		if err := validPattern(pat); err != nil {
			t.Errorf("%s: invalid: %v", name, err)
		}
		if len(pat.Links) != 1 {
			t.Errorf("%s: want 1 link event, got %d", name, len(pat.Links))
		}
	}
}

func TestValidPatternRejectsBadLink(t *testing.T) {
	base := []Keyframe{{AtSec: 0}, {AtSec: 60}}
	bad := []LinkEvent{
		{AtSec: 10, Kind: "boom"},              // unknown kind
		{AtSec: 10, Kind: LinkDrop, DurSec: 5}, // pulse with a duration
		{AtSec: 10, Kind: LinkDeadzone},        // deadzone with no duration
	}
	for _, ev := range bad {
		if err := validPattern(Pattern{Keys: base, Links: []LinkEvent{ev}}); err == nil {
			t.Errorf("expected rejection of %+v", ev)
		}
	}
}
