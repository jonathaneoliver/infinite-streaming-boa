package boa

import (
	"fmt"
	"sync"
	"time"
)

// What happened, as opposed to what is.
//
// Everything else in this daemon reports STATE: who is connected, what each
// radio is doing, what the counters read. State answers "what is true now" and
// is silent about "what just changed", which on a two-radio box is the more
// interesting question -- a device moving from 5GHz to 2.4GHz is the whole
// point of having two radios, and until this existed the only ways to notice
// were to watch a station count tick or to read hostapd's journal over SSH.
//
// Deliberately IN MEMORY and deliberately lossy. A ring buffer costs nothing,
// rebuilds itself within seconds of a restart for anything still happening, and
// writes nothing to the SD card -- an association event per client per roam,
// persisted, is exactly the kind of steady write that wears a card out. The
// cost is that a deploy or a reboot loses the history, which is a real loss
// during a long soak and is the trade that was chosen knowingly.

// eventRing is how many events are kept. A few hundred covers an afternoon of
// ordinary activity and several minutes of a device flapping, which is the case
// where the log matters most and produces events fastest.
const eventRing = 500

// Event kinds. A closed set, because the interface groups and colours by them
// and a free-text kind would silently fall through to the default styling.
const (
	EventJoin    = "join"    // a client associated
	EventLeave   = "leave"   // a client is no longer associated
	EventRoam    = "roam"    // a client moved from one radio to another
	EventRadio   = "radio"   // a radio changed: channel, power, configuration
	EventAction  = "action"  // something was done through this interface
	EventWarning = "warning" // something worth noticing, e.g. a stranded policy
)

// Event is one thing that happened, already phrased for a human.
//
// The text is composed at the point the event is raised rather than assembled
// in the interface, because that is where the before-and-after are both still
// in hand: "moved wlan-usb -> wlan0" needs the old value, which no later reader
// has.
type Event struct {
	// Seq strictly increases, so a reader can ask for what it has not seen
	// without depending on timestamps, which can repeat within a millisecond.
	Seq  uint64 `json:"seq"`
	At   int64  `json:"at"` // unix ms
	Kind string `json:"kind"`
	Text string `json:"text"`
	// MAC and Iface are set when the event is about a particular client or
	// radio, so the interface can filter without parsing the text.
	MAC   string `json:"mac,omitempty"`
	Iface string `json:"iface,omitempty"`
}

// eventLog is the ring. Its own lock rather than the Engine's: events are
// raised from the tick, from API handlers and from background goroutines
// restoring a radio, and making those contend on the snapshot lock would put
// logging in the path of everything else.
type eventLog struct {
	mu   sync.Mutex
	seq  uint64
	ring []Event
	// labels is the last label seen for each MAC, kept here so an action
	// raised from an API handler can name a device the way the client list
	// does without reaching into the engine's snapshot lock.
	labels map[string]string
}

func (l *eventLog) add(kind, iface, mac, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e := Event{Seq: l.seq, At: time.Now().UnixMilli(), Kind: kind, Text: text,
		MAC: mac, Iface: iface}
	if len(l.ring) < eventRing {
		l.ring = append(l.ring, e)
		return
	}
	// Drop the oldest. copy rather than re-slicing so the backing array does
	// not grow without bound on a box that runs for weeks.
	copy(l.ring, l.ring[1:])
	l.ring[len(l.ring)-1] = e
}

// latest is the highest sequence this log has issued.
//
// Published so a reader can tell that the log RESTARTED underneath it. The ring
// is in memory, so the sequence begins again at 1 on every daemon restart --
// every deploy -- while a page that has been open across one is still holding a
// cursor from the previous run. Asking for events after that cursor then
// returns an empty list, correctly and forever, and the panel renders as a
// quiet box rather than a broken one (#196).
//
// Within one run the sequence only grows, so a latest LOWER than the cursor a
// caller holds is unambiguous: there is no other way to observe it.
func (l *eventLog) latest() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

func (l *eventLog) label(mac string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.labels[mac]; s != "" {
		return s
	}
	return mac
}

func (l *eventLog) setLabels(m map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.labels = m
}

