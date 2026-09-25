package boa

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeBridge builds a sysfs-shaped tree: a bridge with the named ports, and a
// phy80211 entry for each one that should look wireless.
func fakeBridge(t *testing.T, bridge string, ports map[string]bool) string {
	t.Helper()
	root := t.TempDir()
	brif := filepath.Join(root, "sys", "class", "net", bridge, "brif")
	if err := os.MkdirAll(brif, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, wireless := range ports {
		if err := os.WriteFile(filepath.Join(brif, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "sys", "class", "net", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if wireless {
			if err := os.Mkdir(filepath.Join(dir, "phy80211"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func TestScanBridgePortsSplitsRadiosFromWiredAndExcludesTheUplink(t *testing.T) {
	root := fakeBridge(t, "br-lan", map[string]bool{
		"eth0":     false, // the uplink
		"eth2":     false, // a wired client port
		"phy0-ap0": true,  // a radio
		"scan0":    true,  // a listen-only radio
	})
	t.Setenv("BOA_SYSFS_ROOT", root)

	wlan, lan := scanBridgePorts("br-lan", "eth0", []string{"scan0"})

	if len(wlan) != 1 || wlan[0] != "phy0-ap0" {
		t.Errorf("wlan = %v, want [phy0-ap0]: the uplink and the scanner must both be left out", wlan)
	}
	if len(lan) != 1 || lan[0] != "eth2" {
		t.Errorf("lan = %v, want [eth2]", lan)
	}
}

// The failure this whole change exists for: a radio that appears AFTER boad
// started must be found, not missed until someone restarts the daemon.
func TestPortsAppearingLaterAreFound(t *testing.T) {
	root := fakeBridge(t, "br-lan", map[string]bool{"eth0": false})
	t.Setenv("BOA_SYSFS_ROOT", root)

	e := &Engine{cfg: Config{Bridge: "br-lan", WANPort: "eth0"}}
	if got := e.WlanPorts(); len(got) != 0 {
		t.Fatalf("WlanPorts() = %v, want none before the radio exists", got)
	}

	// The radio turns up, as a USB one does a few seconds into a boot.
	brif := filepath.Join(root, "sys", "class", "net", "br-lan", "brif")
	if err := os.WriteFile(filepath.Join(brif, "phy1-ap0"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sys", "class", "net", "phy1-ap0", "phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Still cached, deliberately: the cache is what keeps a 1 Hz tick off sysfs.
	if got := e.WlanPorts(); len(got) != 0 {
		t.Errorf("WlanPorts() = %v, want the cached answer within the interval", got)
	}
	e.ports.mu.Lock()
	e.ports.at = time.Now().Add(-2 * portScanInterval)
	e.ports.mu.Unlock()

	got := e.WlanPorts()
	if len(got) != 1 || got[0] != "phy1-ap0" {
		t.Errorf("WlanPorts() = %v, want [phy1-ap0] once the cache has expired", got)
	}
}

// An explicit -wlan pins the list: a bench that leaves a radio out means it.
func TestExplicitPortsWinOverDiscovery(t *testing.T) {
	root := fakeBridge(t, "br-lan", map[string]bool{
		"eth0":     false,
		"phy0-ap0": true,
		"phy1-ap0": true,
	})
	t.Setenv("BOA_SYSFS_ROOT", root)

	e := &Engine{cfg: Config{Bridge: "br-lan", WANPort: "eth0", WlanPorts: []string{"phy0-ap0"}}}
	got := e.WlanPorts()
	if len(got) != 1 || got[0] != "phy0-ap0" {
		t.Errorf("WlanPorts() = %v, want only the named port", got)
	}
}

// A box with no bridge must answer "nothing", not crash: that is every
// development machine running the daemon outside Linux.
func TestNoBridgeIsEmptyRatherThanAnError(t *testing.T) {
	t.Setenv("BOA_SYSFS_ROOT", t.TempDir())
	wlan, lan := scanBridgePorts("br-lan", "eth0", nil)
	if len(wlan) != 0 || len(lan) != 0 {
		t.Errorf("got %v/%v, want both empty", wlan, lan)
	}
}

// #381: the radio list has to reach EVERY consumer, not just the bridge view.
//
// The regression this guards: #376 moved discovery into the daemon and removed
// the init script's computation, so -wlan is empty on every OpenWrt device --
// but most call sites still read the argv snapshot. The bridge view was right
// while the capability block said the box had no radio, the client list had no
// Wi-Fi clients, and steer, deauth and transmit power all looped over nothing.
//
// Measured on a Cudy TR3000 with two radios serving and an iPhone associated:
// caps.radio false, caps.wlan_iface "", one client, and the phone absent.
func TestPrimaryWlanFollowsDiscoveryWhenArgvIsEmpty(t *testing.T) {
	root := fakeBridge(t, "br-lan", map[string]bool{
		"eth0":     false, // the uplink
		"eth1":     false, // a wired client port
		"phy0-ap0": true,
		"phy1-ap0": true,
	})
	t.Setenv("BOA_SYSFS_ROOT", root)

	// Exactly what the OpenWrt init script passes now: no -wlan, no -lan.
	e := &Engine{cfg: Config{Bridge: "br-lan", WANPort: "eth0"}}

	if got := e.primaryWlanNow(); got != "phy0-ap0" {
		t.Errorf("primaryWlanNow() = %q, want phy0-ap0 -- caps.radio is derived from this, and %q makes the interface report no radio at all", got, got)
	}
	if got := e.WlanPorts(); len(got) != 2 {
		t.Errorf("WlanPorts() = %v, want both radios: this is what the station dump and the fdb filter are built from", got)
	}
	if got := e.LanPorts(); len(got) != 1 || got[0] != "eth1" {
		t.Errorf("LanPorts() = %v, want [eth1]", got)
	}

	// An explicit list still pins, which is what a bench that deliberately
	// leaves a port out depends on.
	pinned := &Engine{cfg: Config{Bridge: "br-lan", WANPort: "eth0", WlanPorts: []string{"phy1-ap0"}}}
	if got := pinned.primaryWlanNow(); got != "phy1-ap0" {
		t.Errorf("primaryWlanNow() = %q with an explicit -wlan, want phy1-ap0", got)
	}
}

// #395: the pair counters are one more consumer, and the one #381 missed.
//
// portPairs returns nil below two ports, and Pairs is `omitempty`, so an
// argv-derived list on OpenWrt drops the key from the snapshot entirely and the
// interface hides the flow figure -- while nftables goes on counting into rules
// for a port set that stopped existing, because syncPairRules is only reached
// past that same return.
//
// Measured on the x86-64 guest 2026-09-25: no pairs at all with the flags
// empty, 12 pairs and 96 Mbps on eth1->phy4-ap0 the moment they were filled in,
// and rules still naming a port removed from the bridge an hour earlier.
func TestPairPortsFollowTheBridgeWhenArgvIsEmpty(t *testing.T) {
	root := fakeBridge(t, "br-lan", map[string]bool{
		"eth0":     false, // the uplink
		"eth1":     false, // a wired client port
		"phy0-ap0": true,
		"phy1-ap0": true,
		"scan0":    true, // listen-only: forwards nothing, must stay out
	})
	t.Setenv("BOA_SYSFS_ROOT", root)

	// Exactly what the OpenWrt init script passes: no -wlan, no -lan.
	e := &Engine{cfg: Config{Bridge: "br-lan", WANPort: "eth0", ScanPorts: []string{"scan0"}}}

	if got := e.cfg.bridgePorts(); len(got) >= 2 {
		t.Fatalf("cfg.bridgePorts() = %v -- this test means nothing unless argv alone gives fewer than two", got)
	}
	got := e.effectiveConfig().bridgePorts()
	if len(got) < 2 {
		t.Fatalf("effectiveConfig().bridgePorts() = %v, want the uplink and the bridge's ports: below two, portPairs returns nil and the flow figure disappears", got)
	}
	want := map[string]bool{"eth0": true, "eth1": true, "phy0-ap0": true, "phy1-ap0": true}
	if len(got) != len(want) {
		t.Errorf("bridgePorts() = %v, want exactly the %d ports a frame can be forwarded between", got, len(want))
	}
	for _, p := range got {
		if p == "scan0" {
			t.Errorf("bridgePorts() = %v, want no listen-only radio: it has no hostapd bridge= line, forwards nothing, and a pair naming it could only ever read zero", got)
		} else if !want[p] {
			t.Errorf("bridgePorts() = %v, unexpected port %q", got, p)
		}
	}
}
