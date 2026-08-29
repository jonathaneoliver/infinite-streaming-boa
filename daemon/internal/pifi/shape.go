package pifi

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Shaper owns the kernel's queueing configuration.
//
// # Where each direction is shaped, and why
//
// Both directions are shaped on a TRUE EGRESS queue, on the last interface the
// packet crosses before leaving pifi:
//
//	downlink (internet -> client): egress of the CLIENT'S OWN port (wlan0/lan0)
//	uplink   (client -> internet): egress of the WAN port
//
// Downlink accuracy is the priority, because the common use is throttling
// streaming video on its way to a player. Shaping it on the client's own port
// means the shaper is the last thing to touch the packet before the client
// receives it, so the inter-packet spacing the player measures is exactly the
// spacing that was configured. The alternative -- catching traffic at WAN
// ingress and redirecting it to an ifb device -- also limits the rate, but the
// packet then still traverses the bridge and the client port's own queue, which
// can re-bunch what the shaper carefully spaced out.
//
// A consequence worth knowing: the downlink class lives on whichever port the
// client is currently attached to, so it MOVES when a device roams between
// Wi-Fi and the wired port. The reconciler handles that as an ordinary change.
//
// # What actually enforces the rate
//
// netem's own `rate`, not HTB. HTB is a token bucket: while a class is idle it
// accumulates credit, and when traffic resumes it releases a burst at full line
// rate before pacing takes effect. That is exactly the moment a video player
// starts a segment and measures throughput, so a token bucket systematically
// inflates the player's bandwidth estimate at the start of every segment --
// precisely the measurement under test.
//
// netem's rate model has no credit to accumulate. It computes each packet's
// serialisation time from its length and spaces packets accordingly, which is
// what a real slow link does. HTB is therefore kept purely as a classifier and
// a per-client byte counter, with its rate set to line rate so it never binds.
type Shaper struct {
	mu     sync.Mutex
	wan    string
	bridge string

	ready  bool
	reason string

	ports   map[string]bool // interfaces whose root qdisc we have created
	minors  map[string]int
	nextMin int
	applied map[string]rule

	// exempt is the set of local addresses currently excused from shaping.
	// Tracked so the filters are only rewritten when the set actually changes
	// -- the bridge takes its address by DHCP, so it can change while running.
	exempt []string
}

// rule is the exact state installed for one class. Keeping it lets the
// reconciler skip unchanged entries: re-issuing an identical command is not
// free, and recreating a class would zero the byte counters the throughput
// graphs integrate.
type rule struct {
	minor    int
	ip       string
	downPort string // where the downlink class currently lives
	match    Match
	down, up Shape
	haveDown bool
	haveUp   bool
}

const (
	defaultMinor = 1
	firstMinor   = 16

	// Sub-class filters must be evaluated BEFORE the device default, or the
	// default's match-anything-to-this-address rule would swallow traffic a
	// more specific rule was meant to claim. Lower number wins in tc.
	prefSubBase     = 10000
	prefDefaultBase = 40000

	// HTB is a container here, never a limiter, so its rate is set high enough
	// never to bind on any interface a Pi has.
	containerMbps = 10000.0

	mtuBytes = 1500
)

func NewShaper(wan, bridge string) *Shaper {
	return &Shaper{
		wan: wan, bridge: bridge, ports: map[string]bool{},
		minors: map[string]int{}, applied: map[string]rule{}, nextMin: firstMinor,
	}
}

// prefExempt is below every policy filter (sub-classes start at 10000, device
// defaults at 40000), so management traffic is classified before any rule the
// operator wrote can claim it.
const prefExempt = 50

// localIPv4 returns the addresses belonging to the box itself.
func (s *Shaper) localIPv4() []string {
	var out []string
	for _, name := range []string{s.bridge, s.wan} {
		ifi, err := net.InterfaceByName(name)
		if err != nil {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, n.IP.String())
			}
		}
	}
	sort.Strings(out)
	return out
}

