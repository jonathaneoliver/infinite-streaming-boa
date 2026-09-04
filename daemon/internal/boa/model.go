// Package boa implements the per-client network link conditioner that runs on
// the access point.
//
// The types in this file are the wire contract shared with the Vue UI. Field
// names and units here are the single source of truth; see docs/DATA-CONTRACT.md
// for where each value comes from and what it means.
//
// Unit policy, stated once and enforced at exactly one boundary (shape.go):
//
//	API and UI  ->  megabits/sec, milliseconds, percent   (human units)
//	kernel      ->  bits/sec,     seconds,      fraction  (tc units)
//
// The kernel's own JSON reports class rates in BYTES per second and netem delay
// in seconds, which is why every conversion is funnelled through one file
// instead of being scattered across the collectors.
package boa

import (
	"sort"
	"strings"
	"time"
)

// Shape is one direction's worth of conditioning. Zero values mean "no
// conditioning of this kind", which is why RateMbps == 0 reads as unlimited
// rather than as a total block.
type Shape struct {
	// RateMbps caps throughput. 0 means unlimited.
	RateMbps float64 `json:"rate_mbps"`
	// DelayMs is added latency in ONE direction. A round trip crosses both
	// directions, so the RTT a client observes is roughly down+up.
	DelayMs float64 `json:"delay_ms"`
	// JitterMs randomises DelayMs by +/- this amount.
	JitterMs float64 `json:"jitter_ms"`
	// LossPct is packet loss, 0-100. With LossBurst above 1 it is the MEAN loss
	// rate rather than an independent per-packet probability -- the long-run
	// fraction of packets lost, not the chance of losing any given one.
	LossPct float64 `json:"loss_pct"`
	// LossBurst is the mean length of a loss burst, in packets. 1 (and 0, for
	// stored policy written before this existed) means uniform loss: each
	// packet independently, which is what netem does by default and what
	// essentially never happens on a real link.
	//
	// Above 1 the kernel runs a Gilbert-Elliott model instead, and LossPct
	// becomes the mean over time. The two knobs are chosen so that 1
	// reproduces the old behaviour exactly, which is what lets every stored
	// policy and keyframe keep meaning what it meant.
	//
	// Packets rather than milliseconds because netem's state machine steps per
	// packet: packets is always defined, including at an unlimited rate where
	// a duration is unknowable. The interface derives and shows the wall-clock
	// equivalent from the configured rate.
	LossBurst float64 `json:"loss_burst,omitempty"`

	// The two below are the less common impairments. They are separated in the
	// interface rather than in the model: netem treats all six alike, and a
	// device that has one set is as conditioned as one that has any other.
	//
	// netem's `duplicate` is deliberately NOT here, though it is the obvious
	// third and the issue asking for these assumed it would be. It cannot
	// coexist with any other netem qdisc on the same interface -- "cannot mix
	// duplicating netems with other netems in tree" -- and the refusal is
	// symmetric: a device with duplicate set makes every OTHER device on that
	// port unconditionable, whichever order they are configured in. Measured on
	// 6.12; accepted alone, refused in both orders with a sibling present.
	//
	// That is not a limitation to work around, it is a contradiction of what
	// this box is: per-client conditioning where one device's policy never
	// affects another's. So it is left out rather than exposed with a caveat.

	// ReorderPct is the share of packets released immediately instead of
	// waiting in the delay queue, arriving ahead of packets sent before them.
	//
	// It REQUIRES DelayMs > 0. Reordering is implemented by letting a packet
	// skip the delay queue, so with no queue there is nothing to skip -- and
	// netem does not ignore the combination, it rejects the whole command:
	// "reordering not possible without specifying some delay". A rejected
	// command means NO netem qdisc, so an invalid reorder value would silently
	// take the device's rate and loss down with it. Verified on 6.12.
	ReorderPct float64 `json:"reorder_pct"`
	// CorruptPct is the share of packets given a single-bit error, which fails
	// the checksum and is discarded by the receiver rather than delivered
	// damaged. Distinct from loss: the packet is transmitted and consumes the
	// link, and TCP recovers by a different path than it does from a drop.
	CorruptPct float64 `json:"corrupt_pct"`
}

