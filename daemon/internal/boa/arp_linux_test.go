//go:build linux

package boa

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNameTableRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	l := NewLearner("br0", "wlan0", "lan0")
	l.storeNames("AA:BB:CC:11:22:33",
		map[string]string{"192.168.0.214": "Jons-iPhone"}, "Jons-iPhone")
	l.SaveNames(path)

	back := NewLearner("br0", "wlan0", "lan0")
	back.LoadNames(path)
	if got := back.MACNames()["aa:bb:cc:11:22:33"]; got != "Jons-iPhone" {
		t.Fatalf("want the MAC binding restored, got %q from %v", got, back.MACNames())
	}
	if got := back.Names()["192.168.0.214"]; got != "Jons-iPhone" {
		t.Fatalf("want the address binding restored, got %q", got)
	}
}

// Only a device downstream of the box is a client of it. Everything else on
// the segment announces itself too, and a name learned from upstream is at best
// useless and at worst what evicts a real client's name from a full table.
func TestDownstreamPortsAreTheOnesThatCount(t *testing.T) {
	l := NewLearner("br-lan", "wlan0", "lan0")
	for _, port := range []string{"wlan0", "lan0"} {
		if !l.downstream[port] {
			t.Errorf("%s is a client port and must be accepted", port)
		}
	}
	for _, port := range []string{"eth0", "br-lan", ""} {
		if l.downstream[port] {
			t.Errorf("%q is not a client port and must be rejected", port)
		}
	}
	// lan0 is a USB adapter that may not be fitted. An unset port is not a
	// port anything can arrive on, so it must not match the empty ifname a
	// failed interface lookup returns.
	if bare := NewLearner("br-lan", "wlan0", ""); bare.downstream[""] {
		t.Error("an absent LAN port must not accept unnamed interfaces")
	}
}

// A box upgraded in place still has the old bare address-to-name file. Reading
// it as nothing would blank every label on the one restart where the names have
// not been relearned yet.
func TestLoadNamesReadsTheOldFlatFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	if err := os.WriteFile(path, []byte(`{"192.168.0.214":"Jons-iPhone"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewLearner("br0", "wlan0", "lan0")
	l.LoadNames(path)
	if got := l.Names()["192.168.0.214"]; got != "Jons-iPhone" {
		t.Fatalf("want the old file honoured, got %q", got)
	}
}
