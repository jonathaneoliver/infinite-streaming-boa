package boa

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

/*
 * WHICH DEVICE TALKED TO WHICH -- the question the port matrix can only gesture
 * at.
 *
 * portpairs.go answers "this radio forwarded to that wired port". On a box with
 * several devices per adapter that is a hint rather than an answer: two phones
 * on one radio and two machines on one switch collapse into a single ribbon
 * that says nothing about who was talking to whom. This counts the MAC pair
 * instead, so the ribbon names devices.
 *
 * ONE RULE, NOT ONE PER PAIR, and that is the whole reason this is affordable.
 * The port matrix is a rule per ordered pair because there are five ports and
 * the set is fixed. Devices are neither: twenty devices is 380 ordered pairs,
 * and a rule per pair is a linear scan of 380 comparisons on every forwarded
 * frame, growing as the square of a number the operator does not control.
 *
 * So the pairing goes in a DYNAMIC SET with a counter per element, keyed on the
 * concatenated source and destination MAC, and one rule updates it. The kernel
 * hashes the key: constant work per frame whatever the device count, and the
 * element for a pair is created the first time that pair is seen.
 *
 * Checked before it was relied on, because a set with a counter is not
 * universally available -- `nft add set ... { type ether_addr . ether_addr ;
 * flags dynamic ; counter ; }` was accepted on this box, nftables v1.1.3, and
 * the capability is probed at runtime rather than assumed from that.
 *
 * THE COST WAS MEASURED, alternating the rule in and out, because a box whose
 * purpose is not perturbing the traffic under test cannot take a forward-path
 * change on faith -- and the argument alone would not do even though it is a
 * strong one, since the port matrix also looked free by argument and one early
 * pair of runs there still read as a 14% penalty before alternation showed it
 * was noise. Wired client to a host beyond the WAN port, 2026-09-12, four runs
 * each way:
 *
 *	rule absent    940, 940, 940, 940 Mbit/s
 *	rule present   940, 940, 940, 940
 *
 * WHAT THAT DOES AND DOES NOT SHOW. It shows the rule does not stop the box
 * saturating a gigabit path: 940 is the practical TCP ceiling there and every
 * run hit it, with no variance in either arm. It does NOT show the cost is
 * zero, because a saturated link bounds the measurement -- any CPU cost inside
 * the box's remaining headroom is invisible to it. A test over Wi-Fi was tried
 * first and abandoned as useless: 18 alternating runs put the 95% interval at
 * -3.4% to +9.4%, because that radio's own rate moved between 88 and 208
 * Mbit/s across the session. Resolving a per-frame cost through that is not
 * possible, and a mean difference drawn from it would be noise reported as a
 * finding.
 *
 * WHAT A NON-CLIENT MAC MEANS, since most frames have one. Anything beyond this
 * box is reached through the upstream router, so every frame to or from the
 * internet carries that router's MAC on one side. Those are not separate
 * counterparties and are not drawn as such: a MAC that is not a tracked client
 * is reported as `beyondBox`, which covers the router and any device on the
 * bridge the box has not identified. The distinction between those two is not
 * knowable from a MAC pair alone, and inventing it would be worse than saying
 * so. A multicast or broadcast destination is separated out, because otherwise
 * ARP and mDNS -- which every device emits and nobody sent anywhere -- would be
 * indistinguishable from traffic that went upstream.
 */

// The two sentinel counterparties. Not MACs, and deliberately not MAC-shaped:
// they must not collide with a device, and a reader seeing one in a payload
// should not mistake it for an address.
const (
	// beyondBox is any MAC that is not a tracked client: the upstream router,
	// and anything on the bridge the box has not identified.
	beyondBox = "beyond-the-box"
	// groupMAC is a multicast or broadcast destination -- ARP, mDNS, the
	// discovery chatter every device emits to no one in particular.
	groupMAC = "broadcast"
)

// pairSet is the dynamic set holding one counter per MAC pair. It lives in the
// table portpairs.go owns, because the two are the same feature seen at two
// scopes and a second table would be a second thing to reconcile.
const pairSet = "macpairs"

// pairSetChain is this rule's OWN chain, and the separation is not tidiness.
//
// Two things forced it. `add rule` in nftables APPENDS unconditionally -- it is
// not idempotent, however identical the rule -- so adding it twice puts two
// update statements on the forward path and every frame counts into the set
// element twice. The only safe way to get exactly one is to flush a chain and
// add to it, which cannot be done in the port matrix's chain without
// destroying the port rules.
//
// And the port matrix FLUSHES ITS WHOLE TABLE on a port change, which takes
// every rule in it with it. Sharing a chain meant a hotplug silently stopped
// the device matrix: a figure that goes blank and says nothing, which is the
// failure mode CLAUDE.md names. It is still in the same table, so the flush
// still empties this chain -- see the reset in portPairs, which puts it back on
// the same tick.
const pairSetChain = "devpairs"