// Bursty reports whether this shape asks for correlated loss. Burst length
// without loss imposes nothing -- there are no bursts of nothing -- so it is
// deliberately not part of IsClean.
func (s Shape) Bursty() bool { return s.LossPct > 0 && s.LossBurst > 1 }

// IsClean reports whether this direction imposes nothing at all, which lets the
// engine skip building a netem qdisc rather than installing an identity one.
func (s Shape) IsClean() bool {
	return s.RateMbps == 0 && s.DelayMs == 0 && s.JitterMs == 0 && s.LossPct == 0 &&
		s.ReorderPct == 0 && s.CorruptPct == 0
}

// Match narrows a sub-class to a subset of a device's traffic. An empty Match
// is the device default and catches everything not claimed by a sub-class.
//
// This is the "per-player on a shared IP" mechanism, with an important caveat:
// it can only distinguish traffic that has a stable port or destination. A
// phone's ephemeral source ports change per connection, so true per-player
// separation requires a port-allocating proxy upstream (see README).
type Match struct {
	// DstPort matches the destination port for downlink traffic. 0 = any.
	DstPort int `json:"dst_port,omitempty"`
	// DstCIDR matches the destination network, e.g. "23.32.0.0/16". Empty = any.
	DstCIDR string `json:"dst_cidr,omitempty"`
	// Protocol is "tcp", "udp", or "" for any.
	Protocol string `json:"protocol,omitempty"`
}

// IsEmpty reports whether this matcher constrains nothing.
func (m Match) IsEmpty() bool {
	return m.DstPort == 0 && m.DstCIDR == "" && m.Protocol == ""
}

// SubClass is a named rule inside a device, evaluated before the device
// default. Order matters: sub-classes are installed at a higher filter priority
// so the most specific rule wins.
type SubClass struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Match    Match  `json:"match"`
	Down     Shape  `json:"down"`
	Up       Shape  `json:"up"`
	Enabled  bool   `json:"enabled"`
	classMin int    // kernel class minor, assigned by the shaper; not serialised
}

// Rung is one rendition's delivered bitrate.
type Rung struct {
	// Mbps is what the rendition costs on the wire.
	Mbps float64 `json:"mbps"`

	// UpAtMbps is the cap at which the client CLIMBED INTO this rendition, and
	// DownAtMbps the cap at which it FELL OUT of it. Both are recorded because
	// the cost alone cannot drive a pattern.
	//
	// A player does not select a rendition merely because its bitrate fits. It
	// wants headroom: measured on an iPhone, it took a variant only when the
	// cap was around 1.5 to 1.9 times that variant's cost, never less than 1.5.
	// So capping AT a rung's own bitrate does not hold a player on it -- it
	// drops below. The cap that produces a given rendition is the useful
	// number, and it is not the rendition's bitrate.
	//
	// The two differ from each other as well, because ABR players use
	// hysteresis on purpose so they do not oscillate at a boundary. A sweep
	// that climbs can only observe the up-switch thresholds; one that descends
	// can only observe the down-switch ones. They are different measurements of
	// the same ladder rather than competing attempts at one measurement.
	//
	// Zero means not observed in this run's direction.
	UpAtMbps   float64 `json:"up_at_mbps,omitempty"`
	DownAtMbps float64 `json:"down_at_mbps,omitempty"`

	// Unstable marks a rung whose observation window drifted: its two halves
	// disagreed, so its mean describes neither.
	// Reported rather than dropped, so the operator can see which number to
	// distrust instead of being handed a uniformly confident list.
	Unstable bool `json:"unstable,omitempty"`
}

// Headroom is how much more than its own cost this rendition needed before the
// client would select it. Zero when the climb-in cap was not observed.
func (r Rung) Headroom() float64 {
	if r.Mbps <= 0 || r.UpAtMbps <= 0 {
		return 0
	}
	return r.UpAtMbps / r.Mbps
}

