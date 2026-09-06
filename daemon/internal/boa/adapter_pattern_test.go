package boa

import (
	"strings"
	"testing"
	"time"
)

func adapterPattern(evs ...RadioEvent) Pattern {
	return Pattern{
		Name:   "__adapter__",
		Keys:   []Keyframe{{AtSec: 0, Ease: EaseHold}, {AtSec: 180, Ease: EaseHold}},
		Radios: evs,
		Loop:   true,
	}
}

func TestAdapterPatternValidates(t *testing.T) {
	ok := []RadioEvent{
		{AtSec: 10, Iface: "wlan0", Kind: RadioGather},
		{AtSec: 20, Iface: "wlan0", Kind: RadioEvict},
		{AtSec: 30, Iface: "wlan0", Kind: RadioDeauth},
		{AtSec: 40, Iface: "wlan0", Kind: RadioOff, DurSec: minRadioOffSec},
	}
	if err := validPattern(adapterPattern(ok...)); err != nil {
		t.Fatalf("a valid adapter pattern was rejected: %v", err)
	}

	bad := []struct {
		name string
		ev   RadioEvent
		want string
	}{
		{"no radio", RadioEvent{AtSec: 1, Kind: RadioOff, DurSec: 60}, "no radio named"},
		{"unknown kind", RadioEvent{AtSec: 1, Iface: "wlan0", Kind: "wobble"}, "unknown kind"},
		{"pulse with a duration", RadioEvent{AtSec: 1, Iface: "wlan0", Kind: RadioDeauth, DurSec: 5}, "takes no duration"},
		{"outage with none", RadioEvent{AtSec: 1, Iface: "wlan0", Kind: RadioOff}, "shorter than"},
	}
	for _, c := range bad {
		err := validPattern(adapterPattern(c.ev))
		if err == nil {
			t.Errorf("%s: accepted, want an error", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.want)
		}
	}
}

// TestOutageFloorIsExplained -- the floor is the point, not a formality, so the
// refusal has to say why and what to use instead.
func TestOutageFloorIsExplained(t *testing.T) {
	err := validPattern(adapterPattern(
		RadioEvent{AtSec: 10, Iface: "wlan0", Kind: RadioOff, DurSec: 10},
	))
	if err == nil {
		t.Fatal("a 10s outage was accepted")
	}
	for _, want := range []string{"announces nothing", RadioDeauth} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// TestAdapterRunFiresRadioEventsNotShapes -- a box run conditions no packets.
// Asking it for a Shape would enforce one against a key that is not a device.
func TestAdapterRunFiresRadioEventsNotShapes(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	pat := adapterPattern(
		RadioEvent{AtSec: 5, Iface: "wlan-usb", Kind: RadioGather},
		RadioEvent{AtSec: 20, Iface: "wlan-usb", Kind: RadioOff, DurSec: 40},
	)
	if err := p.Start(BoxBinding, pat, t0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	links, radios := p.Advance(t0.Add(10 * time.Second))
	if len(links) != 0 {
		t.Errorf("a box run produced %d link fires, want none", len(links))
	}
	if len(radios) != 1 || radios[0].Kind != RadioGather {
		t.Fatalf("radio fires = %+v, want one gather", radios)
	}
	if radios[0].Iface != "wlan-usb" {
		t.Errorf("fired on %q, want wlan-usb -- the lane IS the target", radios[0].Iface)
	}

	// And the box run never offers an Override, so nothing tries to shape it.
	if _, _, ok := p.Override(BoxBinding); ok {
		t.Error("a box run offered a conditioning override; it drives radios, not packets")
	}

	_, radios = p.Advance(t0.Add(25 * time.Second))
	if len(radios) != 1 || radios[0].Kind != RadioOff || radios[0].DurSec != 40 {
		t.Errorf("radio fires = %+v, want one 40s off", radios)
	}
}

// TestBoxBindingIsNotAMAC keeps the reserved key from ever colliding with a
// device, which is the whole reason a second map was not needed.
func TestBoxBindingIsNotAMAC(t *testing.T) {
	if validMAC(BoxBinding) {
		t.Errorf("%q passes validMAC; it could collide with a device", BoxBinding)
	}
}

// TestAdapterPatternSharesAClockWithDevices is why #218 came first: the whole
// point of a radio lane is that its outage lands at a known point in a client's
// ladder walk.
func TestAdapterPatternSharesAClockWithDevices(t *testing.T) {
	p := &Player{}
	t0 := time.Now()
	client := Pattern{
		Name: "c",
		Keys: []Keyframe{
			{AtSec: 0, Down: Shape{RateMbps: 8}, Ease: EaseHold},
			{AtSec: 180, Down: Shape{RateMbps: 8}, Ease: EaseHold},
		},
		Loop: true,
	}
	err := p.StartGroup("scn", map[string]Pattern{
		BoxBinding: adapterPattern(RadioEvent{AtSec: 60, Iface: "wlan0", Kind: RadioDeauth}),
		macA:       client,
	}, t0)
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}

	p.Advance(t0.Add(30 * time.Second))
	box, dev := p.View(BoxBinding), p.View(macA)
	if box == nil || dev == nil {
		t.Fatal("both members should have a run")
	}
	if box.PosSec != dev.PosSec {
		t.Errorf("the radio lane and the device lane diverged: %v vs %v",
			box.PosSec, dev.PosSec)
	}
	if box.Group != dev.Group {
		t.Errorf("members are in different groups: %q vs %q", box.Group, dev.Group)
	}
}
