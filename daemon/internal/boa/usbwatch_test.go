package boa

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

/*
 * The watcher's job is to be USEFUL about a fault, which is a different thing
 * from noticing one: a report per record would have put 180 identical lines in
 * the activity log, and a reset per record would have rebound the device 180
 * times.
 *
 * Everything here drives the real handle() path through injected seams, with a
 * clock the test controls -- the intervals that matter are minutes long and a
 * test that waited them out would take a quarter of an hour.
 */

type fakeUSB struct {
	clock    time.Time
	events   []string // "kind|iface|text"
	logs     []string // what went to the daemon's own output
	resets   []string
	resetErr error
}

func newFakeUSB() (*usbWatcher, *fakeUSB) {
	f := &fakeUSB{clock: time.Date(2026, 9, 8, 19, 0, 0, 0, time.UTC)}
	w := &usbWatcher{
		// Every device in these tests resolves, so the tests are about
		// behaviour rather than about sysfs. Attribution has its own test.
		ifaceFor: func(dev string) string {
			if dev == "4-1.3" {
				return "wlan-usb-46c7"
			}
			if dev == "4-1.4" {
				return "lan-usb-6518"
			}
			return ""
		},
		recover: func(dev string) error {
			f.resets = append(f.resets, dev)
			return f.resetErr
		},
		report: func(kind, iface, format string, args ...any) {
			f.events = append(f.events, kind+"|"+iface+"|"+fmt.Sprintf(format, args...))
		},
		log:          func(format string, args ...any) { f.logs = append(f.logs, fmt.Sprintf(format, args...)) },
		now:          func() time.Time { return f.clock },
		bursts:       map[string]*usbBurst{},
		history:      map[string][]time.Time{},
		lastRecovery: map[string]time.Time{},
	}
	return w, f
}

func (f *fakeUSB) advance(d time.Duration) { f.clock = f.clock.Add(d) }

func (f *fakeUSB) matching(sub string) int {
	n := 0
	for _, e := range f.events {
		if strings.Contains(e, sub) {
			n++
		}
	}
	return n
}

func urbFault() usbFault {
	return usbFault{Device: "4-1.3", Message: "mt7921u 4-1.3:1.0: tx urb failed: -71"}
}

// The burst that would have drowned the finding in its own noise.
func TestABurstIsReportedOnce(t *testing.T) {
	w, f := newFakeUSB()
	for i := 0; i < 180; i++ {
		w.handle(urbFault())
		f.advance(133 * time.Microsecond) // the measured spacing
	}

	if len(f.events) != 1 {
		t.Fatalf("180 identical faults raised %d events, want 1:\n%s",
			len(f.events), strings.Join(f.events, "\n"))
	}
	if !strings.Contains(f.events[0], "wlan-usb-46c7") {
		t.Errorf("the event did not name the radio: %q", f.events[0])
	}
	// The bus path is not a name anybody using the box has seen. It must not be
	// what the operator is shown when an interface name exists.
	if strings.Contains(f.events[0], "USB device 4-1.3") {
		t.Errorf("the event named the bus path over the interface: %q", f.events[0])
	}
	if len(f.resets) != 0 {
		t.Errorf("a survivable burst reset the device %d times", len(f.resets))
	}
}

// A fault that returns after the window is news again -- the two measured
// timeouts were 31 minutes apart and both mattered.
func TestAFaultThatReturnsLaterIsReportedAgain(t *testing.T) {
	w, f := newFakeUSB()
	w.handle(urbFault())
	f.advance(usbBurstWindow + time.Second)
	w.handle(urbFault())

	if len(f.events) != 2 {
		t.Fatalf("a fault %s later raised %d events, want 2", usbBurstWindow, len(f.events))
	}
}