// Ladder provenance values, ordered by how much they can be trusted.
const (
	LadderTyped    = "typed"    // the operator entered the rungs
	LadderFetched  = "fetched"  // read from a manifest
	LadderMeasured = "measured" // every rung observed by a sweep
	LadderInferred = "inferred" // one rung observed, the rest synthesised
)

// Ladder is one service's rendition ladder as seen by one device.
//
// Keyed by service, not just by device. A ladder is a property of the content
// and the player that fetched it, not of the hardware: the same set-top box
// streaming two services produces two ladders with nothing in common, so a
// device holds a list of these rather than one.
//
// Service is typed by the operator. Inferring it from SNI or DNS would decay --
// ECH removes SNI, QUIC buries the handshake, DoH removes the DNS -- and would
// decay by silently mislabelling a ladder rather than by failing.
type Ladder struct {
	// Service is the operator's name for what was being streamed, e.g.
	// "netflix". Together with the device MAC it is the ladder's identity.
	Service string `json:"service"`
	// Rungs ascend.
	Rungs []Rung `json:"rungs"`
	// Provenance is one of the Ladder* constants. It exists so the UI can show
	// a measured ladder and a synthesised one with different confidence: they
	// are different claims and must not render alike.
	Provenance string `json:"provenance"`
	MeasuredAt int64  `json:"measured_at,omitempty"`
	Note       string `json:"note,omitempty"`
	// Throttle is what the sweep incidentally learned about the SHAPER, at the
	// caps where the client was pinned to it. It qualifies every rung above:
	// rungs are a mean over a window, so they can be no more accurate or steady
	// than the throttle that produced them.
	Throttle []ThrottlePoint `json:"throttle,omitempty"`
}

// ThrottlePoint is a measurement of the conditioner itself, taken at a cap the
// client could not live under.
//
// A starved client is a perfect instrument. It fetches back to back, so the
// delivered rate is decided entirely by the shaper -- the player has no say,
// the content has no say, VBR has no say. Every sweep passes through at least
// one such level on its way up and would otherwise discard the reading.
type ThrottlePoint struct {
	CapMbps       float64 `json:"cap_mbps"`
	DeliveredMbps float64 `json:"delivered_mbps"`
	// Ratio is delivered over configured. Framing overhead puts a real link
	// slightly under 1.
	Ratio float64 `json:"ratio"`
	// Variation is the coefficient of variation across the window: the jitter a
	// rung measured at this rate inherits from the shaper.
	Variation float64 `json:"variation"`
}

// Policy is everything the operator has configured for one device. It is keyed
// by MAC and persists across DHCP renewal, disconnection and reboot -- an IP is
// too unstable to hang configuration from.
type Policy struct {
	MAC string `json:"mac"`
	// Rev increments on every write to THIS device. A client sends the value it
	// last saw as base_revision; a mismatch means someone else edited the same
	// device in the meantime and the write is refused rather than silently
	// clobbering them. Per-device rather than global, so two operators editing
	// two different clients never collide.
	Rev     uint64     `json:"rev"`
	Label   string     `json:"label"`
	Enabled bool       `json:"enabled"`
	Down    Shape      `json:"down"`
	Up      Shape      `json:"up"`
	Sub     []SubClass `json:"sub"`
	// Ladders is every rendition ladder measured or entered for this device,
	// one per service. Persisted with the policy because it is a durable input
	// the operator owns and edits -- unlike telemetry, it is written once per
	// sweep rather than once per tick, so it costs the SD card nothing.
	Ladders []Ladder `json:"ladders,omitempty"`
	// Pattern is the timeline the operator has authored for this device, if
	// any. Stored with the policy because it is intent, not telemetry: it is
	// written when someone edits a keyframe, never once per tick. Storing it
	// does not run it -- see Player.
	Pattern  *Pattern `json:"pattern,omitempty"`
	classMin int      // kernel class minor for the device default class
}

// LadderFor returns this device's ladder for one service.
func (p Policy) LadderFor(service string) (Ladder, bool) {
	for _, l := range p.Ladders {
		if strings.EqualFold(l.Service, service) {
			return l, true
		}
	}
	return Ladder{}, false
}

