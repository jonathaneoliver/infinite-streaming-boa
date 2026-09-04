package boa

import (
	"strings"
	"testing"
)

// scanFixture is the shape `iw dev <if> scan` produces. Trimmed to the fields
// the parser reads, with the awkward cases kept: an AP whose BSSID line carries
// the interface glued on, a hidden SSID, and neighbours spread across
// overlapping 2.4GHz channels.
const scanFixture = `BSS aa:bb:cc:00:00:01(on wlan0)
	freq: 2412.0
	signal: -42.00 dBm
	SSID: NeighbourOne
	DS Parameter set: channel 1
BSS aa:bb:cc:00:00:02(on wlan0)
	freq: 2437.0
	signal: -71.00 dBm
	SSID: NeighbourTwo
	DS Parameter set: channel 6
BSS aa:bb:cc:00:00:03(on wlan0)
	freq: 2437.0
	signal: -80.00 dBm
	SSID:
	DS Parameter set: channel 6
BSS aa:bb:cc:00:00:04(on wlan0)
	freq: 2462.0
	signal: -55.00 dBm
	SSID: NeighbourFour
	DS Parameter set: channel 11
BSS 9c:ef:d5:f6:3f:f2(on wlan0)
	freq: 2437.0
	signal: -20.00 dBm
	SSID: infinite-streaming-boa
	DS Parameter set: channel 6
`

func TestParseScanReadsEveryAccessPoint(t *testing.T) {
	aps := parseScan(scanFixture)
	if len(aps) != 5 {
		t.Fatalf("want 5 access points, got %d", len(aps))
	}
	// The BSSID line is "BSS <mac>(on <iface>)" -- the interface is glued on
	// with no space, so a naive field split carries it into the MAC.
	if aps[0].BSSID != "aa:bb:cc:00:00:01" {
		t.Errorf("interface suffix not stripped: %q", aps[0].BSSID)
	}
	// `iw scan` prints the frequency as a FLOAT -- "freq: 2412.0" -- while
	// `survey dump` prints it as an integer. Parsing it with a plain Atoi
	// yielded 0 for every access point, and bandOf(0) is "2.4GHz", so the
	// band filter silently excluded nothing.
	if aps[0].Channel != 1 || aps[0].FreqMHz != 2412 {
		t.Errorf("wrong channel/freq: %+v", aps[0])
	}
	if aps[0].SignalDBm != -42 {
		t.Errorf("signal should be negative dBm, got %v", aps[0].SignalDBm)
	}
	// A hidden network still occupies the channel, so it must be counted even
	// though it has no name to show.
	if aps[2].SSID != "" || aps[2].Channel != 6 {
		t.Errorf("hidden SSID should still parse with its channel: %+v", aps[2])
	}
}

func TestOurOwnAPIsNotCountedAsCompetition(t *testing.T) {
	// The radio doing the scanning hears itself. Counting that would make the
	// channel it is already on look permanently the busiest, so the scan would
	// recommend moving every single time it was run.
	aps := parseScan(scanFixture)
	for i := range aps {
		aps[i].Ours = aps[i].BSSID == "9c:ef:d5:f6:3f:f2"
	}
	res := summariseScan("wlan0", "2.4GHz", aps)
	for _, c := range res.Channels {
		if c.Channel == 6 && c.APs != 2 {
			t.Errorf("channel 6 has 2 neighbours plus our own; got %d", c.APs)
		}
	}
}

func TestScanSummaryDropsOtherBands(t *testing.T) {
	// Both chips are dual-band and scan BOTH, so a 2.4GHz radio's scan returns
	// 5GHz neighbours -- measured on hardware, a scan from wlan0 listed a
	// channel 40 access point. Those rows mean nothing in a 2.4GHz table: the
	// radio cannot move there, and a recommendation can never use them.
	aps := []ScanAP{
		{BSSID: "aa:00:00:00:00:01", FreqMHz: 2412, Channel: 1, SignalDBm: -40},
		{BSSID: "aa:00:00:00:00:02", FreqMHz: 5200, Channel: 40, SignalDBm: -71},
	}
	res := summariseScan("wlan0", "2.4GHz", aps)
	for _, c := range res.Channels {
		if c.Channel > 14 {
			t.Errorf("5GHz channel %d listed in a 2.4GHz scan", c.Channel)
		}
	}
	if len(res.Channels) != 1 {
		t.Errorf("want only the 2.4GHz channel, got %+v", res.Channels)
	}
}

func TestBestChannelStaysInTheScannedBand(t *testing.T) {
	// The bug this guards: a 5GHz radio being told to move to channel 1. A scan
	// of one band says nothing about the other, and the radio may not even be
	// able to go there.
	chans := []ScanChannel{{Channel: 36, FreqMHz: 5180, APs: 4}}
	got := pickBestChannel(chans, "5GHz")
	if got <= 14 {
		t.Fatalf("5GHz scan recommended channel %d, which is 2.4GHz", got)
	}
	got24 := pickBestChannel([]ScanChannel{{Channel: 1, FreqMHz: 2412, APs: 4}}, "2.4GHz")
	if got24 > 14 {
		t.Fatalf("2.4GHz scan recommended channel %d, which is 5GHz", got24)
	}
}

