package pifi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// PatternStore holds the box's saved patterns, by name.
//
// Box-level rather than per-device on purpose: a pattern is a SCENARIO, not a
// property of one phone. "The lift drops to 1.5 Mbps for thirty seconds" is the
// same test whichever device you point it at, and per-device storage would mean
// re-authoring it for every client you ever test. Ladder-relative built-ins
// still resolve against whichever device is playing them, so being box-level
// costs nothing in specificity.
//
// A separate file from policy.json, deliberately. Policies are already
// persisted as a bare JSON object keyed by MAC; folding patterns into that file
// would change its shape and every box in existence would need a migration on
// first boot, for no benefit over a second small file.
type PatternStore struct {
	mu   sync.RWMutex
	path string
	pat  map[string]Pattern
}

func NewPatternStore(path string) *PatternStore {
	s := &PatternStore{path: path, pat: map[string]Pattern{}}
	s.load()
	return s
}

func (s *PatternStore) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return // first run: an absent file is normal, not an error
	}
	var pat map[string]Pattern
	if json.Unmarshal(raw, &pat) == nil {
		s.pat = pat
	}
}

// save writes atomically, for the same reason the policy store does: a Pi loses
// power without warning, and a torn file would lose every saved pattern rather
// than the one being written.
func (s *PatternStore) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.pat, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// normPatternName is the storage key: trimmed and lowercased, so "Morning Peak"
// and "morning peak" are one pattern rather than two that look identical in a
// list.
func normPatternName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (s *PatternStore) Get(name string) (Pattern, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pat[normPatternName(name)]
	return p, ok
}

// All returns every saved pattern, sorted by name so a list does not reorder
// itself between requests.
func (s *PatternStore) All() []Pattern {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Pattern, 0, len(s.pat))
	for _, p := range s.pat {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Put saves a pattern under its own name.
//
// A built-in name is refused. Built-ins are derived from the device's ladder
// and reshape themselves when it is re-swept; a saved pattern shadowing one
// would be a frozen copy wearing the same label, and "valley" would quietly
// mean something different here than everywhere else.
func (s *PatternStore) Put(p Pattern) error {
	name := normPatternName(p.Name)
	if name == "" {
		return fmt.Errorf("a saved pattern needs a name")
	}
	if IsBuiltin(name) {
		return fmt.Errorf(
			"%q is a built-in pattern; save your edit under a different name",
			name)
	}
	if err := validPattern(p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Name = name
	s.pat[name] = p
	return s.save()
}

func (s *PatternStore) Delete(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = normPatternName(name)
	if _, ok := s.pat[name]; !ok {
		return false, nil
	}
	delete(s.pat, name)
	return true, s.save()
}

// ReplaceAll swaps the whole set in one write, for a configuration import.
func (s *PatternStore) ReplaceAll(ps []Pattern) error {
	next := make(map[string]Pattern, len(ps))
	for _, p := range ps {
		name := normPatternName(p.Name)
		if name == "" || IsBuiltin(name) {
			continue // validated already; belt and braces
		}
		p.Name = name
		next[name] = p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.pat
	s.pat = next
	if err := s.save(); err != nil {
		s.pat = prev // keep memory and disk agreeing
		return err
	}
	return nil
}
