package boa

import (
	"os/exec"
	"sync"
	"time"
)

/*
 * How busy the air is on each radio's operating channel.
 *
 * A DELTA between two reads, never a single one. The survey counters are
 * monotonic since the interface came up, so one sample gives a fraction
 * averaged over the whole uptime of the access point -- which on a box that has
 * been up for days is a number nobody is asking for. See DATA-CONTRACT
 * Source L.
 *
 * MEASURED 2026-09-04, and this is what sets the interval: on a healthy,
 * beaconing mt7921u the transmit counter moved by +0 ms over 2 s, +0 over 3 s
 * and +0 over 5 s, then +64 ms over 15 s. The driver updates these coarsely, so
 * a short window reads as a flat zero on a radio that is working perfectly. A
 * sampler on a 1 Hz tick would report "idle" forever.
 *
 * The same day established the other half: `iw dev wlan0 survey dump` on
 * brcmfmac returns NOTHING -- no blocks, exit 0. So this figure exists for some
 * radios and not others, and the absence is reported as absence rather than as
 * zero. A gauge reading 0% on a radio that has never been asked is worse than
 * no gauge.
 */

// airtimeEvery is the sampling interval, and it is long on purpose. See above:
// anything much shorter reads zero on hardware that is working.
const airtimeEvery = 15 * time.Second

type airtimeSample struct {
	active, busy int64
	at           time.Time
}

type airtimeWatch struct {
	mu   sync.Mutex
	prev map[string]airtimeSample
	pct  map[string]float64
}

// AirtimePct is the busy fraction per radio, for radios that report one.
//
// Busy INCLUDING this box's own transmissions, and the interface says so where
// it shows the number. Foreign airtime -- what everyone else is using -- would
// be the more useful metric, but it is not reliably computable here: measured
// on this driver, `receive + transmit` can exceed `busy` (79 009 + 219 107
// against 271 397 ms), so subtracting them yields a negative for a channel that
// is plainly in use. Reporting a number that is sometimes nonsense is worse
// than reporting a simpler one honestly.
func (e *Engine) AirtimePct() map[string]float64 {
	e.airtime.mu.Lock()
	defer e.airtime.mu.Unlock()
	out := make(map[string]float64, len(e.airtime.pct))
	for k, v := range e.airtime.pct {
		out[k] = v
	}
	return out
}

// watchAirtime samples every radio forever. One goroutine for all of them: the
// work is a subprocess per radio every fifteen seconds, which does not deserve
// a goroutine each.
func (e *Engine) watchAirtime() {
	if e.cfg.Demo {
		return
	}
	for {
		for _, iface := range e.cfg.WlanPorts {
			e.sampleAirtime(iface)
		}
		time.Sleep(airtimeEvery)
	}
}

func (e *Engine) sampleAirtime(iface string) {
	raw, err := exec.Command("iw", "dev", iface, "survey", "dump").Output()
	if err != nil {
		return // the radio may be gone; absence stays absence
	}
	chans := parseSurvey(string(raw))
	// The ONE populated block is the operating channel. Every other block is a
	// channel the radio has never visited, and ranking or averaging across them
	// is how a "least busy channel" ends up recommending one nobody looked at.
	var cur airtimeSample
	for _, c := range chans {
		if c.ActiveMs > cur.active {
			cur = airtimeSample{active: c.ActiveMs, busy: c.BusyMs, at: time.Now()}
		}
	}
	if cur.active == 0 {
		return // this driver reports nothing; absence stays absence
	}

	e.airtime.mu.Lock()
	defer e.airtime.mu.Unlock()
	if e.airtime.prev == nil {
		e.airtime.prev = map[string]airtimeSample{}
		e.airtime.pct = map[string]float64{}
	}
	prev, had := e.airtime.prev[iface]
	e.airtime.prev[iface] = cur
	if !had {
		return // the first read establishes a baseline and says nothing yet
	}
	dActive := cur.active - prev.active
	dBusy := cur.busy - prev.busy
	// A counter that went backwards means the interface restarted under us --
	// a channel move, a power cycle. Start again rather than reporting a
	// negative or an enormous fraction.
	if dActive <= 0 || dBusy < 0 {
		delete(e.airtime.pct, iface)
		return
	}
	pct := float64(dBusy) / float64(dActive) * 100
	if pct > 100 {
		pct = 100
	}
	e.airtime.pct[iface] = pct
}
