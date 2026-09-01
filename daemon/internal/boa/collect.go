package boa

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Collectors read the system's view of who is connected. See
// docs/DATA-CONTRACT.md for the semantics of each field; the short version is
// that PRESENCE comes from the radio and the bridge, while ADDRESSES come from
// ARP. Neither source alone is sufficient, and neither is authoritative for the
// other's question.

// StationDump parses `iw dev <iface> station dump` -- the authority on which
// wireless clients are actually associated.
//
// Counter direction is from the ACCESS POINT's point of view: the AP's tx is
// the client's download. Inverting this would flip every graph in the UI.
func StationDump(iface string) map[string]*Station {
	out := map[string]*Station{}
	raw, err := exec.Command("iw", "dev", iface, "station", "dump").Output()
	if err != nil {
		return out
	}
	var cur *Station
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "Station ") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			cur = &Station{MAC: normMAC(f[1])}
			out[cur.MAC] = cur
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		f := strings.Fields(strings.TrimSpace(val))
		if len(f) == 0 {
			continue
		}
		switch key {
		case "signal":
			cur.SignalDBm = atoiSafe(f[0])
		case "tx bytes":
			cur.TxBytes = atou64(f[0])
		case "rx bytes":
			cur.RxBytes = atou64(f[0])
		case "tx failed":
			cur.TxFailed = atou64(f[0])
		case "tx bitrate":
			cur.TxPhyMbps = atofSafe(f[0])
		case "rx bitrate":
			cur.RxPhyMbps = atofSafe(f[0])
		case "connected time":
			cur.ConnectedSec = atoiSafe(f[0])
		case "inactive time":
			cur.InactiveMs = atoiSafe(f[0])
		}
	}
	return out
}

// BridgePort is where the bridge last saw a MAC. In a transparent bridge this
// is the only way to know a WIRED client exists at all -- there is no lease and
// no association event, just frames arriving on a port.
type BridgePort struct {
	MAC    string
	Port   string
	Medium string // "wifi" or "wired", derived from which port it is
}

// BridgeFDB reads the forwarding database: the MAC-to-port table every switch
// maintains. Entries marked permanent or self are the bridge's own addresses
// rather than clients, and are skipped.
func BridgeFDB(bridge, wanPort, wlanPort string) map[string]BridgePort {
	out := map[string]BridgePort{}
	raw, err := exec.Command("bridge", "fdb", "show", "br", bridge).Output()
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		mac, port := normMAC(f[0]), ""
		for i := 1; i < len(f)-1; i++ {
			if f[i] == "dev" {
				port = f[i+1]
			}
		}
		// "self"/"permanent" mark the bridge's own MACs, not learned clients.
		if port == "" || port == wanPort ||
			strings.Contains(sc.Text(), "permanent") ||
			strings.Contains(sc.Text(), "self") {
			continue
		}
		medium := "wired"
		if port == wlanPort {
			medium = "wifi"
		}
		out[mac] = BridgePort{MAC: mac, Port: port, Medium: medium}
	}
	return out
}

type neighJSON struct {
	Dst    string   `json:"dst"`
	LLAddr string   `json:"lladdr"`
	State  []string `json:"state"`
}

// NeighTable supplements ARP snooping with whatever the kernel already knows.
// FAILED entries are dropped: they mean the address did not answer, so binding
// a shaping filter to it would condition traffic that goes nowhere.
func NeighTable(iface string) map[string]string {
	raw, err := exec.Command("ip", "-j", "neigh", "show", "dev", iface).Output()
	if err != nil {
		return map[string]string{}
	}
	var entries []neighJSON
	if json.Unmarshal(raw, &entries) != nil {
		return map[string]string{}
	}
	return neighFromEntries(entries)
}

// neighFromEntries is the parse, split from the exec so the family rule can be
// tested without a kernel.
//
// IPv4 ONLY, and the reason is not tidiness. `ip neigh` returns both families,
// the result is keyed by MAC, and a MAC commonly holds three entries at once --
// its v4 address, a routable v6 address, and an IPv6 link-local. Without a
// family check the last one parsed wins, so an fe80:: address would silently
// displace a perfectly good v4 address for the same device.
//
// The single caller assigns this to Client.IP, which is the IPv4 field and
// reaches tc as `protocol ip ... match ip dst`. Handing that an IPv6 address is
// not merely wasteful, it is rejected outright with `Illegal "match"` -- and
// because writeFilters installs the v4 filter FIRST and returns on error, the
// v6 filters below it never get installed either. The device then carries no
// conditioning at all while the interface reports its policy as applied, which
// is the exact failure shape.go:632 exists to prevent.
//
// Routable v6 addresses are dropped rather than merged into Client.IPv6: the
// ARP sniffer already supplies those, and widening this function's contract is
// a separate change.
func neighFromEntries(entries []neighJSON) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		if e.LLAddr == "" || e.Dst == "" {
			continue
		}
		if ip := net.ParseIP(e.Dst); ip == nil || ip.To4() == nil {
			continue
		}
		failed := false
		for _, st := range e.State {
			if st == "FAILED" || st == "INCOMPLETE" {
				failed = true
			}
		}
		if !failed {
			out[normMAC(e.LLAddr)] = e.Dst
		}
	}
	return out
}

