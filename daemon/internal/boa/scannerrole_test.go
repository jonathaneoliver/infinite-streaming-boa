package boa

import "testing"

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
