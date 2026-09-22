package boa

import (
	"strings"
	"testing"
)

// The `+N` on a radio's path is which phy on that device it is, and two phys
// sharing one device is the normal case rather than an oddity: MEASURED on a
// Cudy TR3000, whose 2.4GHz and 5GHz chains are both
// platform/soc/18000000.wifi, told apart only by the suffix.
//
// Pinned because the first version matched the path and ignored the suffix, so
// phy1 resolved to radio0. The revert then re-enabled a radio that was already
// serving and left phy1's access point down -- and the same mistake applied to
// a disable would have taken the WRONG radio off the air.
func TestUCIPathCarriesWhichPhyOnTheDevice(t *testing.T) {
	for _, c := range []struct {
		path string
		base string
		ord  int
	}{
		{"platform/soc/18000000.wifi", "platform/soc/18000000.wifi", 0},
		{"platform/soc/18000000.wifi+1", "platform/soc/18000000.wifi", 1},
		{"platform/soc/11200000.usb/usb2/2-1/2-1:1.0", "platform/soc/11200000.usb/usb2/2-1/2-1:1.0", 0},
		// A suffix that is not a number is part of the path, not an ordinal.
		{"platform/soc/wifi+x", "platform/soc/wifi+x", 0},
	} {
		base, ord := splitUCIPath(c.path)
		if base != c.base || ord != c.ord {
			t.Errorf("splitUCIPath(%q) = %q, %d; want %q, %d", c.path, base, ord, c.base, c.ord)
		}
	}
}

// The listen-only interface is named after its phy, and that name is the only
// thing left to rebuild it from after a reboot.
func TestScanIfaceNameRoundTrips(t *testing.T) {
	if got := scanIfaceFor("phy3"); got != "phy3-scan" {
		t.Fatalf("scanIfaceFor(phy3) = %q", got)
	}
	if got := phyOfScanIface("phy3-scan"); got != "phy3" {
		t.Fatalf("phyOfScanIface(phy3-scan) = %q", got)
	}
	// A scan port named by hand in the config is NOT this box's to recreate:
	// it belongs to whoever put it there, and inventing a phy for it would
	// make an interface nobody asked for.
	for _, name := range []string{"wlan0", "wlan-scan-3ff2", "scan", "phy3-ap0"} {
		if got := phyOfScanIface(name); got != "" {
			t.Errorf("phyOfScanIface(%q) = %q, want none", name, got)
		}
	}
}

// A SECOND SCANNER MUST NOT UNMAKE THE FIRST, which is exactly what shipped.
//
// MEASURED on the Cudy TR3000, 2026-09-22: phy0 had been listening for hours,
// and turning phy3 listen-only from the rack left `boa.main.scan=phy3-scan`.
// The phy0-scan interface was still there and still scanned by hand, but the
// daemon no longer knew it was an instrument -- so the rack drew it as an
// ordinary access point and offered deauth, evict and gather on a BSS that did
// not exist. Issue #351.
//
// Neither unit test in this file could have caught it: both are about ONE
// radio, and the option had always been a list. So this one presses the toggle
// twice, which is the smallest thing the box does that the tests did not.
func TestASecondScannerLeavesTheFirstAlone(t *testing.T) {
	for _, c := range []struct {
		what string
		have []string
		port string
		want bool
		out  []string
	}{
		{"the first scanner", nil, "phy0-scan", true, []string{"phy0-scan"}},
		{"and the second keeps it", []string{"phy0-scan"}, "phy3-scan", true,
			[]string{"phy0-scan", "phy3-scan"}},
		{"asking twice changes nothing", []string{"phy0-scan", "phy3-scan"}, "phy3-scan", true,
			[]string{"phy0-scan", "phy3-scan"}},
		// The same mistake pointed the other way: taking ONE radio back to
		// serving used to delete the option, and with it every other scanner.
		{"taking one back leaves the rest", []string{"phy0-scan", "phy3-scan"}, "phy3-scan", false,
			[]string{"phy0-scan"}},
		{"taking the last one back empties it", []string{"phy0-scan"}, "phy0-scan", false, []string{}},
		{"removing one that was never there", []string{"phy0-scan"}, "phy9-scan", false,
			[]string{"phy0-scan"}},
		// A hand-written config is the operator's, and order is theirs too:
		// the list is read back at every start, so reordering it would move
		// which radio answers first for no reason anybody asked for.
		{"a port named by hand survives", []string{"wlan-mon", "phy0-scan"}, "phy3-scan", true,
			[]string{"wlan-mon", "phy0-scan", "phy3-scan"}},
		{"and a duplicate in the file is not kept", []string{"phy0-scan", "phy0-scan"}, "phy3-scan", true,
			[]string{"phy0-scan", "phy3-scan"}},
	} {
		got := scanPortsAfter(c.have, c.port, c.want)
		if len(got) != len(c.out) {
			t.Errorf("%s: scanPortsAfter(%v, %q, %v) = %v, want %v",
				c.what, c.have, c.port, c.want, got, c.out)
			continue
		}
		for i := range got {
			if got[i] != c.out[i] {
				t.Errorf("%s: scanPortsAfter(%v, %q, %v) = %v, want %v",
					c.what, c.have, c.port, c.want, got, c.out)
				break
			}
		}
	}
}

// The option holds what the daemon's own parser reads back, or a list written
// here is a list nobody can use. SplitPorts is what main.go hands the flag to.
func TestTheWrittenListIsTheListThatIsReadBack(t *testing.T) {
	ports := scanPortsAfter([]string{"phy0-scan"}, "phy3-scan", true)
	back := SplitPorts(strings.Join(ports, " "))
	if len(back) != 2 || back[0] != "phy0-scan" || back[1] != "phy3-scan" {
		t.Fatalf("round trip lost a port: %v -> %q -> %v", ports, strings.Join(ports, " "), back)
	}
}
