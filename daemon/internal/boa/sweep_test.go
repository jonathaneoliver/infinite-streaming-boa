package boa

import (
	"math"
	"strings"
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
	// upshiftSec is the minimum time between upshifts. A real player does not
	// climb the instant headroom appears -- it wants to be confident first --
	// so a cap held steady eventually produces a climb that the same cap did
	// not produce a moment earlier.
	//
	// Without this the model only ever reacts to cap CHANGES, which makes
	// holding a cap pointless by construction and leaves the hold logic
	// untested. Measured on hardware: the client declined to climb at levels 2,
	// 5, 8 and 13 and then took the same headroom a level later.
	upshiftSec float64
	lastClimb  float64
}

// playerDemand is how much more than a rendition's cost this client insists on
// having before it will select that rendition.
//
// Measured, not assumed: across ten switches on an iPhone the ratio between the
// cap in force and the variant taken ran 1.5x to 1.9x, and never below 1.5. The
// model previously used 0.95 -- i.e. a player that grabs anything that fits --
// which is a far more eager client than any real one, and it made cap sizing
// that works on hardware look wrong here.
//
// Selection and delivery are separate: a client needs this much headroom to
// CHOOSE a rendition, but once on one it delivers that rendition's bitrate
// whenever it fits under the cap at all.
const playerDemand = 1.6

// applyCap moves the player, asymmetrically and in its own time.
//
// Called on every observation, not only when the cap changes, because a real
// player's decision depends on how long it has had the headroom as well as on
// how much.
func (p *fakePlayer) applyCap(capMbps, now float64) {
	if len(p.ladder) == 0 {
		return
	}
	fit := -1
	for i, r := range p.ladder {
		if capMbps <= 0 || r*playerDemand <= capMbps {
			fit = i
		}
	}
	if fit < 0 {
		fit = 0 // nothing fits: sit on the bottom and struggle
	}
	switch {
	case fit < p.cur:
		// Losing bandwidth is urgent: drop at once, and hard.
		p.cur = fit - p.downSkip
		if p.cur < 0 {
			p.cur = 0
		}
		p.lastClimb = now
	case fit > p.cur:
		// Gaining it is not: wait until confident, then take one rung.
		if now-p.lastClimb < p.upshiftSec {
			return
		}
		p.cur++
		p.lastClimb = now
	}
}

