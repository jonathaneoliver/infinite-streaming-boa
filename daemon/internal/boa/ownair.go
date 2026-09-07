package boa

import "time"

/*
 * What OUR OWN clients are costing each radio, right now.
 *
 * The counterpart to the neighbours' BSS Load figure, and the one to trust. The
 * two answer different questions and only one of them is ours:
 *
 *	own airtime    our station counters, 1 Hz, verified against iperf3
 *	others (BSS)   what neighbouring access points advertise about the channel
 *
 * Measured 2026-09-07 on channel 149, both clients idle: our own airtime read
 * 1.3% while the single neighbour in earshot advertised 32%. Neither is wrong --
 * that 32% is a distant ASUS describing its own traffic -- but only one of them
 * answers "how busy is MY radio".
 *
 * And on a quiet channel the neighbours' number is at its weakest, because quiet
 * means there is nobody nearby to ask. On channel 40, with two routers a few
 * feet away, their figure bracketed ours within a few points. On 149 the only
 * reporter is 50 dB fainter and hears almost nothing of what we do. So the
 * instrument that degrades gracefully is the one that does not depend on
 * strangers being in earshot.
 */

// ownAirWindow is how far back the average reaches.
//
// FIVE SECONDS to match what the neighbours' figure already is. A BSS Load
// element is averaged over dot11ChannelUtilizationBeaconIntervals -- 50 by
// default -- and every access point measured here beacons at 100 TU, so their
// number covers 50 x 102.4ms = 5.12s. Averaging ours over anything else would
// put two figures side by side that describe different lengths of time and
// invite them to be compared as though they did not.
//
// It is also long enough to be readable. A single tick swings hard: measured
// across one 30s transfer, consecutive samples read 0.1, 2.0, 100.2 and 60.1
// Mbit/s, and the airtime with them. The chart is the place to watch that
// texture; a headline figure that flickers is one nobody can read.
const ownAirWindow = 5 * time.Second

// ownAirtime is the share of each radio's time spent on its own clients,
// averaged over ownAirWindow, keyed by interface.
//
// SUMMED across the clients on a radio, because airtime is occupancy and two
// clients each holding the medium 40% of the time leave it busy 80% of the
// time. That total is the same quantity the stacked chart draws as its top
// edge, so the headline and the chart cannot disagree.
//
// A radio with no entry is one whose driver cannot attribute airtime at all --
// the onboard brcmfmac reports no per-station duration counters, and its
// clients would otherwise sum to a confident 0%. Absent, never zero.
func (e *Engine) ownAirtime(now time.Time, capable map[string]bool) map[string]float64 {
	// No ring at all is a legitimate state, not a bug to crash on: an Engine
	// built directly rather than through NewEngine has none, and buildBridgeState
	// is best-effort throughout -- every other absent piece there yields a
	// smaller answer rather than an error. This one panicked instead, from a
	// goroutine, which takes the whole daemon down rather than one field.
	if e == nil || e.hist == nil {
		return nil
	}
	snap := e.hist.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	from := now.Add(-ownAirWindow)
	out := map[string]float64{}
	for _, samples := range snap {
		// Which radio, and how much, from the SAMPLES rather than from the
		// client's current adapter. A device that roamed mid-window spent part
		// of it on each radio, and each sample already records which one
		// carried it -- so the airtime lands where it was actually spent.
		sum := map[string]float64{}
		n := map[string]int{}
		for _, s := range samples {
			if s.Iface == "" || s.T < from.UnixMilli() {
				continue
			}
			sum[s.Iface] += s.Air
			n[s.Iface]++
		}
		for iface, total := range sum {
			if !capable[iface] || n[iface] == 0 {
				continue
			}
			// The client's MEAN over the window, added to the radio's total.
			// Dividing by this client's own sample count rather than by the
			// window length matters for a device that arrived halfway through:
			// it held the medium for the part it was there, and averaging over
			// time it did not exist would report it as quieter than it was.
			out[iface] += total / float64(n[iface])
		}
	}
	for iface, v := range out {
		// A radio cannot be busy more than all of the time. Clamped rather than
		// trusted, for the same reason the per-client figure is.
		if v > 100 {
			out[iface] = 100
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
