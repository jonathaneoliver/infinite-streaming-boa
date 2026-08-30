package pifi

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sweeper discovers a client's ABR ladder by measurement rather than by
// assumption: it steps the device's downlink cap downward and records where
// throughput SETTLES at each level.
//
// # Why this works without reading a manifest
//
// A player in steady state fetches one segment of media per segment duration,
// so its mean throughput approximates the bitrate of the rendition it is
// actually delivering. Force it off a rendition by capping below that rate and
// it drops to the next one down and stops saturating -- throughput falls off
// the cap onto a plateau, and that plateau IS the lower rung, in absolute Mbps.
// Repeat and the ladder falls out.
//
// Nothing here inspects a payload. It is arithmetic over the per-client
// throughput series the engine already records once a second.
//
// # What it measures, and what it does not
//
// The result is the EFFECTIVE ladder for one device streaming one service: a
// rendition the player never selects -- wrong codec, wrong viewport, skipped by
// its own logic -- never appears, because it is never delivered. Two devices
// can therefore produce different ladders from identical content. That is
// usually the more useful object for testing, but it is not the manifest's
// ladder and must not be labelled as one.
//
// It is also why a ladder is keyed by SERVICE as well as by device. Netflix,
// YouTube and a self-hosted stream share nothing -- not their rungs, not their
// segment durations, not their adaptation logic -- so a device holds one ladder
// per service it has been swept against, never a single ladder of its own.
//
// The service name is typed by the operator rather than inferred. SNI is
// readable today but ECH is rolling out and QUIC buries the handshake; DNS
// snooping fails the moment a player uses DoH, and fails silently. Both would
// decay into confidently mislabelling a ladder. ntopng is already on the box if
// the question is "what was this device actually talking to".
//
// # Why only one sweep at a time
//
// Wi-Fi airtime is shared (PRD 7), so a second device streaming hard during a
// sweep contends for the same radio and the plateaus read low. pifi cannot stop
// the rest of the world contending, but it can decline to contend with itself.
type Sweeper struct {
	mu  sync.Mutex
	run *sweepRun
	// gen distinguishes successive runs, so a tick that read one run cannot
	// write its conclusion into the next.
	gen uint64
}

// SweepObserver is what a sweep needs from the rest of the daemon: the recent
// throughput of a client, and whether that client is still there to measure.
//
// An interface rather than a direct History reference so the state machine can
// be tested against synthetic plateaus without a kernel, a radio or real time.
type SweepObserver interface {
	// Window returns the samples recorded for mac in [from, to].
	Window(mac string, from, to time.Time) []Sample
	// Live reports whether the client is still present and shapeable. A device
	// that left the network cannot be measured, and continuing would record
	// its absence as a rung.
	Live(mac string) bool
}

// SweepParams control the sweep. Defaults come from DefaultSweepParams; the
// API validates any override.
type SweepParams struct {
	// StartMbps is the cap the climb begins from: low enough that the player
	// falls to its bottom rendition.
	StartMbps float64 `json:"start_mbps"`
	// ClimbPct is how far the cap is raised per level, as a percentage of the
	// rung the client currently sits on (or of the current cap, while no new
	// rung has appeared).
	//
	// This is the whole no-skip guarantee, so it matters more than it looks. A
	// cap only a little above the current rung admits the NEXT rendition and
	// nothing beyond it, so the player cannot climb two at once however
	// aggressive its policy is. Real ladders are spaced at least ~1.3x, so 35%
	// clears the next rung while staying well under 1.3^2 = 1.69.
	ClimbPct float64 `json:"climb_pct"`
	// DwellSec is how long to hold a raised cap before concluding the client is
	// not going to climb into it.
	//
	// Fixed, unlike the descent's settle. A downshift announces itself -- the
	// client is pinned to the cap and then falls away from it -- but a climb has
	// no forcing signal: a player that has not begun climbing looks exactly like
	// one that never will. So there is nothing to wait FOR, only a period to
	// wait THROUGH, and it must exceed the confidence interval the player wants
	// before it will step up.
	DwellSec int `json:"dwell_sec"`
	// ObserveSec is the window a rung is measured over, after the dwell.
	ObserveSec int `json:"observe_sec"`
	// RecoverSec is the extra dwell granted to the first capped level, where
	// the client has just been dropped from unlimited to StartMbps and has to
	// drain and refill before it behaves representatively.
	RecoverSec int `json:"recover_sec"`

	// NewRungPct is how much the rate must rise over the current rung to count
	// as having climbed rather than as measurement noise. Repeated windows on
	// one rendition were measured 4.6% apart, so this sits well clear of that.
	NewRungPct float64 `json:"new_rung_pct"`
	// SkipRatio flags a possible skipped rendition: a new rung more than this
	// multiple of the previous one.
	//
	// It can only ever be a hint. In a measured ladder the natural spacing ran
	// 1.30x to 1.84x, while a single skip produces a gap from 1.30^2 = 1.69 --
	// the two ranges OVERLAP, so no multiplier can prove a skip. It marks a gap
	// as worth a closer look; it never asserts a rung exists.
	SkipRatio float64 `json:"skip_ratio"`

	// MinStepMbps floors the per-level cap change so the climb always
	// progresses, and floors rung resolution.
	MinStepMbps float64 `json:"min_step_mbps"`
	// MergePct is the rung-merge tolerance as a percentage of the rate, applied
	// above the MinStepMbps floor.
	MergePct float64 `json:"merge_pct"`
	// MaxLevels bounds the run regardless of arithmetic.
	MaxLevels int `json:"max_levels"`
}

