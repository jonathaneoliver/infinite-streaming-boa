package boa

import "testing"

/*
 * Every record in this file was READ OFF THE BOX with a raw read of /dev/kmsg
 * on 2026-09-08, not composed to match the parser. That matters more than
 * usual here: the parser's whole job is to name a device the kernel blamed, and
 * a fixture invented to fit the code would agree with it about a format neither
 * had checked.
 */

// The fault that started this: the record that was there 182 times while every
// other instrument reported the radio healthy.
const realTxURBFailure = "3,1163,83278214199,-;mt7921u 4-1.3:1.0: tx urb failed: -71\n SUBSYSTEM=usb\n DEVICE=+usb:4-1.3:1.0\n"

// The one that proved the device was gone for good.
const realPendingTxTimeout = "3,1625,85156754845,-;mt7921u 4-1.3:1.0: timed out waiting for pending tx\n SUBSYSTEM=usb\n DEVICE=+usb:4-1.3:1.0\n"

// A disconnect: same subsystem, INFO priority, and a char-device tag rather
// than a bus path. Present in the same burst, and not a fault to act on.
const realDisconnect = "6,895,81731968260,-;usb 4-1: USB disconnect, device number 2\n SUBSYSTEM=usb\n DEVICE=c189:385\n"

func TestParsesTheRecordThatWasMissed(t *testing.T) {
	rec, ok := parseKmsgRecord(realTxURBFailure)
	if !ok {
		t.Fatal("the record the box actually logged did not parse")
	}
	if rec.Priority != 3 {
		t.Errorf("priority = %d, want 3 (KERN_ERR)", rec.Priority)
	}
	if rec.Seq != 1163 {
		t.Errorf("seq = %d, want 1163", rec.Seq)
	}
	if rec.Monotonic != 83278214199 {
		t.Errorf("monotonic = %d, want 83278214199", rec.Monotonic)
	}
	if rec.Subsystem != "usb" {
		t.Errorf("subsystem = %q, want usb", rec.Subsystem)
	}
	if rec.Device != "+usb:4-1.3:1.0" {
		t.Errorf("device = %q", rec.Device)
	}
	// The message must NOT keep the trailing continuation lines: it is shown to
	// an operator, and "tx urb failed: -71 SUBSYSTEM=usb" reads like the kernel
	// blamed the subsystem for the failure.
	if rec.Message != "mt7921u 4-1.3:1.0: tx urb failed: -71" {
		t.Errorf("message = %q", rec.Message)
	}
}

// The device tag is the whole point of using the structured field: it is the
// kernel's own attribution, and it maps onto a sysfs path we can reset.
func TestUSBDevicePathKeepsOnlyWhatSysfsUses(t *testing.T) {
	for _, c := range []struct{ tag, want string }{
		{"+usb:4-1.3:1.0", "4-1.3"},
		{"+usb:4-1", "4-1"},
		{"+usb:3-1.1:1.0", "3-1.1"},
		// A char-device tag names a devnum that is reused as soon as something
		// else enumerates. Resolving one would point at the wrong device.
		{"c189:385", ""},
		{"b8:0", ""},
		{"", ""},
	} {
		if got := usbDevicePath(c.tag); got != c.want {
			t.Errorf("usbDevicePath(%q) = %q, want %q", c.tag, got, c.want)
		}
	}
}

func TestFaultIsRaisedForTheErrorAndNotTheDisconnect(t *testing.T) {
	rec, _ := parseKmsgRecord(realTxURBFailure)
	f, ok := usbFaultOf(rec)
	if !ok {
		t.Fatal("the measured fault was not recognised as one")
	}
	if f.Device != "4-1.3" {
		t.Errorf("fault blamed %q, want 4-1.3", f.Device)
	}
	if f.Terminal {
		t.Error("a single tx urb error was called terminal; a burst of them was survived once")
	}

	// A disconnect is INFO, and its device tag cannot be resolved. Reporting it
	// as a fault would fire on every deliberate replug.
	rec, _ = parseKmsgRecord(realDisconnect)
	if _, ok := usbFaultOf(rec); ok {
		t.Error("a USB disconnect was reported as a fault")
	}
}

