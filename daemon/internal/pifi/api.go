package pifi

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
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
	mux.HandleFunc("POST /api/devices/{mac}/sweep", a.startSweep)
	mux.HandleFunc("DELETE /api/devices/{mac}/sweep", a.stopSweep)
	mux.HandleFunc("PUT /api/devices/{mac}/pattern", a.putPattern)
	mux.HandleFunc("DELETE /api/devices/{mac}/pattern", a.deletePattern)
	mux.HandleFunc("POST /api/devices/{mac}/pattern/play", a.playPattern)
	mux.HandleFunc("DELETE /api/devices/{mac}/pattern/play", a.stopPattern)
	mux.HandleFunc("GET /api/patterns", a.listPatterns)
	mux.HandleFunc("GET /api/patterns/{name}", a.getPattern)
	mux.HandleFunc("PUT /api/patterns/{name}", a.savePattern)
	mux.HandleFunc("DELETE /api/patterns/{name}", a.deleteSavedPattern)
	mux.HandleFunc("POST /api/devices/{mac}/pattern/select", a.selectPattern)
	mux.HandleFunc("GET /api/config", a.getConfig)
	mux.HandleFunc("POST /api/config", a.postConfig)
	mux.HandleFunc("PUT /api/devices/{mac}/ladders/{service}", a.putLadder)
	mux.HandleFunc("DELETE /api/devices/{mac}/ladders/{service}", a.deleteLadder)
	mux.HandleFunc("POST /api/devices/{mac}/reset", a.resetDevice)
	mux.HandleFunc("DELETE /api/devices/{mac}", a.forgetDevice)
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
	// The window the interface is about to draw, so a 1-minute view does not
	// pay for an hour of samples. Clamped rather than rejected: a bad query
	// string should degrade to a sensible chart, not an error page.
	win := time.Duration(clampInt(atoiOr(r.URL.Query().Get("window"), 300), 60, 3600)) * time.Second
	max := clampInt(atoiOr(r.URL.Query().Get("points"), 600), 60, 3600)

	series, bucket := a.e.History().Window(win, max)
	writeJSON(w, http.StatusOK, map[string]any{
		// bucket_ms is what one plotted point actually covers; interval_ms is
		// the live tick the stream appends at. They differ on long ranges, and
		// conflating them would let the chart claim a resolution it does not
		// have.
		"interval_ms": 1000,
		"bucket_ms":   bucket,
		"window_sec":  int(win.Seconds()),
		"now":         nowMs(),
		"clients":     series,
	})
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
	case s.ReorderPct < 0 || s.ReorderPct > 100:
		return fmt.Errorf("reorder_pct must be between 0 and 100")
	case s.CorruptPct < 0 || s.CorruptPct > 100:
		return fmt.Errorf("corrupt_pct must be between 0 and 100")
	case s.ReorderPct > 0 && s.DelayMs <= 0:
		// Refused rather than silently ignored. netem rejects the combination
		// outright, and a rejected command installs no qdisc at all -- so
		// accepting this would take the device's rate and loss down with a
		// setting the operator thought was additive.
		return fmt.Errorf("reorder_pct needs delay_ms above 0: " +
			"reordering works by letting packets skip the delay queue")
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
	// Moving a slider on a device that is mid-pattern pauses the pattern.
	//
	// The run would otherwise overwrite the operator's value on the next tick,
	// which reads as controls that do not work; and letting the edit stand
	// while the run continues means a pattern is playing that no longer
	// describes what the kernel is doing. Stopping and saying so on the
	// snapshot is the only outcome that can be reasoned about from the UI.
	if in.Down != nil || in.Up != nil {
		a.e.Player().Pause(normMAC(r.PathValue("mac")),
			"paused: you changed this device's controls by hand")
	}
	a.commit(w, p)
}

// patternPut carries a whole timeline. There is no partial keyframe patch: a
// pattern is only meaningful as an ordered whole, and a per-keyframe endpoint
// would let two concurrent edits interleave into a timeline neither operator
// authored.
type patternPut struct {
	BaseRevision *uint64  `json:"base_revision"`
	Pattern      *Pattern `json:"pattern"`
}

