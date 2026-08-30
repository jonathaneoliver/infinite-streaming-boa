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
	// aggressive its policy is.
	//
	// The window is narrower than it first appears, because the player keeps
	// headroom of its own -- it will not select a variant costing more than
	// about 95% of what it has. Against the tightest real spacing measured,
	// 1.3x, the cap must therefore exceed 1.3/0.95 = 1.37x the current rung to
	// admit the next one at all, and stay under 1.69/0.95 = 1.78x to exclude the
	// one after. 45% sits in that band with margin at both ends.
	//
	// 35% was tried first and is on the wrong side of it: it yields
	// 1.35 x 0.95 = 1.28, just short of a rung 1.3x up. That went unnoticed
	// because the cap was separately running away, which inflated it enough to
	// work by accident.
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
	// as having climbed rather than as measurement noise.
	//
	// It has to sit in the gap between two things: how much a re-measurement of
	// ONE rendition can vary, and how far apart two real renditions are. VBR
	// moves a window by 15-20% on high-motion content, while the tightest
	// spacing in a real ladder is about 30%. Below the noise, the same rung
	// registers twice at slightly different rates -- observed splitting a 2.5
	// Mbps rendition into 2.32 and 2.69. Above the spacing, a genuine climb is
	// dismissed as noise.
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
		ClimbPct:  45, DwellSec: 75, ObserveSec: 60, RecoverSec: 60,
		NewRungPct: 20, SkipRatio: 1.9,
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

	// mergeFloorMbps stops the merge tolerance collapsing to nothing at very low
	// rates. Well below the spacing of any real ladder -- the tightest measured
	// gap between adjacent variants was 1.3x -- so it never decides anything on
	// its own.
	mergeFloorMbps = 0.02

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
	// pinnedToCap is the share of the cap above which a plateau is the SHAPER
	// rather than a rendition.
	//
	// A player only selects a variant it can serve with headroom, so a rung it
	// actually chose arrives at roughly 0.95 of the cap or less. A client that
	// cannot serve anything takes the whole cap. The gap between those is small
	// but it is real, and it catches a case the duty-cycle test cannot: a player
	// that fetches, fails to start, and retries has gaps in its traffic, so it
	// looks settled while delivering exactly the cap.
	//
	// Observed on hardware: capped at 0.30 Mbps -- 12% above the bottom
	// rendition's wire cost, too tight for playback to start from cold -- the
	// sweep recorded "rung at 0.30 Mbps", which is the cap, not a rendition.
	pinnedToCap = 0.97

	// defaultSpacing and defaultHeadroom are used until the run has measured its
	// own. Both are mid-range values from a real ladder and a real player, not
	// round numbers: spacings ran 1.30 to 1.87, and headroom 1.5 to 1.9.
	defaultSpacing = 1.45
	// defaultHeadroom is only a starting point now that the climb creeps until
	// calibrated, and it is deliberately at the LOW end of what has been seen.
	//
	// 1.6 was used first, taken from a run whose caps were separately running
	// away -- so every cap-over-rung ratio measured there was inflated by the
	// bug rather than telling us anything about the client. Measured against a
	// cap that was not running away, the same player accepted renditions at
	// 1.11x, 1.32x, 1.76x and 1.91x, and declined one at 1.26x. There is no
	// constant here: it moves with buffer state. Erring low costs a dwell;
	// erring high costs a rendition.
	defaultHeadroom = 1.25

	// aimPct is where in the observed spread a cap is aimed. Low on purpose --
	// see lowPct. A quarter of the way up means most gaps are wider than the
	// estimate, so the usual error is a wasted dwell rather than a lost rung.
	aimPct = 0.25

	// creepStep is the cap rise per level before the run has calibrated. Small
	// enough that only one rendition can come within reach at a time, even if
	// the client turns out to accept anything that merely fits.
	creepStep = 1.22

	// stepWiden is how much the spacing estimate grows each time a cap is held
	// without the client climbing. The next rung is evidently further off than
	// assumed, so widen rather than repeat the same guess. Ladder gaps reach
	// 1.87x, so a timid widening just spends levels rediscovering that.
	stepWiden = 1.35

	// capHolds is how many extra dwells a cap is held at when the client has not
	// taken the headroom it already has. Two gives a slow adapter three chances
	// in total before the cap moves, without letting a genuinely-too-low cap
	// stall the run.
	capHolds = 2

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
	// upAt is the cap in force when the client climbed into this rendition;
	// downAt the cap that pushed it out. See Rung.
	upAt   float64
	downAt float64
	// suspect marks a rung reached by a jump wide enough that something was
	// probably stepped over. The GAP below it is then not evidence of this
	// ladder's spacing, and must not be learned from.
	suspect bool
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
	// holds counts consecutive levels the cap has been held at while waiting
	// for the client to take headroom it already has.
	holds int
	// throttle accumulates what the starved levels revealed about the shaper.
	throttle []ThrottlePoint

	state  string // "running", "done", "failed"
	reason string
	stored bool
}

