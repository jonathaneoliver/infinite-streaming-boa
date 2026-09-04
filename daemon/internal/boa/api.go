package boa

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
	mux.HandleFunc("GET /api/bridge", a.getBridge)
	mux.HandleFunc("GET /api/events", a.getEvents)
	mux.HandleFunc("GET /api/bridge/radios/{iface}/survey", a.getSurvey)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/channel", a.postChannel)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/move-channel", a.postMoveChannel)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/deauth-all", a.postDeauthAll)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/link-all", a.postLinkAll)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/power", a.postRadioPower)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/scan", a.postScan)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/profile", a.postRadioProfile)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/threshold", a.postThreshold)
	mux.HandleFunc("POST /api/bridge/radios/{iface}/steer", a.postSteer)
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
	mux.HandleFunc("POST /api/patterns/merge", a.mergePatterns)
	mux.HandleFunc("PUT /api/patterns/{name}", a.savePattern)
	mux.HandleFunc("DELETE /api/patterns/{name}", a.deleteSavedPattern)
	mux.HandleFunc("POST /api/devices/{mac}/pattern/select", a.selectPattern)
	mux.HandleFunc("GET /api/config", a.getConfig)
	mux.HandleFunc("POST /api/config", a.postConfig)
	mux.HandleFunc("PUT /api/devices/{mac}/ladders/{service}", a.putLadder)
	mux.HandleFunc("DELETE /api/devices/{mac}/ladders/{service}", a.deleteLadder)
	mux.HandleFunc("POST /api/devices/{mac}/reset", a.resetDevice)
	mux.HandleFunc("POST /api/devices/{mac}/link/deauth", a.linkDeauth)
	mux.HandleFunc("POST /api/devices/{mac}/link/disassoc", a.linkDisassoc)
	mux.HandleFunc("POST /api/devices/{mac}/link/deadzone", a.linkDeadzone)
	mux.HandleFunc("POST /api/devices/{mac}/link/steer", a.linkSteer)
	mux.HandleFunc("DELETE /api/devices/{mac}", a.forgetDevice)
	mux.Handle("/", cacheHeaders(http.FileServer(http.FS(a.ui))))
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

// getBridge serves the box's own interface inventory: every port, its MAC and
// addresses, and what each radio's access point is doing.
//
// A separate endpoint rather than part of the snapshot, for the same reason
// getHistory is one: the snapshot goes out once a second to every connected
// browser, and this changes on the timescale of somebody plugging a cable in.
// It also costs two hostapd round-trips and a station dump per radio, which has
// no business running at 1 Hz whether or not anyone is looking at it.
func (a *API) getBridge(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.e.BridgeState())
}

// getEvents returns what has HAPPENED, newest events last.
//
// Polled with ?since=<seq> so a caller asks only for what it has not seen: the
// ring holds a few hundred events and re-sending all of them at the tab's poll
// rate would be most of the payload, every time, to say nothing new. since=0
// asks for everything the ring still holds, which is what a freshly-opened tab
// wants.
//
// Deliberately not part of the SSE frame. Events are bursty and rare -- nothing
// for ten minutes, then six in a second when a radio is switched off -- and
// attaching them to a 1Hz snapshot would mean carrying an empty array 99% of
// the time.
func (a *API) getEvents(w http.ResponseWriter, r *http.Request) {
	since := uint64(0)
	if q := strings.TrimSpace(r.URL.Query().Get("since")); q != "" {
		n, err := strconv.ParseUint(q, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since must be an event sequence number")
			return
		}
		since = n
	}
	limit := 200
	if q := strings.TrimSpace(r.URL.Query().Get("limit")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > eventRing {
			writeErr(w, http.StatusBadRequest,
				fmt.Sprintf("limit must be 1-%d", eventRing))
			return
		}
		limit = n
	}
	// events is never null in the payload: a caller that has seen everything
	// gets [], not a value it has to guard before iterating.
	evs := a.e.Events(since, limit)
	if evs == nil {
		evs = []Event{}
	}
	// latest travels with every response, including empty ones, because the
	// empty response is the case that needs it: a caller holding a cursor from
	// before a daemon restart would otherwise be told "nothing new" forever and
	// have no way to tell that from a quiet box (#196).
	writeJSON(w, http.StatusOK, map[string]any{
		"events": evs,
		"latest": a.e.LatestEventSeq(),
	})
}