// writeExemptions excuses traffic ORIGINATING FROM this box from shaping.
//
// Without this, the web interface throttles itself. A reply from pifi to a
// client leaves via that client's own port, where the downlink filter matches
// on destination address -- so the dashboard's own responses, and the SSE
// stream, get conditioned by whatever policy the operator just set from it.
// Capping a phone hard enough would leave the interface needed to undo it
// crawling. The same applies to SSH from a shaped device.
//
// Only the source address is matched: traffic from the box is management,
// traffic merely addressed TO the box is a client's own upload and stays
// subject to its policy.
func (s *Shaper) writeExemptions(dev string, ips []string) {
	for i := range ips {
		tcQuiet("filter", "del", "dev", dev, "parent", "1:", "pref",
			fmt.Sprint(prefExempt+i))
	}
	for i, ip := range ips {
		if err := tc("filter", "add", "dev", dev, "protocol", "ip",
			"parent", "1:", "prio", fmt.Sprint(prefExempt+i), "u32",
			"match", "ip", "src", ip+"/32",
			"flowid", fmt.Sprintf("1:%x", defaultMinor)); err != nil {
			fmt.Printf("infinite-streaming-pifi: could not exempt %s on %s: %v\n", ip, dev, err)
		}
	}
}

func (s *Shaper) Ready() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready, s.reason
}

func (s *Shaper) WAN() string { return s.wan }

func tc(args ...string) error {
	out, err := exec.Command("tc", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tc %s: %v: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tcQuiet runs a command whose failure is expected and uninteresting, such as
// deleting a qdisc that was never created.
func tcQuiet(args ...string) { _ = exec.Command("tc", args...).Run() }

// ensurePort installs a root HTB on an interface the first time a class needs
// to live there. Ports appear over time -- lan0 only exists once a USB adapter
// is plugged in -- so this cannot all happen at startup.
func (s *Shaper) ensurePort(dev string) error {
	if s.ports[dev] {
		return nil
	}
	if exec.Command("ip", "link", "show", dev).Run() != nil {
		return fmt.Errorf("interface %s does not exist", dev)
	}
	tcQuiet("qdisc", "del", "dev", dev, "root")
	if err := tc("qdisc", "add", "dev", dev, "root", "handle", "1:",
		"htb", "default", fmt.Sprintf("%x", defaultMinor)); err != nil {
		return err
	}
	// The catch-all class counts traffic belonging to no policy, which makes
	// "how much of this link is unmanaged" visible rather than invisible.
	if err := s.writeClass(dev, defaultMinor, false); err != nil {
		return err
	}
	s.ports[dev] = true
	s.writeExemptions(dev, s.localIPv4())
	return nil
}

// Setup prepares the WAN port, which is the one interface guaranteed to exist.
func (s *Shaper) Setup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready, s.reason = false, ""
	s.applied = map[string]rule{}
	s.ports = map[string]bool{}

	if err := s.ensurePort(s.wan); err != nil {
		s.reason = "cannot prepare WAN port " + s.wan + ": " + err.Error()
		return fmt.Errorf("%s", s.reason)
	}
	s.ready = true
	return nil
}

func (s *Shaper) Teardown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for dev := range s.ports {
		tcQuiet("qdisc", "del", "dev", dev, "root")
	}
	s.ports = map[string]bool{}
	s.ready = false
}

// writeClass creates or updates the HTB container. `change` is preferred over
// `replace` on an existing class because it preserves the byte counters the
// throughput graphs integrate; a recreate zeroes them and puts a false cliff in
// the chart on every slider movement.
func (s *Shaper) writeClass(dev string, minor int, exists bool) error {
	bits := uint64(containerMbps * 1e6)
	verb := "add"
	if exists {
		verb = "change"
	}
	// quantum is set explicitly because the kernel derives it from rate/r2q and
	// warns at the extremes; at container rate the derived value is far too big.
	return tc("class", verb, "dev", dev, "parent", "1:",
		"classid", fmt.Sprintf("1:%x", minor),
		"htb", "rate", fmt.Sprintf("%dbit", bits),
		"ceil", fmt.Sprintf("%dbit", bits),
		"quantum", "60000")
}

