package boa

import (
	"testing"
	"time"
)

// Both fixtures are real dumps taken from the box on 2026-09-07, the same two
// clients seconds apart on two different radios. They are kept verbatim because
// the DIFFERENCE between them is the thing under test: mt7921u emits the
// airtime lines and the Pi's onboard brcmfmac omits them entirely, and no
// amount of reading either dump in isolation reveals that.
const mt7921uDump = `Station fc:9c:a7:93:7f:ed (on wlan-usb)
	inactive time:	4 ms
	rx bytes:	694424
	rx packets:	8947
	tx bytes:	163389044
	tx packets:	2140667
	tx retries:	0
	tx failed:	0
	signal:  	-32 [-33, -36] dBm
	tx bitrate:	1200.9 MBit/s 80MHz HE-MCS 11 HE-NSS 2 HE-GI 0 HE-DCM 0
	tx duration:	10974690 us
	rx bitrate:	1200.9 MBit/s 80MHz HE-MCS 11 HE-NSS 2 HE-GI 0 HE-DCM 0
	rx duration:	660810 us
	last ack signal:-30 dBm
	connected time:	32 seconds
`

// The same client on wlan0 moments later. Note what is NOT here: no signal, no
// tx retries, no tx duration, no rx duration, and a bare bitrate with no MCS.
const brcmfmacDump = `Station fc:9c:a7:93:7f:ed (on wlan0)
	inactive time:	32000 ms
	rx bytes:	2174920
	rx packets:	19174
	tx bytes:	161622885
	tx packets:	304124
	tx failed:	523
	tx bitrate:	72.2 MBit/s
	rx bitrate:	72.2 MBit/s
	authorized:	yes
	connected time:	70 seconds
`

func TestParseStationDumpReadsAirtimeWhenTheDriverReportsIt(t *testing.T) {
	st := parseStationDump(mt7921uDump)["fc:9c:a7:93:7f:ed"]
	if st == nil {
		t.Fatal("station not parsed")
	}
	if st.TxDurationUs != 10974690 || st.RxDurationUs != 660810 {
		t.Errorf("airtime: got tx=%d rx=%d, want tx=10974690 rx=660810",
			st.TxDurationUs, st.RxDurationUs)
	}
	if !st.DurationKnown {
		t.Error("DurationKnown false on a dump that carried both lines")
	}
}

// The whole point of the flag. A driver that says nothing must not be
// indistinguishable from a client that used no airtime, because the interface
// draws one as a warning and the other as an empty band.
func TestParseStationDumpMarksAirtimeUnknownWhenTheDriverOmitsIt(t *testing.T) {
	st := parseStationDump(brcmfmacDump)["fc:9c:a7:93:7f:ed"]
	if st == nil {
		t.Fatal("station not parsed")
	}
	if st.DurationKnown {
		t.Error("DurationKnown true on a dump with no duration lines at all")
	}
	if st.TxDurationUs != 0 || st.RxDurationUs != 0 {
		t.Errorf("invented airtime: tx=%d rx=%d", st.TxDurationUs, st.RxDurationUs)
	}
	// The rest of the dump must still parse, or a radio without airtime would
	// lose its throughput too.
	if st.TxBytes != 161622885 || st.TxFailed != 523 {
		t.Errorf("other fields lost: tx=%d failed=%d", st.TxBytes, st.TxFailed)
	}
}

func newAirEngine() *Engine {
	return &Engine{airPrev: map[string]airSample{}}
}

// The first sample has nothing to difference against. Reporting anything but
// zero would draw a spike at the moment a client appears, every time.
func TestAirtimePctFirstSampleIsZero(t *testing.T) {
	e := newAirEngine()
	now := time.Now()
	st := &Station{TxDurationUs: 500_000, RxDurationUs: 0, DurationKnown: true}
	if got := e.airtimePct("aa", st, now); got != 0 {
		t.Errorf("first sample: got %v, want 0", got)
	}
}

// The arithmetic, against the real measurement this was built from: over a
// 10.011s window one client's counters advanced 2,652,338us, which is 26.5% of
// the wall clock.
func TestAirtimePctDifferencesAgainstWallClock(t *testing.T) {
	e := newAirEngine()
	t0 := time.Now()
	e.airtimePct("aa", &Station{TxDurationUs: 156950505, RxDurationUs: 0, DurationKnown: true}, t0)
	got := e.airtimePct("aa",
		&Station{TxDurationUs: 159602843, RxDurationUs: 0, DurationKnown: true},
		t0.Add(10011*time.Millisecond))
	if want := 26.49; got < want-0.05 || got > want+0.05 {
		t.Errorf("airtime: got %.2f%%, want about %.2f%%", got, want)
	}
}

