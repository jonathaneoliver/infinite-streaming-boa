package boa

import "testing"

// The two block kinds have DIFFERENT floors, and the difference is the point: a
// silent outage has to outlast a client's beacon timeout, an announced one only
// has to outlast hostapd putting the BSS back.
func TestAPDownHasItsOwnFloor(t *testing.T) {
	// 15s is far below minRadioOffSec and must still be accepted, because this
	// is the block the ping-pong preset is built from.
	p := Pattern{
		Name: "t",
		Keys: []Keyframe{{AtSec: 0}, {AtSec: 60}},
		Radios: []RadioEvent{
			{AtSec: 0, Iface: "wlan0", Kind: RadioAPDown, DurSec: 15},
		},
	}
	if err := validPattern(p); err != nil {
		t.Fatalf("a 15s access-point block was refused: %v", err)
	}

	// The same duration as a POWER cut is refused, unchanged.
	p.Radios[0].Kind = RadioOff
	if err := validPattern(p); err == nil {
		t.Fatal("a 15s silent outage was accepted; the floor is the point")
	}
}

// Below the floor hostapd itself needs, the block is refused: it would ask for
// the access point back before it had finished leaving.
func TestAPDownBelowItsFloorIsRefused(t *testing.T) {
	p := Pattern{
		Name: "t",
		Keys: []Keyframe{{AtSec: 0}, {AtSec: 60}},
		Radios: []RadioEvent{
			{AtSec: 0, Iface: "wlan0", Kind: RadioAPDown, DurSec: 1},
		},
	}
	if err := validPattern(p); err == nil {
		t.Fatal("a 1s access-point block was accepted, shorter than the BSS takes to return")
	}
}

// scan is a pulse. Giving it a duration is a mistake worth naming rather than
// quietly ignoring, because the operator plainly meant something by it.
func TestScanIsAPulse(t *testing.T) {
	p := Pattern{
		Name: "t",
		Keys: []Keyframe{{AtSec: 0}, {AtSec: 60}},
		Radios: []RadioEvent{
			{AtSec: 0, Iface: "wlan0", Kind: RadioScan},
		},
	}
	if err := validPattern(p); err != nil {
		t.Fatalf("a scan pulse was refused: %v", err)
	}
	p.Radios[0].DurSec = 10
	if err := validPattern(p); err == nil {
		t.Fatal("a scan with a duration was accepted")
	}
}
