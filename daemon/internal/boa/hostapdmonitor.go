package boa

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

/*
 * Reading what hostapd says back, as opposed to what it replies.
 *
 * Every other use of the control socket in this codebase is request/reply:
 * hostapdCmd dials, writes one command, reads one answer and closes. That
 * cannot see the messages hostapd sends UNASKED, and one of those is the only
 * evidence a steer produced any result at all.
 *
 * A steer is an 802.11v BSS Transition Management request, and the client
 * answers with a Response frame carrying a status code. hostapd forwards that
 * to anything ATTACHed to its control socket as
 *
 *	BSS-TM-RESP <mac> status_code=<n> bss_termination_delay=<n> [target_bssid=<mac>]
 *
 * Without a monitor connection that line goes nowhere, so the interface could
 * only ever report that a request had been SENT. Whether a device honours a
 * steer is the behaviour the control exists to test, and "it may refuse" is not
 * an answer -- it is the question restated.
 */

// btmWait is how long a steered client has to answer before its silence is
// reported as the outcome.
//
// It was 5s, on the reasoning that a BSS Transition Management Response carries
// a decision and no scanning, so a client that has not answered promptly is not
// thinking about it. The reasoning was wrong about real clients. MEASURED
// 2026-09-07 on this box: an iPhone and a MacBook each answered a steer between
// eight and nine seconds after the request, repeatedly, and were reported mute
// every time -- the answer then arriving with no pending request to attach it
// to, so the line read "asked to move to another radio" instead of naming the
// destination. Twelve seconds covers what was measured with margin.
//
// Generous rather than tight, because the two errors are not symmetric: waiting
// too long costs a later log line, while being early calls a client mute that
// was about to speak and then mis-attributes its answer when it does.
//
// NOT equal to evictDisassocSec any more, which it used to be -- see there.
const btmWait = 12 * time.Second

// pendingSteer is a request whose answer has not arrived yet.
type pendingSteer struct {
	iface string
	to    string
	at    time.Time
	// insist records that this steer carried disassoc_imminent, so silence can
	// be reported with what happens next rather than as a dead end. Without it
	// the log said "did not answer, and has not moved" and then the device
	// moved anyway seconds later, with nothing in between explaining why.
	insist bool
}

// btmStatus renders an 802.11 BSS Transition Management status code.
//
// In words, not as a number. A bare "status_code=7" in an activity log is a
// value the reader has to go and look up, which in practice means it does not
// get read -- and the whole point of surfacing this is that the outcome should
// be legible at a glance. The numbers are from IEEE 802.11 Table 9-428.
func btmStatus(code int) string {
	switch code {
	case 0:
		return "accepted"
	case 1:
		return "refused, giving no reason"
	case 2:
		return "refused: it had no recent measurement of the other radio"
	case 3:
		return "refused: the other radio has no capacity for it"
	case 4:
		return "refused: it does not want this BSS to go away"
	case 5:
		return "refused, asking for more time before this BSS goes away"
	case 6:
		return "refused, offering its own list of candidates instead"
	case 7:
		return "refused: none of the candidates offered suit it"
	case 8:
		return "refused: it is leaving this network entirely"
	default:
		return fmt.Sprintf("refused with status code %d", code)
	}
}

// notePendingSteer records a request so that an answer -- or the absence of one
// -- can be attributed to it.
func (e *Engine) notePendingSteer(mac, fromIface, toIface string, insist bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingSteers == nil {
		e.pendingSteers = map[string]pendingSteer{}
	}
	e.pendingSteers[mac] = pendingSteer{
		iface: fromIface, to: toIface, at: time.Now(), insist: insist,
	}
}

// takePendingSteer removes and returns a pending request, if there is one.
func (e *Engine) takePendingSteer(mac string) (pendingSteer, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.pendingSteers[mac]
	if ok {
		delete(e.pendingSteers, mac)
	}
	return p, ok
}