// Sweep phases.
const (
	phaseCeiling = "ceiling" // unconditioned reconnaissance
	phaseClimb   = "climb"   // the ascent, one rendition at a time
)

// headroomFactor is how much more than a rendition's cost this client demands
// before it will select it, learned from the switches already observed.
//
// It is not a small number and it is not optional. Measured on an iPhone across
// ten switches the ratio ran 1.5x to 1.9x, never below 1.5 -- so a cap set at a
// rendition's own bitrate does not hold a player there, it pushes it off.
// Sizing the climb without this was why levels kept passing with no climb: the
// cap admitted the next rung arithmetically and the player declined it.
func (r *sweepRun) headroomFactor() float64 {
	var seen []float64
	// Skip the first rung. The client did not CLIMB into that one -- it fell
	// there when the cap dropped at the start of the run -- so the cap recorded
	// against it is wherever the sweep began, not a threshold the client chose.
	// Counting it dragged the estimate below what the client actually demands.
	for i, m := range r.rungs {
		if i > 0 && m.upAt > 0 && m.mbps > 0 {
			seen = append(seen, m.upAt/m.mbps)
		}
	}
	if len(seen) == 0 {
		return defaultHeadroom
	}
	sort.Float64s(seen)
	h := lowPct(seen, aimPct)
	// Trust the client over the default, but not beyond reason.
	return math.Max(1.2, math.Min(2.5, h))
}

