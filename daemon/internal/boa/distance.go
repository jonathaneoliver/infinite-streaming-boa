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
 * WRITTEN FROM THE PUBLISHED RELATIONSHIPS, not ported. ns-3 and wmediumd
 * implement the same models and are both GPL-2.0; docs/LICENSING.md commits this
 * repo to containing only our own MIT code, with GPL tools used strictly as
 * subprocesses. The physics is not anyone's to license; their code is.
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
	// Logistic centred just above the floor, so the knee sits where a real link
	// starts retrying rather than in the middle of the usable range.
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
func ShapeForRssi(rssiDbm float64, freqMHz, widthMHz int, deltaDb float64) (down, up Shape) {
	return shapeAtLevel(rssiDbm, freqMHz, widthMHz),
		shapeAtLevel(rssiDbm-deltaDb, freqMHz, widthMHz)
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
const DefaultDeltaDb = 6.0

// MaxDeltaDb bounds it. Past this the uplink is dead across the whole usable
// range and the control stops describing a device.
const MaxDeltaDb = 20.0

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
	freqMHz, width := 5745, 80
	if c.RadioOn != nil {
		if f := freqForChannel(c.RadioOn.Channel); f > 0 {
			freqMHz = f
		}
		if c.RadioOn.WidthMHz > 0 {
			width = c.RadioOn.WidthMHz
		}
	}
	down, up = ShapeForRssi(m.Dbm, freqMHz, width, m.DeltaDb)
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
	return &RssiView{
		Dbm:       m.Dbm,
		DistanceM: round1(DistanceFor(m.Dbm, freq, m.N)),
		UpDbm:     round1(m.Dbm - m.DeltaDb),
		Down:      down,
		Up:        up,
	}
}
