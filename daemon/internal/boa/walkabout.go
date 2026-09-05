package boa

/*
 * THE WALKABOUT: the walk-away-and-back test, as a pattern.
 *
 * Everything else in patternlib walks a ladder of numbers someone chose. This
 * one walks a DISTANCE, and asks the model in distance.go what that distance
 * does -- so its keyframes are not a curve anybody drew. They are the same
 * computation the distance slider performs, evaluated at a series of levels
 * instead of at one, which is the entire difference between the slider and this.
 *
 * WHY IT EXISTS. Issue #221: the most useful Wi-Fi test for a player is a walk
 * away from the router until the stream falls apart, then back, and this box
 * cannot perform one. Transmit power is accepted and ignored (#122, #202) and
 * the PHY rate cannot be clamped either (-95 on both radios), so the radio
 * cannot be made to look further away. The slider models what being further
 * away would do; this sweeps that model on a clock, which is what makes it a
 * walk rather than a position.
 *
 * WHAT MAKES IT MORE THAN A RATE RAMP is the band change. Both radios serve the
 * same SSID and 2.4GHz loses about 7.3 dB less to the path, so a real walk
 * reliably ends with the phone on the onboard radio -- 80MHz ax down to 20MHz n,
 * close to an order of magnitude in one step, plus the handshake. For an
 * adaptive player that is the most interesting moment in the whole test.
 *
 * It will not happen by itself. The real signal never changes while the model
 * runs, so the client has no reason to roam and will sit on 5GHz at -34 dBm at a
 * modelled 40 m. So the walk DRIVES it, with a gather on the link lane at each
 * crossing -- and a client entitled to refuse is the finding, not a failure.
 *
 * IT OWNS EVERY AXIS AT ONCE -- rate, delay, jitter, loss, burst and corruption,
 * in both directions. Under MergePatterns' first-wins fillZeroFields that means
 * it takes essentially everything and leaves later patterns nothing to
 * contribute, so it is not an overlay and does not layer usefully. Same warning
 * as reorderClimbSteps carries, for the same reason.
 */

// PatternWalkabout is the generated name; see BuiltinNames.
const PatternWalkabout = "walkabout"

const (
	/*
	 * Where the walk starts, in path dBm against the 5GHz reference band. -40
	 * is beside the access point: the top of the slider's own range, and
	 * comfortably above what any rung needs.
	 *
	 * WHERE IT ENDS IS NOT A CONSTANT, and deliberately so -- see walkOutLevels.
	 */
	walkNearDbm = -40.0

	/*
	 * A backstop, not the far end. It exists only so a model that never reports
	 * a dead link cannot produce an unbounded walk; the search below stops long
	 * before this on any sane set of constants.
	 */
	walkFloorDbm = -120.0

	/*
	 * 4 dB a step, which is a compromise with the ladder rather than a round
	 * number. Rungs are 2-5 dB apart through the interesting part of the
	 * ladder, so a coarser step would skip whole rungs and a finer one would
	 * spend several keyframes on the same rung producing identical shapes.
	 */
	walkStepDb = 4.0

	/*
	 * THE RETURN LEG IS SLOWER, and this is the detail most likely to be
	 * mistaken for a bug.
	 *
	 * Recovery is not the reverse of degradation. Rate control drops fast --
	 * a few failed frames will do it -- and climbs back conservatively, probing
	 * upward only after sustained success, and an ABR player is more cautious
	 * still: it has a drained buffer to refill before it dares raise its
	 * rendition. A symmetric walk therefore reports a recovery that never
	 * happens, because it gives the way back the same time as the way out.
	 *
	 * Half again is a guess with the right sign. Ours, and unmeasured.
	 */
	walkReturnFactor = 1.5

	/*
	 * The opening gather sits half a second in, not at zero, because an event
	 * at zero can never fire: the playhead starts there, and linkFires asks
	 * whether the event lies in (prev, pos], which excludes the instant the run
	 * begins. An opening gather placed at 0 would silently never happen and the
	 * walk would start on whichever band the client was already on.
	 */
	walkOpenSec = 0.5
)

