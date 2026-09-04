package boa

import (
	"sync"
	"testing"
	"time"
)

// TestDeadzoneLiftsOnTheRadioItBanned pins issue #205.
//
// The deadzone's own deauth moves the client to the other radio -- measured at
// under a second on the bench box, because both radios publish one SSID onto
// one bridge. The lift used to ask radioFor(mac) a second time, by which point
// it named the radio the client had fled TO, so the DEL went there and the ban
// on the original radio was never removed. hostapd answers OK to a DEL for a
// MAC it is not holding, so nothing was logged and nothing failed.
//
// The roam is simulated here by moving stationRadio mid-flight, which is
// exactly what the tick does on hardware.
func TestDeadzoneLiftsOnTheRadioItBanned(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"

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
		stationRadio: map[string]string{mac: "wlan-usb"},
	}

	if err := e.LinkDeadzone(mac, 1, ScopeCurrent); err != nil {
		t.Fatalf("LinkDeadzone: %v", err)
	}

	// The client lands on the other radio, as it does on hardware.
	e.mu.Lock()
	e.stationRadio[mac] = "wlan0"
	e.mu.Unlock()

	// The lift is a background goroutine; wait for it rather than sleeping a
	// fixed time, so a slow machine does not fail a correct implementation.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) < 2 {
		t.Fatalf("wanted an ADD and a DEL, got %d control messages: %v", len(sent), sent)
	}

	add, del := sent[0], sent[1]
	if add[0] != "wlan-usb" {
		t.Errorf("ADD went to %q, want wlan-usb (the radio the client was on)", add[0])
	}
	if add[1] != "DENY_ACL ADD_MAC "+mac {
		t.Errorf("ADD command = %q", add[1])
	}
	if del[0] != "wlan-usb" {
		t.Errorf("DEL went to %q, want wlan-usb: the ban must be lifted from the "+
			"radio that holds it, not from wherever the client has since roamed", del[0])
	}
	if del[1] != "DENY_ACL DEL_MAC "+mac {
		t.Errorf("DEL command = %q", del[1])
	}
}

// TestDeadzoneRejectsBadInput keeps the guards that stop a malformed MAC or an
// absurd duration reaching the control socket.
func TestDeadzoneRejectsBadInput(t *testing.T) {
	origReach := hostapdReachable
	t.Cleanup(func() { hostapdReachable = origReach })
	hostapdReachable = func(string) bool { return true }

	e := &Engine{
		cfg:          Config{WlanPorts: []string{"wlan-usb"}},
		stationRadio: map[string]string{},
	}

	cases := []struct {
		name string
		mac  string
		dur  float64
	}{
		{"not a MAC", "nope", 10},
		{"injection", "aa:bb:cc:dd:ee:ff DEAUTHENTICATE ff:ff:ff:ff:ff:ff", 10},
		{"too short", "aa:bb:cc:dd:ee:ff", 0.5},
		{"too long", "aa:bb:cc:dd:ee:ff", 301},
	}
	for _, c := range cases {
		if err := e.LinkDeadzone(c.mac, c.dur, ScopeCurrent); err == nil {
			t.Errorf("%s: LinkDeadzone(%q, %v) = nil, want an error", c.name, c.mac, c.dur)
		}
	}
}