// pairSetSize caps how many pairs are counted at once.
//
// A BOUND IS REQUIRED, not prudent. A dynamic set grows an element per key the
// traffic presents, and the key includes addresses this box does not choose --
// a scan sweeping spoofed source addresses would otherwise grow it without
// limit on the forward path. At the cap the kernel refuses new elements and
// keeps counting the ones it has, which loses pairs rather than memory.
const pairSetSize = 4096

// pairSetTimeout expires an element that has not been matched.
//
// Ten minutes: long enough that a device idle between bursts keeps its history,
// short enough that a device that has left stops occupying the set. Without it
// the cap above is reached by accumulation rather than by activity, and the
// pairs retained would be the oldest rather than the live ones.
const pairSetTimeout = "10m"

// ClientPair is one ordered device pair's forwarding rate.
type ClientPair struct {
	// From and To are the source and destination, each either a client's MAC
	// or one of the two sentinels above. Resolving a MAC to a name is left to
	// the caller, which already holds the roster.
	From string `json:"from"`
	To   string `json:"to"`
	// Mbps and Packets are derived exactly as PortPair's are, and Packets is
	// not redundant for the same reason -- see that type.
	Mbps    float64 `json:"mbps"`
	Packets float64 `json:"packets"`
}

// nftSetDump is the subset of `nft -j list set` this needs.
//
// An element is either a bare value or, when the set carries a counter, an
// object with `val` and `counter`. Only the second shape is of interest, and
// the first is skipped rather than treated as an error: a set can legitimately
// hold an element whose counter has not been reported.
// Read from a whole-TABLE dump rather than `list set`, so one invocation
// answers both questions: what the counters say, and whether the rule that
// feeds them is still installed. Listing the set alone cannot answer the
// second -- a flushed table keeps the set definition and loses the rule, so
// the counters simply stop moving and nothing reports it.
type nftSetDump struct {
	Nftables []struct {
		Set *struct {
			Elem []json.RawMessage `json:"elem"`
		} `json:"set"`
		// Raw, because this only needs to know whether a rule MENTIONS the
		// set. Decoding the expression tree to recognise an update statement
		// would be more code to reach the same yes or no.
		Rule json.RawMessage `json:"rule"`
	} `json:"nftables"`
}

// nftSetElem is one counted element: a two-MAC concatenation and its counter.
type nftSetElem struct {
	Elem *struct {
		Val struct {
			Concat []string `json:"concat"`
		} `json:"val"`
		Counter *struct {
			Packets uint64 `json:"packets"`
			Bytes   uint64 `json:"bytes"`
		} `json:"counter"`
	} `json:"elem"`
}

