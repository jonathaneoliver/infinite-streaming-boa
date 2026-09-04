package boa

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// readLines pulls up to n newline-delimited JSON objects off a live response,
// giving up when the deadline passes so a broken stream fails the test rather
// than hanging it.
func readLines(t *testing.T, body *bufio.Reader, n int, within time.Duration) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for len(out) < n {
			raw, err := body.ReadBytes('\n')
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				out = append(out, m)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(within):
	}
	return out
}

func streamServer(t *testing.T) (*httptest.Server, *Engine) {
	t.Helper()
	e := &Engine{}
	mux := http.NewServeMux()
	a := &API{e: e}
	mux.HandleFunc("GET /api/events/stream", a.streamEvents)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, e
}

// TestEventStreamEmitsEventsAsNDJSON is the whole point: a capture is one JSON
// object per line, parseable straight out of the file.
func TestEventStreamEmitsEventsAsNDJSON(t *testing.T) {
	srv, e := streamServer(t)
	e.logEvent(EventJoin, "wlan0", "aa:bb:cc:dd:ee:ff", "a joined")

	resp, err := http.Get(srv.URL + "/api/events/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	body := bufio.NewReader(resp.Body)
	got := readLines(t, body, 2, 5*time.Second)
	if len(got) < 2 {
		t.Fatalf("got %d lines, want the open marker and the backlog event", len(got))
	}
	if got[0]["marker"] != "open" {
		t.Errorf("first line = %v, want the open marker", got[0])
	}
	if got[1]["kind"] != EventJoin {
		t.Errorf("second line = %v, want the join already in the ring", got[1])
	}

	// And a NEW event reaches the open stream.
	e.logEvent(EventLeave, "wlan0", "aa:bb:cc:dd:ee:ff", "a left")
	got = readLines(t, body, 1, 5*time.Second)
	if len(got) != 1 || got[0]["kind"] != EventLeave {
		t.Errorf("live event not delivered: %v", got)
	}
}

// TestEventStreamHonoursSince lets a reader resume without replaying a backlog
// it already has.
func TestEventStreamHonoursSince(t *testing.T) {
	srv, e := streamServer(t)
	e.logEvent(EventJoin, "wlan0", "aa:bb:cc:dd:ee:ff", "first")
	e.logEvent(EventJoin, "wlan0", "bb:bb:cc:dd:ee:ff", "second")

	resp, err := http.Get(srv.URL + "/api/events/stream?since=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	got := readLines(t, bufio.NewReader(resp.Body), 2, 5*time.Second)
	if len(got) < 2 {
		t.Fatalf("got %d lines, want the marker and one event", len(got))
	}
	if txt, _ := got[1]["text"].(string); !strings.Contains(txt, "second") {
		t.Errorf("since=1 replayed %v; the first event should have been skipped", got[1])
	}
}

func TestEventStreamRejectsABadCursor(t *testing.T) {
	srv, _ := streamServer(t)
	resp, err := http.Get(srv.URL + "/api/events/stream?since=nonsense")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestEventStreamReportsARestart covers the case that makes a capture
// misleading rather than merely short: the daemon restarts, the sequence begins
// again at 1, and a cursor from the previous run would otherwise match nothing
// for ever. The capture must record that it happened.
func TestEventStreamReportsARestart(t *testing.T) {
	srv, e := streamServer(t)
	for i := 0; i < 5; i++ {
		e.logEvent(EventJoin, "wlan0", "aa:bb:cc:dd:ee:ff", "before")
	}

	resp, err := http.Get(srv.URL + "/api/events/stream?since=4")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body := bufio.NewReader(resp.Body)
	readLines(t, body, 2, 5*time.Second) // open marker + the 5th event

	// The ring is replaced, exactly as a restart does.
	e.events = eventLog{}
	e.logEvent(EventJoin, "wlan0", "aa:bb:cc:dd:ee:ff", "after")

	got := readLines(t, body, 2, 5*time.Second)
	var sawRestart bool
	for _, m := range got {
		if m["marker"] == "restart" {
			sawRestart = true
		}
	}
	if !sawRestart {
		t.Errorf("no restart marker; the capture would show a silence it cannot "+
			"explain. got %v", got)
	}
}