// PutLadder inserts or replaces the ladder for a service, keeping the list
// sorted by name so the UI order does not depend on measurement order.
func (p *Policy) PutLadder(l Ladder) {
	for i := range p.Ladders {
		if strings.EqualFold(p.Ladders[i].Service, l.Service) {
			p.Ladders[i] = l
			return
		}
	}
	p.Ladders = append(p.Ladders, l)
	sort.Slice(p.Ladders, func(i, j int) bool {
		return p.Ladders[i].Service < p.Ladders[j].Service
	})
}

// Station is the radio's view of a client: the authority on who is actually
// associated. Byte counters here are from the ACCESS POINT's perspective, so
// the AP's tx is the client's download.
type Station struct {
	MAC       string  `json:"mac"`
	SignalDBm int     `json:"signal_dbm"`
	TxBytes   uint64  `json:"tx_bytes"`
	RxBytes   uint64  `json:"rx_bytes"`
	TxPhyMbps float64 `json:"tx_phy_mbps"`
	RxPhyMbps float64 `json:"rx_phy_mbps"`
	// TxFailed is transmit failures to this station. The Pi's Broadcom driver
	// does not report per-station RSSI in AP mode -- there is no "signal" line
	// in `iw station dump` at all -- so this is the available proxy for link
	// quality. Rising quickly means the client is struggling.
	TxFailed     uint64 `json:"tx_failed"`
	ConnectedSec int    `json:"connected_sec"`
	InactiveMs   int    `json:"inactive_ms"`
}

// Counters is one class's enforcement statistics, converted to human units.
type Counters struct {
	Bytes      uint64 `json:"bytes"`
	Packets    uint64 `json:"packets"`
	Drops      uint64 `json:"drops"`
	Overlimits uint64 `json:"overlimits"`
	Backlog    uint64 `json:"backlog"`
	Qlen       uint64 `json:"qlen"`
	// ThroughputMbps is derived between polls, not read from the kernel.
	ThroughputMbps float64 `json:"throughput_mbps"`
	// CapMbps is the rate the kernel is actually enforcing right now, read
	// back from tc rather than echoed from the request, so the UI shows what
	// the kernel believes rather than what we asked for.
	CapMbps float64 `json:"cap_mbps"`
}