// putPattern stores a device's timeline. Storing is not playing.
func (a *API) putPattern(w http.ResponseWriter, r *http.Request) {
	var in patternPut
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	if in.Pattern == nil {
		writeErr(w, http.StatusBadRequest, "pattern is required")
		return
	}
	if err := validPattern(*in.Pattern); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := a.load(r.PathValue("mac"))
	if !a.checkRev(w, p, in.BaseRevision) {
		return
	}
	pat := *in.Pattern
	p.Pattern = &pat
	a.commit(w, p)
}

// deletePattern drops the stored timeline, and stops it if it is playing.
//
// Stopping is not optional here: leaving a run driving a pattern that no longer
// exists would condition the device from an object the UI cannot show.
func (a *API) deletePattern(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))
	_ = a.e.Player().Stop(mac)
	p := a.load(mac)
	if p.Pattern == nil {
		writeErr(w, http.StatusNotFound, "this device has no pattern")
		return
	}
	p.Pattern = nil
	a.commit(w, p)
}

// playPattern starts the stored timeline, or resumes a paused run from where it
// stopped.
func (a *API) playPattern(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))
	now := time.Now()
	if err := a.e.Player().Resume(mac, now); err == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"resumed": mac})
		return
	}
	p := a.load(mac)
	if p.Pattern == nil {
		writeErr(w, http.StatusBadRequest,
			"this device has no pattern; author one before playing it")
		return
	}
	// A pattern drives the kernel cap, so a device with no address has nothing
	// to attach a filter to and playback would run invisibly, conditioning
	// nothing while the playhead moved.
	shapeable := false
	for _, c := range a.e.Snapshot().Clients {
		if c.MAC == mac {
			shapeable = c.Present && c.Shapeable
			break
		}
	}
	if !shapeable {
		writeErr(w, http.StatusConflict,
			"device is not present and shapeable; a pattern cannot condition it")
		return
	}
	// A sweep is already driving this device's cap. Two drivers would fight
	// over it once per tick and the loser would be whichever ran first, which
	// is not a behaviour anyone could predict from the interface.
	if sv := a.e.Sweeper().View(mac); sv != nil && sv.State == "running" {
		writeErr(w, http.StatusConflict,
			"a ladder sweep is running on this device; stop it before playing a pattern")
		return
	}
	if err := a.e.Player().Start(mac, *p.Pattern, now); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": mac, "pattern": p.Pattern.Name, "dur_sec": p.Pattern.DurSec(),
	})
}

// stopPattern ends a run. The device returns to stored policy on the next tick;
// nothing needs unwinding because nothing was written.
func (a *API) stopPattern(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))
	if err := a.e.Player().Stop(mac); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stopped": mac})
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

// sweepRequest starts a ladder sweep. Service is required and carries no
// default: a ladder measured against an unnamed service is one nobody can
// interpret later, and guessing "default" would quietly overwrite the previous
// run's result the next time a different service was swept.
type sweepRequest struct {
	Service string       `json:"service"`
	Params  *SweepParams `json:"params"`
}

func validSweepParams(p SweepParams) error {
	switch {
	case p.StartMbps <= 0 || p.StartMbps > 100:
		return fmt.Errorf("start_mbps must be between 0 and 100")
	case p.ClimbPct < 5 || p.ClimbPct > 100:
		// Below 5 the climb creeps; above 100 the cap can admit two renditions
		// at once and the no-skip guarantee is gone.
		return fmt.Errorf("climb_pct must be between 5 and 100")
	case p.DwellSec < 5 || p.DwellSec > 900:
		return fmt.Errorf("dwell_sec must be between 5 and 900")
	case p.ObserveSec < 5 || p.ObserveSec > 900:
		return fmt.Errorf("observe_sec must be between 5 and 900")
	case p.RecoverSec < 0 || p.RecoverSec > 900:
		return fmt.Errorf("recover_sec must be between 0 and 900")
	case p.NewRungPct < 1 || p.NewRungPct > 100:
		return fmt.Errorf("new_rung_pct must be between 1 and 100")
	case p.SkipRatio < 1.1 || p.SkipRatio > 10:
		return fmt.Errorf("skip_ratio must be between 1.1 and 10")
	case p.MinStepMbps <= 0 || p.MinStepMbps > 10:
		return fmt.Errorf("min_step_mbps must be between 0 and 10")
	case p.MergePct < 0 || p.MergePct > 25:
		// Above 25 the tolerance starts to swallow genuine rungs: real ladders
		// are never spaced tighter than about that.
		return fmt.Errorf("merge_pct must be between 0 and 25")
	case p.MaxLevels < 2 || p.MaxLevels > 200:
		return fmt.Errorf("max_levels must be between 2 and 200")
	}
	return nil
}

