//go:build linux

package boa

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Learner passively discovers which address each client MAC holds, and which
// bridge port it is attached to, by watching traffic as it crosses the bridge.
//
// # Why passive learning is needed
//
// boa is a transparent bridge. A router knows every client's address because
// it issued the lease; a bridge issues nothing and is not the destination of
// most traffic, so the kernel's own neighbour table stays largely empty --
// Linux updates existing entries from overheard ARP but will not create new
// ones for exchanges it is not party to.
//
// # Why ETH_P_ALL, and not ETH_P_ARP or ETH_P_IP
//
// This is the subtle part, and getting it wrong produced a bug that looked
// exactly like a configuration problem.
//
// A packet socket opened with a SPECIFIC protocol only receives frames that are
// delivered LOCALLY. In __netif_receive_skb_core the ptype_all handlers run
// first, for every frame; then the bridge's rx_handler claims the frame and
// forwarding stops there. Protocol-specific delivery from ptype_base never
// happens for a frame the bridge forwards.
//
// So on a bridge, an ETH_P_IP socket sees none of the traffic it exists to
// watch. Measured on forwarded client-to-client traffic: ETH_P_IP received 0
// frames while ETH_P_ALL received 119 and learned every MAC/IP pair. The
// earlier ARP-only implementation appeared to work in testing purely because
// the test traffic was addressed to the bridge's own IP.
//
// ETH_P_ALL also delivers at ingress on the REAL interface, before the bridge
// rewrites skb->dev, so the arrival interface is the physical port. That makes
// port discovery reliable rather than dependent on forwarding-database ageing.
//
// # Why sampling
//
// ETH_P_ALL means every forwarded frame is a candidate. Copying all of them
// into userspace would burn a core at line rate for no benefit: boa needs one
// packet per MAC every few seconds, not all of them. The socket has a small
// receive buffer and is drained in bursts with a pause between, so the kernel
// discards the excess. Any device doing anything at all appears within seconds.
type Learner struct {
	mu     sync.RWMutex
	seen   map[string]learned
	bridge string
	fd     int
	mdnsFd int

	// downstream is the set of ports a CLIENT of this box can be on. A frame
	// that arrived anywhere else came from upstream, and upstream is not what
	// this box is for.
	downstream map[string]bool

	ifnames sync.Map // ifindex -> name

	// local is the set of subnets on the bridge. A frame from upstream carries
	// the ROUTER's MAC with a remote source IP, and learning that pair would
	// attribute half the internet to one device.
	localMu sync.RWMutex
	local   []*net.IPNet
	local6  []*net.IPNet

	// macNames maps a client MAC to what the device calls itself, learned from
	// the mDNS announcements every device broadcasts unprompted. The MAC is the
	// device; an address is only somewhere it currently is.
	//
	// names is the same thing keyed by address, kept as the fallback for an
	// announcement whose sender could not be identified -- and because it is
	// what the box learned before MAC keying existed, so a restored table still
	// labels devices while the first announcements come in.
	namesMu  sync.RWMutex
	macNames map[string]string
	names    map[string]string
}

type learned struct {
	ip string
	// v6 is a SET, not a single address. Privacy extensions mean a device holds
	// several valid IPv6 addresses at once and rotates them, so shaping one is
	// shaping a fraction of its traffic.
	v6   map[string]time.Time
	port string
	at   time.Time
}

