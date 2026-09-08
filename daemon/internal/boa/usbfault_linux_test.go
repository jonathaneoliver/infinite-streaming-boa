//go:build linux

package boa

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/*
 * The half that cannot be tested off the appliance: reading real sysfs.
 *
 * The portable tests in usbfault_test.go feed ifaceForUSBDeviceIn a map copied
 * off the box, which proves the MATCHING rules and proves nothing about whether
 * we can build that map. This file closes that gap, and it is written to be
 * self-validating on any box with any topology -- it derives the device paths
 * from sysfs rather than naming the ones this box happened to have, so moving a
 * dongle between a hub and a root port cannot make it stale.
 */

// TestSysfsRoundTripOnRealHardware walks every USB-backed interface the machine
// actually has and checks that a fault on its device would be attributed back
// to it.
//
// The failure this catches is a mismatch between the two halves: netDeviceLinks
// resolving to one shape of path while usbDevicePath produces another. Both
// were derived from the same box on the same afternoon, which is exactly the
// circumstance in which two things agree by coincidence.
func TestSysfsRoundTripOnRealHardware(t *testing.T) {
	links := netDeviceLinks()
	if len(links) == 0 {
		t.Skip("no device-backed network interfaces here")
	}

	var usbSeen int
	for iface, path := range links {
		if !onUSBBus(path) {
			continue // onboard radio, mmc-backed, and so on
		}
		usbSeen++

		// The device is the parent of the interface directory: ".../4-1.3/4-1.3:1.0"
		// -> "4-1.3". This is the same relationship usbDevicePath encodes when
		// it strips the interface number off a DEVICE= tag.
		dev := filepath.Base(filepath.Dir(path))
		if strings.Contains(dev, ":") {
			t.Errorf("%s: derived device %q from %q, which is an interface directory, not a device",
				iface, dev, path)
			continue
		}

		got := ifaceForUSBDeviceIn(links, dev)
		if got != iface {
			// Ambiguity is a legitimate answer only when something really is
			// shared -- a composite device presenting two interfaces, say.
			if got == "" {
				t.Logf("%s: device %q is shared and was left unattributed (acceptable)", iface, dev)
				continue
			}
			t.Errorf("%s on device %q was attributed to %q", iface, dev, got)
		}
	}

	if usbSeen == 0 {
		t.Skip("no USB-backed network interfaces on this machine")
	}
	t.Logf("round-tripped %d USB-backed interface(s)", usbSeen)
}

// The DEVICE= tag the kernel puts on a fault must name a device that exists in
// sysfs, or a reset would have nothing to act on.
func TestDeviceTagsResolveToRealSysfsPaths(t *testing.T) {
	links := netDeviceLinks()
	var checked int
	for iface, path := range links {
		if !onUSBBus(path) {
			continue
		}
		dev := filepath.Base(filepath.Dir(path))
		// This is the path resetUSBDevice globs for its unbind/bind writes.
		matches, err := filepath.Glob("/sys/bus/usb/devices/" + dev + ":*")
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		if len(matches) == 0 {
			t.Errorf("%s: no USB interface directories under /sys/bus/usb/devices/%s:* — a reset would find nothing to rebind", iface, dev)
			continue
		}
		// And each must have a driver we could reload.
		var withDriver int
		for _, m := range matches {
			if _, err := os.Stat(filepath.Join(m, "driver")); err == nil {
				withDriver++
			}
		}
		if withDriver == 0 {
			t.Errorf("%s: device %s has no bound driver in sysfs", iface, dev)
		}
		checked++
	}
	if checked == 0 {
		t.Skip("no USB-backed network interfaces on this machine")
	}
	t.Logf("checked %d USB device(s) are resettable", checked)
}

// TestResetUSBDeviceOnRealHardware actually unbinds and rebinds a device.
//
// Off by default and gated behind an explicit device name, because it drops the
// adapter's clients for about ten seconds. It exists because the reset was
// first proven with two shell redirections, and shell redirections are not what
// ships -- this runs the code that does.
//
//	BOA_TEST_RESET_USB=4-1.3 ./boa.test -test.run TestResetUSBDeviceOnRealHardware -test.v
func TestResetUSBDeviceOnRealHardware(t *testing.T) {
	dev := os.Getenv("BOA_TEST_RESET_USB")
	if dev == "" {
		t.Skip("set BOA_TEST_RESET_USB=<bus path> to reset a device for real")
	}

	before := ifaceForUSBDevice(dev)
	if before == "" {
		t.Fatalf("device %q backs no single network interface; refusing to reset it", dev)
	}
	t.Logf("resetting %s (%s)", dev, before)

	if err := resetUSBDevice(dev); err != nil {
		t.Fatalf("reset failed: %v", err)
	}

	// The interface must come BACK, under the SAME NAME. A reset that leaves the
	// device unbound has made things worse than the fault it was answering, and
	// one that renames the interface breaks every name the operator was using.
	started := time.Now()
	for time.Since(started) < 30*time.Second {
		if after := ifaceForUSBDevice(dev); after != "" {
			t.Logf("came back as %s after %s", after, time.Since(started).Round(time.Second))
			if after != before {
				t.Errorf("interface was renamed across the reset: %s -> %s", before, after)
			}
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("device %s did not come back within 30s; it may now be unbound", dev)
}

// /dev/kmsg must be readable and seekable, or the watch silently never runs.
func TestKmsgIsReadableAndSkippable(t *testing.T) {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		t.Skipf("cannot open /dev/kmsg here: %v", err)
	}
	defer f.Close()

	// SEEK_END is what stops a daemon restart replaying old faults. If the
	// kernel refused it, every restart would re-report the whole ring buffer
	// and could reset a device over a fault from hours ago.
	if _, err := f.Seek(0, 2); err != nil {
		t.Fatalf("seek to end of /dev/kmsg: %v — old faults would be replayed on every restart", err)
	}
}
