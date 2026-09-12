package boa

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

/*
 * What crossed each bridge port, from the kernel's own interface counters.
 *
 * WHY NOT THE tc CLASS COUNTERS, which every other throughput figure on this
 * box comes from: a class only ever sees what its filter matched. Measured on
 * the container host 2026-09-12, on the WAN port:
 *
 *	class 1:1  (default, matched by nothing)   112,693,736 bytes
 *	class 1:10 (the busiest client)             12,268,732 bytes
 *
 * Nine tenths of what crossed that port belonged to no client, so summing the
 * per-client series to get a port total understates it by an order of
 * magnitude. The interface counter has no attribution step to go wrong: it is
 * every frame, including the box's own traffic, untracked devices, broadcast
 * and multicast.
 *
 * That is the whole point of this file. "Is the WAN the bottleneck" cannot be
 * answered from per-client numbers, and it is the one question a transparent
 * bridge makes hard to see -- the box is not a hop, so nothing about it appears
 * in a client's own view of the network.
 */

// PortFlow is one bridge port's total throughput, both directions.
type PortFlow struct {
	Iface string `json:"iface"`
	// Role is the same closed set IfaceInfo.Role uses, so the interface can
	// draw a WAN band differently from a radio without re-deriving which is
	// which. See the Role constants in bridgeinfo.go.
	Role string `json:"role"`
	// DownMbps is traffic moving TOWARD THE CLIENTS and UpMbps away from them,
	// on every port, which is NOT the same as tx and rx.
	//
	// The mapping flips on the WAN, and this is the trap the whole type exists
	// to contain. On a radio or a wired downstream port, a packet heading for a
	// client is transmitted; on the WAN the same packet is received. Read both
	// off `tx` and the WAN band comes out inverted against every other band on
	// the chart -- a picture that looks entirely plausible and is backwards.
	DownMbps float64 `json:"down_mbps"`
	UpMbps   float64 `json:"up_mbps"`
	// UnattributedUpMbps is the part of this port's egress that no client
	// filter claimed: the box's own traffic, plus any device it is not
	// tracking. Set on the WAN only, and zero elsewhere.
	//
	// EXACT, not derived. It is the HTB default class read at the same point,
	// in the same units and on the same tick as the per-client classes beside
	// it -- `tc qdisc show dev wan0` reports `default 0x1`, so everything no
	// filter matched is accounted there and nowhere else. Measured on the
	// container host 2026-09-12: 112,693,736 bytes in that class against
	// 12,268,732 for the busiest client, so this is not a rounding term.
	//
	// THERE IS NO INBOUND EQUIVALENT AND NONE IS OFFERED. Downlink is shaped
	// on each client's own port, so `wan0` has no ingress classes to decompose
	// and the only honest inbound figure is the interface total. Subtracting
	// the client classes on the radios from it would mix counting points --
	// tc on a Wi-Fi port against an Ethernet interface counter, which do not
	// frame alike -- and produce a number that looks exact and is not. The
	// asymmetry is the measurement's, and it is left visible rather than
	// papered over.
	UnattributedUpMbps float64 `json:"unattributed_up_mbps,omitempty"`
	// SpeedMbps is the port's negotiated link rate, 0 when it has none.
	//
	// Carried beside the throughput because it is the only thing that makes
	// the throughput mean anything: 400 Mbit/s is idle on a 2.5 GbE port and
	// saturation on a 100 Mbit one. It is what turns this from a trace into an
	// answer about a bottleneck.
	SpeedMbps int `json:"speed_mbps,omitempty"`
}

// readIfaceBytes returns one interface's cumulative byte counters.
//
// Wire bytes as the kernel counts them, so these include the Ethernet, IP and
// TCP framing that an application's payload count does not -- the +4.6% on
// IPv4 that DATA-CONTRACT.md works out, measured at 4.9% on this box.
func readIfaceBytes(name string) (tx, rx uint64, ok bool) {
	base := filepath.Join("/sys/class/net", name, "statistics")
	t, terr := strconv.ParseUint(
		strings.TrimSpace(readSysfs(filepath.Join(base, "tx_bytes"))), 10, 64)
	r, rerr := strconv.ParseUint(
		strings.TrimSpace(readSysfs(filepath.Join(base, "rx_bytes"))), 10, 64)
	if terr != nil || rerr != nil {
		// An absent port is the normal case, not a fault: a box with one radio
		// and no USB ethernet has several configured names that do not exist.
		return 0, 0, false
	}
	return t, r, true
}

