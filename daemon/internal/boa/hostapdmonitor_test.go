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
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz", false)
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
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz", false)
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
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz", false)

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

// Silence after an INSISTED steer is not a dead end -- the client is about to be
// disassociated -- and the log has to say so.
//
// Observed 2026-09-06: the log said "did not answer, and has not moved" and then
// the device moved 25 seconds later with nothing in between, because the
// deadline runs from the request and most of it had already elapsed by the time
// silence was reported. An operator reading only the first line has no reason to
// keep watching the window in which the interesting thing happens.
func TestSilenceAfterAnInsistedSteerSaysWhatHappensNext(t *testing.T) {
	e := &Engine{}
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz", true)
	e.mu.Lock()
	p := e.pendingSteers["aa:bb:cc:dd:ee:ff"]
	p.at = time.Now().Add(-btmWait - time.Second)
	e.pendingSteers["aa:bb:cc:dd:ee:ff"] = p
	e.mu.Unlock()

	e.reportMuteSteers()
	line := lastEventText(t, e)
	for _, want := range []string{"disassociated", "pick a radio for itself"} {
		if !strings.Contains(line, want) {
			t.Errorf("an insisted steer's silence must mention %q, got: %s", want, line)
		}
	}
	// And it must NOT claim the device has not moved, which was the old line's
	// mistake: it was about to.
	if strings.Contains(line, "has not moved") {
		t.Errorf("an insisted steer should not report a settled outcome: %s", line)
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
	e.notePendingSteer("aa:bb:cc:dd:ee:ff", "wlan0", "5GHz", false)
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

// The whole point of notify_mgmt_frames, end to end: hostapd sends the raw
// refusal and then its own summary of it, and the two become ONE line saying
// both that the client refused and where it asked to go instead.
//
// The order here is hostapd's, not a guess: it forwards the received frame from
// its receive path and emits BSS-TM-RESP from the same path, both on this
// radio's socket, which one goroutine reads in order.
func TestARefusalCarriesTheCandidateListTheClientSent(t *testing.T) {
	e := &Engine{}
	e.notePendingSteer("fc:9c:a7:93:7f:ed", "wlan-usb", "5GHz", false)

	e.handleHostapdEvent("wlan-usb", "<3>AP-MGMT-FRAME-RECEIVED buf="+btmRefusalFrame)
	e.handleHostapdEvent("wlan-usb",
		"<3>BSS-TM-RESP fc:9c:a7:93:7f:ed status_code=6 bss_termination_delay=0")

	line := lastEventText(t, e)
	for _, want := range []string{
		"offering its own list", // the status, in words
		"d8:3a:dd:ad:00:8b",     // the BSS it named
		"channel 6",             // where that is
		"preference 255",        // how much it wants it
	} {
		if !strings.Contains(line, want) {
			t.Errorf("%q missing from: %s", want, line)
		}
	}
	// One line, not two: the raw frame must not log on its own account.
	if n := len(e.events.since(0, 10)); n != 1 {
		t.Errorf("the refusal was reported as %d events, want 1", n)
	}
}

// A client's own reason for leaving is always taken, verbose or not. It is the
// only thing that separates a device that roamed away from one that timed out
// from one that gave up on us -- all three vanish from the station table in
// exactly the same way.
func TestAClientsOwnDisconnectReasonIsAlwaysLogged(t *testing.T) {
	e := &Engine{}
	const deauthReason7 = "c0003c009cefd5f646c7fc9ca7937fed9cefd5f646c700000700"
	e.handleHostapdEvent("wlan-usb", "<3>AP-MGMT-FRAME-RECEIVED buf="+deauthReason7)

	line := lastEventText(t, e)
	if !strings.Contains(line, "deauthenticated itself") {
		t.Errorf("the client's own disconnect was not named: %s", line)
	}
	if strings.Contains(line, "reason code") {
		t.Errorf("the raw reason code leaked into the log: %s", line)
	}
}

// Verbose is off by default, and the activity view is unchanged when it is.
func TestAssociationCapabilitiesAreVerboseOnly(t *testing.T) {
	quiet := &Engine{}
	quiet.handleHostapdEvent("wlan-usb", "<3>AP-MGMT-FRAME-RECEIVED buf="+reassocFrame)
	if n := len(quiet.events.since(0, 10)); n != 0 {
		t.Fatalf("a reassociation logged %d events with verbose off, want 0", n)
	}

	loud := &Engine{}
	loud.setVerbose(true)
	loud.handleHostapdEvent("wlan-usb", "<3>AP-MGMT-FRAME-RECEIVED buf="+reassocFrame)

	line := lastEventText(t, loud)
	for _, want := range []string{"beacon table", "9c:ef:d5:f6:3f:f2"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q missing from: %s", want, line)
		}
	}
}

// A frame that cannot be read must not be able to shout. Client firmware sends
// malformed management frames repeatedly, and this must never become the
// loudest voice in the activity view.
func TestAnUnreadableFrameIsSilentUnlessAsked(t *testing.T) {
	quiet := &Engine{}
	quiet.handleHostapdEvent("wlan-usb", "<3>AP-MGMT-FRAME-RECEIVED buf=zzzz")
	if n := len(quiet.events.since(0, 10)); n != 0 {
		t.Errorf("an unreadable frame logged %d events with verbose off, want 0", n)
	}

	loud := &Engine{}
	loud.setVerbose(true)
	// setVerbose announces itself, so count what arrived AFTER that rather than
	// the whole ring.
	before := len(loud.events.since(0, 10))
	loud.handleHostapdEvent("wlan-usb", "<3>AP-MGMT-FRAME-RECEIVED buf=zzzz")
	if n := len(loud.events.since(0, 10)) - before; n != 1 {
		t.Errorf("an unreadable frame logged %d events with verbose on, want 1", n)
	}
	if line := lastEventText(t, loud); !strings.Contains(line, "could not be read") {
		t.Errorf("the unreadable frame was not reported: %s", line)
	}
}

// Turning the switch off makes lines stop appearing, which is exactly what a
// box that went quiet looks like. The log has to say which happened.
func TestTogglingVerboseIsItselfRecorded(t *testing.T) {
	e := &Engine{}
	e.setVerbose(true)
	if line := lastEventText(t, e); !strings.Contains(line, "what clients say") &&
		!strings.Contains(line, "associate") {
		t.Errorf("turning verbose on was not recorded: %s", line)
	}
	e.setVerbose(false)
	if line := lastEventText(t, e); !strings.Contains(line, "events only") {
		t.Errorf("turning verbose off was not recorded: %s", line)
	}
	// Setting it to what it already is must not fill the log with noise.
	n := len(e.events.since(0, 20))
	e.setVerbose(false)
	if got := len(e.events.since(0, 20)); got != n {
		t.Errorf("a no-op toggle logged %d extra events", got-n)
	}
}
