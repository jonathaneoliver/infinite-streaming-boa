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

// scan builds the reading the poll would get: one busiest measured channel, a
// recommendation, and a second measured channel so there is a range.
func scan(busiest, util, best int) ScanResult {
	return ScanResult{Best: best, Channels: []ScanChannel{
		{Channel: busiest, APs: 6, Stations: 30, UtilPct: float64(util), UtilFrom: 2},
		{Channel: 11, APs: 4, Stations: 12, UtilPct: 3, UtilFrom: 1},
	}}
}

// TestSteadyAirIsMeasuredOnceAndNotLoggedAgain is the whole point of the gate.
//
// MEASURED on the box before it existed: seven of the fifteen events in the
// activity log were wlan0 re-measuring the same air, one every ~16s. The seven
// airtime figures were 32, 30, 44, 33, 33, 37 and 37 percent -- every one
// honest, none of them news -- so those are the readings fed in here.
func TestSteadyAirIsMeasuredOnceAndNotLoggedAgain(t *testing.T) {
	e := &Engine{}
	if !e.worthLogging("wlan0", scan(1, 32, 11)) {
		t.Fatal("the FIRST scan was suppressed; silence would then mean both " +
			"\"steady\" and \"never ran\"")
	}
	for _, util := range []int{30, 44, 33, 33, 37, 37} {
		if e.worthLogging("wlan0", scan(1, util, 11)) {
			t.Errorf("ch1 at %d%% was logged: same busiest channel, same "+
				"recommendation, drift of %d points", util, util-32)
		}
	}
}

// The three changes that ARE news, each from a settled state.
func TestScanLogsWhatAnOperatorWouldActOn(t *testing.T) {
	for _, c := range []struct {
		why  string
		then ScanResult
	}{
		{"the busiest channel moved", scan(6, 32, 11)},
		{"the recommendation moved", scan(1, 32, 1)},
		{"the air changed regime", scan(1, 32+scanUtilJump, 11)},
	} {
		e := &Engine{}
		e.worthLogging("wlan0", scan(1, 32, 11)) // settle
		if !e.worthLogging("wlan0", c.then) {
			t.Errorf("suppressed a line when %s", c.why)
		}
	}
}

// Per radio, not per box. Two radios are two subjects, and one going quiet must
// not silence the other's first word.
func TestScanLoggingIsRememberedPerRadio(t *testing.T) {
	e := &Engine{}
	if !e.worthLogging("wlan0", scan(1, 32, 11)) {
		t.Fatal("first scan of wlan0 suppressed")
	}
	if !e.worthLogging("wlan-scan-9e44", scan(1, 32, 11)) {
		t.Error("the scanner's first scan was suppressed by wlan0's mark")
	}
}

// A scan nobody could measure still says so once, and then stops. The headcount
// is all such a scan has, and repeating it every 15 seconds is the same flood.
func TestUnmeasuredAirIsAlsoOnlySaidOnce(t *testing.T) {
	none := ScanResult{Channels: []ScanChannel{{Channel: 36, APs: 5, Stations: 9}}}
	e := &Engine{}
	if !e.worthLogging("wlan0", none) {
		t.Fatal("the first unmeasured scan was suppressed")
	}
	if e.worthLogging("wlan0", none) {
		t.Error("an unmeasured scan repeated itself into the log")
	}
}
