package boa

import (
	"strings"
	"testing"
)

// The listen-only radio: present, deliberately not serving, scanned so the
// contention figures cost no outage. See #288.
//
// Every test here is about the same confusion. Before this role existed, a
// wireless interface that was not serving meant one thing -- a radio whose
// clients nobody is conditioning -- and the box reported an instrument doing
// its job with the words it used for a fault.

// scanCfg is the arrangement #288 describes: the USB adapter serves, the
// onboard radio listens.
var scanCfg = Config{
	Bridge: "br-lan", WANPort: "eth0", LanPorts: []string{"lan0"},
	WlanPorts: []string{"wlan-usb"}, ScanPorts: []string{"wlan0"},
}

func TestAScannerIsItsOwnRoleNotAnIdleRadio(t *testing.T) {
	if got := ifaceRole("wlan0", IfaceInfo{Wireless: true}, scanCfg); got != RoleScanner {
		t.Errorf("the configured scanner: got %q, want %q", got, RoleScanner)
	}
	// The same interface name on a box that never named it is still just a
	// radio, so the role comes from configuration and not from the name.
	if got := ifaceRole("wlan0", IfaceInfo{Wireless: true}, testCfg); got != RoleRadio {
		t.Errorf("an unnamed radio: got %q, want %q", got, RoleRadio)
	}
	// And a WIRED port named as a scanner is not a scanner. Nothing should
	// reach this -- build.sh refuses the WAN port by name -- but the role is
	// derived from being wireless as well as from being named.
	wiredScan := Config{Bridge: "br-lan", WANPort: "eth0", ScanPorts: []string{"eth9"}}
	if got := ifaceRole("eth9", IfaceInfo{Wireless: false}, wiredScan); got != RoleOther {
		t.Errorf("a wired port named as a scanner: got %q, want %q", got, RoleOther)
	}
}

func TestAScannerIsNotWatchedAndIsNotAFault(t *testing.T) {
	// IsWlan and IsScanner answer different questions, and the scanner must
	// answer no to the first: every site that iterates WlanPorts is asking
	// which radios serve clients.
	if scanCfg.IsWlan("wlan0") {
		t.Error("the scanner must not be one of the watched radios")
	}
	if !scanCfg.IsScanner("wlan0") {
		t.Error("the scanner must be recognised as one")
	}
	if scanCfg.IsScanner("wlan-usb") {
		t.Error("a serving radio must not be recognised as a scanner")
	}
}

func TestAScannerProducesNoIdleRadioNote(t *testing.T) {
	// "wlan0 is up but not serving the access point" is true of a scanner and
	// reads as a fault. This is the note that made the box report its
	// instrument as its problem.
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: true, AP: &APStatus{Enabled: true}},
		{Name: "wlan0", Role: RoleScanner, Wireless: true, Serving: false, Up: true},
	}}
	if notes := bridgeNotes(bi, scanCfg); len(notes) != 0 {
		t.Errorf("a listening radio is not worth a note: %+v", notes)
	}
}

func TestAScannerThatIsDownIsWorthSaying(t *testing.T) {
	// The one thing about a scanner that IS a fault: nothing is refreshing the
	// figures, and every channel colour on the box quietly goes stale.
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: true, AP: &APStatus{Enabled: true}},
		{Name: "wlan0", Role: RoleScanner, Wireless: true, Serving: false, Up: false},
	}}
	notes := bridgeNotes(bi, scanCfg)
	if len(notes) != 1 || notes[0].Level != "warn" {
		t.Fatalf("want one warn note, got %+v", notes)
	}
	for _, want := range []string{"wlan0", "contention"} {
		if !strings.Contains(notes[0].Text, want) {
			t.Errorf("note should mention %q: %s", want, notes[0].Text)
		}
	}
}

func TestAConfiguredScannerThatIsNotThereIsAnError(t *testing.T) {
	// A mistyped interface name. The symptom otherwise is a box with no
	// instrument, no error, and readings that go on costing an outage for a
	// reason nothing states -- which is the silent failure this repository
	// keeps finding.
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: true, AP: &APStatus{Enabled: true}},
	}}
	notes := bridgeNotes(bi, scanCfg)
	if len(notes) != 1 || notes[0].Level != "error" {
		t.Fatalf("want one error note, got %+v", notes)
	}
	if !strings.Contains(notes[0].Text, "wlan0") {
		t.Errorf("the note must name the missing interface: %s", notes[0].Text)
	}
}

