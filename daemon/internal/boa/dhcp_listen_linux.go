package boa

import (
	"log"
	"net"
	"os/exec"
	"strings"
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
//
// THAT WAS ASSERTED, NOT CHECKED, AND THE FAILURE IS THE WRONG WAY ROUND. On a
// box that still routes -- which every device is until convert has run -- a
// DHCP server does want the port, and boad starts first. So the bind SUCCEEDS
// and dnsmasq is the one that fails, which this code never sees. Measured on
// the x86-64 guest, 2026-09-26: dnsmasq in a crash loop with "failed to bind
// DHCP server socket: Address in use", the device able to ping the internet by
// address and resolve nothing, and `apk` unable to fetch the driver the setup
// wizard was asking for.
//
// So the claim is tested before it is relied on. The service is not started at
// all on a routing box now, which makes this belt and braces -- but a daemon
// started by hand must not be able to take DNS down either, and an assumption
// stated in a comment is worth exactly as much as one that is checked.
func (l *Learner) listenDHCP() {
	if !onTransparentBridge() {
		log.Printf("boa: DHCP name learning off -- this device still routes, " +
			"and port 67 belongs to its DHCP server. It starts once the box is bridged.")
		return
	}
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
		// Ranked below mDNS: option 12 is a bare hostname and is often a
		// flattened spelling of the name the device shows its owner. See
		// storeNamesFrom.
		l.storeNamesFrom(nameFromDHCP, mac, nil, name)
	}
}

// onTransparentBridge reports whether this device bridges rather than routes:
// lan takes its address from upstream and there is no wan of our own.
//
// The same pair convert establishes and `boa-setup check` tests, asked of uci
// rather than inferred from the interfaces -- a bridge that is mid-reload still
// has the config that says what it is.
func onTransparentBridge() bool {
	proto, err := exec.Command("uci", "-q", "get", "network.lan.proto").Output()
	if err != nil || strings.TrimSpace(string(proto)) != "dhcp" {
		return false
	}
	// `uci get` on a missing section exits non-zero, which is the answer we want.
	if err := exec.Command("uci", "-q", "get", "network.wan").Run(); err == nil {
		return false
	}
	return true
}
