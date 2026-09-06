package boa

import (
	"strings"
	"testing"
)

// The request body is fixed-layout and hostapd rejects the whole command rather
// than a field, so a wrong offset here produces "FAIL" with nothing saying
// which byte was wrong. Pinned byte by byte.
func TestBeaconRequestLayout(t *testing.T) {
	// Channel 36 on class 115, active mode, measuring 9c:ef:d5:f6:3f:f2.
	got, err := beaconRequestHex(opClass20(36), 36, beaconDurationTU, beaconModeActive,
		"9c:ef:d5:f6:3f:f2")
	if err != nil {
		t.Fatalf("beaconRequestHex: %v", err)
	}
	// 73 = 0x73 = 115 (operating class)
	// 24 = 0x24 = 36  (channel)
	// 0000        randomisation interval, start now
	// 6400        duration 100 TU, LITTLE-endian -- 0x0064 would be 25600 TU,
	//             which is 26 seconds of the client's radio time
	// 01          active
	// 9cefd5f63ff2  BSSID
	const want = "7324" + "0000" + "6400" + "01" + "9cefd5f63ff2"
	if got != want {
		t.Errorf("beacon request\n got %s\nwant %s", got, want)
	}
}

// A channel with no 20MHz class must be refused rather than sent with class 0,
// which hostapd accepts and every client ignores.
func TestBeaconRequestRefusesAChannelItCannotName(t *testing.T) {
	if _, err := beaconRequestHex(opClass20(200), 200, beaconDurationTU,
		beaconModeActive, "9c:ef:d5:f6:3f:f2"); err == nil {
		t.Error("channel 200 has no 20MHz operating class, but the request was built anyway")
	}
	if _, err := beaconRequestHex(opClass20(36), 36, beaconDurationTU,
		beaconModeActive, "not-a-mac"); err == nil {
		t.Error("a malformed BSSID was accepted")
	}
}

// The gap this function exists to avoid: opClassAndPhy(ch, 20) answers 115 for
// every 5GHz channel below 149, which is wrong for everything above 48.
func TestOpClass20CoversEachBand(t *testing.T) {
	for _, c := range []struct{ ch, want int }{
		{1, 81}, {6, 81}, {13, 81}, {14, 82},
		{36, 115}, {48, 115},
		{52, 118}, {64, 118},
		{100, 121}, {144, 121},
		{149, 125}, {165, 125},
		{0, 0}, {15, 0}, {35, 0}, {200, 0},
	} {
		if got := opClass20(c.ch); got != c.want {
			t.Errorf("opClass20(%d) = %d, want %d", c.ch, got, c.want)
		}
	}
	// The specific regression: channels 52-144 must NOT come back as 115.
	for _, ch := range []int{52, 60, 100, 132, 144} {
		if opClass20(ch) == 115 {
			t.Errorf("channel %d reported as class 115, which does not contain it", ch)
		}
	}
}

// RCPI is 0.5 dB steps from -110 dBm, and 255 means "could not measure". Read
// as a plain integer it would arrive as a confident +17.5 dBm.
func TestRCPIConversionRejectsTheReservedRange(t *testing.T) {
	for _, c := range []struct {
		rcpi int
		dbm  int
		ok   bool
	}{
		{0, -110, true},
		{60, -80, true},
		{160, -30, true},
		{220, 0, true},
		{221, 0, false}, // reserved
		{255, 0, false}, // not available
		{-1, 0, false},
	} {
		dbm, ok := rcpiToDBm(c.rcpi)
		if ok != c.ok || (ok && dbm != c.dbm) {
			t.Errorf("rcpiToDBm(%d) = %d,%v; want %d,%v", c.rcpi, dbm, ok, c.dbm, c.ok)
		}
	}
}

