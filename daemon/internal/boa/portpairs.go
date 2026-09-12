package boa

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

/*
 * WHICH PORT A FRAME CAME IN BY AND WHICH IT LEFT BY -- the one thing interface
 * counters cannot tell you.
 *
 * Those give each port's total: a row sum and a column sum, never the matrix.
 * So "is this client talking to the internet or to the device next to it" is
 * unanswerable from them, and on a transparent bridge it is exactly the
 * question nothing else can answer either -- the box is not a hop, so it
 * appears in no client's own view of the network.
 *
 * A COUNTER RULE PER ORDERED PAIR, in the nftables BRIDGE family, on the
 * forward hook. No verdict, so nothing about forwarding changes; the rule
 * exists only to be counted. Chosen over the two alternatives after measuring:
 *
 *	conntrack   would give per-flow detail, but inside the container it has
 *	            byte accounting OFF, no procfs interface and zero tracked
 *	            connections -- and it puts per-flow STATE on the forward path.
 *	tc filters  would need ingress classification on the WAN, which
 *	            DATA-CONTRACT.md rejected for shaping and rejects here.
 *
 * THE COST WAS MEASURED, NOT ASSUMED, because a box whose purpose is not
 * perturbing the traffic under test cannot take this on faith. Twenty rules
 * across five ports, alternating three runs each way through the box on
 * 2026-09-12:
 *
 *	rules off   366, 362, 402 Mbit/s   mean 377
 *	rules on    368, 423, 364          mean 385
 *
 * No effect: the within-arm spread is 40 and 59 Mbit/s against an 8 Mbit/s
 * difference between arms, with the rules nominally faster. One earlier pair of
 * runs read 311 against 362 and looked like a 14% penalty; it was noise, and
 * the arithmetic says it had to be -- forty comparisons per frame at ~1,300
 * frames a second is microseconds of CPU.
 *
 * The reason it is free at all is worth recording, because it would NOT be on a
 * box configured differently: `bridge-nf-call-iptables` is already 1 here, so
 * bridged frames were traversing netfilter before this table existed. Where
 * that hook is not already installed, the first rule would pay for registering
 * it -- and the known penalty there is aggregated frames being segmented for
 * inspection, which is a per-packet cost multiplier rather than a per-rule one.
 */

// pairTable is the nftables table this owns, entirely. Nothing else writes it,
// so reconciling means making it match and never merging with what it finds.
const pairTable = "boaflow"

// PortPair is one ordered port pair's forwarding rate.
type PortPair struct {
	// From and To are bridge port names: in by From, out by To.
	From string `json:"from"`
	To   string `json:"to"`
	// Mbps is the rate between polls, derived the same way every other
	// throughput figure here is.
	Mbps float64 `json:"mbps"`
	// Packets is the rate in frames per second, and it is not redundant.
	//
	// The two disagree by more than a constant: measured on this box, the
	// uplink-to-radio direction ran 19,634 frames for 700 MB -- 35 KB apiece,
	// which is GRO aggregation -- while the return direction ran 96,073 frames
	// for 5.5 MB, 58 bytes apiece, being pure ACKs. A ribbon drawn from bytes
	// alone makes the second look like nothing when it is three times the
	// frame count of the first, and frames are what cost a radio airtime.
	Packets float64 `json:"packets"`
}

// nftRuleset is the subset of `nft -j list table` this needs.
type nftRuleset struct {
	Nftables []struct {
		Rule *struct {
			Expr []struct {
				Match *struct {
					Left struct {
						Meta *struct {
							Key string `json:"key"`
						} `json:"meta"`
					} `json:"left"`
					Right string `json:"right"`
				} `json:"match"`
				Counter *struct {
					Packets uint64 `json:"packets"`
					Bytes   uint64 `json:"bytes"`
				} `json:"counter"`
			} `json:"expr"`
		} `json:"rule"`
	} `json:"nftables"`
}

// bridgePorts is every port a frame can be forwarded between.
//
// THE SCANNER IS ABSENT, and not by oversight: a listen-only radio is given no
// hostapd config, and it is hostapd's `bridge=` line that makes a radio a
// bridge port. It can forward nothing, so a pair naming it could only ever
// read zero and would widen the rule set for nothing.
func (c Config) bridgePorts() []string {
	out := make([]string, 0, 1+len(c.WlanPorts)+len(c.LanPorts))
	if c.WANPort != "" {
		out = append(out, c.WANPort)
	}
	out = append(out, c.WlanPorts...)
	out = append(out, c.LanPorts...)
	sort.Strings(out)
	return out
}

