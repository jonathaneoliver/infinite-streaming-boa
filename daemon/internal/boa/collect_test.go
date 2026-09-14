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

// TestUSBUnderspeedOnlyFlagsAdaptersThatCanDoBetter pins the comparison that
// makes this fault reportable without a threshold guess.
//
// The values are the ones measured on the Pi 2026-09-14: every adapter
// declared bcdUSB 3.20 and negotiated 480 through a USB-C to USB-A lead wired
// for USB 2, which held the Wi-Fi downlink to 92 Mbit/s where a USB 3 port
// gave 632 -- all while the box reported a 1000 Mbit/s link and a 961 Mbit/s
// PHY rate.
//
// The negative cases matter as much as the positive one. A genuinely USB 2
// adapter declares 2.x and 480 is the RIGHT answer for it, so flagging it
// would put a permanent warning on hardware that is behaving.
func TestUSBUnderspeedOnlyFlagsAdaptersThatCanDoBetter(t *testing.T) {
	for _, c := range []struct {
		version string
		link    int
		want    bool
		why     string
	}{
		{" 3.20", 480, true, "measured: the fault, declares USB 3 and got USB 2 rates"},
		{" 3.00", 480, true, "USB 3.0 exactly, still capable of more than it got"},
		{" 3.20", 5000, false, "measured after the fix: attached at full rate"},
		{" 2.10", 480, false, "a real USB 2 hub; 480 is correct for it, not a fault"},
		{" 2.00", 480, false, "likewise"},
		{" 3.20", 0, false, "no speed file is UNKNOWN, not slow -- the onboard radio"},
		{"", 480, false, "no version to judge against; do not convict"},
		{"nonsense", 480, false, "unparseable version reads as no claim"},
		{" 3.20", 10000, false, "USB 3.2 gen2x2 is not underspeed either"},
	} {
		if got := underspeedUSB(c.version, c.link); got != c.want {
			t.Errorf("underspeedUSB(%q, %d) = %v, want %v (%s)",
				c.version, c.link, got, c.want, c.why)
		}
	}
}

// TestUSBMajorParsesWhatSysfsActuallyWrites covers the format rather than the
// judgement: the file carries a leading space and two decimals.
func TestUSBMajorParsesWhatSysfsActuallyWrites(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{
		{" 3.20", 3},
		{" 2.10", 2},
		{" 3.00", 3},
		{"3.20\n", 3},
		{" 1.10", 1},
		{"", 0},
		{"x.yz", 0},
		{".20", 0},
	} {
		if got := usbMajor(c.in); got != c.want {
			t.Errorf("usbMajor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestSuperSpeedIsNotTheInverseOfUnderspeed guards the distinction the two
// helpers exist to keep apart: a USB 2 adapter is not SuperSpeed and is also
// not at fault, and collapsing them would warn about working hardware.
func TestSuperSpeedIsNotTheInverseOfUnderspeed(t *testing.T) {
	usb2 := RadioInfo{Bus: "usb", USBVersion: " 2.10", LinkMbps: 480}
	usb2.USBUnderspeed = underspeedUSB(usb2.USBVersion, usb2.LinkMbps)
	if usb2.SuperSpeed() {
		t.Error("a 480 Mbit/s adapter is not SuperSpeed")
	}
	if usb2.USBUnderspeed {
		t.Error("a USB 2 adapter at 480 is behaving; it must not be flagged")
	}

	onboard := RadioInfo{Bus: "onboard"}
	if onboard.SuperSpeed() {
		t.Error("the onboard radio is not on the bus, so not SuperSpeed")
	}
	if underspeedUSB(onboard.USBVersion, onboard.LinkMbps) {
		t.Error("the onboard radio has no USB speed to be under")
	}
}
