package boa

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The effective port lists: what the box has NOW, not what it had when boad
// was executed.
//
// WHY THIS EXISTS. The OpenWrt init script used to work the port lists out
// itself and pass them on the command line:
//
//	[ -n "$wlan" ] || wlan="$(bridge_ports "$bridge" "$wan" wlan)"
//
// That is a snapshot taken at exec time, and a radio or a bridge port that
// appears afterwards was never noticed. Measured 2026-09-23 and 2026-09-24:
// a USB radio passed through to a running box left boad with an empty -wlan,
// so the interface showed an access point as NOT SERVING while clients were
// associated to it; and a wired port added to br-lan was simply absent from
// the rack. Both looked like boa being broken, and both were fixed by a
// restart -- which is a thing an operator has to know to do. See issue #366.
//
// So discovery moves into the daemon, where it can be repeated. An EXPLICIT
// flag still wins: naming ports pins them, which is what a test bench wants
// when a port is deliberately left out.

// portScanInterval is how stale an answer may be. A hotplug is a human-speed
// event and reading a directory costs microseconds, so this is about not
// hammering sysfs on a 1 Hz tick that asks several times per pass, rather than
// about the cost of being wrong.
const portScanInterval = 2 * time.Second

// sysfsNet is /sys/class/net, or a tree standing in for it. Overridable ONLY
// so the discovery can be tested against a bridge that gains a port halfway
// through -- the failure this exists to prevent, and one that is otherwise
// reproducible only by plugging a radio into a running box.
func sysfsNet() string {
	if root := os.Getenv("BOA_SYSFS_ROOT"); root != "" {
		return filepath.Join(root, "sys", "class", "net")
	}
	return "/sys/class/net"
}

type portCache struct {
	mu   sync.Mutex
	at   time.Time
	wlan []string
	lan  []string
}

// WlanPorts is every Wi-Fi port on the bridge, or exactly those named with
// -wlan when that was given.
func (e *Engine) WlanPorts() []string {
	if len(e.cfg.WlanPorts) > 0 {
		return e.cfg.WlanPorts
	}
	w, _ := e.discoveredPorts()
	return w
}

// LanPorts is every wired bridge port that is not the uplink, or exactly those
// named with -lan when that was given.
func (e *Engine) LanPorts() []string {
	if len(e.cfg.LanPorts) > 0 {
		return e.cfg.LanPorts
	}
	_, l := e.discoveredPorts()
	return l
}

func (e *Engine) discoveredPorts() (wlan, lan []string) {
	e.ports.mu.Lock()
	defer e.ports.mu.Unlock()
	if time.Since(e.ports.at) < portScanInterval {
		return e.ports.wlan, e.ports.lan
	}
	wlan, lan = scanBridgePorts(e.cfg.Bridge, e.cfg.WANPort, e.cfg.ScanPorts)
	e.ports.at, e.ports.wlan, e.ports.lan = time.Now(), wlan, lan
	return wlan, lan
}

// scanBridgePorts splits a bridge's members into radios and wired client
// ports, the same way the init script did and for the same reasons.
//
// The uplink is excluded from both: it is where the rest of the network is,
// not a port clients sit behind. A listen-only radio is excluded too -- it
// carries no clients and has no hostapd config, so treating it as an access
// point would have boa ask hostapd about an interface hostapd has never heard
// of, once per tick.
func scanBridgePorts(bridge, wan string, scan []string) (wlan, lan []string) {
	net := sysfsNet()
	entries, err := os.ReadDir(filepath.Join(net, bridge, "brif"))
	if err != nil {
		// No bridge, or not Linux. An empty answer is right: there is nothing
		// behind a bridge that does not exist, and every caller already copes
		// with a box that has no radios.
		return nil, nil
	}
	isScan := make(map[string]bool, len(scan))
	for _, s := range scan {
		isScan[s] = true
	}
	for _, ent := range entries {
		name := ent.Name()
		if name == wan || isScan[name] {
			continue
		}
		// phy80211 is present iff the interface is wireless -- the same test
		// bridgeinfo.go uses to decide what a port is.
		if _, err := os.Stat(filepath.Join(net, name, "phy80211")); err == nil {
			wlan = append(wlan, name)
		} else {
			lan = append(lan, name)
		}
	}
	sort.Strings(wlan)
	sort.Strings(lan)
	return wlan, lan
}

// effectiveConfig is the running config with the port lists as they are NOW.
//
// For the readers that take a whole Config -- the role classifier and the
// notice builder -- rather than a list. Without it they answer from argv, so a
// radio added after boad started is classified as a bare wireless interface
// and the interface reports it as serving an access point the daemon is not
// watching, which is true at the moment it is printed and wrong a tick later.
func (e *Engine) effectiveConfig() Config {
	c := e.cfg
	c.WlanPorts = e.WlanPorts()
	c.LanPorts = e.LanPorts()
	return c
}

// isWlanNow is Config.IsWlan against the effective list.
//
// The difference decides whether a client is treated as wireless at all: its
// medium, whether an absence from the station table means anything, and
// whether it is shapeable on a radio. Against argv, every client on a
// discovered radio is reported as wired.
func (e *Engine) isWlanNow(name string) bool {
	for _, p := range e.WlanPorts() {
		if p == name {
			return true
		}
	}
	return false
}
