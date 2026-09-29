package boa

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

/*
 * #441: a deliberate move undone by the restore that caught it halfway.
 *
 * An announced move switches the radio when the countdown ends and writes the
 * new preference only afterwards -- 1.4 s later on target 5 -- so for that
 * long the radio is on the new channel while the preference names the old
 * one. These tests put the restore inside that window on purpose, because on
 * hardware it takes a tick landing in it by chance.
 */

// fakeRestoreRadio replaces the restore's two seams for one test: what the
// radio reads as, and a move that only records it was asked for.
type fakeRestoreRadio struct {
	mu      sync.Mutex
	channel int
	moves   []int
	moved   chan int
}

func (f *fakeRestoreRadio) set(ch int) {
	f.mu.Lock()
	f.channel = ch
	f.mu.Unlock()
}

func (f *fakeRestoreRadio) install(t *testing.T) {
	t.Helper()
	f.moved = make(chan int, 4)
	read, move := restoreReadRadio, restoreMove
	restoreReadRadio = func(_ *Engine, iface string) (string, *RadioOn) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return "serving", &RadioOn{Iface: iface, Channel: f.channel, WidthMHz: 80}
	}
	restoreMove = func(_ *Engine, _ string, channel, widthMHz int, _ string) (ChannelMove, error) {
		f.mu.Lock()
		f.moves = append(f.moves, channel)
		f.channel = channel
		f.mu.Unlock()
		f.moved <- channel
		return ChannelMove{Channel: channel, WidthMHz: widthMHz, Method: MethodAnnounce}, nil
	}
	t.Cleanup(func() { restoreReadRadio, restoreMove = read, move })
}

func (f *fakeRestoreRadio) moveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.moves)
}

// restoreEngine is a box with one radio whose remembered channel is 149.
func restoreEngine(t *testing.T, openwrt bool) *Engine {
	t.Helper()
	e := &Engine{
		cfg: Config{WlanPorts: []string{"phy1-ap0"}, OpenWrt: openwrt},
		chp: NewChannelStore(filepath.Join(t.TempDir(), "channels.json")),
	}
	if err := e.chp.Put("phy1-ap0", ChannelPref{Channel: 149, WidthMHz: 80, Settled: 149}); err != nil {
		t.Fatal(err)
	}
	return e
}

// waitRestoreDone waits for the background restore to finish, which is when
// it gives up its in-flight mark.
func waitRestoreDone(t *testing.T, e *Engine, iface string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.restore.mu.Lock()
		busy := e.restore.inFlight[iface]
		e.restore.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the restore never finished")
}

func TestRadioBusyMeansAMoveIsInFlight(t *testing.T) {
	e := &Engine{}
	if e.radioBusy("phy1-ap0") {
		t.Error("a radio nothing has ever moved reads as busy")
	}
	unlock := e.lockRadio("phy1-ap0")
	if !e.radioBusy("phy1-ap0") {
		t.Error("a radio being moved reads as idle")
	}
	if e.radioBusy("phy0-ap0") {
		t.Error("one radio's move made another read as busy")
	}
	unlock()
	if e.radioBusy("phy1-ap0") {
		t.Error("the radio still reads as busy after its move finished")
	}
}

// The tick that undid #441's moves: radio already on 36, preference still 149,
// the move still holding the lock.
func TestATickDuringAMoveDoesNotRestore(t *testing.T) {
	var f fakeRestoreRadio
	f.install(t)
	e := restoreEngine(t, false)
	f.set(36)

	unlock := e.lockRadio("phy1-ap0")
	e.restoreChannels()
	if n := e.restore.count("phy1-ap0"); n != 0 {
		t.Fatalf("a tick during a move started %d restore(s); a move in flight is not a drift", n)
	}
	unlock()

	// The same state with no move in flight IS a drift, and must still be
	// restored -- otherwise the test above passes because the fake cannot be
	// seen drifting at all, and the guard has switched the feature off.
	e.restoreChannels()
	select {
	case ch := <-f.moved:
		if ch != 149 {
			t.Errorf("restored to %d, want the remembered 149", ch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a real drift, with no move in flight, was not restored")
	}
	waitRestoreDone(t, e, "phy1-ap0")
}

// The sequence the event log showed on target 5: the restore decided while
// the radio looked drifted, waited for the move's lock, and then acted on
// what it had decided before waiting. It must decide again once it holds the
// lock, when the move has finished and written the new preference.
func TestARestoreRechecksOnceItHoldsTheLock(t *testing.T) {
	var f fakeRestoreRadio
	f.install(t)
	e := restoreEngine(t, false)

	// A move to 36 is in flight: it holds the lock, and the radio has
	// already switched.
	unlock := e.lockRadio("phy1-ap0")
	f.set(36)

	// A restore that started just before the move took the lock.
	if !e.restore.begin("phy1-ap0") {
		t.Fatal("could not start a restore")
	}
	go e.restoreOne("phy1-ap0")

	// The move finishes: the preference is written, then the lock released.
	time.Sleep(20 * time.Millisecond) // let the restore reach the lock
	if err := e.chp.Put("phy1-ap0", ChannelPref{Channel: 36, WidthMHz: 80, Settled: 36}); err != nil {
		t.Fatal(err)
	}
	unlock()
	waitRestoreDone(t, e, "phy1-ap0")

	if n := f.moveCount(); n != 0 {
		t.Errorf("the restore moved the radio %d time(s) after the operator's move completed; "+
			"it undid a deliberate move", n)
	}
	for _, ev := range e.Events(0, 100) {
		if strings.Contains(ev.Text, "putting it back") {
			t.Errorf("logged %q for a radio that was where it had been put", ev.Text)
		}
	}
}

// On OpenWrt a move is written to uci, and a restarted radio comes back on it,
// so the loop has nothing to restore -- even for a radio that really has
// drifted from what boa remembers.
func TestTheRestoreDoesNotRunOnOpenWrt(t *testing.T) {
	var f fakeRestoreRadio
	f.install(t)
	e := restoreEngine(t, true)
	f.set(36)

	e.restoreChannels()
	time.Sleep(20 * time.Millisecond)
	if n := e.restore.count("phy1-ap0"); n != 0 || f.moveCount() != 0 {
		t.Errorf("the restore ran on OpenWrt: %d attempt(s), %d move(s)", n, f.moveCount())
	}
}