// getSurvey reads a radio's airtime counters.
//
// A GET even though it costs a subprocess, because it changes nothing. Note the
// contract on the way out: this is the OPERATING channel's airtime, never a
// survey of the band, and SurveyResult.Note carries that in the payload so a
// caller cannot lose it. See Source L.
func (a *API) getSurvey(w http.ResponseWriter, r *http.Request) {
	res, err := a.e.Survey(r.PathValue("iface"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// postMoveChannel puts a radio on a chosen channel by taking it down and
// bringing it back up there.
//
// The working counterpart to postChannel. That one announces the move and lets
// clients follow without reconnecting, which is the nicer behaviour and is
// refused by both drivers on this box; this one drops the access point and
// brings it back elsewhere, which works and is what most consumer routers
// actually do. Clients are not told and must rediscover it.
func (a *API) postMoveChannel(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioReady(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ch, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("channel")))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "channel must be a number")
		return
	}
	width := 20
	if q := strings.TrimSpace(r.URL.Query().Get("width")); q != "" {
		n, werr := strconv.Atoi(q)
		if werr != nil {
			writeErr(w, http.StatusBadRequest, "width must be a number")
			return
		}
		width = n
	}
	dropped := len(StationDump(iface))
	now, err := a.e.MoveChannel(iface, ch, width)
	if err != nil {
		// 400 for an argument this box will never accept, 502 for hostapd
		// declining one it might have -- the same split postChannel makes.
		// A width the channel cannot carry belongs on the 400 side: 165 at
		// 80MHz is refused here every time, never attempted, so reporting it
		// as an upstream failure would point at the wrong thing.
		known, ok := apChannels[ch]
		if !ok || width > known.maxWidth() {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "move_channel", "channel": now,
		"width_mhz": width, "stations_dropped": dropped,
	})
}

// postChannel moves a radio, and every client associated to it, to another
// channel via an 802.11h channel switch announcement.
//
// AP-WIDE, unlike every /api/devices action: there is no MAC here because the
// blast radius is the whole radio. The interface says so before the button is
// pressed; this refuses loudly if hostapd will not do it, because a channel
// switch that silently did nothing would look identical to one a client simply
// followed. See issue #122.
func (a *API) postChannel(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioReady(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ch, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("channel")))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "channel must be a number")
		return
	}
	// Width defaults to 20: the width every permitted channel supports, so an
	// omitted parameter cannot produce a command hostapd rejects wholesale.
	width := 20
	if q := strings.TrimSpace(r.URL.Query().Get("width")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "width must be a number")
			return
		}
		width = n
	}
	if err := a.e.ChanSwitch(iface, ch, width); err != nil {
		// 400 for an argument this box will never accept, 502 for hostapd
		// declining one it might have. A width wider than the channel allows
		// is the first kind: 165 at 80MHz is refused here every time, not by
		// hostapd on the day.
		code := http.StatusBadGateway
		known, ok := apChannels[ch]
		if !ok || width != 20 && width != 40 && width != 80 || width > known.maxWidth() {
			code = http.StatusBadRequest
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "chan_switch", "channel": ch, "width_mhz": width,
	})
}

// postDeauthAll drops every station on a radio. AP-wide, same reasoning as
// postChannel; the count returned is how many were there to drop.
func (a *API) postDeauthAll(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioReady(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	n, err := a.e.DeauthAll(iface)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "deauth_all", "stations": n,
	})
}

