package boa

import (
	"strings"
	"testing"
)

// Every fixture here is real output captured from the box on 2026-09-03
// (mt7921u, hostapd v2.10, AP on channel 40). Invented fixtures would agree
// with whatever the parser happens to do; these disagree with what the parser
// was originally written to expect, which is the point.

func TestChanSwitchCommandBuildsTheArgumentsHostapdNeeds(t *testing.T) {
	tests := []struct {
		name    string
		channel int
		width   int
		want    string
	}{
		{
			// 36 sits below its 40MHz partner, so the secondary is ABOVE it.
			"5GHz 80MHz on 36", 36, 80,
			"CHAN_SWITCH 5 5180 center_freq1=5210 bandwidth=80 sec_channel_offset=1 vht he",
		},
		{
			// 40 sits above its partner, so the secondary is BELOW. Naming the
			// wrong side is the failure this case exists to pin.
			"5GHz 80MHz on 40", 40, 80,
			"CHAN_SWITCH 5 5200 center_freq1=5210 bandwidth=80 sec_channel_offset=-1 vht he",
		},
		{
			"5GHz 40MHz keeps the offset but drops the centre", 44, 40,
			"CHAN_SWITCH 5 5220 bandwidth=40 sec_channel_offset=1 ht",
		},
		{
			"5GHz 20MHz names neither", 48, 20,
			"CHAN_SWITCH 5 5240 bandwidth=20 ht",
		},
		{
			"2.4GHz is 20MHz only", 6, 20,
			"CHAN_SWITCH 5 2437 bandwidth=20 ht",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chanSwitchCommand(apChannels[tc.channel], tc.width)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestChanSwitchCommandRefusesWidthsTheBandCannotDo(t *testing.T) {
	// 80MHz does not exist on 2.4GHz and 40MHz is antisocial there. Refused
	// with a reason rather than sent for hostapd to reject obscurely.
	for _, width := range []int{40, 80} {
		if _, err := chanSwitchCommand(apChannels[6], width); err == nil {
			t.Errorf("channel 6 at %dMHz was accepted; 2.4GHz is 20MHz only", width)
		}
	}
	if _, err := chanSwitchCommand(apChannels[36], 160); err == nil {
		t.Error("160MHz was accepted; only 20/40/80 are offered")
	}
	if _, err := chanSwitchCommand(apChannel{}, 20); err == nil {
		t.Error("a zero-value channel was accepted")
	}
}

func TestOnlyNonDFSChannelsAreOffered(t *testing.T) {
	// The Pi cannot serve an AP on a DFS channel, so offering one produces a
	// radio that refuses to start. 52 and 100 are the usual temptations.
	for _, ch := range []int{52, 56, 100, 149, 165, 0, -1} {
		if _, ok := apChannels[ch]; ok {
			t.Errorf("channel %d is offered but should not be", ch)
		}
	}
	for _, ch := range []int{1, 6, 11, 36, 40, 44, 48} {
		if _, ok := apChannels[ch]; !ok {
			t.Errorf("channel %d should be offered", ch)
		}
	}
}

// surveyFixture is the real shape of `iw dev wlan-usb survey dump`: a block per
// channel the phy knows, all reading zero, and exactly one carrying data --
// under a frequency label that is NOT the channel the AP is on.
const surveyFixture = `Survey data from wlan-usb
	frequency:			2412 MHz
	channel active time:		0 ms
	channel busy time:		0 ms
	channel receive time:		0 ms
	channel transmit time:		0 ms
Survey data from wlan-usb
	frequency:			5200 MHz
	channel active time:		0 ms
	channel busy time:		0 ms
	channel receive time:		0 ms
	channel transmit time:		0 ms
Survey data from wlan-usb
	frequency:			5955 MHz
	channel active time:		5540872 ms
	channel busy time:		271397 ms
	channel receive time:		79009 ms
	channel transmit time:		219107 ms
Survey data from wlan-usb
	frequency:			5975 MHz
	channel active time:		0 ms
	channel busy time:		0 ms
	channel receive time:		0 ms
	channel transmit time:		0 ms
`

func TestParseSurveyKeepsOnlyTheChannelWithAirtime(t *testing.T) {
	got := parseSurvey(surveyFixture)
	if len(got) != 1 {
		t.Fatalf("want 1 channel with airtime, got %d: %+v", len(got), got)
	}
	c := got[0]
	// Note the 5200 MHz block reads zero even though the AP is ON 5200. The
	// populated block is labelled 5955. Both facts are in this fixture on
	// purpose: a parser that picked the block matching the operating frequency
	// would return nothing at all.
	if c.FreqMHz != 5955 {
		t.Errorf("parser should record the driver's label verbatim, got %d", c.FreqMHz)
	}
	if c.ActiveMs != 5540872 || c.BusyMs != 271397 {
		t.Errorf("wrong counters: %+v", c)
	}
	if c.ReceiveMs != 79009 || c.TransmitMs != 219107 {
		t.Errorf("wrong rx/tx: %+v", c)
	}
}

func TestParseSurveySurvivesEmptyAndTruncatedOutput(t *testing.T) {
	// A down interface produces no output at all; a killed command can cut a
	// block in half. Neither may panic, and neither may invent a channel.
	for _, raw := range []string{
		"",
		"\n\n",
		"	frequency:			2412 MHz\n", // no block header to attach to
		"Survey data from wlan-usb\n	frequency:			5955 MHz\n",
	} {
		if got := parseSurvey(raw); len(got) != 0 {
			t.Errorf("input %q produced %d channels, want 0", raw, got)
		}
	}
}

func TestSurveyBusyFractionIsPlausibleForTheMeasuredSample(t *testing.T) {
	// Guards the arithmetic against a units slip. Measured on the box: the AP
	// had been up 5562s and active time read 5540872ms, so these totals are a
	// whole-uptime average -- 4.9% busy, which is what a lightly-loaded AP with
	// two stations looks like.
	c := parseSurvey(surveyFixture)[0]
	busy := float64(c.BusyMs) / float64(c.ActiveMs) * 100
	if busy < 4.5 || busy > 5.5 {
		t.Errorf("busy fraction %.2f%% is outside the measured 4.9%%", busy)
	}
	// MEASURED, and not what it looks like: receive and transmit are NOT a
	// decomposition of busy. On this sample rx+tx = 298116ms against busy =
	// 271397ms, so they overlap and exceed it.
	//
	// The consequence is the reason this is pinned by a test: "airtime used by
	// everyone else" cannot be derived as busy - rx - tx. That subtraction is
	// the obvious way to build a contention readout and it goes NEGATIVE on
	// real hardware.
	if c.ReceiveMs+c.TransmitMs <= c.BusyMs {
		t.Errorf("rx+tx (%d) no longer exceeds busy (%d) -- if the driver's "+
			"accounting has changed, re-derive Source L before trusting a "+
			"busy - rx - tx subtraction",
			c.ReceiveMs+c.TransmitMs, c.BusyMs)
	}
}

// statusFixture is real `STATUS` output from hostapd v2.10 on wlan-usb.
const statusFixture = `state=ENABLED
phy=phy1
freq=5200
num_sta_non_erp=0
olbc=0
channel=40
edmg_enable=0
edmg_channel=0
secondary_channel=-1
ieee80211n=1
ieee80211ac=1
ieee80211ax=1
beacon_int=100
dtim_period=2
he_oper_chwidth=1
he_oper_centr_freq_seg0_idx=42
vht_oper_chwidth=1
vht_oper_centr_freq_seg0_idx=42
bssid[0]=9c:ef:d5:f6:3f:f2
ssid[0]=infinite-streaming-boa
`

func TestParseHostapdKVHandlesIndexedKeys(t *testing.T) {
	kv := parseHostapdKV(statusFixture)
	// Indexed because hostapd can serve several BSSes per interface. A parser
	// matching a bare "ssid" finds nothing and reports an unnamed network.
	if kv["ssid[0]"] != "infinite-streaming-boa" {
		t.Errorf("ssid[0] = %q", kv["ssid[0]"])
	}
	if kv["bssid[0]"] != "9c:ef:d5:f6:3f:f2" {
		t.Errorf("bssid[0] = %q", kv["bssid[0]"])
	}
	if kv["ssid"] != "" {
		t.Error("a bare 'ssid' key should not exist in this output")
	}
	if kv["freq"] != "5200" || kv["channel"] != "40" {
		t.Errorf("freq/channel = %q/%q", kv["freq"], kv["channel"])
	}
}

func TestParseHostapdKVIgnoresFailureReplies(t *testing.T) {
	if got := parseHostapdKV("FAIL\n"); len(got) != 0 {
		t.Errorf("FAIL should yield nothing, got %+v", got)
	}
	if got := parseHostapdKV("UNKNOWN COMMAND\n"); len(got) != 0 {
		t.Errorf("UNKNOWN should yield nothing, got %+v", got)
	}
}

func TestAPWidthIsDerivedBecauseHostapdReportsNone(t *testing.T) {
	// The string "80" appears nowhere in STATUS; it comes from chwidth=1.
	if got := apWidth(parseHostapdKV(statusFixture)); got != 80 {
		t.Errorf("measured sample is 80MHz (iw agrees), got %d", got)
	}
	tests := []struct {
		name string
		kv   map[string]string
		want int
	}{
		{"40MHz: no chwidth, a secondary channel",
			map[string]string{"channel": "36", "secondary_channel": "1"}, 40},
		{"20MHz: no chwidth, no secondary",
			map[string]string{"channel": "6", "secondary_channel": "0"}, 20},
		{"vht alone is enough",
			map[string]string{"channel": "36", "vht_oper_chwidth": "1"}, 80},
		{"nothing at all yields nothing, not a default 20",
			map[string]string{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := apWidth(tc.kv); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAPModeNamesTheHighestGeneration(t *testing.T) {
	if got := apMode(parseHostapdKV(statusFixture)); got != "802.11ax" {
		t.Errorf("ax is enabled in the sample, got %q", got)
	}
	if got := apMode(map[string]string{"ieee80211n": "1", "ieee80211ac": "1"}); got != "802.11ac" {
		t.Errorf("got %q, want 802.11ac", got)
	}
	if got := apMode(map[string]string{}); got != "" {
		t.Errorf("nothing enabled should name nothing, got %q", got)
	}
}

// infoFixture is real `iw dev wlan-usb info` output. This, not the survey
// label, is where the operating frequency comes from.
const infoFixture = `Interface wlan-usb
	ifindex 6
	wdev 0x100000001
	addr 9c:ef:d5:f6:3f:f2
	ssid infinite-streaming-boa
	type AP
	wiphy 1
	channel 40 (5200 MHz), width: 80 MHz, center1: 5210 MHz
	txpower 3.00 dBm
`

func TestOperatingFreqReadsTheFrequencyNotTheChannel(t *testing.T) {
	// The line leads with "channel 40", so a parser taking the first number
	// returns 40 -- which is a valid-looking frequency-shaped integer and
	// wrong by two orders of magnitude.
	got := 0
	for _, line := range strings.Split(infoFixture, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "channel ") {
			continue
		}
		open := strings.Index(t, "(")
		if open < 0 {
			continue
		}
		f := strings.Fields(t[open+1:])
		if len(f) == 0 {
			continue
		}
		got = atoiSafe(f[0])
		break
	}
	if got != 5200 {
		t.Errorf("got %d, want 5200 (the frequency, not the channel number)", got)
	}
}