// The distinction that decides whether anything is done about it.
func TestPendingTxTimeoutIsTerminal(t *testing.T) {
	rec, ok := parseKmsgRecord(realPendingTxTimeout)
	if !ok {
		t.Fatal("record did not parse")
	}
	f, ok := usbFaultOf(rec)
	if !ok {
		t.Fatal("not recognised as a fault")
	}
	if !f.Terminal {
		t.Fatal("the fault after which the radio never came back was not terminal")
	}
	if f.Device != "4-1.3" {
		t.Errorf("fault blamed %q, want 4-1.3", f.Device)
	}
}

// The filter is the kernel's structured fields, so a driver this box has never
// seen is covered without adding its message strings.
func TestFaultFilterIsSubsystemAndPriorityNotMessageText(t *testing.T) {
	// An error on a different subsystem is not ours, however alarming.
	rec, _ := parseKmsgRecord("3,1,1,-;mmc0: card never left busy state\n SUBSYSTEM=mmc\n DEVICE=+mmc:mmc0\n")
	if _, ok := usbFaultOf(rec); ok {
		t.Error("an mmc error was reported as a USB fault")
	}

	// A USB error from a driver with no entry anywhere in this package still
	// counts: this is what makes the ethernet adapters covered for free.
	rec, _ = parseKmsgRecord("3,2,2,-;r8152-cfgselector 4-1.4:1.0: Tx status -71\n SUBSYSTEM=usb\n DEVICE=+usb:4-1.4:1.0\n")
	f, ok := usbFaultOf(rec)
	if !ok {
		t.Fatal("a USB error from an unlisted driver was ignored")
	}
	if f.Device != "4-1.4" {
		t.Errorf("fault blamed %q, want 4-1.4", f.Device)
	}

	// Warning priority is not an error. The kernel logs plenty at 4.
	rec, _ = parseKmsgRecord("4,3,3,-;usb 4-1.3: link reset\n SUBSYSTEM=usb\n DEVICE=+usb:4-1.3:1.0\n")
	if _, ok := usbFaultOf(rec); ok {
		t.Error("a KERN_WARNING was reported as a fault")
	}
}

// boxLinks is /sys/class/net as it actually read on 2026-09-08, AFTER a dongle
// was moved off the hub onto a root port. Both topologies are present at once,
// which is the whole point: the same code has to attribute a fault correctly
// whichever way a radio happens to be plugged in.
var boxLinks = map[string]string{
	// Moved to a port directly on the Pi.
	"wlan-usb-3ff2": "/sys/devices/platform/axi/1000120000.pcie/1f00200000.usb/xhci-hcd.0/usb2/2-1/2-1:1.0",
	// Still behind the 4-port hub.
	"wlan-usb-46c7": "/sys/devices/platform/axi/1000120000.pcie/1f00300000.usb/xhci-hcd.1/usb4/4-1/4-1.3/4-1.3:1.0",
	"lan-usb-6518":  "/sys/devices/platform/axi/1000120000.pcie/1f00300000.usb/xhci-hcd.1/usb4/4-1/4-1.1/4-1.1:1.0",
	"lan-usb-a684":  "/sys/devices/platform/axi/1000120000.pcie/1f00300000.usb/xhci-hcd.1/usb4/4-1/4-1.4/4-1.4:1.0",
	// The onboard radio is on mmc, not USB. It must never be blamed for a USB
	// fault, and it has no bus path that could be reset.
	"wlan0": "/sys/devices/platform/axi/1001100000.mmc/mmc_host/mmc1/mmc1:0001/mmc1:0001:1",
}

// ANY DEVICE, ANY PORT. A radio on a root port and a radio behind a hub must
// both be found, and moving one between the two must not need a code change.
func TestFaultsAreAttributedOnAnyPort(t *testing.T) {
	for _, c := range []struct{ device, want string }{
		{"2-1", "wlan-usb-3ff2"},   // straight into the Pi
		{"4-1.3", "wlan-usb-46c7"}, // behind the hub
		{"4-1.1", "lan-usb-6518"},  // an ethernet adapter, same code path
		{"4-1.4", "lan-usb-a684"},
		// A device backing nothing we know about.
		{"1-1", ""},
		{"", ""},
	} {
		if got := ifaceForUSBDeviceIn(boxLinks, c.device); got != c.want {
			t.Errorf("ifaceForUSBDeviceIn(%q) = %q, want %q", c.device, got, c.want)
		}
	}
}

