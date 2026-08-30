package pifi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	out := map[string]string{}
	raw, err := exec.Command("ip", "-j", "neigh", "show", "dev", iface).Output()
	if err != nil {
		return out
	}
	var entries []neighJSON
	if json.Unmarshal(raw, &entries) != nil {
		return out
	}
	for _, e := range entries {
		if e.LLAddr == "" || e.Dst == "" {
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
// everything living upstream of pifi. They are excluded from the client list:
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
