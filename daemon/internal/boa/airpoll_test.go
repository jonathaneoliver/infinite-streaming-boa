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

// TestABandCrossingIsNotNews is the flood that survived the first gate.
//
// MEASURED on the box 2026-09-14, three lines in 130 seconds, and two of them
// were one band's busiest channel overtaking the other's: ch1 at 36%, then
// ch36 at 67%, then ch1 at 36% again. Nothing an operator acts on had changed
// in either band -- 2.4GHz was ch1 throughout and 5GHz was the ch36 block
// throughout -- so ranking them against each other was manufacturing the news.
func TestABandCrossingIsNotNews(t *testing.T) {
	// Both bands in every reading, which is what a wlan0 sweep returns. Only
	// the 5GHz level moves, and it crosses ch1's on the way.
	at := func(util5 int) ScanResult {
		return ScanResult{Best: 11, Channels: []ScanChannel{
			{Channel: 1, UtilPct: 36, UtilFrom: 2},
			{Channel: 36, UtilPct: float64(util5), UtilFrom: 4},
		}}
	}
	e := &Engine{}
	if !e.worthLogging("wlan0", at(30)) {
		t.Fatal("the first scan was suppressed")
	}
	// 30 -> 45 -> 30: ch36 overtakes ch1 and falls back, and in a single
	// busiest-channel-in-the-sweep world each step was a line.
	if e.worthLogging("wlan0", at(45)) {
		t.Error("logged a 5GHz level crossing 2.4GHz's, which is not a change " +
			"in either band")
	}
	if e.worthLogging("wlan0", at(30)) {
		t.Error("logged the crossing back again")
	}
}

// Each band still speaks for itself: a change in 5GHz must not be swallowed
// because 2.4GHz is quiet and unchanged. This is the half that a "judge only
// the scanning radio's own band" rule would have broken -- the free scanner
// here lives on 2.4GHz and 5GHz is where every client is.
func TestEachBandIsJudgedOnItsOwnHistory(t *testing.T) {
	with := func(ch5 int, util5 int) ScanResult {
		return ScanResult{Best: 11, Channels: []ScanChannel{
			{Channel: 1, UtilPct: 36, UtilFrom: 2},
			{Channel: ch5, UtilPct: float64(util5), UtilFrom: 4},
		}}
	}
	e := &Engine{}
	e.worthLogging("wlan0", with(36, 20)) // settle
	if !e.worthLogging("wlan0", with(149, 20)) {
		t.Error("the busiest 5GHz channel moved and nothing was said")
	}
	e2 := &Engine{}
	e2.worthLogging("wlan0", with(36, 20))
	if !e2.worthLogging("wlan0", with(36, 20+scanUtilJump)) {
		t.Error("5GHz airtime jumped a full band and nothing was said")
	}
}

// A band a sweep did not reach INHERITS what was last known about it. Coverage
// varies round to round -- two consecutive wlan0 scans on the box returned 7
// and 11 channels -- and a band dropping out of one sweep is variation in what
// was heard, not a change in the air.
func TestABandMissingFromASweepIsNotAChange(t *testing.T) {
	both := ScanResult{Best: 11, Channels: []ScanChannel{
		{Channel: 1, UtilPct: 36, UtilFrom: 2},
		{Channel: 36, UtilPct: 22, UtilFrom: 4},
	}}
	only24 := ScanResult{Best: 11, Channels: []ScanChannel{
		{Channel: 1, UtilPct: 36, UtilFrom: 2},
	}}
	e := &Engine{}
	e.worthLogging("wlan0", both)
	if e.worthLogging("wlan0", only24) {
		t.Error("a sweep that heard no 5GHz logged as though 5GHz had changed")
	}
	// And the inherited mark is still the one compared against when it returns.
	if e.worthLogging("wlan0", both) {
		t.Error("5GHz came back unchanged and was reported as news")
	}
}

// An 80MHz neighbour gives its whole block one identical figure, so the
// busiest channel in a band is a tie -- and a tie has to break the same way
// every round or it flaps for ever. Measured on the box: ch36, 40, 44 and 48
// all read 22%.
func TestATiedBlockPicksTheSameChannelEveryTime(t *testing.T) {
	block := func() ScanResult {
		r := ScanResult{Best: 11}
		for _, ch := range []int{36, 40, 44, 48} {
			r.Channels = append(r.Channels, ScanChannel{
				Channel: ch, UtilPct: 22, UtilFrom: 4})
		}
		return r
	}
	e := &Engine{}
	e.worthLogging("wlan0", block())
	for i := 0; i < 3; i++ {
		if e.worthLogging("wlan0", block()) {
			t.Fatalf("round %d logged an identical tied block", i+2)
		}
	}
}
