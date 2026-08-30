package pifi

import (
	"math"
	"testing"
	"time"
)

// fakePlayer stands in for an ABR client with a known ladder, so a sweep's
// output can be compared against the answer it was supposed to find.
//
// Its defining property is ASYMMETRY, because that is what the sweep design
// turns on. Losing bandwidth, it drops hard -- past the highest rendition that
// would fit -- to protect its buffer. Gaining bandwidth, it climbs one rung at
// a time, because stepping up costs buffer and it wants confidence first.
//
// That is not invented for the test. Measured against a real iPhone on known
// content, every downshift jumped two renditions: 3200x1800, 1696x954 and
// 1056x594 were all stepped over while the ones between were landed on
// squarely.
type fakePlayer struct {
	ladder []float64 // ascending Mbps
	// cur indexes the rendition currently playing.
	cur int
	// downSkip is how many EXTRA rungs it drops below the highest that fits.
	downSkip int
	// vbrPct swings each burst around the rendition rate, the way scene
	// complexity does.
	vbrPct float64
	// silent makes the client stop sending entirely: playback ended or paused.
	silent bool
	// linkMbps is what the link delivers DURING a fetch. A player pulls a
	// segment at this rate and then idles, so the 1 Hz series is bimodal and no
	// individual sample is the rendition -- only the mean over whole segments
	// is. Zero keeps a continuous model for tests that do not care.
	linkMbps float64
	// segmentSec is the fetch cadence.
	segmentSec float64
	// adaptSec is how long a cap change takes to have any effect.
	adaptSec float64
	// fillUntil is the virtual second before which the player is FILLING its
	// buffer: fetching continuously at link rate, with no idle gaps, pulling
	// faster than the rendition it is playing.
	fillUntil float64
}

const playerHeadroom = 0.95

// applyCap moves the player in response to a new cap, asymmetrically.
func (p *fakePlayer) applyCap(capMbps float64) {
	if len(p.ladder) == 0 {
		return
	}
	fit := -1
	for i, r := range p.ladder {
		if capMbps <= 0 || r <= capMbps*playerHeadroom {
			fit = i
		}
	}
	if fit < 0 {
		fit = 0 // nothing fits: sit on the bottom and struggle
	}
	switch {
	case fit < p.cur:
		p.cur = fit - p.downSkip // drop hard
		if p.cur < 0 {
			p.cur = 0
		}
	case fit > p.cur:
		p.cur++ // climb politely, one rung
	}
}

func (p *fakePlayer) rateAt(capMbps float64) float64 {
	if p.silent {
		return 0
	}
	r := p.ladder[p.cur]
	if capMbps > 0 && capMbps*playerHeadroom < r {
		// Cannot sustain this rendition, so it fetches without pause and TCP
		// fills the shaper completely: the whole cap, continuously. The
		// headroom above applies to CHOOSING a rendition, not to what a starved
		// client receives -- it takes everything there is.
		return capMbps
	}
	return r
}

// throughputAt is what the wire carries at time t: a burst while the segment is
// being fetched, zero while the player waits for the next one.
func (p *fakePlayer) throughputAt(capMbps, t float64) float64 {
	rendition := p.rateAt(capMbps)
	if p.linkMbps == 0 || rendition == 0 {
		return rendition // continuous model
	}
	burst := p.linkMbps
	if capMbps > 0 && capMbps < burst {
		burst = capMbps
	}
	duty := rendition / burst
	if duty >= 0.95 {
		return burst // saturated: no idle time between fetches
	}
	return burst * onFraction(t, p.segmentSec, duty*p.segmentSec)
}

// fakeObs feeds the sweep synthetic telemetry for whatever cap it is currently
// holding, reading that cap back through the same Override the engine uses.
type fakeObs struct {
	sw     *Sweeper
	player *fakePlayer
	away   bool
	seed   uint32

	// Cap-change tracking, so the player can be made to react slowly.
	started  bool
	curCap   float64
	capSince float64 // virtual seconds
	prevRate float64 // the rendition it was on before the cap changed
}

