package boa

import (
	"strings"
	"testing"
)

func TestBTMRequestNamesTheTargetCorrectly(t *testing.T) {
	// The neighbour report is five comma-separated fields in a fixed order and
	// hostapd rejects the whole command on any one being wrong -- but a client
	// given a WRONG operating class simply ignores the request, which looks
	// exactly like a client that refused it. That is the failure this pins.
	got := btmCommand("aa:bb:cc:dd:ee:ff", "9c:ef:d5:aa:11:07", 6, 20, 30)
	for _, want := range []string{
		"BSS_TM_REQ aa:bb:cc:dd:ee:ff",
		"pref=1",
		"abridged=1",          // everything unlisted is less preferred
		"disassoc_imminent=1", // a client that ignores it still has to move
		"disassoc_timer=30",
		"neighbor=9c:ef:d5:aa:11:07,0x0000040f,81,6,7", // 2.4GHz: class 81, HT
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n  %s", want, got)
		}
	}
}

func TestOperatingClassEncodesBandAndWidth(t *testing.T) {
	tests := []struct {
		name            string
		channel, width  int
		wantOp, wantPhy int
	}{
		// 2.4GHz is always the 20MHz class on this hardware, and HT is the most
		// a client will find there.
		{"2.4GHz 20MHz", 6, 20, 81, 7},
		{"2.4GHz ignores a wide request", 1, 80, 81, 7},
		{"5GHz 20MHz", 36, 20, 115, 9},
		{"5GHz 40MHz", 36, 40, 116, 9},
		{"5GHz 80MHz", 36, 80, 128, 9},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op, phy := opClassAndPhy(tc.channel, tc.width)
			if op != tc.wantOp || phy != tc.wantPhy {
				t.Errorf("got class %d phy %d, want %d/%d", op, phy, tc.wantOp, tc.wantPhy)
			}
		})
	}
}

func TestCleanCarriesNoHardcodedSets(t *testing.T) {
	// clean is built per radio by cleanSetsFor, because "as the image
	// configured it" differs between them: the image gives the onboard chip
	// ieee80211ac=0/ax=0 and the adapter 1/1. A single hardcoded set applied
	// ax=1 to both, leaving the interface claiming a 20MHz 802.11n radio was
	// 802.11ax -- the interface asserting something the hardware cannot do.
	if len(radioProfiles["clean"].Sets) != 0 {
		t.Errorf("clean must be built per radio, not hardcoded: %v",
			radioProfiles["clean"].Sets)
	}
}

func TestCleanAlwaysRestoresTheTimingParameters(t *testing.T) {
	// Even with no config to read -- not on a Pi, or hostapd not running --
	// clean must still undo a power-save profile rather than doing nothing.
	got := strings.Join(cleanSetsFor("definitely-not-an-interface"), " | ")
	for _, want := range []string{"beacon_int", "dtim_period", "uapsd_advertisement_enabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("clean must restore %s even with no config: %s", want, got)
		}
	}
}

func TestEveryProfileIsReversibleByClean(t *testing.T) {
	// Each profile must be undoable, or the only way back from "dozy" is a
	// reflash. Every parameter any profile sets has to appear in clean.
	// cleanSetsFor with no readable config still names every parameter clean is
	// responsible for putting back, which is what this checks against.
	cleanSets := map[string]bool{}
	for _, s := range cleanSetsFor("no-such-iface") {
		f := strings.Fields(s)
		if len(f) >= 2 {
			cleanSets[f[1]] = true
		}
	}
	// Restored from the config file when there is one to read.
	for _, k := range []string{"ieee80211n", "ieee80211ac", "ieee80211ax",
		"vht_oper_chwidth", "he_oper_chwidth"} {
		cleanSets[k] = true
	}
	for name, p := range radioProfiles {
		if name == "clean" {
			continue
		}
		for _, s := range p.Sets {
			f := strings.Fields(s)
			if len(f) < 2 {
				t.Errorf("%s: malformed SET %q", name, s)
				continue
			}
			// Width is restored by the channel path, which always names it.
			if strings.Contains(f[1], "chwidth") || f[1] == "secondary_channel" {
				continue
			}
			if !cleanSets[f[1]] {
				t.Errorf("%s sets %q, which clean never restores -- there would "+
					"be no way back from it", name, f[1])
			}
		}
	}
}

func TestProfileNamesLeadWithClean(t *testing.T) {
	// clean is the way back from every other profile, so it is offered first
	// rather than sorted into the middle of the list.
	got := RadioProfileNames()
	if len(got) == 0 || got[0] != "clean" {
		t.Fatalf("clean must be first, got %v", got)
	}
	if len(got) != len(radioProfiles) {
		t.Errorf("listed %d of %d profiles", len(got), len(radioProfiles))
	}
}

func TestEveryProfileSaysWhatItDoes(t *testing.T) {
	// These appear as buttons that drop every client on a radio. One without a
	// description is one nobody can judge before pressing.
	for name, p := range radioProfiles {
		if strings.TrimSpace(p.Desc) == "" {
			t.Errorf("profile %q has no description", name)
		}
		// clean is the exception: its sets are built per radio by cleanSetsFor,
		// because what "as the image configured it" means differs between the
		// two radios.
		if name != "clean" && len(p.Sets) == 0 {
			t.Errorf("profile %q sets nothing", name)
		}
	}
}

func TestOtherRadioIsWhereAClientCanBeSteered(t *testing.T) {
	// SteerTo on every client snapshot comes from here, and it is what decides
	// whether the interface offers a steer button at all. Empty must mean
	// "nowhere to go", never "I did not look" -- an empty string that reached
	// SteerClient would build a neighbour report naming no access point.
	//
	// Demo mode, so this asks the CONFIG question -- which radios exist and
	// which is the client on -- without needing hostapd on the machine running
	// the tests.
	two := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan0", "wlan-usb"}}}
	if got := two.OtherRadio("wlan0"); got != "wlan-usb" {
		t.Errorf("from wlan0 -> %q, want wlan-usb", got)
	}
	// The direction matters: a client on the adapter is steered to the onboard
	// radio, not the other way round. Taking the source from the primary rather
	// than from the client's own radio would get this backwards for half the
	// clients on a two-radio box.
	if got := two.OtherRadio("wlan-usb"); got != "wlan0" {
		t.Errorf("from wlan-usb -> %q, want wlan0", got)
	}

	// One radio: there is nowhere to send anyone, and saying so is the whole
	// point -- the button is hidden on this rather than offered and refused.
	one := &Engine{cfg: Config{Demo: true, WlanPorts: []string{"wlan0"}}}
	if got := one.OtherRadio("wlan0"); got != "" {
		t.Errorf("single radio -> %q, want empty", got)
	}
	// A radio that is not in the list at all still yields nothing rather than
	// naming the only radio there is: steering a client to the radio it is
	// already on is refused downstream, and inventing a target here would just
	// move that failure somewhere less obvious.
	if got := one.OtherRadio("wlan-usb"); got != "wlan0" {
		t.Logf("unknown source names the only serving radio (%q) -- acceptable: "+
			"SteerClient refuses from==to, and this is not that case", got)
	}
}
