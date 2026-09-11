package boa

import "time"

/*
 * Keeping the contention figures fresh, without ever costing an outage.
 *
 * A scan describes a moment and the moment moves: measured 2026-09-07, channel
 * 40 read 9.8% busy with our radio idle and 69.8% under load a few minutes
 * later. A figure on a title bar that only refreshes when somebody presses scan
 * is a figure that is usually wrong, so it is polled.
 *
 * The whole difficulty is that scanning is free on one radio here and expensive
 * on the others:
 *
 *	wlan0 (brcmfmac)   scans while serving, BOTH bands, hostapd stays ENABLED
 *	wlan-usb (mt7921u) refuses while beaconing; only works with the BSS down
 *
 * Polling the second kind would drop every client on it, once a minute, for
 * ever. So this NEVER auto-scans a radio unless it has been established that
 * doing so costs nothing -- and because one wlan0 scan covers channel 40 and
 * channel 149 as well as its own, one free radio answers for the whole box.
 * See airview.go for the merge.
 */

// airScanEvery is how often the poll runs.
//
// FIFTEEN SECONDS is a deliberate trade and the arithmetic should be visible,
// because the cost lands on one radio and the benefit is spread across all of
// them. Against a scan measured at 1298 ms:
//
//	every 60s   2.2% of the scanning radio's time off its own channel
//	every 15s   8.7%
//
// A minute was tried first and was too stale to be useful: a 30-second iperf3
// run finished without the figure moving once, which on a box built to watch
// what a test does to the air is the wrong answer. Fifteen seconds means a run
// of any realistic length refreshes it at least once.
//
// The cost is real but currently falls on nobody: the free scanner here is the
// onboard radio, which carries no clients. It would be felt by a client that
// joined it -- "free" means the ACCESS POINT stays up, verified after every
// scan, not that a client rides through the off-channel window undisturbed. If
// this box ever serves real traffic on its scanning radio, this constant is the
// first thing to revisit.
//
// scanFreqs is what makes the trade affordable at all. Unrestricted the same
// scan takes 4047 ms, and this interval would cost 27% of the radio.
const airScanEvery = 15 * time.Second

// watchAir refreshes the contention figures on a radio that can afford it.
func (e *Engine) watchAir() {
	for {
		time.Sleep(airScanEvery)
		e.airScanOnce()
		// An advertised BSS Load lives in the running hostapd and nowhere else,
		// so every restart silently drops it -- and hostapd is restarted by a
		// profile, a channel move, a power cycle and a USB re-enumeration, which
		// is too many paths to hook one at a time. Re-asserting it here is two
		// control-socket commands per overridden radio, idempotent, and covers
		// the paths nobody has thought of yet.
		e.reapplyBSSLoad()
	}
}

// airScanOnce scans at most one radio, and only a safe one.
//
// Two rules, and both are about not surprising anyone:
//
// A radio is only auto-scanned once it is KNOWN to scan without an outage,
// which ScanBand records from what actually happened rather than from the
// driver's name. Until then the only radio this will touch is one with no
// stations on it, where the question can be settled at no cost to anybody --
// which is how a box learns that its onboard radio is the free one without
// gambling a roomful of clients to find out.
//
// And nothing is scanned while a pattern is driving a client. Taking a radio
// off channel for 1.3s in the middle of a timed impairment run puts a notch in
// the measurement the operator is there to take, and a background refresh must
// never be the thing that corrupts a result.
func (e *Engine) airScanOnce() {
	if e.cfg.Demo || e.patternRunning() {
		return
	}
	// A KNOWN-FREE radio first, always, and one is enough for the whole box --
	// its scan covers the other radios' channels and the merge hands the answer
	// on. Only if nothing is known free is a radio probed to find out.
	//
	// The order is the whole safety property, and getting it wrong cost a real
	// outage. MEASURED 2026-09-07: a gather emptied wlan-usb2, the poll took the
	// first candidate that was merely IDLE, and put that radio off the air for
	// 8 seconds establishing what wlan0 had already established -- while wlan0
	// sat there recorded as free and scanning for nothing. Nobody was dropped,
	// because the radio was empty, but the outage bought nothing at all.
	if w := e.freeScanner(); w != "" {
		e.airScan(w)
		return
	}
	// Nothing known free yet, so learn -- but only on a radio with no stations,
	// where the answer costs no client anything even if it turns out to need
	// the access point taken down. This is how the box discovers which of its
	// radios is the cheap one without gambling a roomful of clients to find out,
	// and it happens at most once per radio: the outcome is recorded either way.
	for _, w := range e.cfg.WlanPorts {
		if e.scanCostKnown(w) || !e.radioIdle(w) {
			continue
		}
		e.airScan(w)
		return
	}
}

