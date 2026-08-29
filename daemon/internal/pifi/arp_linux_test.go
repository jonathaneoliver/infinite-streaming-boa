//go:build linux

package pifi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNameTableRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "names.json")
	l := NewLearner("br0")
	l.storeNames("AA:BB:CC:11:22:33",
		map[string]string{"192.168.0.214": "Jons-iPhone"}, "Jons-iPhone")
	l.SaveNames(path)

	back := NewLearner("br0")
	back.LoadNames(path)
	if got := back.MACNames()["aa:bb:cc:11:22:33"]; got != "Jons-iPhone" {
		t.Fatalf("want the MAC binding restored, got %q from %v", got, back.MACNames())
	}
	if got := back.Names()["192.168.0.214"]; got != "Jons-iPhone" {
		t.Fatalf("want the address binding restored, got %q", got)
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
	l := NewLearner("br0")
	l.LoadNames(path)
	if got := l.Names()["192.168.0.214"]; got != "Jons-iPhone" {
		t.Fatalf("want the old file honoured, got %q", got)
	}
}