// netemLimit sizes the netem queue from the bandwidth-delay product.
//
// This is the most damaging default in the stack. netem queues 1000 packets
// unless told otherwise; a 50 Mbps link with 500 ms of delay holds roughly 2100
// packets in flight, so the default would silently discard half the traffic
// while the UI truthfully reported "0% loss configured", and the tester would
// spend an afternoon blaming the player.
func netemLimit(sh Shape) int {
	rate := sh.RateMbps
	if rate <= 0 {
		rate = 1000 // unlimited: size for a gigabit link's worth of in-flight data
	}
	bdpPackets := rate * 1e6 * (sh.DelayMs + sh.JitterMs) / 1000.0 / (8.0 * mtuBytes)
	limit := int(math.Ceil(bdpPackets * 3))
	if limit < 1000 {
		limit = 1000
	}
	if limit > 200000 {
		limit = 200000 // past here the memory cost outweighs the clipping
	}
	return limit
}

// writeNetem attaches or updates the impairment leaf, which is what actually
// enforces the rate. Returns whether a netem qdisc is present afterwards.
//
// Unit conversion happens here and nowhere else: the API speaks megabits,
// milliseconds and percent; tc's command line takes those same literals; tc's
// JSON readback reports bytes/sec, seconds and fractions. One conversion site
// is what stops a factor-of-1000 error surfacing in a graph six months later.
func (s *Shaper) writeNetem(dev string, minor int, sh Shape, exists bool) (bool, error) {
	// Class ids and handles are HEXADECIMAL to tc, both on input and in its
	// JSON output. Formatting them as decimal creates a class whose real id is
	// the hex reading of those digits (1:16 becomes 0x16, decimal 22), so every
	// counter lookup then misses and silently reports zero.
	handle := fmt.Sprintf("%x:", 0x100+minor)
	parent := fmt.Sprintf("1:%x", minor)

	if sh.IsClean() {
		if exists {
			tcQuiet("qdisc", "del", "dev", dev, "parent", parent)
		}
		return false, nil
	}

	verb := "add"
	if exists {
		verb = "change"
	}
	args := []string{"qdisc", verb, "dev", dev, "parent", parent,
		"handle", handle, "netem", "limit", fmt.Sprint(netemLimit(sh))}

	if sh.RateMbps > 0 {
		// Expressed in bits so the kernel does its own rounding rather than
		// receiving an already-rounded byte figure.
		args = append(args, "rate", fmt.Sprintf("%dbit", uint64(sh.RateMbps*1e6)))
	}
	if sh.DelayMs > 0 || sh.JitterMs > 0 {
		args = append(args, "delay", fmt.Sprintf("%.3fms", sh.DelayMs))
		if sh.JitterMs > 0 {
			// A normal distribution makes jitter behave like real network
			// variance rather than a uniform smear.
			args = append(args, fmt.Sprintf("%.3fms", sh.JitterMs),
				"distribution", "normal")
		}
	}
	if sh.LossPct > 0 {
		args = append(args, "loss", fmt.Sprintf("%.4f%%", sh.LossPct))
	}
	if err := tc(args...); err != nil {
		return exists, err
	}
	return true, nil
}

// matchArgs builds the u32 selector for one class in one direction.
//
// The direction flip is the subtle part. A sub-class is written by the operator
// in terms of the SERVICE being talked to -- "port 443", "this CDN's network".
// On the uplink that service is the packet's destination; on the downlink the
// very same service is its SOURCE. Matching dst in both directions would
// silently catch nothing on the downlink, and the rule would look configured
// while conditioning zero bytes.
func matchArgs(clientIP string, m Match, downlink bool) []string {
	var a []string
	if downlink {
		a = append(a, "match", "ip", "dst", clientIP+"/32")
		if m.DstCIDR != "" {
			a = append(a, "match", "ip", "src", m.DstCIDR)
		}
		if m.DstPort != 0 {
			a = append(a, "match", "ip", "sport", fmt.Sprint(m.DstPort), "0xffff")
		}
	} else {
		a = append(a, "match", "ip", "src", clientIP+"/32")
		if m.DstCIDR != "" {
			a = append(a, "match", "ip", "dst", m.DstCIDR)
		}
		if m.DstPort != 0 {
			a = append(a, "match", "ip", "dport", fmt.Sprint(m.DstPort), "0xffff")
		}
	}
	switch strings.ToLower(m.Protocol) {
	case "tcp":
		a = append(a, "match", "ip", "protocol", "6", "0xff")
	case "udp":
		a = append(a, "match", "ip", "protocol", "17", "0xff")
	}
	return a
}