// reportMuteSteers says so when a steered client never answered.
//
// The silence is the finding, so it is stated rather than left as an absence.
// A reader who sees "asked to move" and then nothing cannot tell whether the
// client refused, whether the request failed, or whether they simply have not
// waited long enough -- and a log that requires that distinction to be guessed
// is the silent failure this codebase keeps being bitten by.
//
// Whether it MOVED is reported separately from whether it ANSWERED, because
// they are independent and this was measured to be so. On 2026-09-04 an iPhone
// asked to leave wlan-usb moved one second later and sent no response frame at
// all: it plainly acted on the request. An earlier version of this said such a
// client "does not support 802.11v transitions, or chose to ignore it", two
// lines below the roam event proving it had done neither. Reporting the two
// facts separately is the only way to be right about both.
func (e *Engine) reportMuteSteers() {
	now := time.Now()
	type mute struct {
		mac string
		p   pendingSteer
	}
	var overdue []mute
	e.mu.Lock()
	for mac, p := range e.pendingSteers {
		if now.Sub(p.at) >= btmWait {
			overdue = append(overdue, mute{mac, p})
			delete(e.pendingSteers, mac)
		}
	}
	e.mu.Unlock()

	for _, m := range overdue {
		label := e.labelFor(m.mac)
		if to := e.radioFor(m.mac); to != "" && to != m.p.iface {
			// It went, without saying so. Worth its own line: the transition
			// worked, and the only thing missing is the acknowledgement.
			e.logEvent(EventAction, m.p.iface, m.mac,
				"%s moved to %s without answering — it acted on the request "+
					"but sent no 802.11v response",
				label, e.describeRadio(to))
			continue
		}
		if m.p.insist {
			// Says what happens NEXT, because something does. The deadline is
			// measured from the request, so by the time silence is reported
			// most or all of it has already elapsed -- and an operator told
			// only "has not moved" has no reason to keep watching the very
			// window in which the interesting thing happens.
			//
			// Three cases, because btmWait is now longer than
			// evictDisassocSec and the tense has to follow: still to come,
			// arriving about now, or already past. Saying "is being
			// disassociated now" seven seconds after it happened would be the
			// same class of lie as reporting a mute client that had answered.
			left := evictDisassocSec - int(btmWait/time.Second)
			var when string
			switch {
			case left > 1:
				when = fmt.Sprintf("It is being disassociated in about %ds and will then pick", left)
			case left > -2:
				when = "It is being disassociated now and will then pick"
			default:
				when = fmt.Sprintf("It was disassociated about %ds ago and is picking", -left)
			}
			e.logEvent(EventAction, m.p.iface, m.mac,
				"%s did not answer the request to leave for %s. %s a radio for "+
					"itself — which need not be that one",
				label, m.p.to, when)
			continue
		}
		e.logEvent(EventAction, m.p.iface, m.mac,
			"%s did not answer the request to move to %s, and has not moved",
			label, m.p.to)
	}
}

// watchHostapdEvents keeps a monitor connection to every radio's control
// socket for as long as the daemon runs.
//
// One goroutine per radio, each responsible for its own reconnection: hostapd
// is restarted by select-radio, by a channel move, and by hand, and a monitor
// that gave up on the first of those would report nothing for the rest of the
// box's uptime while looking exactly like a box where nobody steered anything.
func (e *Engine) watchHostapdEvents() {
	if e.cfg.Demo {
		return
	}
	for _, iface := range e.cfg.WlanPorts {
		go e.watchOneRadio(iface)
	}
}

func (e *Engine) watchOneRadio(iface string) {
	// Backoff between attempts, not a tight loop: a radio may be absent for
	// the whole life of the daemon -- an unplugged adapter -- and retrying it
	// every millisecond would spend a core on a socket that is never coming.
	const retry = 5 * time.Second
	for {
		conn, err := hostapdAttach(iface)
		if err != nil {
			time.Sleep(retry)
			continue
		}
		e.readHostapdEvents(iface, conn)
		// Say goodbye before dropping the socket, so hostapd forgets this
		// monitor rather than discovering it is gone one failed send at a
		// time. Best effort by nature -- the usual reason the read loop ended
		// is that hostapd is no longer there to be told.
		hostapdDetach(conn)
		conn.Close()
		time.Sleep(retry)
	}
}

