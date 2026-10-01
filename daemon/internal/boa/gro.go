package boa

import (
	"fmt"
	"os"
	"strings"
)

// GRO OFF UNDER A LOW CAP (#475, #476).
//
// GRO merges back-to-back frames arriving on an interface into one packet of up
// to 64 KB, and the shaper then handles that packet as ONE: netem charges it the
// right time for its length, but releases it in a single lump, and counts it as
// one packet against its limit. Measured on the Cudy TR3000, OpenWrt 25.12.5,
// 2026-09-30, a MacBook uploading at a 1 Mbps cap through an RTL8156 on eth2:
//
//	tcpdump on eth0 egress: 40 packets in 9.6 s, up to 44,888 bytes each
//	the shaper, per 0.25 s: 0 to 1.74 Mbps, zero windows with 36-48 packets
//	                        waiting; exact on average
//	a "17 packet" queue:    17 lumps of 45 KB is ~6 s at 1 Mbps, not 0.2 s
//
// `tc -s` hid it: its packet counter counts a merged packet's segments, so
// bytes/packets averaged 1507 and looked like ordinary frames.
//
// With GRO off on the arrival port, same cap, same sender:
//
//	                 evenness (cv)  goodput   latency
//	GRO on,  deep        0.49        fails     9.3 s
//	GRO off, 17 pkts     0.02        -5.4 %    166 ms   <- what a 1 Mbps link does
//	GRO on,  5 Mbps, 83  0.10       -35.1 %    2.0 s
//	GRO off, 5 Mbps, 83  0.00        -5.2 %    170 ms
//
// The access point interface merges too (14 KB lumps over Wi-Fi; off: 465 ->
// 162 ms), and so does the WAN port for downlink (60 Mbps: 486 -> 172 ms).
//
// IT COSTS CPU, which is why it is not always off. Without merging the Cudy's
// two cores carried 574 Mbps of uplink against 925 with it, and a 500 Mbps cap
// fell 8.7 % short. So GRO goes off on a port only while a cap below
// groOffBelowMbps runs through it -- the operator's threshold -- and comes back
// when none does. Above it a port keeps merging, and its uplink keeps the deep
// queue a merging port needs.
//
// WHICH PORT, by where the traffic ARRIVES, because that is where GRO acts:
// an uplink cap needs the client's own port, a downlink cap the WAN port.
const groOffBelowMbps = 30.0

func lowCap(rateMbps float64) bool { return rateMbps > 0 && rateMbps < groOffBelowMbps }

// groPorts is the set of interfaces that must not merge for this desired set.
func groPorts(want []Desired, wan string) map[string]bool {
	out := map[string]bool{}
	for _, w := range want {
		if w.IP == "" && len(w.IPv6) == 0 {
			continue // not shaped, so nothing to make accurate
		}
		if lowCap(w.Up.RateMbps) && w.Port != "" {
			out[w.Port] = true
		}
		if lowCap(w.Down.RateMbps) && wan != "" {
			out[wan] = true
		}
	}
	return out
}

// groHold records a port boa turned GRO off on, and which incarnation of it.
// An access point's interface is recreated whenever hostapd restarts or a
// channel moves, and comes back merging; the ifindex is how that is noticed
// without asking the kernel for GRO state every tick.
type groHold struct{ ifindex string }

func ifindexOf(dev string) string {
	b, err := os.ReadFile("/sys/class/net/" + dev + "/ifindex")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// syncGRO turns GRO off on every port in want and back on for every port boa
// turned off that is no longer wanted. It returns the ports that are, as far as
// the kernel says, not merging -- only those get a time-sized uplink queue.
// Callers hold s.mu.
func (s *Shaper) syncGRO(want map[string]bool) map[string]bool {
	if s.gro == nil {
		s.gro = map[string]groHold{}
		s.groWarned = map[string]bool{}
	}
	off := map[string]bool{}
	for dev := range want {
		idx := ifindexOf(dev)
		if h, ok := s.gro[dev]; ok && idx != "" && h.ifindex == idx {
			off[dev] = true
			continue
		}
		on, err := groGet(dev)
		if err != nil {
			s.warnGRO(dev, err)
			continue
		}
		if !on {
			// Already off. Ours from an earlier incarnation, or someone else's
			// choice: either way it is not merging, and only what boa turned
			// off is boa's to turn back on.
			if _, ours := s.gro[dev]; ours {
				s.gro[dev] = groHold{ifindex: idx}
			}
			off[dev] = true
			continue
		}
		if err := groSet(dev, false); err != nil {
			s.warnGRO(dev, err)
			continue
		}
		// CHECKED, not assumed: a driver can report success and keep the bit.
		if still, err := groGet(dev); err != nil || still {
			s.warnGRO(dev, fmt.Errorf("GRO still on after turning it off (%v)", err))
			continue
		}
		s.gro[dev] = groHold{ifindex: idx}
		delete(s.groWarned, dev)
		off[dev] = true
		fmt.Printf("infinite-streaming-boa: shaping: GRO off on %s, so a cap below %.0f Mbps is paced per frame\n",
			dev, groOffBelowMbps)
	}
	for dev := range s.gro {
		if want[dev] {
			continue
		}
		s.restoreGRO(dev)
	}
	return off
}

// restoreGRO turns GRO back on for a port boa turned off. A port that has gone,
// or been recreated, already merges again, so there is nothing to undo.
func (s *Shaper) restoreGRO(dev string) {
	h := s.gro[dev]
	delete(s.gro, dev)
	if idx := ifindexOf(dev); idx == "" || idx != h.ifindex {
		return
	}
	if err := groSet(dev, true); err != nil {
		fmt.Printf("infinite-streaming-boa: shaping: could not turn GRO back on for %s: %v\n", dev, err)
		return
	}
	fmt.Printf("infinite-streaming-boa: shaping: GRO back on for %s: no cap below %.0f Mbps uses it\n",
		dev, groOffBelowMbps)
}

// warnGRO says once per port why it is still merging, because the consequence
// -- a low cap released in lumps, with seconds of uplink latency -- is otherwise
// invisible from everything but tcpdump.
func (s *Shaper) warnGRO(dev string, err error) {
	if s.groWarned[dev] {
		return
	}
	s.groWarned[dev] = true
	fmt.Printf("infinite-streaming-boa: shaping: %s keeps GRO, so caps below %.0f Mbps through it are released in lumps: %v\n",
		dev, groOffBelowMbps, err)
}