// pref gives every class a unique filter priority, so one filter can be deleted
// by priority alone without first discovering its kernel-assigned handle.
func pref(minor int, isSub bool) int {
	if isSub {
		return prefSubBase + minor
	}
	return prefDefaultBase + minor
}

func (s *Shaper) writeFilter(dev string, minor int, ip string, m Match, isSub, downlink bool) error {
	p := fmt.Sprint(pref(minor, isSub))
	tcQuiet("filter", "del", "dev", dev, "parent", "1:", "pref", p)
	args := append([]string{"filter", "add", "dev", dev, "protocol", "ip",
		"parent", "1:", "prio", p, "u32"}, matchArgs(ip, m, downlink)...)
	return tc(append(args, "flowid", fmt.Sprintf("1:%x", minor))...)
}

func (s *Shaper) delOn(dev string, minor int, isSub bool) {
	if dev == "" {
		return
	}
	tcQuiet("filter", "del", "dev", dev, "parent", "1:", "pref",
		fmt.Sprint(pref(minor, isSub)))
	tcQuiet("qdisc", "del", "dev", dev, "parent", fmt.Sprintf("1:%x", minor))
	tcQuiet("class", "del", "dev", dev, "classid", fmt.Sprintf("1:%x", minor))
}

// Desired is one entity the caller wants conditioned: a device's default class,
// or one of its sub-classes.
type Desired struct {
	Key      string // stable identity: MAC, or MAC/subclass-id
	IP       string
	Port     string // the client's current bridge port; downlink is shaped here
	Match    Match
	Down, Up Shape
	IsSub    bool
}

// Apply reconciles the kernel against the desired set, touching only what
// changed. Anything absent from `want` is torn down.
func (s *Shaper) Apply(want []Desired) []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		return []error{fmt.Errorf("shaper not ready: %s", s.reason)}
	}

	// The box's address can change under it (the bridge is a DHCP client), and
	// a stale exemption would silently start shaping the control interface.
	if ips := s.localIPv4(); !slices.Equal(ips, s.exempt) {
		s.exempt = ips
		for dev := range s.ports {
			s.writeExemptions(dev, ips)
		}
	}

	var errs []error
	seen := map[string]bool{}

	// Sorted for deterministic minor assignment, which keeps class ids stable
	// across restarts and keeps `tc` output readable when debugging.
	sort.Slice(want, func(i, j int) bool { return want[i].Key < want[j].Key })

	for _, w := range want {
		if w.IP == "" {
			continue // not shapeable: a u32 filter needs an address to match
		}
		seen[w.Key] = true

		minor, ok := s.minors[w.Key]
		if !ok {
			minor = s.nextMin
			s.nextMin++
			s.minors[w.Key] = minor
		}
		prev, existed := s.applied[w.Key]

		// The downlink port can be unknown (the bridge has not learned the
		// client yet) or can change when a device roams. A move is a delete on
		// the old port followed by a fresh create on the new one.
		downPort := w.Port
		if downPort != "" {
			if err := s.ensurePort(downPort); err != nil {
				errs = append(errs, err)
				downPort = ""
			}
		}
		moved := existed && prev.downPort != downPort
		if moved {
			s.delOn(prev.downPort, minor, w.IsSub)
		}

		unchanged := existed && !moved && prev.ip == w.IP &&
			prev.match == w.Match && prev.down == w.Down && prev.up == w.Up
		if unchanged {
			continue
		}

		next := rule{minor: minor, ip: w.IP, downPort: downPort,
			match: w.Match, down: w.Down, up: w.Up}

		// --- downlink, on the client's own port -------------------------
		if downPort != "" {
			hadDown := existed && !moved && prev.haveDown
			if err := s.writeClass(downPort, minor, existed && !moved); err != nil {
				errs = append(errs, err)
			} else {
				var err error
				if next.haveDown, err = s.writeNetem(downPort, minor, w.Down, hadDown); err != nil {
					errs = append(errs, err)
				}
				if moved || !existed || prev.ip != w.IP || prev.match != w.Match {
					if err := s.writeFilter(downPort, minor, w.IP, w.Match, w.IsSub, true); err != nil {
						errs = append(errs, err)
					}
				}
			}
		}

		// --- uplink, on the WAN port ------------------------------------
		if err := s.writeClass(s.wan, minor, existed); err != nil {
			errs = append(errs, err)
		} else {
			var err error
			if next.haveUp, err = s.writeNetem(s.wan, minor, w.Up, prev.haveUp); err != nil {
				errs = append(errs, err)
			}
			if !existed || prev.ip != w.IP || prev.match != w.Match {
				if err := s.writeFilter(s.wan, minor, w.IP, w.Match, w.IsSub, false); err != nil {
					errs = append(errs, err)
				}
			}
		}

		s.applied[w.Key] = next
	}

	for key, r := range s.applied {
		if seen[key] {
			continue
		}
		isSub := strings.Contains(key, "/")
		s.delOn(r.downPort, r.minor, isSub)
		s.delOn(s.wan, r.minor, isSub)
		delete(s.applied, key)
		delete(s.minors, key)
	}
	return errs
}

