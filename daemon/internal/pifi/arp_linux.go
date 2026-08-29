//go:build linux

package pifi

import (
	"net"
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
}

type learned struct {
	ip   string
	port string
	at   time.Time
}

const (
	ethPALL = 0x0003
	ethPARP = 0x0806
	ethPIP  = 0x0800

	arpLen        = 28
	arpSenderMAC  = 8
	arpSenderIPv4 = 14

	ipMinLen = 20
	ipSrc    = 12

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
	return &Learner{seen: map[string]learned{}, bridge: bridge, fd: -1}
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
	var nets []*net.IPNet
	if ifi, err := net.InterfaceByName(l.bridge); err == nil {
		addrs, _ := ifi.Addrs()
		for _, ad := range addrs {
			if n, ok := ad.(*net.IPNet); ok && n.IP.To4() != nil {
				nets = append(nets, n)
			}
		}
	}
	l.localMu.Lock()
	l.local = nets
	l.localMu.Unlock()
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

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, int(htons(ethPALL)))
	if err != nil {
		return err
	}
	l.fd = fd
	defer syscall.Close(fd)

	// A small buffer bounds what the kernel holds for us and makes overflow --
	// that is, sampling -- the normal case rather than an error case.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 128*1024)

	buf := make([]byte, 128) // only the front of the header is ever needed
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
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || !l.isLocal(ip) {
		return
	}
	mac = normMAC(mac)
	now := time.Now()
	if t, ok := recent[mac]; ok && now.Sub(t) < dedupeWindow {
		return
	}
	recent[mac] = now

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
	l.seen[mac] = learned{ip: ip.String(), port: port, at: now}
	l.mu.Unlock()
}

func (l *Learner) Close() {
	if l.fd >= 0 {
		_ = syscall.Close(l.fd)
	}
}

// Seen is one learned client: where it is and what address it holds.
type Seen struct {
	IP   string
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
		if e.at.After(cut) {
			out[mac] = Seen{IP: e.ip, Port: e.port}
		}
	}
	return out
}
