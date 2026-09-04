package boa

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The coexistence swap is the case that decides whether the restore terminates.
//
// Asking for 36 at 80MHz lands on 40, every time, because hostapd's 20/40
// coexistence scan swaps primary and secondary. If the live channel were
// compared against the REQUESTED channel alone, that radio would look drifted
// forever and the tick would take its access point down once a second trying
// to correct something that is already correct.
func TestACoexistenceSwapCountsAsSatisfied(t *testing.T) {
	p := ChannelPref{Channel: 36, WidthMHz: 80, Settled: 40}

	if !p.satisfiedBy(40, 80) {
		t.Error("a radio on the channel it settled on reads as drifted, which would restore forever")
	}
	if !p.satisfiedBy(36, 80) {
		t.Error("a radio on the channel actually asked for reads as drifted")
	}
	if p.satisfiedBy(149, 80) {
		t.Error("a radio somewhere else entirely reads as satisfied")
	}
}

// Width is not swapped by anything, so a radio that fell back to 20MHz is a
// radio that is not where it was put -- even on the right channel.
func TestAWidthFallbackIsADrift(t *testing.T) {
	p := ChannelPref{Channel: 149, WidthMHz: 80, Settled: 149}
	if p.satisfiedBy(149, 20) {
		t.Error("80MHz asked for, 20MHz delivered, reported as satisfied")
	}
	if !p.satisfiedBy(149, 80) {
		t.Error("the width it was asked for reads as a drift")
	}
}

// An unreadable channel is not a mismatch. Moving a radio because a read
// failed would turn a transient hostapd hiccup into a deliberate outage.
func TestAnUnreadableChannelIsNotADrift(t *testing.T) {
	p := ChannelPref{Channel: 149, WidthMHz: 80, Settled: 149}
	if !p.satisfiedBy(0, 0) {
		t.Error("a radio whose channel could not be read reads as drifted")
	}
}

func TestPreferencesSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	s := NewChannelStore(path)
	if err := s.Put("wlan-usb", ChannelPref{Channel: 36, WidthMHz: 80, Settled: 40}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("wlan0", ChannelPref{Channel: 11, WidthMHz: 20, Settled: 11}); err != nil {
		t.Fatal(err)
	}

	again := NewChannelStore(path)
	got, ok := again.Get("wlan-usb")
	if !ok {
		t.Fatal("wlan-usb's preference did not survive")
	}
	if got.Channel != 36 || got.WidthMHz != 80 || got.Settled != 40 {
		t.Errorf("got %+v, want {36 80 40}", got)
	}
	// Per radio, not one for the box: two radios on different bands must not
	// share a channel.
	if g, _ := again.Get("wlan0"); g.Channel != 11 {
		t.Errorf("wlan0 = %+v, want channel 11", g)
	}
	if _, ok := again.Get("wlan9"); ok {
		t.Error("a radio nobody has moved reports a preference")
	}
}

// An absent file is a new box, not a fault.
func TestNoFileMeansNoPreferences(t *testing.T) {
	s := NewChannelStore(filepath.Join(t.TempDir(), "channels.json"))
	if len(s.All()) != 0 {
		t.Errorf("a box that has never moved a radio has preferences: %v", s.All())
	}
	if _, ok := s.Get("wlan-usb"); ok {
		t.Error("Get invented a preference")
	}
}

// Corrupt JSON leaves the box with no preference rather than a garbage one: a
// half-written file must not become a channel the tick chases.
func TestCorruptFileIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewChannelStore(path).All(); len(got) != 0 {
		t.Errorf("read %v out of a corrupt file", got)
	}
}

/*
 * The guards that keep the restore from becoming the outage.
 */

// One move at a time per radio. A move takes seconds and the tick is one
// second, so without this the second tick starts a move on top of the first.
func TestOnlyOneRestoreInFlightPerRadio(t *testing.T) {
	var r restoreState
	if !r.begin("wlan-usb") {
		t.Fatal("the first attempt was refused")
	}
	if r.begin("wlan-usb") {
		t.Error("a second move started while the first was still running")
	}
	// A different radio is unaffected: two radios drift independently.
	if !r.begin("wlan0") {
		t.Error("one radio's move blocked another radio's")
	}
	r.done("wlan-usb")
	if !r.begin("wlan-usb") {
		t.Error("the radio stayed blocked after its move finished")
	}
}

// The budget is what makes it terminate. A channel that will not stick is a
// cause the daemon cannot fix, and retrying it forever is an access point that
// goes down every tick in pursuit of it.
func TestTheRestoreGivesUpRatherThanLoopingForever(t *testing.T) {
	var r restoreState
	for i := 0; i < restoreBudget; i++ {
		if !r.begin("wlan-usb") {
			t.Fatalf("attempt %d was refused before the budget was spent", i+1)
		}
		r.done("wlan-usb")
	}
	if r.begin("wlan-usb") {
		t.Fatalf("kept trying after %d attempts, which is an outage per tick", restoreBudget)
	}
	// ...and stays given up, rather than resuming on the next tick.
	if r.begin("wlan-usb") {
		t.Error("resumed after having given up")
	}
}

// Giving up is not permanent: a fresh instruction from the operator, or the
// radio arriving where it belongs, makes it worth trying again.
func TestSettlingClearsAGiveUp(t *testing.T) {
	var r restoreState
	for i := 0; i < restoreBudget; i++ {
		r.begin("wlan-usb")
		r.done("wlan-usb")
	}
	if r.begin("wlan-usb") {
		t.Fatal("the budget did not stop it")
	}
	r.settled("wlan-usb")
	if !r.begin("wlan-usb") {
		t.Error("a radio that settled is still refused, so a later drift is never corrected")
	}
}

// Two moves on one radio must not interleave.
//
// MEASURED 2026-09-04: two sessions moved wlan-usb in the same second, one
// asking for 40 and one for 149, and the first was told it "came back on 149"
// -- a coexistence swap between two different BANDS, which cannot happen. Each
// call had read back the other's result. Harmless while that answer was only a
// message; not harmless once it is written down as the operator's choice and
// acted on later.
func TestOneRadioReconfiguresOneAtATime(t *testing.T) {
	e := &Engine{}
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup

	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			unlock := e.lockRadio("wlan-usb")
			defer unlock()
			mu.Lock()
			order = append(order, name+":start")
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			order = append(order, name+":end")
			mu.Unlock()
		}(name)
	}
	wg.Wait()

	if len(order) != 4 {
		t.Fatalf("got %v", order)
	}
	// Whichever ran first must have finished before the other started.
	if order[0][:1] != order[1][:1] || order[1][2:] != "end" {
		t.Errorf("the two moves interleaved: %v", order)
	}
}

// Two different radios must NOT wait on each other: a two-radio box would
// otherwise take twice as long to set up, for nothing.
func TestDifferentRadiosDoNotBlockEachOther(t *testing.T) {
	e := &Engine{}
	unlock := e.lockRadio("wlan-usb")
	defer unlock()

	done := make(chan struct{})
	go func() {
		e.lockRadio("wlan0")()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("moving wlan0 waited on a move of wlan-usb")
	}
}
