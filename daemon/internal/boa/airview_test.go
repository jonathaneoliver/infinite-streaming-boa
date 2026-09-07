package boa

import "testing"

// The whole point: a scan taken by the radio that can afford one answers for
// the radios that cannot.
//
// wlan0 scans both bands while it keeps serving; the mt7921u adapters have to
// have their access point taken down to scan at all. So wlan-usb's contention
// figure comes from wlan0's scan, and nobody is dropped to get it.
func TestAirViewsAnswerForARadioThatNeverScanned(t *testing.T) {
	scans := map[string]ScanSummary{
		"wlan0": {At: 1000, Channels: []ScanChannel{
			{Channel: 6, UtilPct: 12, UtilFrom: 1, StrongestDBm: -34},
			{Channel: 40, UtilPct: 69.8, UtilFrom: 3, StrongestDBm: -18},
		}, Ours: map[string]float64{"wlan-usb": -27}},
	}
	got := airViews(scans, map[string]int{"wlan0": 6, "wlan-usb": 40})
	v, ok := got["wlan-usb"]
	if !ok {
		t.Fatal("wlan-usb got no view from wlan0's scan")
	}
	if v.From != "wlan0" {
		t.Errorf("From = %q, want wlan0", v.From)
	}
	if v.UtilPct != 69.8 || !v.UtilKnown {
		t.Errorf("util = %v known=%v, want 69.8 known", v.UtilPct, v.UtilKnown)
	}
	if v.LoudestDBm != -18 {
		t.Errorf("loudest = %v, want -18", v.LoudestDBm)
	}
	if v.OursDBm != -27 || !v.OursKnown {
		t.Errorf("ours = %v known=%v, want -27 known", v.OursDBm, v.OursKnown)
	}
}

// A radio cannot hear itself, so the scanner has no "ours" figure of its own --
// and that absence must be a flag rather than a zero, since 0 dBm is a
// perfectly valid (if implausible) signal.
func TestAirViewsHaveNoOwnSignalForTheScanningRadio(t *testing.T) {
	scans := map[string]ScanSummary{
		"wlan0": {At: 1000, Channels: []ScanChannel{{Channel: 6, StrongestDBm: -34}}},
	}
	v := airViews(scans, map[string]int{"wlan0": 6})["wlan0"]
	if v.OursKnown {
		t.Error("the scanning radio claims to have heard itself")
	}
	if v.LoudestDBm != -34 {
		t.Errorf("loudest = %v, want -34 -- the neighbours are still heard", v.LoudestDBm)
	}
}

// Freshest wins, even against the radio's own older scan. A scan describes a
// room that moves: channel 40 measured 9.8% idle and 69.8% under load minutes
// apart, so a stale first-hand reading is worth less than a current second-hand
// one.
func TestAirViewsPreferTheFreshestScanNotTheRadiosOwn(t *testing.T) {
	scans := map[string]ScanSummary{
		"wlan-usb": {At: 1000, Channels: []ScanChannel{
			{Channel: 40, UtilPct: 9.8, UtilFrom: 1},
		}},
		"wlan0": {At: 2000, Channels: []ScanChannel{
			{Channel: 40, UtilPct: 69.8, UtilFrom: 3},
		}},
	}
	v := airViews(scans, map[string]int{"wlan-usb": 40})["wlan-usb"]
	if v.From != "wlan0" || v.UtilPct != 69.8 {
		t.Errorf("got %s %v, want the newer wlan0 reading of 69.8", v.From, v.UtilPct)
	}
}

// A channel nobody has scanned gets NO entry. Absent means "not looked at";
// a zero would claim an idle channel on evidence nobody gathered.
func TestAirViewsOmitAChannelNobodyHasScanned(t *testing.T) {
	scans := map[string]ScanSummary{
		"wlan0": {At: 1000, Channels: []ScanChannel{{Channel: 6, UtilPct: 12, UtilFrom: 1}}},
	}
	got := airViews(scans, map[string]int{"wlan0": 6, "wlan-usb2": 149})
	if _, ok := got["wlan-usb2"]; ok {
		t.Error("channel 149 was never scanned but got a view anyway")
	}
	if _, ok := got["wlan0"]; !ok {
		t.Error("wlan0 lost its own view")
	}
}

// Nobody advertising BSS Load is not an idle channel. UtilFrom == 0 has to
// travel as UtilKnown == false, or the quietest-looking channel on screen is
// the one no neighbour measured.
func TestAirViewsMarkUtilisationUnknownWhenNobodyAdvertisedIt(t *testing.T) {
	scans := map[string]ScanSummary{
		"wlan0": {At: 1000, Channels: []ScanChannel{
			{Channel: 6, UtilPct: 0, UtilFrom: 0, StrongestDBm: -34},
		}},
	}
	v := airViews(scans, map[string]int{"wlan0": 6})["wlan0"]
	if v.UtilKnown {
		t.Error("utilisation reported as known when no neighbour advertised it")
	}
}

// A radio whose channel could not be read is skipped rather than matched
// against channel zero.
func TestAirViewsSkipARadioWithNoChannel(t *testing.T) {
	scans := map[string]ScanSummary{
		"wlan0": {At: 1000, Channels: []ScanChannel{{Channel: 0, UtilPct: 50, UtilFrom: 1}}},
	}
	if got := airViews(scans, map[string]int{"wlan0": 0}); len(got) != 0 {
		t.Errorf("got %+v, want nothing for a radio with no channel", got)
	}
}
