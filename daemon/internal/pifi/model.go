// Package pifi implements the per-client network link conditioner that runs on
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
package pifi

import "time"

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
	// LossPct is random packet loss, 0-100.
	LossPct float64 `json:"loss_pct"`
}

// IsClean reports whether this direction imposes nothing at all, which lets the
// engine skip building a netem qdisc rather than installing an identity one.
func (s Shape) IsClean() bool {
	return s.RateMbps == 0 && s.DelayMs == 0 && s.JitterMs == 0 && s.LossPct == 0
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
	Rev      uint64     `json:"rev"`
	Label    string     `json:"label"`
	Enabled  bool       `json:"enabled"`
	Down     Shape      `json:"down"`
	Up       Shape      `json:"up"`
	Sub      []SubClass `json:"sub"`
	classMin int        // kernel class minor for the device default class
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
	MAC      string `json:"mac"`
	IP       string `json:"ip,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Label    string `json:"label"`
	// Medium is "wifi" or "wired". It affects only what telemetry exists --
	// conditioning is identical for both, because policy is keyed by MAC and
	// filters match on IP, neither of which knows about the physical layer.
	Medium string `json:"medium"`
	// Port is the bridge port the client was last seen on, for display.
	Port string `json:"port,omitempty"`

	// Present means currently associated to the radio. A DHCP lease alone does
	// NOT set this: leases outlive the clients that held them.
	Present bool `json:"present"`
	// Shapeable is false when the client has no IP yet, because every tc filter
	// needs an address to match on.
	Shapeable bool `json:"shapeable"`

	Station  *Station `json:"station,omitempty"`
	Policy   Policy   `json:"policy"`
	LastSeen int64    `json:"last_seen"`

	// DownCounters and UpCounters are the device default class. Sub-class
	// counters are keyed by SubClass.ID.
	DownCounters Counters            `json:"down_counters"`
	UpCounters   Counters            `json:"up_counters"`
	SubCounters  map[string]Counters `json:"sub_counters,omitempty"`

	// RTTAddedMs is Down.DelayMs + Up.DelayMs, computed here so the UI cannot
	// get the round-trip arithmetic wrong.
	RTTAddedMs float64 `json:"rtt_added_ms"`
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
	UplinkIf  string `json:"uplink_if"`
	// Ntopng reports whether ntopng is actually ANSWERING, not merely
	// installed. The UI hides its deep links otherwise, because a reflashed
	// image without the prebuilt artifact would otherwise show dead links.
	Ntopng     bool   `json:"ntopng"`
	NtopngPort int    `json:"ntopng_port"`
	Reason     string `json:"reason,omitempty"`
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
type Notice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

func nowMs() int64 { return time.Now().UnixMilli() }