// Client is the joined view the UI renders: one row per device.
//
// The join is station-dump LEFT JOIN (leases, neigh). Presence comes from the
// radio; the address is decoration and may legitimately be absent while a
// client is associated but has not finished DHCP.
type Client struct {
	MAC string `json:"mac"`
	IP  string `json:"ip,omitempty"`
	// IPv6 is every routable v6 address the client currently holds. Privacy
	// extensions mean a device usually has more than one, and each needs its
	// own filter or part of its traffic escapes conditioning entirely.
	IPv6     []string `json:"ipv6,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Label    string   `json:"label"`
	// Medium is "wifi" or "wired". It affects only what telemetry exists --
	// conditioning is identical for both, because policy is keyed by MAC and
	// filters match on IP, neither of which knows about the physical layer.
	Medium string `json:"medium"`
	// Port is the bridge port the client was last seen on, for display.
	Port string `json:"port,omitempty"`
	// RadioOn describes the access point a WIRELESS client is associated to.
	//
	// It matters now that the box serves two bands at once: a client on 2.4GHz
	// at 20MHz and one on 5GHz at 80MHz behave completely differently, and
	// until this existed the interface said only "wifi" for both. Absent for a
	// wired client, and for a wireless one whose radio could not be read.
	RadioOn *RadioOn `json:"radio_on,omitempty"`

	// SteerTo is the radio this client could be ASKED to move to, or empty when
	// there is nowhere to send it.
	//
	// Computed here rather than left to the interface, which would otherwise
	// have to work out the box's radio topology from the device list to decide
	// whether to offer a button -- and would get it wrong for a client whose
	// own radio is the only one serving. Empty is the honest answer in three
	// cases that look different and are not: a wired client, a wireless client
	// not currently associated, and a box with one radio.
	SteerTo string `json:"steer_to,omitempty"`

	// Present means currently associated to the radio. A DHCP lease alone does
	// NOT set this: leases outlive the clients that held them.
	Present bool `json:"present"`
	// Shapeable is false when the client has no IP yet, because every tc filter
	// needs an address to match on.
	Shapeable bool `json:"shapeable"`

	Station  *Station `json:"station,omitempty"`
	Policy   Policy   `json:"policy"`
	LastSeen int64    `json:"last_seen"`
	// LastActiveMs is the last time this client moved more than a trickle of
	// traffic, in unix milliseconds. 0 means never seen doing anything.
	//
	// Distinct from LastSeen, which only says the device was THERE: a phone in a
	// pocket is seen continuously and does nothing for hours. This is what
	// orders a list of devices that are all equally idle right now, where
	// "streaming ten seconds ago" and "silent since Tuesday" are the difference
	// between the one worth looking at and the rest.
	LastActiveMs int64 `json:"last_active_ms,omitempty"`

	// DownCounters and UpCounters are the device default class. Sub-class
	// counters are keyed by SubClass.ID.
	DownCounters Counters            `json:"down_counters"`
	UpCounters   Counters            `json:"up_counters"`
	SubCounters  map[string]Counters `json:"sub_counters,omitempty"`

	// Sweep is the ladder sweep running on this device, or the outcome of the
	// last one. Absent when the device has never been swept this daemon run.
	Sweep *SweepView `json:"sweep,omitempty"`

	// PatternRun is the pattern playing on this device, if one is. Named apart
	// from Policy.Pattern deliberately: that is the timeline as authored, this
	// is a playhead moving along it, and a UI that confused the two would edit
	// the wrong object.
	PatternRun *PatternView `json:"pattern_run,omitempty"`
}

// SweepView is a ladder sweep's progress, carried in every snapshot so the UI
// can show a run that takes minutes without polling a second endpoint.
//
// Live only: a sweep is transient, and a daemon restart cancels it. What
// survives a restart is the Ladder it produced, on the device's policy.
type SweepView struct {
	// State is "running", "done" or "failed".
	State string `json:"state"`
	// Phase is "settling" while a new cap beds in, "observing" while the
	// plateau is being measured, and mirrors State once the run has ended.
	Phase string `json:"phase"`
	// Pass is "map" while finding where the rungs are, "measure" while
	// measuring what they are. The two want opposite window lengths, which is
	// why they are separate passes rather than one compromise.
	Pass    string `json:"pass"`
	Service string `json:"service"`
	Level   int    `json:"level"`
	// CapMbps is the cap being held right now. 0 is the opening unconditioned
	// probe that establishes the ceiling.
	CapMbps     float64 `json:"cap_mbps"`
	CeilingMbps float64 `json:"ceiling_mbps"`
	// Found is the rungs discovered so far, ascending. It grows during the run.
	Found []Rung `json:"found,omitempty"`
	// Levels is every level's outcome, so a surprising ladder can be read back
	// to the observation that produced it rather than being taken on trust.
	Levels    []SweepLevel `json:"levels,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	StartedAt int64        `json:"started_at"`
}