// DefaultSweepParams measures the ceiling unconditioned, then climbs from a low
// cap, one rendition at a time.
//
// # Why it climbs
//
// The first version descended, stepping the cap below each rung the player
// demonstrated. It recovered barely half the ladder, because DOWNSHIFTS SKIP:
// measured against known content, an iPhone dropped two renditions at a time,
// missing 3200x1800, 1696x954 and 1056x594 while landing squarely on the ones
// between. That is not a defect in the search -- it is what a player does when
// it loses bandwidth, protecting its buffer by dropping hard.
//
// Upshifts are the opposite: a player climbs conservatively, one rung at a
// time, because stepping up costs it buffer and it wants confidence first. So
// the ladder is enumerated on the way UP.
//
// # Why it cannot skip
//
// Not by trusting that politeness. The cap is held just above the rung the
// client currently occupies, so the next rendition fits underneath it and the
// one after does not. Whatever the player's policy, there is only one rung it
// can climb to. Skipping is removed as an opportunity rather than assumed away.
//
// # Why the unconditioned probe survives
//
// It is not part of the search. It is reconnaissance: it confirms playback is
// running before the device is starved for a quarter of an hour, and it gives
// the climb a TERMINATION TARGET. Without it, "the rate stopped rising" is
// ambiguous -- reached the top rung, or the player merely stopped gaining
// confidence? With it, the climb is done when it arrives at a number already
// measured.
func DefaultSweepParams() SweepParams {
	return SweepParams{
		StartMbps: 0.3,
		ClimbPct:  35, DwellSec: 75, ObserveSec: 60, RecoverSec: 60,
		NewRungPct: 12, SkipRatio: 1.9,
		MinStepMbps: 0.25, MergePct: rungMergeDefault, MaxLevels: 40,
	}
}