func TestBestChannelOnlyEverPicksNonOverlapping(t *testing.T) {
	// Channel 3 is empty in this fixture and is still the wrong answer: it
	// overlaps 1 and 6, taking interference from both and giving it back.
	chans := []ScanChannel{
		{Channel: 1, FreqMHz: 2412, APs: 3, StrongestDBm: -40},
		{Channel: 6, FreqMHz: 2437, APs: 1, StrongestDBm: -80},
		{Channel: 11, FreqMHz: 2462, APs: 4, StrongestDBm: -50},
	}
	got := pickBestChannel(chans, "2.4GHz")
	if _, ok := apChannels[got]; !ok {
		t.Fatalf("recommended channel %d is not one the AP may use", got)
	}
	if got != 1 && got != 6 && got != 11 {
		t.Fatalf("recommended %d; only 1/6/11 are non-overlapping", got)
	}
}

func TestBestChannelCountsOverlapOn24GHz(t *testing.T) {
	// Everything sits on 4 and 5, which are nominally "empty" channels but
	// overlap 6 heavily. A picker matching channels exactly would see 6 as
	// empty and recommend it; counting the overlap window sees the truth.
	chans := []ScanChannel{
		{Channel: 4, FreqMHz: 2427, APs: 5, StrongestDBm: -40},
		{Channel: 5, FreqMHz: 2432, APs: 5, StrongestDBm: -42},
	}
	if got := pickBestChannel(chans, "2.4GHz"); got == 6 {
		t.Error("channel 6 overlaps the crowded 4 and 5; it is not the quiet choice")
	}
}

func TestChannelForFreqCoversBothBands(t *testing.T) {
	tests := []struct{ mhz, ch int }{
		{2412, 1}, {2437, 6}, {2462, 11}, {2484, 14},
		{5180, 36}, {5200, 40}, {5220, 44}, {5240, 48},
		{1, 0}, {9999, 0}, // nothing plausible, and must not guess
	}
	for _, tc := range tests {
		if got := channelForFreq(tc.mhz); got != tc.ch {
			t.Errorf("channelForFreq(%d) = %d, want %d", tc.mhz, got, tc.ch)
		}
	}
}

func TestSetChannelCommandsAgreeWithThemselves(t *testing.T) {
	// hostapd rejects the whole ENABLE if the primary channel, the side its
	// 40MHz secondary sits and the centre of the 80MHz block contradict each
	// other -- with an error about the kernel driver that says nothing about
	// the cause. These three have to be built together or not at all.
	got := strings.Join(setChannelCommands(apChannels[36], 80), " | ")
	for _, want := range []string{
		"SET channel 36",
		"SET ht_capab [HT40+]", // 36 sits below its partner
		"SET vht_oper_chwidth 1",
		"SET vht_oper_centr_freq_seg0_idx 42", // 5210MHz == channel 42
		"SET he_oper_centr_freq_seg0_idx 42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}

	// 40 sits ABOVE its partner, so the secondary is below it. Naming the wrong
	// side is the same failure as naming none.
	if !strings.Contains(strings.Join(setChannelCommands(apChannels[40], 80), " "),
		"SET ht_capab [HT40-]") {
		t.Error("channel 40's secondary must be below the primary")
	}
	// secondary_channel is DERIVED state, reported in STATUS and refused by
	// SET. Measured: it is the single parameter of these that hostapd rejects,
	// and sending it aborted the rest of a channel move half way.
	for _, ch := range []int{6, 36, 40} {
		for _, c := range setChannelCommands(apChannels[ch], 80) {
			if strings.Contains(c, "secondary_channel") {
				t.Errorf("ch %d: hostapd refuses SET secondary_channel: %q", ch, c)
			}
		}
	}
}

func TestMovingDownTo20MHzClearsTheWideSettings(t *testing.T) {
	// A radio dropping from 80MHz to a 20MHz channel must not keep a centre
	// frequency describing a block that no longer contains it -- hostapd would
	// refuse to come back up, leaving the AP down after a scan.
	got := strings.Join(setChannelCommands(apChannels[6], 20), " | ")
	for _, want := range []string{
		"SET channel 6",
		"SET vht_oper_chwidth 0",
		"SET he_oper_chwidth 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, "centr_freq") {
		t.Errorf("20MHz must not carry a centre frequency: %s", got)
	}
}

func TestParseScanSurvivesEmptyAndTruncatedOutput(t *testing.T) {
	for _, raw := range []string{
		"",
		"\n\n",
		"	freq: 2412\n", // no BSS header to attach to
		"BSS aa:bb:cc:00:00:01(on wlan0)\n",
	} {
		got := parseScan(raw)
		// The last case is a header with no fields: it is still a real AP
		// sighting, just an empty one, so it parses to one entry.
		if len(got) > 1 {
			t.Errorf("input %q produced %d entries", raw, len(got))
		}
	}
}