// lowPct is the value below which `frac` of the samples fall, on an already
// sorted slice.
//
// Aiming a cap uses a LOW percentile of what has been seen, not the middle one,
// because the two ways of being wrong do not cost the same. Aim too low and the
// client does not climb: one wasted dwell, recovered on the next level. Aim too
// high and the cap admits two renditions at once, the client takes the higher,
// and a rung is gone -- no later level revisits it, because the climb only goes
// one way.
//
// A median is wrong here for a reason no amount of tuning fixes: by
// construction half the gaps are wider than it, so half of them overshoot. The
// estimate should sit under most of what it has seen and creep up on the rest.
func lowPct(sorted []float64, frac float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(frac * float64(len(sorted)-1))
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

// trustedRatios is the spacing between adjacent rungs, EXCLUDING any gap the
// skip detector flagged.
//
// Without that exclusion the estimator trains on its own errors: a skipped
// rendition doubles an observed gap, the inflated spacing aims the next cap
// further ahead, that causes another skip, and the estimate runs away. Measured
// on hardware -- two skips at the bottom of a ladder pushed the median spacing
// to 2.02 where the real one is about 1.5.
func (r *sweepRun) trustedRatios() []float64 {
	var out []float64
	for i := 1; i < len(r.rungs); i++ {
		if r.rungs[i].suspect {
			continue
		}
		lo, hi := r.rungs[i-1].mbps, r.rungs[i].mbps
		if lo > 0 && hi > lo {
			out = append(out, hi/lo)
		}
	}
	return out
}

// suspectAbove is the gap ratio beyond which a jump is worth flagging as a
// possible skipped rendition.
//
// Relative to what THIS ladder does, not an absolute number. A fixed 1.9x cries
// skip on every gap of a ladder whose genuine spacing is 2x -- and because a
// flagged gap is then excluded from the spacing estimate, that starves the very
// estimate it feeds, the run never calibrates, and it creeps to the top. Seen
// on a synthetic ladder spaced 1.58x to 2.25x: three real gaps flagged, twenty
// levels where ten would do.
//
// So the bar is the widest gap already accepted, with room above it. Until
// there is one, fall back to the configured absolute.
func (r *sweepRun) suspectAbove() float64 {
	widest := 0.0
	for _, x := range r.trustedRatios() {
		if x > widest {
			widest = x
		}
	}
	if widest == 0 {
		return r.params.SkipRatio
	}
	return math.Max(r.params.SkipRatio, widest*1.4)
}

// calibrated reports whether the run has learned enough about THIS ladder to
// aim a cap at the next rung rather than creep towards it.
//
// THREE trusted gaps, not two. With two the median is merely their average, and
// measured spacings on one real ladder ran 1.30x to 1.87x -- two samples can sit
// at one end of that and mislead with complete confidence. Three gives a middle
// value that is an actual observation rather than a blend of the extremes.
//
// The cost is a few more creeping levels before aiming starts. That is the
// cheaper error: creeping loses time, and aiming on a bad estimate loses
// renditions that no later level can recover.
func (r *sweepRun) calibrated() bool {
	return len(r.trustedRatios()) >= 3
}

// minRatio is the TIGHTEST spacing seen so far. The upper bound on a cap comes
// from this rather than the median: admitting two rungs at once causes a skip,
// and the pair most likely to be skipped over is the closest pair.
func (r *sweepRun) minRatio() float64 {
	best := 0.0
	for _, x := range r.trustedRatios() {
		if best == 0 || x < best {
			best = x
		}
	}
	return best // zero when fewer than two rungs are known
}

// spacingEstimate predicts how far above the current rung the next one sits,
// from the gaps already measured on THIS ladder. Before there is data, a
// mid-range ladder spacing is assumed.
//
// Deliberately a low percentile rather than the middle: see lowPct.
func (r *sweepRun) spacingEstimate() float64 {
	if len(r.rungs) < 2 {
		return defaultSpacing
	}
	ratios := r.trustedRatios()
	if len(ratios) == 0 {
		return defaultSpacing
	}
	sort.Float64s(ratios)
	return lowPct(ratios, aimPct)
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
	if r.capMbps > 0 && (r.pinned || rate >= r.capMbps*pinnedToCap) {
		lv.Saturated = true
		r.levels = append(r.levels, lv)
		// Free calibration. Pinned to the cap, the client is measuring the
		// shaper rather than the content, so this reading qualifies every rung
		// the sweep goes on to report.
		cv := variation(samples)
		r.throttle = append(r.throttle, ThrottlePoint{
			CapMbps: round2(r.capMbps), DeliveredMbps: round2(rate),
			Ratio: round3(rate / r.capMbps), Variation: round3(cv),
		})
		fmt.Printf("infinite-streaming-pifi: sweep %s: level %d, cap %.2f Mbps: "+
			"starved at %.2f Mbps (%.1f%% of cap, %.1f%% variation), "+
			"no rendition fits under this cap\n",
			r.mac, lv.Level, lv.CapMbps, rate, 100*rate/r.capMbps, 100*cv)
		r.nextLevel(now)
		return
	}

	// Climbing. A rung is new when the rate has risen meaningfully above the
	// one the client was already sitting on; anything less is the same
	// rendition measured again, since repeated windows on one rendition were
	// observed to differ by a few percent.
	climbed := r.current <= 0 || rate > r.current*(1+r.params.NewRungPct/100)
	if climbed {
		lv.NewRung = r.addRung(rate, drift, r.capMbps)
		prev := r.current
		r.current = rate
		r.holds = 0 // the estimate was good; stop widening it
		if prev > 0 && rate > prev*r.suspectAbove() {
			// A hint, never a verdict. Natural ladder spacing and single-skip
			// gaps overlap, so this cannot prove anything -- it marks a gap
			// worth a closer look.
			lv.SuspectSkip = true
			if n := len(r.rungs); n > 0 {
				r.rungs[n-1].suspect = true
			}
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
	// The stop must sit above the cap the TOP rendition needs, or the sweep
	// terminates before it can reach its own target. The client wants roughly
	// headroom x a rendition's cost before selecting it, so the ceiling rung
	// requires ceiling x headroom -- a flat 1.5x ceiling was below that and made
	// the top rung unreachable by construction.
	if lim := r.ceiling * r.headroomFactor() * 1.3; r.ceiling > 0 && next > lim {
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
// nextCap keeps the cap just above the rung the client currently occupies.
//
// The cap must not run away from the player. It used to raise from
// max(cap, current), so a level where the client did not climb still inflated
// the cap by ClimbPct -- and since a player needs recovery time between
// upshifts, the cap outran it steadily.
//
// Measured: by level 13 the cap was 19.10 while the client sat at 8.04, and the
// run then hit its "cap far past the ceiling" stop with two renditions still
// unclimbed. The top of the ladder was lost, not because the client would not
// climb but because the sweep ran out of budget waiting.
//
// So: catch up when the cap is BEHIND the client, and HOLD when it is already
// ahead. When a client has headroom it has not taken, more headroom is not the
// missing ingredient -- time is. Holding is bounded, so a cap that genuinely is
// too low still rises rather than stalling forever.
func (r *sweepRun) nextCap() float64 {
	if r.current <= 0 {
		return r.raiseFromCap()
	}

	// Until this run has measured the ladder it is on, CREEP rather than aim.
	//
	// Aiming needs two numbers -- how far apart the rungs are, and how much
	// headroom this client wants -- and at the start it has neither, only
	// defaults. Both skips observed on hardware happened in the first two
	// levels, taken on those defaults, before there was anything to learn from.
	//
	// A small step cannot skip: it brings at most one rendition within reach at
	// a time whatever the client's policy turns out to be. It is slower, and
	// that is the right trade -- an undersized step costs a dwell, an oversized
	// one costs a rung, and a lost rung cannot be recovered later in the run.
	if !r.calibrated() {
		next := math.Max(r.capMbps, r.current) * creepStep
		if next < r.capMbps+r.params.MinStepMbps {
			next = r.capMbps + r.params.MinStepMbps
		}
		if next > r.capMbps {
			r.holds = 0
			return next
		}
		if r.holds < capHolds {
			r.holds++
			return r.capMbps
		}
		r.holds = 0
		return r.raiseFromCap()
	}

	// Where the next rung probably sits, and what the client will want before
	// taking it. Both are measured from this run once there is anything to
	// measure, so the sweep gets better at aiming as it goes.
	spacing := r.spacingEstimate() * math.Pow(stepWiden, float64(r.holds))
	head := r.headroomFactor()
	target := r.current * spacing * head * 1.02

	// Never admit two rungs. The client takes the HIGHEST rendition it will
	// accept, so a cap clearing two of them skips one -- exactly the failure a
	// descending sweep suffers from and this design exists to avoid. The bound
	// uses the tightest spacing SEEN, since the closest pair is the one at risk.
	//
	// Only once there is something to see. Applying it with a default spacing
	// clamped the cap below what the next rung needed and the run escaped only
	// by timing out its holds -- a bound invented from no data blocking a step
	// the data would have allowed.
	if mr := r.minRatio(); mr > 0 {
		if lid := r.current * mr * mr * head * 0.98; target > lid {
			target = lid
		}
	}

	if target > r.capMbps {
		r.holds = 0
		return target
	}
	// The cap already offers more than the client has taken. Waiting is the
	// only thing that can help; raising cannot.
	if r.holds < capHolds {
		r.holds++
		return r.capMbps
	}
	r.holds++
	return r.raiseFromCap()
}

// raiseFromCap nudges the cap up when there is no rung to aim from, or when
// aiming has not produced a climb.
func (r *sweepRun) raiseFromCap() float64 {
	next := r.capMbps * (1 + r.params.ClimbPct/100)
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
func (r *sweepRun) addRung(mbps, drift, atCap float64) bool {
	for i, existing := range r.rungs {
		if math.Abs(existing.mbps-mbps) >= r.mergeWithin(existing.mbps) {
			continue
		}
		if drift < existing.drift {
			// Take the cleaner rate but keep the threshold already learned: the
			// first sighting is where the switch actually happened, and a later,
			// steadier look at the same rendition did not cause it.
			r.rungs[i].mbps, r.rungs[i].drift = mbps, drift
		}
		return false
	}
	m := mapped{mbps: mbps, drift: drift}
	if r.phase == phaseClimb {
		m.upAt = atCap
	}
	r.rungs = append(r.rungs, m)

	// Getting here means the client left whatever it was on, and this cap is
	// what moved it. Climbing, the threshold belongs to the rendition departed:
	// this is the cap at which that one became untenable.
	if r.phase == phaseClimb && len(r.rungs) > 1 {
		r.rungs[len(r.rungs)-2].downAt = atCap
	}
	return true
}

// mergeWithin is how close two plateaus must be to count as one rendition.
//
// PURELY RELATIVE, plus a floor small enough never to bind on a real ladder.
// It used to take max(MinStepMbps, ...), which conflated two unrelated things:
// MinStepMbps sizes the CAP STEP, and reusing it as rung resolution swamped the
// relative term at the bottom of the range.
//
// Measured: a real 234p rung at 0.28 Mbps and a real 360p rung at 0.51 -- two
// variants 1.9x apart -- were 0.23 apart in absolute terms, under the 0.25
// floor, and got merged into one. The bottom of the ladder was silently lost.
//
// A percentage cannot make that mistake: 10% of 0.28 is 0.028, and nothing in a
// real ladder sits that close. The absolute floor remains only to stop the
// tolerance collapsing to nothing near zero.
func (r *sweepRun) mergeWithin(mbps float64) float64 {
	pct := r.params.MergePct
	if pct <= 0 {
		pct = rungMergeDefault
	}
	return math.Max(mergeFloorMbps, mbps*pct/100)
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

// variation is the coefficient of variation of a window: standard deviation
// over the mean. On a starved client this is the shaper's own jitter, which is
// the floor on how precisely any rung can be measured at that rate.
func variation(samples []Sample) float64 {
	m := meanDown(samples)
	if m <= 0 || len(samples) < 2 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		d := s.Down - m
		sum += d * d
	}
	return math.Sqrt(sum/float64(len(samples))) / m
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
		rungs = append(rungs, Rung{
			Mbps:       round2(m.mbps),
			UpAtMbps:   round2(m.upAt),
			DownAtMbps: round2(m.downAt),
			Unstable:   unstable,
		})
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
	var hsum float64
	var hn int
	for _, x := range rungs {
		if h := x.Headroom(); h > 0 {
			hsum += h
			hn++
		}
	}
	if hn > 0 {
		note += fmt.Sprintf("; the client wanted %.2fx a rendition's cost before "+
			"selecting it, over %d observed switches", hsum/float64(hn), hn)
	}
	if len(r.throttle) > 0 {
		worst := 0.0
		for _, t := range r.throttle {
			if d := math.Abs(1 - t.Ratio); d > worst {
				worst = d
			}
		}
		note += fmt.Sprintf("; throttle measured %.0f%% off configured at its worst "+
			"across %d starved level(s)", 100*worst, len(r.throttle))
	}
	return Ladder{
		Service:    r.service,
		Rungs:      rungs,
		Provenance: LadderMeasured,
		MeasuredAt: r.started.UnixMilli(),
		Note:       note,
		Throttle:   r.throttle,
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
		found = append(found, Rung{
			Mbps: round2(m.mbps), UpAtMbps: round2(m.upAt),
			DownAtMbps: round2(m.downAt), Unstable: m.drift > unstableDrift,
		})
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
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