// hostapdAttach opens a monitor connection and asks hostapd to send events to
// it.
//
// The same abstract-socket trick hostapdCmd documents at length: the daemon
// runs with PrivateTmp, so a socket under /tmp is invisible to hostapd and its
// messages are silently dropped. Abstract sockets live in the network
// namespace, which both share.
func hostapdAttach(iface string) (*net.UnixConn, error) {
	if !hostapdAvailable(iface) {
		return nil, fmt.Errorf("no control socket for %s", iface)
	}
	// STABLE per radio, with no pid in it.
	//
	// hostapd remembers each ATTACHed address and keeps sending to it. An
	// address carrying the daemon's pid is a NEW monitor on every restart, so
	// each deploy left another dead registration behind and hostapd logged
	// "CTRL_IFACE monitor: Connection refused" for every event against every
	// one of them until it gave up after ten failures. MEASURED 2026-09-04: 34
	// of those in half an hour across a day's deploys.
	//
	// A stable name means a restart re-attaches as the SAME monitor rather
	// than accumulating a new one, so the worst case is one stale registration
	// per radio instead of one per deploy.
	local := fmt.Sprintf("@boa-hostapd-mon-%s", iface)
	conn, err := net.DialUnix("unixgram",
		&net.UnixAddr{Name: local, Net: "unixgram"},
		&net.UnixAddr{Name: hostapdSocket(iface), Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ATTACH")); err != nil {
		conn.Close()
		return nil, err
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "OK") {
		conn.Close()
		return nil, fmt.Errorf("%s refused ATTACH: %q", iface, strings.TrimSpace(string(buf[:n])))
	}
	return conn, nil
}

// hostapdDetach unregisters a monitor connection.
//
// Short deadline and the reply ignored: this runs on a path where hostapd has
// usually just gone away, and waiting on an answer that is not coming would
// hold the reconnect loop open for no benefit. Sending it costs one datagram
// and saves hostapd ten failed deliveries per orphaned monitor.
func hostapdDetach(conn *net.UnixConn) {
	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = conn.Write([]byte("DETACH"))
}

// readHostapdEvents reads until the connection fails, which is how a hostapd
// restart is noticed.
func (e *Engine) readHostapdEvents(iface string, conn *net.UnixConn) {
	buf := make([]byte, 4096)
	for {
		// A deadline rather than a blocking read, so a socket whose hostapd
		// died quietly is noticed rather than held open forever. A timeout is
		// not an error: most of the time nothing has happened, which is the
		// normal state of a radio.
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if !hostapdAvailable(iface) {
					return // hostapd has gone; reconnect when it comes back
				}
				continue
			}
			return
		}
		e.handleHostapdEvent(iface, string(buf[:n]))
	}
}

