package boa

import (
	"sync"
	"testing"
)

// Ticks must not overlap. BumpControl runs a tick on the HTTP request's own
// goroutine so a policy write applies at once, while Start's ticker runs one
// every second; the tick's maps (e.prev above all) are owned by "the tick" and
// unlocked, which is only true while there is one at a time. On the Cudy,
// 2026-09-30, a sweep sending three policy writes a second crashed boad three
// times in an hour with "concurrent map read and map write" in rateRaw, and
// procd stopped restarting it. Run with -race: without the serialisation the
// detector reports the overlap here.
func TestTicksDoNotOverlap(t *testing.T) {
	e := NewEngine(Config{Demo: true, Tick: 1})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				e.BumpControl()
			}
		}()
	}
	wg.Wait()
}