// postLinkAll applies a per-client link event to every station on a radio.
//
// The AP-wide sibling of the drop and nudge buttons on a device card. Both are
// ANNOUNCED -- the clients are told and reconnect knowing why, which is the
// whole distinction from switching the radio off.
func (a *API) postLinkAll(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioReady(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = LinkDrop
	}
	n, err := a.e.LinkAll(iface, kind)
	if err != nil {
		code := http.StatusBadGateway
		if kind != LinkDrop && kind != LinkNudge {
			code = http.StatusBadRequest
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "link_all", "kind": kind, "stations": n,
	})
}

// postRadioPower cuts or restores a radio's power, telling its clients nothing.
//
// The one impairment here that is SILENT. Every other action announces itself:
// a deauthenticated client knows it was thrown off and reconnects in a second
// or two. A client whose access point loses power is told nothing at all and
// has to notice the beacons stopped, which takes it tens of seconds of
// believing it is still connected. That is the case a real power cut, a
// tripped breaker or walking round a corner produces, and nothing else in this
// codebase can imitate it.
//
// `?on=0|1` sets it and leaves it. `?dur=N` cuts power for N seconds and
// restores it -- the more useful form, since what matters is what a player does
// DURING an outage and how it recovers, and a manual off/on pair makes the
// duration whatever the operator's reflexes were.
func (a *API) postRadioPower(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioExists(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	q := r.URL.Query()
	// A timed outage is a different verb from a plain toggle, so it is decided
	// before `on` is read: ?dur= always means "off, then back".
	if s := strings.TrimSpace(q.Get("dur")); s != "" {
		dur, err := strconv.ParseFloat(s, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "dur must be a number of seconds")
			return
		}
		if err := a.e.RadioOutage(iface, dur); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"iface": iface, "action": "power_outage", "dur_sec": dur,
		})
		return
	}
	on := q.Get("on") != "0" && !strings.EqualFold(q.Get("on"), "false")
	if err := a.e.SetRadioPower(iface, on); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "power", "on": on,
	})
}

// postScan takes a radio out of service, scans its band, and puts it back --
// optionally on the quietest channel it found (`?apply=1`).
//
// A beaconing radio cannot survey other channels, so this is genuinely
// disruptive: the BSS is torn down for a few seconds and its clients are
// dropped. On a box serving two radios they land on the other band and come
// back, which is what makes it affordable; on a single-radio box it is an
// outage. Either way the cost is reported in the result as outage_sec.
//
// Applying happens while the radio is still down, which is why it works on
// hardware that refuses CHAN_SWITCH (issue #154): nothing is announced, the
// access point simply reappears elsewhere.
func (a *API) postScan(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioReady(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	apply := r.URL.Query().Get("apply") == "1"
	res, err := a.e.ScanBand(iface, apply)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// postRadioProfile applies a named PHY or power-save profile, restarting the
// BSS. Every client on the radio is dropped: these parameters are advertised in
// the beacon and negotiated at association, so an associated station cannot be
// told about them.
func (a *API) postRadioProfile(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioReady(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	dropped, err := a.e.ApplyRadioProfile(iface, name)
	if err != nil {
		// A refused SETTING is a partial success worth reporting as one: the
		// radio is back up, some of the profile took, and the caller is told
		// exactly which parts did not.
		if dropped > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"iface": iface, "action": "profile", "profile": name,
				"stations_dropped": dropped, "warning": err.Error(),
			})
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "profile", "profile": name,
		"stations_dropped": dropped,
	})
}

