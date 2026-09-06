package boa

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

/*
 * 802.11k beacon requests: asking a client what IT can see.
 *
 * The box knows the signal of a client on the radio it is associated to, and
 * nothing at all about the radios it is not -- which is every destination a
 * steer chooses between. That gap is why a refusal could be reported but never
 * explained: the client said "not that one, here is my own candidate list"
 * (BSS-TM-RESP status_code=6) and the list itself never reached us, because
 * hostapd's event for a code-6 response carries no target_bssid.
 *
 * A beacon request closes it from the other end. The AP asks the client to tune
 * to a named channel, measure a named BSSID, and report what it heard. The
 * answer is the client's own number for a radio it is not using, which is
 * exactly the measurement a steering decision needs and the only way to get it.
 *
 * See #228. Deliberately NOT wired into any automatic decision: this reports
 * what the client sees, and choosing a destination from it stays the operator's
 * call -- a control whose destination moves with transient state is one you
 * cannot run the same test against twice.
 */

// BeaconReport is one client's own measurement of one BSS.
type BeaconReport struct {
	// BSSID measured, and the interface of ours that owns it when it is ours.
	// A client may report a neighbour's BSS too; naming ours is what makes the
	// number comparable to the radio row beside it.
	BSSID string `json:"bssid"`
	Iface string `json:"iface,omitempty"`
	// Channel the measurement was taken on, as the CLIENT reports it rather
	// than as we asked -- a client that measured somewhere else has told us
	// something worth seeing rather than something to normalise away.
	Channel int `json:"channel"`
	// RCPI as received, and the dBm it converts to. Both, because the
	// conversion is lossy in the sense that matters: 255 means "not available"
	// and would otherwise arrive as a confident 17.5 dBm.
	RCPI      int  `json:"rcpi"`
	SignalDBm int  `json:"signal_dbm"`
	HasSignal bool `json:"has_signal"`
	// RSNI in the same units the standard uses: 0.5 dB steps from -10 dB.
	RSNIdB    float64 `json:"rsni_db,omitempty"`
	HasRSNI   bool    `json:"has_rsni,omitempty"`
	AtMs      int64   `json:"at_ms"`
	Requested bool    `json:"requested"`
}

// beaconReportLen is the fixed part of a Measurement Report body for a beacon
// report: operating class, channel, 8-byte start time, 2-byte duration, frame
// info, RCPI, RSNI, BSSID, antenna id and a 4-byte parent TSF.
const beaconReportLen = 26

// Field offsets within that body. Named rather than inlined because every one
// of them is a place a wrong constant produces a plausible number instead of an
// error -- an RCPI read one byte early is an RSNI, and both are small integers.
//
// Only the fields actually read are named. The report also carries an operating
// class at 0, an 8-byte measurement start time at 2, a duration at 10, frame
// info at 12, an antenna id at 21 and a parent TSF at 22; naming those without
// reading them would make this look like a full decoder, which it is not.
const (
	brOffChannel = 1
	brOffRCPI    = 13
	brOffRSNI    = 14
	brOffBSSID   = 15
)

// rcpiToDBm converts RCPI to dBm. RCPI counts 0.5 dB steps from -110 dBm, so
// the useful range 0..220 spans -110..0 dBm; anything above is reserved.
func rcpiToDBm(rcpi int) (dbm int, ok bool) {
	if rcpi < 0 || rcpi > 220 {
		return 0, false
	}
	return rcpi/2 - 110, true
}

// rsniToDB converts RSNI to dB. RSNI counts 0.5 dB steps from -10 dB; 255 means
// not available.
func rsniToDB(rsni int) (float64, bool) {
	if rsni < 0 || rsni >= 255 {
		return 0, false
	}
	return float64(rsni)/2 - 10, true
}

// opClass20 is the global operating class for a channel measured at 20MHz.
//
// Its own function rather than opClassAndPhy(ch, 20), for two reasons. A
// measurement request wants the client to tune somewhere and listen, so the
// narrow class is the right description whatever width the AP is actually
// using -- naming an 80MHz class asks for a wider measurement than we need.
// And opClassAndPhy has a gap here: at 20MHz it falls through to 115 for
// everything below 149, so channels 52-144 come back as class 115, which does
// not contain them. That gap is invisible today because the planner only ever
// picks 36 and 149, both non-DFS, and it would not stay invisible here.
func opClass20(channel int) int {
	switch {
	case channel >= 1 && channel <= 13:
		return 81
	case channel == 14:
		return 82
	case channel >= 36 && channel <= 48:
		return 115
	case channel >= 52 && channel <= 64:
		return 118
	case channel >= 100 && channel <= 144:
		return 121
	case channel >= 149 && channel <= 169:
		return 125
	default:
		return 0
	}
}

