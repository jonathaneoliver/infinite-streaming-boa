package boa

import "testing"

// Captured from the Cudy TR3000 on 2026-09-22, trimmed to the lines that
// matter plus their neighbours.
const cudyDevInfo = `Interface phy1-ap0
	ifindex 12
	wdev 0x100000002
	addr d6:0d:ab:46:3f:17
	ssid cudy1263
	type AP
	wiphy 1
	channel 40 (5200 MHz), width: 80 MHz, center1: 5210 MHz
	txpower 23.00 dBm
	multicast TXQ:`

const cudyPhyInfo = `Wiphy phy1
	Band 2:
		Frequencies:
			* 5180.0 MHz [36] (23.0 dBm)
			* 5200.0 MHz [40] (23.0 dBm)
			* 5220.0 MHz [44] (23.0 dBm)
			* 5260.0 MHz [52] (24.0 dBm) (radar detection)
			* 5745.0 MHz [149] (30.0 dBm)`

func TestParseTxPower(t *testing.T) {
	if got, ok := parseTxPower(cudyDevInfo); !ok || got != 23 {
		t.Fatalf("parseTxPower = %v, %v; want 23, true", got, ok)
	}
	// The mt7921u's signature (#202): a reading, and a wrong one. Parsed as
	// what it says; whether to trust it is the driver table's job.
	if got, ok := parseTxPower("\ttxpower 3.00 dBm\n"); !ok || got != 3 {
		t.Fatalf("parseTxPower(3.00) = %v, %v", got, ok)
	}
	if _, ok := parseTxPower("Interface wlan0\n\ttype managed\n"); ok {
		t.Fatal("an interface with no txpower line must not read as 0 dBm")
	}
}

// The limit is the CURRENT channel's, and the channels around it differ: a
// neighbour's figure would put the top of the slider somewhere the radio is
// not allowed to go.
func TestChannelMaxDBmIsTheCurrentChannels(t *testing.T) {
	for _, c := range []struct {
		freq int
		want float64
	}{
		{5200, 23},
		{5260, 24}, // a flag after the power must not break the parse
		{5745, 30},
		{5500, 0}, // not in the list
		{0, 0},    // radio down
	} {
		if got := channelMaxDBm(cudyPhyInfo, c.freq); got != c.want {
			t.Errorf("channelMaxDBm(%d) = %v, want %v", c.freq, got, c.want)
		}
	}
}

func TestTxPowerRefusedOnTheDriverThatIgnoresIt(t *testing.T) {
	if _, bad := txpowerIgnoredBy["mt7921u"]; !bad {
		t.Fatal("mt7921u must be listed: it discards every setting (#202)")
	}
	if _, bad := txpowerIgnoredBy["mt798x-wmac"]; bad {
		t.Fatal("mt798x-wmac was measured honouring it and must stay settable")
	}
}

// A driver caught discarding a level is remembered for every radio on it, and
// only until the daemon restarts: the reason is a claim about driver code, and
// driver code gets fixed. The mt7921u patch in #202 is unmerged, not
// imaginary, and a refusal written to disk would outlive the bug with no way
// back that anybody would think to look for.
func TestALearnedRefusalCoversTheDriverAndIsNotPersisted(t *testing.T) {
	e := &Engine{}
	if got := e.txIgnoredReason("mt798x-wmac"); got != "" {
		t.Fatalf("a driver nobody has caught must start settable, got %q", got)
	}
	e.noteTxPowerIgnored("mt798x-wmac", "asked for 10 dBm, it reports 23.00")
	if got := e.txIgnoredReason("mt798x-wmac"); got == "" {
		t.Fatal("a driver caught discarding a level must be remembered")
	}
	// A fresh Engine is a restarted daemon: the refusal is gone, so a fixed
	// driver gets another chance without anybody editing a file.
	if (&Engine{}).txIgnoredReason("mt798x-wmac") != "" {
		t.Fatal("a learned refusal must not outlive the daemon")
	}
	// The static list needs no learning at all.
	if (&Engine{}).txIgnoredReason("mt7921u") == "" {
		t.Fatal("mt7921u is known before anyone tries (#202)")
	}
}
