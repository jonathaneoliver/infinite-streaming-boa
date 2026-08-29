//go:build linux

package pifi

import (
	"net"
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
// pifi is a transparent bridge. A router knows every client's address because
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
// into userspace would burn a core at line rate for no benefit: pifi needs one
// packet per MAC every few seconds, not all of them. The socket has a small
// receive buffer and is drained in bursts with a pause between, so the kernel
// discards the excess. Any device doing anything at all appears within seconds.
type Learner struct {
	mu     sync.RWMutex
	seen   map[string]learned
	bridge string
	fd     int

	ifnames sync.Map // ifindex -> name

	// local is the set of subnets on the bridge. A frame from upstream carries
	// the ROUTER's MAC with a remote source IP, and learning that pair would
	// attribute half the internet to one device.
	localMu sync.RWMutex
	local   []*net.IPNet
	local6  []*net.IPNet

	// names maps an address to what the device calls itself, learned from the
	// mDNS announcements every device broadcasts unprompted.
	namesMu sync.RWMutex
	names   map[string]string
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

	ipSrc  = 12
	ip6Src = 8

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

func NewLearner(bridge string) *Learner {
	return &Learner{
		seen: map[string]learned{}, names: map[string]string{},
		bridge: bridge, fd: -1,
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

	// A DEDICATED listener for mDNS, not the sampled packet stream below.
	//
	// The sampler is tuned for volume: it catches any packet from a device that
	// is doing something, which is the right shape for learning addresses. mDNS
	// is the opposite -- a handful of packets every few minutes -- so sampling
	// would routinely discard the one packet carrying the name. Joining the
	// multicast group and reading every announcement costs almost nothing,
	// because that traffic is tiny.
	//
	// Still passive: nothing is queried, nothing is sent, the box stays
	// invisible.
	go l.listenMDNS("udp4", &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort})
	go l.listenMDNS("udp6", &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: mdnsPort})

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
				l.maybeMDNS(buf[:n])
			case htons(ethPIPV6):
				if n < ip6MinLen || ll.Halen < 6 {
					continue
				}
				mac = net.HardwareAddr(ll.Addr[:6]).String()
				ip = net.IP(buf[ip6Src : ip6Src+16])
				l.maybeMDNS(buf[:n])
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

// maybeMDNS records any address-to-name bindings an mDNS packet carries.
//
// Multicast announcements are addressed to the group, not to this box, but a
// bridge sees them anyway -- which is the whole reason this works without
// querying anything.
func (l *Learner) maybeMDNS(pkt []byte) {
	payload, ok := udpPayload(pkt, mdnsPort)
	if !ok {
		return
	}
	found := ParseMDNS(payload)
	if len(found) == 0 {
		return
	}
	l.namesMu.Lock()
	for ip, name := range found {
		if name != "" {
			l.names[ip] = name
		}
	}
	// Bound the table. Names are a display convenience; an unbounded map fed
	// by anything on the network is not.
	if len(l.names) > 512 {
		for k := range l.names {
			delete(l.names, k)
			if len(l.names) <= 384 {
				break
			}
		}
	}
	l.namesMu.Unlock()
}

// Names returns the address-to-name bindings learned so far.
func (l *Learner) Names() map[string]string {
	l.namesMu.RLock()
	defer l.namesMu.RUnlock()
	out := make(map[string]string, len(l.names))
	for k, v := range l.names {
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
		return
	}
	conn, err := net.ListenMulticastUDP(network, ifi, group)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(64 * 1024)

	buf := make([]byte, 9000) // a large record set can exceed one MTU
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		found := ParseMDNS(buf[:n])
		if len(found) == 0 {
			continue
		}
		l.namesMu.Lock()
		for ip, name := range found {
			if name != "" {
				l.names[ip] = name
			}
		}
		l.namesMu.Unlock()
	}
}