const (
	// saturatedFrac is the share of the cap above which a plateau is read as
	// "still saturated" rather than as a rung.
	//
	// A configured cap delivers at about -4.5% (PRD 8), so a saturated client
	// sits near 0.955 of its cap; 0.85 leaves room for that plus the jitter of
	// a median over a short window. The cost of the gap is a genuine rung
	// sitting within 15% of the cap being read as saturation -- it is found a
	// level or two later instead, once the cap has moved further below it.
	saturatedFrac = 0.85

	// rungMergeDefault is the default rung-merge tolerance, as a percentage of
	// the rate. It sits above the MinStepMbps floor, because the error in a
	// window's median scales with the rate while a fixed step does not: one
	// rendition observed in two windows lands on two slightly different numbers.
	//
	// Measured against a synthetic fleet with +/-10% VBR and ~25 samples a
	// window, one rendition's two observations differed by 2.9% (9.38 vs 9.65)
	// and by 5.4% (5.57 vs 5.87) of the rate. 10% clears both.
	//
	// The safety margin is on the other side: a real rendition ladder is never
	// spaced tighter than about 25%, and usually 40-100%, so a 10% tolerance
	// cannot swallow a genuine rung. That gap -- 10% against 25% -- is the whole
	// justification for the number. It is a starting point measured against
	// synthetic content; real content is the test.
	rungMergeDefault = 10.0

	// silentMbps is the line below which a client is taken to have stopped
	// sending rather than to be starving.
	//
	// It must NOT be FloorMbps. That is the floor on the CAP -- a different
	// quantity -- and at the bottom of a descent a rebuffering player delivers
	// just under its cap, so comparing against it reported a player pinned at
	// the lowest cap as one that had stopped playing. 50 kbps is below any
	// rendition, including an audio-only fallback, and comfortably above the
	// noise of an idle counter.
	silentMbps = 0.05

	// idleSampleFrac is the share of a window's mean below which a sample counts
	// as an idle gap between segment fetches rather than part of one.
	idleSampleFrac = 0.25
	// starvedDuty is the duty cycle above which a client is taken to be PINNED
	// to its cap rather than playing a rendition that sits close under it.
	//
	// A ratio against the cap cannot do this during a climb: the cap is held
	// just above the current rung, so a legitimate new rung lands at ~96% of it
	// -- the same place a starved client sits. The physical difference is the
	// fetching pattern: a client that cannot sustain any rendition fetches back
	// to back, while one that is comfortable idles between segments.
	//
	// Counting near-zero samples was tried first and fails at high duty: at 91%
	// busy the idle time is shorter than the 1 Hz sampling interval, so no
	// sample ever reads low and the window looks perfectly continuous. Duty
	// measured as mean-over-peak has no such quantisation floor.
	starvedDuty = 0.96

	// minIdleFrac is how much of a window must be idle before a client is taken
	// to be in steady state rather than fetching flat out.
	//
	// Measured on real traffic: a player pinned to its cap showed 0% idle, and
	// the same device in steady state showed 18-26%. Anything above zero
	// separates them, so 5% is a generous margin.
	minIdleFrac = 0.05

	// settleSteady is how closely two consecutive windows must agree before a
	// level is called settled. Looser than unstableDrift: this only has to
	// establish that the switch is over, not that the window is good enough to
	// publish a rung from.
	settleSteady = 0.30

	// unstableDrift flags a window whose two halves disagree by more than this
	// share of its mean -- the rate was still moving, so the window is not
	// describing one steady rendition. Usually a player still hunting, or a
	// buffer state that changed mid-window. Reported rather than discarded: the
	// operator can see which rung to distrust.
	//
	// 20% is well above the drift two halves of a steady bursty fetch show
	// (each half averages over its own bursts and idles) and well below a real
	// rendition change.
	unstableDrift = 0.20
)

// SweepLevel is the outcome of one cap level, kept so the operator can see how
// each rung was arrived at rather than being handed a bare list of numbers.
type SweepLevel struct {
	Level int `json:"level"`
	// CapMbps is the cap held during this level. 0 is the opening
	// unconditioned probe that establishes the ceiling.
	CapMbps float64 `json:"cap_mbps"`
	// RateMbps is the observed plateau: the MEAN over the window, which is the
	// delivered rendition. See plateau() for why a mean and not a median.
	RateMbps float64 `json:"rate_mbps"`
	// Drift is how far the window's two halves disagreed, over its mean: how
	// steady the rate was, which burstiness does not disturb.
	Drift float64 `json:"drift"`
	// Saturated means throughput stayed up against the cap, so the client did
	// not reveal a rung at this level.
	Saturated bool `json:"saturated"`
	// NewRung means this level's plateau was accepted as a rung not already
	// known.
	NewRung bool `json:"new_rung"`
	Samples int  `json:"samples"`
	// SuspectSkip marks a jump wide enough that a rendition may sit inside it.
	// A hint only: natural ladder spacing and single-skip gaps overlap.
	SuspectSkip bool `json:"suspect_skip,omitempty"`
}

// mapped is one discovered rung, kept with the quality of the observation that
// produced it.
//
// The drift matters at merge time. A rung is usually seen more than once, and
// the FIRST sighting is systematically the worst one -- it is the level where
// the player was mid-transition. Keeping the first and discarding the rest
// stored the contaminated value and then anchored every later merge decision on
// it, which on a real iPhone split one 14.3 Mbps rendition into "16.40" and
// "14.20". The lowest-drift sighting wins instead.
type mapped struct {
	mbps  float64
	drift float64
}

type sweepRun struct {
	mac string
	// service is what the device was streaming, typed by the operator. It is
	// half the key the resulting ladder is stored under.
	service string
	params  SweepParams
	started time.Time
	gen     uint64

	// phase is phaseCeiling for the opening unconditioned probe, then
	// phaseClimb for the ascent.
	phase string
	level int
	// capMbps is the cap in force. 0 is unconditioned.
	capMbps float64
	// observing is false while dwelling.
	observing bool
	phaseAt   time.Time
	// capAt is when the current cap was applied.
	capAt time.Time

	// ceiling is the unconditioned rate: the climb's termination target.
	ceiling float64
	// ceilingLinkLimited records that the opening level never reached steady
	// state, so the ceiling may be the link rather than the top rendition.
	ceilingLinkLimited bool
	// current is the rung the client is sitting on right now.
	current float64
	rungs   []mapped
	levels  []SweepLevel

	// pinned records that this level timed out still fetching continuously.
	pinned bool

	state  string // "running", "done", "failed"
	reason string
	stored bool
}