// syncClientPairSet creates the set, its own chain, and the single rule.
//
// IDEMPOTENT BY FLUSHING, not by `add` being a no-op -- it is not one, see
// pairSetChain. The chain is emptied and exactly one rule added, so however
// often this runs the forward path carries one update statement.
//
// THE SET IS NOT FLUSHED, and that asymmetry is deliberate: flushing it would
// discard every counter, and unlike the port rules there is no event that
// invalidates the CONTENTS. Elements age out on their own timeout instead.
func (e *Engine) syncClientPairSet() error {
	script := fmt.Sprintf("add table bridge %s\n", pairTable) +
		fmt.Sprintf("add set bridge %s %s { type ether_addr . ether_addr ; flags dynamic ; counter ; size %d ; timeout %s ; }\n",
			pairTable, pairSet, pairSetSize, pairSetTimeout) +
		fmt.Sprintf("add chain bridge %s %s { type filter hook forward priority 0 ; }\n",
			pairTable, pairSetChain) +
		fmt.Sprintf("flush chain bridge %s %s\n", pairTable, pairSetChain) +
		// No verdict, like every rule in this table: it exists to count.
		fmt.Sprintf("add rule bridge %s %s update @%s { ether saddr . ether daddr }\n",
			pairTable, pairSetChain, pairSet)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isGroupMAC reports whether an address is multicast or broadcast.
//
// The low bit of the FIRST octet, which is the IEEE group bit -- not a
// comparison against ff:ff:ff:ff:ff:ff, which would catch broadcast and miss
// every multicast address mDNS and IPv6 neighbour discovery actually use.
func isGroupMAC(mac string) bool {
	if len(mac) < 2 {
		return false
	}
	var first uint8
	if _, err := fmt.Sscanf(mac[:2], "%02x", &first); err != nil {
		return false
	}
	return first&1 == 1
}

// clientPairs reads the MAC-pair counters and differences them into rates.
//
// CALLED FROM THE TICK, OUTSIDE e.mu, for exactly the reasons portPairs is --
// it spawns `nft` and reports through a mutex-taking notice. See the note at
// the lock in state.go, which cost a wedged box to write.
//
// known is the set of MACs the box is tracking, so a counterparty can be told
// from a router. Passed in rather than read from e.clients here, because that
// map is mutex-owned and this runs outside the lock.
func (e *Engine) clientPairs(now time.Time, known map[string]bool) []ClientPair {
	if !e.pairSetReady {
		if err := e.syncClientPairSet(); err != nil {
			// Reported through the SAME notice as the port matrix. Both fail
			// for the same reasons -- no nftables, no bridge family, a kernel
			// without dynamic sets -- and two notices for one cause would be
			// two lines for the operator to reconcile.
			e.notePairsBlocked(err.Error())
			return nil
		}
		e.pairSetReady = true
	}

	raw, err := exec.Command("nft", "-j", "list", "table", "bridge", pairTable).Output()
	if err != nil {
		e.pairSetReady = false // rebuild it next tick; the table may have gone
		e.notePairsBlocked(fmt.Sprintf("reading the device pair counters failed: %v", err))
		return nil
	}
	var dump nftSetDump
	if err := json.Unmarshal(raw, &dump); err != nil {
		e.notePairsBlocked(fmt.Sprintf("the device pair counters did not parse: %v", err))
		return nil
	}

	// IS THE RULE STILL THERE. Anything that flushes this table -- the port
	// matrix on a hotplug, a person with nft, another tool -- takes the rule
	// and leaves the set, and the counters then sit still while everything
	// looks healthy. Measured on the box: after `nft flush table bridge
	// boaflow` both matrices read zero and stayed there, because nothing
	// noticed. So the presence of the rule is checked on every read and the
	// chain is rebuilt when it has gone.
	installed := false
	for _, n := range dump.Nftables {
		if len(n.Rule) > 0 && strings.Contains(string(n.Rule), pairSet) {
			installed = true
			break
		}
	}
	if !installed {
		e.pairSetReady = false
		if err := e.syncClientPairSet(); err != nil {
			e.notePairsBlocked(err.Error())
			return nil
		}
		e.pairSetReady = true
		return nil // the counters restart from here; next tick has a baseline
	}

	// Rates are accumulated per RESOLVED pair, not per MAC pair, because many
	// MAC pairs collapse onto one: every conversation with the internet becomes
	// the same client against beyondBox. Summing before differencing would be
	// wrong -- the differencing is per counter -- so each element is
	// differenced on its own key and the rates are added afterwards.
	acc := make(map[string]*ClientPair)
	for _, n := range dump.Nftables {
		if n.Set == nil {
			continue
		}
		for _, rawElem := range n.Set.Elem {
			var el nftSetElem
			if err := json.Unmarshal(rawElem, &el); err != nil || el.Elem == nil {
				continue // a bare element, or a shape this does not read
			}
			if el.Elem.Counter == nil || len(el.Elem.Val.Concat) != 2 {
				continue
			}
			src, dst := el.Elem.Val.Concat[0], el.Elem.Val.Concat[1]
			key := src + ">" + dst
			mbps := e.rate("cpair-b/"+key, el.Elem.Counter.Bytes, now)
			pkts := e.rateRaw("cpair-p/"+key, el.Elem.Counter.Packets, now)
			if mbps == 0 && pkts == 0 {
				continue
			}
			from, to := resolveParty(src, known, false), resolveParty(dst, known, true)
			if from == to {
				continue // a party talking to itself is not a flow
			}
			rk := from + ">" + to
			p := acc[rk]
			if p == nil {
				p = &ClientPair{From: from, To: to}
				acc[rk] = p
			}
			p.Mbps += mbps
			p.Packets += pkts
		}
	}

	out := make([]ClientPair, 0, len(acc))
	for _, p := range acc {
		out = append(out, *p)
	}
	return out
}

// resolveParty maps one address to the thing the diagram should name.
//
// A tracked client keeps its MAC, which the caller turns into a name from the
// roster it already holds. Everything else becomes a sentinel: the group bit
// means nobody in particular, and an unknown unicast address is upstream or
// untracked, which a MAC pair cannot separate.
//
// dest says which side of the pair this is, because the group bit only means
// "sent to everyone" on a destination -- a source address with it set is
// malformed rather than broadcast, and calling it broadcast would put made-up
// traffic in the figure.
func resolveParty(mac string, known map[string]bool, dest bool) string {
	if known[strings.ToLower(mac)] {
		return strings.ToLower(mac)
	}
	if dest && isGroupMAC(mac) {
		return groupMAC
	}
	return beyondBox
}
