package boa

import "testing"

// dhcpPacket builds a BOOTP/DHCP message. op 1 is a client request; opts is
// appended after the magic cookie.
func dhcpPacket(op byte, hlen byte, chaddr []byte, cookie bool, opts ...byte) []byte {
	p := make([]byte, dhcpFixedLen)
	p[0] = op
	p[dhcpHLenOff] = hlen
	copy(p[dhcpCHAddrOff:], chaddr)
	if cookie {
		p = append(p, dhcpMagic[:]...)
	} else {
		p = append(p, 0, 0, 0, 0)
	}
	return append(p, opts...)
}

// hostnameOpt is option 12 carrying s.
func hostnameOpt(s string) []byte {
	return append([]byte{dhcpOptHostname, byte(len(s))}, []byte(s)...)
}

var testMAC = []byte{0x36, 0x15, 0xf0, 0x21, 0xe0, 0xe0}

func TestParseDHCPReadsNameAndMAC(t *testing.T) {
	pkt := dhcpPacket(dhcpOpRequest, dhcpEthHLen, testMAC, true, hostnameOpt("Watch")...)
	mac, name := ParseDHCP(pkt)
	if mac != "36:15:f0:21:e0:e0" {
		t.Errorf("mac = %q, want 36:15:f0:21:e0:e0", mac)
	}
	if name != "Watch" {
		t.Errorf("name = %q, want Watch", name)
	}
}

// Options before the hostname must be walked over, not tripped on. A real
// request puts 53 (message type) and 55 (parameter list) ahead of it.
func TestParseDHCPSkipsEarlierOptions(t *testing.T) {
	opts := []byte{53, 1, 3, 55, 3, 1, 3, 6, dhcpOptPad}
	opts = append(opts, hostnameOpt("MacBook-Pro")...)
	opts = append(opts, dhcpOptEnd)
	_, name := ParseDHCP(dhcpPacket(dhcpOpRequest, dhcpEthHLen, testMAC, true, opts...))
	if name != "MacBook-Pro" {
		t.Errorf("name = %q, want MacBook-Pro", name)
	}
}

// A server's reply also carries option 12, but that is the SERVER's idea of the
// client's name. Only what the device says about itself is wanted.
func TestParseDHCPIgnoresReplies(t *testing.T) {
	pkt := dhcpPacket(2, dhcpEthHLen, testMAC, true, hostnameOpt("NotThis")...)
	if mac, name := ParseDHCP(pkt); mac != "" || name != "" {
		t.Errorf("reply accepted: mac=%q name=%q", mac, name)
	}
}

func TestParseDHCPRejects(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
	}{
		{"no magic cookie", dhcpPacket(dhcpOpRequest, dhcpEthHLen, testMAC, false, hostnameOpt("X")...)},
		{"no hostname option", dhcpPacket(dhcpOpRequest, dhcpEthHLen, testMAC, true, 53, 1, 3, dhcpOptEnd)},
		{"empty", nil},
		{"truncated before options", make([]byte, dhcpFixedLen)},
		{"zero chaddr", dhcpPacket(dhcpOpRequest, dhcpEthHLen, make([]byte, 6), true, hostnameOpt("X")...)},
		{"non-ethernet hlen", dhcpPacket(dhcpOpRequest, 8, testMAC, true, hostnameOpt("X")...)},
	}
	for _, c := range cases {
		if mac, name := ParseDHCP(c.pkt); mac != "" || name != "" {
			t.Errorf("%s: accepted mac=%q name=%q", c.name, mac, name)
		}
	}
}

// Options are length-prefixed and come from the network. A length that runs off
// the end must stop the walk, not read past the buffer.
func TestParseDHCPMalformedOptionsDoNotPanic(t *testing.T) {
	cases := [][]byte{
		{dhcpOptHostname, 200, 'a'}, // length far beyond the buffer
		{dhcpOptHostname},           // option with no length byte at all
		{53, 255},                   // length with no value
		{dhcpOptHostname, 0},        // zero-length name
		{dhcpOptPad, dhcpOptPad, dhcpOptPad},
	}
	for i, opts := range cases {
		pkt := dhcpPacket(dhcpOpRequest, dhcpEthHLen, testMAC, true, opts...)
		_, name := ParseDHCP(pkt) // must simply return, never panic
		if i == 3 && name != "" {
			t.Errorf("zero-length name produced %q", name)
		}
	}
}

// A device chooses these bytes. The interface has to render whatever arrives.
func TestSanitiseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Watch", "Watch"},
		{"Jonathans-iPhone", "Jonathans-iPhone"},
		{"padded\x00\x00\x00", "padded"}, // C-string padding
		{"trailing.", "trailing"},        // a stray FQDN dot
		{"  spaced  ", "spaced"},
		{"ctrl\x07chars\x1b", "ctrlchars"}, // bell and escape stripped
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitiseName(c.in); got != c.want {
			t.Errorf("sanitiseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	if got := sanitiseName(string(long)); len(got) != 63 {
		t.Errorf("a 300-byte name was not capped: got %d bytes", len(got))
	}
}
