package boa

import "math"

/*
 * What a link would look like from further away.
 *
 * The most useful Wi-Fi test for a player is the least repeatable one: walk away
 * from the router until the stream falls apart, then walk back. This box cannot
 * do it for real. Transmit power is not settable on this hardware (#122, #202:
 * `iw ... set txpower` validates and is then ignored), and neither is the rate
 * set -- measured 2026-09-05, `iw dev <if> set bitrates` returns
 * `Operation not supported (-95)` on BOTH radios, because mt76 sets
 * HAS_RATE_CONTROL and mt7921_ops implements no .set_bitrate_mask. The firmware
 * picks the rate; nothing on the host may overrule it.
 *
 * So the radio cannot be made to look further away, and this models what being
 * further away would DO instead. Everything here is arithmetic over published
 * relationships, applied to the impairments boa already has.
 *
 * WHAT THIS DOES NOT MOVE, which the interface has to admit: RSSI, PHY rate,
 * airtime and tx-failed all keep reporting the real, healthy radio. A client at
 * a modelled 40m still reads -34 dBm at 961 Mbit/s PHY while being handed 6.
 * That is a real contradiction on screen and is why a client under this model is
 * annotated. It is survivable because ABR players adapt on observed throughput
 * and buffer level rather than on signal strength -- the gap bites only for
 * something that reads RSSI directly, which is a different test.
 *
 * WHERE THIS COMES FROM, and where it does not.
 *
 * Two of the three layers are standard and named; the third is invented here,
 * and the difference matters more than either on its own. A reader has to be
 * able to tell which numbers carry the authority of a published relationship
 * and which are a shape someone chose because it looked right.
 *
 *   1. FREE-SPACE LOSS is the Friis transmission equation, in its decibel form
 *      for MHz and metres. Exact, and the constant -27.55 falls out of it
 *      rather than being fitted.
 *
 *   2. LOG-DISTANCE PATH LOSS is the standard indoor propagation model,
 *      PL(d) = PL(d0) + 10*n*log10(d/d0). The 3 dB per doubling of channel
 *      width is thermal noise scaling, 10*log10(2), not an estimate.
 *
 *      The exponent VALUES -- about 2 in free space, 3 in a typical building,
 *      near 4 through several walls -- are conventional figures written from
 *      memory rather than looked up. So are the -82 dBm sensitivity floor, the
 *      PHY ceilings and the per-device dB. NO SOURCE WAS CONSULTED for any
 *      number in this file, which is a weaker claim than "published" and is
 *      why they are all marked unverified in Source S.
 *
 *   3. THE DEGRADATION CURVE IS OURS. quality() is a logistic, and its
 *      steepness, centre and 30 dB headroom were CHOSEN to put the knee where a
 *      link starts retrying and to make the collapse happen inside about 10 dB.
 *      The rate curve, the corruption and loss thresholds, and the per-device
 *      antenna and transmit figures are all the same: plausible, internally
 *      consistent, and nobody's published result. Frame error rate against SNR
 *      really is a sigmoid; THIS sigmoid is an assertion about its parameters.
 *
 * So the model is not ported and is not cited: it is arithmetic over named
 * relationships with a made-up curve joining them, and DATA-CONTRACT Source S
 * carries the same split per number. That is also why every output is labelled
 * typed rather than measured, and why the calibration walk in #221 exists.
 *
 * NOT PORTED, deliberately. ns-3 and wmediumd implement the same standard
 * models and are both GPL-2.0; docs/LICENSING.md commits this repo to
 * containing only our own MIT code, with GPL tools used strictly as
 * subprocesses. A physical relationship is not anyone's to license; an
 * implementation of it is.
 */

/*
 * Path-loss exponents, from ITU-R P.1238's distance power loss coefficient N.
 *
 * The recommendation tabulates N (which is 10n in the exponent form used here):
 * N = 28 for residential construction, and N = 31 for a 5 GHz office. It also
 * says plainly that site-calibrated values are needed for real link planning,
 * which is the same thing #221's walk exists to produce.
 *
 * ITU handles walls and floors as a SEPARATE additive term Lf(n) rather than by
 * inflating N, so the old "through walls 3.8" here was conflating two effects.
 * It is kept as a coarse stand-in for a heavily obstructed path and named as
 * such, rather than pretending to be a tabulated figure.
 */
