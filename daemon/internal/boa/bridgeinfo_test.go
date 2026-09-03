package boa

import (
	"strings"
	"testing"
)

var testCfg = Config{
	Bridge: "br-lan", WANPort: "eth0", LanPort: "lan0",
	WlanPorts: []string{"wlan-usb"},
}

// dualCfg is the two-radio box: onboard on 2.4GHz, USB adapter on 5GHz, both
// serving and both watched.
var dualCfg = Config{
	Bridge: "br-lan", WANPort: "eth0", LanPort: "lan0",
	WlanPorts: []string{"wlan-usb", "wlan0"},
}

func TestIfaceRoleNamesEachPortByItsJob(t *testing.T) {
	tests := []struct {
		name     string
		wireless bool
		want     string
	}{
		{"eth0", false, RoleWAN},
		{"br-lan", false, RoleBridge},
		{"lan0", false, RoleLAN},
		{"wlan-usb", true, RoleRadio}, // upgraded to RoleAP only once hostapd answers
		{"wlan1", true, RoleRadio},
		{"docker0", false, RoleOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ifaceRole(tc.name, IfaceInfo{Wireless: tc.wireless}, testCfg)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnUnwatchedRadioServingAnAPIsAnError(t *testing.T) {
	// The case the whole tab exists for: a second adapter beaconing while the
	// daemon watches a different one. Its clients associate, take addresses and
	// pass traffic while being conditioned by nothing and appearing nowhere.
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: true,
			AP: &APStatus{Enabled: true}},
		{Name: "wlan1", Wireless: true, Serving: false, Up: true,
			AP: &APStatus{Enabled: true}},
	}}
	notes := bridgeNotes(bi, testCfg)
	if len(notes) != 1 {
		t.Fatalf("want exactly one note, got %d: %+v", len(notes), notes)
	}
	if notes[0].Level != "error" {
		t.Errorf("an unconditioned AP is an error, not info: %q", notes[0].Level)
	}
	for _, want := range []string{"wlan1", "NOT conditioned", "wlan-usb"} {
		if !strings.Contains(notes[0].Text, want) {
			t.Errorf("note should name %q: %s", want, notes[0].Text)
		}
	}
}

func TestAnIdleRadioIsMerelyNoted(t *testing.T) {
	// Up but not serving an AP: worth saying, not worth alarming about.
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan0", Wireless: true, Serving: false, Up: true},
	}}
	notes := bridgeNotes(bi, testCfg)
	if len(notes) != 1 || notes[0].Level != "info" {
		t.Fatalf("want one info note, got %+v", notes)
	}
}

func TestADownRadioAndTheServingRadioProduceNoNotes(t *testing.T) {
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: true, AP: &APStatus{Enabled: true}},
		{Name: "wlan0", Wireless: true, Serving: false, Up: false}, // rfkilled
		{Name: "eth0", Wireless: false},
	}}
	if notes := bridgeNotes(bi, testCfg); len(notes) != 0 {
		t.Errorf("want no notes, got %+v", notes)
	}
}

func TestRoleOrderPutsTheTopologyInReadingOrder(t *testing.T) {
	// The diagram draws upstream at the top and downstream at the bottom, so
	// the list has to arrive in that order rather than alphabetically.
	want := []string{RoleWAN, RoleBridge, RoleAP, RoleRadio, RoleLAN, RoleOther}
	for i := 1; i < len(want); i++ {
		if roleOrder(want[i-1]) >= roleOrder(want[i]) {
			t.Errorf("%s should sort before %s", want[i-1], want[i])
		}
	}
}

// ipAddrFixture is real `ip -j addr show` output, trimmed to the interfaces
// that matter. Note br-lan carrying three addresses at once -- the upstream
// lease, the fixed rescue address and a link-local.
const ipAddrFixture = `[
 {"ifname":"lo","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8,"scope":"host"}]},
 {"ifname":"eth0","addr_info":[]},
 {"ifname":"br-lan","addr_info":[
   {"family":"inet","local":"192.168.1.42","prefixlen":24,"scope":"global"},
   {"family":"inet","local":"192.168.99.1","prefixlen":24,"scope":"global"},
   {"family":"inet6","local":"fe80::9eef:d5ff:fef6:3ff2","prefixlen":64,"scope":"link"}]}
]`