// handleHostapdEvent turns one control-socket message into an activity-log
// line, for the messages worth one.
//
// Deliberately narrow. hostapd is talkative and most of what it says is either
// already reported by the tick -- associations, disassociations -- or is noise
// at this altitude. What is here is what nothing else can see.
func (e *Engine) handleHostapdEvent(iface, msg string) {
	// Events arrive with a priority prefix, e.g. "<3>BSS-TM-RESP ...".
	if i := strings.IndexByte(msg, '>'); i >= 0 && strings.HasPrefix(msg, "<") {
		msg = msg[i+1:]
	}
	msg = strings.TrimSpace(msg)

	// Associations, timestamped by hostapd rather than by the tick that would
	// otherwise notice them up to a second later. These raise no event of their
	// own -- the tick still decides WHAT happened, so a radio without a control
	// socket is not silently dropped from the log -- they only record WHEN, for
	// the tick to stamp its own event with. See noteAssoc and addAt.
	if rest, ok := strings.CutPrefix(msg, "AP-STA-CONNECTED "); ok {
		e.noteAssoc(iface, firstField(rest), true)
		return
	}
	if rest, ok := strings.CutPrefix(msg, "AP-STA-DISCONNECTED "); ok {
		e.noteAssoc(iface, firstField(rest), false)
		return
	}

	// A client's answer to a beacon request: what IT heard, for a BSS it is not
	// associated to. See beaconreport.go and #228.
	//
	//	BEACON-RESP-RX <mac> <token> <rep_mode> <measurement report hexdump>
	//
	// rep_mode is non-zero when the client REFUSED or could not run the
	// measurement, and carries no report body with it -- a refusal logged as a
	// missing report would read as a parse failure, so the two are separated
	// here rather than at the point they are printed.
	if rest, ok := strings.CutPrefix(msg, "BEACON-RESP-RX "); ok {
		e.handleBeaconResp(rest)
		return
	}

	// A raw management frame, from notify_mgmt_frames=1. See mgmtframe.go.
	//
	//	AP-MGMT-FRAME-RECEIVED buf=<hex>
	if rest, ok := strings.CutPrefix(msg, "AP-MGMT-FRAME-RECEIVED buf="); ok {
		e.handleMgmtFrame(iface, rest)
		return
	}

	rest, ok := strings.CutPrefix(msg, "BSS-TM-RESP ")
	if !ok {
		return
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return
	}
	mac := normMAC(fields[0])
	status, target := -1, ""
	for _, f := range fields[1:] {
		k, v, found := strings.Cut(f, "=")
		if !found {
			continue
		}
		switch k {
		case "status_code":
			status, _ = strconv.Atoi(v)
		case "target_bssid":
			target = v
		}
	}
	if status < 0 {
		return
	}

	p, had := e.takePendingSteer(mac)
	to := p.to
	// An answer with no request behind it is still worth logging: it means
	// something else on the box steered this client, and a log that dropped it
	// would be quietly incomplete.
	if !had {
		to = "another radio"
	}
	label := e.labelFor(mac)
	if status == 0 {
		e.logEvent(EventAction, iface, mac,
			"%s accepted the request to move to %s%s",
			label, to, targetNote(target))
		return
	}
	// The candidate list the client sent with its refusal, recovered from the
	// raw frame a moment ago. Without it this line says a device refused and
	// stops there, which is the question restated rather than an answer.
	e.logEvent(EventAction, iface, mac,
		"%s %s (asked to move to %s)%s",
		label, btmStatus(status), to, e.takeBTMCandidates(mac))
}

// noteBTMCandidates parks the candidate list from a transition refusal so the
// BSS-TM-RESP line can carry it.
//
// No race and no expiry, because there is no window: hostapd emits the raw
// frame and then its own BSS-TM-RESP from the same receive path, both on this
// radio's control socket, and readHostapdEvents processes that socket on one
// goroutine in order. The stash is written and read microseconds apart by the
// same goroutine. It is a map rather than a field only because two radios have
// two goroutines, and the mutex is for that.
//
// A list left behind -- a refusal hostapd did not follow with an event -- is
// overwritten by the next one for that client rather than accumulating.
func (e *Engine) noteBTMCandidates(mac string, cands []neighborCandidate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.btmCandidates == nil {
		e.btmCandidates = map[string][]neighborCandidate{}
	}
	e.btmCandidates[mac] = cands
}