// startSweep begins measuring a device's ladder for one service.
//
// The device must be streaming that service already: the sweep's opening level
// is an unconditioned observation, and with nothing playing it measures nothing
// and says so rather than reporting an empty ladder.
func (a *API) startSweep(w http.ResponseWriter, r *http.Request) {
	var in sweepRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	service := strings.TrimSpace(in.Service)
	if service == "" {
		writeErr(w, http.StatusBadRequest,
			"service is required: name what the device is streaming, e.g. \"netflix\". "+
				"A ladder belongs to a service, not to a device")
		return
	}
	p := DefaultSweepParams()
	if in.Params != nil {
		p = *in.Params
		if err := validSweepParams(p); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	mac := normMAC(r.PathValue("mac"))
	// A sweep drives the kernel cap, so a device with no address has nothing to
	// attach a filter to and the descent would silently do nothing.
	shapeable := false
	for _, c := range a.e.Snapshot().Clients {
		if c.MAC == mac {
			shapeable = c.Present && c.Shapeable
			break
		}
	}
	if !shapeable {
		writeErr(w, http.StatusConflict,
			"device is not present and shapeable; a sweep cannot condition it")
		return
	}
	// A pattern is already driving this device's cap; see playPattern for why
	// the two are refused rather than layered.
	if a.e.Player().Running(mac) {
		writeErr(w, http.StatusConflict,
			"a pattern is playing on this device; stop it before sweeping")
		return
	}
	if err := a.e.Sweeper().Start(mac, service, p, time.Now()); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"started": mac, "service": service, "params": p,
	})
}

// stopSweep abandons a running sweep. The device returns to stored policy on
// the next tick; nothing needs unwinding because nothing was written.
func (a *API) stopSweep(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))
	if err := a.e.Sweeper().Stop(mac); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stopped": mac})
}

// patternEntry is one row of the pattern list.
type patternEntry struct {
	Name    string  `json:"name"`
	Builtin bool    `json:"builtin"`
	DurSec  float64 `json:"dur_sec"`
	Keys    int     `json:"keys"`
	Loop    bool    `json:"loop"`
	// Selected marks the pattern currently loaded on the device the list was
	// asked for. The list is single-select: a device runs one pattern, so the
	// UI needs to know which row is the live one.
	Selected bool `json:"selected,omitempty"`
	// LadderService names the ladder a built-in was generated from, and Ladder
	// says whether that ladder was real. A pattern built from the stand-in
	// ladder is a plausibly-shaped test rather than a test of this content, and
	// the difference must be visible rather than inferred from the rates.
	LadderService string `json:"ladder_service,omitempty"`
	Ladder        string `json:"ladder,omitempty"`
	// Unavailable explains why a built-in could not be generated, instead of
	// omitting the row. A pattern that silently vanishes from a list reads as a
	// missing feature; one that says why reads as a thing to fix.
	Unavailable string `json:"unavailable,omitempty"`
}

// resolveBuiltin generates a built-in for a device, reporting which ladder it
// came from.
func (a *API) resolveBuiltin(name string, p Policy, service string,
	stretch float64) (Pattern, Ladder, bool, error) {

	l, ok := pickLadder(p, service)
	if !ok {
		l = DefaultLadder()
	}
	// Generated at the default dwell and then stretched, rather than generated
	// at a stretched dwell. One path, so a built-in and a saved pattern are
	// scaled by exactly the same code and cannot drift apart in rounding.
	pat, err := LadderPattern(name, l, 0)
	if err != nil {
		return pat, l, ok, err
	}
	pat, err = StretchPattern(pat, stretch)
	return pat, l, ok, err
}

