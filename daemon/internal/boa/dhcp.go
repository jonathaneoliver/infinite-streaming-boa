package boa

import "net"

// Names learned from DHCP.
//
// mDNS is opt-in advertising: a device announces because it has a service to
// offer. Plenty of devices have none and stay anonymous forever -- an Apple
// Watch, a smart plug, a camera, a printer that nobody has enabled sharing on.
// Those are exactly the devices an operator most needs named, because a bare
// MAC says nothing about what it is or whether it matters.
//
// DHCP is not opt-in. Anything that wants an address asks for one, and almost
// everything puts its own name in the request as option 12. A transparent
// bridge sits directly in that path, so the name arrives unasked -- and, as
// with mDNS, nothing is queried and nothing is sent.
//
// This was written after an unidentified 65 Mbit/s station was found dragging
// down every measurement on the AP. It took port scanning and inference to
// place it, while ntopng named it immediately from the DHCP hostname it had
// been broadcasting in cleartext the whole time. boa was listening to the one
// protocol that device had nothing to say on.
//
// The limit worth knowing: this only sees a lease being negotiated. A device
// that got its address before the box started stays anonymous until it renews.

const (
	// dhcpServerPort is the destination of a client's request. Matching on it
	// keeps replies out: a server's OFFER also carries option 12 sometimes, but
	// that is the SERVER's idea of the client's name, not the device's own.
	dhcpServerPort = 67

	// A BOOTP message is fixed-size up to the options, which begin after a
	// four-byte magic cookie. Anything shorter cannot carry a name.
	dhcpFixedLen  = 236
	dhcpCookieLen = 4

	dhcpOptPad      = 0
	dhcpOptHostname = 12
	dhcpOptEnd      = 255

	// op=1 is BOOTREQUEST. A reply is op=2 and is ignored, per above.
	dhcpOpRequest = 1

	// chaddr, the client's own hardware address, at a fixed offset in the BOOTP
	// header. hlen says how much of the 16-byte field is real.
	dhcpHLenOff   = 2
	dhcpCHAddrOff = 28
	dhcpEthHLen   = 6
)

// dhcpMagic is the cookie that separates the fixed BOOTP header from the
// options, and the cheapest way to reject a packet that merely landed on the
// right port.
var dhcpMagic = [4]byte{99, 130, 83, 99}

// ParseDHCP returns the MAC a client gives as its own and the host name it
// claims, or empty strings if the payload is not a client request carrying
// both.
//
// The MAC comes from chaddr rather than from the frame, because this is read
// from a UDP socket that never sees an Ethernet header. That is the same field
// the DHCP server keys the lease on, so it matches what the rest of the network
// believes about the device. It is still the device's own claim: a name learned
// this way labels a client, and nothing more -- shaping is attached by the port
// traffic actually arrives on, which no packet can assert about itself.
func ParseDHCP(payload []byte) (mac, name string) {
	name = parseDHCPName(payload)
	if name == "" {
		return "", ""
	}
	if len(payload) < dhcpCHAddrOff+dhcpEthHLen {
		return "", ""
	}
	// Only ethernet-length addresses; anything else is not a client of ours.
	if int(payload[dhcpHLenOff]) != dhcpEthHLen {
		return "", ""
	}
	h := payload[dhcpCHAddrOff : dhcpCHAddrOff+dhcpEthHLen]
	if h[0] == 0 && h[1] == 0 && h[2] == 0 && h[3] == 0 && h[4] == 0 && h[5] == 0 {
		return "", ""
	}
	return net.HardwareAddr(h).String(), name
}

func parseDHCPName(payload []byte) string {
	if len(payload) < dhcpFixedLen+dhcpCookieLen {
		return ""
	}
	if payload[0] != dhcpOpRequest {
		return "" // a reply, or not BOOTP at all
	}
	if payload[dhcpFixedLen] != dhcpMagic[0] || payload[dhcpFixedLen+1] != dhcpMagic[1] ||
		payload[dhcpFixedLen+2] != dhcpMagic[2] || payload[dhcpFixedLen+3] != dhcpMagic[3] {
		return ""
	}

	// Walk the options. Length-prefixed and attacker-influenced, so every step
	// is bounds-checked: a malformed option must end the walk, never read past
	// the buffer.
	for i := dhcpFixedLen + dhcpCookieLen; i < len(payload); {
		opt := payload[i]
		if opt == dhcpOptEnd {
			return ""
		}
		if opt == dhcpOptPad {
			i++
			continue
		}
		if i+1 >= len(payload) {
			return ""
		}
		length := int(payload[i+1])
		val := i + 2
		if val+length > len(payload) {
			return ""
		}
		if opt == dhcpOptHostname {
			return sanitiseName(string(payload[val : val+length]))
		}
		i = val + length
	}
	return ""
}

// sanitiseName trims what a device says about itself down to something safe to
// display. The value is arbitrary bytes chosen by the device: some pad with
// NULs, some send trailing dots, and nothing stops one sending control
// characters or a kilobyte of text into an interface that has to render it.
func sanitiseName(s string) string {
	const maxNameLen = 63 // a DNS label, which is more than any real device uses
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == 0 {
			break // C-string padding: the name ends here
		}
		if r < 0x20 || r == 0x7f {
			continue // control characters have no place in a device name
		}
		out = append(out, r)
		if len(out) >= maxNameLen {
			break
		}
	}
	// Trailing dots and spaces are common and mean nothing.
	for len(out) > 0 && (out[len(out)-1] == '.' || out[len(out)-1] == ' ') {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == ' ' {
		out = out[1:]
	}
	return string(out)
}
