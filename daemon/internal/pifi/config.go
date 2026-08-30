package pifi

import (
	"fmt"
	"sort"
)

// ConfigVersion is the schema version of an exported configuration.
//
// Written on export and checked on import. A refused import is recoverable; an
// import that half-understands a future document is not, because it would write
// a policy the operator did not describe and then condition traffic by it.
const ConfigVersion = 1

// ConfigExport is the whole box's operator intent in one document: every
// device's conditioning, sub-classes, ladders and pattern.
//
// It exists because a profile spans four endpoints -- PATCH /policy for the
// shapes, PUT /pattern, one PUT per ladder and one POST per sub-class -- and
// reassembling a device by hand from those is both tedious and easy to get
// half-right. A ladder in particular costs an hour of a real device streaming
// real content, and the box is not the system of record: reflashing replaces
// the whole image.
//
// Telemetry is deliberately absent, for the same reason the Store never
// persists it: it rebuilds itself on restart and means nothing on another box.
type ConfigExport struct {
	Version int `json:"version"`
	// ExportedAt is informational. Import never reads it -- a configuration is
	// applied because the operator asked, not because it is recent.
	ExportedAt int64 `json:"exported_at"`
	// Devices is sorted by MAC. Go randomises map iteration, and these
	// documents are meant to be committed to a repository and diffed, where
	// unstable ordering turns a no-op export into a whole-file change.
	Devices []Policy `json:"devices"`
}

// ImportMode decides what happens to devices the document does not mention.
type ImportMode string

const (
	// ImportMerge upserts the devices in the document and leaves every other
	// device alone. The default, because it is the one that cannot destroy
	// configuration the operator did not mean to touch.
	ImportMerge ImportMode = "merge"
	// ImportReplace additionally deletes devices absent from the document, so
	// the box ends up matching it exactly.
	ImportReplace ImportMode = "replace"
)

// ExportConfig snapshots the store as a document.
//
// Rev is cleared. It is the live optimistic-concurrency counter for one box's
// edit history: exporting it invites a restore to carry a stale value that
// belongs to a different timeline, and it would churn the diff of a committed
// file on every unrelated edit.
func ExportConfig(all map[string]Policy) ConfigExport {
	out := ConfigExport{Version: ConfigVersion, ExportedAt: nowMs()}
	for _, p := range all {
		p.Rev = 0
		out.Devices = append(out.Devices, p)
	}
	sort.Slice(out.Devices, func(i, j int) bool {
		return out.Devices[i].MAC < out.Devices[j].MAC
	})
	return out
}

// Validate checks a document completely, before any of it is applied.
//
// All-or-nothing on purpose. A partially applied configuration leaves the box
// conditioning traffic by a state that is neither the old one nor the new one,
// and nothing in the UI would say so.
func (c ConfigExport) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("unsupported config version %d, expected %d",
			c.Version, ConfigVersion)
	}
	if len(c.Devices) == 0 {
		return fmt.Errorf("config contains no devices")
	}
	seen := map[string]bool{}
	for i, p := range c.Devices {
		mac := normMAC(p.MAC)
		if mac == "" {
			return fmt.Errorf("device %d: mac is required", i)
		}
		if seen[mac] {
			// Last-wins would silently discard one of them, and which one
			// depends on document order rather than on anything the operator
			// intended.
			return fmt.Errorf("device %s appears twice", mac)
		}
		seen[mac] = true
		if err := validShape(p.Down); err != nil {
			return fmt.Errorf("device %s: down: %w", mac, err)
		}
		if err := validShape(p.Up); err != nil {
			return fmt.Errorf("device %s: up: %w", mac, err)
		}
		for _, l := range p.Ladders {
			if l.Service == "" {
				return fmt.Errorf("device %s: a ladder needs a service", mac)
			}
			if len(l.Rungs) == 0 {
				return fmt.Errorf("device %s: ladder %q has no rungs",
					mac, l.Service)
			}
			for _, r := range l.Rungs {
				if r.Mbps <= 0 || r.Mbps > 10000 {
					return fmt.Errorf(
						"device %s: ladder %q: each rung must be between 0 and 10000 Mbps",
						mac, l.Service)
				}
			}
		}
		if p.Pattern != nil {
			if err := validPattern(*p.Pattern); err != nil {
				return fmt.Errorf("device %s: pattern: %w", mac, err)
			}
		}
	}
	return nil
}

// Apply merges a validated document into the current policies and returns the
// result, along with the MACs it touched and removed.
//
// Ladder provenance is carried through UNCHANGED, which is the one place this
// differs from every other write path. A hand PUT demotes a ladder to "typed",
// so that the authority of a measurement is never attached to a number nobody
// measured -- but a restore is not a hand edit, and demoting on import would
// mean a box could never be returned to the state it was backed up from. The
// document is operator-owned, and is trusted the way any configuration file is.
func (c ConfigExport) Apply(cur map[string]Policy, mode ImportMode) (
	next map[string]Policy, wrote []string, removed []string) {

	next = make(map[string]Policy, len(cur))
	for k, v := range cur {
		next[k] = v
	}
	inDoc := map[string]bool{}
	for _, p := range c.Devices {
		mac := normMAC(p.MAC)
		inDoc[mac] = true
		p.MAC = mac
		// Preserve the live counter: importing must not reset other clients'
		// view of how many times this device has changed, or their next write
		// would be accepted against a revision they never saw.
		if prev, ok := next[mac]; ok {
			p.Rev = prev.Rev + 1
		} else {
			p.Rev = 1
		}
		next[mac] = p
		wrote = append(wrote, mac)
	}
	if mode == ImportReplace {
		for mac := range next {
			if !inDoc[mac] {
				delete(next, mac)
				removed = append(removed, mac)
			}
		}
	}
	sort.Strings(wrote)
	sort.Strings(removed)
	return next, wrote, removed
}