func (s *Shaper) MinorFor(key string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.minors[key]
	return m, ok
}

// DownPortFor reports which interface currently carries a key's downlink class,
// so the caller knows where to read its counters from.
func (s *Shaper) DownPortFor(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applied[key].downPort
}

type tcClassJSON struct {
	Handle string `json:"handle"`
	Stats  struct {
		Bytes      uint64 `json:"bytes"`
		Packets    uint64 `json:"packets"`
		Drops      uint64 `json:"drops"`
		Overlimits uint64 `json:"overlimits"`
		Backlog    uint64 `json:"backlog"`
		Qlen       uint64 `json:"qlen"`
	} `json:"stats"`
}

type tcQdiscJSON struct {
	Kind    string `json:"kind"`
	Parent  string `json:"parent"`
	Options struct {
		Rate struct {
			Rate uint64 `json:"rate"` // BYTES per second, verified against iproute2 6.15
		} `json:"rate"`
	} `json:"options"`
}

// ReadCounters returns per-minor statistics for one interface.
func (s *Shaper) ReadCounters(dev string) map[int]Counters {
	out := map[int]Counters{}
	if dev == "" {
		return out
	}
	raw, err := exec.Command("tc", "-s", "-j", "class", "show", "dev", dev).Output()
	if err != nil {
		return out
	}
	var classes []tcClassJSON
	if json.Unmarshal(raw, &classes) != nil {
		return out
	}
	for _, c := range classes {
		var maj, min int
		if _, err := fmt.Sscanf(c.Handle, "%x:%x", &maj, &min); err != nil {
			continue
		}
		out[min] = Counters{
			Bytes: c.Stats.Bytes, Packets: c.Stats.Packets,
			Drops: c.Stats.Drops, Overlimits: c.Stats.Overlimits,
			Backlog: c.Stats.Backlog, Qlen: c.Stats.Qlen,
		}
	}
	// The enforced rate is read back from netem rather than echoed from the
	// request, so the UI shows what the kernel is actually doing. HTB's own
	// rate is meaningless here: it is a container, deliberately set to line
	// rate so it never binds.
	for minor, mbps := range s.readNetemRates(dev) {
		c := out[minor]
		c.CapMbps = mbps
		out[minor] = c
	}
	return out
}

func (s *Shaper) readNetemRates(dev string) map[int]float64 {
	out := map[int]float64{}
	raw, err := exec.Command("tc", "-j", "qdisc", "show", "dev", dev).Output()
	if err != nil {
		return out
	}
	var qs []tcQdiscJSON
	if json.Unmarshal(raw, &qs) != nil {
		return out
	}
	for _, q := range qs {
		if q.Kind != "netem" || q.Options.Rate.Rate == 0 {
			continue
		}
		var maj, min int
		if _, err := fmt.Sscanf(q.Parent, "%x:%x", &maj, &min); err != nil {
			continue
		}
		out[min] = float64(q.Options.Rate.Rate) * 8 / 1e6 // bytes/s -> Mbps
	}
	return out
}
