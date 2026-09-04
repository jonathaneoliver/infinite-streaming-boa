package boa

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder stubs the control socket and remembers what went where, so a test
// can assert which radios a deadzone actually covered.
type recorder struct {
	mu   sync.Mutex
	sent [][2]string
}

func (r *recorder) send(iface, cmd string) (string, error) {
	r.mu.Lock()
	r.sent = append(r.sent, [2]string{iface, cmd})
	r.mu.Unlock()
	return "OK\n", nil
}

func (r *recorder) on(op string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.sent {
		if strings.HasPrefix(s[1], "DENY_ACL "+op+"_MAC ") {
			out = append(out, s[0])
		}
	}
	return out
}

// stubRadios points the three seams at a fixed picture of the box: every radio
// in `reachable` is serving, every radio in `present` exists but is not.
func stubRadios(t *testing.T, r *recorder, reachable, present []string) {
	t.Helper()
	oS, oR, oP := hostapdSend, hostapdReachable, linkPresent
	t.Cleanup(func() { hostapdSend, hostapdReachable, linkPresent = oS, oR, oP })

	has := func(list []string, w string) bool {
		for _, x := range list {
			if x == w {
				return true
			}
		}
		return false
	}
	hostapdSend = r.send
	hostapdReachable = func(w string) bool { return has(reachable, w) }
	linkPresent = func(w string) bool { return has(reachable, w) || has(present, w) }
}

func engineWith(mac, radio string, ports ...string) *Engine {
	return &Engine{
		cfg:          Config{WlanPorts: ports},
		stationRadio: map[string]string{mac: radio},
	}
}

// waitFor polls until cond holds or the deadline passes, so a slow machine does
// not fail a correct implementation and a fast one does not wait needlessly.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestDeadzoneScopeCurrentCoversOneRadio keeps today's behaviour under its new
// name: a deadzone that names no scope, or names "current", denies only where
// the client is -- which on a two-radio box is a forced roam, not an outage.
func TestDeadzoneScopeCurrentCoversOneRadio(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	for _, scope := range []string{"", ScopeCurrent} {
		r := &recorder{}
		stubRadios(t, r, []string{"wlan-usb", "wlan0"}, nil)
		e := engineWith(mac, "wlan-usb", "wlan-usb", "wlan0")

		if err := e.LinkDeadzone(mac, 1, scope); err != nil {
			t.Fatalf("scope %q: %v", scope, err)
		}
		if got := r.on("ADD"); len(got) != 1 || got[0] != "wlan-usb" {
			t.Errorf("scope %q denied on %v, want [wlan-usb] only", scope, got)
		}
		waitFor(t, "the lift", func() bool { return len(r.on("DEL")) == 1 })
		if got := r.on("DEL"); got[0] != "wlan-usb" {
			t.Errorf("scope %q lifted from %v, want wlan-usb", scope, got)
		}
	}
}

// TestDeadzoneScopeAllCoversEveryRadio is the point of the change: "all" must
// close the door the second radio leaves open, and must lift every ban it set.
func TestDeadzoneScopeAllCoversEveryRadio(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	r := &recorder{}
	stubRadios(t, r, []string{"wlan-usb", "wlan0"}, nil)
	e := engineWith(mac, "wlan-usb", "wlan-usb", "wlan0")

	if err := e.LinkDeadzone(mac, 1, ScopeAll); err != nil {
		t.Fatalf("LinkDeadzone: %v", err)
	}

	add := r.on("ADD")
	if len(add) != 2 {
		t.Fatalf("denied on %v, want both radios", add)
	}
	for _, want := range []string{"wlan-usb", "wlan0"} {
		found := false
		for _, w := range add {
			if w == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not denied, so the client can associate there and the "+
				"outage has a hole in it", want)
		}
	}

	waitFor(t, "the lift", func() bool { return len(r.on("DEL")) == 2 })
	if got := len(r.on("DEL")); got != 2 {
		t.Errorf("lifted %d bans, want 2 -- an unlifted ban strands the client (#205)", got)
	}
}

// TestDeadzoneScopeAllRefusesAHole covers the case the strictness exists for: a
// radio that is present but whose control socket is missing cannot be denied
// on, so an "all" deadzone across it would read as total and deliver a roam.
func TestDeadzoneScopeAllRefusesAHole(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	r := &recorder{}
	stubRadios(t, r, []string{"wlan-usb"}, []string{"wlan0"})
	e := engineWith(mac, "wlan-usb", "wlan-usb", "wlan0")

	err := e.LinkDeadzone(mac, 1, ScopeAll)
	if err == nil {
		t.Fatal("LinkDeadzone(all) = nil, want a refusal naming the radio it cannot cover")
	}
	if !strings.Contains(err.Error(), "wlan0") {
		t.Errorf("error does not name the uncoverable radio: %v", err)
	}
	// And nothing may be left behind by a refusal.
	if got := r.on("ADD"); len(got) != 0 {
		t.Errorf("a refused deadzone still applied bans on %v", got)
	}
}

// TestDeadzoneRejectsUnknownScope stops a typo silently becoming "current",
// which would be a total outage quietly downgraded to a roam.
func TestDeadzoneRejectsUnknownScope(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	r := &recorder{}
	stubRadios(t, r, []string{"wlan-usb"}, nil)
	e := engineWith(mac, "wlan-usb", "wlan-usb")

	if err := e.LinkDeadzone(mac, 1, "everywhere"); err == nil {
		t.Error("LinkDeadzone(scope=everywhere) = nil, want an error")
	}
	if got := r.on("ADD"); len(got) != 0 {
		t.Errorf("an invalid scope still applied bans on %v", got)
	}
}

// TestValidPatternChecksDeadzoneScope keeps a bad scope out of a saved pattern,
// where it would fail later and further from the person who typed it.
func TestValidPatternChecksDeadzoneScope(t *testing.T) {
	base := func(ev LinkEvent) Pattern {
		return Pattern{
			Name:  "p",
			Keys:  []Keyframe{{AtSec: 0, Ease: EaseHold}, {AtSec: 60, Ease: EaseHold}},
			Links: []LinkEvent{ev},
		}
	}
	ok := []LinkEvent{
		{AtSec: 10, Kind: LinkDeadzone, DurSec: 5},
		{AtSec: 10, Kind: LinkDeadzone, DurSec: 5, Scope: ScopeCurrent},
		{AtSec: 10, Kind: LinkDeadzone, DurSec: 5, Scope: ScopeAll},
	}
	for _, ev := range ok {
		if err := validPattern(base(ev)); err != nil {
			t.Errorf("scope %q rejected: %v", ev.Scope, err)
		}
	}

	bad := []LinkEvent{
		{AtSec: 10, Kind: LinkDeadzone, DurSec: 5, Scope: "both"},
		// Scope is meaningless on a pulse, and accepting it silently would
		// promise something the runtime does not do.
		{AtSec: 10, Kind: LinkDrop, Scope: ScopeAll},
	}
	for _, ev := range bad {
		if err := validPattern(base(ev)); err == nil {
			t.Errorf("%s with scope %q was accepted, want an error", ev.Kind, ev.Scope)
		}
	}
}