/*
 * walkabout generates the pattern. dwellSec is the OUT leg's dwell per step;
 * the return leg stretches it by walkReturnFactor.
 *
 * The device modelled is a phone (DefaultRxDb / DefaultTxDb), because a phone
 * is what gets carried around a building and because it is the harder case:
 * a smaller antenna and less transmit power than a laptop, so it loses the
 * uplink sooner. The exponent is the residential one, this being a box for
 * testing where people actually watch video.
 */
func walkabout(name string, dwellSec float64) Pattern {
	if dwellSec <= 0 {
		dwellSec = defaultRungDwellSec
	}
	m := RssiModel{
		N:        DefaultExponent,
		RxDb:     DefaultRxDb,
		TxDb:     DefaultTxDb,
		AutoBand: true,
	}

	levels := walkLevels(m)
	apex := len(walkOutLevels(m)) - 1 // index of the far end within levels

	keys := make([]Keyframe, 0, len(levels))
	var links []LinkEvent
	prevFreq := 0
	at := 0.0

	for i, lvl := range levels {
		down, up, freq, _ := AutoShapesAt(lvl, m)
		t := snapHalf(at)
		keys = append(keys, Keyframe{
			AtSec: t,
			Down:  down,
			Up:    up,
			// Hold, never ramp, for two reasons that happen to agree. The
			// ladder steps and does not glide, so an interpolated segment
			// describes a link that does not exist; and lerpShape does not
			// interpolate LossBurst, so a ramped segment would silently drop
			// the burst length and turn correlated loss into uniform loss --
			// which is netem's default and essentially never happens on a real
			// link.
			Ease: EaseHold,
		})
		if freq != prevFreq {
			gatherAt := t
			if i == 0 {
				gatherAt = walkOpenSec
			}
			links = append(links, LinkEvent{
				AtSec: gatherAt, Kind: LinkGather, ToBandMHz: freq,
			})
			prevFreq = freq
		}
		if i >= apex {
			at += dwellSec * walkReturnFactor
		} else {
			at += dwellSec
		}
	}

	// Loops. The sequence ends on the level it began with, so the seam is not a
	// step change, and the opening gather names the band the walk ended on --
	// so a second lap starts where the first one finished.
	return Pattern{Name: name, Keys: keys, Links: links, Loop: true}
}

/*
 * walkOutLevels is the way out, and it ends where the MODEL says the link ends
 * rather than at a level someone picked.
 *
 * The obvious version of this was a walkFarDbm constant, and it was wrong twice
 * over within an hour of being written. Too near and the walk finished with the
 * stream still comfortable -- the first draft ended at -84 dBm delivering
 * 16.9 Mbit/s, which is 4K with room to spare and is not what "walk until it
 * falls apart" means. Too far and it spends its last steps on a dead link,
 * repeating "nothing gets through" at three different distances.
 *
 * Worse, either number goes stale silently. Every constant behind the model
 * moved during this feature -- the sensitivity ladder, the path-loss exponents,
 * the implementation gain -- and each move slides the cliff along the dBm axis
 * while a hand-picked endpoint stays put. Asking the model where its own floor
 * is means the walk is still a walk to the cliff after the next such change.
 *
 * So: walk out, stop at the last level BOTH directions still hold. That level
 * is the edge -- the far end of a real walk, where the link is at its worst and
 * still alive -- and going dark is a deadzone's job, which layers on top.
 */
func walkOutLevels(m RssiModel) []float64 {
	var out []float64
	for l := walkNearDbm; l >= walkFloorDbm; l -= walkStepDb {
		down, up, _, _ := AutoShapesAt(l, m)
		// Total loss is how the model says "out of range"; a rate of 0 means
		// unlimited everywhere else here, so it cannot mean this.
		if down.LossPct >= 100 || up.LossPct >= 100 {
			break
		}
		out = append(out, l)
	}
	return out
}

// walkLevels is out and back, without holding the far end twice, ending on the
// level it started from.
func walkLevels(m RssiModel) []float64 {
	out := walkOutLevels(m)
	seq := make([]float64, 0, 2*len(out)-1)
	seq = append(seq, out...)
	for i := len(out) - 2; i >= 0; i-- {
		seq = append(seq, out[i])
	}
	return seq
}
