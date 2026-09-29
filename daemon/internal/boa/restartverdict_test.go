package boa

import "testing"

// #445: a restart-mode move compared only the channel hostapd reported, and
// hostapd reports its CONFIGURED channel whether or not the BSS came up. So a
// dead access point on the right channel number read as a successful move.
func TestARestartMoveOnlyFailsOnAnAnsweredDownAP(t *testing.T) {
	for _, c := range []struct {
		name                       string
		wasEnabled, enabled, known bool
		want                       restartOutcome
	}{
		// The #445 case: hostapd answers, and the access point is not up.
		{"answered, not enabled", true, false, true, restartDown},
		{"back up", true, true, true, restartUp},
		// A control socket that has gone quiet is a radio re-initialising as
		// often as a broken one -- over two minutes on mt7921u. Failing the
		// move on a timeout would be the confident wrong answer apState exists
		// to prevent, so it is reported as not known.
		{"hostapd not answering", true, false, false, restartUnconfirmed},
		// A radio that was not serving before the move was never taken down
		// by it, so its state after cannot be the move's failure.
		{"was not serving", false, false, true, restartUp},
		{"was not serving, not answering", false, false, false, restartUp},
	} {
		if got := restartVerdict(c.wasEnabled, c.enabled, c.known); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