// Sweep phases.
const (
	phaseCeiling = "ceiling" // unconditioned reconnaissance
	phaseClimb   = "climb"   // the ascent, one rendition at a time
)

// medianRatio is the typical spacing between the rungs found so far, used to
// predict where the next one sits. Before there is data, a mid-range ladder
// spacing is assumed.
func (r *sweepRun) medianRatio() float64 {
	if len(r.rungs) < 2 {
		return 1.4
	}
	var ratios []float64
	for i := 1; i < len(r.rungs); i++ {
		lo, hi := r.rungs[i-1].mbps, r.rungs[i].mbps
		if lo > 0 && hi > lo {
			ratios = append(ratios, hi/lo)
		}
	}
	if len(ratios) == 0 {
		return 1.4
	}
	sort.Float64s(ratios)
	return ratios[len(ratios)/2]
}

// ready reports whether the client is in steady state and can be measured.
//
// The signal is the duty cycle, and it means one thing: IS THE BUFFER FULL. A
// player only idles between fetches once it has enough media banked; until then
// it pulls flat out, at whatever the link or the cap will give it, and the rate
// it shows has nothing to do with the rendition it is playing.
//
// Fetching continuously is therefore ambiguous, and deliberately not resolved
// here. It can mean:
//
//   - the buffer is still filling, after a cap change or at startup
//   - the client is starved: no rendition fits under this cap at all
//   - the LINK is the constraint rather than the player (unconditioned only)
//
// The first ends on its own; the others do not. Time is the only thing that
// separates them, so this returns "not yet" for all three and the caller
// decides on timeout. Measured live, an unconditioned window read 29.31 Mbps
// against content whose nearest variants were 32.7 and 23.1, purely because the
// buffer was still filling.
func (r *sweepRun) ready(samples []Sample, now time.Time) (bool, string) {
	if now.Sub(r.capAt) < time.Duration(r.dwellSec())*time.Second {
		return false, "minimum dwell"
	}
	half := r.params.ObserveSec / 4
	if half < 4 {
		half = 4
	}
	if len(samples) < 2*half {
		return false, "not enough samples yet"
	}
	recent := samples[len(samples)-2*half:]
	if dutyCycle(recent) > starvedDuty {
		return false, "fetching continuously: buffer still filling, or nothing fits"
	}
	m := meanDown(recent)
	if m <= 0 {
		return true, ""
	}
	if math.Abs(meanDown(recent[:half])-meanDown(recent[half:]))/m > settleSteady {
		return false, "rate still moving"
	}
	return true, ""
}

// StartSweep begins a ladder sweep on one device.
//
// The opening level is deliberately UNCONDITIONED: the ceiling has to be
// measured before there is anything to step down from, and the same
// steady-state argument that makes each plateau a rung makes the unconditioned
// plateau the top rung.
func (s *Sweeper) Start(mac, service string, p SweepParams, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil && s.run.state == "running" {
		if s.run.mac == mac {
			return fmt.Errorf("a sweep is already running on this device")
		}
		return fmt.Errorf("a sweep is already running on %s; "+
			"Wi-Fi airtime is shared, so two at once would measure each other", s.run.mac)
	}
	s.gen++
	s.run = &sweepRun{
		mac: mac, service: service, params: p, started: now, gen: s.gen,
		phase: phaseCeiling, capMbps: 0, observing: false,
		phaseAt: now, capAt: now,
		state: "running",
	}
	fmt.Printf("infinite-streaming-pifi: sweep %s (%s): level 0, unconditioned, "+
		"measuring the ceiling to climb towards\n", mac, service)
	return nil
}

// Stop ends a running sweep. The override disappears with it, so the device
// returns to stored policy on the next tick without anything being written.
func (s *Sweeper) Stop(mac string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || s.run.state != "running" || s.run.mac != mac {
		return fmt.Errorf("no sweep is running on this device")
	}
	s.run.state = "failed"
	s.run.reason = "stopped by the operator"
	return nil
}