const (
	ethPALL  = 0x0003
	ethPARP  = 0x0806
	ethPIP   = 0x0800
	ethPIPV6 = 0x86DD

	arpLen        = 28
	arpSenderMAC  = 8
	arpSenderIPv4 = 14

	// Cap the filters a single device can demand. A misbehaving stack cycling
	// through addresses should not be able to fill the filter table.
	maxV6PerMAC = 8

	// One packet per MAC every few seconds is ample, so the sampler stays well
	// below any rate that would cost measurable CPU on a Pi.
	sampleBurst = 128
	samplePause = 100 * time.Millisecond

	// Skip the shared-map write when this MAC was refreshed very recently.
	// The reader goroutine owns the dedupe cache, so the common case takes no
	// lock at all.
	dedupeWindow = 5 * time.Second
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func NewLearner(bridge, wlanPort, lanPort string) *Learner {
	// The same test the client list uses: a device is a client of this box only
	// if its traffic arrives on a downstream port. An absent port -- lan0 with
	// no USB adapter fitted -- is not a port anything can arrive on.
	down := map[string]bool{}
	for _, p := range []string{wlanPort, lanPort} {
		if p != "" {
			down[p] = true
		}
	}
	return &Learner{
		seen: map[string]learned{}, names: map[string]string{},
		macNames: map[string]string{}, downstream: down,
		bridge: bridge, fd: -1, mdnsFd: -1,
	}
}

// fromClient reports whether a frame arriving on this interface came from a
// device downstream of the box.
//
// With ETH_P_ALL the arrival interface is the physical port, before the bridge
// rewrites it, so this is the real answer rather than an inference. It also
// excludes the bridge's own copy of a multicast frame, which is delivered a
// second time with the interface rewritten to the bridge itself.
func (l *Learner) fromClient(ifindex int) bool {
	return l.downstream[l.ifname(ifindex)]
}

// nameTable is the on-disk form of both name tables.
//
// The file used to be a bare address-to-name object. A box upgraded in place
// still has one, and unmarshalling that into this struct leaves both fields
// nil, which is how the old shape is recognised and kept rather than thrown
// away on the one restart where the names have not been relearned yet.
type nameTable struct {
	ByMAC     map[string]string `json:"by_mac"`
	ByAddress map[string]string `json:"by_address"`
}

// LoadNames restores previously learned names so a restart does not blank every
// device back to a bare MAC while waiting for the next announcement.
func (l *Learner) LoadNames(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var t nameTable
	if json.Unmarshal(raw, &t) != nil {
		return
	}
	if t.ByMAC == nil && t.ByAddress == nil {
		if json.Unmarshal(raw, &t.ByAddress) != nil {
			return
		}
	}
	l.namesMu.Lock()
	for k, v := range t.ByMAC {
		l.macNames[normMAC(k)] = v
	}
	for k, v := range t.ByAddress {
		l.names[k] = v
	}
	l.namesMu.Unlock()
}

// SaveNames writes the tables out. Best-effort by design: a display name must
// never be able to fail the daemon.
func (l *Learner) SaveNames(path string) {
	l.namesMu.RLock()
	raw, err := json.Marshal(nameTable{ByMAC: l.macNames, ByAddress: l.names})
	l.namesMu.RUnlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

func (l *Learner) ifname(idx int) string {
	if n, ok := l.ifnames.Load(idx); ok {
		return n.(string)
	}
	n := ""
	if ifi, err := net.InterfaceByIndex(idx); err == nil {
		n = ifi.Name
	}
	l.ifnames.Store(idx, n)
	return n
}

// refreshLocal keeps the bridge's own subnets current. The bridge takes its
// management address by DHCP, so this can change while running.
func (l *Learner) refreshLocal() {
	var nets, nets6 []*net.IPNet
	if ifi, err := net.InterfaceByName(l.bridge); err == nil {
		addrs, _ := ifi.Addrs()
		for _, ad := range addrs {
			n, ok := ad.(*net.IPNet)
			if !ok {
				continue
			}
			if n.IP.To4() != nil {
				nets = append(nets, n)
			} else if !n.IP.IsLinkLocalUnicast() {
				// Link-local prefixes are excluded: fe80::/64 covers the whole
				// segment, so accepting it would match every device rather than
				// identifying the ones sharing our routable prefix.
				nets6 = append(nets6, n)
			}
		}
	}
	l.localMu.Lock()
	l.local = nets
	l.local6 = nets6
	l.localMu.Unlock()
}

// isLocal6 reports whether an IPv6 address sits in a prefix the bridge is on.
// Unlike the v4 case this fails CLOSED when no prefix is known: every device on
// the segment has a link-local address, so accepting anything would sweep up
// the whole network.
func (l *Learner) isLocal6(ip net.IP) bool {
	l.localMu.RLock()
	defer l.localMu.RUnlock()
	for _, n := range l.local6 {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isLocal reports whether an address belongs to a subnet the bridge is on.
// With no subnets known yet it accepts everything rather than nothing, so
// discovery still works before the bridge has finished DHCP.
func (l *Learner) isLocal(ip net.IP) bool {
	l.localMu.RLock()
	defer l.localMu.RUnlock()
	if len(l.local) == 0 {
		return true
	}
	for _, n := range l.local {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Run listens until the socket is closed. Failure is not fatal to the daemon:
// discovery degrades to the forwarding database and neighbour table, and the
// caller surfaces that rather than pretending everything is fine.
func (l *Learner) Run() error {
	l.refreshLocal()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			l.refreshLocal()
		}
	}()

	// A DEDICATED path for mDNS, not the sampled packet stream below.
	//
	// The sampler is tuned for volume: it catches any packet from a device that
	// is doing something, which is the right shape for learning addresses. mDNS
	// is the opposite -- a handful of packets every few minutes -- so sampling
	// would routinely discard the one packet carrying the name. Reading every
	// announcement costs almost nothing, because that traffic is tiny.
	//
	// The filtered packet socket is preferred over joining the multicast group,
	// for two reasons. It carries the SENDER'S MAC, which is what a name has to
	// be keyed by; and it reports the arrival PORT, so an announcement from
	// upstream can be told apart from one made by a client. A multicast
	// listener bound to the bridge can do neither: it is handed a payload, and
	// the frame that carried it is gone.
	//
	// So the listeners run only when the packet socket could not be opened.
	// They learn names by address for the whole segment, upstream included,
	// which is worse -- but it is what shipped before, and a box that shows
	// slightly too many names is better than one that shows none.
	//
	// Either way this is passive: nothing is queried, nothing is sent, the box
	// stays invisible.
	if !l.startMDNSCapture() {
		go l.listenMDNS("udp4", &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort})
		go l.listenMDNS("udp6", &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort})
	}

	// Names from DHCP, alongside mDNS rather than instead of it. The two see
	// different devices: mDNS names whatever advertises a service, DHCP names
	// whatever asks for an address, and the second set is the one that would
	// otherwise show as a bare MAC forever. Runs unconditionally -- unlike the
	// mDNS listeners above it is not a fallback for the capture socket.
	go l.listenDHCP()

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, int(htons(ethPALL)))
	if err != nil {
		return err
	}
	l.fd = fd
	defer syscall.Close(fd)

	// A small buffer bounds what the kernel holds for us and makes overflow --
	// that is, sampling -- the normal case rather than an error case.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 128*1024)

	// Large enough for a whole mDNS announcement. The previous 128 bytes was
	// fine for reading addresses out of a header, but recvfrom truncates
	// silently, so a name would have been cut in half rather than missing.
	buf := make([]byte, 1500)
	recent := map[string]time.Time{}

	for {
		for i := 0; i < sampleBurst; i++ {
			n, from, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				return err
			}
			ll, ok := from.(*syscall.SockaddrLinklayer)
			if !ok {
				continue
			}
			// Outgoing frames carry the REMOTE host's source address, so
			// learning from them would bind a client MAC to an internet
			// address. Only received frames say anything about who is here.
			if ll.Pkttype == syscall.PACKET_OUTGOING {
				continue
			}

			var mac string
			var ip net.IP
			switch ll.Protocol {
			case htons(ethPARP):
				if n < arpLen {
					continue
				}
				mac = net.HardwareAddr(buf[arpSenderMAC : arpSenderMAC+6]).String()
				ip = net.IP(buf[arpSenderIPv4 : arpSenderIPv4+4])
			case htons(ethPIP):
				if n < ipMinLen || ll.Halen < 6 {
					continue
				}
				// Source MAC from the sockaddr, source IP from the header.
				mac = net.HardwareAddr(ll.Addr[:6]).String()
				ip = net.IP(buf[ipSrc : ipSrc+4])
				l.recordMDNS(mac, buf[:n], ll.Ifindex)
			case htons(ethPIPV6):
				if n < ip6MinLen || ll.Halen < 6 {
					continue
				}
				mac = net.HardwareAddr(ll.Addr[:6]).String()
				ip = net.IP(buf[ip6Src : ip6Src+16])
				l.recordMDNS(mac, buf[:n], ll.Ifindex)
			default:
				continue
			}
			l.record(recent, mac, ip, ll.Ifindex)
		}
		// Pause so the kernel, not this process, absorbs a firehose.
		time.Sleep(samplePause)
	}
}