// DefaultRouteIface reports which interface carries the default route. In
// bridge mode this is the bridge itself rather than a physical port, which is
// precisely why the WAN port must be configured explicitly rather than guessed.
func DefaultRouteIface() string {
	raw, err := exec.Command("ip", "-j", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	var routes []struct {
		Dev string `json:"dev"`
	}
	if json.Unmarshal(raw, &routes) != nil || len(routes) == 0 {
		return ""
	}
	return routes[0].Dev
}

// LinkExists reports whether an interface is present, used for capability
// reporting so the UI can explain a missing USB adapter instead of silently
// showing nothing.
func LinkExists(name string) bool {
	return exec.Command("ip", "link", "show", name).Run() == nil
}

// PortListening reports whether anything holds a listening TCP socket on a
// local port.
//
// Deliberately NOT a dial. Connecting to an iperf3 server starts a control
// session it then waits on, so a liveness probe every few seconds would leave
// a trail of "the client has unexpectedly closed the connection" in the
// journal -- the health check inventing the noise it exists to detect. The
// kernel already publishes the answer.
//
// Reading /proc means this is Linux-only in practice; elsewhere the file is
// absent and the answer is a plain false, which is correct for a box that is
// not the appliance.
func PortListening(port int) bool {
	// 0A is TCP_LISTEN. The local address column is HEX_ADDR:HEX_PORT.
	want := fmt.Sprintf(":%04X", port)
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(string(raw)))
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			if strings.HasSuffix(fields[1], want) {
				return true
			}
		}
	}
	return false
}

func normMAC(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(s, ","))
	return n
}

func atou64(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSuffix(s, ","), 10, 64)
	return n
}

func atofSafe(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s, ","), 64)
	return f
}

// WANSideMACs returns the MACs the bridge has learned on the WAN port, i.e.
// everything living upstream of boa. They are excluded from the client list:
// they generate ARP like anything else, but they are not devices this box is
// conditioning, and listing them would bury the real clients.
func WANSideMACs(bridge, wanPort string) map[string]bool {
	out := map[string]bool{}
	raw, err := exec.Command("bridge", "fdb", "show", "br", bridge).Output()
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		if len(f) < 3 || strings.Contains(line, "permanent") || strings.Contains(line, "self") {
			continue
		}
		for i := 1; i < len(f)-1; i++ {
			if f[i] == "dev" && f[i+1] == wanPort {
				out[normMAC(f[0])] = true
			}
		}
	}
	return out
}

// RadioInfo describes the interface serving the access point: whether it is
// the onboard chip or a plugged-in USB adapter, and for USB, which speed the
// link actually negotiated.
//
// The speed is here because getting it wrong is invisible and expensive. A USB
// 3.0 Wi-Fi adapter that is not fully seated, or is on a cable without
// SuperSpeed pins, enumerates as High-Speed and then behaves like a working
// adapter in every respect that is normally checked -- same 80MHz channel, same
// 802.11ax, same PHY rate over 1 Gbit/s -- while delivering about a sixth of
// the throughput. Measured here: 717 Mbit/s on USB 3.0 against 117 on USB 2.0,
// same adapter, same radio settings, no error logged anywhere.
type RadioInfo struct {
	Iface  string `json:"iface"`
	Driver string `json:"driver,omitempty"`
	// Bus is "usb" or "onboard". Onboard is not a judgement: the Pi 5's chip is
	// simply not on the USB bus, so none of the fields below apply to it.
	Bus string `json:"bus"`
	// Product and Vendor are the USB descriptor strings, so the interface can
	// name the adapter rather than making the operator run lsusb.
	Product string `json:"product,omitempty"`
	Vendor  string `json:"vendor,omitempty"`
	// LinkMbps is the NEGOTIATED speed, from sysfs `speed`: 5000 for
	// SuperSpeed, 480 for High-Speed. Not the advertised capability -- that is
	// the whole point, since the two disagree exactly when it matters.
	LinkMbps int `json:"link_mbps,omitempty"`
	// USBVersion is bcdUSB as the device declares it, e.g. "3.20" or "2.10".
	USBVersion string `json:"usb_version,omitempty"`
}

// Radio inspects the interface serving the AP. Everything comes from sysfs
// rather than lsusb, which is not installed on a minimal image.
func Radio(iface string) RadioInfo {
	info := RadioInfo{Iface: iface, Bus: "onboard"}
	if iface == "" {
		return info
	}
	base := filepath.Join("/sys/class/net", iface, "device")
	if drv, err := os.Readlink(filepath.Join(base, "driver")); err == nil {
		info.Driver = filepath.Base(drv)
	}
	// The USB device is the PARENT of the interface's device node: the device
	// link points at the USB *interface* (2-1:1.0), and speed, version and the
	// descriptor strings all live one level up on the device itself (2-1).
	//
	// EvalSymlinks first, and that is the whole subtlety. `device` is a symlink
	// into /sys/devices, and filepath.Join(base, "..") cleans the path
	// LEXICALLY -- it yields /sys/class/net/<iface> rather than the USB device,
	// so every read below quietly returns empty and a USB adapter reports as
	// onboard. Resolve, then take the parent.
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return info
	}
	parent := filepath.Dir(realBase)
	speed := strings.TrimSpace(readSysfs(filepath.Join(parent, "speed")))
	if speed == "" {
		// No speed file means not a USB device. The onboard radio hangs off
		// SDIO, so this is the normal case, not a failure to read.
		return info
	}
	info.Bus = "usb"
	if n, err := strconv.Atoi(speed); err == nil {
		info.LinkMbps = n
	}
	info.USBVersion = strings.TrimSpace(readSysfs(filepath.Join(parent, "version")))
	info.Product = strings.TrimSpace(readSysfs(filepath.Join(parent, "product")))
	info.Vendor = strings.TrimSpace(readSysfs(filepath.Join(parent, "manufacturer")))
	return info
}

// SuperSpeed reports whether a USB radio negotiated USB 3 rates. False for an
// onboard radio, which is not slow -- it is simply not on the bus.
func (r RadioInfo) SuperSpeed() bool { return r.Bus == "usb" && r.LinkMbps >= 5000 }

func readSysfs(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
