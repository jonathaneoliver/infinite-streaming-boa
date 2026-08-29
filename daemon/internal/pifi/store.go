package pifi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store persists operator intent -- and only operator intent. Telemetry is
// never written: it is cheap to rebuild on restart and would otherwise wear out
// the SD card, which is the usual way a Pi appliance dies.
//
// Policies are keyed by MAC because that is the one identifier stable across a
// DHCP renewal, a reboot, and a client roaming between the wireless and wired
// ports. Keying by IP would silently detach a policy from its device.
type Store struct {
	mu   sync.RWMutex
	path string
	pol  map[string]Policy
}

func NewStore(path string) *Store {
	s := &Store{path: path, pol: map[string]Policy{}}
	s.load()
	return s
}

func (s *Store) load() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return // first run: an absent file is normal, not an error
	}
	var pol map[string]Policy
	if json.Unmarshal(raw, &pol) == nil {
		s.pol = pol
	}
}

// save writes atomically. A Pi loses power without warning; a torn policy file
// would leave the box conditioning traffic according to half a config, so the
// rename makes the switch all-or-nothing.
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.pol, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Get(mac string) (Policy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pol[normMAC(mac)]
	return p, ok
}

func (s *Store) All() map[string]Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Policy, len(s.pol))
	for k, v := range s.pol {
		out[k] = v
	}
	return out
}

func (s *Store) Put(p Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.MAC = normMAC(p.MAC)
	s.pol[p.MAC] = p
	return s.save()
}

func (s *Store) Delete(mac string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pol, normMAC(mac))
	return s.save()
}
