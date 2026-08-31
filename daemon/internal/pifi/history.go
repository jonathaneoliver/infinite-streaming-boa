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
	// Cap is the downlink cap the kernel was enforcing at this instant, 0 for
	// unlimited.
	//
	// Recorded per sample rather than read from the policy when the chart is
	// drawn, because a pattern moves the cap while the chart is being watched:
	// the current value says nothing about what was in force when the player
	// reacted three minutes ago. Lining a player's behaviour up against the cap
	// that caused it is the entire purpose of this box, and it cannot be done
	// from a single number.
	//
	// It is the ENFORCED cap read back from tc, not the requested one, for the
	// same reason DownCounters.CapMbps is: the chart should show what the
	// kernel believed, so a shaping failure appears as a flat line at the old
	// value rather than being papered over by the value we asked for.
	Cap float64 `json:"cap"`
}

const (
	// Sized to the LONGEST selectable chart range: 3600 samples at the 1 Hz
	// tick is one hour. Shorter ranges are a slice of this, so switching to 1m
	// or 5m costs the server nothing.
	//
	// One hour at full resolution rather than coarse buckets because the whole
	// point of this box is watching a player react to a cap, and a rendition
	// switch is a step that lasts a couple of seconds. Bucketing at source
	// would erase exactly the event being looked for. The API decimates on the
	// way out instead, so the fine detail is still there for short ranges.
	historyLen = 3600
	// Clients quiet for longer than this are dropped, so a network with many
	// transient devices cannot grow the table without bound.
	historyIdle = 30 * time.Minute
	// Lowered from 256 alongside the twelvefold ring: a Sample is 32 bytes, so
	// the worst case is 64 x 3600 x 32 = 7.4 MB resident, against 22 MB at the
	// old ceiling. A per-client link conditioner with more than 64 devices on
	// it is not the machine this was built for.
	historyMaxClients = 64
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

// Between returns one client's RAW samples falling in [from, to].
//
// Distinct from Window, which serves the charts: that one covers every client
// and decimates into buckets to keep the payload small. A ladder sweep needs
// the opposite -- one client, a short span, and no bucketing at all. Bucketing
// averages away the burst-and-idle structure of a segment fetch, which is
// precisely the shape the sweep reads to tell a full buffer from a starved
// client.
//
// It is also asked for every tick, so copying every series to read a
// 60-second window would be waste that scales with the size of the network.
func (h *History) Between(mac string, from, to time.Time) []Sample {
	lo, hi := from.UnixMilli(), to.UnixMilli()
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []Sample
	for _, s := range h.byMAC[mac] {
		if s.T >= lo && s.T <= hi {
			out = append(out, s)
		}
	}
	return out
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

// Window returns each client's series over the last `dur`, decimated so no
// series exceeds maxPoints. It also reports the bucket width used.
//
// Decimation exists because of the link this page is served over: the operator
// may have just throttled it to 1 Mbps on purpose. An undecimated hour is 3600
// samples per client -- around 140 KB of JSON each -- which is a visible stall
// on a page whose job is to look responsive while the network is being ruined.
//
// Buckets carry the MEAN, not the peak. The y-axis is Mbps and the plot is a
// filled area, so the area under it should correspond to bytes moved; taking
// the maximum would inflate a quiet minute with one burst into a solid block.
// The cost is that a long range smooths short spikes, which is why bucketMS is
// returned: the interface labels the axis with it rather than implying every
// range is raw 1 Hz data.
func (h *History) Window(dur time.Duration, maxPoints int) (series map[string][]Sample, bucketMS int64) {
	if maxPoints < 1 {
		maxPoints = 1
	}
	cut := time.Now().Add(-dur).UnixMilli()

	// One bucket per sample until the range no longer fits, so short ranges are
	// returned untouched rather than being averaged for no reason.
	//
	// Divided by maxPoints-1, not maxPoints: buckets are aligned to absolute
	// time, so a window almost always straddles one extra boundary and a naive
	// division returns maxPoints+1 points -- breaking the very budget the
	// caller asked for. Reserving one point for the straddle is exact.
	bucketMS = 1000
	if maxPoints > 1 {
		if want := dur.Milliseconds() / int64(maxPoints-1); want > bucketMS {
			bucketMS = want
		}
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	series = make(map[string][]Sample, len(h.byMAC))
	for mac, all := range h.byMAC {
		var out []Sample
		var sumD, sumU float64
		var n int
		var slot int64 = -1
		// The cap is NOT averaged. It is a step, set to values somebody chose,
		// so the mean of a bucket spanning a change is a cap that was never in
		// force -- a bucket covering 12 and 1.5 would draw a line at 6.75 and
		// invite the reader to explain a player's behaviour against it. The
		// first sample's value is taken instead, which matches the bucket's
		// timestamp: buckets are stamped at their start.
		var capFirst float64

		flush := func() {
			if n == 0 {
				return
			}
			out = append(out, Sample{
				T:    slot * bucketMS,
				Down: sumD / float64(n),
				Up:   sumU / float64(n),
				Cap:  capFirst,
			})
			sumD, sumU, n = 0, 0, 0
		}

		for _, sm := range all {
			if sm.T < cut {
				continue
			}
			if b := sm.T / bucketMS; b != slot {
				flush()
				slot = b
			}
			if n == 0 {
				capFirst = sm.Cap
			}
			sumD += sm.Down
			sumU += sm.Up
			n++
		}
		flush()

		// A client with nothing in the window is omitted entirely rather than
		// sent as an empty array: the interface treats "no key" as "no history
		// yet" and starts accumulating live, which is the truthful state.
		if len(out) > 0 {
			series[mac] = out
		}
	}
	return series, bucketMS
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
