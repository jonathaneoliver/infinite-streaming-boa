package boa

import (
	"strconv"
	"strings"
)

/*
 * USB faults, read from the kernel rather than inferred from what hostapd says.
 *
 * MEASURED 2026-09-08 on the box. A wlan dongle stopped transmitting and every
 * instrument the daemon already had reported it healthy:
 *
 *	hostapd_cli status   state=ENABLED, right SSID, right channel
 *	bridge link show     master br-lan state forwarding
 *	ip -br link          UP LOWER_UP
 *	boactl radio         healthy
 *
 * Clients were steered onto it and vanished, because there was nothing on the
 * air to land on: a scan run from the box's OTHER dongle, ten centimetres away,
 * found eleven networks including a neighbour on the same 5745 MHz and did not
 * find this one at all.
 *
 * The kernel had said so 182 times and nobody was listening:
 *
 *	3,1163,83278214199,-;mt7921u 4-1.3:1.0: tx urb failed: -71
 *	 SUBSYSTEM=usb
 *	 DEVICE=+usb:4-1.3:1.0
 *
 * That is the whole reason this file exists. The existing wedge detection
 * (#182, radiowedge_test.go) keys on hostapd CONTRADICTING itself, which is a
 * real signal for the rfkill wedge and no signal at all for this one -- here
 * hostapd was consistent, confident and wrong, because the fault was below it.
 *
 * Priority 3 is KERN_ERR. SUBSYSTEM and DEVICE are the kernel's own structured
 * continuation lines, so the device is named by the kernel rather than scraped
 * out of the message text, and "+usb:4-1.3:1.0" maps straight onto the sysfs
 * path that /sys/class/net/<iface>/device resolves to.
 */

// kmsgRecord is one record read from /dev/kmsg.
//
// The format is "<prio>,<seq>,<monotonic-usec>,<flags>;<message>" followed by
// zero or more continuation lines, each beginning with a space, carrying
// KEY=value. Documented in Documentation/ABI/testing/dev-kmsg.
type kmsgRecord struct {
	Priority  int
	Seq       uint64
	Monotonic int64 // microseconds since boot
	Message   string
	Subsystem string // from " SUBSYSTEM=..."
	Device    string // from " DEVICE=..."
}

// parseKmsgRecord reads one record. It returns false for anything it cannot
// make sense of rather than guessing, because a misparsed record would name the
// wrong device and send a recovery at a radio that was working.
func parseKmsgRecord(rec string) (kmsgRecord, bool) {
	semi := strings.IndexByte(rec, ';')
	if semi < 0 {
		return kmsgRecord{}, false
	}
	head := strings.Split(rec[:semi], ",")
	if len(head) < 3 {
		return kmsgRecord{}, false
	}
	prio, err := strconv.Atoi(head[0])
	if err != nil {
		return kmsgRecord{}, false
	}
	seq, err := strconv.ParseUint(head[1], 10, 64)
	if err != nil {
		return kmsgRecord{}, false
	}
	mono, err := strconv.ParseInt(head[2], 10, 64)
	if err != nil {
		return kmsgRecord{}, false
	}

	out := kmsgRecord{Priority: prio, Seq: seq, Monotonic: mono}
	lines := strings.Split(rec[semi+1:], "\n")
	out.Message = lines[0]
	for _, l := range lines[1:] {
		// Continuation lines begin with a space. Anything else is not ours.
		if !strings.HasPrefix(l, " ") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(l, " "), "=")
		if !ok {
			continue
		}
		switch k {
		case "SUBSYSTEM":
			out.Subsystem = v
		case "DEVICE":
			out.Device = v
		}
	}
	return out, true
}