func (p *fakePlayer) rateAt(capMbps float64) float64 {
	if p.silent {
		return 0
	}
	r := p.ladder[p.cur]
	if capMbps > 0 && r > capMbps {
		// Cannot even fit, so it fetches without pause and TCP fills the shaper
		// completely: the whole cap, continuously. The demand factor governs
		// which rendition gets CHOSEN, not what a starved client receives -- it
		// takes everything there is.
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
	}
	o.player.applyCap(capMbps, float64(to.Unix()))
	var out []Sample
	for t := from; !t.After(to); t = t.Add(time.Second) {
		ts := float64(t.Unix())
		var v float64
		switch {
		case ts < o.capSince+o.player.adaptSec:
			// The cap change has not taken effect yet.
			v = o.prevRate
			if capMbps > 0 && v > capMbps {
				v = capMbps
			}
		case ts < o.player.fillUntil:
			// Filling: flat out, no gaps. Perfectly steady, and nothing to do
			// with the rendition being played.
			v = o.player.linkMbps
			if capMbps > 0 && v > capMbps {
				v = capMbps
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
	// Needs a while before it will take headroom, which is what the cap-hold
	// logic exists to wait out.
	player.upshiftSec = 40
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

func TestAPlateauSittingAtTheCapIsNotARung(t *testing.T) {
	// Observed on hardware. Capped at 0.30 Mbps -- only 12% above the bottom
	// rendition's wire cost, too tight for playback to start from cold -- the
	// client fetched, failed to start, and retried. Those retries left gaps in
	// its traffic, so the duty-cycle test saw a settled client and the sweep
	// recorded "rung at 0.30 Mbps": the cap, not a rendition.
	//
	// A player only selects a variant it can serve with headroom, so a rung it
	// actually chose arrives at about 0.95 of the cap or below. Taking the whole
	// cap means nothing fits.
	r := &sweepRun{
		params: testParams(), phase: phaseClimb,
		level: 1, capMbps: 0.30, state: "running",
	}
	var pinned []Sample
	for i := 0; i < 40; i++ {
		pinned = append(pinned, Sample{Down: 0.30}) // exactly the cap
	}
	r.evaluate(pinned, time.Unix(1700000000, 0))

	if len(r.rungs) != 0 {
		t.Errorf("recorded %v as a rung; that is the shaper, not a rendition", r.rungs)
	}
	if len(r.levels) != 1 || !r.levels[0].Saturated {
		t.Errorf("level not marked starved: %+v", r.levels)
	}
	if len(r.throttle) != 1 {
		t.Fatalf("got %d throttle points, want 1 -- a client pinned to the cap "+
			"is a free measurement of the shaper", len(r.throttle))
	}

	// A rung the player genuinely chose sits below the cap and must survive.
	r2 := &sweepRun{
		params: testParams(), phase: phaseClimb,
		level: 1, capMbps: 0.30, state: "running",
	}
	var chosen []Sample
	for i := 0; i < 40; i++ {
		chosen = append(chosen, Sample{Down: 0.264}) // 234p, 88% of the cap
	}
	r2.evaluate(chosen, time.Unix(1700000000, 0))
	if len(r2.rungs) != 1 {
		t.Errorf("a rung at 88%% of the cap was discarded: %v", r2.rungs)
	}
}

func TestARungRecordsTheCapThatCausedTheSwitch(t *testing.T) {
	// The rung's own bitrate cannot drive a pattern. A player does not select a
	// rendition merely because its bitrate fits -- measured on an iPhone, it
	// took a variant only when the cap was 1.5x to 1.9x that variant's cost.
	// Cap AT a rung's bitrate and the player drops below it, so the useful
	// number is the cap that produces the rendition, not the rendition's cost.
	r := &sweepRun{params: testParams(), phase: phaseClimb}

	// Climbed into 1.0 with the cap at 1.6, then into 2.0 with the cap at 3.2.
	if !r.addRung(1.0, 0.05, 1.6) {
		t.Fatal("first rung rejected")
	}
	if !r.addRung(2.0, 0.05, 3.2) {
		t.Fatal("second rung rejected")
	}
	l := r.ladder()
	if len(l.Rungs) != 2 {
		t.Fatalf("got %d rungs, want 2", len(l.Rungs))
	}

	low, high := l.Rungs[0], l.Rungs[1]
	if low.UpAtMbps != 1.6 || high.UpAtMbps != 3.2 {
		t.Errorf("climb-in caps = %.2f, %.2f; want 1.60, 3.20",
			low.UpAtMbps, high.UpAtMbps)
	}
	// Reaching 2.0 means 1.0 was left behind, and 3.2 is the cap that did it.
	if low.DownAtMbps != 3.2 {
		t.Errorf("lower rung's departure cap = %.2f, want 3.20", low.DownAtMbps)
	}
	if h := low.Headroom(); h < 1.59 || h > 1.61 {
		t.Errorf("headroom = %.2f, want 1.60 (cap over cost)", h)
	}
	if !strings.Contains(l.Note, "before") {
		t.Errorf("ladder note does not report the measured headroom: %q", l.Note)
	}
}

func TestSweepCapAdmitsOnlyOneRungAtATime(t *testing.T) {
	// The no-skip guarantee is structural, not a bet on the player being
	// polite: the cap sits just above the rung the client occupies, so the next
	// rendition fits under it and the one after does not.
	// Seeded with two rungs 1.3x apart, because the two-rung bound is
	// data-driven: it is computed from the TIGHTEST spacing observed, and with
	// nothing observed there is no honest basis for one. Early in a run the
	// sweep accepts that exposure rather than inventing a bound that would
	// block wide ladders -- a default lid stalls a client whose next rendition
	// is 2x away, which is worse than the risk it avoids.
	r := &sweepRun{params: testParams(), phase: phaseClimb, current: 4, capMbps: 4.2}
	// Three trusted gaps, because aiming does not begin until the run has that
	// many: two samples of a spacing that ranges 1.30x to 1.87x can both sit at
	// one end and mislead. Below that it creeps instead, which is a different
	// code path and not what this test is about.
	r.rungs = []mapped{
		{mbps: 4 / 1.3 / 1.3 / 1.3, upAt: 4 / 1.3 / 1.3 / 1.3 * playerDemand},
		{mbps: 4 / 1.3 / 1.3, upAt: 4 / 1.3 / 1.3 * playerDemand},
		{mbps: 4 / 1.3, upAt: 4 / 1.3 * playerDemand},
		{mbps: 4, upAt: 4 * playerDemand},
	}
	next := r.nextCap()
	// Real ladders are spaced at least ~1.3x, so the rung above 4 sits at 5.2 or
	// higher and the one above that at 6.8 or higher. The player keeps ~5%
	// headroom of its own, which is what makes this window narrow.
	if next/playerDemand < 4*1.3 {
		t.Errorf("cap %.2f cannot admit the next rung at 5.2: the client wants "+
			"%.2fx a rendition's cost before taking it", next, playerDemand)
	}
	if next/playerDemand >= 4*1.3*1.3 {
		t.Errorf("cap %.2f admits two rungs at once. The client takes the "+
			"highest it will accept, so that is a skip", next)
	}

	// The client has not climbed and the cap is already above it. Raising
	// further does not help -- the constraint is the client's confidence, not
	// the cap -- so it HOLDS, for a bounded number of levels.
	r.capMbps = next
	if got := r.nextCap(); got != r.capMbps {
		t.Errorf("cap moved to %.2f on the first no-climb; it should hold once "+
			"and give the client another dwell", got)
	}
	// ...then widens: two things explain a client that did not climb -- it
	// needs longer, or the next rung is further off than assumed -- and they
	// cannot be told apart. One dwell covers the first; after that the estimate
	// is the thing more likely to be wrong, so the cap moves.
	got := r.nextCap()
	if got <= r.capMbps {
		t.Errorf("cap did not widen after a wasted hold: %.2f", got)
	}
	// Widening must not overshoot into admitting two rungs.
	if got/playerDemand >= 4*1.3*1.3 {
		t.Errorf("widened cap %.2f admits two rungs at once", got)
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
	if !r.addRung(16.40, 0.45, 26.24) { // contaminated: high drift
		t.Fatal("first rung rejected")
	}
	if r.addRung(14.90, 0.05, 23.84) { // clean: low drift, same rendition
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
	if !r.addRung(3.0, 0.1, 4.80) {
		t.Fatal("first rung rejected")
	}
	if r.addRung(3.1, 0.1, 4.96) {
		t.Error("accepted a rung 100 kbps from an existing one")
	}
	if !r.addRung(3.4, 0.1, 5.44) {
		t.Error("rejected a rung 400 kbps away, which is resolvable")
	}
}

func TestSweepMergeThresholdGrowsWithRate(t *testing.T) {
	// Regression for two real observations against the synthetic fleet: one
	// 9.5 Mbps rendition read as 9.38 and 9.65 from two windows, and one at
	// 5.8 read as 5.57 and 5.87. A flat 250 kbps threshold reported each as two
	// rungs. Median error scales with the rate, so the threshold has to as well.
	r := &sweepRun{params: testParams()}
	if !r.addRung(9.38, 0.1, 15.01) {
		t.Fatal("first rung rejected")
	}
	if r.addRung(9.65, 0.1, 15.44) {
		t.Error("9.38 and 9.65 recorded as separate rungs; that is one rendition")
	}
	r2 := &sweepRun{params: testParams()}
	if !r2.addRung(5.57, 0.1, 8.91) {
		t.Fatal("first rung rejected")
	}
	if r2.addRung(5.87, 0.1, 9.39) {
		t.Error("5.57 and 5.87 recorded as separate rungs; that is one rendition")
	}
	// The tolerance is RELATIVE. It used to take max(MinStepMbps, pct), which
	// conflated cap-step size with rung resolution and swamped the percentage at
	// low rates.
	if got, want := r.mergeWithin(1.0), 0.10; math.Abs(got-want) > 1e-9 {
		t.Errorf("mergeWithin(1.0) = %.3f, want %.2f (10%%)", got, want)
	}

	// Regression for the rung this cost. Measured on hardware: a real 234p rung
	// at 0.28 and a real 360p rung at 0.51 -- two variants 1.9x apart -- sat
	// 0.23 apart in absolute terms, under the old 0.25 floor, and were merged
	// into one. The bottom of the ladder vanished from the saved result.
	low := &sweepRun{params: testParams()}
	if !low.addRung(0.28, 0.05, 0.45) {
		t.Fatal("first rung rejected")
	}
	if !low.addRung(0.51, 0.05, 0.82) {
		t.Error("0.28 and 0.51 merged; those are 234p and 360p, 1.9x apart")
	}
	// The margin that makes the tolerance safe: real ladders are never spaced
	// tighter than about 25%, so a genuine neighbouring rung must survive.
	if !r.addRung(12.0, 0.1, 19.20) {
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

func TestStarvedLevelsCalibrateTheThrottle(t *testing.T) {
	// A starved client is measuring the shaper and nothing else: it fetches
	// back to back, so the delivered rate is decided entirely by the cap. Every
	// climb passes through at least one such level and used to discard it.
	//
	// Here the shaper under-delivers by 6%, the way a real one does once
	// framing is counted, and that has to reach the ladder rather than be
	// silently folded into the rungs.
	const shaperRatio = 0.94
	r := &sweepRun{
		params: testParams(), phase: phaseClimb,
		level: 1, capMbps: 0.5, pinned: true, state: "running",
	}
	var samples []Sample
	for i := 0; i < 40; i++ {
		samples = append(samples, Sample{Down: 0.5 * shaperRatio})
	}
	r.evaluate(samples, time.Unix(1700000000, 0))

	if len(r.throttle) != 1 {
		t.Fatalf("got %d throttle points, want 1", len(r.throttle))
	}
	got := r.throttle[0]
	if math.Abs(got.Ratio-shaperRatio) > 0.005 {
		t.Errorf("ratio = %.3f, want %.3f", got.Ratio, shaperRatio)
	}
	if got.CapMbps != 0.5 {
		t.Errorf("cap = %.2f, want 0.50", got.CapMbps)
	}
	if got.Variation > 0.01 {
		t.Errorf("variation = %.3f on a flat window, want ~0", got.Variation)
	}
	if len(r.rungs) != 0 {
		t.Errorf("recorded %v as rungs; a starved level is not a rendition", r.rungs)
	}
}

func TestVariationIsTheJitterARungInherits(t *testing.T) {
	flat := make([]Sample, 20)
	for i := range flat {
		flat[i] = Sample{Down: 4}
	}
	if v := variation(flat); v > 0.001 {
		t.Errorf("flat window varied by %.3f", v)
	}
	noisy := make([]Sample, 20)
	for i := range noisy {
		noisy[i] = Sample{Down: 4}
		if i%2 == 0 {
			noisy[i] = Sample{Down: 6}
		}
	}
	// +/-20% around a mean of 5.
	if v := variation(noisy); math.Abs(v-0.2) > 0.01 {
		t.Errorf("variation = %.3f, want ~0.20", v)
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