// stretchParam reads the time-stretch multiplier, defaulting to 1.
func stretchParam(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// Absent, not zero: an unstretched list is the sensible default, and a
		// query string cannot express "explicitly 1" any other way.
		return 1, nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("stretch must be a number")
	}
	return f, nil
}

// listPatterns returns the built-ins and every saved pattern.
//
// Built-ins are GENERATED per request rather than stored, so they always
// describe the ladder the device has now: re-sweep it and they reshape
// themselves. That is also why they cannot be edited in place -- see
// PatternStore.Put.
func (a *API) listPatterns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mac := normMAC(q.Get("mac"))
	service := q.Get("service")
	// The list reports durations AT the requested stretch, so a slider can show
	// what it is about to do before anything is selected.
	stretch, err := stretchParam(q.Get("stretch"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var p Policy
	if mac != "" {
		p = a.load(mac)
	}
	current := ""
	if p.Pattern != nil {
		current = normPatternName(p.Pattern.Name)
	}

	out := []patternEntry{}
	for _, name := range BuiltinNames {
		e := patternEntry{Name: name, Builtin: true, Loop: true,
			Selected: current == name}
		if mac == "" {
			// Without a device there is no ladder, so a built-in has no rates.
			// Listed anyway, so the UI can show what exists.
			e.Unavailable = "choose a device: a built-in is built from its ladder"
			out = append(out, e)
			continue
		}
		pat, l, real, err := a.resolveBuiltin(name, p, service, stretch)
		if err != nil {
			e.Unavailable = err.Error()
			out = append(out, e)
			continue
		}
		e.DurSec, e.Keys = pat.DurSec(), len(pat.Keys)
		e.LadderService = l.Service
		if real {
			e.Ladder = string(l.Provenance)
		} else {
			e.Ladder = "default"
		}
		out = append(out, e)
	}
	for _, sp := range a.e.PatternStore().All() {
		e := patternEntry{Name: sp.Name, DurSec: sp.DurSec(),
			Keys: len(sp.Keys), Loop: sp.Loop, Selected: current == sp.Name}
		if st, err := StretchPattern(sp, stretch); err == nil {
			e.DurSec = st.DurSec()
		} else {
			e.Unavailable = err.Error()
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"patterns": out})
}

// getPattern resolves ONE pattern to its concrete timeline, without selecting
// it.
//
// A built-in has no stored keyframes -- it is generated from the device's
// ladder on demand -- so there was previously no way to see its rates except by
// selecting it onto a device, which changes what that device is set to. Reading
// something must not have the side effect of applying it: cloning a pattern to
// edit a copy, or simply looking at what a built-in would do, are both reads.
//
// mac and service pick the ladder for a built-in, and stretch scales the
// result, exactly as they do for the list.
func (a *API) getPattern(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	stretch, err := stretchParam(q.Get("stretch"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := normPatternName(r.PathValue("name"))

	if IsBuiltin(name) {
		mac := normMAC(q.Get("mac"))
		if mac == "" {
			writeErr(w, http.StatusBadRequest,
				"a built-in is generated from a device's ladder; name a mac")
			return
		}
		pat, l, real, err := a.resolveBuiltin(name, a.load(mac), q.Get("service"),
			stretch)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		ladder := "default"
		if real {
			ladder = string(l.Provenance)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"pattern": pat, "builtin": true,
			"ladder_service": l.Service, "ladder": ladder,
		})
		return
	}

	sp, ok := a.e.PatternStore().Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "no pattern named "+name)
		return
	}
	st, err := StretchPattern(sp, stretch)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pattern": st, "builtin": false})
}

