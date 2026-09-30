package boa

import (
	"errors"
	"reflect"
	"testing"
)

/*
 * A move to a channel the radio does not have. Forcing the MT7915E's 2.4 GHz
 * phy0 onto channel 36 was accepted, took the access point down, and it never
 * came back -- a 3.5-minute request to find out. The fixtures are the two
 * phys' real `iw phy info` from target 5, US domain.
 */

func fakePhyInfo(t *testing.T, info string, err error) {
	t.Helper()
	orig := phyInfoFor
	phyInfoFor = func(string) (string, error) { return info, err }
	t.Cleanup(func() { phyInfoFor = orig })
}

func TestASingleBandRadioCannotServeTheOtherBand(t *testing.T) {
	fakePhyInfo(t, readFixture(t, "iw-phy-info-mt7915e-2g-us.txt"), nil)

	serves, known, bands := radioServes("phy0-ap0", 5180) // channel 36
	if !known {
		t.Fatal("a readable phy was reported as unknown")
	}
	if serves {
		t.Error("the 2.4 GHz-only phy was allowed onto 5 GHz channel 36")
	}
	if !reflect.DeepEqual(bands, []string{"2.4GHz"}) {
		t.Errorf("bands = %v, want [2.4GHz] -- the error names what the radio CAN serve", bands)
	}
	if serves, _, _ := radioServes("phy0-ap0", 2437); !serves {
		t.Error("the 2.4 GHz phy was refused its own channel 6")
	}
}

func TestTheFiveGigahertzHalfServesItsOwnBand(t *testing.T) {
	fakePhyInfo(t, readFixture(t, "iw-phy-info-mt7915e-5g-us.txt"), nil)

	if serves, _, _ := radioServes("phy1-ap0", 5745); !serves { // 149
		t.Error("the 5 GHz phy was refused channel 149")
	}
	if serves, _, _ := radioServes("phy1-ap0", 2412); serves {
		t.Error("the 5 GHz-only phy was allowed onto 2.4 GHz channel 1")
	}
}

// No IR splits the two uses of the same parse: a scan may listen there (#442),
// an access point may not start there.
func TestANoIRChannelIsScannableButNotServable(t *testing.T) {
	info := readFixture(t, "iw-phy-info-mt7915e-5g-us.txt")
	if !phyEnabledMHz(info)[5845] {
		t.Error("channel 169 (no IR) was dropped from the scan list")
	}
	if phyServableMHz(info)[5845] {
		t.Error("channel 169 (no IR) was offered to an access point, which must transmit first")
	}
}

// An unreadable phy does not refuse the move: that would block every move on a
// box where `iw` is missing or slow, for a check that exists to save one.
func TestAnUnreadablePhyDoesNotRefuseTheMove(t *testing.T) {
	fakePhyInfo(t, "", errors.New("iw: command not found"))
	if _, known, _ := radioServes("phy0-ap0", 5180); known {
		t.Error("an unreadable phy was reported as known, so the move would be refused on a guess")
	}
	fakePhyInfo(t, "no channel lines here", nil)
	if _, known, _ := radioServes("phy0-ap0", 5180); known {
		t.Error("output with no channel lines was read as a phy with nothing to serve")
	}
}

func TestBandListReadsAsASentence(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"2.4GHz"}, "2.4GHz only"},
		{[]string{"2.4GHz", "5GHz"}, "2.4GHz and 5GHz"},
		{nil, "no channel at all"},
	} {
		if got := bandList(c.in); got != c.want {
			t.Errorf("bandList(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
