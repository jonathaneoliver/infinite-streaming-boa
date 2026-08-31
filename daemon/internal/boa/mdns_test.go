package boa

import (
	"net"
	"testing"
)

// buildMsg assembles a DNS message from a header and a body, so the tests read
// as data rather than as byte arithmetic.
func buildMsg(qd, an, ns, ar int, body []byte) []byte {
	h := []byte{
		0, 0, // id
		0x84, 0x00, // flags: authoritative response
		byte(qd >> 8), byte(qd),
		byte(an >> 8), byte(an),
		byte(ns >> 8), byte(ns),
		byte(ar >> 8), byte(ar),
	}
	return append(h, body...)
}

func name(labels ...string) []byte {
	var b []byte
	for _, l := range labels {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

func TestParseMDNS_ARecord(t *testing.T) {
	body := append(name("Jons-iPhone", "local"),
		0, dnsTypeA, // type A
		0x80, 1, // class IN, cache-flush bit set as devices really do
		0, 0, 0, 120, // ttl
		0, 4, // rdlength
		192, 168, 0, 214,
	)
	got := ParseMDNS(buildMsg(0, 1, 0, 0, body))
	if got["192.168.0.214"] != "Jons-iPhone" {
		t.Fatalf("want Jons-iPhone for 192.168.0.214, got %v", got)
	}
}

func TestParseMDNS_AAAAInAdditionalSection(t *testing.T) {
	// Apple devices routinely put the address record in the additional
	// section of a service announcement rather than the answer section.
	body := append(name("AppleTV", "local"),
		0, dnsTypeAAAA,
		0x80, 1,
		0, 0, 0, 120,
		0, 16,
		0xfd, 0xd5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x05,
	)
	got := ParseMDNS(buildMsg(0, 0, 0, 1, body))
	if got["fdd5::5"] != "AppleTV" {
		t.Fatalf("want AppleTV for fdd5::5, got %v", got)
	}
}

// A compression pointer may point anywhere, including at itself. Without a hop
// budget this input spins forever -- and it arrives from any device on the
// network, unauthenticated.
func TestParseMDNS_PointerLoopTerminates(t *testing.T) {
	body := []byte{
		0xC0, dnsHeaderLen, // a pointer to itself
		0, dnsTypeA, 0x80, 1, 0, 0, 0, 120, 0, 4, 10, 0, 0, 1,
	}
	done := make(chan struct{})
	go func() {
		ParseMDNS(buildMsg(0, 1, 0, 0, body))
		close(done)
	}()
	select {
	case <-done:
	default:
		// Give it a moment; the point is that it returns at all.
	}
	<-done
}

func TestParseMDNS_TruncatedIsSafe(t *testing.T) {
	full := append(name("Thing", "local"),
		0, dnsTypeA, 0x80, 1, 0, 0, 0, 120, 0, 4, 10, 0, 0, 1)
	msg := buildMsg(0, 1, 0, 0, full)
	// Every prefix must return rather than panic or read out of bounds.
	for i := 0; i < len(msg); i++ {
		ParseMDNS(msg[:i])
	}
}

func TestUDPPayload(t *testing.T) {
	// IPv4 header (20 bytes) + UDP header, destined for the mDNS port.
	pkt := make([]byte, 20+8+4)
	pkt[0] = 0x45                 // version 4, IHL 5
	pkt[9] = 17                   // UDP
	pkt[20], pkt[21] = 0x14, 0xe9 // sport 5353
	pkt[22], pkt[23] = 0x14, 0xe9 // dport 5353
	copy(pkt[28:], []byte{1, 2, 3, 4})

	copy(pkt[ipSrc:], []byte{192, 168, 0, 214})

	got, src, ok := udpPayload(pkt, mdnsPort)
	if !ok || len(got) != 4 || got[0] != 1 {
		t.Fatalf("want the 4-byte payload, got %v ok=%v", got, ok)
	}
	if src.String() != "192.168.0.214" {
		t.Fatalf("want the source address 192.168.0.214, got %v", src)
	}
	if _, _, ok := udpPayload(pkt, 53); ok {
		t.Fatal("must not match a different port")
	}
	if _, _, ok := udpPayload([]byte{0x45}, mdnsPort); ok {
		t.Fatal("must reject a runt packet")
	}
}

// iOS publishes a random UUID hostname per network alongside its friendly one.
// Whichever record arrived last used to win, so a device flipped between a real
// name and a UUID -- which is a worse label than the MAC it replaced.
func TestParseMDNS_RejectsUUIDHostname(t *testing.T) {
	body := append(name("b38b77bd-3b57-4cc3-9474-ef67aebf801f", "local"),
		0, dnsTypeA, 0x80, 1, 0, 0, 0, 120, 0, 4, 192, 168, 0, 214)
	got := ParseMDNS(buildMsg(0, 1, 0, 0, body))
	if v, ok := got["192.168.0.214"]; ok && v != "" {
		t.Fatalf("a bare UUID must not be used as a name, got %q", v)
	}
}

func TestIsUUIDName(t *testing.T) {
	for _, s := range []string{
		"b38b77bd-3b57-4cc3-9474-ef67aebf801f",
		"B38B77BD-3B57-4CC3-9474-EF67AEBF801F",
	} {
		if !isUUIDName(s) {
			t.Errorf("%q should be recognised as a UUID", s)
		}
	}
	for _, s := range []string{
		"Jonathans-iPhone", "appletv", "Graces-MacBook-Air-2",
		"HP9CB654502EE1", "a-b-c-d-e", "",
	} {
		if isUUIDName(s) {
			t.Errorf("%q must not be treated as a UUID", s)
		}
	}
}

// frame wraps a DNS message in a UDP datagram inside an IP packet, the shape
// the capture socket delivers: no Ethernet header, IP header first.
func frame(src, dst net.IP, msg []byte) []byte {
	udp := []byte{0x14, 0xe9, 0x14, 0xe9, 0, 0, 0, 0} // 5353 -> 5353
	udp[4] = byte((len(msg) + 8) >> 8)
	udp[5] = byte(len(msg) + 8)
	if v4 := src.To4(); v4 != nil {
		h := make([]byte, ipMinLen)
		h[0] = 0x45
		h[9] = 17
		copy(h[ipSrc:], v4)
		copy(h[16:], dst.To4())
		return append(append(h, udp...), msg...)
	}
	h := make([]byte, ip6MinLen)
	h[0] = 0x60
	h[6] = 17
	copy(h[ip6Src:], src.To16())
	copy(h[24:], dst.To16())
	return append(append(h, udp...), msg...)
}

// A device is named by the record for the address it is announcing FROM. That
// binding is the one that can be keyed by MAC, which is what makes a name stick
// to a device whose address this box has never otherwise seen.
func TestParseMDNSFrame_NamesTheSender(t *testing.T) {
	v4 := append(name("Jons-iPhone", "local"),
		0, dnsTypeA, 0x80, 1, 0, 0, 0, 120, 0, 4, 192, 168, 0, 214)
	_, sender := ParseMDNSFrame(frame(
		net.ParseIP("192.168.0.214"), net.ParseIP("224.0.0.251"),
		buildMsg(0, 1, 0, 0, v4)))
	if sender != "Jons-iPhone" {
		t.Fatalf("want Jons-iPhone, got %q", sender)
	}

	// IPv6 is the case that matters most: measured on a real network, most
	// announcements arrive over v6, on addresses boa has not otherwise seen.
	v6 := append(name("AppleTV", "local"),
		0, dnsTypeAAAA, 0x80, 1, 0, 0, 0, 120, 0, 16,
		0xfd, 0xd5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x05)
	byAddr, sender := ParseMDNSFrame(frame(
		net.ParseIP("fdd5::5"), net.ParseIP("ff02::fb"),
		buildMsg(0, 0, 0, 1, v6)))
	if sender != "AppleTV" {
		t.Fatalf("want AppleTV, got %q", sender)
	}
	if byAddr["fdd5::5"] != "AppleTV" {
		t.Fatalf("the address binding must survive too, got %v", byAddr)
	}
}

// Bonjour Sleep Proxy: an Apple TV answers on behalf of a sleeping laptop, so
// the name in the packet is not the sender's. Attributing it would put the
// laptop's name on the Apple TV's card and leave it there.
func TestParseMDNSFrame_RefusesAnotherHostsName(t *testing.T) {
	body := append(name("Graces-MacBook", "local"),
		0, dnsTypeA, 0x80, 1, 0, 0, 0, 120, 0, 4, 192, 168, 0, 50)
	byAddr, sender := ParseMDNSFrame(frame(
		net.ParseIP("192.168.0.9"), net.ParseIP("224.0.0.251"),
		buildMsg(0, 1, 0, 0, body)))
	if sender != "" {
		t.Fatalf("a name for another host must not be attributed to the sender, got %q", sender)
	}
	// It is still worth knowing by address; only the MAC binding is refused.
	if byAddr["192.168.0.50"] != "Graces-MacBook" {
		t.Fatalf("want the address binding kept, got %v", byAddr)
	}
}

func TestParseMDNSFrame_IgnoresNonMDNS(t *testing.T) {
	pkt := make([]byte, ipMinLen+8)
	pkt[0], pkt[9] = 0x45, 17
	pkt[22], pkt[23] = 0, 53 // port 53, not 5353
	if byAddr, sender := ParseMDNSFrame(pkt); byAddr != nil || sender != "" {
		t.Fatalf("want nothing from a non-mDNS packet, got %v %q", byAddr, sender)
	}
}