const (
	ExponentResidential = 2.8 // ITU-R P.1238, N = 28
	ExponentOffice      = 3.1 // ITU-R P.1238, N = 31 at 5 GHz
	ExponentObstructed  = 3.8 // OURS: a stand-in for what ITU models as floor loss
)

// DefaultExponent is the residential figure, this being a box for testing in
// the places people actually watch video.
const DefaultExponent = ExponentResidential

// freqForChannel is the inverse of channelForFreq (radiopower.go:1219) and is
// deliberately written to mirror it, so the two cannot disagree about where the
// band boundaries sit.
func freqForChannel(ch int) int {
	switch {
	case ch == 14:
		return 2484
	case ch >= 1 && ch <= 13:
		return 2407 + ch*5
	case ch >= 32 && ch <= 177:
		return 5000 + ch*5
	}
	return 0
}

/*
 * Free-space loss at one metre: 20*log10(f_MHz) - 27.55.
 *
 * 47.6 dB at 5745 MHz against 40.3 dB at 2462 MHz. That 7.3 dB is the whole
 * reason 5GHz has shorter range than 2.4GHz at the same power, and it is why
 * the two bands need separate curves rather than one curve and an offset
 * applied at the end.
 */
func freeSpaceAt1m(freqMHz int) float64 {
	if freqMHz <= 0 {
		return 0
	}
	return 20*math.Log10(float64(freqMHz)) - 27.55
}

// txDbm is the effective radiated power assumed for the access point.
//
// Assumed rather than read, because this box cannot read it either: Source Q
// records `iw ... get txpower` as subject to a known driver misreport. 20 dBm
// (100 mW) is the common regulatory ceiling for these bands and is close enough
// for a model whose output is deliberately labelled as typed.
const txDbm = 20.0

// RssiAt is the signal level a client would see at some distance, by the
// log-distance path loss model:
//
//	RSSI(d) = Ptx - FSPL(1m) - 10*n*log10(d)
//
// At n=3 every DOUBLING of distance costs about 9 dB, which is why a distance
// control has to be logarithmic: 1m to 2m costs as much as 8m to 16m.
func RssiAt(distanceM float64, freqMHz int, n float64) float64 {
	if distanceM < 1 {
		distanceM = 1 // the reference distance; closer than this the model says nothing
	}
	if n <= 0 {
		n = DefaultExponent
	}
	return txDbm - freeSpaceAt1m(freqMHz) - 10*n*math.Log10(distanceM)
}

// DistanceFor inverts RssiAt, for showing metres beside a stored dBm.
func DistanceFor(rssiDbm float64, freqMHz int, n float64) float64 {
	if n <= 0 {
		n = DefaultExponent
	}
	return math.Pow(10, (txDbm-freeSpaceAt1m(freqMHz)-rssiDbm)/(10*n))
}

/*
 * THE MCS LADDER, from IEEE Std 802.11-2020 Table 21-25.
 *
 * This replaces a made-up sensitivity floor and a made-up rate curve with the
 * standard's own numbers. Each rung carries the minimum receiver sensitivity
 * the standard requires at 20 MHz, and the two OFDM parameters needed to
 * compute its data rate at any width.
 *
 * Sensitivities (MCS 0..9 at 20 MHz, dBm):
 *   -82, -79, -77, -74, -70, -66, -65, -64, -59, -57
 *
 * They are not evenly spaced, and that unevenness is the cliff: MCS 5, 6 and 7
 * sit within 2 dB of each other, so a link crossing that stretch loses three
 * rungs almost at once. The made-up logistic was an attempt to imitate this
 * shape; the ladder simply has it.
 */
