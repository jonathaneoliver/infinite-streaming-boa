package boa

import (
	"strings"
	"testing"
)

var testCfg = Config{Bridge: "br-lan", WANPort: "eth0", WlanPort: "wlan-usb", LanPort: "lan0"}

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