func (l *Learner) record(recent map[string]time.Time, mac string, ip net.IP, ifindex int) {
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return
	}
	v6 := ip.To4() == nil
	if v6 {
		// Link-local is skipped: it never leaves the segment, so conditioning
		// it would shape nothing anyone is testing.
		if ip.IsLinkLocalUnicast() || !l.isLocal6(ip) {
			return
		}
	} else if !l.isLocal(ip) {
		return
	}
	mac = normMAC(mac)
	now := time.Now()
	// The dedupe window is keyed per address family so a chatty IPv4 flow does
	// not mask the first sighting of a device's IPv6 address.
	rk := mac
	if v6 {
		rk = mac + "/6"
	}
	if t, ok := recent[rk]; ok && now.Sub(t) < dedupeWindow {
		return
	}
	recent[rk] = now

	// With ETH_P_ALL the arrival interface is the physical port, which is what
	// the downlink shaper needs. The bridge's own copy is ignored so it cannot
	// overwrite a good port.
	port := l.ifname(ifindex)
	if port == l.bridge {
		port = ""
	}
	l.mu.Lock()
	prev := l.seen[mac]
	if port == "" {
		port = prev.port // keep a previously known port
	}
	e := learned{ip: prev.ip, v6: prev.v6, port: port, at: now}
	if e.v6 == nil {
		e.v6 = map[string]time.Time{}
	}
	if v6 {
		e.v6[ip.String()] = now
		// Drop the oldest once past the cap.
		for len(e.v6) > maxV6PerMAC {
			var oldest string
			var oldestAt time.Time
			for a, t := range e.v6 {
				if oldest == "" || t.Before(oldestAt) {
					oldest, oldestAt = a, t
				}
			}
			delete(e.v6, oldest)
		}
	} else {
		e.ip = ip.String()
	}
	l.seen[mac] = e
	l.mu.Unlock()
}