// Beacon request measurement modes, and the order they are worth trying in.
//
// A CLIENT DOES NOT HAVE TO SUPPORT ALL THREE, and hostapd refuses to transmit
// a request for one it has not advertised -- MEASURED 2026-09-06, a MacBook on
// this box:
//
//	Beacon request: ea:83:50:19:1f:d1 does not support active beacon report
//
// so a single-mode implementation reports "the client refused" for a client
// that never saw the request. The ladder below asks for the most useful thing
// the device will actually do:
//
//   - ACTIVE probes on the named channel. Most accurate, and it does not depend
//     on when the measurement window happens to fall.
//   - PASSIVE listens instead of probing, so it needs a window long enough to
//     span a beacon -- hence beaconDurationPassiveTU below.
//   - TABLE returns whatever is already in the client's scan cache. Instant,
//     needs no radio time, and may be minutes old, which is why it is last: a
//     stale number an operator acts on is worse than a slow one.
//
// The mode that answered is reported, because "-42 dBm measured just now" and
// "-42 dBm from a cache of unknown age" are not the same claim.
const (
	beaconModePassive = 0
	beaconModeActive  = 1
	beaconModeTable   = 2
)

// beaconModeLadder is the order modes are attempted in, best first.
var beaconModeLadder = []int{beaconModeActive, beaconModePassive, beaconModeTable}

// beaconModeName renders a mode for the activity log, in words rather than as
// the number the standard uses.
func beaconModeName(mode int) string {
	switch mode {
	case beaconModeActive:
		return "probing"
	case beaconModePassive:
		return "listening"
	case beaconModeTable:
		return "from its scan cache"
	default:
		return fmt.Sprintf("mode %d", mode)
	}
}

// How long the client should measure, in TUs.
//
// Active probing needs only long enough to send a probe request and collect the
// response. Passive has to OVERHEAR a beacon, and every radio here beacons
// every 100 TU, so a 100 TU window that starts just after one goes out catches
// nothing -- doubled, so a passive measurement always spans at least one
// beacon whenever it starts. Longer costs the client radio time away from its
// own traffic, which is why it is not longer still.
const (
	beaconDurationTU        = 100
	beaconDurationPassiveTU = 200
)

// beaconDurationFor is the window a mode needs. Table mode reads a cache and
// measures nothing, so its duration is ignored by the client either way.
func beaconDurationFor(mode int) int {
	if mode == beaconModePassive {
		return beaconDurationPassiveTU
	}
	return beaconDurationTU
}

// beaconRequestHex builds the Measurement Request body hostapd's REQ_BEACON
// expects: a hexdump starting at the operating class.
//
// The layout is fixed and unforgiving -- operating class, channel,
// randomisation interval, duration, mode, BSSID -- and hostapd rejects the
// whole command rather than a field, so this is pure and separately tested.
//
// No subelements. The default Reporting Detail asks for more of the beacon than
// we read, which costs a longer response and nothing else, whereas an explicit
// Reporting Detail of 0 is described in the standard as "no fixed-length fields
// or elements" and is implemented inconsistently enough across client firmware
// that it risks a report with no RCPI in it. A bigger answer we ignore beats a
// smaller one that might be empty.
func beaconRequestHex(opClass, channel, durationTU, mode int, bssid string) (string, error) {
	if opClass == 0 {
		return "", fmt.Errorf("no 20MHz operating class contains channel %d", channel)
	}
	if channel <= 0 || channel > 255 {
		return "", fmt.Errorf("channel %d is not one a measurement can name", channel)
	}
	mac, err := macBytes(bssid)
	if err != nil {
		return "", err
	}
	body := []byte{
		byte(opClass),
		byte(channel),
		0x00, 0x00, // randomisation interval: start now
		byte(durationTU & 0xff), byte(durationTU >> 8), // little-endian TUs
		byte(mode),
	}
	body = append(body, mac...)
	return hex.EncodeToString(body), nil
}

// macBytes parses a colon-separated MAC into its six octets.
func macBytes(mac string) ([]byte, error) {
	parts := strings.Split(strings.TrimSpace(mac), ":")
	if len(parts) != 6 {
		return nil, fmt.Errorf("not a MAC address: %q", mac)
	}
	out := make([]byte, 6)
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("not a MAC address: %q", mac)
		}
		out[i] = byte(v)
	}
	return out, nil
}

