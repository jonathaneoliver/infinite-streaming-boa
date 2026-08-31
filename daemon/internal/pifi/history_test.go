package pifi

import (
	"testing"
	"time"
)

// fill adds n samples ending now, one per second, with a constant rate.
func fill(h *History, mac string, n int, down, up float64) {
	now := time.Now().UnixMilli()
	for i := n - 1; i >= 0; i-- {
		h.Add(mac, Sample{T: now - int64(i)*1000, Down: down, Up: up})
	}
}

func TestWindowKeepsFullResolutionWhenItFits(t *testing.T) {
	h := NewHistory()
	fill(h, "aa", 120, 5, 1)

	got, bucket := h.Window(5*time.Minute, 600)
	if bucket != 1000 {
		t.Fatalf("bucket = %d, want 1000: a range that fits must not be averaged", bucket)
	}
	if len(got["aa"]) != 120 {
		t.Fatalf("got %d samples, want 120", len(got["aa"]))
	}
}

func TestWindowDecimatesLongRangesToTheMean(t *testing.T) {
	h := NewHistory()
	// Alternating 0 and 10 averages to 5. Taking the peak instead would report
	// 10 and overstate the data actually moved.
	now := time.Now().UnixMilli()
	for i := 3599; i >= 0; i-- {
		v := 0.0
		if i%2 == 0 {
			v = 10
		}
		h.Add("aa", Sample{T: now - int64(i)*1000, Down: v})
	}

	got, bucket := h.Window(time.Hour, 600)
	if bucket != 6010 {
		t.Fatalf("bucket = %d, want 6010 (3600s over 599 buckets, one held back "+
			"for the boundary a window straddles)", bucket)
	}
	if n := len(got["aa"]); n > 600 {
		t.Fatalf("got %d points, want at most 600", n)
	}
	// A bucket spans an odd number of samples, so an alternating series
	// averages a little either side of 5 within one bucket. What must hold is
	// that no bucket reports the PEAK -- the failure being guarded against is
	// decimating by max, which would draw a flat 10 and claim twice the data
	// actually moved -- and that the series as a whole still averages 5.
	var sum float64
	pts := got["aa"]
	for i, s := range pts {
		// Buckets are aligned to ABSOLUTE time, so the first and last are
		// partial: whichever samples fall either side of the window edge can
		// leave a boundary bucket holding a single sample. A lone 10 there is
		// genuinely that bucket's mean, not a peak, so asserting on it made this
		// test depend on what time of day it ran -- it passed alone and failed
		// in the suite purely because the preceding tests took longer.
		if i > 0 && i < len(pts)-1 && s.Down > 9 {
			t.Fatalf("interior bucket = %v, looks like a peak rather than a mean",
				s.Down)
		}
		sum += s.Down
	}
	if mean := sum / float64(len(got["aa"])); mean < 4.9 || mean > 5.1 {
		t.Fatalf("series mean = %v, want ~5", mean)
	}
}

func TestWindowExcludesSamplesOlderThanTheRange(t *testing.T) {
	h := NewHistory()
	now := time.Now().UnixMilli()
	h.Add("aa", Sample{T: now - 20*60*1000, Down: 99}) // 20 minutes ago
	h.Add("aa", Sample{T: now - 5*1000, Down: 1})

	got, _ := h.Window(time.Minute, 600)
	if len(got["aa"]) != 1 {
		t.Fatalf("got %d samples, want only the recent one", len(got["aa"]))
	}
	if got["aa"][0].Down != 1 {
		t.Fatalf("kept the stale sample: %v", got["aa"][0])
	}
}

// A client with nothing in the window must be absent, not present-and-empty:
// the interface reads a missing key as "start accumulating live", and an empty
// array would draw a client that has said nothing as a flat zero line.
func TestWindowOmitsClientsWithNothingInRange(t *testing.T) {
	h := NewHistory()
	h.Add("aa", Sample{T: time.Now().Add(-20 * time.Minute).UnixMilli(), Down: 5})

	got, _ := h.Window(time.Minute, 600)
	if _, ok := got["aa"]; ok {
		t.Fatal("client with no samples in range should be omitted")
	}
}

// A cap is a step set to values somebody chose, so decimating it by averaging
// would draw a line at a cap that was never in force -- and invite the reader
// to explain a player's behaviour against it. The bucket takes the value at its
// start, which is where the bucket is stamped.
func TestWindowDoesNotAverageTheCapAcrossAChange(t *testing.T) {
	h := NewHistory()
	now := time.Now().UnixMilli()
	// 120 samples over two minutes: the first minute capped at 12, the second
	// at 1.5. Decimated hard enough that buckets must span the change.
	for i := 119; i >= 0; i-- {
		cap := 12.0
		if i < 60 {
			cap = 1.5
		}
		h.Add("aa", Sample{T: now - int64(i)*1000, Down: 5, Cap: cap})
	}
	series, bucket := h.Window(2*time.Minute, 8)
	if bucket <= 1000 {
		t.Fatalf("bucket %dms: the window was not decimated, so nothing is proven", bucket)
	}
	for _, s := range series["aa"] {
		if s.Cap != 12 && s.Cap != 1.5 {
			t.Fatalf("bucket at %d reports cap %g, which was never set", s.T, s.Cap)
		}
	}
	// Both values must survive: a decimation that kept only one would lose the
	// change the chart exists to show.
	var saw12, saw15 bool
	for _, s := range series["aa"] {
		saw12 = saw12 || s.Cap == 12
		saw15 = saw15 || s.Cap == 1.5
	}
	if !saw12 || !saw15 {
		t.Fatalf("lost a cap level: 12=%v 1.5=%v", saw12, saw15)
	}
}

// Unlimited must stay 0 rather than being smeared into a neighbouring bucket's
// value: 0 means "no ceiling", and any other number drawn there would claim a
// throttle that was not applied.
func TestWindowKeepsUnlimitedAsZero(t *testing.T) {
	h := NewHistory()
	now := time.Now().UnixMilli()
	for i := 59; i >= 0; i-- {
		h.Add("aa", Sample{T: now - int64(i)*1000, Down: 5, Cap: 0})
	}
	series, _ := h.Window(time.Minute, 10)
	for _, s := range series["aa"] {
		if s.Cap != 0 {
			t.Fatalf("unconditioned bucket reports cap %g", s.Cap)
		}
	}
}
