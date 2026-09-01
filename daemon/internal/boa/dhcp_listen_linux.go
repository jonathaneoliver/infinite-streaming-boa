package boa

import (
	"log"
	"net"
)

// listenDHCP learns device names from DHCP requests crossing the bridge.
//
// A plain UDP socket on the server port, not a filtered packet socket like the
// mDNS path. Two reasons. A client's request is broadcast to 255.255.255.255,
// so it arrives here without any capture machinery; and the sender's MAC is
// inside the payload as chaddr, so nothing is lost by never seeing the frame.
// That keeps this off the hand-assembled BPF program, whose jump offsets are
// computed by hand and would all have to move to admit a second protocol.
//
// Binding port 67 is safe on this box specifically: it is a transparent bridge
// that issues no addresses -- running a DHCP server is an explicit non-goal --
// so nothing else wants the port. If something ever does, the bind fails, this
// logs once and returns, and every other source of names carries on.
func (l *Learner) listenDHCP() {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: dhcpServerPort})
	if err != nil {
		// Not fatal, and not silent. Names are a convenience; conditioning does
		// not depend on them. But a quiet failure here would look exactly like
		// "no device has renewed a lease yet", which is a normal state, so the
		// two have to be told apart.
		log.Printf("boa: DHCP name learning unavailable (port %d): %v", dhcpServerPort, err)
		return
	}
	defer conn.Close()
	log.Printf("boa: DHCP name learning active on port %d", dhcpServerPort)

	// A BOOTP message with options fits well inside this; the option field is
	// bounded by the 576-byte minimum DHCP message size in practice.
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("boa: DHCP name learning stopped: %v", err)
			return
		}
		mac, name := ParseDHCP(buf[:n])
		if mac == "" || name == "" {
			continue
		}
		// Keyed by MAC only. Unlike mDNS there is no address to bind a name to:
		// the whole point of the exchange is that the client does not have one
		// yet, and the address it is being offered is in the server's reply,
		// which is deliberately not read here.
		l.storeNames(mac, nil, name)
	}
}
