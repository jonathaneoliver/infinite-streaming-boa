//go:build linux

package boa

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// watchUSBFaults follows the kernel log and reports USB errors against the
// interface they belong to.
//
// /dev/kmsg rather than journalctl: it is world-readable, the daemon already
// runs as root with ProtectKernelLogs=no, each read returns exactly one record
// with the kernel's own structured fields attached, and it costs no subprocess.
// journalctl -k -o json carries the same fields and would have meant spawning
// and parsing a child process for the life of the daemon.
func (e *Engine) watchUSBFaults() {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		// Reported, not swallowed. Without this watch a dead radio looks
		// healthy, which is precisely the failure it exists to catch, so the
		// operator has to know the watch is not running.
		fmt.Printf("infinite-streaming-boa: USB fault watch unavailable: %v\n", err)
		return
	}

	// Skip everything already in the ring buffer. A daemon restart must not
	// replay old faults: they would be reported as if they had just happened
	// and, worse, a stale terminal fault would reset a device that is working
	// now. SEEK_END on kmsg means exactly "start from the next record".
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		fmt.Printf("infinite-streaming-boa: USB fault watch could not skip history: %v\n", err)
		f.Close()
		return
	}

	w := newUSBWatcher(e)
	buf := make([]byte, 8192)
	for {
		n, err := f.Read(buf)
		if err != nil {
			// EPIPE means the ring overwrote records we had not read yet. The
			// records are gone; the next read resumes at the oldest surviving
			// one. Nothing to do but carry on.
			if errors.Is(err, syscall.EPIPE) {
				continue
			}
			// EINVAL is what a short buffer looks like. Ours is 8K against a
			// kernel record limit well under that, so this is not expected --
			// but a tight loop on a permanent error would spin a core, so it
			// is treated as fatal to the watch and said out loud.
			fmt.Printf("infinite-streaming-boa: USB fault watch stopped: %v\n", err)
			f.Close()
			return
		}
		rec, ok := parseKmsgRecord(string(buf[:n]))
		if !ok {
			continue
		}
		fault, ok := usbFaultOf(rec)
		if !ok {
			continue
		}
		w.handle(fault)
	}
}

// ifaceForUSBDevice names the network interface a USB device backs, by asking
// sysfs where each interface actually lives. See ifaceForUSBDeviceIn for the
// matching rules; this half only gathers the evidence.
func ifaceForUSBDevice(device string) string {
	return ifaceForUSBDeviceIn(netDeviceLinks(), device)
}

// netDeviceLinks maps each network interface to the device directory it
// resolves to. Interfaces with no device link -- the bridge, loopback, and the
// onboard radio, which hangs off mmc rather than USB -- are simply absent.
func netDeviceLinks() map[string]string {
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, ent := range entries {
		target, err := filepath.EvalSymlinks(filepath.Join(sysClassNet, ent.Name(), "device"))
		if err != nil {
			continue // not a device-backed interface, or gone since the listing
		}
		out[ent.Name()] = target
	}
	return out
}

// sysClassNet is a variable so a test can point it at a tree it built.
var sysClassNet = "/sys/class/net"

// resetUSBDevice reloads a USB device's driver, which reloads its firmware.
//
// MEASURED 2026-09-08 on the box, on the dongle that had been off the air for
// 75 minutes with hostapd insisting it was ENABLED:
//
//	echo 4-1.3:1.0 > /sys/bus/usb/drivers/mt7921u/unbind
//	echo 4-1.3:1.0 > /sys/bus/usb/drivers/mt7921u/bind
//
//	usb 4-1.3: reset SuperSpeed USB device number 9 using xhci-hcd
//	mt7921u 4-1.3:1.0: WM Firmware Version: ____010000
//	mt7921u 4-1.3:1.0 wlan-usb-46c7: renamed from wlan1
//
// and two clients associated within the minute. A physical replug was not
// needed, which is the point: an appliance that needs somebody to walk over to
// it has not recovered.
//
// The driver is read from sysfs rather than assumed, so this works for the
// r8152 ethernet adapters on the same hub without knowing their name.
func resetUSBDevice(device string) error {
	ifaces, err := filepath.Glob("/sys/bus/usb/devices/" + device + ":*")
	if err != nil || len(ifaces) == 0 {
		return fmt.Errorf("no USB interfaces found for device %s", device)
	}

	var reset int
	var firstErr error
	for _, dir := range ifaces {
		name := filepath.Base(dir) // e.g. "4-1.3:1.0"
		drvPath, err := filepath.EvalSymlinks(filepath.Join(dir, "driver"))
		if err != nil {
			continue // this interface has no driver bound; nothing to reload
		}
		drv := filepath.Base(drvPath)

		if err := writeSysfs(filepath.Join(drvPath, "unbind"), name); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("unbind %s from %s: %w", name, drv, err)
			}
			continue
		}
		// The device needs a moment to settle before it will accept a bind;
		// binding into a teardown fails and leaves the interface unbound, which
		// is worse than the fault we started with.
		time.Sleep(3 * time.Second)
		if err := writeSysfs(filepath.Join(drvPath, "bind"), name); err != nil {
			// Nothing to fall back on: the device is now bound to no driver.
			// Say so plainly -- this is the one outcome worse than doing
			// nothing, and it must never be reported as a success.
			return fmt.Errorf("rebind %s to %s FAILED, the device is now unbound: %w", name, drv, err)
		}
		reset++
	}

	if reset == 0 {
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("device %s had no bound driver to reload", device)
	}
	return nil
}

func writeSysfs(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(value); err != nil {
		// A sysfs write that is refused reports the reason here and nowhere
		// else; the file has no other output.
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return pe.Err
		}
		return err
	}
	return nil
}
