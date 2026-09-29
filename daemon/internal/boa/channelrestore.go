package boa

import "sync"

// lockRadio serialises the operations that reconfigure one radio, and returns
// the unlock.
//
// Held across a whole move, which is seconds, so it is a real wait -- but the
// alternative is two reconfigurations interleaving on the same hardware. The
// map is guarded by its own mutex because radios appear and disappear: a USB
// adapter can arrive after the engine was built.
func (e *Engine) lockRadio(iface string) func() {
	e.radioMu.Lock()
	if e.radioLocks == nil {
		e.radioLocks = map[string]*sync.Mutex{}
	}
	m, ok := e.radioLocks[iface]
	if !ok {
		m = &sync.Mutex{}
		e.radioLocks[iface] = m
	}
	e.radioMu.Unlock()

	m.Lock()
	return m.Unlock
}

// radioBusy reports whether a reconfiguration of this radio is under way.
//
// A try, not a wait: the tick asks many times a second's worth of radios and
// must never block behind a move that takes seconds. A true answer is only as
// good as the instant it was taken, which is all the tick needs -- it will ask
// again in a second.
func (e *Engine) radioBusy(iface string) bool {
	e.radioMu.Lock()
	m, ok := e.radioLocks[iface]
	e.radioMu.Unlock()
	if !ok {
		return false
	}
	if m.TryLock() {
		m.Unlock()
		return false
	}
	return true
}

/*
 * Putting a radio back on the channel it was told to be on.
 *
 * The tick already reads every radio's channel and serving state, so noticing
 * that one has moved costs nothing new. What it needs is care about WHEN to act
 * and, far more importantly, when to stop.
 *
 * The hazard this file is mostly about: MoveChannel takes the access point down
 * and brings it back. A restore that fires on every tick would take the AP down
 * once a second forever -- an outage far worse than the wrong channel it is
 * trying to correct, and self-inflicted. Every guard below exists to make that
 * impossible rather than unlikely.
 */

// restoreBudget is how many times a radio's channel is put back before the
// daemon stops trying and says so.
//
// Three, and then silence-with-a-warning rather than retrying forever. If three
// moves have not made the channel stick, a fourth will not either: the cause is
// something the daemon cannot fix from here -- a driver refusing the width, a
// regulatory rule, a config that disagrees -- and continuing would be an
// outage every tick in pursuit of a channel that is not going to happen.
const restoreBudget = 3

// restoreState is the per-radio bookkeeping that keeps the restore terminating.
type restoreState struct {
	mu       sync.Mutex
	inFlight map[string]bool
	tries    map[string]int
	gaveUp   map[string]bool
}

func (r *restoreState) begin(iface string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight == nil {
		r.inFlight, r.tries, r.gaveUp = map[string]bool{}, map[string]int{}, map[string]bool{}
	}
	// A move takes seconds and the tick is one second, so without this the
	// second tick would start a move on top of the first.
	if r.inFlight[iface] || r.gaveUp[iface] {
		return false
	}
	if r.tries[iface] >= restoreBudget {
		r.gaveUp[iface] = true
		return false
	}
	r.tries[iface]++
	r.inFlight[iface] = true
	return true
}

func (r *restoreState) done(iface string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, iface)
}

// settled forgets a radio's failures once it is where it belongs, so a box that
// gave up hours ago will try again after the next deliberate move.
func (r *restoreState) settled(iface string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tries, iface)
	delete(r.gaveUp, iface)
}