// wobble returns a deterministic pseudo-random multiplier spread symmetrically
// around 1, standing in for scene-complexity variation between segments.
//
// Deliberately NOT an alternating +/- swing: that is bimodal, has no central
// mass, and a median over it lands on one mode or the other rather than
// converging -- which says something about the test, not about the detector.
func (o *fakeObs) wobble(pct float64) float64 {
	if pct == 0 {
		return 1
	}
	o.seed = o.seed*1664525 + 1013904223
	unit := float64(o.seed>>8)/float64(1<<24)*2 - 1 // [-1, 1)
	return 1 + unit*pct/100
}

func (o *fakeObs) Live(string) bool { return !o.away }

func (o *fakeObs) Window(mac string, from, to time.Time) []Sample {
	capMbps := 0.0
	if s, ok := o.sw.Override(mac); ok {
		capMbps = s.RateMbps
	}
	if !o.started || capMbps != o.curCap {
		o.prevRate = o.player.rateAt(o.curCap)
		o.curCap, o.capSince, o.started = capMbps, float64(from.Unix()), true
		o.player.applyCap(capMbps)
	}
	var out []Sample
	for t := from; !t.After(to); t = t.Add(time.Second) {
		ts := float64(t.Unix())
		var v float64
		switch {
		case ts < o.capSince+o.player.adaptSec:
			// The cap change has not taken effect yet.
			v = o.prevRate
			if capMbps > 0 && capMbps*playerHeadroom < v {
				v = capMbps * playerHeadroom
			}
		case ts < o.player.fillUntil:
			// Filling: flat out, no gaps. Perfectly steady, and nothing to do
			// with the rendition being played.
			v = o.player.linkMbps
			if capMbps > 0 && capMbps*playerHeadroom < v {
				v = capMbps * playerHeadroom
			}
		default:
			v = o.player.throughputAt(capMbps, ts)
		}
		// Content variation only shows through when the CONTENT is the
		// bottleneck. A flow pinned to the shaper carries exactly the rate
		// netem was told to deliver, whatever is on screen -- so a starved
		// client's series is flat, and that flatness is what identifies it.
		pinned := capMbps > 0 && o.player.rateAt(capMbps) >= capMbps*0.999
		if v > 0 && !pinned {
			v *= o.wobble(o.player.vbrPct)
		}
		out = append(out, Sample{T: t.UnixMilli(), Down: v})
	}
	return out
}

// newPlayer builds a player already sitting on its top rendition, which is
// where an unconditioned client starts.
// Bursty by default: real traffic always is, and a continuous model is what
// let a median-based detector pass every test here before hardware showed the
// series is bimodal. There is no reason to keep the unrealistic option around.
func newPlayer(ladder []float64) *fakePlayer {
	return &fakePlayer{
		ladder: ladder, cur: len(ladder) - 1,
		linkMbps: ladder[len(ladder)-1] * 8, segmentSec: 6,
	}
}

// runSweep drives a sweep to completion over virtual time, one tick a second.
func runSweep(t *testing.T, sw *Sweeper, obs *fakeObs, mac string, maxTicks int) *SweepView {
	t.Helper()
	now := time.Unix(1700000000, 0)
	for i := 0; i < maxTicks; i++ {
		sw.Advance(now, obs)
		if v := sw.View(mac); v != nil && v.State != "running" {
			return v
		}
		now = now.Add(time.Second)
	}
	t.Fatalf("sweep did not finish within %d ticks", maxTicks)
	return nil
}

func testParams() SweepParams {
	p := DefaultSweepParams()
	// Short phases keep the virtual clock cheap; the logic under test does not
	// depend on how long each phase is held.
	p.DwellSec, p.ObserveSec, p.RecoverSec = 10, 16, 10
	return p
}

func rungMbps(l Ladder) []float64 {
	out := make([]float64, 0, len(l.Rungs))
	for _, r := range l.Rungs {
		out = append(out, r.Mbps)
	}
	return out
}

