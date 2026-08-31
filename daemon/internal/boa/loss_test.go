package boa

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

// meanLoss and meanBurst invert geParams: they read the two numbers an operator
// actually configured back out of the Markov chain the kernel is given. If the
// round trip does not hold, the interface is asking for one link and the kernel
// is delivering another.
func meanLoss(pPct, rPct float64) float64 { return pPct / (pPct + rPct) * 100 }
func meanBurst(rPct float64) float64      { return 100 / rPct }

func TestGEParamsRoundTrip(t *testing.T) {
	for _, want := range []struct{ loss, burst float64 }{
		{0.1, 3}, {1, 8}, {2, 10}, {2, 15}, {5, 20}, {0.01, 50}, {20, 2},
	} {
		p, r := geParams(want.loss, want.burst)
		if got := meanLoss(p, r); math.Abs(got-want.loss) > 1e-9 {
			t.Errorf("loss %g burst %g: mean loss came back %g", want.loss, want.burst, got)
		}
		if got := meanBurst(r); math.Abs(got-want.burst) > 1e-9 {
			t.Errorf("loss %g burst %g: mean burst came back %g", want.loss, want.burst, got)
		}
	}
}

// The identity point. A burst length of 1 has to be exactly the old uniform
// loss, because every policy and keyframe stored before bursts existed carries
// no burst field at all -- and they must keep meaning what they meant.
func TestBurstOfOneIsUniformLoss(t *testing.T) {
	for _, loss := range []float64{0.1, 1, 2, 5, 20, 50} {
		p, r := geParams(loss, 1)
		if r != 100 {
			t.Errorf("loss %g: r = %g%%, want 100%% (leave the bad state every packet)", loss, r)
		}
		if got := meanLoss(p, r); math.Abs(got-loss) > 1e-9 {
			t.Errorf("loss %g: mean loss %g, want the same", loss, got)
		}
	}
	// An absent field reads as zero, which must behave as 1 rather than as a
	// division by zero.
	if p, r := geParams(2, 0); math.Abs(meanLoss(p, r)-2) > 1e-9 || r != 100 {
		t.Errorf("burst 0 should behave as uniform, got p=%g r=%g", p, r)
	}
}

// The parameters are handed to tc as decimal percentages. At the sparsest
// combination the API allows, p must still survive the format -- rounding it to
// zero would mean the kernel never enters the bad state and drops NOTHING,
// which is the opposite of the loss that was configured and would be silent.
func TestGEParamsSurviveTheWireFormat(t *testing.T) {
	p, _ := geParams(minBurstyLossPct, maxLossBurst)
	s := fmt.Sprintf("%.6f", p)
	got, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("formatting %g gave %q: %v", p, s, err)
	}
	if got == 0 {
		t.Fatalf("p rounds to zero at loss %g%% burst %g (%q): the kernel would drop nothing",
			minBurstyLossPct, float64(maxLossBurst), s)
	}
}

func TestBursty(t *testing.T) {
	cases := []struct {
		sh   Shape
		want bool
	}{
		{Shape{LossPct: 2, LossBurst: 10}, true},
		{Shape{LossPct: 2, LossBurst: 1}, false}, // uniform
		{Shape{LossPct: 2}, false},               // stored before bursts existed
		{Shape{LossBurst: 10}, false},            // no loss: there are no bursts of nothing
		{Shape{RateMbps: 5, LossBurst: 10}, false},
	}
	for _, c := range cases {
		if got := c.sh.Bursty(); got != c.want {
			t.Errorf("%+v: Bursty() = %v, want %v", c.sh, got, c.want)
		}
	}
}

// Burst length imposes nothing on its own, so it must not make a shape dirty --
// otherwise a device with no conditioning would still get a netem qdisc built
// for it.
func TestBurstAloneIsStillClean(t *testing.T) {
	if !(Shape{LossBurst: 20}).IsClean() {
		t.Error("a burst length with no loss should still be clean")
	}
}

func TestValidShapeBurst(t *testing.T) {
	ok := []Shape{
		{LossPct: 2, LossBurst: 10},
		{LossPct: 2, LossBurst: 1},
		{LossPct: 2},
		{LossPct: minBurstyLossPct, LossBurst: maxLossBurst},
		// Turning loss off while a burst length is still set is how anyone
		// stops using this, and it must not be an error: with no loss there
		// are no bursts, so the length is inert rather than wrong.
		{LossPct: 0, LossBurst: 20},
	}
	for _, s := range ok {
		if err := validShape(s); err != nil {
			t.Errorf("%+v rejected: %v", s, err)
		}
	}
	bad := map[string]Shape{
		"burst past the ceiling": {LossPct: 2, LossBurst: maxLossBurst + 1},
		"negative burst":         {LossPct: 2, LossBurst: -1},
		// Too rare to express: the transition probability would round away.
		"bursty with almost no loss": {LossPct: 0.001, LossBurst: 20},
	}
	for name, s := range bad {
		if err := validShape(s); err == nil {
			t.Errorf("%s: accepted, want rejection", name)
		}
	}
}

// The arguments handed to tc, which is the only place any of this becomes real.
func TestNetemLossArgs(t *testing.T) {
	uniform := netemLossArgs(Shape{LossPct: 2})
	if strings.Join(uniform, " ") != "loss 2.0000%" {
		t.Errorf("uniform loss: got %q", uniform)
	}
	if got := netemLossArgs(Shape{LossPct: 2, LossBurst: 1}); strings.Join(got, " ") != "loss 2.0000%" {
		t.Errorf("burst of 1 must be the uniform form, got %q", got)
	}
	bursty := netemLossArgs(Shape{LossPct: 2, LossBurst: 10})
	if len(bursty) != 6 || bursty[1] != "gemodel" {
		t.Fatalf("bursty loss should use gemodel, got %q", bursty)
	}
	// 1-h and 1-k are stated rather than defaulted, so a change in netem's
	// defaults cannot quietly change what a stored policy means.
	if bursty[4] != "100%" || bursty[5] != "0%" {
		t.Errorf("expected total loss in the bad state and none in the good one, got %q", bursty)
	}
	// 100% loss is a blackhole with no burst structure, and the transition
	// probability diverges there.
	if got := netemLossArgs(Shape{LossPct: 100, LossBurst: 10}); strings.Join(got, " ") != "loss 100.0000%" {
		t.Errorf("100%% loss should stay plain, got %q", got)
	}
	if got := netemLossArgs(Shape{}); got != nil {
		t.Errorf("no loss should add no arguments, got %q", got)
	}
}