// takeBTMCandidates renders and clears the parked candidate list, or returns
// the empty string when the client named none.
func (e *Engine) takeBTMCandidates(mac string) string {
	e.mu.Lock()
	cands := e.btmCandidates[mac]
	delete(e.btmCandidates, mac)
	e.mu.Unlock()
	if len(cands) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		// The radio is named where the candidate is one of ours, because
		// "wlan0" means something to the reader and a BSSID does not. A
		// candidate that is NOT ours is kept and shown as an address: a client
		// asking to leave for a neighbour's network is telling us something
		// louder than one asking for the other band.
		//
		// describeRadio already carries the band and channel, so the channel is
		// added only for an address it could not name -- otherwise the line
		// reads "wlan0 (2.4GHz ch 6) (channel 6)".
		part := e.radioByBSSID(c.BSSID)
		if part == "" {
			part = fmt.Sprintf("%s (channel %d)", c.BSSID, c.Channel)
		}
		if c.Preference >= 0 {
			part += fmt.Sprintf(", preference %d", c.Preference)
		}
		parts = append(parts, part)
	}
	// "named" rather than "asked for ... instead": the status this is appended
	// to already ends in "offering its own list of candidates instead", and the
	// line should not say instead twice.
	return " — it named " + strings.Join(parts, ", ")
}

// verbose reports whether the activity log is currently carrying the received
// management frames that are context rather than events.
//
// A live value, not the flag it started as. The moment anyone wants this is in
// the middle of watching something, and a setting that needs a daemon restart
// to change is one that gets turned on for the run that has already finished.
func (e *Engine) verbose() bool { return e.verboseOn.Load() }

// SetVerbose is the API's way in. See setVerbose.
func (e *Engine) SetVerbose(on bool) { e.setVerbose(on) }

// setVerbose turns that on or off, and says so in the log it is about.
//
// The log records the change in itself deliberately: turning verbose off makes
// lines stop appearing, which is indistinguishable from a box that went quiet
// unless something says which happened.
func (e *Engine) setVerbose(on bool) {
	if e.verboseOn.Swap(on) == on {
		return
	}
	if on {
		e.logEvent(EventAction, "", "",
			"activity log is now also showing what clients say when they "+
				"associate — capabilities, and frame types nothing acts on")
		return
	}
	e.logEvent(EventAction, "", "",
		"activity log is back to events only — refusals and disconnect "+
			"reasons are still shown")
}

// handleMgmtFrame decodes one raw received management frame and says what is
// worth saying about it.
//
// Two thresholds, deliberately different. The candidate list and a client's own
// reason for leaving are always taken: both are rare, both answer a question
// the interface currently cannot, and neither can be recovered later. Everything
// else -- capability elements on every join, subtypes nothing acts on -- is
// verbose only, because it repeats on every association and would bury the
// events that matter in the same view.
func (e *Engine) handleMgmtFrame(iface, dump string) {
	f, err := parseMgmtFrame(dump)
	if err != nil {
		// Once, and only when asked. A client sending malformed management
		// frames is a real finding, but it is also a thing that repeats every
		// few seconds, and this must not become the loudest voice in the log.
		if e.verbose() {
			e.logEvent(EventWarning, iface, "",
				"a management frame could not be read: %v", err)
		}
		return
	}
	mac := normMAC(f.Src)
	label := e.labelFor(mac)

	switch {
	case f.IsBTMResponse:
		// Parked rather than logged: the BSS-TM-RESP event that follows this
		// frame carries the status code, and one line saying both is worth
		// more than two saying half each.
		if len(f.Candidates) > 0 {
			e.noteBTMCandidates(mac, f.Candidates)
		}

	case f.Subtype == subtypeDeauth || f.Subtype == subtypeDisassoc:
		// The client's OWN disconnect, not ours. This is the only thing that
		// tells a device that roamed away from one that timed out from one
		// that gave up on us, all of which vanish from the station table
		// identically.
		e.logEvent(EventLeave, iface, mac,
			"%s %s itself from %s: %s",
			label, map[bool]string{true: "deauthenticated", false: "disassociated"}[f.Subtype == subtypeDeauth],
			e.describeRadio(iface), disconnectReason(f.Reason))

	case e.verbose() && (f.Subtype == subtypeAssocReq || f.Subtype == subtypeReassocReq):
		var extra string
		if f.RMCapabilitiesSet {
			extra = fmt.Sprintf(", 802.11k: %s", rmCapabilitySummary(f.RMCapabilities))
		} else {
			extra = ", claiming no 802.11k"
		}
		if f.CurrentAP != "" {
			from := e.radioByBSSID(f.CurrentAP)
			if from == "" {
				from = f.CurrentAP
			}
			extra = fmt.Sprintf(", coming from %s%s", from, extra)
		}
		e.logEvent(EventJoin, iface, mac, "%s sent %s%s",
			label, withArticle(subtypeName(f.Subtype)), extra)

	case e.verbose():
		e.logEvent(EventAction, iface, mac, "%s sent %s",
			label, withArticle(subtypeName(f.Subtype)))
	}
}

