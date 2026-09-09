package boa

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempOffDir points the marker directory at a directory the test may write
// to. /run/infinite-streaming-boa does not exist on a developer's laptop, and
// the daemon is developed on macOS.
func useTempOffDir(t *testing.T) string {
	t.Helper()
	prev := radioOffDir
	radioOffDir = filepath.Join(t.TempDir(), "radio-off")
	t.Cleanup(func() { radioOffDir = prev })
	return radioOffDir
}

// The round trip a power switch actually makes: nothing recorded, off, back on.
// "Nothing recorded" must read as "" rather than as an error, because that is
// every radio on every box until someone switches one off.
func TestRadioOffMarkerRoundTrip(t *testing.T) {
	useTempOffDir(t)

	if why := radioOffMarker("wlan0"); why != "" {
		t.Fatalf("a radio nobody has touched: got %q, want \"\"", why)
	}
	if err := setRadioOffMarker("wlan0", offByOperator); err != nil {
		t.Fatalf("recording the operator's switch-off: %v", err)
	}
	if why := radioOffMarker("wlan0"); why != offByOperator {
		t.Fatalf("after switching off: got %q, want %q", why, offByOperator)
	}
	// A second radio must be unaffected -- the marker is per interface, and a
	// two-radio box is the case this whole mechanism exists for.
	if why := radioOffMarker("wlan-usb"); why != "" {
		t.Fatalf("the other radio: got %q, want \"\"", why)
	}
	if err := setRadioOffMarker("wlan0", ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if why := radioOffMarker("wlan0"); why != "" {
		t.Fatalf("after switching back on: got %q, want \"\"", why)
	}
}

// Clearing a marker that is not there is the normal case, not an error: every
// switch-ON of a radio that was already on takes this path.
func TestRadioOffMarkerClearAbsentIsNotAnError(t *testing.T) {
	useTempOffDir(t)
	if err := setRadioOffMarker("wlan0", ""); err != nil {
		t.Fatalf("clearing an absent marker: %v", err)
	}
}

// The reason is what separates a decision from a timed cut, so it has to
// survive the round trip -- restoreRadioPower restores one and honours the
// other.
func TestRadioOffMarkerKeepsTheReason(t *testing.T) {
	useTempOffDir(t)
	for _, why := range []radioOffReason{offByOperator, offByOutage} {
		if err := setRadioOffMarker("wlan0", why); err != nil {
			t.Fatalf("recording %q: %v", why, err)
		}
		if got := radioOffMarker("wlan0"); got != why {
			t.Fatalf("reason %q: got %q", why, got)
		}
	}
}

// A marker whose first word is not a reason this build knows answers "", which
// switches the radio back on. The other way round -- treating an unreadable
// file as the operator's decision -- would strand a radio off on a guess, with
// no way back short of a reboot.
func TestRadioOffMarkerUnrecognisedReadsAsNoDecision(t *testing.T) {
	dir := useTempOffDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "\n", "banana 2026-09-04T10:00:00Z"} {
		if err := os.WriteFile(filepath.Join(dir, "wlan0"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if why := radioOffMarker("wlan0"); why != "" {
			t.Fatalf("marker %q: got %q, want \"\"", body, why)
		}
	}
}

// The interface name is joined onto a path and arrives from a URL path segment,
// so a name that would escape the directory is refused rather than sanitised.
func TestRadioOffMarkerPathRefusesNonInterfaceNames(t *testing.T) {
	useTempOffDir(t)
	for _, bad := range []string{"", ".", "..", "../etc/shadow", "a/b", "/wlan0"} {
		if _, err := radioOffMarkerPath(bad); err == nil {
			t.Errorf("%q was accepted as an interface name", bad)
		}
		// And the writers refuse it too, rather than only the path builder.
		if err := setRadioOffMarker(bad, offByOperator); err == nil {
			t.Errorf("%q was accepted by setRadioOffMarker", bad)
		}
		if why := radioOffMarker(bad); why != "" {
			t.Errorf("%q read back as %q", bad, why)
		}
	}
}

// The directory is created on demand. systemd makes the RuntimeDirectory but
// not this subdirectory of it, and the first switch-off on a fresh boot is
// where that shows up.
func TestRadioOffMarkerCreatesItsDirectory(t *testing.T) {
	dir := useTempOffDir(t)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the test started with the directory already there")
	}
	if err := setRadioOffMarker("wlan0", offByOperator); err != nil {
		t.Fatalf("first switch-off after a boot: %v", err)
	}
	if why := radioOffMarker("wlan0"); why != offByOperator {
		t.Fatalf("got %q, want %q", why, offByOperator)
	}
}

// An access point down on purpose must still say so after a daemon restart.
//
// The radio's power already survived one, because radioOffMarker records it.
// The ACCESS POINT did not, and checkRadiosAtStart cannot tell a deliberately
// disabled BSS from a radio that failed to come back: both are a powered radio
// with nothing on the air. Measured 2026-09-09, an operator's `disable AP` was
// undone about fifty seconds after a restart, with the log announcing the
// rescue (#282).
//
// The two reasons are asserted separately because they are meant to end
// differently: an operator's decision is indefinite, a timed outage is one the
// daemon intends to undo itself and must not strand if it dies mid-cut.
func TestAPOffIntentSurvivesAndSaysWhy(t *testing.T) {
	apOffDir = t.TempDir()

	if got := apOffMarker("wlan-test"); got != "" {
		t.Fatalf("nothing has been recorded, so the marker must be empty, got %q", got)
	}

	for _, why := range []radioOffReason{offByOperator, offByOutage} {
		if err := setAPOffMarker("wlan-test", why); err != nil {
			t.Fatalf("recording %q: %v", why, err)
		}
		if got := apOffMarker("wlan-test"); got != why {
			t.Errorf("recorded %q, read back %q -- the startup check reads this "+
				"to decide whether to rebuild", why, got)
		}
	}

	if err := setAPOffMarker("wlan-test", ""); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got := apOffMarker("wlan-test"); got != "" {
		t.Errorf("cleared, but still reads %q", got)
	}
}

// An unreadable or unknown marker must answer "", so the access point comes
// back. Failing the other way stands an AP down on a guess, with nothing short
// of a reboot to undo it -- the same reasoning radioOffMarker records.
func TestAPOffIntentFailsTowardsServing(t *testing.T) {
	apOffDir = t.TempDir()
	p, err := apOffMarkerPath("wlan-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("gremlins 2026-09-09T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := apOffMarker("wlan-test"); got != "" {
		t.Errorf("an unrecognised reason must read as %q so the AP returns, got %q", "", got)
	}
}

// A path that is not a bare interface name is refused, for the reason
// radioOffMarkerPath is: the name arrives from a URL path segment, which is the
// one place a "/" or a ".." can come from.
func TestAPOffMarkerPathRefusesTraversal(t *testing.T) {
	apOffDir = t.TempDir()
	for _, bad := range []string{"", ".", "..", "../escape", "wlan/0"} {
		if _, err := apOffMarkerPath(bad); err == nil {
			t.Errorf("%q was accepted as an interface name", bad)
		}
	}
}
