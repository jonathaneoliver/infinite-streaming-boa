package boa

import "testing"

func TestLinkCommand(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"deauth no reason", deauthCommand("aa:bb:cc:dd:ee:ff", 0), "DEAUTHENTICATE aa:bb:cc:dd:ee:ff"},
		{"deauth reason 5", deauthCommand("aa:bb:cc:dd:ee:ff", 5), "DEAUTHENTICATE aa:bb:cc:dd:ee:ff reason=5"},
		{"disassoc no reason", disassocCommand("aa:bb:cc:dd:ee:ff", 0), "DISASSOCIATE aa:bb:cc:dd:ee:ff"},
		{"disassoc reason 8", disassocCommand("aa:bb:cc:dd:ee:ff", 8), "DISASSOCIATE aa:bb:cc:dd:ee:ff reason=8"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestValidMAC(t *testing.T) {
	good := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55", "fc:9c:a7:93:7f:ed"}
	for _, m := range good {
		if !validMAC(m) {
			t.Errorf("validMAC(%q) = false, want true", m)
		}
	}
	// Reject anything that is not a canonical lower-case MAC -- especially
	// values that could smuggle a second word into the control message.
	bad := []string{
		"AA:BB:CC:DD:EE:FF",            // upper case: normMAC lowers first, but the raw form must not pass
		"aa:bb:cc:dd:ee",               // too short
		"aa:bb:cc:dd:ee:ff:00",         // too long
		"aa:bb:cc:dd:ee:ff DEAUTH all", // injection attempt
		"aa:bb:cc:dd:ee:ff\nATTACH",    // newline injection
		"",                             // empty
		"zz:bb:cc:dd:ee:ff",            // non-hex
	}
	for _, m := range bad {
		if validMAC(m) {
			t.Errorf("validMAC(%q) = true, want false", m)
		}
	}
}

func TestHostapdSocket(t *testing.T) {
	if got, want := hostapdSocket("wlan-usb"), "/var/run/hostapd/wlan-usb"; got != want {
		t.Errorf("hostapdSocket = %q, want %q", got, want)
	}
}

func TestHostapdAvailableEmpty(t *testing.T) {
	if hostapdAvailable("") {
		t.Error("hostapdAvailable(\"\") = true, want false")
	}
}
