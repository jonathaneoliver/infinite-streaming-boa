package boa

import (
	"strings"
	"testing"
	"time"
)

// The message hostapd actually sends, priority prefix and all, parsed into the
// three things worth reading: who, what they decided, and where they went.
func TestBTMResponseIsParsedFromWhatHostapdSends(t *testing.T) {
	e := &Engine{}
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz")
	e.handleHostapdEvent("wlan0",
		"<3>BSS-TM-RESP aa:bb:cc:dd:ee:ff status_code=0 bss_termination_delay=0 target_bssid=9c:ef:d5:f6:3f:f2")

	line := lastEventText(t, e)
	for _, want := range []string{"accepted", "5GHz", "9c:ef:d5:f6:3f:f2"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q missing from: %s", want, line)
		}
	}
	// The request is answered, so nothing should later report it as mute.
	if _, still := e.takePendingSteer("aa:bb:cc:dd:ee:ff"); still {
		t.Error("an answered request stayed pending, so it would be reported mute as well")
	}
}

// A refusal must name the reason in words. A bare status_code=7 is a number
// the reader has to go and look up, which means it does not get read.
func TestARefusalIsReportedInWords(t *testing.T) {
	e := &Engine{}
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz")
	e.handleHostapdEvent("wlan0",
		"<3>BSS-TM-RESP aa:bb:cc:dd:ee:ff status_code=7 bss_termination_delay=0")

	line := lastEventText(t, e)
	if strings.Contains(line, "status_code") {
		t.Errorf("the raw code leaked into the log: %s", line)
	}
	if !strings.Contains(line, "none of the candidates") {
		t.Errorf("the refusal was not explained: %s", line)
	}
}

// Silence is the finding, so it is stated. A reader who sees "asked to move"
// and then nothing cannot tell a refusal from a request that went nowhere.
func TestAClientThatNeverAnswersIsReportedAsSuch(t *testing.T) {
	e := &Engine{}
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz")

	// Not yet: a client answering promptly must not be called mute first.
	e.reportMuteSteers()
	if n := len(e.events.since(0, 10)); n != 0 {
		t.Fatalf("reported silence after no wait at all (%d events)", n)
	}

	// Aged past the wait.
	e.mu.Lock()
	p := e.pendingSteers["aa:bb:cc:dd:ee:ff"]
	p.at = time.Now().Add(-btmWait - time.Second)
	e.pendingSteers["aa:bb:cc:dd:ee:ff"] = p
	e.mu.Unlock()

	e.reportMuteSteers()
	line := lastEventText(t, e)
	if !strings.Contains(line, "did not answer") || !strings.Contains(line, "has not moved") {
		t.Errorf("silence was not reported: %s", line)
	}
	// Reported once, not every tick from here on.
	e.reportMuteSteers()
	if n := len(e.events.since(0, 10)); n != 1 {
		t.Errorf("silence was reported %d times, want once", n)
	}
}

// Anything that is not a transition response is not this monitor's business.
// hostapd is talkative and most of what it says is already reported elsewhere.
func TestUnrelatedEventsAreIgnored(t *testing.T) {
	e := &Engine{}
	for _, msg := range []string{
		"<3>AP-STA-CONNECTED aa:bb:cc:dd:ee:ff",
		"<3>AP-ENABLED",
		"<3>CTRL-EVENT-TERMINATING",
		"BSS-TM-RESP",                                  // truncated
		"<3>BSS-TM-RESP aa:bb:cc:dd:ee:ff",             // no status
		"<3>BSS-TM-RESP aa:bb:cc:dd:ee:ff nonsense=42", // no status
	} {
		e.handleHostapdEvent("wlan0", msg)
	}
	if n := len(e.events.since(0, 10)); n != 0 {
		t.Errorf("logged %d events for messages that carry no transition result", n)
	}
}

// MEASURED 2026-09-04: an iPhone asked to leave wlan-usb moved one second
// later and sent no response frame at all. Reporting that as "did not support
// 802.11v, or ignored it" contradicted the roam event two lines above it, so
// moving and answering are reported as the separate facts they are.
func TestAClientThatMovesWithoutAnsweringIsNotCalledUnresponsive(t *testing.T) {
	e := &Engine{
		cfg:          Config{WlanPorts: []string{"wlan0", "wlan-usb"}},
		stationRadio: map[string]string{"aa:bb:cc:dd:ee:ff": "wlan-usb"},
	}
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz")
	e.mu.Lock()
	p := e.pendingSteers["aa:bb:cc:dd:ee:ff"]
	p.at = time.Now().Add(-btmWait - time.Second)
	e.pendingSteers["aa:bb:cc:dd:ee:ff"] = p
	e.mu.Unlock()

	e.reportMuteSteers()
	line := lastEventText(t, e)
	if !strings.Contains(line, "without answering") {
		t.Errorf("a client that moved was not reported as having moved: %s", line)
	}
	for _, wrong := range []string{"has not moved", "does not support"} {
		if strings.Contains(line, wrong) {
			t.Errorf("%q claimed about a client that demonstrably moved: %s", wrong, line)
		}
	}
}

// An answer with no request behind it still means something -- another
// operator, or a hand-run hostapd_cli -- so it is logged rather than dropped.
func TestAnUnsolicitedAnswerIsStillLogged(t *testing.T) {
	e := &Engine{}
	e.handleHostapdEvent("wlan0", "<3>BSS-TM-RESP aa:bb:cc:dd:ee:ff status_code=1")
	if n := len(e.events.since(0, 10)); n != 1 {
		t.Fatalf("an unsolicited response was dropped (%d events)", n)
	}
}

func lastEventText(t *testing.T, e *Engine) string {
	t.Helper()
	evs := e.events.since(0, 10)
	if len(evs) == 0 {
		t.Fatal("no event was logged")
	}
	return evs[len(evs)-1].Text
}