// The measured failure: one line, and the device is known not to be coming back.
func TestAKnownTerminalFaultRecoversImmediately(t *testing.T) {
	w, f := newFakeUSB()
	w.handle(usbFault{
		Device:   "4-1.3",
		Message:  "mt7921u 4-1.3:1.0: timed out waiting for pending tx",
		Terminal: true,
	})

	if len(f.resets) != 1 || f.resets[0] != "4-1.3" {
		t.Fatalf("the terminal fault did not reset the device: %v", f.resets)
	}
	if f.matching("off the air") == 0 {
		t.Errorf("the report did not say the radio was off the air:\n%s", strings.Join(f.events, "\n"))
	}
	if f.matching(EventAction+"|") < 2 {
		t.Errorf("the reset was not reported before and after:\n%s", strings.Join(f.events, "\n"))
	}
}

// THE REQUIREMENT THAT THIS WORK FOR ANY DEVICE ON ANY PORT.
//
// A driver nothing in this package has heard of, reporting a failure in words
// nothing in this package matches, on a device that is not a radio. It must
// still be reported, and it must still reach a reset once it has proved it is
// not recovering -- by repetition alone, with no message string consulted.
func TestAnUnknownDeviceEscalatesByRepetitionAlone(t *testing.T) {
	w, f := newFakeUSB()
	odd := usbFault{Device: "4-1.4", Message: "somedriver 4-1.4:1.0: widget bus stalled (0x8e)"}

	for i := 0; i < usbEscalateAfter; i++ {
		w.handle(odd)
		f.advance(usbBurstWindow + time.Second)
	}

	if len(f.resets) != 1 || f.resets[0] != "4-1.4" {
		t.Fatalf("an unknown device failing %d times was not reset: %v", usbEscalateAfter, f.resets)
	}
	if f.matching("lan-usb-6518") == 0 {
		t.Errorf("the events did not name the interface:\n%s", strings.Join(f.events, "\n"))
	}
}

// Escalation must need PERSISTENCE, not just repetition spread thin. The
// measured hardware threw one burst and recovered on its own; resetting a
// device that is already coming back interrupts the recovery.
func TestFaultsSpreadBeyondTheWindowDoNotEscalate(t *testing.T) {
	w, f := newFakeUSB()
	for i := 0; i < usbEscalateAfter+2; i++ {
		w.handle(urbFault())
		f.advance(usbEscalateWindow + time.Minute)
	}
	if len(f.resets) != 0 {
		t.Fatalf("isolated faults far apart triggered %d resets", len(f.resets))
	}
}

// One device's trouble must not reset another's. They share a hub, and the hub
// is exactly what fails.
func TestEscalationIsPerDevice(t *testing.T) {
	w, f := newFakeUSB()
	for i := 0; i < usbEscalateAfter; i++ {
		w.handle(usbFault{Device: "4-1.3", Message: "a"})
		w.handle(usbFault{Device: "4-1.4", Message: "b"})
		f.advance(usbBurstWindow + time.Second)
	}
	if len(f.resets) != 2 {
		t.Fatalf("want one reset each, got %v", f.resets)
	}
	seen := map[string]bool{}
	for _, r := range f.resets {
		if seen[r] {
			t.Fatalf("a device was reset twice: %v", f.resets)
		}
		seen[r] = true
	}
}

// A device that reports its decline in several different phrasings is still one
// unwell device. Escalation is keyed on the device, not the message.
func TestDifferentMessagesFromOneDeviceStillEscalate(t *testing.T) {
	w, f := newFakeUSB()
	for i, msg := range []string{"first phrasing", "second phrasing", "third phrasing"} {
		w.handle(usbFault{Device: "4-1.3", Message: msg})
		if i < 2 {
			f.advance(usbBurstWindow + time.Second)
		}
	}
	if len(f.resets) != 1 {
		t.Fatalf("three different messages from one device gave %d resets, want 1", len(f.resets))
	}
}