func TestSweepRecoversEveryRungOfAKnownLadder(t *testing.T) {
	// The player drops TWO renditions on every downshift and climbs one at a
	// time -- the behaviour measured on a real iPhone. A descending sweep
	// recovers about half such a ladder; climbing recovers all of it, because
	// the cap never admits more than one rung above where the client sits.
	want := []float64{0.4, 0.9, 1.8, 3.2, 5.8, 9.5, 15}
	sw := &Sweeper{}
	player := newPlayer(want)
	player.downSkip = 1
	obs := &fakeObs{sw: sw, player: player}
	if err := sw.Start("aa:bb", "avplayer", testParams(), time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	v := runSweep(t, sw, obs, "aa:bb", 40000)
	if v.State != "done" {
		t.Fatalf("state = %q (%s), want done", v.State, v.Reason)
	}
	_, ladder, ok := sw.TakeResult()
	if !ok {
		t.Fatal("no ladder from a completed sweep")
	}
	got := rungMbps(ladder)
	if len(got) != len(want) {
		t.Fatalf("found %v, want all %d rungs %v", got, len(want), want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.12*want[i] {
			t.Errorf("rung %d = %.2f, want %.2f (within 12%%)", i, got[i], want[i])
		}
	}
	if ladder.Provenance != LadderMeasured {
		t.Errorf("provenance = %q, want %q", ladder.Provenance, LadderMeasured)
	}
	if ladder.Service != "avplayer" {
		t.Errorf("service = %q", ladder.Service)
	}

	// The invariant: starvation means no rendition fits, so it can only happen
	// while the cap is below the BOTTOM rung. A level marked starved above that
	// is a rendition being thrown away, and would leave a hole in the ladder.
	bottom := want[0]
	for _, lv := range v.Levels {
		if lv.Saturated && lv.CapMbps > bottom {
			t.Errorf("level %d called starved at a %.2f Mbps cap, above the "+
				"%.2f Mbps bottom rung", lv.Level, lv.CapMbps, bottom)
		}
	}
}

func TestSweepCapAdmitsOnlyOneRungAtATime(t *testing.T) {
	// The no-skip guarantee is structural, not a bet on the player being
	// polite: the cap sits just above the rung the client occupies, so the next
	// rendition fits under it and the one after does not.
	r := &sweepRun{params: testParams(), phase: phaseClimb, current: 4, capMbps: 4.2}
	next := r.nextCap()
	// Real ladders are spaced at least ~1.3x, so the rung above 4 is at 5.2 or
	// higher, and the one above that at 6.8 or higher.
	if next*playerHeadroom < 4*1.3 {
		t.Errorf("cap %.2f cannot admit the next rung at 5.2", next)
	}
	if next*playerHeadroom >= 4*1.3*1.3 {
		t.Errorf("cap %.2f could admit two rungs at once, so a skip is possible", next)
	}
	// Nothing climbed: the cap must still rise, or the search stalls.
	r.capMbps, r.current = next, 4
	if again := r.nextCap(); again <= next {
		t.Errorf("cap did not rise when no rung appeared: %.2f then %.2f", next, again)
	}
}

func TestSweepSurvivesVBRSwing(t *testing.T) {
	// +/-20% per sample is ordinary for high-motion content. The median must
	// still land on the rendition rate rather than wandering between rungs.
	want := []float64{1, 2.5, 6}
	sw := &Sweeper{}
	player := newPlayer(want)
	player.vbrPct = 20
	obs := &fakeObs{sw: sw, player: player, seed: 7}
	p := testParams()
	// A longer window is what actually defeats VBR: the median needs enough
	// samples for the noise to cancel. This is the parameter to reach for on
	// high-motion content, not a wider rung-merge threshold.
	p.ObserveSec = 30
	if err := sw.Start("aa:bb", "svc", p, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	runSweep(t, sw, obs, "aa:bb", 20000)
	_, ladder, ok := sw.TakeResult()
	if !ok {
		t.Fatal("no ladder")
	}
	got := rungMbps(ladder)
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.25 {
			t.Errorf("rung %d = %.2f, want %.2f", i, got[i], want[i])
		}
	}
}

func TestSweepRecoversALadderThroughBurstyFetches(t *testing.T) {
	// The test the continuous model could not do. Traffic arrives as bursts at
	// link rate separated by idle, exactly as it does on hardware, so no single
	// sample is ever a rendition rate.
	want := []float64{0.5, 1.2, 2.5, 5, 9}
	sw := &Sweeper{}
	player := newPlayer(want)
	player.linkMbps, player.segmentSec, player.vbrPct = 22, 6, 15
	obs := &fakeObs{sw: sw, player: player, seed: 11}
	p := testParams()
	// The window has to span several segment periods for a mean to mean
	// anything: one that stops mid-fetch weighs that burst too heavily.
	p.ObserveSec = 36
	if err := sw.Start("aa:bb", "avplayer", p, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	v := runSweep(t, sw, obs, "aa:bb", 40000)
	if v.State != "done" {
		t.Fatalf("state = %q (%s)", v.State, v.Reason)
	}
	_, ladder, ok := sw.TakeResult()
	if !ok {
		t.Fatal("no ladder")
	}
	got := rungMbps(ladder)
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.15*want[i] {
			t.Errorf("rung %d = %.2f, want %.2f (within 15%%)", i, got[i], want[i])
		}
	}
}

func TestSweepWaitsOutASlowAdaptingPlayer(t *testing.T) {
	// The regression for the defect a real iPhone exposed. The player clings to
	// its rendition for 40 s after the cap drops -- pinned to the cap, no idle
	// gaps -- then switches. A fixed settle opened the observation window
	// mid-transition and reported a rung 15% above where it actually sat.
	//
	// The settle predicate waits for the client to stop saturating, so it must
	// now recover the ladder exactly despite an adaptation slower than any
	// fixed wait would have allowed for.
	want := []float64{0.6, 1.5, 3.5, 7, 14}
	sw := &Sweeper{}
	player := newPlayer(want)
	player.linkMbps, player.segmentSec, player.vbrPct, player.adaptSec = 25, 6, 10, 40
	obs := &fakeObs{sw: sw, player: player, seed: 3}
	p := testParams()
	// The dwell has to exceed the player's adaptation delay: a climb has no
	// forcing signal, so there is nothing to wait FOR, only a period to wait
	// THROUGH.
	p.DwellSec, p.ObserveSec = 60, 30
	if err := sw.Start("aa:bb", "avplayer", p, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	v := runSweep(t, sw, obs, "aa:bb", 60000)
	if v.State != "done" {
		t.Fatalf("state = %q (%s)", v.State, v.Reason)
	}
	_, ladder, ok := sw.TakeResult()
	if !ok {
		t.Fatal("no ladder")
	}
	got := rungMbps(ladder)
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.1*want[i] {
			t.Errorf("rung %d = %.2f, want %.2f (within 10%%)", i, got[i], want[i])
		}
	}
}

func TestSweepMergeKeepsTheLowerDriftSighting(t *testing.T) {
	// A rung is usually seen more than once, and the first sighting is
	// systematically the worst -- it is the level where the player was
	// mid-transition. Keeping the first stored 16.40 for a rung that sat at
	// 14.3, and then anchored later merges on it, splitting one rendition in
	// two on real hardware.
	r := &sweepRun{params: testParams()}
	if !r.addRung(16.40, 0.45) { // contaminated: high drift
		t.Fatal("first rung rejected")
	}
	if r.addRung(14.90, 0.05) { // clean: low drift, same rendition
		t.Error("recorded a second rung for one rendition")
	}
	if len(r.rungs) != 1 {
		t.Fatalf("got %d rungs, want 1", len(r.rungs))
	}
	if math.Abs(r.rungs[0].mbps-14.90) > 1e-9 {
		t.Errorf("kept %.2f, want the cleaner 14.90 sighting", r.rungs[0].mbps)
	}
}

func TestSweepWaitsForTheBufferToFillBeforeMeasuringTheCeiling(t *testing.T) {
	// The opening level is unconditioned, so there is no cap for the client to
	// be pinned against -- but a player filling its buffer fetches flat out at
	// LINK rate, which has nothing to do with the rendition it is playing.
	//
	// On hardware this produced a 29.31 Mbps "ceiling" for content whose
	// nearest variants were 32.685 and 23.147, and a two-minute mean of 36.76
	// -- above the top variant, which is only possible while filling.
	// Steadiness alone cannot catch it: a player pulling flat out is steady.
	want := []float64{1.5, 4, 10}
	sw := &Sweeper{}
	player := newPlayer(want)
	player.linkMbps, player.segmentSec = 40, 6
	// Fill for the first 60 virtual seconds, at 40 Mbps -- four times the top
	// rung, and nowhere near any rung.
	player.fillUntil = 1700000000 + 60
	obs := &fakeObs{sw: sw, player: player, seed: 5}
	p := testParams()
	p.ObserveSec, p.DwellSec, p.RecoverSec = 24, 20, 60
	if err := sw.Start("aa:bb", "avplayer", p, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	v := runSweep(t, sw, obs, "aa:bb", 60000)
	if v.State != "done" {
		t.Fatalf("state = %q (%s)", v.State, v.Reason)
	}
	_, ladder, ok := sw.TakeResult()
	if !ok {
		t.Fatal("no ladder")
	}
	if math.Abs(v.CeilingMbps-10) > 1.0 {
		t.Errorf("ceiling = %.2f, want ~10 (the top rung); a fill-contaminated "+
			"measurement would read near the 40 Mbps link rate", v.CeilingMbps)
	}
	got := rungMbps(ladder)
	if len(got) != len(want) {
		t.Errorf("found %v, want %v", got, want)
	}
}

func TestIdleFractionSeparatesFillingFromSteadyState(t *testing.T) {
	// The numbers come from real traffic: a client pinned to its cap showed 0%
	// idle, the same device in steady state 18-26%.
	var filling []Sample
	for i := 0; i < 20; i++ {
		filling = append(filling, Sample{Down: 22 + float64(i%3)})
	}
	if got := idleFraction(filling); got >= minIdleFrac {
		t.Errorf("continuous fetching read as %.2f idle, want < %.2f", got, minIdleFrac)
	}

	var steady []Sample
	for i := 0; i < 20; i++ {
		v := 22.0
		if i%4 == 3 {
			v = 0
		}
		steady = append(steady, Sample{Down: v})
	}
	if got := idleFraction(steady); got < minIdleFrac {
		t.Errorf("bursty steady state read as %.2f idle, want >= %.2f", got, minIdleFrac)
	}
}

func TestSweepMergesRungsItCannotResolve(t *testing.T) {
	// Two renditions 100 kbps apart cannot be told apart by a sweep whose
	// smallest step is 250 kbps. Reporting both would invent resolution.
	r := &sweepRun{params: testParams()}
	if !r.addRung(3.0, 0.1) {
		t.Fatal("first rung rejected")
	}
	if r.addRung(3.1, 0.1) {
		t.Error("accepted a rung 100 kbps from an existing one")
	}
	if !r.addRung(3.4, 0.1) {
		t.Error("rejected a rung 400 kbps away, which is resolvable")
	}
}

func TestSweepMergeThresholdGrowsWithRate(t *testing.T) {
	// Regression for two real observations against the synthetic fleet: one
	// 9.5 Mbps rendition read as 9.38 and 9.65 from two windows, and one at
	// 5.8 read as 5.57 and 5.87. A flat 250 kbps threshold reported each as two
	// rungs. Median error scales with the rate, so the threshold has to as well.
	r := &sweepRun{params: testParams()}
	if !r.addRung(9.38, 0.1) {
		t.Fatal("first rung rejected")
	}
	if r.addRung(9.65, 0.1) {
		t.Error("9.38 and 9.65 recorded as separate rungs; that is one rendition")
	}
	r2 := &sweepRun{params: testParams()}
	if !r2.addRung(5.57, 0.1) {
		t.Fatal("first rung rejected")
	}
	if r2.addRung(5.87, 0.1) {
		t.Error("5.57 and 5.87 recorded as separate rungs; that is one rendition")
	}
	// The floor still governs at the bottom, where the percentage is smaller.
	if got, want := r.mergeWithin(1.0), 0.25; math.Abs(got-want) > 1e-9 {
		t.Errorf("mergeWithin(1.0) = %.3f, want the %.2f floor", got, want)
	}
	// The margin that makes the tolerance safe: real ladders are never spaced
	// tighter than about 25%, so a genuine neighbouring rung must survive.
	if !r.addRung(12.0, 0.1) {
		t.Error("merged a rung 28% above an existing one; that is a real rung")
	}
}

func TestSweepFailsWhenNothingIsPlaying(t *testing.T) {
	// The opening level is unconditioned. With no traffic there is no ceiling,
	// and an empty ladder reported as a result would be worse than an error.
	sw := &Sweeper{}
	silent := newPlayer([]float64{5})
	silent.silent = true
	obs := &fakeObs{sw: sw, player: silent}
	if err := sw.Start("aa:bb", "svc", testParams(), time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	v := runSweep(t, sw, obs, "aa:bb", 200)
	if v.State != "failed" {
		t.Fatalf("state = %q, want failed", v.State)
	}
	if _, _, ok := sw.TakeResult(); ok {
		t.Error("a failed sweep yielded a ladder")
	}
}

func TestSweepStopsWhenPlaybackEndsMidRun(t *testing.T) {
	// A player that goes quiet part-way through has not revealed a bottom rung;
	// it has stopped. The rungs already found are kept, but the sweep must not
	// record the silence as one.
	sw := &Sweeper{}
	player := newPlayer([]float64{1, 3, 6})
	obs := &fakeObs{sw: sw, player: player}
	if err := sw.Start("aa:bb", "svc", testParams(), time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	for i := 0; i < 20000; i++ {
		if v := sw.View("aa:bb"); v != nil && len(v.Found) >= 2 {
			player.silent = true
		}
		sw.Advance(now, obs)
		if v := sw.View("aa:bb"); v != nil && v.State != "running" {
			if v.State != "done" {
				t.Fatalf("state = %q (%s), want done", v.State, v.Reason)
			}
			for _, r := range v.Found {
				if r.Mbps < silentMbps {
					t.Errorf("recorded %.2f Mbps as a rung; that is silence", r.Mbps)
				}
			}
			return
		}
		now = now.Add(time.Second)
	}
	t.Fatal("sweep never finished")
}

func TestSweepAbortsWhenDeviceLeaves(t *testing.T) {
	sw := &Sweeper{}
	obs := &fakeObs{sw: sw, player: newPlayer([]float64{2, 5})}
	if err := sw.Start("aa:bb", "svc", testParams(), time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	obs.away = true
	sw.Advance(time.Unix(1700000000, 0), obs)
	v := sw.View("aa:bb")
	if v.State != "failed" {
		t.Fatalf("state = %q, want failed when the device is gone", v.State)
	}
	if _, ok := sw.Override("aa:bb"); ok {
		t.Error("override outlived the failed sweep; the device stays capped")
	}
}

func TestSweepRefusesASecondConcurrentRun(t *testing.T) {
	// Wi-Fi airtime is shared: two sweeps at once measure each other.
	sw := &Sweeper{}
	now := time.Unix(1700000000, 0)
	if err := sw.Start("aa:bb", "svc", testParams(), now); err != nil {
		t.Fatal(err)
	}
	if err := sw.Start("cc:dd", "svc", testParams(), now); err == nil {
		t.Error("started a second sweep on another device")
	}
	if err := sw.Start("aa:bb", "svc", testParams(), now); err == nil {
		t.Error("started a second sweep on the same device")
	}
}

func TestSweepOverrideIsCleanAndEndsWithTheRun(t *testing.T) {
	// The override drops the operator's delay/jitter/loss: a ladder measured
	// through impairment is the player reacting to the impairment, not to rate.
	sw := &Sweeper{}
	now := time.Unix(1700000000, 0)
	if err := sw.Start("aa:bb", "svc", testParams(), now); err != nil {
		t.Fatal(err)
	}
	s, ok := sw.Override("aa:bb")
	if !ok {
		t.Fatal("no override during a running sweep")
	}
	if !s.IsClean() {
		t.Errorf("opening level = %+v, want unconditioned for the ceiling probe", s)
	}
	if _, ok := sw.Override("cc:dd"); ok {
		t.Error("override leaked to a device that is not being swept")
	}
	if err := sw.Stop("aa:bb"); err != nil {
		t.Fatal(err)
	}
	if _, ok := sw.Override("aa:bb"); ok {
		t.Error("override outlived the sweep; the device would stay capped")
	}
}

func TestPlateauUsesMeanNotMedian(t *testing.T) {
	// Regression for the defect real hardware exposed. A player on an uncapped
	// link bursts at line rate and then idles, so the series is bimodal. The
	// mean is the delivered rendition; the median is an artefact of the duty
	// cycle and is a rate the traffic never carried.
	//
	// These are the shape of the numbers measured off a real iPhone: bursts
	// around 22 Mbps, ~40% idle, delivering about 13 Mbps.
	var samples []Sample
	for i := 0; i < 10; i++ {
		if i < 6 {
			samples = append(samples, Sample{Down: 22})
		} else {
			samples = append(samples, Sample{Down: 0})
		}
	}
	rate, _, n := plateau(samples)
	if n != 10 {
		t.Fatalf("n = %d", n)
	}
	if math.Abs(rate-13.2) > 0.1 {
		t.Errorf("rate = %.2f, want 13.2 (bytes over time); a median would say 22", rate)
	}
}

func TestPlateauDriftIgnoresBurstinessButCatchesMovement(t *testing.T) {
	// A duty-cycled fetch sitting rock steady on one rendition must NOT be
	// flagged: an interquartile spread called this unstable, which on real
	// traffic would have flagged every level.
	var bursty []Sample
	for i := 0; i < 20; i++ {
		v := 0.0
		if i%3 != 2 {
			v = 20
		}
		bursty = append(bursty, Sample{Down: v})
	}
	if _, drift, _ := plateau(bursty); drift > unstableDrift {
		t.Errorf("steady bursty fetch flagged unstable: drift %.2f", drift)
	}

	// A rate that actually moved across the window must be flagged: the two
	// halves disagree, so the mean describes neither.
	var moving []Sample
	for i := 0; i < 20; i++ {
		v := 12.0
		if i >= 10 {
			v = 4
		}
		moving = append(moving, Sample{Down: v})
	}
	if _, drift, _ := plateau(moving); drift <= unstableDrift {
		t.Errorf("a window spanning a rendition change was not flagged: drift %.2f", drift)
	}
}

func TestPutLadderIsKeyedByService(t *testing.T) {
	// The correction that matters: one device, several services, no
	// overwriting. Netflix and YouTube share nothing.
	var p Policy
	p.PutLadder(Ladder{Service: "netflix", Rungs: []Rung{{Mbps: 5}}})
	p.PutLadder(Ladder{Service: "youtube", Rungs: []Rung{{Mbps: 2}}})
	if len(p.Ladders) != 2 {
		t.Fatalf("got %d ladders, want 2", len(p.Ladders))
	}
	p.PutLadder(Ladder{Service: "Netflix", Rungs: []Rung{{Mbps: 8}}})
	if len(p.Ladders) != 2 {
		t.Fatalf("case-different service added a duplicate: %d ladders", len(p.Ladders))
	}
	l, ok := p.LadderFor("NETFLIX")
	if !ok || l.Rungs[0].Mbps != 8 {
		t.Errorf("lookup = %+v, %v; want the replaced 8 Mbps ladder", l, ok)
	}
	if _, ok := p.LadderFor("youtube"); !ok {
		t.Error("replacing one service's ladder dropped another's")
	}
}

func TestHistoryBetweenBounds(t *testing.T) {
	h := NewHistory()
	base := time.Unix(1700000000, 0)
	for i := 0; i < 10; i++ {
		h.Add("aa:bb", Sample{T: base.Add(time.Duration(i) * time.Second).UnixMilli(), Down: float64(i)})
	}
	got := h.Between("aa:bb", base.Add(2*time.Second), base.Add(5*time.Second))
	if len(got) != 4 {
		t.Fatalf("got %d samples, want 4 (inclusive both ends)", len(got))
	}
	if got[0].Down != 2 || got[3].Down != 5 {
		t.Errorf("window = %v", got)
	}
	if n := len(h.Between("no:such", base, base.Add(time.Hour))); n != 0 {
		t.Errorf("unknown mac returned %d samples", n)
	}
}