// radioByBSSID names one of our own radios by the address a client used for it,
// or "" when the address is not ours.
//
// Asked of the kernel rather than of hostapd: an AP interface's BSSID is its
// hardware address, and net.InterfaceByName is one syscall against a control
// round-trip per radio. The alternative matters because this runs while
// rendering a log line, on the goroutine that is also reading the socket.
//
// A candidate that is not ours is not an error and must not be turned into one
// -- a client asking to leave for a neighbour's network is a louder finding
// than one asking for the other band, and the caller shows the raw address.
func (e *Engine) radioByBSSID(bssid string) string {
	want := normMAC(bssid)
	if want == "" {
		return ""
	}
	for _, iface := range e.cfg.WlanPorts {
		ni, err := net.InterfaceByName(iface)
		if err != nil || ni.HardwareAddr == nil {
			continue
		}
		if normMAC(ni.HardwareAddr.String()) == want {
			return e.describeRadio(iface)
		}
	}
	return ""
}

func targetNote(bssid string) string {
	if bssid == "" {
		return ""
	}
	return " (" + bssid + ")"
}

// firstField is the MAC out of the remainder of an AP-STA-* line, which may
// carry more words after it depending on hostapd's build.
func firstField(rest string) string {
	if f := strings.Fields(rest); len(f) > 0 {
		return f[0]
	}
	return ""
}

// assocFresh is how long an observation may be used to stamp an event.
//
// Comfortably more than a tick, so a transition seen just after one poll is
// still available to the next, and short enough that a stale record cannot
// backdate an unrelated event later on. A client that joins, leaves and rejoins
// inside this window has its most recent transition used, which is the one the
// tick is about to raise.
const assocFresh = 5 * time.Second

// assocObs is one association transition as hostapd reported it, in real time.
type assocObs struct {
	iface     string
	at        time.Time
	connected bool
}