// Override returns the downlink shape a sweep is imposing on a device, if any.
//
// The shape is CLEAN apart from the cap: the operator's delay, jitter and loss
// are dropped for the duration. A ladder measured through 5% loss would not be
// a ladder -- the player's rendition choice would be reacting to the impairment
// rather than to the rate, and every plateau would read low.
//
// Held in memory only, never written to the Store, which persists operator
// intent alone. A daemon restart mid-sweep therefore restores the device to
// stored policy by simply forgetting, rather than by having to unwind anything.
func (s *Sweeper) Override(mac string) (Shape, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || s.run.state != "running" || s.run.mac != mac {
		return Shape{}, false
	}
	return Shape{RateMbps: s.run.capMbps}, true
}

// Advance drives the state machine one tick. It is called from the engine's
// tick with the client list already built, so presence is current.
//
// The observer is called with NO lock held. It reaches into another subsystem
// that has its own mutex, and holding one lock across a call into another is
// how a deadlock gets designed in rather than discovered. The run is re-checked
// by generation afterwards, so a sweep stopped or restarted from an HTTP
// handler mid-call cannot be written to by the tick that overlapped it.
func (s *Sweeper) Advance(now time.Time, obs SweepObserver) {
	s.mu.Lock()
	r := s.run
	if r == nil || r.state != "running" {
		s.mu.Unlock()
		return
	}
	mac, gen := r.mac, r.gen
	dwelling, from := !r.observing, r.phaseAt
	observeFor := time.Duration(r.params.ObserveSec) * time.Second
	elapsed := now.Sub(r.phaseAt)
	s.mu.Unlock()

	if !obs.Live(mac) {
		s.withRun(gen, func(r *sweepRun) {
			r.fail("device left the network before the sweep finished")
		})
		return
	}
	if dwelling {
		samples := obs.Window(mac, from, now)
		s.withRun(gen, func(r *sweepRun) { r.advanceDwell(samples, now) })
		return
	}
	if elapsed < observeFor {
		return
	}
	samples := obs.Window(mac, from, now)
	s.withRun(gen, func(r *sweepRun) { r.evaluate(samples, now) })
}

// dwellSec is how long to hold the current cap before measuring. The first
// capped level gets extra: the client has just been dropped from unlimited to
// the starting cap and has to drain and refill before it is representative.
// maxDwellSec bounds the wait for a buffer that may never fill.
//
// Generous, because refilling under a tight cap is slow. A player on rendition
// R with the cap at 1.35R refills at only 0.35R of surplus, so twenty seconds
// of drained buffer takes nearly a minute to restore. That is the price of a
// cap tight enough to make skipping impossible.
func (r *sweepRun) maxDwellSec() int {
	return r.dwellSec() + r.params.RecoverSec
}

func (r *sweepRun) dwellSec() int {
	if r.phase == phaseClimb && r.level == 1 {
		return r.params.DwellSec + r.params.RecoverSec
	}
	return r.params.DwellSec
}

// advanceDwell moves a level on to measurement once the client is in steady
// state, or gives up waiting and records why.
func (r *sweepRun) advanceDwell(samples []Sample, now time.Time) {
	ok, why := r.ready(samples, now)
	if ok {
		r.observing, r.phaseAt = true, now
		return
	}
	if now.Sub(r.capAt) < time.Duration(r.maxDwellSec())*time.Second {
		return
	}
	// Out of patience. Whatever it was doing, it was not filling a buffer that
	// was ever going to fill.
	if strings.HasPrefix(why, "fetching continuously") {
		r.pinned = true
		if r.phase == phaseCeiling {
			r.ceilingLinkLimited = true
			fmt.Printf("infinite-streaming-pifi: sweep %s: WARNING: client never "+
				"stopped fetching continuously unconditioned -- the link, not the "+
				"player, may be setting this ceiling\n", r.mac)
		}
	}
	r.observing, r.phaseAt = true, now
}

// withRun applies fn to the active run only if it is still the same running
// sweep that Advance read at the start of this tick.
func (s *Sweeper) withRun(gen uint64, fn func(*sweepRun)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run == nil || s.run.gen != gen || s.run.state != "running" {
		return
	}
	fn(s.run)
}

