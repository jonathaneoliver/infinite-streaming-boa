package boa

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

/*
 * Where the reason a radio is switched off is written down.
 *
 * rfkill records the STATE and not the reason, and the two things that write to
 * it mean quite different things by the same bit. The power control means "the
 * operator switched this off and it stays off". select-radio's unblock loop
 * means "clear the stale block left by the regime where an adapter rfkilled the
 * onboard radio", a regime that no longer exists. Both are soft = 1, so the
 * loop could not tell a decision taken thirty seconds ago from a leftover, and
 * unblocked either -- at boot AND on every radio hotplug, which on a box whose
 * USB adapter has a history of re-enumerating is a routine event (#186).
 *
 * That is not merely surprising. Radio power is a test instrument here: the
 * measurement IS "switch it off and watch what the client does", so an outage
 * that ends itself early does not startle the operator so much as invalidate
 * the run -- and did so with nothing in the activity log that would let the
 * result be questioned afterwards.
 *
 * So the reason is recorded beside the state, and the things that would
 * otherwise override it consult the reason rather than guess from the bit.
 *
 * A DIRECTORY OF MARKER FILES rather than a line in /etc/default or a JSON
 * document, for one reason: the other reader is /bin/sh. `[ -f "$dir/$iface" ]`
 * cannot misparse, needs no JSON parser built out of grep and sed, and cannot
 * be found half-written by a script that ran while the daemon was mid-write.
 *
 * IN /run, so a reboot clears it. That is the line a bench instrument wants
 * drawn: a radio switched off for a test comes back after a power cycle, and
 * only the things with no business overriding the operator -- a hotplug, the
 * daemon restart every deploy performs -- are stopped from undoing it. The unit
 * already says exactly that in systemd's own terms, with
 * RuntimeDirectory=infinite-streaming-boa and RuntimeDirectoryPreserve=restart.
 */

// radioOffDir holds one marker per radio that is off on purpose.
//
// Under the unit's RuntimeDirectory, which systemd creates before the daemon
// starts and keeps across a restart. A var rather than a const so a test can
// point it at a directory it is allowed to write to.
var radioOffDir = "/run/infinite-streaming-boa/radio-off"

// radioOffReason is why a radio is off, which is the part rfkill cannot record.
type radioOffReason string

const (
	// offByOperator is indefinite: it outlives a hotplug and a daemon restart,
	// and only the operator switching the radio back on ends it.
	offByOperator radioOffReason = "operator"
	// offByOutage is a timed cut the daemon intends to undo itself. It stops a
	// hotplug ending the outage early, but a daemon that DIED mid-outage must
	// not leave the radio off forever, so the startup restore clears it.
	offByOutage radioOffReason = "outage"
)

// radioOffMarkerPath names the marker for one interface, and refuses a name
// that has no business being one.
//
// Interface names reach SetRadioPower from a URL path segment, and a path
// segment is the one place a "/" or a ".." could arrive from. radioExists
// rejects those already by asking the kernel whether the link exists, but a
// function that joins caller-supplied text onto a path should not rely on a
// check made somewhere else.
func radioOffMarkerPath(iface string) (string, error) {
	if iface == "" || iface == "." || iface == ".." || iface != filepath.Base(iface) {
		return "", fmt.Errorf("not an interface name: %q", iface)
	}
	return filepath.Join(radioOffDir, iface), nil
}

// setRadioOffMarker records why a radio is off, or clears the record when why
// is empty.
func setRadioOffMarker(iface string, why radioOffReason) error {
	p, err := radioOffMarkerPath(iface)
	if err != nil {
		return err
	}
	if why == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clearing the off marker for %s: %w", iface, err)
		}
		return nil
	}
	if err := os.MkdirAll(radioOffDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", radioOffDir, err)
	}
	// Reason first so one field split reads it, timestamp after for whoever is
	// looking at this over SSH wondering why a radio will not come up. Nothing
	// but the first word is parsed.
	body := fmt.Sprintf("%s %s\n", why, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return fmt.Errorf("recording that %s is switched off: %w", iface, err)
	}
	return nil
}

// radioOffMarker reports why a radio is off on purpose, or "" if nothing says
// it is.
//
// An absent, unreadable or unrecognised marker answers "", and that is the safe
// way round. "" makes the box behave as every box did before this existed: the
// radio comes back on. Answering offByOperator on a file it could not read
// would strand a radio off on the strength of a guess, with no way back short
// of a reboot.
func radioOffMarker(iface string) radioOffReason {
	p, err := radioOffMarkerPath(iface)
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return ""
	}
	switch why := radioOffReason(f[0]); why {
	case offByOperator, offByOutage:
		return why
	default:
		return ""
	}
}
