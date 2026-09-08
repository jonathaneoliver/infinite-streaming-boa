//go:build !linux

package boa

import "fmt"

// The USB fault watch reads /dev/kmsg and rebinds drivers through sysfs, so it
// exists only on the appliance. The daemon still has to COMPILE on a developer's
// Mac -- see CLAUDE.md -- hence these three.
//
// Each one is honest about doing nothing rather than reporting a success it did
// not have. A stub that returned nil from resetUSBDevice would tell the event
// log a device had been reset when nothing had happened, which is the exact
// failure mode this feature was written to remove.

func (e *Engine) watchUSBFaults() {}

func ifaceForUSBDevice(string) string { return "" }

func resetUSBDevice(device string) error {
	return fmt.Errorf("cannot reset USB device %s: only implemented on Linux", device)
}