func (l *Learner) Close() {
	if l.fd >= 0 {
		_ = syscall.Close(l.fd)
	}
	if l.mdnsFd >= 0 {
		_ = syscall.Close(l.mdnsFd)
	}
}

// Seen is one learned client: where it is and what address it holds.
type Seen struct {
	IP string
	// IPv6 holds every routable v6 address recently seen for this MAC. A
	// device commonly has several at once because of privacy extensions, and
	// shaping only one would shape only part of its traffic.
	IPv6 []string
	Port string
}

// Table returns bindings observed within ttl. Older entries are dropped: a
// stale binding would aim a shaping filter at an address that may since have
// moved to a different device.
func (l *Learner) Table(ttl time.Duration) map[string]Seen {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]Seen, len(l.seen))
	cut := time.Now().Add(-ttl)
	for mac, e := range l.seen {
		if !e.at.After(cut) {
			continue
		}
		var v6 []string
		for a, t := range e.v6 {
			if t.After(cut) {
				v6 = append(v6, a)
			}
		}
		sort.Strings(v6) // deterministic filter ordering
		out[mac] = Seen{IP: e.ip, IPv6: v6, Port: e.port}
	}
	return out
}

// recordMDNS records what an mDNS packet says, against the MAC that sent it
// where the sender can be identified, and against addresses either way.
//
// Multicast announcements are addressed to the group, not to this box, but a
// bridge sees them anyway -- which is the whole reason this works without
// querying anything. It also means this box hears every device on the segment,
// most of which are UPSTREAM and are not its clients. Those are dropped here,
// on the arrival port, rather than filtered out at the point of display: their
// names are of no use to anyone, they are what would push a real client's name
// out of a full table, and a test appliance has no business writing the names
// of the neighbouring network's devices to its disk.
func (l *Learner) recordMDNS(mac string, pkt []byte, ifindex int) {
	if !l.fromClient(ifindex) {
		return
	}
	byAddr, sender := ParseMDNSFrame(pkt)
	l.storeNames(mac, byAddr, sender)
}