// A rebind takes seconds, during which the dying device logs more faults. Those
// must not drive a second reset into the middle of the first.
func TestTheCooldownStopsResetsPilingUp(t *testing.T) {
	w, f := newFakeUSB()
	term := usbFault{Device: "4-1.3", Message: "timed out waiting for pending tx", Terminal: true}

	w.handle(term)
	f.advance(2 * time.Second)
	w.handle(term)

	if len(f.resets) != 1 {
		t.Fatalf("got %d resets inside the cooldown, want 1", len(f.resets))
	}
	// And it must SAY it held off. A device still failing through the cooldown
	// is a hardware problem for the operator, not one the box is handling.
	if f.matching("Check the cable") == 0 {
		t.Errorf("holding off was not reported:\n%s", strings.Join(f.events, "\n"))
	}
}

// Once the cooldown is over, a device that fails again is reset again.
func TestRecoveryResumesAfterTheCooldown(t *testing.T) {
	w, f := newFakeUSB()
	term := usbFault{Device: "4-1.3", Message: "timed out waiting for pending tx", Terminal: true}

	w.handle(term)
	f.advance(usbRecoveryCooldown + time.Minute)
	w.handle(term)

	if len(f.resets) != 2 {
		t.Fatalf("got %d resets, want 2", len(f.resets))
	}
}

// A reset that recovered the device must not leave its old failures banked, or
// the next isolated hiccup reaches the escalation threshold instantly.
func TestARecoveredDeviceStartsItsEvidenceAgain(t *testing.T) {
	w, f := newFakeUSB()
	for i := 0; i < usbEscalateAfter; i++ {
		w.handle(urbFault())
		f.advance(usbBurstWindow + time.Second)
	}
	if len(f.resets) != 1 {
		t.Fatalf("setup: want 1 reset, got %d", len(f.resets))
	}

	// Well past the cooldown, one more isolated fault.
	f.advance(usbRecoveryCooldown + time.Minute)
	w.handle(urbFault())

	if len(f.resets) != 1 {
		t.Fatalf("a single fault after a successful reset reset the device again: %v", f.resets)
	}
}

// A failed reset must be reported as a failure. Announcing a recovery that did
// not happen is the same class of bug this whole feature exists to remove.
func TestAFailedResetIsReportedAsOne(t *testing.T) {
	w, f := newFakeUSB()
	f.resetErr = fmt.Errorf("rebind 4-1.3:1.0 to mt7921u FAILED, the device is now unbound")

	w.handle(usbFault{Device: "4-1.3", Message: "timed out waiting for pending tx", Terminal: true})

	if f.matching("could not reset") == 0 {
		t.Fatalf("a failed reset was not reported:\n%s", strings.Join(f.events, "\n"))
	}
	for _, e := range f.events {
		if strings.Contains(e, "should return within") {
			t.Fatalf("a failed reset claimed the device was coming back: %q", e)
		}
	}
}

// A fault on something with no interface of its own -- a hub, or a device
// shared by several adapters -- is still worth reporting. It is the hub every
// dongle hangs off.
func TestAFaultWithNoInterfaceIsStillReported(t *testing.T) {
	w, f := newFakeUSB()
	w.handle(usbFault{Device: "4-1", Message: "usb 4-1: link is down"})

	if len(f.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(f.events))
	}
	if !strings.Contains(f.events[0], "USB device 4-1") {
		t.Errorf("an unattributable fault was not named by its bus path: %q", f.events[0])
	}
}

// THE ACTIVITY LOG REQUIREMENT: every line reaches BOTH the event ring the
// interface renders and the daemon's own output. The ring is in memory and does
// not survive a restart -- and a USB fault can take the box down with it, so
// the durable copy is the one that is still there afterwards.
func TestEveryReportReachesBothLogs(t *testing.T) {
	w, f := newFakeUSB()
	w.handle(usbFault{Device: "4-1.3", Message: "timed out waiting for pending tx", Terminal: true})

	if len(f.events) == 0 {
		t.Fatal("nothing reached the activity log")
	}
	if len(f.logs) != len(f.events) {
		t.Fatalf("activity log has %d lines, daemon output has %d; they must match",
			len(f.events), len(f.logs))
	}
}