// savePattern stores an authored pattern under a name.
//
// Editing a built-in and saving it is a "save as": the name in the path must be
// a new one, because a built-in is derived and a frozen copy wearing its label
// would make the same name mean different things on different boxes.
func (a *API) savePattern(w http.ResponseWriter, r *http.Request) {
	var in patternPut
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	if in.Pattern == nil {
		writeErr(w, http.StatusBadRequest, "a pattern is required")
		return
	}
	pat := *in.Pattern
	pat.Name = r.PathValue("name")
	if err := a.e.PatternStore().Put(pat); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.e.BumpControl()
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": normPatternName(pat.Name)})
}

func (a *API) deleteSavedPattern(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if IsBuiltin(name) {
		writeErr(w, http.StatusBadRequest,
			"built-in patterns are generated, not stored; there is nothing to delete")
		return
	}
	ok, err := a.e.PatternStore().Delete(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "no saved pattern named "+name)
		return
	}
	a.e.BumpControl()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": normPatternName(name)})
}

// patternSelect names the pattern to load onto a device.
type patternSelect struct {
	BaseRevision *uint64 `json:"base_revision"`
	Name         string  `json:"name"`
	// Service picks which of the device's ladders a built-in is built from. A
	// device holds one per service and they share nothing, so "the ladder" only
	// means something once a service is named; empty takes the most recently
	// measured.
	Service string `json:"service"`
	// Stretch scales the pattern in time, keeping its shape and its rates.
	//
	// A POINTER, so that absent and 0 are different requests. Absent means "do
	// not stretch"; 0 is a request to collapse every step into one instant,
	// which is a constant rate rather than a pattern, and is refused with that
	// explanation. A bare float would silently turn one into the other -- the
	// same trap policyPatch uses pointers to avoid.
	//
	// See StretchPattern for why this is a multiplier rather than a per-step
	// duration.
	Stretch *float64 `json:"stretch"`
}

// selectPattern loads a named pattern onto a device.
//
// Selecting is not playing: it fills the device's pattern slot, and
// POST /pattern/play starts it. Keeping them separate means the list can be
// changed without conditioning traffic the moment a row is ticked.
//
// A built-in is resolved to concrete rates HERE, at selection, and the device
// stores the result. So a device keeps running the shape it was given even if
// the ladder is re-swept underneath it -- re-select to pick up the new one.
// The alternative, resolving on every tick, means a sweep silently rewrites a
// pattern mid-run and the trace no longer matches what was requested.
func (a *API) selectPattern(w http.ResponseWriter, r *http.Request) {
	var in patternSelect
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	mac := normMAC(r.PathValue("mac"))
	p := a.load(mac)
	if !a.checkRev(w, p, in.BaseRevision) {
		return
	}

	stretch := 1.0
	if in.Stretch != nil {
		stretch = *in.Stretch
	}

	name := normPatternName(in.Name)
	switch {
	case name == "":
		// Deselecting is a legitimate request: it stops the run and clears the
		// slot, which is what the "none" row in the list does.
		a.e.Player().Stop(mac)
		p.Pattern = nil
	case IsBuiltin(name):
		pat, _, _, err := a.resolveBuiltin(name, p, in.Service, stretch)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.e.Player().Stop(mac)
		p.Pattern = &pat
	default:
		sp, ok := a.e.PatternStore().Get(name)
		if !ok {
			writeErr(w, http.StatusNotFound, "no pattern named "+in.Name)
			return
		}
		// Stretched on the way in, so the device stores the timeline it will
		// actually play. The saved pattern in the library is untouched: the
		// slider is a property of this selection, not an edit to the library.
		st, err := StretchPattern(sp, stretch)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		a.e.Player().Stop(mac)
		p.Pattern = &st
	}
	a.commit(w, p)
}

// getConfig exports every device's operator intent as one document.
func (a *API) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK,
		ExportConfig(a.e.Store().All(), a.e.PatternStore().All()))
}

// configPost carries a configuration to import.
//
// The document is the body itself, with mode alongside it, so that a file
// written by GET /api/config can be POSTed straight back without being
// rewrapped. Mode therefore comes from the query string.
type configPost struct {
	ConfigExport
}