// evaluate closes one level: decide what the measured rate means, then move on.
func (r *sweepRun) evaluate(samples []Sample, now time.Time) {
	rate, drift, n := plateau(samples)

	lv := SweepLevel{
		Level: r.level, CapMbps: r.capMbps,
		RateMbps: rate, Drift: drift, Samples: n,
	}

	// Half the expected samples is the floor for believing a window at all.
	// Telemetry is 1 Hz, so far fewer means the series has gaps and the mean
	// describes nothing.
	if n < r.params.ObserveSec/2 {
		r.levels = append(r.levels, lv)
		r.fail(fmt.Sprintf("only %d telemetry samples in a %ds window; "+
			"cannot trust the plateau", n, r.params.ObserveSec))
		return
	}

	// Nothing flowing. A starved player still consumes its whole cap, so
	// near-zero is not a struggling player -- it is a stopped one.
	if rate < silentMbps {
		r.levels = append(r.levels, lv)
		if r.phase == phaseCeiling {
			r.fail("no traffic to measure: start playback on the device, then sweep")
			return
		}
		r.finish("client stopped sending; playback ended, paused, or gave up under the cap")
		return
	}

	if r.phase == phaseCeiling {
		r.ceiling = rate
		r.levels = append(r.levels, lv)
		fmt.Printf("infinite-streaming-pifi: sweep %s: ceiling %.2f Mbps%s -- "+
			"dropping to %.2f Mbps and climbing\n",
			r.mac, rate, driftFlag(drift), r.params.StartMbps)
		r.phase = phaseClimb
		r.setCap(r.params.StartMbps, now)
		r.pinned = false
		return
	}

	// Never stopped fetching continuously, even after the longest dwell. Its
	// buffer was not filling, so this rate is the cap talking rather than a
	// rendition. Record the level, raise the cap, and try again.
	if r.capMbps > 0 && r.pinned {
		lv.Saturated = true
		r.levels = append(r.levels, lv)
		fmt.Printf("infinite-streaming-pifi: sweep %s: level %d, cap %.2f Mbps: "+
			"starved at %.2f Mbps, no rendition fits under this cap\n",
			r.mac, lv.Level, lv.CapMbps, rate)
		r.nextLevel(now)
		return
	}

	// Climbing. A rung is new when the rate has risen meaningfully above the
	// one the client was already sitting on; anything less is the same
	// rendition measured again, since repeated windows on one rendition were
	// observed to differ by a few percent.
	climbed := r.current <= 0 || rate > r.current*(1+r.params.NewRungPct/100)
	if climbed {
		lv.NewRung = r.addRung(rate, drift)
		prev := r.current
		r.current = rate
		if prev > 0 && rate > prev*r.params.SkipRatio {
			// A hint, never a verdict. Natural ladder spacing and single-skip
			// gaps overlap, so this cannot prove anything -- it marks a gap
			// worth a closer look.
			lv.SuspectSkip = true
			fmt.Printf("infinite-streaming-pifi: sweep %s: level %d: %.2f -> %.2f Mbps "+
				"is a %.2fx jump; a rendition may sit between them\n",
				r.mac, r.level, prev, rate, rate/prev)
		}
		fmt.Printf("infinite-streaming-pifi: sweep %s: level %d, cap %.2f Mbps: "+
			"rung at %.2f Mbps%s\n", r.mac, lv.Level, lv.CapMbps, rate, driftFlag(drift))
	} else {
		fmt.Printf("infinite-streaming-pifi: sweep %s: level %d, cap %.2f Mbps: "+
			"still %.2f Mbps, no higher rendition fits yet%s\n",
			r.mac, lv.Level, lv.CapMbps, rate, driftFlag(drift))
	}
	r.levels = append(r.levels, lv)
	r.nextLevel(now)
}

func driftFlag(drift float64) string {
	if drift > unstableDrift {
		return " (unstable)"
	}
	return ""
}

// nextLevel raises the cap for the next rung, or finishes.
func (r *sweepRun) nextLevel(now time.Time) {
	if r.level+1 >= r.params.MaxLevels {
		r.finish(fmt.Sprintf("reached the %d-level ceiling", r.params.MaxLevels))
		return
	}
	// Arrived. The climb is done when it reaches the rate measured
	// unconditioned -- which is why that probe is worth its two minutes: without
	// a target, "the rate stopped rising" cannot be told from "the player
	// stopped gaining confidence".
	if r.ceiling > 0 && r.current >= r.ceiling*(1-r.params.NewRungPct/100) {
		r.finish(fmt.Sprintf("climbed to the %.2f Mbps ceiling", r.ceiling))
		return
	}
	next := r.nextCap()
	// Once the cap is comfortably past the ceiling there is nothing left above
	// to admit, so the client is at its top rendition whatever the rate says.
	if r.ceiling > 0 && next > r.ceiling*1.5 {
		r.finish("cap raised well past the ceiling with no further rendition")
		return
	}
	r.setCap(next, now)
}

func (r *sweepRun) setCap(cap float64, now time.Time) {
	r.level++
	r.capMbps = cap
	r.observing = false
	r.pinned = false
	r.phaseAt, r.capAt = now, now
}