type mcsRung struct {
	Index  int
	Sens20 float64 // minimum sensitivity at 20 MHz, dBm
	Bits   float64 // coded bits per subcarrier: BPSK 1, QPSK 2, 16-QAM 4, 64-QAM 6, 256-QAM 8
	Coding float64 // FEC rate
}

var vhtLadder = []mcsRung{
	{0, -82, 1, 1.0 / 2},
	{1, -79, 2, 1.0 / 2},
	{2, -77, 2, 3.0 / 4},
	{3, -74, 4, 1.0 / 2},
	{4, -70, 4, 3.0 / 4},
	{5, -66, 6, 2.0 / 3},
	{6, -65, 6, 3.0 / 4},
	{7, -64, 6, 5.0 / 6},
	{8, -59, 8, 3.0 / 4},
	{9, -57, 8, 5.0 / 6},
}

/*
 * Data subcarriers per channel width, from the standard's OFDM numerology.
 *
 * With the 4 us long-guard-interval symbol these reproduce the published rates
 * exactly, which is the check that the derivation is right rather than
 * remembered: MCS0 at 20 MHz gives 52*1*0.5/4 = 6.5 Mbit/s, MCS7 at 20 MHz
 * gives 65, and MCS9 at 80 MHz gives 234*8*(5/6)/4 = 390. All three are the
 * well-known values.
 */
func dataSubcarriers(widthMHz int) float64 {
	switch {
	case widthMHz >= 160:
		return 468
	case widthMHz >= 80:
		return 234
	case widthMHz >= 40:
		return 108
	default:
		return 52
	}
}

// symbolMicros is the long guard interval symbol: 3.2 us of data plus 0.8 us of
// guard. Short GI is not modelled -- it is a 10% difference and the rest of the
// model is not that precise.
const symbolMicros = 4.0

// rungRate is one spatial stream at that rung and width, in Mbit/s.
func rungRate(r mcsRung, widthMHz int) float64 {
	return dataSubcarriers(widthMHz) * r.Bits * r.Coding / symbolMicros
}

/*
 * rungSensitivity scales the 20 MHz figure for wider channels.
 *
 * +3 dB per doubling, which the standard's own scaling uses and which
 * independent write-ups on 802.11ac sensitivity state in the same words: "for
 * every doubling of channel width, you require 3 dB better signal to achieve
 * the same MCS rate". It is also just thermal noise: 10*log10(2).
 */
func rungSensitivity(r mcsRung, widthMHz int) float64 {
	return r.Sens20 + 10*math.Log10(float64(widthMHz)/20)
}

/*
 * topRung caps the ladder for a band that cannot reach the higher rungs.
 *
 * The 2.4 GHz radio on this box runs 802.11n, whose HT ladder stops at MCS 7 --
 * 256-QAM arrived with 802.11ac. Modelling it up to MCS 9 would hand it rates
 * the hardware cannot produce.
 */
func topRung(freqMHz int) int {
	if freqMHz < 3000 {
		return 7
	}
	return 9
}

/*
 * IMPLEMENTATION GAIN: real radios beat the standard's minimum.
 *
 * Table 21-25 states the WORST a conforming receiver may be, at 10% packet
 * error for a 4096-octet frame. Commodity silicon is several dB better than the
 * requirement, so taking the table literally would put every rung about 6 dB
 * further out than it belongs and make -60 dBm on an 80 MHz channel look
 * marginal when in practice it is comfortable.
 *
 * Six dB is ours, and it is the single number doing the most work in this file.
 */
const implGainDb = 6.0

// rungNeed is the level this hardware is assumed to need for that rung.
func rungNeed(r mcsRung, widthMHz int) float64 {
	return rungSensitivity(r, widthMHz) - implGainDb
}

// bestRung is rate control: the highest rung this level can hold.
func bestRung(levelDbm float64, freqMHz, widthMHz int) (rung mcsRung, ok bool) {
	top := topRung(freqMHz)
	for i := len(vhtLadder) - 1; i >= 0; i-- {
		r := vhtLadder[i]
		if r.Index > top {
			continue
		}
		if levelDbm >= rungNeed(r, widthMHz) {
			return r, true
		}
	}
	return vhtLadder[0], false
}