// since returns events newer than seq, oldest first. seq 0 asks for everything
// the ring still holds.
func (l *eventLog) since(seq uint64, limit int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, len(l.ring))
	for _, e := range l.ring {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// Events returns what the log holds newer than seq.
func (e *Engine) Events(seq uint64, limit int) []Event { return e.events.since(seq, limit) }

// LatestEventSeq is the highest sequence issued, so a caller can notice the log
// restarted under it. See eventLog.latest.
func (e *Engine) LatestEventSeq() uint64 { return e.events.latest() }

// logEvent records something that happened. Never blocks the caller for long
// and never fails: a log that can refuse is one more thing to check.
func (e *Engine) logEvent(kind, iface, mac, format string, args ...any) {
	e.events.add(kind, iface, mac, fmt.Sprintf(format, args...))
}

// labelFor names a device the way the interface does, falling back to its MAC.
func (e *Engine) labelFor(mac string) string { return e.events.label(mac) }

// noteLinkAll records a radio-wide drop or nudge. A method rather than a line
// at the call site because the demo path returns before the real one and both
// have to say the same thing -- a demo whose buttons log nothing looks broken
// in exactly the way this panel exists to rule out.
func (e *Engine) noteLinkAll(iface, kind string, n int) {
	verb := "deauthenticated"
	if kind == LinkNudge {
		verb = "disassociated"
	}
	e.logEvent(EventAction, iface, "", "%s %d client(s) on %s — announced, so they reconnect",
		verb, n, iface)
}

// noteMoveChannel, noteProfile and noteSteer exist for the same reason as
// noteLinkAll: each is raised from two places, the real path and the demo one.
func (e *Engine) noteMoveChannel(iface string, channel int) {
	e.logEvent(EventRadio, iface, "",
		"%s moved to channel %d — the AP vanished and reappeared, so clients rejoined",
		iface, channel)
}

func (e *Engine) noteProfile(iface, name string, dropped int) {
	e.logEvent(EventRadio, iface, "", "%s conditioned: profile %q (%d client(s) dropped)",
		iface, name, dropped)
}

func (e *Engine) noteSteer(mac, from, to string) {
	e.logEvent(EventAction, from, mac, "%s asked to move %s → %s (802.11v; it may refuse)",
		e.labelFor(mac), from, to)
}

// describeRadio names a radio the way the interface does -- by band, because
// "5GHz" says something about how a client will behave and "wlan-usb" says
// which adapter happens to serve it.
func (e *Engine) describeRadio(iface string) string {
	if iface == "" {
		return "no radio"
	}
	if r := e.radioOnFor(iface); r != nil && r.Band != "" {
		return fmt.Sprintf("%s (%s ch %d)", iface, r.Band, r.Channel)
	}
	return iface
}

// radioSummary is a radio's on-air configuration as one comparable string.
func (e *Engine) radioSummary(iface string) string {
	r := e.radioOnFor(iface)
	if r == nil || r.Channel == 0 {
		return "off the air"
	}
	return fmt.Sprintf("ch %d · %d MHz · %s", r.Channel, r.WidthMHz, r.Mode)
}

// noteRadioChanges reports a radio whose channel, width or mode changed.
//
// The point is changes this daemon did NOT make -- a hand-run hostapd_cli, a
// driver falling back to 20MHz, an AP that came back somewhere other than
// where it was asked to. Everything done through the interface records itself
// and then calls syncRadioState, so it is not reported twice.
func (e *Engine) noteRadioChanges() {
	for _, w := range e.cfg.WlanPorts {
		now := e.radioSummary(w)
		if was := e.setRadioState(w, now); was != "" && was != now {
			e.logEvent(EventRadio, w, "", "%s changed: %s → %s", w, was, now)
		}
	}
}

// syncRadioState records a radio's configuration WITHOUT logging it, for use
// straight after an action that already said what it did. Without this, the
// tick would notice the same change a second later and report it again.
func (e *Engine) syncRadioState(iface string) { e.setRadioState(iface, e.radioSummary(iface)) }

/*
 * Whether a radio is SERVING, which the channel summary above cannot say.
 *
 * radioSummary is built from channel/width/mode, and a DISABLED BSS still
 * answers STATUS with the channel it will use when it returns -- so a radio
 * serving nobody produced exactly the same summary as one serving happily, and
 * the tick saw no change. That is how a radio came back from a power cut with
 * no access point on it, warned once, recovered minutes later, and left the
 * warning standing as the last word on it (issue #174).
 *
 * Four states rather than a bool, because "not serving" has three quite
 * different meanings and only one of them is worth an alarm:
 *
 *	serving    the BSS is up
 *	down       powered on, hostapd present, BSS not up -- the fault case
 *	off        deliberately switched off; not serving is CORRECT here, and
 *	           warning would make the power button alarm every time it is used
 *	unmanaged  no control socket, so there is no BSS to have an opinion about
 */
func (e *Engine) apServiceState(iface string) string {
	if e.cfg.Demo {
		return "serving"
	}
	if on, known := radioPowered(iface); known && !on {
		return "off"
	}
	if !hostapdAvailable(iface) {
		return "unmanaged"
	}
	if r := e.radioOnFor(iface); r != nil && r.Serving {
		return "serving"
	}
	return "down"
}

// noteAPServing reports a radio that stopped serving, and -- the half that was
// missing -- one that started again.
//
// Only the two edges that carry information are logged. A deliberate power
// cycle passes through "off", and notePower has already said so in the operator's
// own terms; repeating it here would double every press of the button.
func (e *Engine) noteAPServing() {
	for _, w := range e.cfg.WlanPorts {
		now := e.apServiceState(w)
		was := e.setAPServing(w, now)
		if was == "" || was == now {
			continue
		}
		switch {
		case was == "serving" && now == "down":
			// SERVING -> down specifically, not "anything -> down". A normal
			// power cycle passes off -> down -> serving on its way back up, and
			// warning on the middle step would make the power button raise an
			// alarm every time it is used. What this catches is a radio that
			// was serving and stopped: a hand-run DISABLE, a driver that
			// wedged, a cut that did not come back. The tick does not know
			// which, and does not need to -- it reports the fact.
			e.logEvent(EventWarning, w, "",
				"%s stopped serving — it is powered on but has no access point on it", w)
		case was == "down" && now == "serving":
			e.logEvent(EventRadio, w, "", "%s is serving again", w)
		}
	}
}

// syncAPServing records the serving state WITHOUT logging it, for the same
// reason syncRadioState exists: an action that already reported what it did
// must not have the next tick report it a second time.
func (e *Engine) syncAPServing(iface string) { e.setAPServing(iface, e.apServiceState(iface)) }

// setAPServing stores a radio's serving state and returns the previous one, or
// "" on the first reading.
func (e *Engine) setAPServing(iface, now string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prevAPServing == nil {
		e.prevAPServing = map[string]string{}
	}
	was := e.prevAPServing[iface]
	e.prevAPServing[iface] = now
	return was
}

// setRadioState stores a radio's summary and returns the previous one, or ""
// when this is the first reading. Locked: the tick and an API handler both
// reach it.
func (e *Engine) setRadioState(iface, now string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prevRadioState == nil {
		e.prevRadioState = map[string]string{}
	}
	was := e.prevRadioState[iface]
	e.prevRadioState[iface] = now
	return was
}

// noteClientChanges raises the join, leave and roam events by comparing this
// tick's associations with the last one.
//
// Roam is the reason this exists. It is invisible in state -- a client is
// simply on a different radio than it was, with nothing anywhere saying it
// moved -- and it is the single most interesting thing a two-radio box does.
func (e *Engine) noteClientChanges(now map[string]string, labels map[string]string) {
	e.events.setLabels(labels)
	name := func(mac string) string {
		if l := labels[mac]; l != "" && l != mac {
			return l
		}
		return mac
	}
	for mac, iface := range now {
		was, seen := e.prevRadio[mac]
		switch {
		case !seen:
			e.logEvent(EventJoin, iface, mac, "%s joined %s",
				name(mac), e.describeRadio(iface))
		case was != iface:
			// The event the whole log is for.
			e.logEvent(EventRoam, iface, mac, "%s moved %s → %s",
				name(mac), e.describeRadio(was), e.describeRadio(iface))
		}
	}
	for mac, was := range e.prevRadio {
		if _, still := now[mac]; !still {
			e.logEvent(EventLeave, was, mac, "%s left %s",
				name(mac), e.describeRadio(was))
		}
	}
	e.prevRadio = now
}
