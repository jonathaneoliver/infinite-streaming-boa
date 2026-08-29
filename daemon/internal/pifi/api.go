package pifi

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// API exposes the engine over HTTP.
//
// The transport design is lifted from the streaming test harness, where it was
// proven under exactly this shape of load:
//
//   - Server-sent events carry FULL snapshots, never deltas. A client that
//     misses a frame cannot drift; it simply renders the next complete one.
//   - A slow consumer is dropped for that frame rather than back-pressuring the
//     tick loop.
//   - Polling GET /api/state is an equivalent fallback for anything that cannot
//     hold an SSE connection open.
type API struct {
	e  *Engine
	ui fs.FS
}

func NewAPI(e *Engine, ui fs.FS) *API { return &API{e: e, ui: ui} }

func (a *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.getState)
	mux.HandleFunc("GET /api/state/stream", a.stream)
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("GET /api/history", a.getHistory)
	mux.HandleFunc("PATCH /api/devices/{mac}/policy", a.patchPolicy)
	mux.HandleFunc("POST /api/devices/{mac}/sub", a.postSub)
	mux.HandleFunc("PATCH /api/devices/{mac}/sub/{id}", a.patchSub)
	mux.HandleFunc("DELETE /api/devices/{mac}/sub/{id}", a.deleteSub)
	mux.HandleFunc("POST /api/devices/{mac}/reset", a.resetDevice)
	mux.Handle("/", http.FileServer(http.FS(a.ui)))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *API) getState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.e.Snapshot())
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	s := a.e.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": s.Caps.Shaping, "caps": s.Caps, "revision": s.Revision,
	})
}

// getHistory serves the per-client throughput series.
//
// A separate endpoint rather than part of the snapshot: including it in every
// server-sent event, once a second, would multiply the stream roughly a
// hundredfold to re-send data the client already has. The UI fetches this once
// on load and appends from the live stream thereafter.
func (a *API) getHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"interval_ms": 1000,
		"now":         nowMs(),
		"clients":     a.e.History().Snapshot(),
	})
}

func (a *API) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer would defeat the point of streaming entirely.
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := a.e.Subscribe()
	defer cancel()

	// Send the current state immediately so a reconnecting UI paints at once
	// rather than waiting for the next tick.
	send := func(s Snapshot) bool {
		raw, err := json.Marshal(s)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send(a.e.Snapshot()) {
		return
	}

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case s, ok := <-ch:
			if !ok || !send(s) {
				return
			}
		case <-keepalive.C:
			// A comment frame keeps intermediaries from timing out an idle
			// connection without disturbing the client's event handler.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// policyPatch is the writable surface of a device. Every field is a pointer so
// that "absent" and "set to zero" are distinguishable -- without this, a UI
// sending only a label change would silently clear every shaping value to 0.
type policyPatch struct {
	BaseRevision *uint64 `json:"base_revision"`
	Label        *string `json:"label"`
	Enabled      *bool   `json:"enabled"`
	Down         *Shape  `json:"down"`
	Up           *Shape  `json:"up"`
}

func validShape(s Shape) error {
	switch {
	case s.RateMbps < 0 || s.RateMbps > 10000:
		return fmt.Errorf("rate_mbps must be between 0 (unlimited) and 10000")
	case s.DelayMs < 0 || s.DelayMs > 10000:
		return fmt.Errorf("delay_ms must be between 0 and 10000")
	case s.JitterMs < 0 || s.JitterMs > 5000:
		return fmt.Errorf("jitter_ms must be between 0 and 5000")
	case s.JitterMs > s.DelayMs:
		// netem subtracts jitter from delay per packet; more jitter than delay
		// asks for negative latency, which it silently clamps, producing a
		// distribution that is not what anyone configured.
		return fmt.Errorf("jitter_ms cannot exceed delay_ms")
	case s.LossPct < 0 || s.LossPct > 100:
		return fmt.Errorf("loss_pct must be between 0 and 100")
	}
	return nil
}

// load fetches the stored policy for a MAC, or a blank enabled one if the
// device has never been configured. A device seen on the network but never
// edited has no stored row, and asking to edit it must not 404.
func (a *API) load(mac string) Policy {
	mac = normMAC(mac)
	if p, ok := a.e.Store().Get(mac); ok {
		return p
	}
	return Policy{MAC: mac, Enabled: true}
}

// checkRev enforces optimistic concurrency. Returns false having already
// written the response when the caller's view is stale.
func (a *API) checkRev(w http.ResponseWriter, p Policy, base *uint64) bool {
	if base != nil && *base != p.Rev {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "stale base_revision; someone else changed this device",
			"current":  p,
			"revision": p.Rev,
		})
		return false
	}
	return true
}

