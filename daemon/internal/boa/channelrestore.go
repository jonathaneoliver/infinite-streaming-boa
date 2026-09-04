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
	for _, iface := range e.cfg.WlanPorts {
		pref, ok := e.chp.Get(iface)
		if !ok {
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
		if e.apServiceState(iface) != "serving" {
			continue
		}
		r := e.radioOnFor(iface)
		if r == nil {
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
		go e.restoreOne(iface, pref, r.Channel)
	}
}

func (e *Engine) restoreOne(iface string, pref ChannelPref, was int) {
	defer e.restore.done(iface)

	// Said BEFORE the move, not after. The move drops every client on the
	// radio without telling them, so the log has to explain the outage that is
	// about to happen rather than account for it afterwards.
	e.logEvent(EventRadio, iface, "",
		"%s is on channel %d but was set to %d — putting it back, which drops "+
			"anyone on it", iface, was, pref.Channel)

	now, err := e.MoveChannel(iface, pref.Channel, pref.WidthMHz)
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
	e.logEvent(EventRadio, iface, "", "%s is back on channel %d", iface, now)
}

func (r *restoreState) count(iface string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tries[iface]
}