// usbDevicePath pulls the bus path out of a DEVICE= tag, e.g. "+usb:4-1.3:1.0"
// -> "4-1.3". The interface suffix is dropped because /sys/class/net links to
// the interface (".../4-1.3/4-1.3:1.0") while the thing that can be reset is
// the device, and callers want to compare and act on the same string.
//
// Char- and block-device tags ("c189:385") name a devnum that has already been
// reused by the time a fault is read, so they are deliberately NOT accepted:
// resolving one would point at whatever enumerated next.
func usbDevicePath(tag string) string {
	rest, ok := strings.CutPrefix(tag, "+usb:")
	if !ok {
		return ""
	}
	// "4-1.3:1.0" -> "4-1.3". A device path never contains a colon; the
	// interface number always follows one.
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// ifaceForUSBDeviceIn names the interface a USB device backs, given each
// interface's resolved device directory.
//
// MEASURED on the box, and the two shapes are why this matches whole path
// COMPONENTS rather than substrings:
//
//	direct to the Pi     .../xhci-hcd.0/usb2/2-1/2-1:1.0
//	behind the hub       .../xhci-hcd.1/usb4/4-1/4-1.3/4-1.3:1.0
//
// "4-1" is a prefix of "4-1.3", so a substring match would blame a dongle for
// every fault on the hub above it -- and a dongle moved from a hub port to a
// root port changes its path completely, which is exactly the case that must
// keep working.
//
// A fault on the hub itself genuinely belongs to every adapter hanging off it.
// That is not an attribution this can make, so ambiguity returns "" and the
// caller names the bus path instead of guessing.
func ifaceForUSBDeviceIn(links map[string]string, device string) string {
	// A bus path never contains a colon; an INTERFACE directory ("4-1.3:1.0")
	// always does. usbDevicePath already strips the interface number, so a
	// colon here means the caller passed something that is not a device, and
	// matching it would be matching a different kind of thing by accident.
	if device == "" || strings.Contains(device, ":") {
		return ""
	}
	var found string
	for iface, path := range links {
		// Only interfaces that actually hang off a USB root hub can be blamed
		// for a USB fault. Without this the match is plain path-component
		// matching, and the onboard radio -- whose sysfs path contains
		// "mmc1:0001" -- is only spared by luck rather than by rule.
		if !onUSBBus(path) || !hasPathComponent(path, device) {
			continue
		}
		if found != "" {
			return "" // more than one: the fault is on something they share
		}
		found = iface
	}
	return found
}

// onUSBBus reports whether a device path passes through a USB root hub, which
// every USB device's does: ".../xhci-hcd.0/usb2/2-1/..." on a root port and
// ".../xhci-hcd.1/usb4/4-1/4-1.3/..." behind a hub.
func onUSBBus(path string) bool {
	for _, c := range strings.Split(path, "/") {
		rest, ok := strings.CutPrefix(c, "usb")
		if !ok || rest == "" {
			continue
		}
		if strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return true
		}
	}
	return false
}

// hasPathComponent reports whether want is one whole component of path.
func hasPathComponent(path, want string) bool {
	for _, c := range strings.Split(path, "/") {
		if c == want {
			return true
		}
	}
	return false
}

// usbFault is a kernel-reported USB failure attributed to one device.
type usbFault struct {
	Device  string // bus path, e.g. "4-1.3"
	Message string
	// Terminal marks a fault after which the device is known not to recover on
	// its own. See knownTerminalFaults, and note that a device can also earn a
	// reset without one of these by simply going on failing -- see usbwatch.go.
	Terminal bool
}

// knownTerminalFaults are the messages after which the hardware stayed dead.
//
// MEASURED: "timed out waiting for pending tx" was logged at 19:33 and again at
// 20:04, and the radio was still off the air at 20:48 when it was reset by
// hand. The tx-urb burst that preceded the first one is a symptom of the same
// failure, but a handful of urb errors alone can be survived, so only the
// timeout is treated as proof the device is gone.
var knownTerminalFaults = []string{
	"timed out waiting for pending tx",
	"timed out waiting for pending rx",
}

// usbFaultOf decides whether a record is a USB fault worth reporting, and
// returns it attributed to a device.
//
// The filter is the kernel's own structured fields, not the message text:
// SUBSYSTEM=usb says the kernel filed it against USB, and priority <= KERN_ERR
// says the kernel considered it an error. That catches r8152 ethernet adapters
// and any future dongle for free, where a list of mt7921u message strings would
// have to be extended for each.
func usbFaultOf(rec kmsgRecord) (usbFault, bool) {
	if rec.Subsystem != "usb" || rec.Priority > 3 {
		return usbFault{}, false
	}
	dev := usbDevicePath(rec.Device)
	if dev == "" {
		return usbFault{}, false
	}
	f := usbFault{Device: dev, Message: strings.TrimSpace(rec.Message)}
	for _, t := range knownTerminalFaults {
		if strings.Contains(f.Message, t) {
			f.Terminal = true
			break
		}
	}
	return f, true
}