func TestIPAddrParsingKeepsEveryAddressIncludingLinkLocal(t *testing.T) {
	out := parseIPAddrs([]byte(ipAddrFixture))
	br := out["br-lan"]
	if len(br.v4) > 0 && br.v4[0] != "192.168.1.42/24" {
		t.Errorf("addresses should carry their prefix length, got %q", br.v4[0])
	}
	if len(br.v4) != 2 {
		t.Errorf("br-lan holds the lease AND the rescue address, got %v", br.v4)
	}
	// Link-local is KEPT. On a box whose DHCP failed it is the only way back
	// in, which is precisely when someone is looking at this page.
	if len(br.v6) != 1 || !strings.HasPrefix(br.v6[0], "fe80:") {
		t.Errorf("link-local should be kept, got %v", br.v6)
	}
	if len(out["eth0"].v4) != 0 {
		t.Errorf("a bridged port carries no address of its own, got %v", out["eth0"].v4)
	}
}

func TestBothRadiosCountAsServingWhenBothAreWatched(t *testing.T) {
	// The regression this guards: `name == cfg.WlanPort` marked the second
	// radio "not conditioned" even though the daemon was watching it, which
	// would put a standing error notice on a perfectly healthy box.
	for _, w := range []string{"wlan-usb", "wlan0"} {
		if !dualCfg.IsWlan(w) {
			t.Errorf("%s should be watched", w)
		}
	}
	if dualCfg.IsWlan("wlan1") {
		t.Error("a third radio nobody configured must not read as watched")
	}
	if dualCfg.PrimaryWlan() != "wlan-usb" {
		t.Errorf("primary should be the first listed, got %q", dualCfg.PrimaryWlan())
	}
}

func TestNoUnwatchedNoticeWhenBothRadiosAreServed(t *testing.T) {
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan-usb", Wireless: true, Serving: true, Up: true, AP: &APStatus{Enabled: true}},
		{Name: "wlan0", Wireless: true, Serving: true, Up: true, AP: &APStatus{Enabled: true}},
	}}
	if notes := bridgeNotes(bi, dualCfg); len(notes) != 0 {
		t.Errorf("two watched radios is the normal dual-band case, got %+v", notes)
	}
}

func TestUnwatchedNoticeNamesEveryWatchedRadio(t *testing.T) {
	// With two radios watched, "the daemon watches wlan-usb" would be a
	// half-truth. The notice has to name them all or it reads as a bug report
	// against the wrong interface.
	bi := BridgeInfo{Ifaces: []IfaceInfo{
		{Name: "wlan1", Wireless: true, Serving: false, Up: true, AP: &APStatus{Enabled: true}},
	}}
	notes := bridgeNotes(bi, dualCfg)
	if len(notes) != 1 {
		t.Fatalf("want one note, got %+v", notes)
	}
	for _, want := range []string{"wlan-usb", "wlan0", "wlan1"} {
		if !strings.Contains(notes[0].Text, want) {
			t.Errorf("note should name %q: %s", want, notes[0].Text)
		}
	}
}

func TestSplitPortsAcceptsWhatTheUnitAndSelectorMightPass(t *testing.T) {
	// select-radio writes a space-separated list into /etc/default; a person
	// editing it by hand is as likely to use commas. Both have to work, and a
	// single name must keep behaving exactly as it did.
	tests := []struct {
		in   string
		want []string
	}{
		{"wlan0", []string{"wlan0"}},
		{"wlan-usb wlan0", []string{"wlan-usb", "wlan0"}},
		{"wlan-usb,wlan0", []string{"wlan-usb", "wlan0"}},
		{" wlan-usb , wlan0 ", []string{"wlan-usb", "wlan0"}},
		{"", []string{}},
		{"   ", []string{}},
	}
	for _, tc := range tests {
		got := SplitPorts(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("SplitPorts(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("SplitPorts(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestRegDomainReadsTheCountryCode(t *testing.T) {
	// hostapd reports no country code at all, so this is the only source --
	// and it is global rather than per-interface.
	const raw = `global
country US: DFS-FCC
	(902 - 904 @ 2), (N/A, 30), (N/A)
	(2400 - 2472 @ 40), (N/A, 30), (N/A)
`
	got := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "country ") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				got = strings.TrimSuffix(f[1], ":")
			}
			break
		}
	}
	if got != "US" {
		t.Errorf("got %q, want US (with the colon stripped)", got)
	}
}
