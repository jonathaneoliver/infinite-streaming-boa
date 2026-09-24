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
