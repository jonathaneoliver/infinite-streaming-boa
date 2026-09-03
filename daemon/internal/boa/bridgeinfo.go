package boa

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The box's own interfaces: what it has, what they are called, what addresses
// they carry, and for a radio, what the access point is actually doing.
//
// Source J and Source K in docs/DATA-CONTRACT.md. Read the semantics there
// before changing anything here -- three of the fields do not mean what their
// names suggest, and each one produces a plausible-looking wrong answer.
//
// Radios are DISCOVERED rather than read from cfg.WlanPort. The daemon watches
// exactly one radio, so a second adapter is invisible to conditioning and its
// clients never appear in the device list. Enumerating the hardware instead of
// the configuration is what lets the interface SAY that, rather than leaving an
// operator to notice that half their clients are missing.

// Roles an interface can have in the bridge. Presentation leans on these, so
// they are a closed set rather than free text.
const (
	RoleWAN    = "wan"    // the port cabled to the existing network
	RoleBridge = "bridge" // br-lan itself
	RoleAP     = "ap"     // a radio hostapd is serving
	RoleRadio  = "radio"  // a wireless interface that is not serving the AP
	RoleLAN    = "lan"    // the downstream USB ethernet port
	RoleOther  = "other"
)

// APStatus is what a hostapd-served radio is doing right now.
//
// Nothing here is known from configuration: SSID, channel, band and width are
// baked into /etc/hostapd/boa.conf at IMAGE BUILD time from .env, so the only
// way to learn them on a running box is to ask hostapd. See Source K.
type APStatus struct {
	SSID  string `json:"ssid,omitempty"`
	BSSID string `json:"bssid,omitempty"`
	// Country is the GLOBAL regulatory domain from `iw reg get`. hostapd
	// reports no country code at all despite being configured with one, so
	// attributing this to a particular radio is a display choice rather than
	// something the radio told us.
	Country string `json:"country,omitempty"`
	Channel int    `json:"channel,omitempty"`
	FreqMHz int    `json:"freq_mhz,omitempty"`
	// WidthMHz is DERIVED -- hostapd has no width field. See apWidth.
	WidthMHz int `json:"width_mhz,omitempty"`
	// Mode is the highest enabled generation, e.g. "802.11ax".
	Mode string `json:"mode,omitempty"`
	// Enabled is true only when hostapd reports state=ENABLED, i.e. actually
	// beaconing rather than merely configured.
	Enabled  bool `json:"enabled"`
	Stations int  `json:"stations"`
	// BeaconIntMs and DTIMPeriod are the power-save timing knobs. Shown
	// because a phone's downlink behaviour between segment fetches is governed
	// by them and by nothing else visible in this interface.
	BeaconIntMs int `json:"beacon_int_ms,omitempty"`
	DTIMPeriod  int `json:"dtim_period,omitempty"`
}

// IfaceInfo is one interface as the bridge view draws it.
type IfaceInfo struct {
	Name string   `json:"name"`
	Role string   `json:"role"`
	MAC  string   `json:"mac,omitempty"`
	IPv4 []string `json:"ipv4,omitempty"`
	IPv6 []string `json:"ipv6,omitempty"`
	// Up is operstate == "up".
	Up bool `json:"up"`
	// Carrier is a THREE-state field flattened deliberately: sysfs `carrier`
	// returns EINVAL on a down interface, so "no carrier" and "could not ask"
	// are different facts. CarrierKnown says which this is; without it a
	// perfectly healthy interface that happens to be down reports as a dead
	// link. See Source J.
	Carrier      bool `json:"carrier"`
	CarrierKnown bool `json:"carrier_known"`
	// SpeedMbps is meaningful only for a WIRED port. A bridge reports a speed
	// too (br-lan reads 1000), which describes nothing when the bridge spans
	// an 80MHz radio, so it is only populated for a real ethernet port.
	SpeedMbps int `json:"speed_mbps,omitempty"`
	// Master is the bridge this interface is a port of, from the sysfs
	// symlink. Cheaper than `bridge link show` and, unlike it, still answers
	// for an interface that is down.
	Master   string     `json:"master,omitempty"`
	Wireless bool       `json:"wireless"`
	Radio    *RadioInfo `json:"radio,omitempty"`
	AP       *APStatus  `json:"ap,omitempty"`
	// Serving marks the one radio the daemon watches. Clients on any other
	// radio are not conditioned and do not appear in the device list.
	Serving bool `json:"serving"`
}

// BridgeInfo is the whole answer for the bridge view.
type BridgeInfo struct {
	Bridge string      `json:"bridge"`
	Ifaces []IfaceInfo `json:"ifaces"`
	// Notes are stated limitations, not errors -- chiefly "this radio's
	// clients are not conditioned". They exist because the alternative is an
	// operator discovering it from an empty device list.
	Notes []Notice `json:"notes,omitempty"`
}