func TestReconcileRolesLetsTheOperatorWin(t *testing.T) {
	// The planner writes WlanPorts from whatever hardware it found; ScanPorts
	// is a person naming one radio as an instrument. Honouring the planner
	// would put that instrument on air and take the reading away.
	cfg := Config{
		WlanPorts: []string{"wlan-usb", "wlan0"},
		ScanPorts: []string{"wlan0"},
	}
	dropped := reconcileRoles(&cfg)
	if len(dropped) != 1 || dropped[0] != "wlan0" {
		t.Fatalf("want wlan0 reported as dropped, got %v", dropped)
	}
	if len(cfg.WlanPorts) != 1 || cfg.WlanPorts[0] != "wlan-usb" {
		t.Errorf("wlan0 should be out of WlanPorts, got %v", cfg.WlanPorts)
	}
	if cfg.IsWlan("wlan0") {
		t.Error("a reconciled scanner must not still be watched")
	}
}

func TestReconcileRolesIsSilentWhenNothingOverlaps(t *testing.T) {
	// It returns names so the caller can SAY so, which means it must return
	// none when there is nothing to say -- otherwise every boot logs a
	// disagreement that did not happen.
	cfg := Config{WlanPorts: []string{"wlan-usb"}, ScanPorts: []string{"wlan0"}}
	if dropped := reconcileRoles(&cfg); dropped != nil {
		t.Errorf("want nothing dropped, got %v", dropped)
	}
	if len(cfg.WlanPorts) != 1 {
		t.Errorf("WlanPorts should be untouched, got %v", cfg.WlanPorts)
	}
	// And a box with no scanner at all is left exactly as it was.
	plain := Config{WlanPorts: []string{"wlan-usb", "wlan0"}}
	if dropped := reconcileRoles(&plain); dropped != nil {
		t.Errorf("no scanner configured, want nothing dropped, got %v", dropped)
	}
	if len(plain.WlanPorts) != 2 {
		t.Errorf("WlanPorts should be untouched, got %v", plain.WlanPorts)
	}
}

func TestTheScannerIsThePollsFirstChoice(t *testing.T) {
	// It is free by DEFINITION -- no access point to take down -- so it is
	// taken without consulting scanFree, which is what lets a box with one
	// skip the probe-an-idle-radio dance entirely.
	//
	// Demo mode stands in for LinkExists here: the point under test is the
	// preference order, not whether the hardware is plugged in.
	cfg := scanCfg
	cfg.Demo = true
	e := newEngine(cfg)
	// wlan-usb is recorded as a free scanner too, so a wrong preference order
	// would still return something and the test has to distinguish them.
	e.scanFree = map[string]bool{"wlan-usb": true}
	if got := e.freeScanner(); got != "wlan0" {
		t.Errorf("the listen-only radio should be chosen first: got %q", got)
	}
}

func TestWithoutAScannerThePollFallsBackToWhatItLearned(t *testing.T) {
	cfg := testCfg
	cfg.Demo = true
	e := newEngine(cfg)
	e.scanFree = map[string]bool{"wlan-usb": true}
	if got := e.freeScanner(); got != "wlan-usb" {
		t.Errorf("want the learned-free radio, got %q", got)
	}
}

func TestAScannerNeedsNoHostapdToBeScanned(t *testing.T) {
	// radioReady insists on a hostapd control socket, which is right for every
	// action that IS a hostapd command and wrong for a scan. Sending a scanner
	// through it refused with "hostapd is not serving" -- true, and entirely
	// the wrong reason -- and the background poll then skipped the one radio
	// that could answer for free, silently.
	cfg := scanCfg
	cfg.Demo = true
	e := newEngine(cfg)
	if err := e.readyToScan("wlan0"); err != nil {
		t.Errorf("the scanner should be scannable: %v", err)
	}
	if err := e.readyToScan(""); err == nil {
		t.Error("an unnamed radio should still be refused")
	}
}

func TestScanBlockedIsReportedOnTheEdge(t *testing.T) {
	// The poll runs every 15 seconds and the conditions here persist, so a
	// line per round would be four a minute for ever. Silence is not the
	// alternative: that is the bug this replaces.
	e := newEngine(scanCfg)
	e.noteScanBlocked("wlan0", "interface is down")
	first := len(e.Events(0, 0))
	if first == 0 {
		t.Fatal("the first refusal must be reported")
	}
	e.noteScanBlocked("wlan0", "interface is down")
	if got := len(e.Events(0, 0)); got != first {
		t.Errorf("the same reason must not be logged twice: %d -> %d", first, got)
	}
	// A DIFFERENT reason is new information and is reported.
	e.noteScanBlocked("wlan0", "no interface named wlan0 on this box")
	if got := len(e.Events(0, 0)); got <= first {
		t.Errorf("a changed reason should be reported: %d -> %d", first, got)
	}
	// And clearing says the figures are live again, which is the half that
	// makes the warning safe to trust.
	before := len(e.Events(0, 0))
	e.noteScanBlocked("wlan0", "")
	if got := len(e.Events(0, 0)); got <= before {
		t.Error("recovery should be reported too")
	}
	e.noteScanBlocked("wlan0", "")
	if got := len(e.Events(0, 0)); got != before+1 {
		t.Error("a second clear must not be logged")
	}
}