// noteAssoc records that hostapd saw a client associate or disassociate, with
// the time it said so.
//
// Deliberately NOT an event. hostapd only speaks for radios it is serving, and
// this box can run one radio under hostapd and another under NetworkManager --
// see anyLinkControl. Raising the log line from here would make a client on the
// second radio invisible, which is a worse failure than a coarse timestamp. So
// the tick remains the single thing that decides an event happened, and this
// only sharpens when it says it did.
func (e *Engine) noteAssoc(iface, mac string, connected bool) {
	mac = normMAC(mac)
	if !validMAC(mac) {
		return
	}
	// A gather's deny list exists to constrain one re-association. This is that
	// re-association, so the ban has done its job and can go now rather than
	// sitting out a timeout.
	//
	// Registered BEFORE the unlock below, which means it runs AFTER it: deferred
	// calls run last-in-first-out, so a defer placed after `defer e.mu.Unlock()`
	// would run while the lock is still held -- and liftPinOnArrival takes the
	// same non-reentrant mutex. That was a deadlock, and it presented as the
	// whole test binary hanging until Go's ten-minute timeout killed it rather
	// than as anything pointing at this line.
	if connected {
		defer e.liftPinOnArrival(iface, mac)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.assocSeen == nil {
		e.assocSeen = map[string]assocObs{}
	}
	e.assocSeen[mac] = assocObs{iface: iface, at: time.Now(), connected: connected}

	// A DURABLE record of the departure, which assocSeen cannot be: assocTime
	// consumes that one so a single transition stamps a single event, and the
	// tick needs to keep asking "has this client left?" for as long as it is
	// deciding whether to list it. Cleared on the way back in, so a client that
	// returns is not held out by a departure it has already reversed.
	if e.assocGone == nil {
		e.assocGone = map[string]time.Time{}
	}
	if connected {
		delete(e.assocGone, mac)
	} else {
		e.assocGone[mac] = time.Now()
	}
}

// assocTime is when hostapd saw this client's most recent transition of the
// kind being reported, or now if it did not see one.
//
// The record is consumed, so a single transition stamps a single event: if the
// same observation stamped a join and then a later leave, the second would
// carry a time it has no claim to.
func (e *Engine) assocTime(mac string, connected bool) time.Time {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	obs, ok := e.assocSeen[mac]
	if !ok || obs.connected != connected || now.Sub(obs.at) > assocFresh {
		return now
	}
	delete(e.assocSeen, mac)
	return obs.at
}

// handleBeaconResp decodes one BEACON-RESP-RX payload: everything after the
// event name.
func (e *Engine) handleBeaconResp(rest string) {
	fields := strings.Fields(rest)
	// mac, token, rep_mode, report -- the report is absent on a refusal.
	if len(fields) < 3 {
		return
	}
	mac := normMAC(fields[0])
	if !validMAC(mac) {
		return
	}
	// rep_mode is a bitmask: late, incapable, refused. Any bit set means there
	// is no measurement to read, and saying WHICH is the difference between "it
	// cannot do this" and "it would not do this now".
	repMode, err := strconv.ParseUint(fields[2], 16, 8)
	if err != nil {
		return
	}
	if repMode != 0 {
		e.logEvent(EventWarning, e.radioFor(mac), mac,
			"%s declined to measure the other radios: %s",
			e.labelFor(mac), beaconRefusal(uint8(repMode)))
		return
	}
	// Accepted, and answered with NOTHING.
	//
	// Not a malformed response and not a refusal: the client ran the
	// measurement and found no BSS to report. In beacon-table mode -- the only
	// mode Apple devices here accept -- that means the BSSID was not in its
	// scan cache, so it never looked rather than looked and heard nothing.
	//
	// MEASURED 2026-09-06: an iPhone answered exactly this for both other
	// radios on the box. Logged rather than dropped, because an empty answer is
	// the outcome and a silent return leaves the operator watching a button
	// that appears to do nothing at all.
	if len(fields) < 4 || strings.TrimSpace(fields[3]) == "" {
		e.logEvent(EventWarning, e.radioFor(mac), mac,
			"%s accepted the measurement but reported no BSS — in scan-cache "+
				"mode that means the radio was not in its cache, so it never "+
				"went and listened", e.labelFor(mac))
		return
	}
	br, err := parseBeaconReport(fields[3])
	if err != nil {
		e.logEvent(EventWarning, e.radioFor(mac), mac,
			"%s answered a beacon request but the report could not be read: %v",
			e.labelFor(mac), err)
		return
	}
	e.noteBeaconReport(mac, br)
}

// beaconRefusal renders the Measurement Report Mode bits in words.
//
// In words for the same reason btmStatus is: a bare "rep_mode=2" in an activity
// log is a value the reader has to go and look up, which in practice means it
// does not get read. The bits are from IEEE 802.11 Figure 9-198.
func beaconRefusal(mode uint8) string {
	var why []string
	if mode&0x01 != 0 {
		why = append(why, "it was too late to start the measurement")
	}
	if mode&0x02 != 0 {
		why = append(why, "it is not capable of this measurement")
	}
	if mode&0x04 != 0 {
		why = append(why, "it refused")
	}
	if len(why) == 0 {
		return fmt.Sprintf("report mode 0x%02x", mode)
	}
	return strings.Join(why, "; ")
}