// BridgeState assembles the inventory. Best-effort by design: a box with no
// radio, no hostapd or no USB ethernet is a normal box, and every absent piece
// simply yields a smaller list rather than an error.
func (e *Engine) BridgeState() BridgeInfo {
	if e.cfg.Demo {
		return demoBridgeState(e.cfg)
	}
	bi := BridgeInfo{Bridge: e.cfg.Bridge}
	addrs := ipAddrs()
	country := regDomain()

	for _, name := range netInterfaces() {
		if name == "lo" {
			continue // not a bridge port, and nothing to draw
		}
		in := readIface(name)
		in.Role = ifaceRole(name, in, e.cfg)
		// A bridge reports a speed -- br-lan reads 1000 -- and it describes
		// nothing: the bridge spans an 80MHz radio and a gigabit port at once,
		// so the number is neither of them. Dropped rather than displayed with
		// a caveat, because a figure on screen gets believed.
		if in.Role == RoleBridge {
			in.SpeedMbps = 0
		}
		in.IPv4, in.IPv6 = addrs[name].v4, addrs[name].v6
		if in.Wireless {
			r := Radio(name)
			in.Radio = &r
			in.Serving = name == e.cfg.WlanPort
			if hostapdAvailable(name) {
				if ap := apStatus(name, country); ap != nil {
					in.AP = ap
					in.Role = RoleAP
				}
			}
		}
		bi.Ifaces = append(bi.Ifaces, in)
	}
	sort.SliceStable(bi.Ifaces, func(i, j int) bool {
		return roleOrder(bi.Ifaces[i].Role) < roleOrder(bi.Ifaces[j].Role)
	})
	bi.Notes = bridgeNotes(bi, e.cfg)
	return bi
}

// bridgeNotes states the things that are true and would otherwise be found out
// the hard way. A radio the daemon does not watch is the important one: its
// clients associate, get addresses, pass traffic, and appear nowhere.
func bridgeNotes(bi BridgeInfo, cfg Config) []Notice {
	var out []Notice
	for _, in := range bi.Ifaces {
		if !in.Wireless || in.Serving {
			continue
		}
		if in.AP != nil && in.AP.Enabled {
			out = append(out, Notice{"error", fmt.Sprintf(
				"%s is serving an access point but the daemon watches %s. "+
					"Clients on %s are NOT conditioned and do not appear in the device list.",
				in.Name, cfg.WlanPort, in.Name)})
			continue
		}
		if in.Up {
			out = append(out, Notice{"info", fmt.Sprintf(
				"%s is up but not serving the access point.", in.Name)})
		}
	}
	return out
}

func roleOrder(role string) int {
	switch role {
	case RoleWAN:
		return 0
	case RoleBridge:
		return 1
	case RoleAP:
		return 2
	case RoleRadio:
		return 3
	case RoleLAN:
		return 4
	}
	return 5
}

func ifaceRole(name string, in IfaceInfo, cfg Config) string {
	switch name {
	case cfg.Bridge:
		return RoleBridge
	case cfg.WANPort:
		return RoleWAN
	case cfg.LanPort:
		return RoleLAN
	}
	if in.Wireless {
		return RoleRadio
	}
	return RoleOther
}

// netInterfaces lists interface names from sysfs. Sorted, so the output does
// not reorder itself between polls and make the view flicker.
func netInterfaces() []string {
	ents, err := filepath.Glob("/sys/class/net/*")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, filepath.Base(e))
	}
	sort.Strings(out)
	return out
}

// readIface reads one interface's sysfs facts. See Source J for why carrier and
// speed are handled the way they are.
func readIface(name string) IfaceInfo {
	base := filepath.Join("/sys/class/net", name)
	in := IfaceInfo{
		Name: name,
		MAC:  strings.TrimSpace(readSysfs(filepath.Join(base, "address"))),
		Up:   strings.TrimSpace(readSysfs(filepath.Join(base, "operstate"))) == "up",
	}
	// phy80211 is the test for "is this a radio". Present iff wireless.
	if _, err := filepath.EvalSymlinks(filepath.Join(base, "phy80211")); err == nil {
		in.Wireless = true
	}
	// carrier returns EINVAL while the interface is down, so an empty read is
	// "could not ask" rather than "no link". Conflating the two reports a
	// healthy-but-down interface as a dead cable.
	if c := strings.TrimSpace(readSysfs(filepath.Join(base, "carrier"))); c != "" {
		in.CarrierKnown = true
		in.Carrier = c == "1"
	}
	// Speed only for a wired port: a bridge reports one (br-lan reads 1000)
	// and it describes nothing, while a radio has no speed file at all.
	if !in.Wireless {
		if n, err := strconv.Atoi(strings.TrimSpace(readSysfs(filepath.Join(base, "speed")))); err == nil && n > 0 {
			in.SpeedMbps = n
		}
	}
	if m, err := filepath.EvalSymlinks(filepath.Join(base, "master")); err == nil {
		in.Master = filepath.Base(m)
	}
	return in
}

type ifAddrs struct{ v4, v6 []string }