// The hub backs three adapters at once. Blaming any single one of them would
// point the operator at a radio that is fine, so an ambiguous fault is reported
// against the bus path instead.
func TestAFaultOnTheHubIsNotBlamedOnOneAdapter(t *testing.T) {
	if got := ifaceForUSBDeviceIn(boxLinks, "4-1"); got != "" {
		t.Errorf("a hub fault was blamed on %q; it backs three adapters", got)
	}
}

// "4-1" is a prefix of "4-1.3". Substring matching would make every hub fault
// look like a fault on each dongle beneath it.
func TestPrefixesAreNotMistakenForDevices(t *testing.T) {
	links := map[string]string{"wlan-usb-46c7": "/sys/devices/usb4/4-1/4-1.3/4-1.3:1.0"}
	if got := ifaceForUSBDeviceIn(links, "4-1.3"); got != "wlan-usb-46c7" {
		t.Errorf("exact device not matched: got %q", got)
	}
	// Only one interface is behind this hub, so "4-1" resolves to it -- that is
	// correct and not the bug. The bug would be "4-1.30" or "-1.3" matching.
	for _, notADevice := range []string{"4-1.30", "-1.3", "1.3", "4-1.3:1.0"} {
		if got := ifaceForUSBDeviceIn(links, notADevice); got != "" {
			t.Errorf("ifaceForUSBDeviceIn(%q) matched %q by substring", notADevice, got)
		}
	}
}

// The onboard radio is not on USB at all. A USB fault must never land on it.
func TestTheOnboardRadioIsNeverBlamedForAUSBFault(t *testing.T) {
	for _, dev := range []string{"2-1", "4-1", "4-1.3", "mmc1:0001"} {
		if got := ifaceForUSBDeviceIn(boxLinks, dev); got == "wlan0" {
			t.Errorf("USB device %q was blamed on the onboard radio", dev)
		}
	}
}

// A fault must not be forgeable, because acting on one unbinds a driver.
//
// MEASURED on the box: writing an exact copy of the real message into
// /dev/kmsg produced this record. The kernel stamps facility LOG_USER (adding
// 8 to the priority) and attaches NO structured fields, so a userspace writer
// cannot claim to be the USB subsystem or name a device. Both halves of the
// filter reject it independently.
const forgedFromUserspace = "11,1952,87630012402,-;mt7921u 9-9:1.0: tx urb failed: -71\n"

func TestAUserspaceWriteCannotForgeAFault(t *testing.T) {
	rec, ok := parseKmsgRecord(forgedFromUserspace)
	if !ok {
		t.Fatal("the record did not parse")
	}
	// Facility is folded into the priority byte: kernel messages are 0-7,
	// anything written by userspace is 8 or above. That alone excludes it.
	if rec.Priority <= 3 {
		t.Errorf("a userspace write arrived with kernel priority %d", rec.Priority)
	}
	if rec.Subsystem != "" || rec.Device != "" {
		t.Errorf("userspace managed to set structured fields: subsystem=%q device=%q",
			rec.Subsystem, rec.Device)
	}
	if _, ok := usbFaultOf(rec); ok {
		t.Fatal("a forged message was accepted as a USB fault; it could trigger a driver unbind")
	}
}

// Malformed input must be dropped, not guessed at. A misparse here names the
// wrong device, and the action taken on a fault is a reset of that device.
func TestMalformedRecordsAreRejected(t *testing.T) {
	for _, bad := range []string{
		"",
		"no semicolon at all",
		"3,1163;too few header fields",
		"x,1163,83278214199,-;priority is not a number",
		"3,x,83278214199,-;seq is not a number",
		"3,1163,x,-;timestamp is not a number",
	} {
		if _, ok := parseKmsgRecord(bad); ok {
			t.Errorf("parsed malformed record %q", bad)
		}
	}
}

// A record with no continuation lines is well-formed and simply carries no
// device. It must parse, and must not be blamed on anything.
func TestRecordWithoutContinuationLinesCarriesNoDevice(t *testing.T) {
	rec, ok := parseKmsgRecord("3,10,20,-;something broke")
	if !ok {
		t.Fatal("a record without continuation lines did not parse")
	}
	if rec.Message != "something broke" {
		t.Errorf("message = %q", rec.Message)
	}
	if _, ok := usbFaultOf(rec); ok {
		t.Error("a fault with no device was attributed to one anyway")
	}
}