// parseBeaconReport decodes the Measurement Report body from a BEACON-RESP-RX
// event. Trailing subelements are ignored; only the fixed fields are read.
func parseBeaconReport(dump string) (BeaconReport, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(dump))
	if err != nil {
		return BeaconReport{}, fmt.Errorf("beacon report is not hex: %w", err)
	}
	if len(raw) < beaconReportLen {
		return BeaconReport{}, fmt.Errorf(
			"beacon report is %d bytes, need at least %d", len(raw), beaconReportLen)
	}
	br := BeaconReport{
		Channel: int(raw[brOffChannel]),
		RCPI:    int(raw[brOffRCPI]),
		BSSID:   formatMAC(raw[brOffBSSID : brOffBSSID+6]),
		AtMs:    time.Now().UnixMilli(),
	}
	if dbm, ok := rcpiToDBm(br.RCPI); ok {
		br.SignalDBm, br.HasSignal = dbm, true
	}
	if db, ok := rsniToDB(int(raw[brOffRSNI])); ok {
		br.RSNIdB, br.HasRSNI = db, true
	}
	return br, nil
}

// formatMAC renders six octets the way every other MAC in this codebase is
// rendered, so a beacon report's BSSID compares equal to a radio's.
func formatMAC(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02x", v)
	}
	return strings.Join(parts, ":")
}

// --- storage ---------------------------------------------------------------

// beaconStore holds the most recent report per (client, BSSID).
//
// Most recent rather than a history: the question is "what does this device see
// right now", and a list of past measurements answers a different one while
// making the current answer harder to find. Age is carried on the report so a
// stale number is visibly stale rather than silently old.
type beaconStore struct {
	mu sync.Mutex
	by map[string]map[string]BeaconReport // mac -> bssid -> report
}

func (s *beaconStore) put(mac string, br BeaconReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]map[string]BeaconReport{}
	}
	m := s.by[mac]
	if m == nil {
		m = map[string]BeaconReport{}
		s.by[mac] = m
	}
	m[br.BSSID] = br
}

// get returns a client's reports, newest measurement per BSS, sorted by signal
// so the strongest is first -- which is the order the question is asked in.
func (s *beaconStore) get(mac string) []BeaconReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.by[mac]
	if len(m) == 0 {
		return nil
	}
	out := make([]BeaconReport, 0, len(m))
	for _, br := range m {
		out = append(out, br)
	}
	// Strongest first, and a report with no usable signal last rather than
	// sorted as if its zero were a measurement.
	sortBeaconReports(out)
	return out
}

// forget drops everything known about a client, for a device that has left.
func (s *beaconStore) forget(mac string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.by, mac)
}