// Capabilities tells the UI which parts of the system are actually working, so
// a missing kernel module degrades into a clear banner instead of controls that
// silently do nothing.
type Capabilities struct {
	Shaping   bool   `json:"shaping"`
	Uplink    bool   `json:"uplink"`
	Radio     bool   `json:"radio"`
	Leases    bool   `json:"leases"`
	WlanIface string `json:"wlan_iface"`
	// WlanIfaces is EVERY radio the daemon watches. WlanIface stays as the
	// first of them so older readers keep working; a box serving two radios
	// reports both here, and a client on any of them is conditioned.
	WlanIfaces []string `json:"wlan_ifaces,omitempty"`
	// Adapter names the radio actually serving the AP and, when it is a USB
	// one, the speed it NEGOTIATED. Both matter to an operator: which radio is
	// live is otherwise a guess when two are plugged in, and a USB 3 adapter
	// that quietly enumerated at USB 2 looks identical everywhere else while
	// delivering a sixth of the throughput. Distinct from Radio above, which
	// only says whether an interface is present at all.
	Adapter  RadioInfo `json:"adapter"`
	UplinkIf string    `json:"uplink_if"`
	// Ntopng reports whether ntopng is actually ANSWERING, not merely
	// installed. The UI hides its deep links otherwise, because a reflashed
	// image without the prebuilt artifact would otherwise show dead links.
	Ntopng     bool `json:"ntopng"`
	NtopngPort int  `json:"ntopng_port"`
	// Iperf reports whether the iperf3 server is LISTENING, so a script can
	// check the box can measure its own link without parsing the notices.
	// It measures the unshaped link only; see the unit file for why.
	Iperf     bool `json:"iperf"`
	IperfPort int  `json:"iperf_port"`
	// LinkControl reports whether per-client link events (deauth/disassoc) can
	// be driven -- i.e. hostapd is serving the AP and exposing its control
	// socket. False on the onboard/NetworkManager radio, which offers no such
	// control, so the UI hides the actions rather than offer a dead button.
	// See hostapd.go and issue #135.
	LinkControl bool `json:"link_control"`
	// NamesLearned is how many address-to-name bindings mDNS has yielded, and
	// NamesByMAC how many of the MAC-keyed bindings that actually label a
	// client. Zero means nothing is being heard; a healthy number while a
	// client still shows a MAC means that device simply has not announced
	// itself. Without this the two cases are indistinguishable from outside
	// the box. They are separate because the MAC-keyed count is the one that
	// says the filtered capture socket is working -- it stays zero if that
	// failed to open, while the address count keeps climbing.
	NamesLearned int    `json:"names_learned"`
	NamesByMAC   int    `json:"names_by_mac"`
	Reason       string `json:"reason,omitempty"`
	// LossBurst reports whether this kernel's netem accepts a Gilbert-Elliott
	// loss model, asked at startup rather than assumed. False disables the
	// burst control with LossBurstNote as the reason -- the alternative is an
	// interface that says "bursty" while the kernel delivers uniform loss,
	// which is the failure the feature exists to fix, wearing a disguise.
	LossBurst     bool   `json:"loss_burst"`
	LossBurstNote string `json:"loss_burst_note,omitempty"`
}

// Snapshot is the whole server state, delivered complete on every SSE event.
//
// Sending full snapshots rather than deltas is deliberate: a client that misses
// an event cannot drift, it simply renders the next one. The two revisions are
// separate because they answer different questions --
//
//	Revision        changes every tick (telemetry moved)
//	ControlRevision changes only when a policy was written
//
// The UI resyncs slider positions only when ControlRevision changes, so live
// telemetry updates never yank a control out from under the operator's cursor.
type Snapshot struct {
	// Version is the running build's version string (see main.version). The UI
	// shows it so an operator can tell which build a box is on.
	Version         string       `json:"version"`
	Revision        uint64       `json:"revision"`
	ControlRevision uint64       `json:"control_revision"`
	Time            int64        `json:"time"`
	Caps            Capabilities `json:"caps"`
	Clients         []Client     `json:"clients"`
	// Notices are operational truths the UI must surface. Severity is carried
	// in the data rather than inferred from the wording, because where a
	// notice belongs on screen depends on whether it is actionable: an error
	// buried at the foot of the page is worse than clutter at the top.
	Notices []Notice `json:"notices,omitempty"`
}

// Notice is one message for the operator.
//
//	"error" - something is wrong and the box is not doing its job
//	"info"  - a standing truth about how the system behaves
//
// RadioOn is the access point a client is associated to, as the device list
// shows it. A trimmed APStatus: the client card wants what distinguishes one
// radio from the other, not the whole BSS configuration.
type RadioOn struct {
	Iface    string `json:"iface"`
	Channel  int    `json:"channel,omitempty"`
	WidthMHz int    `json:"width_mhz,omitempty"`
	Mode     string `json:"mode,omitempty"`
	// Band is "2.4GHz" or "5GHz", derived from the channel. The single most
	// useful fact about which radio a client is on.
	Band string `json:"band,omitempty"`
}

type Notice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

func nowMs() int64 { return time.Now().UnixMilli() }
