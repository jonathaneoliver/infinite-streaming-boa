package boa

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

/*
 * #442: a scan on a single-band radio listed frequencies the radio did not
 * have, `iw` rejected the whole scan with EINVAL, and the fallback took the
 * access point down to retry the same bad list.
 *
 * The fixtures are `iw phy <phy> info` from the two halves of the MT7915E on
 * target 5, 2026-09-29, US domain, reduced to the band and frequency lines.
 */

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A 5GHz-only phy scans 5GHz only. Every 2.4GHz entry in the fixed list is
// a frequency this phy does not have, and any one of them fails the scan.
func TestAFiveGigahertzPhyScansOnlyFiveGigahertz(t *testing.T) {
	freqs, chans := scanPlan(phyEnabledMHz(readFixture(t, "iw-phy-info-mt7915e-5g-us.txt")))

	wantChans := []int{36, 40, 44, 48, 149, 153, 157, 161, 165}
	if !reflect.DeepEqual(chans, wantChans) {
		t.Errorf("channels = %v, want %v", chans, wantChans)
	}
	wantFreqs := []string{"5180", "5200", "5220", "5240", "5745", "5765", "5785", "5805", "5825"}
	if !reflect.DeepEqual(freqs, wantFreqs) {
		t.Errorf("freqs = %v, want %v", freqs, wantFreqs)
	}
}

// A 2.4GHz phy in the US has 12, 13 and 14 disabled. 12 and 13 are in the
// fixed list, and measured on the box, `scan freq 2467` alone fails -22 -- so
// before this every 2.4GHz scan in such a domain was a bad request.
func TestAChannelDisabledByTheDomainIsNotScanned(t *testing.T) {
	_, chans := scanPlan(phyEnabledMHz(readFixture(t, "iw-phy-info-mt7915e-2g-us.txt")))

	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	if !reflect.DeepEqual(chans, want) {
		t.Errorf("channels = %v, want %v -- 12 and 13 are disabled in this domain", chans, want)
	}
}

// No IR forbids transmitting first, not listening: `scan freq 5845` on a
// no-IR channel succeeded on the box. So it stays in the enabled set.
func TestANoIRChannelIsStillScannable(t *testing.T) {
	enabled := phyEnabledMHz(readFixture(t, "iw-phy-info-mt7915e-5g-us.txt"))
	if !enabled[5845] {
		t.Error("channel 169 (5845 MHz, no IR) was dropped; a scan may listen there")
	}
	if enabled[2412] {
		t.Error("a 5GHz-only phy reported 2412 MHz as enabled")
	}
}

// What the scan records as having LISTENED to is the same narrowed list, so a
// 5GHz radio does not claim to have visited 2.4GHz and heard nothing there.
func TestTheRecordOfWhatWasScannedIsPerPhy(t *testing.T) {
	info := readFixture(t, "iw-phy-info-mt7915e-5g-us.txt")
	read := phyInfoFor
	phyInfoFor = func(string) (string, error) { return info, nil }
	t.Cleanup(func() { phyInfoFor = read })

	for _, ch := range scanLooked("phy1-ap0") {
		if ch <= 14 {
			t.Fatalf("a 5GHz-only radio records channel %d as scanned: %v", ch, scanLooked("phy1-ap0"))
		}
	}
}

// An unreadable phy falls back to the whole list, which is what every scan did
// before -- rather than scanning nothing.
func TestAnUnreadablePhyScansTheWholeList(t *testing.T) {
	read := phyInfoFor
	phyInfoFor = func(string) (string, error) { return "", errors.New("no such phy") }
	t.Cleanup(func() { phyInfoFor = read })

	freqs, chans := scanPlanFor("phy9-ap0")
	if len(chans) != len(scanChannels()) || len(freqs) != len(chans) {
		t.Errorf("got %d channels / %d freqs, want the whole list of %d", len(chans), len(freqs), len(scanChannels()))
	}
	if phyEnabledMHz("no channel lines here") != nil {
		t.Error("text with no channel lines read as a phy with nothing enabled, not as unreadable")
	}
}

// EINVAL is the request being wrong. It must not be read as "will not scan
// while serving", which is what took the access point down (#442).
func TestAnInvalidArgumentIsABadRequestNotARefusalToScanWhileServing(t *testing.T) {
	einval := errors.New("scan on phy1-ap0: command failed: Invalid argument (-22) (exit status 234)")
	if !badScanRequest(einval) {
		t.Error("EINVAL was not recognised as a bad request")
	}
	// The two refusals the fallback exists for, measured on the Pi, must
	// still reach it.
	for _, refusal := range []string{
		"command failed: Operation not supported (-95)", // mt7921u while beaconing
		"command failed: Network is down (-100)",        // brcmfmac with the BSS disabled
		"command failed: Device or resource busy (-16)",
	} {
		if badScanRequest(errors.New(refusal)) {
			t.Errorf("%q read as a bad request; the fallback would never be tried", refusal)
		}
	}
	if badScanRequest(nil) {
		t.Error("no error read as a bad request")
	}
}
