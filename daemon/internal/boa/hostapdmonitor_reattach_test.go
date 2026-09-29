package boa

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

/*
 * #443: after a radio is rebuilt, hostapd serves a NEW control socket at the
 * same path and has forgotten this monitor. Nothing arrives to say so -- a
 * datagram socket has no end -- so the monitor must notice by the socket's
 * identity, and end its read so watchOneRadio attaches afresh.
 */

// monitorPair is a real unixgram connection standing in for hostapd's control
// socket: the read loop under test runs against a genuine socket, only the
// socket's identity is faked.
func monitorPair(t *testing.T) *net.UnixConn {
	t.Helper()
	// Not t.TempDir(): its path carries the test's name, and a Unix socket
	// path is capped at 104 bytes on macOS, so bind fails before the test
	// begins.
	dir, err := os.MkdirTemp("", "boa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	srv, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: filepath.Join(dir, "hostapd"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	conn, err := net.DialUnix("unixgram",
		&net.UnixAddr{Name: filepath.Join(dir, "boa"), Net: "unixgram"},
		&net.UnixAddr{Name: filepath.Join(dir, "hostapd"), Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// fakeSocketIdentity replaces hostapdSocketID with one the test controls, and
// shortens the quiet check so the test runs in milliseconds.
func fakeSocketIdentity(t *testing.T, start uint64) (set func(uint64), vanish func()) {
	t.Helper()
	var id atomic.Uint64
	var gone atomic.Bool
	id.Store(start)
	idFn, quiet := hostapdSocketID, monitorQuietCheck
	hostapdSocketID = func(string) (uint64, bool) {
		if gone.Load() {
			return 0, false
		}
		return id.Load(), true
	}
	monitorQuietCheck = 10 * time.Millisecond
	t.Cleanup(func() { hostapdSocketID, monitorQuietCheck = idFn, quiet })
	return id.Store, func() { gone.Store(true) }
}

func runMonitor(e *Engine, conn *net.UnixConn, attached uint64) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		e.readHostapdEvents("phy1-ap0", conn, attached)
		close(done)
	}()
	return done
}

// The measured case: `wifi up` moved the socket from inode 356 to 368.
func TestAReplacedControlSocketEndsTheMonitorSoItReattaches(t *testing.T) {
	set, _ := fakeSocketIdentity(t, 356)
	done := runMonitor(&Engine{}, monitorPair(t), 356)

	// The same socket, quiet: this is the normal state of a radio, and the
	// monitor must keep listening through it. Many quiet checks go by here.
	select {
	case <-done:
		t.Fatal("the monitor stopped on a quiet radio whose socket had not changed")
	case <-time.After(150 * time.Millisecond):
	}

	set(368)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the socket was replaced and the monitor kept waiting on the old one; " +
			"every event on this radio would be lost until boad restarted (#443)")
	}
}

// A socket that has gone -- the radio removed, hostapd stopped -- ends the read
// too, so the monitor re-attaches when one appears.
func TestAVanishedControlSocketEndsTheMonitor(t *testing.T) {
	_, vanish := fakeSocketIdentity(t, 356)
	done := runMonitor(&Engine{}, monitorPair(t), 356)

	vanish()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the control socket disappeared and the monitor kept waiting")
	}
}

// A forced channel move is DISABLE and ENABLE in the same process: the socket
// keeps its inode (measured, 356 before and after) and hostapd keeps the
// monitor. Re-attaching then would be harmless but pointless -- and a monitor
// that restarted on every quiet check would drop events in the gaps.
func TestTheMonitorOutlivesAMoveThatKeepsTheSocket(t *testing.T) {
	fakeSocketIdentity(t, 356)
	done := runMonitor(&Engine{}, monitorPair(t), 356)

	select {
	case <-done:
		t.Fatal("the monitor re-attached although the socket never changed")
	case <-time.After(200 * time.Millisecond):
	}
}
