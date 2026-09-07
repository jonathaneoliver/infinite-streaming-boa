package boa

import "testing"

/*
 * Every frame here except the deliberately malformed ones was captured from
 * this box's own radios on 2026-09-07, with notify_mgmt_frames=1 and two real
 * clients associated. They are kept verbatim rather than reduced to minimal
 * examples: the point of the parser is to survive what client firmware actually
 * sends, and a hand-written frame only proves it survives what we imagined.
 */

// A BSS Transition Management Response refusing a steer and naming where it
// would rather go. This is the frame issue #228 was about: hostapd's cooked
// event reports "status_code=6" and discards everything after it.
const btmRefusalFrame = "d0003c009cefd5f646c7fc9ca7937fed9cefd5f646c790d2" +
	"0a080106003410d83addad008b000000100706000301ff"

// A reassociation request, carrying the radio the client is coming from and
// what 802.11k it will do.
const reassocFrame = "20003c009cefd5f646c7fc9ca7937fed9cefd5f646c7a02b" +
	"111014009cefd5f63ff20016696e66696e6974652d73747265616d696e672d626f61" +
	"01088c129824b048606c2102f915240a24043404640c9504a501" +
	"30140100000fac040100000fac040100000fac020c00" +
	"460541080100002d1a6f001bffff000000000000000000000000000000"

// A Radio Measurement action frame -- a beacon report. Same subtype as the BTM
// response above and a different category, so it must not be mistaken for one.
const beaconReportFrame = "d0003c009cefd5f646c7fc9ca7937fed9cefd5f646c790d2" +
	"0501012703010005"

func TestParseBTMRefusalCarriesCandidateList(t *testing.T) {
	f, err := parseMgmtFrame(btmRefusalFrame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !f.IsBTMResponse {
		t.Fatalf("not recognised as a BTM response")
	}
	if f.Status != 6 {
		t.Errorf("status = %d, want 6", f.Status)
	}
	if f.DialogToken != 1 {
		t.Errorf("dialog token = %d, want 1", f.DialogToken)
	}
	if f.Src != "fc:9c:a7:93:7f:ed" {
		t.Errorf("src = %q", f.Src)
	}
	if len(f.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(f.Candidates))
	}
	c := f.Candidates[0]
	// The whole reason the issue exists: the client is not refusing to move,
	// it is asking for wlan0 on channel 6 as hard as the protocol allows.
	if c.BSSID != "d8:3a:dd:ad:00:8b" {
		t.Errorf("candidate BSSID = %q", c.BSSID)
	}
	if c.Channel != 6 {
		t.Errorf("candidate channel = %d, want 6", c.Channel)
	}
	if c.OpClass != 7 {
		t.Errorf("candidate operating class = %d, want 7", c.OpClass)
	}
	if c.Preference != 255 {
		t.Errorf("candidate preference = %d, want 255", c.Preference)
	}
}

func TestParseReassocRequest(t *testing.T) {
	f, err := parseMgmtFrame(reassocFrame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Subtype != subtypeReassocReq {
		t.Fatalf("subtype = %d, want %d", f.Subtype, subtypeReassocReq)
	}
	// The frame names the radio it is leaving, which nothing else on the box
	// can tell us -- the station table knows where a client is, not what the
	// client believes it just left.
	if f.CurrentAP != "9c:ef:d5:f6:3f:f2" {
		t.Errorf("current AP = %q, want 9c:ef:d5:f6:3f:f2", f.CurrentAP)
	}
	if !f.RMCapabilitiesSet {
		t.Fatalf("RM Enabled Capabilities not found")
	}
	if f.RMCapabilities != 0x41 {
		t.Errorf("RM capabilities = 0x%02x, want 0x41", f.RMCapabilities)
	}
	// 0x41 is link measurement + beacon table, and crucially NOT beacon passive
	// or active -- which is why hostapd refuses to send those requests at all.
	if got, want := rmCapabilitySummary(f.RMCapabilities), "link measurement, beacon table"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func TestBeaconReportIsNotABTMResponse(t *testing.T) {
	f, err := parseMgmtFrame(beaconReportFrame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Subtype != subtypeAction {
		t.Errorf("subtype = %d, want action", f.Subtype)
	}
	if f.IsBTMResponse {
		t.Errorf("a radio-measurement action frame was read as a BTM response")
	}
	if len(f.Candidates) != 0 {
		t.Errorf("candidates = %d, want none", len(f.Candidates))
	}
}

func TestParseDeauthReason(t *testing.T) {
	// Reason 7 is what a real client sent, four times, after being deauthed by
	// the box: it kept receiving frames addressed to a station it no longer
	// considered associated.
	const frame = "c0003c009cefd5f646c7fc9ca7937fed9cefd5f646c700000700"
	f, err := parseMgmtFrame(frame)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Subtype != subtypeDeauth {
		t.Fatalf("subtype = %d, want deauth", f.Subtype)
	}
	if f.Reason != 7 {
		t.Errorf("reason = %d, want 7", f.Reason)
	}
	if f.Src != "fc:9c:a7:93:7f:ed" {
		t.Errorf("src = %q", f.Src)
	}
}

func TestRMCapabilitySummaryNamesEachBit(t *testing.T) {
	for _, tc := range []struct {
		v    byte
		want string
	}{
		{0x00, "none"},
		{0x41, "link measurement, beacon table"},
		{0x43, "link measurement, neighbor report, beacon table"},
		{0x70, "beacon passive, beacon active, beacon table"},
	} {
		if got := rmCapabilitySummary(tc.v); got != tc.want {
			t.Errorf("0x%02x -> %q, want %q", tc.v, got, tc.want)
		}
	}
}

// A parser fed rubbish must return an error rather than panic. These run on a
// monitor goroutine that owns one radio's only connection to hostapd, so a
// panic here would leave that radio silent for the rest of the daemon's life.
func TestParseMgmtFrameRejectsRubbishWithoutPanicking(t *testing.T) {
	for name, dump := range map[string]string{
		"empty":            "",
		"not hex":          "zzzz",
		"odd length":       "b0003c0",
		"shorter than hdr": "b0003c009cefd5f646c7",
		"data frame":       "08003c009cefd5f646c7fc9ca7937fed9cefd5f646c700a8",
	} {
		if _, err := parseMgmtFrame(dump); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// A truncated or lying element length must stop the walk, not read past it and
// not take the goroutine down. Client firmware sends these.
func TestParseMgmtFrameSurvivesLyingElementLengths(t *testing.T) {
	for name, dump := range map[string]string{
		// Reassociation whose RM element claims 5 octets and supplies one.
		"element overruns":   "20003c009cefd5f646c7fc9ca7937fed9cefd5f646c7a02b111014009cefd5f63ff24605ff",
		"element header cut": "20003c009cefd5f646c7fc9ca7937fed9cefd5f646c7a02b111014009cefd5f63ff246",
		// BTM response ending in the middle of a neighbor report.
		"neighbor truncated": "d0003c009cefd5f646c7fc9ca7937fed9cefd5f646c790d20a080106003410d83addad",
	} {
		f, err := parseMgmtFrame(dump)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if len(f.Candidates) != 0 {
			t.Errorf("%s: took %d candidates from a truncated frame", name, len(f.Candidates))
		}
	}
}