type ipAddrJSON struct {
	IFName   string `json:"ifname"`
	AddrInfo []struct {
		Family string `json:"family"`
		Local  string `json:"local"`
		Prefix int    `json:"prefixlen"`
		Scope  string `json:"scope"`
	} `json:"addr_info"`
}

// ipAddrs reads every interface's addresses in one call.
//
// Link-local IPv6 is KEPT. On a box whose DHCP has failed it is the only way
// back in -- the console banner prints it for exactly that reason -- so hiding
// it as noise would remove the address an operator most needs when things have
// gone wrong.
func ipAddrs() map[string]ifAddrs {
	raw, err := exec.Command("ip", "-j", "addr", "show").Output()
	if err != nil {
		return map[string]ifAddrs{}
	}
	return parseIPAddrs(raw)
}

// parseIPAddrs is split out so it can be tested against real `ip -j addr`
// output without a kernel to ask.
func parseIPAddrs(raw []byte) map[string]ifAddrs {
	out := map[string]ifAddrs{}
	var entries []ipAddrJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return out
	}
	for _, e := range entries {
		a := out[e.IFName]
		for _, ai := range e.AddrInfo {
			cidr := fmt.Sprintf("%s/%d", ai.Local, ai.Prefix)
			switch ai.Family {
			case "inet":
				a.v4 = append(a.v4, cidr)
			case "inet6":
				a.v6 = append(a.v6, cidr)
			}
		}
		out[e.IFName] = a
	}
	return out
}

// regDomain reads the GLOBAL regulatory domain. hostapd reports no country code
// even when configured with one, so this is the only source. It is not
// per-interface: one answer covers the box.
func regDomain() string {
	raw, err := exec.Command("iw", "reg", "get").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "country ") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				return strings.TrimSuffix(f[1], ":")
			}
		}
	}
	return ""
}

// apStatus asks hostapd what the AP is doing. Returns nil when the socket is
// there but the exchange fails, so a radio is never reported as an access point
// on the strength of a socket alone.
func apStatus(iface, country string) *APStatus {
	status, err := hostapdCmd(iface, "STATUS")
	if err != nil {
		return nil
	}
	kv := parseHostapdKV(status)
	if len(kv) == 0 {
		return nil
	}
	ap := &APStatus{
		Country:     country,
		Enabled:     kv["state"] == "ENABLED",
		Channel:     atoiSafe(kv["channel"]),
		FreqMHz:     atoiSafe(kv["freq"]),
		BeaconIntMs: atoiSafe(kv["beacon_int"]),
		DTIMPeriod:  atoiSafe(kv["dtim_period"]),
		WidthMHz:    apWidth(kv),
		Mode:        apMode(kv),
	}
	// ssid and bssid are INDEXED (ssid[0]) because hostapd can serve several
	// BSSes on one interface. STATUS carries them too; GET_CONFIG is asked
	// only when it has to be.
	ap.SSID, ap.BSSID = kv["ssid[0]"], kv["bssid[0]"]
	if ap.SSID == "" || ap.BSSID == "" {
		if cfg, err := hostapdCmd(iface, "GET_CONFIG"); err == nil {
			ckv := parseHostapdKV(cfg)
			if ap.SSID == "" {
				ap.SSID = firstNonEmpty(ckv["ssid[0]"], ckv["ssid"])
			}
			if ap.BSSID == "" {
				ap.BSSID = firstNonEmpty(ckv["bssid[0]"], ckv["bssid"])
			}
		}
	}
	ap.Stations = len(StationDump(iface))
	return ap
}

// apWidth derives the channel width, which hostapd does not report.
//
// The string "80" appears nowhere in STATUS. Measured on mt7921u at channel 40:
// secondary_channel=-1, he_oper_chwidth=1, which `iw dev wlan-usb info`
// independently calls "width: 80 MHz". Do not be tempted by
// he_oper_centr_freq_seg0_idx -- that is the CENTRE of the 80MHz block, not a
// channel the AP is on, and reading it as one is wrong by up to two channels.
func apWidth(kv map[string]string) int {
	if kv["vht_oper_chwidth"] == "1" || kv["he_oper_chwidth"] == "1" {
		return 80
	}
	if sec := kv["secondary_channel"]; sec != "" && sec != "0" {
		return 40
	}
	if kv["channel"] == "" {
		return 0
	}
	return 20
}

// apMode names the highest generation enabled, which is what bounds the AP's
// ceiling and therefore the top of any measured ladder.
func apMode(kv map[string]string) string {
	switch {
	case kv["ieee80211ax"] == "1":
		return "802.11ax"
	case kv["ieee80211ac"] == "1":
		return "802.11ac"
	case kv["ieee80211n"] == "1":
		return "802.11n"
	}
	return ""
}

// parseHostapdKV splits hostapd's control replies, which are plain key=value
// lines. Keys may carry a BSS index, e.g. "ssid[0]", and are kept verbatim.
func parseHostapdKV(reply string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(reply, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "FAIL") || strings.HasPrefix(line, "UNKNOWN") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
