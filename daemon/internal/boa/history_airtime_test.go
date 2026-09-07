package boa

import (
	"testing"
	"time"
)

// Airtime has to survive the trip out, at BOTH resolutions.
//
// It did not, and the failure was silent in exactly the way that matters here:
// Window builds a FRESH Sample per bucket and copies the fields it knows about,
// so a newly added one is dropped with no error anywhere. Live samples appended
// by the stream carried airtime while the seed fetched on page load did not,
// which renders as a stacked chart that is blank on load and fills slowly from
// the right -- indistinguishable, on screen, from a radio nobody is using.
//
// Caught by reading the API back from the box rather than from the build
// passing, which is the whole reason that check exists.
func TestWindowKeepsAirtimeAtFullResolution(t *testing.T) {
	h := NewHistory()
	now := time.Now().UnixMilli()
	for i := 9; i >= 0; i-- {
		h.Add("aa", Sample{T: now - int64(i)*1000, Down: 100, Air: 42})
	}
	got, _ := h.Window(time.Minute, 600)
	if len(got["aa"]) == 0 {
		t.Fatal("no samples returned")
	}
	for i, s := range got["aa"] {
		if s.Air != 42 {
			t.Fatalf("sample %d: air = %v, want 42 -- dropped on the way out", i, s.Air)
		}
	}
}

// Meaned across a bucket, like the PHY rates and unlike the cap. A bucket's
// mean airtime is the fraction of that whole span the client held, which is
// exactly how wide its band should be drawn. Decimating by peak would draw a
// client that used the radio half the time as one that never let go of it.
func TestWindowMeansAirtimeAcrossADecimatedBucket(t *testing.T) {
	h := NewHistory()
	now := time.Now().UnixMilli()
	for i := 3599; i >= 0; i-- {
		air := 0.0
		if i%2 == 0 {
			air = 80
		}
		h.Add("aa", Sample{T: now - int64(i)*1000, Air: air})
	}
	pts, _ := h.Window(time.Hour, 600)
	var sum float64
	for _, s := range pts["aa"] {
		sum += s.Air
	}
	if mean := sum / float64(len(pts["aa"])); mean < 39 || mean > 41 {
		t.Fatalf("series mean = %v, want ~40 -- the mean of 0 and 80, not the peak", mean)
	}
}