// syncPairRules makes the table match the current port set, and reports
// whether it had to change anything.
//
// REBUILT WHOLESALE rather than diffed. The port set changes on a hotplug and
// at no other time, so a rebuild costs one flush and twenty adds on an event
// that already restarts hostapd -- where a diff would be more code carrying
// the risk of a rule the daemon has forgotten it owns.
func (e *Engine) syncPairRules(ports []string) error {
	if len(ports) < 2 {
		return nil // nothing can be forwarded between fewer than two ports
	}
	// One batch, fed to `nft -f -`: twenty separate invocations is twenty
	// process spawns per hotplug, and a partial failure would leave a table
	// that is neither the old set nor the new one.
	script := fmt.Sprintf("add table bridge %s\n", pairTable) +
		fmt.Sprintf("flush table bridge %s\n", pairTable) +
		fmt.Sprintf("add chain bridge %s pairs { type filter hook forward priority 0 ; }\n", pairTable)
	for _, a := range ports {
		for _, b := range ports {
			if a == b {
				continue
			}
			script += fmt.Sprintf(
				"add rule bridge %s pairs iifname %q oifname %q counter\n",
				pairTable, a, b)
		}
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// portPairs reads the per-pair counters and differences them into rates.
//
// CALLED FROM THE TICK, OUTSIDE e.mu, and the distinction is load-bearing: it
// spawns `nft`, and it reports failures through notePairsBlocked which takes
// that same mutex. Called from within the locked section it deadlocks the tick
// against itself -- measured, see the note at the lock in state.go.
//
// e.prev is still safe to touch here. It is tick-owned rather than
// mutex-owned, and this is the tick.
//
// Best effort, and LOUD about it once. A box without nftables, or one whose
// kernel has no bridge family, simply has no pair data and the interface draws
// no flow diagram -- but it says so rather than showing an empty figure that
// looks like idleness.
func (e *Engine) portPairs(now time.Time) []PortPair {
	ports := e.cfg.bridgePorts()
	if len(ports) < 2 {
		return nil
	}
	// The rule set follows the ports. Checked every tick and rebuilt only when
	// the set actually differs, so the common case is one string compare.
	key := fmt.Sprint(ports)
	if e.pairPortsKey != key {
		if err := e.syncPairRules(ports); err != nil {
			e.notePairsBlocked(err.Error())
			return nil
		}
		e.pairPortsKey = key
	}

	raw, err := exec.Command("nft", "-j", "list", "table", "bridge", pairTable).Output()
	if err != nil {
		e.notePairsBlocked(fmt.Sprintf("reading the pair counters failed: %v", err))
		return nil
	}
	var rs nftRuleset
	if err := json.Unmarshal(raw, &rs); err != nil {
		e.notePairsBlocked(fmt.Sprintf("the pair counters did not parse: %v", err))
		return nil
	}
	e.notePairsBlocked("")

	out := make([]PortPair, 0, len(ports)*(len(ports)-1))
	for _, n := range rs.Nftables {
		if n.Rule == nil {
			continue
		}
		var in, eg string
		var bytes, pkts uint64
		for _, ex := range n.Rule.Expr {
			if m := ex.Match; m != nil && m.Left.Meta != nil {
				switch m.Left.Meta.Key {
				case "iifname":
					in = m.Right
				case "oifname":
					eg = m.Right
				}
			}
			if c := ex.Counter; c != nil {
				bytes, pkts = c.Bytes, c.Packets
			}
		}
		if in == "" || eg == "" {
			continue
		}
		k := in + ">" + eg
		out = append(out, PortPair{
			From:    in,
			To:      eg,
			Mbps:    e.rate("pair-b/"+k, bytes, now),
			Packets: e.rateRaw("pair-p/"+k, pkts, now),
		})
	}
	return out
}

// notePairsBlocked reports why there is no pair data, on the EDGE.
//
// Same shape and the same reason as noteScanBlocked: the tick runs once a
// second and these conditions persist -- no nftables, no bridge family -- so a
// line per tick would be sixty a minute for ever. Silence is not the
// alternative; an interface drawing no flow diagram and saying nothing is
// indistinguishable from a box with no traffic.
func (e *Engine) notePairsBlocked(reason string) {
	e.mu.Lock()
	was := e.pairsBlocked
	e.pairsBlocked = reason
	e.mu.Unlock()
	if reason == was {
		return
	}
	if reason != "" {
		e.logEvent(EventWarning, "", "",
			"per-port-pair traffic cannot be counted, so the flow view is unavailable: %s",
			reason)
		return
	}
	e.logEvent(EventAction, "", "", "per-port-pair traffic is being counted again")
}
