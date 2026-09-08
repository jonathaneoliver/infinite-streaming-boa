package boa

import (
	"fmt"
	"sync"
	"time"
)

/*
 * What to DO about a USB fault, kept apart from how one is read.
 *
 * Three things have to happen and they pull against each other.
 *
 * The fault must be REPORTED, because the box reporting a dead radio as healthy
 * is what cost an hour on 2026-09-08.
 *
 * It must be reported ONCE, because the failure arrives as a burst -- 180
 * identical records inside 24 milliseconds -- and an event log holding 180
 * copies of one line has buried the finding as thoroughly as saying nothing.
 *
 * And it must work for HARDWARE THIS BOX HAS NEVER SEEN. Nothing here may test
 * a driver name, a message string or a bus path to decide whether a device is
 * ours or where it is plugged in. The kernel already attributes the fault; this
 * file's job is to believe it. The one concession is knownTerminalFaults, which
 * only makes recovery FASTER for a failure already measured -- every other
 * device reaches the same recovery by repetition alone.
 */

// usbBurstWindow is how long identical faults on one device are folded into a
// single report. Chosen against the measurement: the burst spanned 24ms, and
// the two terminal timeouts were 31 minutes apart and are both worth seeing.
const usbBurstWindow = 30 * time.Second

// A device that keeps failing has not recovered, whatever its driver calls the
// failure. These two numbers are the device-agnostic route to a reset: separate
// bursts of USB errors, from one device, inside one window.
//
// Three rather than two because the measured hardware threw one burst, dropped
// off the bus and came back working on its own. Resetting on that would have
// interrupted a recovery already in progress.
const (
	usbEscalateAfter  = 3
	usbEscalateWindow = 10 * time.Minute
)

// usbRecoveryCooldown is the shortest gap between two automatic recoveries of
// the same device.
//
// A rebind takes about 8 seconds to bring the interface back and hostapd a few
// more to rebuild the BSS, during which further faults can be logged. Without a
// cooldown those would drive a second rebind into the middle of the first.
const usbRecoveryCooldown = 5 * time.Minute

// usbWatcher turns a stream of faults into events and, for devices that are not
// coming back on their own, a reset.
//
// The seams are fields rather than package functions so a test can drive the
// whole path -- burst, escalation, report, recover -- without a USB bus.
type usbWatcher struct {
	// ifaceFor names the network interface a USB device backs, or "" when the
	// device backs none or is shared by several. Faults on a device with no
	// interface are still reported: a hub throwing errors is worth knowing
	// about, and it is the hub every dongle hangs off.
	ifaceFor func(device string) string
	// recover resets one device. It returns an error the caller reports rather
	// than swallowing, because a recovery that silently failed to happen is the
	// same class of bug this whole file exists to fix.
	recover func(device string) error
	// report raises one event, for the activity log in the interface.
	report func(kind, iface, format string, args ...any)
	// log writes the same thing to the daemon's own output, which is where it
	// becomes durable: the event ring is in memory and does not survive a
	// restart, and a fault that takes a radio down can be followed by one.
	log func(format string, args ...any)
	now func() time.Time

	mu sync.Mutex
	// bursts counts identical faults inside the current window, keyed by device
	// and message.
	bursts map[string]*usbBurst
	// history is when each distinct burst on a device began, for escalation.
	history map[string][]time.Time
	// lastRecovery is when each device was last reset.
	lastRecovery map[string]time.Time
}

type usbBurst struct {
	first time.Time
	count int
	// reported marks that the window's first event has already gone out, so
	// the rest of the burst only increments the count.
	reported bool
}

func newUSBWatcher(e *Engine) *usbWatcher {
	return &usbWatcher{
		ifaceFor: ifaceForUSBDevice,
		recover:  resetUSBDevice,
		report: func(kind, iface, format string, args ...any) {
			e.logEvent(kind, iface, "", format, args...)
		},
		log: func(format string, args ...any) {
			fmt.Printf("infinite-streaming-boa: "+format+"\n", args...)
		},
		now:          time.Now,
		bursts:       map[string]*usbBurst{},
		history:      map[string][]time.Time{},
		lastRecovery: map[string]time.Time{},
	}
}

