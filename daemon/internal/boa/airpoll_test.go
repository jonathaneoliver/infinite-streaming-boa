package boa

import "testing"

// A radio that refuses to scan while serving must be REMEMBERED as refusing.
//
// Observed on the box 2026-09-07, immediately after a deploy: the poll asked
// wlan-usb every 15 seconds for ever, failed every time, and never reached
// wlan0 -- which scans both bands for free and would have answered for all
// three radios. The refusal was recorded only on the success path, so the cost
// stayed unknown and the candidate loop kept picking the same radio.
//
// It cost nothing but the figures, since the whole point of scanBandFree is
// that a refusal is free. Before that existed, this same loop was taking the
// radio off the air on every round.
func TestScanCostIsRememberedWhenTheDriverRefuses(t *testing.T) {
	e := &Engine{}
	if e.scanCostKnown("wlan-usb") {
		t.Fatal("cost known before anything was tried")
	}
	// What the refusal path does.
	e.rememberScanCost("wlan-usb", true)

	if !e.scanCostKnown("wlan-usb") {
		t.Error("a refusal was not recorded, so the poll would ask again for ever")
	}
	if e.freeScanner() == "wlan-usb" {
		t.Error("a radio that refused is being offered as the free scanner")
	}
}

// And the free one is preferred once known, whatever order the ports are in.
//
// The ports here are listed with the expensive radios first, which is the real
// configuration on this box -- wlan-usb, wlan-usb2, wlan0 -- and is why picking
// "the first candidate" rather than "the known-free one" put an access point
// off the air for 8 seconds to learn what wlan0 had already established.
func TestFreeScannerIsPreferredOverAnUntriedRadio(t *testing.T) {
	e := &Engine{cfg: Config{WlanPorts: []string{"wlan-usb", "wlan-usb2", "wlan0"}}}
	e.rememberScanCost("wlan-usb", true) // refused
	e.rememberScanCost("wlan0", false)   // free

	if got := e.freeScanner(); got != "wlan0" {
		t.Errorf("freeScanner = %q, want wlan0 -- the only one known to be free", got)
	}
}

// Nothing known yet means nothing is claimed to be free.
func TestFreeScannerIsEmptyBeforeAnythingIsKnown(t *testing.T) {
	e := &Engine{cfg: Config{WlanPorts: []string{"wlan-usb", "wlan0"}}}
	if got := e.freeScanner(); got != "" {
		t.Errorf("freeScanner = %q on a box that has scanned nothing", got)
	}
}