// restoreChannels puts each radio back on its remembered channel.
//
// Called from the tick. Does nothing at all on a box where no channel has ever
// been chosen, which is every box until someone moves one.
func (e *Engine) restoreChannels() {
	if e.cfg.Demo {
		return
	}
	// NOT ON OPENWRT, where there is nothing for it to restore. The loop exists
	// because on the Pi and the container a move lives only in the running
	// hostapd: a restarted process reads a config that still names the old
	// channel. On OpenWrt, rememberChannel also writes the move into
	// /etc/config/wireless, and netifd rebuilds hostapd from exactly that, so
	// a restarted radio comes back where it was put. Measured on target 5,
	// 2026-09-29: `wifi up` and forced restarts all returned on the chosen
	// channel. The only thing the loop ever did there was #441 -- undo a
	// deliberate move it caught halfway. A failed uci write is already
	// reported loudly by rememberChannel.
	if e.cfg.OpenWrt {
		return
	}
	for _, iface := range e.WlanPorts() {
		pref, ok := e.chp.Get(iface)
		if !ok {
			continue
		}
		// NOT WHILE THE RADIO IS BEING MOVED. An announced move switches the
		// radio when the countdown ends and writes the new preference only
		// after that, so for over a second the radio is on the new channel
		// while the preference still names the old one. Measured 2026-09-29 on
		// target 5: 1.4 s on every move. A tick landing there read a
		// deliberate move as a drift and undid it, three moves out of three
		// (#441). A move in flight is not a drift, by definition.
		if e.radioBusy(iface) {
			continue
		}
		// SERVING ONLY, and this is the guard that matters most.
		//
		//	off        deliberately switched off. Moving it would switch it
		//	           back on, which is the operator's decision to make and
		//	           not this loop's -- the same mistake as select-radio's
		//	           unconditional rfkill unblock (#186).
		//	down       powered but no BSS. A move cannot fix that and would
		//	           fight whatever recovery is under way.
		//	unmanaged  no control socket; there is nothing to move.
		state, r := restoreReadRadio(e, iface)
		if state != "serving" || r == nil {
			continue
		}
		if pref.satisfiedBy(r.Channel, r.WidthMHz) {
			e.restore.settled(iface)
			continue
		}
		if !e.restore.begin(iface) {
			continue
		}
		// In the background: MoveChannel takes seconds and the tick must not
		// block behind it, or every client's counters stall while a radio
		// comes back.
		go e.restoreOne(iface)
	}
}

// restoreReadRadio and restoreMove are the seams a test replaces, so the
// restore's decisions can be exercised without a hostapd to read or move. In
// production they are the methods they wrap.
var (
	restoreReadRadio = func(e *Engine, iface string) (string, *RadioOn) {
		return e.apServiceState(iface), e.radioOnFor(iface)
	}
	restoreMove = (*Engine).moveChannelLocked
)

func (e *Engine) restoreOne(iface string) {
	defer e.restore.done(iface)

	// DECIDED AGAIN UNDER THE LOCK. The tick decided from what it read a
	// moment ago, and a move can have started since then: the tick's check
	// that no move is in flight and this goroutine taking the lock are two
	// separate instants. The lock is what a move holds from its first SET to
	// writing the new preference, so once it is ours the preference and the
	// radio agree with each other, and what the tick saw may no longer be
	// true. Acting on it anyway is what undid a deliberate move (#441): the
	// restore waited for the lock and then put the radio back on the channel
	// the operator had just moved it off.
	unlock := e.lockRadio(iface)
	defer unlock()

	pref, ok := e.chp.Get(iface)
	if !ok {
		return
	}
	// A fresh read, not the cache: the cache is up to 15 s old, and the move
	// that just released the lock is exactly what it would be stale about.
	e.forgetRadioOn()
	state, r := restoreReadRadio(e, iface)
	if state != "serving" || r == nil {
		return
	}
	if pref.satisfiedBy(r.Channel, r.WidthMHz) {
		e.restore.settled(iface)
		return
	}

	// Said BEFORE the move, not after, so the log explains what is about to
	// happen rather than accounting for it afterwards. What that IS now
	// depends on the radio: one that can announce the switch keeps its
	// clients, and one that cannot drops them without telling them. Which
	// happened is in the move's own event, so this one no longer promises
	// either.
	e.logEvent(EventRadio, iface, "",
		"%s is on channel %d but was set to %d — putting it back", iface, r.Channel, pref.Channel)

	move, err := restoreMove(e, iface, pref.Channel, pref.WidthMHz, MethodAnnounce)
	if err != nil {
		// MoveChannel already logs a warning naming the channel it came back
		// on, so this adds only what that cannot know: whether anything will
		// try again.
		if left := restoreBudget - e.restore.count(iface); left <= 0 {
			e.logEvent(EventWarning, iface, "",
				"%s could not be put back on channel %d after %d attempts, and "+
					"will be left where it is: %v",
				iface, pref.Channel, restoreBudget, err)
		}
		return
	}
	e.restore.settled(iface)
	e.logEvent(EventRadio, iface, "", "%s is back on channel %d, by %s",
		iface, move.Channel, move.Method)
}

func (r *restoreState) count(iface string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tries[iface]
}
