package boa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LadderStore holds THE ladder: one per box, not one per device per service.
//
// # Why the box owns it
//
// Because the numbers in it belong to the box. Both ladders measured here -- a
// native player and a self-hosted stream, different content and codecs --
// produced identical up_at values while their costs differed throughout, and
// that is not a coincidence: up_at is the cap at which the SWEEP saw a switch,
// and a sweep climbs a fixed schedule. It is a property of the instrument. See
// GlobalLadder for the full argument.
//
// Keeping it under a device meant the one genuinely expensive artefact on the
// box -- an hour of real streaming -- could only be backed up by exporting
// every device's MAC, label, sub-classes and a baked copy of its loaded
// pattern along with it. Half of that export was device state nobody wanted and
// none of it was portable.
//
// # A separate file, for the same reason patterns are
//
// policy.json is a bare object keyed by MAC. Folding a box-level value into it
// would change that file's shape and need a migration on every existing box,
// for no benefit over a second very small file.
type LadderStore struct {
	mu   sync.RWMutex
	path string
	lad  Ladder
	set  bool
}

func NewLadderStore(path string) *LadderStore {
	s := &LadderStore{path: path}
	s.load()
	return s
}

func (s *LadderStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return // absent is normal: nothing has been swept, or this is a new box
	}
	var l Ladder
	if json.Unmarshal(raw, &l) != nil || len(l.Rungs) < 2 {
		return
	}
	s.lad, s.set = l, true
}

// Get reports the stored ladder, and whether there is one.
func (s *LadderStore) Get() (Ladder, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lad, s.set
}

// Put replaces the ladder. A ladder too short to walk is refused rather than
// stored: it cannot drive a pattern, and storing it would shadow a usable one.
func (s *LadderStore) Put(l Ladder) error {
	if len(l.Rungs) < 2 {
		return fmt.Errorf("a ladder needs at least 2 rungs, got %d", len(l.Rungs))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lad, s.set = l, true
	return s.save()
}

// save writes atomically, for the same reason the other stores do: a Pi loses
// power without warning, and a half-written ladder is worse than none.
func (s *LadderStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.lad, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
