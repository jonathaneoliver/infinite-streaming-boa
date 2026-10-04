package boa

import (
	"strings"
	"testing"
)

// The activity log names which kind of move request was sent (#525). Before,
// steer, warn, term and force all wrote the same line, so a session tested by
// hand could not be read back by button.
func TestSteerEventNamesTheMode(t *testing.T) {
	want := map[steerMode]string{
		steerSuggest:   "steer:",
		steerImminent:  "warn:",
		steerTerminate: "term:",
		steerInsist:    "force:",
	}
	seen := map[string]bool{}
	for mode, label := range want {
		e := &Engine{}
		e.noteSteer("aa:bb:cc:dd:ee:ff", "phy1-ap0", "phy0-ap0", mode)
		evs := e.Events(0, 10)
		if len(evs) != 1 {
			t.Fatalf("%s: %d events, want 1", mode, len(evs))
		}
		text := evs[0].Text
		if !strings.Contains(text, label) {
			t.Errorf("%s: %q does not name the button %q", mode, text, label)
		}
		if seen[text] {
			t.Errorf("%s: %q is the same line another mode writes", mode, text)
		}
		seen[text] = true
	}
}