// htbDefaultMinor is the class every packet no filter claimed lands in.
//
// 1, because the root qdisc is created with `default 0x1` -- and the minor is
// HEXADECIMAL, which is the trap CLAUDE.md names: the twentieth class is 1:14,
// not 1:20. Reading the wrong minor here would report a client's traffic as
// the box's own.
const htbDefaultMinor = 1

// portFlows differences every bridge port's counters into Mbit/s.
//
// CALLED FROM THE TICK, INSIDE e.mu, because it reuses Engine.rate and with it
// e.prev -- which is tick-owned and unlocked. The background bridge rebuild
// runs on its own goroutine (see BridgeState), so computing this there instead
// would race e.prev against the tick for no gain: the client series are sampled
// on this clock, and two series drawn on one x-axis have to share it.
//
// The BRIDGE ITSELF IS DELIBERATELY ABSENT. br-lan spans every port, so
// including it would roughly double every total on the chart while looking like
// a legitimate extra band.
//
// wanClasses is the WAN port's per-class statistics as the tick already read
// them, used for the unattributed split. Nil is fine: the split is then absent
// rather than zero, which is the right answer on a box whose shaper is not up.
func (e *Engine) portFlows(now time.Time, wanClasses map[int]Counters) []PortFlow {
	// WAN first, then the radios, then the wired ports: upstream at the top,
	// the same order roleOrder gives the interface list, so the legend does not
	// disagree with the diagram about which end is which.
	type port struct {
		name string
		role string
	}
	var want []port
	if e.cfg.WANPort != "" {
		want = append(want, port{e.cfg.WANPort, RoleWAN})
	}
	for _, w := range e.cfg.WlanPorts {
		want = append(want, port{w, RoleAP})
	}
	for _, s := range e.cfg.ScanPorts {
		want = append(want, port{s, RoleScanner})
	}
	for _, l := range e.cfg.LanPorts {
		want = append(want, port{l, RoleLAN})
	}

	out := make([]PortFlow, 0, len(want))
	for _, p := range want {
		tx, rx, ok := readIfaceBytes(p.name)
		if !ok {
			continue
		}
		// Distinct key prefixes so these cannot collide with the per-class
		// rates, which key on "d/<minor>" and "u/<minor>".
		txRate := e.rate("port-tx/"+p.name, tx, now)
		rxRate := e.rate("port-rx/"+p.name, rx, now)
		f := PortFlow{Iface: p.name, Role: p.role, SpeedMbps: linkSpeedMbps(p.name)}
		// The flip. See PortFlow.DownMbps.
		if p.role == RoleWAN {
			f.DownMbps, f.UpMbps = rxRate, txRate
			// What left the uplink without belonging to any client. Egress
			// only; see UnattributedUpMbps for why there is no inbound twin.
			if c, ok := wanClasses[htbDefaultMinor]; ok {
				f.UnattributedUpMbps = e.rate("port-unattr/"+p.name, c.Bytes, now)
			}
		} else {
			f.DownMbps, f.UpMbps = txRate, rxRate
		}
		out = append(out, f)
	}
	return out
}

// linkSpeedMbps reads a port's negotiated rate, 0 when it has none.
//
// Wireless interfaces have no `speed` file at all -- the figure that would
// answer "how fast is this radio" is the per-client PHY rate, which moves per
// frame and is already carried on the client. Returning 0 here rather than
// guessing is what stops a chart drawing a radio's band against a ceiling
// nobody measured.
func linkSpeedMbps(name string) int {
	s := strings.TrimSpace(readSysfs(filepath.Join("/sys/class/net", name, "speed")))
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
