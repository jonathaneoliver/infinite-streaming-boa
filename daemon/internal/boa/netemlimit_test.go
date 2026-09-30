package boa

import "testing"

// queueSeconds is how long a full queue of limit packets takes to drain at the
// cap: the latency the queue alone adds, and the longest a raised cap waits.
func queueSeconds(limit int, rateMbps float64) float64 {
	return float64(limit) * mtuBytes * 8 / (rateMbps * 1e6)
}

// #470: the queue used to floor at 1000 packets whatever the rate -- 48 s at
// 0.25 Mbps. With no delay it is now the rate queue alone, about 200 ms at the
// cap, or the packet floor where 200 ms is less than that.
func TestTheQueueIsSizedInTimeNotPackets(t *testing.T) {
	for _, rate := range []float64{0.25, 1, 5, 25, 50, 100, 1000} {
		limit := netemLimit(Shape{RateMbps: rate})
		secs := queueSeconds(limit, rate)
		floorSecs := queueSeconds(netemMinPackets, rate)
		max := float64(netemQueueMs)/1000 + 0.02 // a packet's rounding
		if floorSecs > max {
			max = floorSecs + 0.02
		}
		if secs > max {
			t.Errorf("%g Mbps: limit %d is %.2f s of queue, want at most %.2f s -- "+
				"the latency a real link of that speed does not have", rate, limit, secs, max)
		}
		if limit < netemMinPackets {
			t.Errorf("%g Mbps: limit %d is below the %d-packet floor", rate, limit, netemMinPackets)
		}
	}
	// The case that motivated it, stated as a number: 0.25 Mbps was 1000
	// packets, 48 s.
	if l := netemLimit(Shape{RateMbps: 0.25}); queueSeconds(l, 0.25) > 1 {
		t.Errorf("0.25 Mbps still queues %.1f s", queueSeconds(l, 0.25))
	}
}

// The delay term is why the queue is sized at all: a 50 Mbps link with 500 ms
// of delay holds ~2100 packets in flight, and a queue smaller than that drops
// traffic while the UI reports 0 % loss.
func TestADelayedLinkStillHoldsItsBandwidthDelayProduct(t *testing.T) {
	sh := Shape{RateMbps: 50, DelayMs: 500}
	inFlight := 50e6 * 0.5 / (8 * mtuBytes) // ~2083
	if l := netemLimit(sh); float64(l) < 3*inFlight {
		t.Errorf("50 Mbps + 500 ms: limit %d, want at least 3x the %.0f packets in flight", l, inFlight)
	}
	// Jitter counts as delay: the queue holds the worst case.
	if netemLimit(Shape{RateMbps: 50, DelayMs: 500, JitterMs: 100}) <= netemLimit(sh) {
		t.Error("adding jitter did not grow the queue")
	}
}

// With no rate there is no rate queue: netem only delays, and the old floor
// and the in-flight sizing stand.
func TestAnUnlimitedRateKeepsTheOldSizing(t *testing.T) {
	if l := netemLimit(Shape{DelayMs: 0}); l != 1000 {
		t.Errorf("no rate, no delay: %d, want 1000", l)
	}
	if l := netemLimit(Shape{DelayMs: 500}); float64(l) < 3*1000e6*0.5/(8*mtuBytes) {
		t.Errorf("no rate, 500 ms: %d is below a gigabit's in-flight data x3", l)
	}
	if l := netemLimit(Shape{RateMbps: 10000, DelayMs: 10000}); l != 200000 {
		t.Errorf("the memory ceiling did not hold: %d", l)
	}
}