// Transmit and receive are both the radio's time and both count.
func TestAirtimePctSumsBothDirections(t *testing.T) {
	e := newAirEngine()
	t0 := time.Now()
	e.airtimePct("aa", &Station{DurationKnown: true}, t0)
	got := e.airtimePct("aa",
		&Station{TxDurationUs: 300_000, RxDurationUs: 200_000, DurationKnown: true},
		t0.Add(time.Second))
	if want := 50.0; got < want-0.01 || got > want+0.01 {
		t.Errorf("got %.2f%%, want %.2f%%", got, want)
	}
}

// A re-association restarts the driver's counters at zero. Without the check
// the delta is hugely negative and the band renders as a spike or a hole -- the
// same failure rate() guards against for bytes.
func TestAirtimePctSurvivesACounterReset(t *testing.T) {
	e := newAirEngine()
	t0 := time.Now()
	e.airtimePct("aa", &Station{TxDurationUs: 9_000_000, DurationKnown: true}, t0)
	got := e.airtimePct("aa",
		&Station{TxDurationUs: 1_000, DurationKnown: true}, t0.Add(time.Second))
	if got != 0 {
		t.Errorf("counter reset: got %v, want 0", got)
	}
	// And the next tick recovers rather than staying stuck at zero.
	got = e.airtimePct("aa",
		&Station{TxDurationUs: 501_000, DurationKnown: true}, t0.Add(2*time.Second))
	if want := 50.0; got < want-0.01 || got > want+0.01 {
		t.Errorf("after reset: got %.2f%%, want %.2f%%", got, want)
	}
}

// A driver that cannot answer yields zero AND forgets its previous sample, so
// that a radio which starts reporting mid-session does not difference against
// a stale reading from before the gap.
func TestAirtimePctIsZeroAndForgetsWhenTheDriverCannotReport(t *testing.T) {
	e := newAirEngine()
	t0 := time.Now()
	e.airtimePct("aa", &Station{TxDurationUs: 5_000_000, DurationKnown: true}, t0)
	if got := e.airtimePct("aa", &Station{DurationKnown: false}, t0.Add(time.Second)); got != 0 {
		t.Errorf("unreportable driver: got %v, want 0", got)
	}
	if _, ok := e.airPrev["aa"]; ok {
		t.Error("kept a previous sample for a station it can no longer measure")
	}
	if got := e.airtimePct("aa", nil, t0.Add(2*time.Second)); got != 0 {
		t.Errorf("absent station: got %v, want 0", got)
	}
}

// A radio cannot transmit and receive at once, so the pair cannot exceed the
// clock. Clamped rather than trusted: on a fixed 0-100% axis a 140% band would
// push everything below it off the top of the chart.
func TestAirtimePctClampsAtOneHundred(t *testing.T) {
	e := newAirEngine()
	t0 := time.Now()
	e.airtimePct("aa", &Station{DurationKnown: true}, t0)
	got := e.airtimePct("aa",
		&Station{TxDurationUs: 1_400_000, RxDurationUs: 100_000, DurationKnown: true},
		t0.Add(time.Second))
	if got != 100 {
		t.Errorf("got %v, want it clamped to 100", got)
	}
}

// A tick that arrives with no time on the clock must not divide by zero.
func TestAirtimePctHandlesAZeroWindow(t *testing.T) {
	e := newAirEngine()
	t0 := time.Now()
	e.airtimePct("aa", &Station{TxDurationUs: 1000, DurationKnown: true}, t0)
	if got := e.airtimePct("aa", &Station{TxDurationUs: 2000, DurationKnown: true}, t0); got != 0 {
		t.Errorf("zero window: got %v, want 0", got)
	}
}

// The radio-level flag is merged, not replaced, so a radio whose clients have
// all left keeps the answer it gave while it had some -- the question is about
// the driver, not about who happens to be associated this second.
func TestRememberAirSeenKeepsAnswersForAnEmptiedRadio(t *testing.T) {
	e := &Engine{}
	e.rememberAirSeen(map[string]bool{"wlan-usb": true, "wlan0": false})
	e.rememberAirSeen(map[string]bool{"wlan-usb": true})
	got := e.airtimeSeen()
	if !got["wlan-usb"] {
		t.Error("wlan-usb lost its capability")
	}
	if v, ok := got["wlan0"]; !ok || v {
		t.Errorf("wlan0: got %v present=%v, want false and still present", v, ok)
	}
}

// An empty round changes nothing. A tick where no radio had a station is not
// evidence that every radio has stopped reporting.
func TestRememberAirSeenIgnoresAnEmptyRound(t *testing.T) {
	e := &Engine{}
	e.rememberAirSeen(map[string]bool{"wlan-usb": true})
	e.rememberAirSeen(map[string]bool{})
	if !e.airtimeSeen()["wlan-usb"] {
		t.Error("an empty round erased a known capability")
	}
}