// airScan refreshes one radio's reading.
func (e *Engine) airScan(iface string) {
	// readyToScan, not radioReady: a listen-only radio has no hostapd control
	// socket, and radioReady's insistence on one made this return here without
	// a word -- a configured scanner that never scanned, and figures that
	// stayed stale for the one reason nothing was reporting. See #288.
	if err := e.readyToScan(iface); err != nil {
		e.noteScanBlocked(iface, err.Error())
		return
	}
	e.noteScanBlocked(iface, "")
	// scanBandFree, never ScanBand: this refreshes a reading, and a background
	// task is never entitled to take an access point off the air to do it. A
	// driver that refuses while serving has answered the question at no cost.
	//
	// It also never moves a radio -- a background task that relocated an access
	// point on its own would be the single most surprising thing this box does.
	if _, err := e.scanBandFree(iface); err != nil {
		e.logEvent(EventRadio, iface, "",
			"background scan of %s failed, so its contention figures are stale: %v", iface, err)
	}
}

// freeScanner is a radio that can be scanned without an outage, or empty.
//
// A LISTEN-ONLY radio first, and without consulting scanFree at all. That map
// answers "did scanning this radio take its access point down", and a scanner
// has no access point for the question to be about: there is nothing to drop,
// nothing to re-enable, and nothing to learn by trying. Every box that has one
// therefore skips the probe-an-idle-radio dance below entirely.
//
// LinkExists is checked rather than assumed, because a configured scanner that
// is not plugged in must fall through to the serving radios rather than make
// the poll spend every round failing on an interface that is not there.
func (e *Engine) freeScanner() string {
	for _, s := range e.cfg.ScanPorts {
		if e.cfg.Demo || LinkExists(s) {
			return s
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, w := range e.cfg.WlanPorts {
		if e.scanFree[w] {
			return w
		}
	}
	return ""
}

// noteScanBlocked reports that a radio cannot be scanned at all, on the edge.
//
// An empty reason clears it, which is what says the figures are live again.
// Edge-triggered because the poll is a 15-second loop and the conditions here
// -- an interface that is down, hardware that is not plugged in -- persist: a
// line per round would be four a minute for ever. Reporting nothing at all is
// the failure mode this replaces.
func (e *Engine) noteScanBlocked(iface, reason string) {
	e.mu.Lock()
	if e.scanBlocked == nil {
		e.scanBlocked = map[string]string{}
	}
	was := e.scanBlocked[iface]
	if reason == "" {
		delete(e.scanBlocked, iface)
	} else {
		e.scanBlocked[iface] = reason
	}
	e.mu.Unlock()

	if reason == was {
		return
	}
	if reason != "" {
		e.logEvent(EventWarning, iface, "",
			"%s cannot be scanned, so its contention figures will go stale: %s",
			iface, reason)
		return
	}
	e.logEvent(EventAction, iface, "",
		"%s can be scanned again; its contention figures are live", iface)
}

// scanCostKnown reports that this radio's scan cost has already been observed,
// so there is nothing left to learn by probing it again.
func (e *Engine) scanCostKnown(iface string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.scanFree[iface]
	return ok
}

// radioIdle reports that nothing is associated, so a scan can cost no client
// anything even if it turns out to need the access point taken down.
func (e *Engine) radioIdle(iface string) bool {
	return len(StationDump(iface)) == 0
}

// patternRunning reports whether any client is mid-pattern.
func (e *Engine) patternRunning() bool {
	for mac := range e.st.All() {
		if e.player.Running(mac) {
			return true
		}
	}
	return false
}

// scanIsFree reports whether the last scan of this radio kept its access point
// on the air. Absent until one has been taken -- unknown is not free.
func (e *Engine) scanIsFree(iface string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.scanFree[iface]
}

// rememberScanCost records what a scan actually cost this radio, so the poll
// can decide whether it may ever repeat it unattended.
//
// Recorded from the OUTCOME, not from the driver: radiopower.go documents both
// drivers here behaving in exactly opposite ways, and one of them can exit zero
// while having taken the BSS down anyway.
func (e *Engine) rememberScanCost(iface string, disrupted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scanFree == nil {
		e.scanFree = map[string]bool{}
	}
	e.scanFree[iface] = !disrupted
}