func sortBeaconReports(rs []BeaconReport) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && beaconLess(rs[j], rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func beaconLess(a, b BeaconReport) bool {
	if a.HasSignal != b.HasSignal {
		return a.HasSignal // a measurement beats "could not measure"
	}
	if a.SignalDBm != b.SignalDBm {
		return a.SignalDBm > b.SignalDBm // stronger first
	}
	return a.BSSID < b.BSSID
}

// --- asking ----------------------------------------------------------------

// beaconTarget is one radio a client can be asked to measure.
type beaconTarget struct {
	iface   string
	bssid   string
	channel int
}

// beaconTargets lists the OTHER serving radios, with the BSSID and channel each
// one is actually using.
//
// Asked of hostapd rather than assumed, for the same reason SteerClient asks: a
// measurement request naming the wrong BSSID is one the client answers with
// "not available", which is indistinguishable from a client that refused.
//
// The radio the client is ON is excluded. Its signal is already known from the
// station dump, directly and without asking the client for a favour, and
// including it would invite the reader to compare a measurement the AP made
// with one the client made -- two different quantities at two ends of the link.
func (e *Engine) beaconTargets(exclude string) []beaconTarget {
	var out []beaconTarget
	for _, w := range e.cfg.WlanPorts {
		if w == exclude || !hostapdAvailable(w) {
			continue
		}
		st, err := hostapdCmd(w, "STATUS")
		if err != nil {
			continue
		}
		kv := parseHostapdKV(st)
		bssid, ch := kv["bssid[0]"], atoiSafe(kv["channel"])
		if bssid == "" || ch == 0 || kv["state"] != "ENABLED" {
			continue
		}
		out = append(out, beaconTarget{iface: w, bssid: bssid, channel: ch})
	}
	return out
}

// RequestBeaconReports asks one client to measure every other serving radio and
// report what it hears. Returns how many measurements were requested.
//
// Requested, not received: the answers arrive asynchronously through the
// monitor connection, exactly as a BSS-TM-RESP does, and a client is entirely
// free to ignore the request. That is the same shape as a steer and for the
// same reason -- 802.11k is a request, and whether a device honours one is
// itself worth knowing.
func (e *Engine) RequestBeaconReports(mac string) (int, error) {
	m := normMAC(mac)
	if !validMAC(m) {
		return 0, fmt.Errorf("not a MAC address: %s", mac)
	}
	on := e.radioFor(m)
	if on == "" {
		return 0, fmt.Errorf(
			"%s is not associated to any radio, so there is nothing to ask "+
				"through -- a beacon request reaches a client through the access "+
				"point it is already on", e.labelFor(m))
	}
	if e.cfg.Demo {
		return 0, nil
	}
	targets := e.beaconTargets(on)
	if len(targets) == 0 {
		return 0, fmt.Errorf(
			"no other radio is serving, so there is nothing for %s to measure",
			e.labelFor(m))
	}
	asked := 0
	used := map[int]bool{}
	var firstErr error
	for _, t := range targets {
		mode, err := e.askBeacon(on, m, t)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		used[mode] = true
		asked++
	}
	if asked == 0 {
		if firstErr != nil {
			return 0, firstErr
		}
		return 0, fmt.Errorf("no beacon request could be sent to %s", e.labelFor(m))
	}
	var how []string
	for _, mode := range beaconModeLadder {
		if used[mode] {
			how = append(how, beaconModeName(mode))
		}
	}
	e.logEvent(EventAction, on, m,
		"%s asked to measure %d other radio(s) and report what it can hear "+
			"(802.11k, %s). It may still decline.",
		e.labelFor(m), asked, strings.Join(how, " / "))
	return asked, firstErr
}

// askBeacon sends one request for one target, walking the mode ladder until the
// client's own capabilities accept one. Returns the mode that was accepted.
//
// hostapd checks the STA's advertised RRM capabilities and answers FAIL without
// transmitting, so a rejection here is cheap and says something real about the
// device rather than about the radio.
func (e *Engine) askBeacon(on, mac string, t beaconTarget) (int, error) {
	var lastErr error
	for _, mode := range beaconModeLadder {
		hexReq, err := beaconRequestHex(opClass20(t.channel), t.channel,
			beaconDurationFor(mode), mode, t.bssid)
		if err != nil {
			// A bad channel or BSSID is not something another mode fixes.
			return 0, err
		}
		reply, err := hostapdCmd(on, "REQ_BEACON "+mac+" "+hexReq)
		if err != nil {
			return 0, err
		}
		if !strings.HasPrefix(strings.TrimSpace(reply), "FAIL") {
			return mode, nil
		}
		lastErr = fmt.Errorf(
			"%s would not accept a beacon request for %s in any mode "+
				"(tried %s). The device advertises no beacon-report capability "+
				"this box can use, which is a finding about the device rather "+
				"than a fault here",
			e.labelFor(mac), t.iface, beaconModeName(mode))
	}
	return 0, lastErr
}

// noteBeaconReport files one answer and says so in the activity log.
//
// The BSSID is matched back to one of our radios so the number can be read
// beside that radio's row. A report for a BSS that is not ours is kept rather
// than dropped -- a client naming a neighbour's access point is telling us what
// it would rather be on, which is the same question this feature exists to ask.
func (e *Engine) noteBeaconReport(mac string, br BeaconReport) {
	for _, w := range e.cfg.WlanPorts {
		if strings.EqualFold(radioBSSID(w), br.BSSID) {
			br.Iface = w
			break
		}
	}
	br.Requested = true
	e.beacons.put(mac, br)

	where := br.Iface
	if where == "" {
		where = br.BSSID + " (not one of ours)"
	}
	if !br.HasSignal {
		e.logEvent(EventWarning, br.Iface, mac,
			"%s measured %s and could not report a signal for it (RCPI %d)",
			e.labelFor(mac), where, br.RCPI)
		return
	}
	e.logEvent(EventAction, br.Iface, mac,
		"%s hears %s at %d dBm on channel %d — its own measurement, not ours",
		e.labelFor(mac), where, br.SignalDBm, br.Channel)
}

// BeaconReportsFor returns what a client last reported, strongest first.
func (e *Engine) BeaconReportsFor(mac string) []BeaconReport {
	return e.beacons.get(normMAC(mac))
}

// radioBSSID is a radio's own BSSID, for matching a report back to it.
func radioBSSID(iface string) string {
	if !hostapdAvailable(iface) {
		return ""
	}
	st, err := hostapdCmd(iface, "STATUS")
	if err != nil {
		return ""
	}
	return parseHostapdKV(st)["bssid[0]"]
}
