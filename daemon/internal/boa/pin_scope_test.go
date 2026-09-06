package boa

import (
	"strings"
	"sync"
	"testing"
)

/*
 * A PER-CLIENT MOVEMENT MUST NOT CANCEL ANOTHER CLIENT'S.
 *
 * clearPins lifts every operation in force, which is right for a box-wide
 * control: a gather covers every client, so every older claim really is
 * superseded. Inheriting it at per-client scope is a silent bug -- two devices
 * each running a walkabout cancel each other at every crossing, and the first
 * client wanders off the band its own pattern is still writing keyframes for.
 *
 * The release itself is not optional, which is the part that makes this subtle.
 * e.pins holds ONE op per MAC, so re-claiming a client without releasing the
 * old op first orphans that op's deny entries for it. Narrow, not absent.
 */
func TestAPinOnOneClientLeavesAnotherClientsAlone(t *testing.T) {
	const a, b = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"

	var mu sync.Mutex
	var sent [][2]string
	origSend, origReach := hostapdSend, hostapdReachable
	t.Cleanup(func() { hostapdSend, hostapdReachable = origSend, origReach })
	hostapdReachable = func(string) bool { return true }
	hostapdSend = func(iface, cmd string) (string, error) {
		mu.Lock()
		sent = append(sent, [2]string{iface, cmd})
		mu.Unlock()
		return "OK\n", nil
	}

	e := &Engine{
		cfg:          Config{WlanPorts: []string{"wlan-usb", "wlan0"}},
		stationRadio: map[string]string{a: "wlan-usb", b: "wlan-usb"},
	}

	// Two independent operations, one per client.
	opA := &pinOp{to: "wlan0", deny: []string{"wlan-usb"}}
	opB := &pinOp{to: "wlan0", deny: []string{"wlan-usb"}}
	opA.pending = map[string]bool{a: true}
	opB.pending = map[string]bool{b: true}
	e.notePins(opA)
	e.notePins(opB)

	// Through the REAL control, not clearPinsFor directly: the bug being guarded
	// is which clear the control calls, so the test has to go through it.
	if err := e.EvictClient(b, 5); err != nil {
		t.Fatalf("EvictClient: %v", err)
	}

	e.mu.RLock()
	stillA, stillB := e.pins[a], e.pins[b]
	e.mu.RUnlock()

	if stillA != opA {
		t.Errorf("client A's pin was cleared by a control aimed at client B; "+
			"A is now held by %v", stillA)
	}
	if stillB == opB {
		t.Error("client B's old pin survived its own supersede")
	}

	// And the release actually happened for B, or the next claim would orphan
	// the entries this one left behind.
	mu.Lock()
	defer mu.Unlock()
	var delB bool
	for _, s := range sent {
		if strings.Contains(s[1], "DEL_MAC "+b) {
			delB = true
		}
		if strings.Contains(s[1], "DEL_MAC "+a) {
			t.Errorf("client A was released on %s by a control aimed at B", s[0])
		}
	}
	if !delB {
		t.Errorf("client B's deny was never removed; sent=%v", sent)
	}
}

/*
 * Taking the last client out of a shared operation finishes it, once.
 *
 * A pinOp can cover several clients and lifts "together". If a per-client
 * control removes the last one, the operation is over -- and its own timeout
 * must not later log a lift for nobody. The once is consumed here to stop that.
 */
func TestEmptyingASharedOperationFinishesItExactlyOnce(t *testing.T) {
	const a, b = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"

	origSend, origReach := hostapdSend, hostapdReachable
	t.Cleanup(func() { hostapdSend, hostapdReachable = origSend, origReach })
	hostapdReachable = func(string) bool { return true }
	hostapdSend = func(string, string) (string, error) { return "OK\n", nil }

	e := &Engine{
		cfg:          Config{WlanPorts: []string{"wlan-usb", "wlan0"}},
		stationRadio: map[string]string{a: "wlan-usb", b: "wlan-usb"},
	}

	// One operation covering both, as a box-wide gather produces.
	op := &pinOp{to: "wlan0", deny: []string{"wlan-usb"}}
	op.pending = map[string]bool{a: true, b: true}
	e.notePins(op)

	e.clearPinsFor([]string{a}, "superseded")
	e.mu.RLock()
	remaining := len(op.pending)
	heldB := e.pins[b]
	e.mu.RUnlock()
	if remaining != 1 || heldB != op {
		t.Fatalf("taking A out should leave B in the operation; pending=%d heldB=%v",
			remaining, heldB)
	}

	e.clearPinsFor([]string{b}, "superseded")

	// Consumed: a later lift must find nothing to do rather than logging again.
	ran := false
	op.once.Do(func() { ran = true })
	if ran {
		t.Error("the operation's once was not consumed when its last client left, " +
			"so its timeout would log a lift for nobody")
	}
}