// postThreshold sets the RTS or fragmentation threshold. The only radio
// impairment here that costs nothing: live on the next frame, nobody dropped.
func (a *API) postThreshold(w http.ResponseWriter, r *http.Request) {
	iface := r.PathValue("iface")
	if err := a.e.radioExists(iface); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	q := r.URL.Query()
	kind := strings.TrimSpace(q.Get("kind"))
	// -1 is "off", and is the default so a request with no value turns the
	// impairment off rather than silently setting it to zero -- which for rts
	// means "every frame" and is the strongest setting there is.
	val := -1
	if s := strings.TrimSpace(q.Get("value")); s != "" && s != "off" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "value must be a number, or 'off'")
			return
		}
		val = n
	}
	if err := a.e.SetPhyThreshold(iface, kind, val); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": iface, "action": "threshold", "kind": kind, "value": val,
	})
}

// postSteer asks clients to move to the other radio via 802.11v.
//
// A REQUEST, not an instruction: the decision stays with the client, and
// whether a given phone honours it is exactly the behaviour worth testing.
// `?mac=` steers one; omitted, it asks everyone on the radio.
func (a *API) postSteer(w http.ResponseWriter, r *http.Request) {
	from := r.PathValue("iface")
	if err := a.e.radioReady(from); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	if to == "" {
		to = a.e.OtherRadio(from)
	}
	if to == "" {
		writeErr(w, http.StatusServiceUnavailable,
			"nowhere to steer to: this box is serving only one radio, and a "+
				"transition request needs another access point to name")
		return
	}
	if mac := strings.TrimSpace(r.URL.Query().Get("mac")); mac != "" {
		if err := a.e.SteerClient(mac, from, to); err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"iface": from, "action": "steer", "to": to, "asked": 1, "mac": normMAC(mac),
		})
		return
	}
	n, err := a.e.SteerAll(from, to)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"iface": from, "action": "steer", "to": to, "asked": n,
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
	case s.LossBurst < 0 || s.LossBurst > maxLossBurst:
		// Above this you are describing an outage rather than correlated loss
		// inside a working link, and an outage is a keyframe on a pattern --
		// where it is visible, timed and reproducible.
		return fmt.Errorf("loss_burst must be between 1 (uniform) and %d packets, "+
			"or 0 for uniform; a longer dropout belongs on a pattern", maxLossBurst)
	// Guarded on LossPct > 0 to match Shape.Bursty exactly. Without it, turning
	// loss OFF while a burst length is still set -- the ordinary way anyone
	// stops using this -- is refused, because zero is below the floor.
	case s.LossBurst > 1 && s.LossPct > 0 && s.LossPct < minBurstyLossPct:
		// Bursts this rare cannot be expressed: the Gilbert-Elliott transition
		// probability rounds to zero at the precision tc takes, which would
		// mean no loss at all rather than the loss that was asked for.
		return fmt.Errorf("bursty loss needs loss_pct of at least %g; "+
			"below that the bursts are too rare to configure", minBurstyLossPct)
	}
	return nil
}

const (
	// maxLossBurst bounds a burst at what still reads as a lossy link rather
	// than an outage. See validShape.
	maxLossBurst = 50
	// minBurstyLossPct is the smallest mean loss whose Gilbert-Elliott
	// parameters survive tc's percentage precision at the longest burst.
	minBurstyLossPct = 0.01
)

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

	// One ladder for the box, not this device's. The caps a pattern uses are
	// the sweep's grid rather than the content's bitrates, and that grid is the
	// same everywhere -- see GlobalLadder. `p` and `service` are still taken so
	// the per-device view can say which measurement is in force.
	stored, storedOK := a.e.LadderStore().Get()
	l, ok := GlobalLadder(stored, storedOK, a.e.Store().All())
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
		// A mac is optional now: built-ins are generated from the box's one
		// ladder rather than a device's, so the same name means the same
		// pattern everywhere. It is still accepted, and still names which
		// device the caller was looking at.
		pat, l, real, err := a.resolveBuiltin(name, a.load(normMAC(q.Get("mac"))),
			q.Get("service"), stretch)
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

