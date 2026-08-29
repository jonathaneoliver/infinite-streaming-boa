package pifi

import (
	"net"
	"strings"
)

// Minimal mDNS parsing, enough to learn what a device calls itself.
//
// Why parse this at all: pifi shows bare MAC addresses, which are unreadable,
// and every other source came up empty on a real network -- reverse DNS has no
// PTR records, the kernel neighbour table has no names, and ntopng knows the
// name but only behind an HTTP credential this box should not have to store.
//
// mDNS is the source that actually works. Devices announce themselves
// unprompted and repeatedly on 224.0.0.251:5353 and ff02::fb, and pifi is a
// bridge, so it already sees every one of those packets. Nothing is queried and
// nothing is injected -- the appliance stays invisible.
//
// Why hand-rolled: the daemon has no third-party dependencies, which keeps the
// licence audit trivial and the binary self-contained. A DNS answer walker is
// about a hundred lines, and paying a dependency for that is a poor trade.
// Only A and AAAA answers are read -- the direct name-to-address mapping --
// rather than reconstructing service instances from PTR and SRV records.

const (
	mdnsPort = 5353

	// Header lengths live here rather than in the Linux-only listener so the
	// packet dissection below stays portable and testable off the Pi.
	ipMinLen  = 20 // IPv4 header, minimum
	ip6MinLen = 40 // IPv6 fixed header

	dnsHeaderLen = 12
	dnsTypeA     = 1
	dnsTypeAAAA  = 28

	// A compression pointer is signalled by the top two bits of a length byte.
	dnsPtrMask = 0xC0
)

// parseDNSName reads a (possibly compressed) name, returning the name, the
// offset just past it, and whether parsing succeeded.
//
// Compression pointers can point backwards to anywhere in the message, so a
// malicious or corrupt packet can describe a loop. The hop budget bounds that:
// without it, a crafted mDNS packet from any device on the network would spin
// this goroutine forever.
func parseDNSName(msg []byte, off int) (string, int, bool) {
	var parts []string
	hops := 0
	next := -1 // where to resume after the first pointer is followed

	for {
		if off < 0 || off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		switch {
		case l == 0:
			off++
			if next >= 0 {
				off = next
			}
			return strings.Join(parts, "."), off, true

		case l&dnsPtrMask == dnsPtrMask:
			if off+1 >= len(msg) {
				return "", 0, false
			}
			ptr := (l&^dnsPtrMask)<<8 | int(msg[off+1])
			if next < 0 {
				next = off + 2
			}
			hops++
			if hops > 16 {
				return "", 0, false // pointer loop
			}
			off = ptr

		default:
			if l > 63 || off+1+l > len(msg) {
				return "", 0, false
			}
			parts = append(parts, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

// skipName advances past a name without building it.
func skipName(msg []byte, off int) (int, bool) {
	_, n, ok := parseDNSName(msg, off)
	return n, ok
}

// ParseMDNS extracts address-to-name bindings from an mDNS message.
//
// Both the answer and additional sections are read: Apple devices routinely put
// the A/AAAA record for their hostname in the additional section of a service
// announcement rather than the answer section, and ignoring it would miss most
// announcements.
func ParseMDNS(msg []byte) map[string]string {
	out := map[string]string{}
	if len(msg) < dnsHeaderLen {
		return out
	}
	be16 := func(i int) int { return int(msg[i])<<8 | int(msg[i+1]) }

	qd, an, ns, ar := be16(4), be16(6), be16(8), be16(10)
	off := dnsHeaderLen

	// Questions: name plus type and class.
	for i := 0; i < qd; i++ {
		n, ok := skipName(msg, off)
		if !ok || n+4 > len(msg) {
			return out
		}
		off = n + 4
	}

	for i := 0; i < an+ns+ar; i++ {
		name, n, ok := parseDNSName(msg, off)
		if !ok || n+10 > len(msg) {
			return out
		}
		rtype := int(msg[n])<<8 | int(msg[n+1])
		rdlen := int(msg[n+8])<<8 | int(msg[n+9])
		rd := n + 10
		if rd+rdlen > len(msg) {
			return out
		}

		switch {
		case rtype == dnsTypeA && rdlen == 4:
			out[net.IP(msg[rd:rd+4]).String()] = tidyName(name)
		case rtype == dnsTypeAAAA && rdlen == 16:
			out[net.IP(msg[rd:rd+16]).String()] = tidyName(name)
		}
		off = rd + rdlen
	}
	return out
}

// tidyName turns "Jons-iPhone.local" into "Jons-iPhone", and rejects names that
// carry no meaning for a human.
//
// iOS publishes a per-network RANDOM UUID hostname alongside its friendly one,
// as a privacy measure that pairs with Private Wi-Fi Address. Whichever record
// arrives last would otherwise win, and a device would flip between
// "Jons-iPhone" and "b38b77bd-3b57-4cc3-9474-ef67aebf801f". A raw UUID is worse
// than a MAC as a label -- longer, equally meaningless, and it changes -- so it
// is discarded and a real name is kept.
func tidyName(n string) string {
	n = strings.TrimSuffix(n, ".")
	n = strings.TrimSuffix(n, ".local")
	if isUUIDName(n) {
		return ""
	}
	return n
}

// isUUIDName reports whether a name is nothing but a UUID: 8-4-4-4-12 hex.
func isUUIDName(n string) bool {
	groups := strings.Split(n, "-")
	if len(groups) != 5 {
		return false
	}
	for i, want := range []int{8, 4, 4, 4, 12} {
		if len(groups[i]) != want {
			return false
		}
		for _, c := range groups[i] {
			isHex := (c >= '0' && c <= '9') ||
				(c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// udpPayload returns the UDP payload of an IPv4 or IPv6 packet when it is
// addressed to or from the given port, along with whether it matched.
//
// IPv6 extension headers are not walked: mDNS in practice carries none, and
// chasing a header chain to salvage a display name is not worth the code.
func udpPayload(pkt []byte, port int) ([]byte, bool) {
	if len(pkt) < 1 {
		return nil, false
	}
	var hdrLen, proto int

	switch pkt[0] >> 4 {
	case 4:
		if len(pkt) < ipMinLen {
			return nil, false
		}
		hdrLen = int(pkt[0]&0x0F) * 4
		if hdrLen < ipMinLen {
			return nil, false
		}
		proto = int(pkt[9])
	case 6:
		if len(pkt) < ip6MinLen {
			return nil, false
		}
		hdrLen = ip6MinLen
		proto = int(pkt[6])
	default:
		return nil, false
	}

	const udp = 17
	if proto != udp || len(pkt) < hdrLen+8 {
		return nil, false
	}
	src := int(pkt[hdrLen])<<8 | int(pkt[hdrLen+1])
	dst := int(pkt[hdrLen+2])<<8 | int(pkt[hdrLen+3])
	if src != port && dst != port {
		return nil, false
	}
	return pkt[hdrLen+8:], true
}
