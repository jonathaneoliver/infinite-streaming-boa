package pifi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// History keeps a short throughput time series per client so a browser reload
// does not start from a blank chart.
//
// Before this, the series lived only in the page: refreshing threw away five
// minutes of context and you waited five more to get it back. The daemon
// computed each sample and immediately discarded it.
//
// Deliberately NOT part of the snapshot stream. Shipping the whole series in
// every server-sent event, once a second, would multiply the stream by roughly
// a hundred to re-send data the client already holds. It is fetched once on
// page load instead, and the live stream appends from there.
//
// Samples carry a timestamp rather than relying on an implied 1 Hz cadence.
// Without one, a gap -- a daemon restart, a sleeping laptop, a client that went
// away -- silently compresses into a continuous line, and the chart lies about
// when things happened.
type History struct {
	mu     sync.RWMutex
	max    int
	byMAC  map[string][]Sample
	warned bool
}

// Sample is one throughput observation, in the units the UI displays.
type Sample struct {
	T    int64   `json:"t"` // unix milliseconds
	Down float64 `json:"down"`
	Up   float64 `json:"up"`
}

const (
	// Matches the chart window: 300 samples at the 1 Hz tick.
	historyLen = 300
	// Clients quiet for longer than this are dropped, so a network with many
	// transient devices cannot grow the table without bound.
	historyIdle       = 30 * time.Minute
	historyMaxClients = 256
)

func NewHistory() *History {
	return &History{max: historyLen, byMAC: map[string][]Sample{}}
}

// Add records one sample for a client.
func (h *History) Add(mac string, s Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()

	series := h.byMAC[mac]
	if len(series) >= h.max {
		// Shift rather than reslice: s[1:] would share a backing array that
		// grows without bound as the window advances. Copying 300 elements at
		// 1 Hz is not worth optimising around.
		copy(series, series[1:])
		series = series[:h.max-1]
	}
	h.byMAC[mac] = append(series, s)
}

// Prune drops clients that have gone quiet, and caps the table.
func (h *History) Prune(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	cut := now.Add(-historyIdle).UnixMilli()
	for mac, series := range h.byMAC {
		if len(series) == 0 || series[len(series)-1].T < cut {
			delete(h.byMAC, mac)
		}
	}
	// A hard ceiling as well as an age limit: the age limit alone still allows
	// a burst of short-lived devices to balloon the table.
	for len(h.byMAC) > historyMaxClients {
		var oldest string
		var oldestAt int64
		for mac, series := range h.byMAC {
			t := series[len(series)-1].T
			if oldest == "" || t < oldestAt {
				oldest, oldestAt = mac, t
			}
		}
		delete(h.byMAC, oldest)
	}
}

// Snapshot returns a copy of every series.
func (h *History) Snapshot() map[string][]Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string][]Sample, len(h.byMAC))
	for mac, series := range h.byMAC {
		cp := make([]Sample, len(series))
		copy(cp, series)
		out[mac] = cp
	}
	return out
}

// Load restores a previously saved series.
//
// The path is expected to be on tmpfs: history must survive a daemon restart --
// which happens on every deploy -- but writing a time series to the SD card
// every few seconds is how a Pi appliance wears out its storage. RAM-backed
// gives the first without the second, and clears on reboot, which is fine.
func (h *History) Load(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string][]Sample
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	cut := time.Now().Add(-historyIdle).UnixMilli()
	h.mu.Lock()
	defer h.mu.Unlock()
	for mac, series := range m {
		if len(series) == 0 || series[len(series)-1].T < cut {
			continue // stale enough that showing it would mislead
		}
		if len(series) > h.max {
			series = series[len(series)-h.max:]
		}
		h.byMAC[mac] = series
	}
}

// Save writes the series out. Best-effort -- chart history must never be able
// to fail the daemon -- but NOT silent: the first failure is reported.
//
// A silent best-effort write hid a real bug here. The unit sets
// ProtectSystem=strict, which makes the whole filesystem read-only apart from
// the paths it names, so writes to /run were denied and history quietly failed
// to survive restarts with nothing to explain why.
func (h *History) Save(path string) {
	h.mu.RLock()
	raw, err := json.Marshal(h.byMAC)
	h.mu.RUnlock()
	if err != nil {
		h.warnOnce("marshal history: " + err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.warnOnce("create " + filepath.Dir(path) + ": " + err.Error())
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		h.warnOnce("write " + tmp + ": " + err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		h.warnOnce("rename " + tmp + ": " + err.Error())
	}
}

// warnOnce reports a persistence problem a single time. Repeating it every two
// minutes forever would be noise; saying nothing at all hid the last bug.
func (h *History) warnOnce(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.warned {
		return
	}
	h.warned = true
	fmt.Printf("infinite-streaming-pifi: chart history will not persist: %s\n", msg)
}