// handle processes one fault.
func (w *usbWatcher) handle(f usbFault) {
	iface := w.ifaceFor(f.Device)

	w.mu.Lock()
	now := w.now()
	key := f.Device + "\x00" + f.Message
	b := w.bursts[key]
	newBurst := b == nil || now.Sub(b.first) > usbBurstWindow
	if newBurst {
		b = &usbBurst{first: now}
		w.bursts[key] = b
		// A new burst is a new piece of evidence that this device is unwell.
		// Kept per DEVICE, not per message, so a driver that reports its
		// decline in three different phrasings still escalates.
		h := append(w.history[f.Device], now)
		for len(h) > 0 && now.Sub(h[0]) > usbEscalateWindow {
			h = h[1:]
		}
		w.history[f.Device] = h
	}
	b.count++
	persistent := len(w.history[f.Device]) >= usbEscalateAfter
	w.mu.Unlock()

	if newBurst {
		w.reportFault(f, iface, persistent)
	}

	// Two independent routes to the same remedy: a failure already MEASURED to
	// be unrecoverable on this hardware, or any device that has gone on failing
	// long enough to prove the point for itself.
	if f.Terminal || persistent {
		w.recoverDevice(f, iface, persistent)
	}
}

// nameFor is how a fault is addressed in the log. The interface name wherever
// one is known -- "4-1.3" is not a name anybody using this box has ever seen,
// and the whole value of the report is that it points at the radio on screen.
func nameFor(device, iface string) string {
	if iface != "" {
		return iface
	}
	return "USB device " + device
}

func (w *usbWatcher) reportFault(f usbFault, iface string, persistent bool) {
	who := nameFor(f.Device, iface)
	switch {
	case f.Terminal:
		w.say(EventWarning, iface,
			"%s — the USB link failed (%s). It is off the air and will not come back on its own.",
			who, f.Message)
	case persistent:
		w.say(EventWarning, iface,
			"%s — USB errors keep recurring (%s). Treating the device as failed.",
			who, f.Message)
	default:
		w.say(EventWarning, iface,
			"%s — USB error reported by the kernel: %s", who, f.Message)
	}
}

// recoverDevice resets a device whose USB link is not coming back by itself.
//
// MEASURED 2026-09-08: unbinding and rebinding the driver reloaded the firmware
// and put the radio back on the air, and two clients associated to it within
// the minute. A replug was NOT needed, which matters -- the box is meant to run
// without somebody standing next to it.
func (w *usbWatcher) recoverDevice(f usbFault, iface string, persistent bool) {
	who := nameFor(f.Device, iface)

	w.mu.Lock()
	last, seen := w.lastRecovery[f.Device]
	if seen && w.now().Sub(last) < usbRecoveryCooldown {
		w.mu.Unlock()
		// Said out loud rather than skipped quietly: a fault recurring through
		// the cooldown is a hardware problem the operator needs to know is
		// happening, not one the box is quietly handling.
		w.say(EventWarning, iface,
			"%s — the USB link failed again within %s of the last reset; leaving it alone. Check the cable, the port and the hub.",
			who, usbRecoveryCooldown)
		return
	}
	w.lastRecovery[f.Device] = w.now()
	// Escalation evidence is spent along with the reset: without this a device
	// that recovers would still be carrying three old bursts and would reset
	// again on its next isolated hiccup.
	delete(w.history, f.Device)
	w.mu.Unlock()

	w.say(EventAction, iface, "%s — resetting the USB device to bring it back", who)
	if err := w.recover(f.Device); err != nil {
		w.say(EventWarning, iface, "%s — could not reset the USB device: %v", who, err)
		return
	}
	w.say(EventAction, iface, "%s — USB device reset; it should return within a few seconds", who)
}

// say puts one line in both places: the activity log the interface shows, and
// the daemon's output, which systemd keeps after this process is gone.
func (w *usbWatcher) say(kind, iface, format string, args ...any) {
	w.report(kind, iface, format, args...)
	w.log(format, args...)
}
