package pifi

import (
	"fmt"
	"sort"
)

// ConfigVersion is the schema version of an exported configuration.
//
// Written on export. On import, OLDER documents are accepted and newer ones
// refused, which is the asymmetry that matters: a document from the past is
// one this code has already understood, while a document from the future would
// be half-understood, and half-understanding it means writing a policy the
// operator did not describe and then conditioning traffic by it. A refused
// import is recoverable; that is not.
//
// The obligation this places on every future field: absent must mean what
// happened before the field existed. A file saved today has no opinion about a
// setting invented tomorrow, so tomorrow's zero value has to be today's
// behaviour. A field that cannot honour that needs a version bump AND a
// migration, not just a bump -- because a bump alone no longer refuses the old
// file, it accepts it and gets it wrong.
const ConfigVersion = 2

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
	// Patterns is the box's saved pattern library, sorted by name. Built-ins
	// are absent on purpose: they are generated from whichever ladder a device
	// has, so storing them would freeze a shape that is meant to track the
	// content, and restoring one onto another box would describe that box's
	// ladders wrongly.
	Patterns []Pattern `json:"patterns,omitempty"`
	// Devices is sorted by MAC. Go randomises map iteration, and these
	// documents are meant to be committed to a repository and diffed, where
	// unstable ordering turns a no-op export into a whole-file change.
	// Devices is READ but no longer written; see ExportConfig for why. Kept so
	// a version 1 document, which carried them, still restores what it can.
	Devices []Policy `json:"devices,omitempty"`
	// Ladder is the box's one measured ladder: the only genuinely expensive
	// thing here, an hour of a real device streaming real content, and the
	// reason this document exists at all.
	Ladder *Ladder `json:"ladder,omitempty"`
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
// Devices are no longer exported, as of version 2.
//
// Measured on a real box, they were half the document and almost none of it was
// worth keeping: a MAC and a label that mean nothing anywhere else, a `rev`
// that is one box's optimistic-concurrency counter, sub-classes and shapes that
// are working state an operator re-sets per test -- and a baked copy of each
// device's loaded pattern, roughly nine kilobytes of it, duplicating what the
// library already holds.
//
// What was worth keeping was the ladder, and that is no longer device-specific
// either: it is one ladder for the box (see LadderStore). Lifting it out leaves
// a document with no MACs in it at all, which is both a smaller backup and a
// thing that can simply be sent to someone.
func ExportConfig(ladder *Ladder, patterns []Pattern) ConfigExport {
	out := ConfigExport{Version: ConfigVersion, ExportedAt: nowMs(), Ladder: ladder}
	for _, pat := range patterns {
		if !IsBuiltin(pat.Name) {
			out.Patterns = append(out.Patterns, pat)
		}
	}
	sort.Slice(out.Patterns, func(i, j int) bool {
		return out.Patterns[i].Name < out.Patterns[j].Name
	})
	return out
}

// Validate checks a document completely, before any of it is applied.
//
// All-or-nothing on purpose. A partially applied configuration leaves the box
// conditioning traffic by a state that is neither the old one nor the new one,
// and nothing in the UI would say so.
func (c ConfigExport) Validate() error {
	if c.Version > ConfigVersion {
		return fmt.Errorf(
			"config version %d is newer than this box understands (%d)",
			c.Version, ConfigVersion)
	}
	if c.Version < 1 {
		return fmt.Errorf("config version %d is not a version", c.Version)
	}
	// Devices OR patterns. Requiring devices made a pattern library
	// unshareable, and patterns are not device-specific: PatternStore is keyed
	// by name, and since generation moved to one ladder for the box they are
	// not device-specific in their VALUES either. A document carrying only
	// patterns is a pattern library, which is a thing worth sending someone.
	//
	// Empty of both is still refused: that is not a configuration, and applying
	// it in replace mode would delete every device to honour a document that
	// says nothing.
	if len(c.Devices) == 0 && len(c.Patterns) == 0 && c.Ladder == nil {
		return fmt.Errorf("config contains no ladder, patterns or devices")
	}
	if c.Ladder != nil && len(c.Ladder.Rungs) < 2 {
		return fmt.Errorf("ladder: needs at least 2 rungs, got %d", len(c.Ladder.Rungs))
	}
	seenPattern := map[string]bool{}
	for i, pat := range c.Patterns {
		name := normPatternName(pat.Name)
		if name == "" {
			return fmt.Errorf("pattern %d: a saved pattern needs a name", i)
		}
		if IsBuiltin(name) {
			return fmt.Errorf(
				"pattern %q is a built-in name; built-ins are generated, not stored",
				name)
		}
		if seenPattern[name] {
			return fmt.Errorf("pattern %q appears twice", name)
		}
		seenPattern[name] = true
		if err := validPattern(pat); err != nil {
			return fmt.Errorf("pattern %q: %w", name, err)
		}
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