// storeNames merges one announcement into both tables. mac may be empty, for a
// path that has no link-layer header to read it from.
func (l *Learner) storeNames(mac string, byAddr map[string]string, sender string) {
	if len(byAddr) == 0 && sender == "" {
		return
	}
	l.namesMu.Lock()
	defer l.namesMu.Unlock()
	for ip, name := range byAddr {
		if name != "" {
			l.names[ip] = name
		}
	}
	if mac != "" && sender != "" {
		l.macNames[normMAC(mac)] = sender
	}
	// Bound both tables. Names are a display convenience; an unbounded map fed
	// by anything on the network is not.
	capNames(l.names)
	capNames(l.macNames)
}

// capNames drops entries once a table grows past what any real network needs.
// Which entries go is unspecified: the point is a ceiling, and anything still
// on the network re-announces within minutes.
func capNames(m map[string]string) {
	if len(m) <= 512 {
		return
	}
	for k := range m {
		delete(m, k)
		if len(m) <= 384 {
			return
		}
	}
}

// Names returns the address-to-name bindings learned so far.
func (l *Learner) Names() map[string]string {
	l.namesMu.RLock()
	defer l.namesMu.RUnlock()
	return copyNames(l.names)
}

// MACNames returns the MAC-to-name bindings learned so far -- the ones that
// hold whether or not this box has ever seen the address a device announced on.
func (l *Learner) MACNames() map[string]string {
	l.namesMu.RLock()
	defer l.namesMu.RUnlock()
	return copyNames(l.macNames)
}

func copyNames(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		// Filter here as well as at parse time, so a UUID already sitting in
		// memory or restored from disk can never reach the interface.
		if v == "" || isUUIDName(v) {
			continue
		}
		out[k] = v
	}
	return out
}

// listenMDNS joins the mDNS multicast group on the bridge and records the
// address-to-name bindings every announcement carries.
//
// Failure is deliberately quiet and non-fatal: names are a display convenience,
// and a box that conditions traffic correctly while showing MAC addresses is
// far better than one refusing to start because a multicast join failed.
func (l *Learner) listenMDNS(network string, group *net.UDPAddr) {
	ifi, err := net.InterfaceByName(l.bridge)
	if err != nil {
		fmt.Printf("infinite-streaming-boa: mDNS %s: no interface %s: %v\n",
			network, l.bridge, err)
		return
	}
	// avahi-daemon is also bound to 5353 on this box. A multicast listener
	// sets SO_REUSEADDR so both can receive, but if that ever stops holding
	// the failure must be visible: silently showing MAC addresses forever,
	// with no indication why, is the worst outcome.
	conn, err := net.ListenMulticastUDP(network, ifi, group)
	if err != nil {
		fmt.Printf("infinite-streaming-boa: mDNS %s join failed on %s: %v "+
			"(device names will not be learned)\n", network, l.bridge, err)
		return
	}
	fmt.Printf("infinite-streaming-boa: mDNS %s listening on %s\n", network, l.bridge)
	defer conn.Close()
	_ = conn.SetReadBuffer(64 * 1024)

	buf := make([]byte, 9000) // a large record set can exceed one MTU
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// No MAC on this path: the kernel handed over a UDP payload, and the
		// frame that carried it is long gone. Addresses only.
		l.storeNames("", ParseMDNS(buf[:n]), "")
	}
}
