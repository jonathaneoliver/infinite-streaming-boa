package boa

/*
 * One name per frame, and a way back from the names we used to use. See #229.
 *
 * The same 802.11 deauthentication used to be called "drop" on a client card,
 * "deauth" on a radio lane, `deauth` in the URL and `LinkDrop` in the code; a
 * disassociation was "nudge", "disassoc" and `LinkNudge`. Three vocabularies
 * for two frames, so the activity log mixed them in one list and nothing said
 * that a radio lane's deauth and a client button's drop were one action.
 *
 * The rule now, and the reason `deadzone`, `gather` and `evict` keep their
 * invented names while these lose theirs:
 *
 *	if it is 1-1 with something in the standard, use the standard's word;
 *	if this box invented the composition, use a plain word -- and never a
 *	standard-sounding word for an invented thing.
 *
 * A deauthentication and a disassociation are exactly what an 802.11 reader
 * expects them to be, so they take the standard's names. A deadzone is a deny
 * ACL plus a deauth plus a timer, which is nothing in the standard, so it keeps
 * a word of our own.
 *
 * THE MIGRATION IS THE RISKY PART. These strings are persisted: they sit in
 * saved patterns in the library on disk and in exported files. Renaming the
 * constant without rewriting what has already been written would leave every
 * saved pattern carrying a kind no reader matches -- and the readers here skip
 * an unknown kind rather than failing, so a link lane would quietly stop doing
 * anything with nothing on screen to say so.
 *
 * So there is ONE normaliser, applied on the read path, rather than a switch in
 * each reader. A missed reader is exactly the silent failure above.
 */

// legacyKinds maps every name this box has ever written to the one it writes
// now. Kept as data rather than a switch so the test can walk it.
var legacyKinds = map[string]string{
	"drop":  LinkDeauth,
	"nudge": LinkDisassoc,
	"off":   RadioOff,
	// Three spellings of what is now the SILENT lane, and each is on disk
	// somewhere. "apdown" is the original; "ap-down" and "disable-ap" were
	// stops on the way here, each shipped long enough to be written into a real
	// patterns.json. A migration that only covers the oldest name is not a
	// migration.
	//
	// All map to the silent lane because that is what every one of them DID:
	// the announced behaviour existed for one deploy under a name that claimed
	// the opposite, and reading those events as announced would change what a
	// saved pattern does rather than what it is called.
	"apdown":  RadioAPDown,
	"ap-down": RadioAPDown,
}

// normaliseKind upgrades one persisted kind. Unknown values pass through
// untouched: this is a rename, not a validator, and refusing here would turn a
// pattern written by a newer build into an error rather than something a reader
// can decline on its own terms.
func normaliseKind(kind string) string {
	if to, ok := legacyKinds[kind]; ok {
		return to
	}
	return kind
}

// NormalisePattern rewrites a pattern's link and radio kinds to current names.
//
// Returns whether anything changed, so a caller reading from disk can save the
// upgrade back rather than redoing it on every read -- and so a caller that
// only displays does not have to.
func NormalisePattern(p Pattern) (Pattern, bool) {
	changed := false
	links := make([]LinkEvent, len(p.Links))
	copy(links, p.Links)
	for i := range links {
		if k := normaliseKind(links[i].Kind); k != links[i].Kind {
			links[i].Kind = k
			changed = true
		}
	}
	radios := make([]RadioEvent, len(p.Radios))
	copy(radios, p.Radios)
	for i := range radios {
		if k := normaliseKind(radios[i].Kind); k != radios[i].Kind {
			radios[i].Kind = k
			changed = true
		}
	}
	if !changed {
		return p, false
	}
	out := p
	out.Links = links
	out.Radios = radios
	return out, true
}