/*
 * headroomDb is how far above LOSING THE LINK ENTIRELY this level sits.
 *
 * Measured against the bottom of the ladder rather than against the rung
 * currently held, and that choice matters. Margin above the current rung
 * sawtooths -- it resets to zero every time rate control steps down -- so
 * impairment derived from it would fall as a link got weaker, which is both
 * wrong and exactly what the tests caught.
 *
 * Distance from the floor is monotonic, so the RATE steps down the ladder while
 * corruption, loss and delay rise smoothly. That is also what a real link does:
 * the rung changes discretely, the error rate does not.
 */
func headroomDb(levelDbm float64, widthMHz int) float64 {
	return levelDbm - rungNeed(vhtLadder[0], widthMHz)
}

/*
 * ShapeForRssi is the whole model: a signal level in, the impairments boa
 * already has out.
 *
 * The ordering between the fields matters as much as their values, because it
 * is what makes the result read like a weakening radio rather than a throttle:
 *
 *   - CORRUPT LEADS. A weak signal does not drop IP packets, it damages frames
 *     that then fail their checksum -- having already spent the airtime to send
 *     them. CorruptPct is exactly that, and TCP recovers from it by a different
 *     path than from a drop.
 *   - LOSS FOLLOWS LATE, AND CORRELATED. 802.11 retries hide errors from the
 *     application until they are exhausted, so loss stays at zero for most of
 *     the walk and then arrives in bursts. LossBurst > 1 puts netem on its
 *     Gilbert-Elliott path, where LossPct becomes a mean over time rather than
 *     a per-packet coin flip.
 *   - RATE FALLS THROUGHOUT, because the MCS rung is walking down.
 *   - DELAY RISES WITH RETRIES, and jitter with it: a retried frame arrives
 *     late, and how late varies.
 *
 * UPLINK IS THE SAME AIR, HEARD FROM A QUIETER TRANSMITTER. Both directions
 * cross one channel, so they share the loss, corruption and delay of that
 * channel -- but the client is not as loud as the access point, so its frames
 * arrive weaker and its direction fails FIRST. That is modelled by evaluating
 * the same curve at a level offset by the power deficit, which is the honest
 * asymmetry.
 *
 * It replaces a rate multiplier that was here first and was wrong: scaling
 * uplink to a fraction of downlink models an ISP's asymmetric plan, which has
 * nothing to do with distance and is what the named presets are for. It also
 * had the asymmetry backwards, giving uplink a smaller pipe on an equally good
 * link rather than an equally sized pipe on a worse one.
 */
func ShapeForLevels(downDbm, upDbm float64, freqMHz, widthMHz int) (down, up Shape) {
	return shapeAtLevel(downDbm, freqMHz, widthMHz),
		shapeAtLevel(upDbm, freqMHz, widthMHz)
}

/*
 * LevelsFor turns a path level into the two levels that actually arrive.
 *
 * The slider sets the PATH -- what a device with no losses of its own would
 * see at that distance. What each direction then gets depends on the device:
 *
 *   down = path - antenna
 *   up   = path - antenna - transmit
 *
 * The antenna appears in both because its gain is reciprocal, which is the
 * whole reason changing the device kind moves both numbers. The transmit
 * deficit appears once, which is why uplink fails first.
 */
func LevelsFor(pathDbm float64, m RssiModel) (downDbm, upDbm float64) {
	downDbm = pathDbm - m.RxDb
	return downDbm, downDbm - m.TxDb
}

/*
 * DefaultDeltaDb is how much quieter a typical client is than the access point.
 *
 * A phone transmits around 13-15 dBm against an AP's 20, and it has a smaller
 * antenna. Six dB is the middle of that and is what makes uplink cross the
 * sensitivity floor before downlink does -- so on a modelled walk the device
 * loses the ability to be heard before it loses the ability to hear, which is
 * the order a real walk fails in.
 *
 * A DEFAULT rather than a constant, because it is the one part of this model
 * that genuinely varies by device: a watch is quieter than a laptop, and a
 * phone in a pocket is quieter than one on a desk. Setting it to 0 makes the
 * two directions equally loud, which is a legitimate thing to want to test.
 */
