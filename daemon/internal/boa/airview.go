package boa

/*
 * How contested each radio's channel is, merged across every scan taken.
 *
 * The problem this solves is asymmetric hardware. Scanning is free on the
 * onboard brcmfmac radio -- it sweeps BOTH bands while continuing to serve, and
 * hostapd never leaves ENABLED -- and expensive on the mt7921u adapters, which
 * refuse to scan while beaconing and have to have their access point taken down
 * for it. Measured 2026-09-07:
 *
 *	wlan0     scan while serving   1298 ms, no outage, 14-17 BSS, both bands
 *	mt7921u   scan while serving   refused; only works with the BSS down
 *
 * So the radio that can afford to look is not the radio you want to know about.
 * Merging fixes that: one wlan0 scan sees channel 40 and channel 149 perfectly
 * well, and every radio reads its own answer out of whoever happened to measure
 * its channel most recently.
 *
 * The alternative -- each radio scanning for itself -- costs an outage on the
 * two radios that carry the test traffic, every time the figure refreshes.
 */

// airViews resolves one AirView per radio from the scans held.
//
// FRESHEST wins where two scans cover the same channel, rather than preferring
// the radio's own scan. A scan is a snapshot of a room that changes: measured
// here, channel 40 read 9.8% at idle and 69.8% under load minutes apart, so an
// older reading by the radio itself is worth less than a newer one by its
// neighbour. Provenance travels in From so the interface can say who looked.
//
// A radio with no scan covering its channel gets NO entry, rather than a zeroed
// one. Absent means "nobody has looked here yet", and a 0% would claim an idle
// channel on evidence nobody gathered -- the same rule the per-client airtime
// map and the survey note already follow.
func airViews(scans map[string]ScanSummary, chanOf map[string]int) map[string]AirView {
	if len(scans) == 0 || len(chanOf) == 0 {
		return nil
	}
	out := map[string]AirView{}
	for iface, ch := range chanOf {
		if ch == 0 {
			continue // a radio whose channel could not be read
		}
		for from, sum := range scans {
			// Did this scan LISTEN to the channel, rather than hear anything on
			// it? A clear channel produces no entry in Channels at all, and
			// asking Channels alone made every fresh scan look silent about
			// channel 149 -- so the merge kept falling back to a three-minute
			// old scan while the radio there sat at 87% airtime. Hearing
			// nothing is the measurement. See ScanSummary.Looked.
			if !looked(sum, ch) {
				continue
			}
			// Freshest wins. Equal timestamps cannot happen across two scans of
			// different radios in practice, and if they did either answer is as
			// good as the other.
			if prev, ok := out[iface]; ok && prev.At >= sum.At {
				continue
			}
			v := AirView{Channel: ch, From: from, At: sum.At}
			// Nil c means the channel was listened to and nothing was heard:
			// no neighbours, and therefore nobody to report utilisation. Left
			// as unknown rather than 0%, because "nobody advertised it" is not
			// "the channel is idle" -- our own traffic is not in this figure.
			if c := channelIn(sum, ch); c != nil {
				v.UtilPct, v.UtilKnown = c.UtilPct, c.UtilFrom > 0
				v.UtilMinPct, v.UtilReporters = c.UtilMinPct, c.UtilFrom
				v.LoudestDBm = c.StrongestDBm
			}
			// Our own access point, as the scanning radio heard it. Absent when
			// the scan was taken BY this radio -- a radio cannot hear itself --
			// which is why this is a separate known flag rather than a zero.
			if s, ok := sum.Ours[iface]; ok {
				v.OursDBm, v.OursKnown = s, true
			}
			out[iface] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// looked reports whether this scan actually listened to the channel.
//
// Older summaries carry no Looked list at all, from before it was recorded. For
// those, fall back to "did it hear anything here" -- the old behaviour, which is
// wrong for a clear channel but is the most that can be said about a scan that
// never wrote down where it went.
func looked(sum ScanSummary, ch int) bool {
	if len(sum.Looked) == 0 {
		return channelIn(sum, ch) != nil
	}
	for _, c := range sum.Looked {
		if c == ch {
			return true
		}
	}
	return false
}

// channelIn finds one channel's entry in a scan summary.
//
// Covered channels count, not only the ones an access point beacons on: an
// 80MHz neighbour centred over 36/40/44/48 is occupying all four, and
// summariseScan already records its utilisation against each. A radio sitting
// on 44 with no AP primary there is not on a clear channel.
func channelIn(sum ScanSummary, ch int) *ScanChannel {
	for i := range sum.Channels {
		if sum.Channels[i].Channel == ch {
			return &sum.Channels[i]
		}
	}
	return nil
}
