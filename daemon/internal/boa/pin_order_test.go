package boa

import (
	"strings"
	"sync"
	"testing"
)

// A PIN BARS THE OTHER RADIOS BEFORE THE ONE THE CLIENT IS ON (#498).
//
// The client is asked to move while still associated, so every radio it must
// not use has to be barred first: otherwise it can leave, or be disconnected,
// and land on one of them. The radio it is on is barred last, which is what
// disconnects a client that refused. Nothing is ever sent to the destination:
// the old final deauth went to wherever the client was at that moment, and a
// quick client was by then on the radio it had just been sent to.
func TestPinBarsOtherRadiosBeforeTheCurrentOne(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:01"

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
		cfg:          Config{WlanPorts: []string{"wlan0", "wlan-usb", "wlan-usb2"}},
		stationRadio: map[string]string{mac: "wlan-usb"},
	}
	t.Cleanup(func() { e.clearPins("test over") })

	// A gather onto wlan0: the client is on wlan-usb, and wlan-usb2 is the
	// third radio it must not escape to.
	op := &pinOp{to: "wlan0", deny: []string{"wlan-usb", "wlan-usb2"}}
	if _, err := e.runPin(op, []string{mac}, "wlan0", "wlan0", 5,
		"%d client(s) to %s, denied on %s for %.0fs"); err != nil {
		t.Fatalf("runPin: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	addAt := map[string]int{}
	for i, s := range sent {
		if s[0] == "wlan0" {
			t.Errorf("sent %q to the destination wlan0; nothing may touch it", s[1])
		}
		if strings.HasPrefix(s[1], "DENY_ACL ADD_MAC") {
			if _, seen := addAt[s[0]]; !seen {
				addAt[s[0]] = i
			}
		}
	}
	other, current := addAt["wlan-usb2"], addAt["wlan-usb"]
	if _, ok := addAt["wlan-usb2"]; !ok {
		t.Fatalf("never barred wlan-usb2: %v", sent)
	}
	if _, ok := addAt["wlan-usb"]; !ok {
		t.Fatalf("never barred wlan-usb, the radio the client is on: %v", sent)
	}
	if other > current {
		t.Errorf("barred the client's own radio (step %d) before the other one (step %d): %v",
			current, other, sent)
	}
}