// The report is 26 fixed bytes and every offset is a place a wrong constant
// yields a plausible small integer instead of an error -- an RCPI read one byte
// early is an RSNI, and both look like signals.
func TestBeaconReportParsesTheFixedFields(t *testing.T) {
	// class=115 ch=36 | start time (8) | duration (2) | frame info
	// | RCPI=160 (-30 dBm) | RSNI=80 (+30 dB) | BSSID | antenna | parent TSF
	dump := "73" + "24" +
		"0000000000000000" + "6400" + "05" +
		"a0" + "50" +
		"9cefd5f63ff2" + "01" + "00000000"
	br, err := parseBeaconReport(dump)
	if err != nil {
		t.Fatalf("parseBeaconReport: %v", err)
	}
	if br.Channel != 36 {
		t.Errorf("channel = %d, want 36", br.Channel)
	}
	if br.RCPI != 160 || !br.HasSignal || br.SignalDBm != -30 {
		t.Errorf("RCPI %d -> %d dBm (has=%v), want 160 -> -30 dBm",
			br.RCPI, br.SignalDBm, br.HasSignal)
	}
	if !br.HasRSNI || br.RSNIdB != 30 {
		t.Errorf("RSNI = %v dB (has=%v), want 30", br.RSNIdB, br.HasRSNI)
	}
	if br.BSSID != "9c:ef:d5:f6:3f:f2" {
		t.Errorf("BSSID = %q, want 9c:ef:d5:f6:3f:f2", br.BSSID)
	}
}

// A short report is a truncated one, and reading past it would return zeros
// that look exactly like a real measurement of a very weak signal.
func TestBeaconReportRefusesAShortBody(t *testing.T) {
	if _, err := parseBeaconReport("7324a050"); err == nil {
		t.Error("a 4-byte report was accepted")
	}
	if _, err := parseBeaconReport("nothex"); err == nil {
		t.Error("a non-hex report was accepted")
	}
}

// A client that could not measure must not be filed as a measurement of 0 dBm.
func TestBeaconReportCarriesNotAvailableThrough(t *testing.T) {
	dump := "73" + "24" + "0000000000000000" + "6400" + "05" +
		"ff" + "ff" + "9cefd5f63ff2" + "01" + "00000000"
	br, err := parseBeaconReport(dump)
	if err != nil {
		t.Fatalf("parseBeaconReport: %v", err)
	}
	if br.HasSignal {
		t.Errorf("RCPI 255 means not available, but a signal of %d dBm was reported",
			br.SignalDBm)
	}
	if br.HasRSNI {
		t.Error("RSNI 255 means not available, but a value was reported")
	}
}

// Strongest first, and "could not measure" last rather than sorted as if its
// zero were the loudest thing on the box.
func TestBeaconStoreSortsStrongestFirst(t *testing.T) {
	var s beaconStore
	s.put("aa:bb:cc:dd:ee:ff", BeaconReport{BSSID: "11:11:11:11:11:11", SignalDBm: -70, HasSignal: true})
	s.put("aa:bb:cc:dd:ee:ff", BeaconReport{BSSID: "22:22:22:22:22:22", SignalDBm: -30, HasSignal: true})
	s.put("aa:bb:cc:dd:ee:ff", BeaconReport{BSSID: "33:33:33:33:33:33"}) // not available
	got := s.get("aa:bb:cc:dd:ee:ff")
	if len(got) != 3 {
		t.Fatalf("got %d reports, want 3", len(got))
	}
	if got[0].BSSID != "22:22:22:22:22:22" || got[1].BSSID != "11:11:11:11:11:11" {
		t.Errorf("wrong order: %s then %s", got[0].BSSID, got[1].BSSID)
	}
	if got[2].HasSignal {
		t.Error("the unmeasured report should sort last")
	}
	// One report per BSS: a second measurement replaces the first rather than
	// accumulating a history nobody asked for.
	s.put("aa:bb:cc:dd:ee:ff", BeaconReport{BSSID: "22:22:22:22:22:22", SignalDBm: -40, HasSignal: true})
	if got := s.get("aa:bb:cc:dd:ee:ff"); len(got) != 3 || got[0].SignalDBm != -40 {
		t.Errorf("a repeat measurement should replace, got %d reports, first %d dBm",
			len(got), got[0].SignalDBm)
	}
	s.forget("aa:bb:cc:dd:ee:ff")
	if got := s.get("aa:bb:cc:dd:ee:ff"); got != nil {
		t.Errorf("forget left %d reports", len(got))
	}
}

// A refusal has to name itself, or "no report" reads as a parse failure.
func TestBeaconRefusalIsInWords(t *testing.T) {
	for _, c := range []struct {
		mode uint8
		want string
	}{
		{0x02, "not capable"},
		{0x04, "refused"},
		{0x01, "too late"},
	} {
		if got := beaconRefusal(c.mode); !strings.Contains(got, c.want) {
			t.Errorf("beaconRefusal(0x%02x) = %q, want it to mention %q",
				c.mode, got, c.want)
		}
	}
}