// nextCap raises the cap just enough to admit ONE more rendition.
//
// This is where the no-skip guarantee lives, and it does not rest on the player
// being well behaved. The cap sits a little above the rung the client currently
// occupies, so the next rendition fits underneath it and the one after does not
// -- there is only one rung available to climb to, whatever the policy.
//
// Real ladders are spaced at least ~1.3x, so a 35% raise clears the next rung
// while staying under 1.3^2 = 1.69. When nothing climbed, the cap is raised
// again from ITSELF rather than from the rung, so the search creeps upward
// until the next rendition comes within reach.
func (r *sweepRun) nextCap() float64 {
	base := r.capMbps
	if r.current > 0 && r.current > base {
		base = r.current
	}
	next := base * (1 + r.params.ClimbPct/100)
	if next < r.capMbps+r.params.MinStepMbps {
		next = r.capMbps + r.params.MinStepMbps
	}
	return next
}

// addRung records a plateau, merging it into an existing rung when the two are
// closer than the sweep can resolve. Below that separation it cannot tell two
// renditions apart from one rendition measured twice, so reporting both would
// be inventing resolution it does not have.
//
// On a merge the LOWER-DRIFT sighting wins. Keeping the first instead stored
// the contaminated value -- the first sighting of a rung is the level where the
// player was mid-transition -- and then anchored every later merge on it, which
// split one real 14.3 Mbps rendition into "16.40" and "14.20" on hardware.
func (r *sweepRun) addRung(mbps, drift float64) bool {
	for i, existing := range r.rungs {
		if math.Abs(existing.mbps-mbps) >= r.mergeWithin(existing.mbps) {
			continue
		}
		if drift < existing.drift {
			r.rungs[i] = mapped{mbps: mbps, drift: drift}
		}
		return false
	}
	r.rungs = append(r.rungs, mapped{mbps: mbps, drift: drift})
	return true
}

func (r *sweepRun) mergeWithin(mbps float64) float64 {
	pct := r.params.MergePct
	if pct <= 0 {
		pct = rungMergeDefault
	}
	return math.Max(r.params.MinStepMbps, mbps*pct/100)
}

func (r *sweepRun) finish(reason string) {
	r.state, r.reason = "done", reason
	fmt.Printf("infinite-streaming-pifi: sweep %s: done, %d rungs (%s)\n",
		r.mac, len(r.rungs), reason)
}

func (r *sweepRun) fail(reason string) {
	r.state, r.reason = "failed", reason
	fmt.Printf("infinite-streaming-pifi: sweep %s: failed: %s\n", r.mac, reason)
}

// plateau reduces an observation window to a rate, a drift measure, and a
// sample count.
//
// # Why the MEAN, and not the median
//
// This started as a median, to be robust against VBR. That was defending
// against the wrong threat, and real hardware showed it: a player on an
// uncapped link does not deliver a steady rate at all. It fetches a segment at
// line rate and then goes idle, so the 1 Hz series is bimodal -- bursts at
// 20-25 Mbps separated by samples at zero.
//
// A median over a bimodal sample lands on whichever mode holds more than half
// the samples, which is a number the traffic never had. Measured against a real
// iPhone: mean 13.52 Mbps, median 16.75 Mbps, 18% of samples at idle. The mean
// is the delivered rendition; the median is an artefact of the duty cycle.
//
// The mean is also what the underlying claim actually says. "A player in steady
// state fetches one segment of media per segment duration" is a statement about
// BYTES OVER TIME, and bytes over time is the mean. VBR is handled by making
// the window span several segments, not by choosing a different statistic.
//
// The synthetic fleet never caught this because it emits continuous throughput
// with no idle gaps. A model that cannot go idle cannot reproduce a duty cycle.
func plateau(samples []Sample) (rate, drift float64, n int) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	rate = meanDown(samples)
	if rate <= 0 {
		return rate, 0, len(samples)
	}
	return rate, halfDrift(samples, rate), len(samples)
}

// idleFraction is the share of samples that are gaps between fetches rather
// than part of one. A player fetching continuously has none.
func idleFraction(samples []Sample) float64 {
	m := meanDown(samples)
	if m <= 0 {
		return 1 // nothing flowing at all is entirely idle
	}
	idle := 0
	for _, s := range samples {
		if s.Down < m*idleSampleFrac {
			idle++
		}
	}
	return float64(idle) / float64(len(samples))
}