const (
	DefaultRxDb = 2.0 // a phone's antenna, against a laptop's
	DefaultTxDb = 4.0 // and its lower transmit power, on top
)

// MaxDeviceDb bounds each half. Past this a device is dead across the whole
// usable range and the control stops describing anything.
const MaxDeviceDb = 20.0

func shapeAtLevel(rssiDbm float64, freqMHz, widthMHz int) Shape {
	if widthMHz <= 0 {
		widthMHz = 20
	}
	if freqMHz <= 0 {
		freqMHz = 5745
	}

	rung, ok := bestRung(rssiDbm, freqMHz, widthMHz)

	// Below the lowest rung the link does not hold. Expressed as total loss
	// rather than a zero rate, because a rate of 0 means UNLIMITED everywhere
	// else in this codebase and would read as "no cap" rather than "nothing
	// gets through".
	if !ok {
		return Shape{LossPct: 100, LossBurst: 8}
	}

	// The rung's own rate, from the standard's OFDM parameters. Two thirds of
	// it reaches the application: 802.11 spends the rest on preambles, inter
	// frame spacing, block acks and contention, and that overhead is roughly a
	// constant fraction. Ours, and the roundest number in the file.
	const macEfficiency = 0.65
	rate := rungRate(rung, widthMHz) * macEfficiency

	/*
	 * WHAT THE HEADROOM COSTS -- everything from here down is ours.
	 *
	 * The ladder says which rung a link can hold and nothing about how
	 * comfortably. Minimum sensitivity is defined at 10% packet error, so a
	 * link near the bottom of the ladder is already losing frames and one well
	 * above it is not. Impairment is therefore a function of HEADROOM above
	 * losing the link, not of absolute level.
	 *
	 * The shape is asserted rather than fitted. It has the two properties a
	 * real link has -- corruption arrives before loss, and both are negligible
	 * until the headroom is nearly gone -- and beyond that the numbers are
	 * plausible rather than measured. See DATA-CONTRACT Source S.
	 */
	const comfortDb = 10.0 // above this much headroom, nothing is imposed
	head := headroomDb(rssiDbm, widthMHz)
	shortfall := 0.0
	if head < comfortDb {
		shortfall = (comfortDb - head) / comfortDb // 0 comfortable .. 1 at the floor
	}

	// Corruption leads: a weak signal damages frames that fail their checksum
	// having already spent the airtime. Rising towards the standard's own 10%
	// at the floor, since that is what the sensitivity figure means.
	corrupt := 10 * shortfall * shortfall

	// Loss follows late and correlated, once retries are being exhausted.
	loss, burst := 0.0, 0.0
	if shortfall > 0.75 {
		loss = 20 * (shortfall - 0.75) / 0.25
		burst = 6
	}

	// Retries cost time before they cost delivery.
	delay := 2 + 40*shortfall*shortfall
	// Jitter is a QUARTER of the delay, not the 45% this used to carry.
	// netem's jitter reorders packets, and reordered ACKs make TCP see
	// duplicates and collapse its window -- so an over-large jitter does far
	// more damage than its milliseconds suggest, and 45% of the mean was not
	// defensible at any signal level.
	jitter := delay * 0.25

	return Shape{
		RateMbps:   round1(rate),
		DelayMs:    round1(delay),
		JitterMs:   round1(jitter),
		LossPct:    round2(loss),
		LossBurst:  burst,
		CorruptPct: round2(corrupt),
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

/*
 * The radios a client with none of its own can be modelled as being on.
 *
 * Two, because this box runs exactly one of each, and they are the two cases
 * worth testing: the wide fast one that dies sooner and the narrow slow one
 * that reaches further. The first is the REFERENCE -- see BestBandFor.
 */
var ModelBands = []struct{ FreqMHz, WidthMHz int }{
	{FreqMHz: 5745, WidthMHz: 80},
	{FreqMHz: 2462, WidthMHz: 20},
}

/*
 * BestBandFor picks the radio that gives the better link at one distance.
 *
 * This is the rule a band switch should actually use, and it is deliberately
 * not "5GHz until some threshold". Quality already accounts for both things
 * that decide the answer -- 5GHz loses 7.3 dB more to the path at the same
 * distance, and its wider channel needs 6 dB more signal for the same error
 * rate -- so comparing the two curves gives the crossover rather than assuming
 * it. It generalises: with two 5GHz radios of different widths the narrower one
 * would win further out, which a band-name rule could never express.
 *
 * NO HYSTERESIS, and that is not an omission. Hysteresis exists to stop a
 * jittering input flapping the choice; the input here is a slider a person
 * moves, so for a given distance the answer is always the same and there is
 * nothing to flap. A real client roaming on measured RSSI is the case that
 * needs it, and that is not this.
 *
 * The stored level is read as the REFERENCE band's, because a level in dBm is
 * only meaningful against a frequency -- the same distance is 7.3 dB weaker on
 * 5GHz. Distance is the band-independent quantity, so the level is converted to
 * one and both bands are evaluated from there.
 */
func BestBandFor(refDbm float64, m RssiModel) (freqMHz, widthMHz int) {
	ref := ModelBands[0]
	dist := DistanceFor(refDbm, ref.FreqMHz, m.N)
	best, bestQ, bestLvl := ref, -1.0, math.Inf(-1) // bestQ is Mbps
	for _, b := range ModelBands {
		// The level this band would deliver at that distance, after the
		// device's own antenna.
		lvl := RssiAt(dist, b.FreqMHz, m.N) - m.RxDb
		/*
		 * Scored on THROUGHPUT, not on quality.
		 *
		 * Quality saturates at 1 once a link has margin to spare, and 2.4GHz
		 * gets there sooner because its narrower channel has a lower floor. So
		 * comparing quality made 2.4GHz win at every distance including three
		 * metres, where 5GHz would carry eight times as much -- both links were
		 * "perfect" and the tie went to the wrong one.
		 *
		 * Throughput separates them where quality cannot: it carries the
		 * ceiling as well as the condition, which is exactly the trade a band
		 * choice is. It is also what a real client's steering optimises for.
		 */
		q := shapeAtLevel(lvl, b.FreqMHz, b.WidthMHz).RateMbps
		/*
		 * A CHALLENGER MUST BE CLEARLY BETTER, not merely ahead.
		 *
		 * Rates are now a staircase off the MCS ladder rather than a smooth
		 * curve, and two staircases interleave: near the crossing the slower
		 * band briefly leads each time the faster one steps down, then loses
		 * again on the next step. A walk outwards changed band three times
		 * because of it.
		 *
		 * Requiring a clear win collapses those near-ties to the one real
		 * crossing, and does it without hysteresis -- the answer still depends
		 * only on distance, so the same distance always gives the same radio.
		 * The first band listed is the incumbent, which is why the reference is
		 * the wide fast one.
		 */
		const clearWin = 1.15
		switch {
		case q > bestQ*clearWin:
			best, bestQ, bestLvl = b, q, lvl
		case math.Abs(q-bestQ) <= 1e-9 && q <= 0 && lvl > bestLvl:
			/*
			 * BOTH DEAD, so break the tie on raw level.
			 *
			 * Quality clamps to zero past the floor, so beyond the range of
			 * every radio they score the same and the first listed would win by
			 * default -- which sent a walk back to 5GHz once it was out of
			 * range of everything, a second crossover on the way out. Choosing
			 * the stronger level keeps it on the radio that will recover first
			 * coming back, and makes the walk cross over exactly once. There is
			 * a test for the count.
			 */
			best, bestLvl = b, lvl
		}
	}
	return best.FreqMHz, best.WidthMHz
}

/*
 * RssiShapesFor resolves one client's distance model to the shapes it implies.
 *
 * ONE resolver, used by both the tick that applies the shapes and the snapshot
 * that shows them. They were briefly separate and that is exactly how a control
 * comes to display something other than what it is enforcing -- the interface
 * would have been lying about a number it had itself computed.
 *
 * The band comes from the client's own RadioOn rather than from an engine
 * lookup: reading radio state inside the tick once deadlocked the non-reentrant
 * lock and took the API down with it, and everything needed is already here.
 */
func RssiShapesFor(c Client) (down, up Shape, freqMHz int, ok bool) {
	m := c.Policy.Rssi
	if m == nil || !c.Policy.Enabled {
		return Shape{}, Shape{}, 0, false
	}
	/*
	 * The radio to model: READ from the client when it is on one, CHOSEN when
	 * it is not.
	 *
	 * On a Wi-Fi client the band is a fact, and letting it be overridden would
	 * let the interface disagree with the hardware. On the wired port there is
	 * no fact to read, so the operator names the radio to imitate -- which is a
	 * legitimate test: it is the Wi-Fi degradation profile with none of a real
	 * radio's variability underneath, so a run repeats exactly.
	 *
	 * What is NOT allowed is a silent default. This once fell back to 5GHz at
	 * 80MHz whenever a client had no radio, which handed wired devices a curve
	 * they had nothing to do with and said nothing about it. With neither a
	 * radio nor a choice, there is no honest curve and the model is refused.
	 */
	var width int
	switch {
	case c.RadioOn != nil && c.RadioOn.Channel != 0:
		freqMHz = freqForChannel(c.RadioOn.Channel)
		width = c.RadioOn.WidthMHz
	case m.AutoBand:
		freqMHz, width = BestBandFor(m.Dbm, *m)
	case m.FreqMHz > 0:
		freqMHz, width = m.FreqMHz, m.WidthMHz
	}
	if freqMHz == 0 {
		return Shape{}, Shape{}, 0, false
	}
	if width <= 0 {
		width = 20
	}
	// Under AutoBand the stored level belongs to the reference band, so it is
	// re-expressed against whichever band was chosen before the shapes are
	// derived. Otherwise a switch would change the curve without changing the
	// level feeding it.
	path := m.Dbm
	if m.AutoBand && (c.RadioOn == nil || c.RadioOn.Channel == 0) {
		dist := DistanceFor(m.Dbm, ModelBands[0].FreqMHz, m.N)
		path = RssiAt(dist, freqMHz, m.N)
	}
	dn, upl := LevelsFor(path, *m)
	down, up = ShapeForLevels(dn, upl, freqMHz, width)
	return down, up, freqMHz, true
}

// rssiViewFor is what the snapshot shows: the model's own numbers plus the
// shapes it is imposing, so the interface never has to recompute them and so
// the two can never disagree.
func rssiViewFor(c Client) *RssiView {
	down, up, freq, ok := RssiShapesFor(c)
	if !ok {
		return nil
	}
	m := c.Policy.Rssi
	// The level is quoted against the band actually in use, so an auto switch
	// moves the two figures the way a real band change would.
	path := m.Dbm
	if m.AutoBand && (c.RadioOn == nil || c.RadioOn.Channel == 0) {
		dist := DistanceFor(m.Dbm, ModelBands[0].FreqMHz, m.N)
		path = RssiAt(dist, freq, m.N)
	}
	dn, upl := LevelsFor(path, *m)
	return &RssiView{
		Dbm:       round1(path),
		DistanceM: round1(DistanceFor(m.Dbm, ModelBands[0].FreqMHz, m.N)),
		DownDbm:   round1(dn),
		UpDbm:     round1(upl),
		FreqMHz:   freq,
		WidthMHz:  widthOf(c, m, freq),
		Down:      down,
		Up:        up,
	}
}

// widthOf reports the width that went with the resolved frequency, for display.
func widthOf(c Client, m *RssiModel, freqMHz int) int {
	if c.RadioOn != nil && c.RadioOn.Channel != 0 {
		return c.RadioOn.WidthMHz
	}
	for _, b := range ModelBands {
		if b.FreqMHz == freqMHz {
			return b.WidthMHz
		}
	}
	return m.WidthMHz
}
