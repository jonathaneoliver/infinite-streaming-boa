package boa

import (
	"strings"
	"testing"
)

// keysOf names the parameters a channel move speaks about, whatever it says
// about them. `SET ht_capab ` with an empty value still counts: clearing a
// parameter is saying something about it, and the whole question here is which
// parameters a move leaves unmentioned.
func keysOf(cmds []string) map[string]string {
	out := map[string]string{}
	for _, c := range cmds {
		rest, ok := strings.CutPrefix(c, "SET ")
		if !ok {
			continue
		}
		k, v, _ := strings.Cut(rest, " ")
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// Every width must speak about every parameter any other width sets.
//
// This is the invariant behind #184. hostapd validates the channel parameters
// as a COUPLED GROUP, and each SET is applied to a BSS that still carries the
// previous channel's values -- so a width that stays silent about a parameter
// does not leave it at a default, it leaves it at whatever the last channel
// needed.
//
// MEASURED on the box 2026-09-04: a move from 149 at 80MHz to channel 40 at
// 40MHz emitted only `SET channel 40` and `SET ht_capab [HT40-]`, leaving
// vht_oper_chwidth at 1 and the centre index at 155 -- the middle of the UNII-3
// block, named as the centre of a channel in UNII-1. Every SET was
// acknowledged and the ENABLE then failed:
//
//	Wrong coupling between HT and VHT/HE channel setting
//	Could not set channel for kernel driver
//	Interface initialization failed
//
// leaving the radio powered with no access point on it. Asserting the
// invariant rather than that one transition, because the same silence at any
// other width would fail the same way.
func TestEveryWidthSpeaksAboutEveryCoupledParameter(t *testing.T) {
	type move struct{ ch, width int }
	var moves []move
	group := map[string]bool{}
	for ch, entry := range apChannels {
		for _, w := range []int{20, 40, 80} {
			if w > entry.maxWidth() {
				continue
			}
			moves = append(moves, move{ch, w})
			for k := range keysOf(setChannelCommands(entry, w)) {
				group[k] = true
			}
		}
	}
	for _, m := range moves {
		entry := apChannels[m.ch]
		said := keysOf(setChannelCommands(entry, m.width))
		for k := range group {
			// 2.4GHz has no VHT/HE centre to carry, so the group is per band.
			if entry.is24() && strings.Contains(k, "centr_freq") {
				continue
			}
			if _, ok := said[k]; !ok {
				t.Errorf("channel %d at %dMHz says nothing about %s, so the "+
					"previous channel's value survives into the BSS",
					m.ch, m.width, k)
			}
		}
	}
}

// The transition that actually failed, stated as itself.
func TestNarrowingFrom80ClearsTheWideSettings(t *testing.T) {
	got := keysOf(setChannelCommands(apChannels[40], 40))
	for key, want := range map[string]string{
		"channel":                      "40",
		"ht_capab":                     "[HT40-]",
		"vht_oper_chwidth":             "0",
		"he_oper_chwidth":              "0",
		"vht_oper_centr_freq_seg0_idx": "38",
		"he_oper_centr_freq_seg0_idx":  "38",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
}

// At 40MHz the centre is the middle of the PAIR, not the primary, so both
// members of a pair name the same one whichever of them is primary. Taken from
// SecOffset so it cannot disagree with the ht_capab side written beside it.
func TestThe40MHzCentreIsTheMiddleOfThePair(t *testing.T) {
	for _, pair := range [][2]int{{36, 40}, {44, 48}, {149, 153}, {157, 161}} {
		a := keysOf(setChannelCommands(apChannels[pair[0]], 40))["vht_oper_centr_freq_seg0_idx"]
		b := keysOf(setChannelCommands(apChannels[pair[1]], 40))["vht_oper_centr_freq_seg0_idx"]
		if a == "" || a != b {
			t.Errorf("%d and %d disagree on their 40MHz centre: %q vs %q",
				pair[0], pair[1], a, b)
		}
	}
}