func (a *API) commit(w http.ResponseWriter, p Policy) {
	p.Rev++
	if err := a.e.Store().Put(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.e.BumpControl()
	writeJSON(w, http.StatusOK, p)
}

func (a *API) patchPolicy(w http.ResponseWriter, r *http.Request) {
	var in policyPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	p := a.load(r.PathValue("mac"))
	if !a.checkRev(w, p, in.BaseRevision) {
		return
	}
	if in.Down != nil {
		if err := validShape(*in.Down); err != nil {
			writeErr(w, http.StatusBadRequest, "down: "+err.Error())
			return
		}
		p.Down = *in.Down
	}
	if in.Up != nil {
		if err := validShape(*in.Up); err != nil {
			writeErr(w, http.StatusBadRequest, "up: "+err.Error())
			return
		}
		p.Up = *in.Up
	}
	if in.Label != nil {
		p.Label = strings.TrimSpace(*in.Label)
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	a.commit(w, p)
}

type subPatch struct {
	BaseRevision *uint64 `json:"base_revision"`
	Name         *string `json:"name"`
	Match        *Match  `json:"match"`
	Down         *Shape  `json:"down"`
	Up           *Shape  `json:"up"`
	Enabled      *bool   `json:"enabled"`
}

func (a *API) postSub(w http.ResponseWriter, r *http.Request) {
	var in subPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	p := a.load(r.PathValue("mac"))
	if !a.checkRev(w, p, in.BaseRevision) {
		return
	}
	// 15 is the ceiling because class minors are allocated in blocks and the
	// UI becomes unusable long before anyone reaches it.
	if len(p.Sub) >= 15 {
		writeErr(w, http.StatusBadRequest, "a device may have at most 15 sub-classes")
		return
	}
	sub := SubClass{
		ID:      fmt.Sprintf("s%d", time.Now().UnixNano()%1e9),
		Name:    "new rule",
		Enabled: true,
	}
	if in.Name != nil {
		sub.Name = *in.Name
	}
	if in.Match != nil {
		sub.Match = *in.Match
	}
	if in.Down != nil {
		sub.Down = *in.Down
	}
	if in.Up != nil {
		sub.Up = *in.Up
	}
	p.Sub = append(p.Sub, sub)
	a.commit(w, p)
}

func (a *API) patchSub(w http.ResponseWriter, r *http.Request) {
	var in subPatch
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	p := a.load(r.PathValue("mac"))
	if !a.checkRev(w, p, in.BaseRevision) {
		return
	}
	id := r.PathValue("id")
	for i := range p.Sub {
		if p.Sub[i].ID != id {
			continue
		}
		if in.Down != nil {
			if err := validShape(*in.Down); err != nil {
				writeErr(w, http.StatusBadRequest, "down: "+err.Error())
				return
			}
			p.Sub[i].Down = *in.Down
		}
		if in.Up != nil {
			if err := validShape(*in.Up); err != nil {
				writeErr(w, http.StatusBadRequest, "up: "+err.Error())
				return
			}
			p.Sub[i].Up = *in.Up
		}
		if in.Name != nil {
			p.Sub[i].Name = *in.Name
		}
		if in.Match != nil {
			p.Sub[i].Match = *in.Match
		}
		if in.Enabled != nil {
			p.Sub[i].Enabled = *in.Enabled
		}
		a.commit(w, p)
		return
	}
	writeErr(w, http.StatusNotFound, "no such sub-class: "+id)
}

func (a *API) deleteSub(w http.ResponseWriter, r *http.Request) {
	p := a.load(r.PathValue("mac"))
	id := r.PathValue("id")
	out := p.Sub[:0]
	found := false
	for _, s := range p.Sub {
		if s.ID == id {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no such sub-class: "+id)
		return
	}
	p.Sub = out
	a.commit(w, p)
}

// resetDevice clears conditioning but keeps the device's label and sub-class
// definitions, because the common case is "give me a clean baseline again",
// not "forget everything I set up".
func (a *API) resetDevice(w http.ResponseWriter, r *http.Request) {
	p := a.load(r.PathValue("mac"))
	p.Down, p.Up = Shape{}, Shape{}
	for i := range p.Sub {
		p.Sub[i].Enabled = false
	}
	a.commit(w, p)
}