// cacheHeaders tells the browser what it may keep, because otherwise it decides
// for itself and gets it wrong.
//
// The embedded filesystem has no modification times -- embed.FS reports zero --
// so http.FileServer emits no Last-Modified, and nothing here was emitting an
// ETag or a Cache-Control either. A response with no validators and no
// directives is not "do not cache": it licenses HEURISTIC caching, and a
// browser may then reuse it for the rest of a session.
//
// For index.html that is the expensive one. Vite fingerprints every asset, so a
// deploy changes their names -- but only index.html knows the new names. Serve
// a stale index and the browser dutifully loads the OLD bundle, and the box
// then runs code the operator cannot see and did not deploy. That happened here
// across a dozen deploys before anyone noticed, and it presented as two
// unrelated bugs: a button that did nothing and a layout that did not fit.
//
// So: the fingerprinted assets are immutable, because their names change when
// their content does. Everything else must revalidate. no-cache does not mean
// "do not store" -- it means "ask first" -- which is exactly right for a file
// whose name never changes and whose content does.
func cacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// mergePatterns lays several patterns over one another and returns the result
// WITHOUT saving it.
//
// A preview rather than a write, for two reasons. The operator has to name the
// thing, and they will name it better once they can see what it turned out to
// be -- a merge whose sources both drove the rate is a merge that did nothing,
// and that is worth noticing before it acquires a name and a place in the
// library. And saving goes through the existing PUT, so a merged pattern is
// stored by exactly the same path as a hand-built one and cannot drift from it.
//
// Order matters and is the caller's: where two sources drive the same axis, the
// first in this list wins. See MergePatterns.
func (a *API) mergePatterns(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Names   []string `json:"names"`
		MAC     string   `json:"mac"`
		Service string   `json:"service"`
		Stretch float64  `json:"stretch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed body: "+err.Error())
		return
	}
	if len(in.Names) < 2 {
		writeErr(w, http.StatusBadRequest, "a merge needs at least 2 patterns")
		return
	}
	if in.Stretch == 0 {
		in.Stretch = 1
	}
	pol := a.load(normMAC(in.MAC))

	pats := make([]Pattern, 0, len(in.Names))
	for _, n := range in.Names {
		n = normPatternName(n)
		if IsBuiltin(n) {
			// Built-ins are per-device, so a merge is too: the same two names
			// produce different patterns on two devices with different ladders,
			// which is the point of a built-in.
			pat, _, _, err := a.resolveBuiltin(n, pol, in.Service, in.Stretch)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			pats = append(pats, pat)
			continue
		}
		sp, ok := a.e.PatternStore().Get(n)
		if !ok {
			writeErr(w, http.StatusNotFound, "no pattern named "+n)
			return
		}
		st, err := StretchPattern(sp, in.Stretch)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		pats = append(pats, st)
	}

	merged, err := MergePatterns(strings.Join(in.Names, "+"), pats)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// What it is, alongside what it computed to. The keyframes are what plays;
	// the recipe is how it was made, and is what makes a merge shareable and
	// rebuildable. See PatternRecipe.
	merged.Recipe = &PatternRecipe{Sources: in.Names, Stretch: in.Stretch}
	// Validated here rather than only at save time, so an impossible
	// combination is reported while the operator can still see which two
	// patterns they picked -- reorder over a zero delay being the one that
	// actually happens. See reorderClimbSteps.
	if err := validPattern(merged); err != nil {
		writeErr(w, http.StatusBadRequest,
			"these patterns cannot be layered: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pattern": merged,
		"sources": in.Names,
	})
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
	var ladder *Ladder
	if l, ok := a.e.LadderStore().Get(); ok {
		ladder = &l
	} else if l, ok := GlobalLadder(Ladder{}, false, a.e.Store().All()); ok {
		// Not yet moved out of a device on this box. Export it anyway rather
		// than making the operator re-sweep to get a backup worth having.
		ladder = &l
	}
	writeJSON(w, http.StatusOK,
		ExportConfig(ladder, a.e.PatternStore().All()))
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
	// Patterns follow the same rule as devices: merge upserts by name and
	// leaves the rest alone, replace makes the library match the document.
	//
	// It used to replace wholesale in both modes, on the grounds that a
	// box-level library has no subset a merge could sensibly leave alone. That
	// was true while this document was a whole-box backup. It stopped being
	// true when a document carrying only patterns became the way to send
	// someone a pattern -- at which point importing a colleague's library
	// silently destroyed your own, and the word on the button said "merge".
	//
	// A document with no patterns key at all is one exported before the library
	// existed, and must not wipe it in either mode.
	if in.Patterns != nil {
		if mode == ImportReplace {
			if err := a.e.PatternStore().ReplaceAll(in.Patterns); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			for _, pat := range in.Patterns {
				if err := a.e.PatternStore().Put(pat); err != nil {
					writeErr(w, http.StatusBadRequest, "pattern "+pat.Name+": "+err.Error())
					return
				}
			}
		}
	}
	// The ladder likewise: one per box, so there is nothing to merge. Applied
	// after the patterns because every built-in is generated from it, so a
	// document's patterns and its ladder describe one state and the box should
	// not sit in half of it any longer than it must.
	if in.Ladder != nil {
		if err := a.e.LadderStore().Put(*in.Ladder); err != nil {
			writeErr(w, http.StatusBadRequest, "ladder: "+err.Error())
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
		"ladder": in.Ladder != nil, "patterns": len(in.Patterns),
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

func (a *API) linkDeauth(w http.ResponseWriter, r *http.Request)   { a.linkEvent(w, r, "deauth") }
func (a *API) linkDisassoc(w http.ResponseWriter, r *http.Request) { a.linkEvent(w, r, "disassoc") }

// linkSteer asks ONE client to move to the box's other radio (802.11v).
//
// The per-client counterpart to the radio-wide steer on the bridge diagram, and
// the more useful of the two: moving every client at once changes the whole
// box, where the question worth asking is usually "what does THIS phone do when
// told to move".
//
// A REQUEST, not an instruction, exactly as the radio-wide one is: the client
// decides, and whether a given device honours a transition request is the
// behaviour under test. A refusal is therefore a RESULT, not an error -- what
// this reports is whether the request was delivered.
//
// Both radios are resolved here rather than taken from the caller. The source
// is the radio the client is actually associated to, so a client on either band
// is steered the right way round, and the target is whatever else is serving.
func (a *API) linkSteer(w http.ResponseWriter, r *http.Request) {
	if !a.e.LinkControlAvailable() {
		writeErr(w, http.StatusServiceUnavailable,
			"link control unavailable: hostapd is not serving the AP (onboard radio, or ctrl_interface missing)")
		return
	}
	mac := normMAC(r.PathValue("mac"))
	if !validMAC(mac) {
		writeErr(w, http.StatusBadRequest, "not a MAC address: "+mac)
		return
	}
	from := a.e.radioFor(mac)
	to := a.e.OtherRadio(from)
	if to == "" {
		// 503, not 400: the request is well formed and would work on a box with
		// two radios serving. Nothing about the MAC is wrong.
		writeErr(w, http.StatusServiceUnavailable,
			"nowhere to steer to: this box is serving only one radio, and a "+
				"transition request needs another access point to name")
		return
	}
	if err := a.e.SteerClient(mac, from, to); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// Logged as an OPERATOR action, like deauth and disassoc, through the same
	// helper SteerClient's demo path uses so both read identically in the log.
	// The join on the other radio, if the client accepts, is recorded
	// separately by the station watcher -- and the gap between the two, or the
	// absence of a join at all, is exactly what this button is for.
	if !a.e.cfg.Demo {
		a.e.noteSteer(mac, from, to)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mac": mac, "action": "steer", "from": from, "to": to,
	})
}

// linkDeadzone holds a client off the AP for ?dur=<seconds> (default 10) --
// a sustained outage, long enough to actually stall a stream, unlike a single
// deauth. See issue #135.
func (a *API) linkDeadzone(w http.ResponseWriter, r *http.Request) {
	if !a.e.LinkControlAvailable() {
		writeErr(w, http.StatusServiceUnavailable,
			"link control unavailable: hostapd is not serving the AP (onboard radio, or ctrl_interface missing)")
		return
	}
	mac := normMAC(r.PathValue("mac"))
	if !validMAC(mac) {
		writeErr(w, http.StatusBadRequest, "not a MAC address: "+mac)
		return
	}
	dur := 10.0
	if q := strings.TrimSpace(r.URL.Query().Get("dur")); q != "" {
		f, err := strconv.ParseFloat(q, 64)
		if err != nil || f < 1 || f > 300 {
			writeErr(w, http.StatusBadRequest, "dur must be 1-300 seconds")
			return
		}
		dur = f
	}
	// Which radios the ban covers. Default is the client's own radio, which is
	// what this endpoint has always done -- so an existing caller is unchanged.
	// "all" is the sustained outage; see LinkDeadzone and issue #206.
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	switch scope {
	case "", ScopeCurrent, ScopeAll:
	default:
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("scope must be %q or %q (got %q)", ScopeCurrent, ScopeAll, scope))
		return
	}
	if scope == "" {
		scope = ScopeCurrent
	}
	if err := a.e.LinkDeadzone(mac, dur, scope); err != nil {
		// A refused "all" is a statement about the box, not a bad request: the
		// caller asked for something coherent that this box cannot currently
		// deliver, which is the same shape as steering with one radio.
		code := http.StatusBadGateway
		if scope == ScopeAll {
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mac": mac, "action": "deadzone", "dur_sec": dur, "scope": scope,
	})
}

// linkEvent drives a Group A per-client link event (deauth or disassoc) through
// hostapd. It is a POST with no body; an optional ?reason=<N> supplies an IEEE
// 802.11 reason code. Unlike shaping, this acts on the client's ASSOCIATION,
// not its packets -- so it is refused rather than silently ignored when hostapd
// is not the thing serving the AP. See issue #135.
func (a *API) linkEvent(w http.ResponseWriter, r *http.Request, action string) {
	if !a.e.LinkControlAvailable() {
		writeErr(w, http.StatusServiceUnavailable,
			"link control unavailable: hostapd is not serving the AP (onboard radio, or ctrl_interface missing)")
		return
	}
	mac := normMAC(r.PathValue("mac"))
	if !validMAC(mac) {
		writeErr(w, http.StatusBadRequest, "not a MAC address: "+mac)
		return
	}
	reason := 0
	if q := strings.TrimSpace(r.URL.Query().Get("reason")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 65535 {
			writeErr(w, http.StatusBadRequest, "reason must be an 802.11 reason code, 1-65535")
			return
		}
		reason = n
	}
	var err error
	if action == "disassoc" {
		err = a.e.LinkDisassoc(mac, reason)
	} else {
		err = a.e.LinkDeauth(mac, reason)
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// Logged HERE rather than in LinkDeauth, so the log records what an
	// OPERATOR did. The same two calls are made at up to 1Hz by a running
	// pattern, and a deadzone would fill the ring with its own repeats inside a
	// minute; what a pattern does to a client already shows up as the join and
	// leave it causes.
	verb := "dropped"
	if action == "disassoc" {
		verb = "nudged"
	}
	a.e.logEvent(EventAction, "", mac, "%s %s", verb, a.e.labelFor(mac))
	writeJSON(w, http.StatusOK, map[string]any{"mac": mac, "action": action, "reason": reason})
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
// which is the correct outcome: boa is describing what it sees.
func (a *API) forgetDevice(w http.ResponseWriter, r *http.Request) {
	mac := normMAC(r.PathValue("mac"))
	if err := a.e.Store().Delete(mac); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.e.BumpControl()
	writeJSON(w, http.StatusOK, map[string]string{"forgotten": mac})
}