// postConfig imports a configuration.
//
// Validated in full before anything is written, and written in one store
// operation, so the box either ends up matching the document or is left exactly
// as it was. Import is not a merge of individual fields: a device present in
// the document replaces that device's policy wholesale, because a configuration
// describes a state rather than a change.
func (a *API) postConfig(w http.ResponseWriter, r *http.Request) {
	var in configPost
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	mode := ImportMerge
	switch q := strings.TrimSpace(r.URL.Query().Get("mode")); q {
	case "", string(ImportMerge):
	case string(ImportReplace):
		mode = ImportReplace
	default:
		writeErr(w, http.StatusBadRequest,
			"mode must be \"merge\" or \"replace\"")
		return
	}
	if err := in.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	next, wrote, removed := in.Apply(a.e.Store().All(), mode)
	if err := a.e.Store().ReplaceAll(next); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The library is replaced wholesale whenever the document carries one. It
	// is box-level rather than per-device, so there is no subset of it that a
	// merge could sensibly leave alone -- and a document with no patterns key
	// at all is one exported before the library existed, which must not wipe it.
	if in.Patterns != nil {
		if err := a.e.PatternStore().ReplaceAll(in.Patterns); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	// Every imported device's conditioning may have changed, and a pattern that
	// was mid-run now belongs to a policy nobody in this session authored.
	for _, mac := range wrote {
		a.e.Player().Pause(mac, "paused: a configuration was imported")
	}
	a.e.BumpControl()
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": string(mode), "wrote": wrote, "removed": removed,
	})
}

type ladderPut struct {
	BaseRevision *uint64 `json:"base_revision"`
	Rungs        []Rung  `json:"rungs"`
	Note         string  `json:"note"`
}

// putLadder writes a ladder by hand, or corrects a measured one.
//
// Any hand-edited ladder becomes "typed" regardless of what it was before.
// Leaving it labelled "measured" after an operator moved a rung would attach
// the authority of a measurement to a number nobody measured.
func (a *API) putLadder(w http.ResponseWriter, r *http.Request) {
	var in ladderPut
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	service := strings.TrimSpace(r.PathValue("service"))
	if service == "" {
		writeErr(w, http.StatusBadRequest, "service is required")
		return
	}
	if len(in.Rungs) == 0 {
		writeErr(w, http.StatusBadRequest, "a ladder needs at least one rung")
		return
	}
	for _, rung := range in.Rungs {
		if rung.Mbps <= 0 || rung.Mbps > 10000 {
			writeErr(w, http.StatusBadRequest, "each rung must be between 0 and 10000 Mbps")
			return
		}
	}
	p := a.load(r.PathValue("mac"))
	if !a.checkRev(w, p, in.BaseRevision) {
		return
	}
	rungs := append([]Rung(nil), in.Rungs...)
	sort.Slice(rungs, func(i, j int) bool { return rungs[i].Mbps < rungs[j].Mbps })
	p.PutLadder(Ladder{
		Service: service, Rungs: rungs,
		Provenance: LadderTyped, MeasuredAt: nowMs(), Note: in.Note,
	})
	a.commit(w, p)
}

func (a *API) deleteLadder(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	p := a.load(r.PathValue("mac"))
	out := p.Ladders[:0]
	found := false
	for _, l := range p.Ladders {
		if strings.EqualFold(l.Service, service) {
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		writeErr(w, http.StatusNotFound, "no ladder for service: "+service)
		return
	}
	p.Ladders = out
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

// forgetDevice removes a device's stored configuration entirely.
//
// A device with stored policy is listed even when absent, so its settings can
// be prepared before it reconnects. That is useful until something transient
// leaves a row behind -- a test rig, a guest, a randomised MAC that will never
// return -- and then the list only grows. Reset clears conditioning but keeps
// the row; this drops it.
//
// A device that is still on the network will simply reappear, unconfigured,
// which is the correct outcome: pifi is describing what it sees.
func (a *API) forgetDevice(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))
	if err := a.e.Store().Delete(mac); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.e.BumpControl()
	writeJSON(w, http.StatusOK, map[string]string{"forgotten": mac})
}
