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

// Path-loss exponents, for turning a distance into a signal level. The only
// free parameter in the whole model, and the reason distance is a LABEL here
// rather than the stored value: it is a per-building guess, so a policy stored
// in metres would mean a different impairment in a different building.
const (
	ExponentOpen  = 2.2 // open plan, few obstructions
	ExponentHome  = 3.0 // a typical home: some walls, some furniture
	ExponentWalls = 3.8 // through several walls
)

// DefaultExponent is what the interface offers until someone calibrates their
// own building against a measured walk.
const DefaultExponent = ExponentHome

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
 * The sensitivity floor, per band and width.
 *
 * A wider channel spreads the same power over more spectrum, so it needs a
 * stronger signal for the same error rate: about 3 dB per doubling of width.
 * This is why a 5GHz 80MHz link dies before a 2.4GHz 20MHz one at the same
 * distance, on top of the 7.3 dB of extra path loss.
 */
func floorDbm(widthMHz int) float64 {
	base := -82.0 // roughly MCS0 at 20MHz on commodity silicon
	switch {
	case widthMHz >= 160:
		return base + 9
	case widthMHz >= 80:
		return base + 6
	case widthMHz >= 40:
		return base + 3
	}
	return base
}

// headroomDb is how far above the floor a link is completely untroubled. Inside
// this margin nothing at all should be imposed: a strong link is strong.
const headroomDb = 30.0

/*
 * quality maps a signal level onto 0 (unusable) .. 1 (untroubled).
 *
 * A SIGMOID, not a ramp, and this is the single most important property in the
 * file. Frame error rate against SNR does not slope: almost nothing happens
 * across most of the range and then everything happens inside about 10 dB. That
 * is the cliff every real walk has -- fine, fine, fine, then it falls apart over
 * a few steps -- and a model that degrades linearly with distance feels wrong in
 * a way that is hard to name and obvious to anyone who has done the walk.
 *
 * Do not "fix" this into a slope to make the control feel smoother. The
 * unevenness is the fidelity.
 */
func quality(rssiDbm float64, widthMHz int) float64 {
	floor := floorDbm(widthMHz)
	// Distance above the floor, normalised over the headroom.
	x := (rssiDbm - floor) / headroomDb
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	// CHOSEN, not derived and not cited. The logistic shape is the right family
	// -- frame error rate against SNR really does turn over sharply -- but these
	// two numbers are an assertion about where and how sharply, picked to put
	// the knee where a link starts retrying rather than in the middle of the
	// usable range. Replaced by a fitted curve when #221's walk is recorded.
	const steepness = 9.0
	const centre = 0.32
	q := 1 / (1 + math.Exp(-steepness*(x-centre)))
	// Rescale so the ends are exactly 0 and 1 rather than the logistic's
	// asymptotes, which would otherwise impose a little loss on a perfect link.
	lo := 1 / (1 + math.Exp(steepness*centre))
	hi := 1 / (1 + math.Exp(-steepness*(1-centre)))
	return (q - lo) / (hi - lo)
}

// phyCeilingMbps is roughly what the radio negotiates at full signal, per band
// and width. Typed, not measured: replaced by a calibration walk once #106
// records signal and airtime over time.
func phyCeilingMbps(freqMHz, widthMHz int) float64 {
	if freqMHz < 3000 { // 2.4GHz, in practice 20MHz 802.11n on this box
		return 72
	}
	switch {
	case widthMHz >= 160:
		return 1200
	case widthMHz >= 80:
		return 600
	case widthMHz >= 40:
		return 300
	}
	return 150
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
	q := quality(rssiDbm, widthMHz)
	ceiling := phyCeilingMbps(freqMHz, widthMHz)

	// Out of range: the link is gone. Expressed as total loss rather than a
	// zero rate, because a rate of 0 means UNLIMITED everywhere else in this
	// codebase and would read as "no cap" rather than "nothing gets through".
	if q <= 0 {
		return Shape{LossPct: 100, LossBurst: 8}
	}

	// Throughput tracks the MCS ladder, which is roughly linear in quality once
	// the link is usable at all, with a floor at the lowest rung rather than at
	// zero -- a barely-connected client still passes a trickle.
	//
	// The exact curve is ours: the shape is asserted, not fitted to anything.
	rate := ceiling * (0.06 + 0.94*q*q)

	// Corruption is the leading indicator, rising as the floor approaches.
	corrupt := 0.0
	if q < 0.55 {
		corrupt = 12 * (0.55 - q) * (0.55 - q) / (0.55 * 0.55)
	}

	// Loss only once retries are being exhausted, and always in bursts.
	loss, burst := 0.0, 0.0
	if q < 0.22 {
		loss = 18 * (0.22 - q) / 0.22
		burst = 6
	}

	// Retries cost time before they cost delivery.
	delay := 2 + 55*(1-q)*(1-q)
	jitter := delay * 0.45

	return Shape{
		RateMbps:   round1(rate),
		DelayMs:    round1(delay),
		JitterMs:   round1(jitter),
		LossPct:    round2(loss),
		LossBurst:  burst,
		CorruptPct: round2(corrupt),
	}
}

// round1 sits beside sweep.go's round2/round3 rather than duplicating them.
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
		switch {
		case q > bestQ+1e-9:
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
