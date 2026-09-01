package boa

import "testing"

// SuperSpeed is what the interface uses to say "this adapter is running at the
// speed it was sold at". The USB 2.0 case is the one worth pinning: it is a
// working adapter by every other measure, so nothing else in the system
// distinguishes it.
func TestSuperSpeed(t *testing.T) {
	cases := []struct {
		name string
		in   RadioInfo
		want bool
	}{
		{"usb3 negotiated", RadioInfo{Bus: "usb", LinkMbps: 5000}, true},
		{"usb3 gen2 negotiated", RadioInfo{Bus: "usb", LinkMbps: 10000}, true},
		{"usb3 adapter that fell back to high-speed", RadioInfo{Bus: "usb", LinkMbps: 480}, false},
		{"onboard is not slow, it is not on the bus", RadioInfo{Bus: "onboard"}, false},
		{"unknown speed is not claimed as fast", RadioInfo{Bus: "usb"}, false},
	}
	for _, c := range cases {
		if got := c.in.SuperSpeed(); got != c.want {
			t.Errorf("%s: SuperSpeed()=%v want %v", c.name, got, c.want)
		}
	}
}

// A missing interface must not look like a USB adapter, and must not panic:
// the AP interface can legitimately be absent while an adapter is unplugged.
func TestRadioMissingInterface(t *testing.T) {
	got := Radio("definitely-not-an-interface")
	if got.Bus != "onboard" {
		t.Errorf("Bus = %q, want onboard for a missing interface", got.Bus)
	}
	if got.SuperSpeed() {
		t.Error("a missing interface must not report SuperSpeed")
	}
	if got.Iface != "definitely-not-an-interface" {
		t.Errorf("Iface = %q, want it echoed back", got.Iface)
	}
}

func TestRadioEmptyName(t *testing.T) {
	if got := Radio(""); got.Bus != "onboard" || got.Driver != "" {
		t.Errorf("Radio(%q) = %+v, want an empty onboard result", "", got)
	}
}