// dutyCycle is the share of a window spent actually fetching: the mean over the
// rate seen during bursts.
//
// A high percentile rather than the maximum, so one VBR spike cannot deflate
// the result. Pinned to a cap gives ~1; a rendition with room to spare gives
// its rate over the cap.
func dutyCycle(samples []Sample) float64 {
	if len(samples) == 0 {
		return 1
	}
	v := make([]float64, len(samples))
	for i, s := range samples {
		v[i] = s.Down
	}
	sort.Float64s(v)
	peak := v[(len(v)*9)/10]
	if peak <= 0 {
		return 1
	}
	return meanDown(samples) / peak
}

func meanDown(samples []Sample) float64 {
	var sum float64
	for _, s := range samples {
		sum += s.Down
	}
	return sum / float64(len(samples))
}

// halfDrift measures how far the first half of a window differs from the
// second, relative to the whole window's mean.
//
// This replaces an interquartile spread, which is meaningless on bursty data:
// a duty-cycled fetch has a huge IQR while sitting rock steady on one
// rendition, so every level would have been flagged unstable on real traffic.
//
// What actually matters is whether the rate is STEADY across the window. A
// player still hunting between renditions, or one whose buffer state changed
// mid-window, shows up as the two halves disagreeing. Bursts do not, because
// each half averages over its own bursts and idles.
func halfDrift(samples []Sample, rate float64) float64 {
	if len(samples) < 4 {
		return 0 // too short to split meaningfully
	}
	mid := len(samples) / 2
	return math.Abs(meanDown(samples[:mid])-meanDown(samples[mid:])) / rate
}

// TakeResult hands a completed sweep's ladder to the caller exactly once, so
// the engine can persist it from the tick loop without re-writing the store
// every second for the rest of the run.
//
// A failed or stopped sweep yields nothing: a partial descent has measured a
// real ceiling and possibly some real rungs, but it cannot say whether the
// ladder continues below where it stopped, and a truncated ladder stored as if
// it were complete is exactly the quiet wrong answer to avoid.
func (s *Sweeper) TakeResult() (string, Ladder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.run
	if r == nil || r.state != "done" || r.stored {
		return "", Ladder{}, false
	}
	r.stored = true
	return r.mac, r.ladder(), true
}

func (r *sweepRun) ladder() Ladder {
	rungs := make([]Rung, 0, len(r.rungs))
	for _, m := range r.rungs {
		unstable := m.drift > unstableDrift
		// A link-limited ceiling is the top rung by construction, and must not
		// render with the same confidence as one the player actually chose.
		if r.ceilingLinkLimited && math.Abs(m.mbps-r.ceiling) < 1e-9 {
			unstable = true
		}
		rungs = append(rungs, Rung{Mbps: round2(m.mbps), Unstable: unstable})
	}
	sort.Slice(rungs, func(i, j int) bool { return rungs[i].Mbps < rungs[j].Mbps })
	note := fmt.Sprintf("climbed from %.2f Mbps in %.0f%% steps towards a %.2f Mbps ceiling, %ds windows",
		r.params.StartMbps, r.params.ClimbPct, r.ceiling, r.params.ObserveSec)
	for _, lv := range r.levels {
		if lv.SuspectSkip {
			note += "; at least one gap is wide enough that a rendition may sit inside it"
			break
		}
	}
	if r.ceilingLinkLimited {
		note += "; the top rung is UNRELIABLE -- the client never stopped fetching " +
			"continuously while unconditioned, so the link may have set the ceiling " +
			"rather than the player's own top rendition"
	}
	return Ladder{
		Service:    r.service,
		Rungs:      rungs,
		Provenance: LadderMeasured,
		MeasuredAt: r.started.UnixMilli(),
		Note:       note,
	}
}

// View is the sweep's progress as the UI renders it.
func (s *Sweeper) View(mac string) *SweepView {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.run
	if r == nil || r.mac != mac {
		return nil
	}
	phase := "settling"
	if r.observing {
		phase = "observing"
	}
	if r.state != "running" {
		phase = r.state
	}
	found := make([]Rung, 0, len(r.rungs))
	for _, m := range r.rungs {
		found = append(found, Rung{Mbps: round2(m.mbps), Unstable: m.drift > unstableDrift})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Mbps < found[j].Mbps })
	return &SweepView{
		State: r.state, Phase: phase, Service: r.service,
		Level: r.level, CapMbps: round2(r.capMbps),
		CeilingMbps: round2(r.ceiling),
		Found:       found, Levels: r.levels,
		Reason: r.reason, StartedAt: r.started.UnixMilli(),
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
