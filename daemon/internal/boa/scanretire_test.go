package boa

import "testing"

// A serve retires the radio's listen-only interface before deleting it, so the
// rebuild timer -- which works from the ScanPorts read at startup -- cannot
// put it back in the seconds before the restart (2026-10-04, the Cudy).
func TestRetiredScanInterfaceIsNotRebuilt(t *testing.T) {
	e := &Engine{cfg: Config{ScanPorts: []string{"phy3-scan"}}}
	if e.scanIsRetired("phy3-scan") {
		t.Fatal("phy3-scan reads as retired before anything retired it")
	}
	e.retireScan("phy3-scan")
	if !e.scanIsRetired("phy3-scan") {
		t.Fatal("retireScan did not stick")
	}
	if e.scanIsRetired("phy0-scan") {
		t.Fatal("retiring one radio's interface retired another's")
	}
}

// An unclaimed listen-only interface is deleted only when its radio is serving
// an access point: that is a leftover from a serve. On a radio doing nothing
// else it may be deliberate, and is left alone.
func TestOrphanIsDeletedOnlyBesideAServingAP(t *testing.T) {
	cases := []struct {
		phy, name string
		servesAP  bool
		want      bool
	}{
		{"phy3", "phy3-scan", true, true},   // the Cudy case
		{"phy3", "phy3-scan", false, false}, // a radio that only listens
		{"phy3", "phy2-scan", true, false},  // not this radio's interface
		{"", "phy3-scan", true, false},
		{"phy3", "wlan0", true, false}, // not a listen-only name
	}
	for _, c := range cases {
		if got := orphanIsLeftOverFromServe(c.phy, c.name, c.servesAP); got != c.want {
			t.Errorf("orphanIsLeftOverFromServe(%q, %q, %v) = %v, want %v",
				c.phy, c.name, c.servesAP, got, c.want)
		}
	}
}
