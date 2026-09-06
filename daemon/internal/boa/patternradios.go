package boa

import (
	"fmt"
	"sort"
)

/*
 * Keeping an adapter pattern pointed at the radios the box actually has.
 *
 * A radio event names its interface as a string, and interface names here are
 * bound to a USB SOCKET rather than to a dongle -- so moving an adapter to
 * another port renames it, and a pattern authored against wlan-usb2 is left
 * naming something that no longer exists. Unplugging one and plugging in
 * another does the same thing.
 *
 * Left alone, those events do not disappear: they stay in the saved pattern,
 * are still sent at play time, and the daemon skips each one with "no interface
 * named wlan-usb2 on this box". The editor, meanwhile, builds its lanes from
 * the radios that exist, so it shows NOTHING for them. A pattern that looks
 * empty and is not is the failure this file exists to prevent.
 *
 * Three rules, in the order they are applied:
 *
 *   1. A radio that has gone, paired with a radio that has arrived, is
 *      RENAMED onto it. Swapping a dongle should not cost you the timeline you
 *      drew for it -- that is the same physical slot doing the same job under a
 *      new name.
 *   2. A radio that has gone with nothing to pair it with keeps its events,
 *      untouched. The editor shows the lane greyed out rather than hiding it,
 *      so an operator can see what they still have and delete it deliberately.
 *   3. A radio that has arrived with nothing to inherit gets an empty lane, by
 *      simply existing in the current list. Nothing to do here.
 *
 * No extra state is persisted for this. The radios a pattern was authored
 * against are exactly the ones its own events name, so "gone" and "spare" are a
 * comparison rather than a memory -- and a remembered list is one more thing
 * that can be wrong after a crash.
 */

// patternRadios lists the interfaces a pattern's radio events name, sorted.
func patternRadios(p Pattern) []string {
	seen := map[string]bool{}
	var out []string
	for _, ev := range p.Radios {
		if ev.Iface != "" && !seen[ev.Iface] {
			seen[ev.Iface] = true
			out = append(out, ev.Iface)
		}
	}
	sort.Strings(out)
	return out
}

// OrphanedRadios lists interfaces a pattern still names that the box no longer
// has. The editor uses this to show a lane greyed out rather than not at all.
func OrphanedRadios(p Pattern, have []string) []string {
	live := map[string]bool{}
	for _, w := range have {
		live[w] = true
	}
	var out []string
	for _, w := range patternRadios(p) {
		if !live[w] {
			out = append(out, w)
		}
	}
	return out
}

// reconcileAdapterPattern renames a pattern's radio events onto the radios the
// box has now, pairing each departed radio with an arrived one.
//
// Returns the pattern, one line per rename for the activity log, and whether
// anything changed. Deterministic: both lists are sorted before pairing, so the
// same swap produces the same mapping every time rather than depending on map
// iteration order.
func reconcileAdapterPattern(p Pattern, have []string) (Pattern, []string, bool) {
	if len(p.Radios) == 0 || len(have) == 0 {
		return p, nil, false
	}
	live := map[string]bool{}
	for _, w := range have {
		live[w] = true
	}
	used := map[string]bool{}
	for _, w := range patternRadios(p) {
		used[w] = true
	}

	// Gone: named by the pattern, absent from the box.
	var gone []string
	for _, w := range patternRadios(p) {
		if !live[w] {
			gone = append(gone, w)
		}
	}
	if len(gone) == 0 {
		return p, nil, false
	}
	// Spare: present on the box, named by nothing in the pattern. Sorted, so a
	// swap maps the same way on every boot.
	var spare []string
	for _, w := range have {
		if !used[w] {
			spare = append(spare, w)
		}
	}
	sort.Strings(spare)

	rename := map[string]string{}
	for i, w := range gone {
		if i >= len(spare) {
			// Rule 2: nothing to pair it with. Its events stay exactly as they
			// are, and the editor greys the lane rather than hiding it.
			break
		}
		rename[w] = spare[i]
	}
	if len(rename) == 0 {
		return p, nil, false
	}

	out := p
	out.Radios = append([]RadioEvent(nil), p.Radios...)
	counts := map[string]int{}
	for i := range out.Radios {
		if to, ok := rename[out.Radios[i].Iface]; ok {
			counts[out.Radios[i].Iface]++
			out.Radios[i].Iface = to
		}
	}
	var notes []string
	for from, to := range rename {
		notes = append(notes, fmt.Sprintf(
			"%s is gone and %s is new, so the %d event(s) drawn for %s now run on %s",
			from, to, counts[from], from, to))
	}
	sort.Strings(notes)
	return out, notes, true
}
