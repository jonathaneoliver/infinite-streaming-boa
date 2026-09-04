package boa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

/*
 * Where a radio's channel is REMEMBERED, as against where it currently is.
 *
 * A channel move is applied to the running hostapd through its control socket,
 * because 802.11h CSA is refused by both drivers here (#154). That makes the
 * change live exactly as long as the process: measured 2026-09-04, a radio
 * serving on 149 came back on 36 after a restart, from a config nobody had
 * looked at in weeks (#183).
 *
 * The obvious fix -- write it into hostapd's config -- does not work, and not
 * for a fixable reason. customize.sh emits one config per ROLE, and two of them
 * declare interface=wlan0: boa-onboard24.conf at 2.4GHz and boa-onboard.conf at
 * 5GHz, with select-radio choosing between them by whether the USB adapter
 * enumerated. So "the config file for wlan0" has two answers in different
 * bands, and which is live depends on what is plugged in. A channel written to
 * the wrong one is silently lost AND arms the next USB dropout with a config
 * hostapd will refuse.
 *
 * The fact has no home in those files, so it gets one here: the operator's
 * choice, owned by the daemon, in one place, re-applied through the move path
 * that already validates and reads back.
 */

// ChannelPref is the operator's chosen channel for one radio.
type ChannelPref struct {
	Channel  int `json:"channel"`
	WidthMHz int `json:"width_mhz"`
	// Settled is where the radio ACTUALLY came up when this preference was
	// last applied, which is not always what was asked for.
	//
	// hostapd's 20/40MHz coexistence scan swaps primary and secondary when it
	// finds neighbours on the secondary, so a request for 36 at 80MHz comes up
	// on 40 -- documented in coexError, and the normal case on this box.
	// Comparing the live channel against Channel alone would therefore see a
	// permanent mismatch on a radio that is exactly where it was put, and
	// "re-apply on mismatch" would take the access point down and back up
	// every tick, forever. Recording where it landed is what makes the
	// comparison terminate.
	Settled int `json:"settled"`
}

// ChannelStore holds one preference per radio, keyed by interface name.
//
// By interface rather than by role: the interface is what the operator pointed
// at, what the event log names, and what MoveChannel takes. A radio that is
// absent simply has a preference nothing consults, which is the right
// behaviour for an adapter that has been unplugged.
//
// A separate file from policy.json for the same reason patterns and the ladder
// are: that file is a bare object keyed by MAC, and folding a box-level value
// into it would change its shape and need a migration on every existing box.
type ChannelStore struct {
	mu   sync.RWMutex
	path string
	pref map[string]ChannelPref
}

func NewChannelStore(path string) *ChannelStore {
	s := &ChannelStore{path: path, pref: map[string]ChannelPref{}}
	s.load()
	return s
}

func (s *ChannelStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return // first run: an absent file is normal, not an error
	}
	var pref map[string]ChannelPref
	if json.Unmarshal(raw, &pref) == nil {
		s.pref = pref
	}
}

// save writes atomically, for the reason the other stores do: a Pi loses power
// without warning, and this box loses it ON PURPOSE.
func (s *ChannelStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.pref, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get reports the preference for a radio, and whether there is one.
func (s *ChannelStore) Get(iface string) (ChannelPref, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pref[iface]
	return p, ok
}

// Put records where a radio was put, and where it actually landed.
//
// Called only after a move whose access point came back up: a preference is a
// place the radio has served, never merely a place it was asked for. Storing an
// intent that was never reached would have the tick chase it forever.
func (s *ChannelStore) Put(iface string, p ChannelPref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pref == nil {
		s.pref = map[string]ChannelPref{}
	}
	s.pref[iface] = p
	return s.save()
}

// All returns a copy of every preference, for the export path and for tests.
func (s *ChannelStore) All() map[string]ChannelPref {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]ChannelPref, len(s.pref))
	for k, v := range s.pref {
		out[k] = v
	}
	return out
}

// satisfiedBy reports whether a radio sitting on this channel and width counts
// as being where the preference asked for.
//
// Either the channel asked for or the one it settled on, because the
// coexistence swap makes those different and both correct. Width must match
// outright: it is not swapped by anything, and a radio that fell back to 20MHz
// is a radio that is not where it was put.
func (p ChannelPref) satisfiedBy(channel, widthMHz int) bool {
	if channel == 0 {
		return true // nothing readable; not a mismatch, just no answer
	}
	if p.WidthMHz != 0 && widthMHz != 0 && widthMHz != p.WidthMHz {
		return false
	}
	return channel == p.Channel || (p.Settled != 0 && channel == p.Settled)
}
