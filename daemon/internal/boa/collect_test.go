package boa

import "testing"

// The fixture is the real neighbour table from the box, where one MAC held all
// three entries at once. Before the family check the fe80:: address was parsed
// last and displaced the v4 address, which then reached tc as an IPv4 filter
// and was rejected -- taking the device's IPv6 filters down with it, because
// writeFilters returns on the first error.
func TestNeighFromEntriesKeepsV4WhenAMACAlsoHasV6(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "192.168.0.25", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
		{Dst: "fdd5:a04f:f953:4412:cf8:b01b:263c:c723", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
		{Dst: "fe80::cf3:a992:792a:b28e", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
	})
	if want := "192.168.0.25"; got["12:bb:19:0e:ac:7c"] != want {
		t.Errorf("v6 displaced the v4 address: got %q, want %q",
			got["12:bb:19:0e:ac:7c"], want)
	}
}

// Ordering must not decide the answer. The kernel does not promise one, and the
// original bug was only visible because the fe80:: entry happened to come last.
func TestNeighFromEntriesIgnoresOrdering(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "fe80::cf3:a992:792a:b28e", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
		{Dst: "192.168.0.25", LLAddr: "12:bb:19:0e:ac:7c", State: []string{"STALE"}},
	})
	if want := "192.168.0.25"; got["12:bb:19:0e:ac:7c"] != want {
		t.Errorf("got %q, want %q", got["12:bb:19:0e:ac:7c"], want)
	}
}

// A MAC with nothing but v6 yields no entry at all, rather than an entry that
// cannot be turned into a working filter. Absent is honest; wrong is not.
func TestNeighFromEntriesDropsV6OnlyMACs(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "fdd5:a04f:f953:4412:81a:ed9:4fed:6ce", LLAddr: "a0:ce:c8:b6:de:53", State: []string{"REACHABLE"}},
	})
	if v, ok := got["a0:ce:c8:b6:de:53"]; ok {
		t.Errorf("v6-only MAC produced an IPv4 entry: %q", v)
	}
}

// The pre-existing reachability rule still holds: an address that did not
// answer must not carry a filter.
func TestNeighFromEntriesStillDropsUnreachable(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "192.168.0.9", LLAddr: "aa:bb:cc:dd:ee:01", State: []string{"FAILED"}},
		{Dst: "192.168.0.10", LLAddr: "aa:bb:cc:dd:ee:02", State: []string{"INCOMPLETE"}},
		{Dst: "192.168.0.11", LLAddr: "aa:bb:cc:dd:ee:03", State: []string{"REACHABLE"}},
	})
	if len(got) != 1 || got["aa:bb:cc:dd:ee:03"] != "192.168.0.11" {
		t.Errorf("reachability filtering regressed: %v", got)
	}
}

// Garbage in the Dst field must not become a filter argument.
func TestNeighFromEntriesRejectsUnparseableAddresses(t *testing.T) {
	got := neighFromEntries([]neighJSON{
		{Dst: "not-an-address", LLAddr: "aa:bb:cc:dd:ee:04", State: []string{"REACHABLE"}},
	})
	if len(got) != 0 {
		t.Errorf("unparseable address was accepted: %v", got)
	}
}

// --- USB attachment -------------------------------------------------------

// TestUSBUnderspeedIsAnythingBelowUSB3 pins the rule, which is deliberately
// blunt: on the bus and not at USB 3 rates.
//
// IT DOES NOT CONSULT THE DEVICE'S OWN CLAIM, and the first version of this
// did. sysfs `version` reports the bcdUSB of the CONNECTION rather than the
// hardware -- MEASURED on the Pi 2026-09-14, the same four adapters read
// " 3.20" while attached at 5000 and " 2.10" while attached at 480 through a
// USB 2 hub. So "declares USB 3 but negotiated USB 2" describes nothing that
// can happen, and the check it produced never fired on the box it was written
// for. This is what replaced it.
func TestUSBUnderspeedIsAnythingBelowUSB3(t *testing.T) {
	for _, c := range []struct {
		bus  string
		link int
		want bool
		why  string
	}{
		{"usb", 480, true, "measured on the box: a USB 2 path, whoever imposed it"},
		{"usb", 12, true, "Full-Speed is worse, not exempt"},
		{"usb", 5000, false, "measured after the fix: USB 3 rates"},
		{"usb", 10000, false, "USB 3.2 gen2x2 is not underspeed"},
		{"usb", 0, false, "no speed file is UNKNOWN, not slow"},
		{"onboard", 0, false, "the onboard radio is not on the bus at all"},
	} {
		r := RadioInfo{Bus: c.bus, LinkMbps: c.link}
		if got := r.LinkMbps > 0 && !r.SuperSpeed(); got != c.want {
			t.Errorf("bus=%q link=%d -> %v, want %v (%s)",
				c.bus, c.link, got, c.want, c.why)
		}
	}
}

// TestUSBUnderspeedIgnoresTheDeclaredVersion guards the mistake directly, so a
// future edit cannot quietly reintroduce a rule that reads `version`.
//
// Both rows were observed on the same four adapters on the same box an hour
// apart, with nothing changed but which hub they were plugged into.
func TestUSBUnderspeedIgnoresTheDeclaredVersion(t *testing.T) {
	for _, c := range []struct {
		version string
		link    int
		want    bool
	}{
		{" 2.10", 480, true},   // as observed on the USB 2 hub: still a fault
		{" 3.20", 5000, false}, // as observed on the USB 3 hub: fine
	} {
		r := RadioInfo{Bus: "usb", USBVersion: c.version, LinkMbps: c.link}
		if got := r.LinkMbps > 0 && !r.SuperSpeed(); got != c.want {
			t.Errorf("version=%q link=%d -> %v, want %v",
				c.version, c.link, got, c.want)
		}
	}
}
