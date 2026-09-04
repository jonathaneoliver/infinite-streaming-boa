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
// A BSS Transition Management Response is sent immediately on receipt -- it
// carries no scanning, only a decision -- so a client that has not answered in
// this long is not thinking about it. Generous rather than tight, because the
// cost of waiting is a later log line and the cost of being early is calling a
// slow client mute.
const btmWait = 5 * time.Second

// pendingSteer is a request whose answer has not arrived yet.
type pendingSteer struct {
	iface string
	to    string
	at    time.Time
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
func (e *Engine) notePendingSteer(mac, fromIface, toIface string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingSteers == nil {
		e.pendingSteers = map[string]pendingSteer{}
	}
	e.pendingSteers[mac] = pendingSteer{iface: fromIface, to: toIface, at: time.Now()}
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
	e.logEvent(EventAction, iface, mac,
		"%s %s (asked to move to %s)", label, btmStatus(status), to)
}

func targetNote(bssid string) string {
	if bssid == "" {
		return ""
	}
	return " (" + bssid + ")"
}
