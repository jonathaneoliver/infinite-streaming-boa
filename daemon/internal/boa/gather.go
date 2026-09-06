package boa

import (
	"fmt"
	"log"
	"sync"
	"time"
)

/*
 * Pinned gather and evict: making the movement controls mean what they say.
 *
 * A steer cannot place a client. 802.11v hands the decision to the device, and
 * a device that refuses -- or one that is disassociated anyway and rescans --
 * picks for itself. Measured repeatedly on 2026-09-06: "gather to wlan-usb"
 * ending with the device on wlan0, and an evict off wlan0 ending with one
 * client on the named radio and the other somewhere else entirely.
 *
 * There is no request that fixes this, because there is no request in 802.11
 * that places a station on a BSS. So this does not ask. It REMOVES THE
 * ALTERNATIVES, through the runtime deny ACL:
 *
 *   - GATHER denies every serving radio EXCEPT the destination. The client's
 *     own rescan finds exactly one access point here it may join.
 *   - EVICT denies only the radio being emptied. The client keeps every choice
 *     except coming back, which is what "get off this radio" means.
 *
 * That is how band steering works on real controllers, and it is the only
 * deterministic answer available. The cost is honesty about what it is: neither
 * is a measurement of whether the device honours a transition request any more
 * -- it cannot refuse what it was never asked. The plain steer button still
 * exists for that question and remains the right control for it.
 *
 * TIMED, always. A deny list is a real outage for that client on those radios,
 * and a forgotten one is indistinguishable from a broken device. Daemon startup
 * clears every runtime deny entry (clearDenyACL), so a crash mid-operation
 * cannot strand anybody.
 *
 * DELIBERATELY SEPARATE FROM DEADZONES, which use the same hostapd deny lists
 * for a different purpose and on a different clock. A deadzone is an outage the
 * operator asked for and may be minutes long; a pin is a few seconds of
 * plumbing. Sharing the registry made a gather silently END a running deadzone:
 * noteDeadzone is keyed by MAC, so the pin overwrote the deadzone's record, and
 * the pin's lift then deleted the deadzone's own ACL entry seconds into a
 * sixty-second outage -- with the interface still showing the deadzone as in
 * force.
 *
 * So pins keep their own registry, and a client already held by a deadzone is
 * SKIPPED rather than moved: cancelling a deliberate outage to satisfy a gather
 * is not a trade this box gets to make on the operator's behalf.
 */

// How long the deny lists are held when a client does not turn up.
//
// gatherPinSec is a POLL, not a deadline: every one of these the operation asks
// whether it can stop. gatherPinMaxSec is the deadline, and exists only so
// nothing is stranded (#205) rather than as a routine exit.
//
// FIVE SECONDS WAS A GUESS, AND IT WAS WRONG. It was chosen to limit how much
// of Apple's association backoff the box provokes, without measuring what a
// re-association actually costs. Measured on 2026-09-06 across a reflash, from
// the deauth to AP-STA-CONNECTED:
//
//	MacBook   270ms   (straight back onto the radio it had just left)
//	iPhone     41s    (same box, same evening)
//
// Two orders of magnitude apart, so no single number covers both. A 5s deadline
// caught the MacBook mid-scan: the bans came off at 18:09:39.129 and it joined
// wlan-usb2 -- a radio it had been denied -- at 18:09:39.395, 266ms later. It
// was about to land on the right one.
//
// TEN, and no cleverness around it.
//
// A conditional hold was tried -- keep waiting while a pending client is
// associated to nothing, on the reasoning that off the air means still
// choosing. It failed on its first run, releasing the bans 650ms before the
// MacBook associated, because the signal it asked was not trustworthy:
// radioFor falls back to scanning hostapd's station tables, and hostapd still
// lists a station on a radio it has LEFT. A client mid-rescan therefore reads
// as associated, and the hold gave up at precisely the wrong moment.
//
// The fast path was never this number. Every client landing lifts the bans at
// once, which is the common case and costs nothing; this only governs how long
// a straggler is given. Ten comfortably covers the MacBook's measured 5.6s
// while holding a deny entry for a fraction of the time an outlier would need
// -- the 41s iPhone will still escape it, and the log says so when it does.
const gatherPinSec = 10

// pinOp is ONE movement command, covering every client it affects.
//
// One operation rather than one ban per client, because a gather is not done
// until ALL of its clients have moved. Lifting each client's ban the moment
// that client arrived freed it to roam straight back into the radios the
// operation was still denying to everybody else -- so a two-client gather could
// end with one client where it was asked and the other back where it started,
// let out by its own success.
//
// Two shapes, one mechanism:
//
//   - GATHER denies every radio but one, and `to` names it. Arriving there is
//     what each ban was for.
//   - EVICT denies only the radio being emptied, and `to` is empty. Arriving
//     anywhere except that radio is what each ban was for.
type pinOp struct {
	// to is the one radio clients are allowed onto, or empty for an evict,
	// where the ban says where they may NOT go rather than where they must.
	to   string
	deny []string
	// pending is the clients that have not landed yet. The operation ends when
	// this empties, or when the timeout fires, whichever comes first.
	pending map[string]bool
	// once guards the lift: the last arrival and the timeout race to do it, and
	// a double DEL would be answered OK by hostapd while a double log line
	// would tell the operator it happened twice.
	once sync.Once
}

// satisfiedBy reports whether an arrival on iface is the landing this operation
// was waiting for.
func (p *pinOp) satisfiedBy(iface string) bool {
	if p.to != "" {
		return iface == p.to
	}
	// An evict: anywhere that is not the radio they were pushed off.
	for _, w := range p.deny {
		if w == iface {
			return false
		}
	}
	return true
}

// stillDenied reports whether ANY live claim still bars this client from this
// radio.
//
// The deny list is the OR of two independent claims -- a deadzone, which is an
// outage the operator asked for and may run for minutes, and a pin, which is a
// few seconds of plumbing behind a gather or an evict. They write the SAME
// hostapd entries, and DENY_ACL has no notion of who asked for one, so two
// identical entries cannot be told apart once written.
//
// That is why neither side may simply delete what it added. Whichever finishes
// first asks this question and leaves the entry alone when the answer is yes;
// an entry comes out only when NEITHER claim wants it any more. Without that a
// gather silently ended a running deadzone seconds into a sixty-second outage,
// with the interface still showing the deadzone in force -- and equally a
// deadzone expiring mid-gather would have released a client the gather was
// still holding.
//
// The caller drops its OWN claim from the registry before asking, so this sees
// only the other side.
func (e *Engine) stillDenied(mac, iface string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if b := e.deadzones[mac]; b != nil && time.Now().Before(b.until) {
		for _, w := range b.radios {
			if w == iface {
				return true
			}
		}
	}
	if op := e.pins[mac]; op != nil {
		for _, w := range op.deny {
			if w == iface {
				return true
			}
		}
	}
	return false
}

// releaseDeny gives up one claim on (mac, radios), removing an entry only where
// NEITHER claim still wants it.
//
// Asked before deleting, rather than deleted and put back. Both orders end in
// the same place, but remove-then-restore leaves a window -- however short --
// in which a deadzone the operator asked for is not being enforced, and on a
// box whose whole subject is what a client does the instant a door opens, a
// client can associate inside that window. Asking first has no window at all.
//
// The caller drops its OWN claim from the registry before calling, so a radio
// that still answers stillDenied is one the OTHER claim is holding, and the
// entry is left exactly where it is.
func (e *Engine) releaseDeny(mac string, radios []string) {
	for _, w := range radios {
		if e.stillDenied(mac, w) {
			e.logEvent(EventAction, w, mac,
				"%s stays denied on %s: the other deny outlives the one that "+
					"just finished", e.labelFor(mac), w)
			continue
		}
		if err := e.denyACLOn(w, "DEL", mac); err != nil {
			log.Printf("release deny %s on %s: %v", mac, w, err)
		}
	}
}

// notePins registers one operation against every client it covers.
func (e *Engine) notePins(op *pinOp) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pins == nil {
		e.pins = map[string]*pinOp{}
	}
	for mac := range op.pending {
		e.pins[mac] = op
	}
}

// liftPinOnArrival is called when a client associates. It marks that client as
// landed and, once the LAST of them has, ends the operation.
//
// Reading the arrival rather than assuming it is the difference between ending
// an operation that worked and one that leaked: for an evict the arrival is the
// entire result, since nothing chose the destination.
func (e *Engine) liftPinOnArrival(iface, mac string) {
	e.mu.Lock()
	op := e.pins[mac]
	if op == nil || !op.satisfiedBy(iface) {
		e.mu.Unlock()
		return
	}
	delete(op.pending, mac)
	done := len(op.pending) == 0
	e.mu.Unlock()

	if !done {
		// Deliberately still denied. The bans come off together or not at all,
		// or the client that arrived first is free to wander back into the
		// radios everybody else is still being held out of.
		e.logEvent(EventAction, iface, mac,
			"%s landed on %s; the other radios stay denied until the rest arrive",
			e.labelFor(mac), iface)
		return
	}
	e.liftPins(op, "every client landed")
}

// liftPins removes every deny entry the operation placed, and says why it ended.
func (e *Engine) liftPins(op *pinOp, why string) {
	op.once.Do(func() {
		e.mu.Lock()
		var macs []string
		for mac, p := range e.pins {
			if p == op {
				macs = append(macs, mac)
			}
		}
		for _, mac := range macs {
			delete(e.pins, mac)
		}
		e.mu.Unlock()

		// The pin's claim is already dropped from e.pins above, so releaseDeny
		// sees only what a deadzone still owes.
		for _, mac := range macs {
			e.releaseDeny(mac, op.deny)
		}
		where := op.to
		if where == "" {
			where = joinRadios(op.deny)
		}
		e.logEvent(EventAction, where, "",
			"%d client(s) may use every radio again (%s)", len(macs), why)
	})
}

// clearPins ends every operation currently in force.
//
// CALLED BY ANY NEW MOVEMENT COMMAND, before it does anything. Two gathers in a
// row would otherwise lock a client off the box entirely: the first denies it on
// B and C to hold it at A, the second denies it on A and C to hold it at B, and
// the entry on B from the first is still there -- so the client is denied on A,
// B and C at once and can associate nowhere. Its own retry backoff then takes
// over and it leaves the network, which is not a state any button here offers
// and not one the deny lists say out loud.
//
// Observed 2026-09-06: both clients vanished from all three radios after
// repeated gathers, with every deny list looking individually reasonable.
//
// A new command SUPERSEDES the old one rather than combining with it, which is
// also the only reading an operator would expect: pressing gather again means
// "now do this instead", never "do both".
func (e *Engine) clearPins(why string) {
	e.mu.RLock()
	seen := map[*pinOp]bool{}
	var ops []*pinOp
	for _, op := range e.pins {
		if !seen[op] {
			seen[op] = true
			ops = append(ops, op)
		}
	}
	e.mu.RUnlock()
	// Outside the lock: liftPins takes it again, and holding it across a
	// control-socket round trip per radio would stall every reader of the
	// engine for as long as hostapd takes to answer.
	for _, op := range ops {
		e.liftPins(op, why)
	}
}

// GatherTo moves every client on the box's other radios onto iface, and holds
// them there by denying them everywhere else until they have all arrived.
//
// Returns how many clients were moved. Unlike a steer this is not a request, so
// the count is what was ACTED ON rather than what was asked.
func (e *Engine) GatherTo(iface string, durSec float64) (int, error) {
	if !e.LinkControlAvailable() {
		return 0, fmt.Errorf("link control unavailable: hostapd is not serving the AP")
	}
	if durSec < 1 || durSec > 300 {
		return 0, fmt.Errorf("pin duration must be 1-300 seconds")
	}
	if !e.cfg.Demo && !hostapdReachable(iface) {
		return 0, fmt.Errorf(
			"%s is not serving an access point, so there is nothing to gather to", iface)
	}

	deny, err := e.gatherDeny(iface)
	if err != nil {
		return 0, err
	}
	if e.cfg.Demo {
		return 0, nil
	}
	// Whatever the last command decided is no longer the answer. Cleared BEFORE
	// the new bans go on, or the two sets overlap and a client is denied
	// everywhere -- see clearPins.
	e.clearPins("superseded by a new gather")

	// DEDUPED. hostapd can still list a station on a radio it has left, so one
	// client showing on two radios was counted twice, deauthed twice, and
	// reported as two -- "gathering 3 client(s)" on a box with two devices.
	seen := map[string]bool{}
	var move []string
	for _, w := range deny {
		for mac := range StationDump(w) {
			m := normMAC(mac)
			if !seen[m] {
				seen[m] = true
				move = append(move, m)
			}
		}
	}
	if len(move) == 0 {
		return 0, nil
	}
	return e.runPin(&pinOp{to: iface, deny: deny}, move, iface, iface, durSec,
		"gathering %d client(s) onto %s: denied on %s, so a rescan has one access "+
			"point left to choose. The bans lift together once they have all "+
			"arrived, or after %.0fs. Not a request — they cannot refuse what "+
			"they were not asked")
}

// gatherDeny is every OTHER serving radio: the ones a client must be kept off
// for a pin to iface to mean anything.
//
// Strict about a radio that is present but unreachable, for the reason
// deadzoneRadios is: a client could still associate there, so the pin would have
// a hole in it and the gather would silently not be a gather.
func (e *Engine) gatherDeny(iface string) ([]string, error) {
	var deny []string
	for _, w := range e.cfg.WlanPorts {
		if w == iface {
			continue
		}
		switch {
		case hostapdReachable(w):
			deny = append(deny, w)
		case linkPresent(w):
			return nil, fmt.Errorf(
				"cannot pin clients to %s: %s is present but hostapd is not serving "+
					"it, so a client could associate there and the gather would have "+
					"a hole in it", iface, w)
		}
	}
	if len(deny) == 0 {
		return nil, fmt.Errorf(
			"%s is the only radio serving, so there is nothing to gather from", iface)
	}
	return deny, nil
}

/*
 * GatherClientTo pins ONE client to iface. Same mechanism, smaller client set.
 *
 * The reason this exists rather than a per-client transition request: MEASURED
 * 2026-09-06 on this box, an iPhone ignored a same-band request entirely and
 * then REFUSED a cross-band one, offering its own candidate list instead. It was
 * behaving correctly -- the distance model does not move real RSSI, so its 5GHz
 * signal was excellent and it had no reason to go anywhere. A modelled walk
 * therefore cannot reach 2.4GHz by asking, which is the same conclusion the
 * radio-lane controls reached: 802.11 has no request that places a station on a
 * BSS, so a control naming a destination has to remove the alternatives instead.
 *
 * Everything that makes a gather trustworthy comes with it unchanged: the ban is
 * registered before it is applied, lifts on arrival, lifts on a timeout
 * regardless, and supersedes whatever was in force.
 */
func (e *Engine) GatherClientTo(mac, iface string, durSec float64) error {
	if !e.LinkControlAvailable() {
		return fmt.Errorf("link control unavailable: hostapd is not serving the AP")
	}
	m := normMAC(mac)
	if !validMAC(m) {
		return fmt.Errorf("not a MAC address: %s", mac)
	}
	if durSec <= 0 {
		durSec = gatherPinSec
	}
	if durSec < 1 || durSec > 300 {
		return fmt.Errorf("pin duration must be 1-300 seconds")
	}
	if !e.cfg.Demo && !hostapdReachable(iface) {
		return fmt.Errorf(
			"%s is not serving an access point, so there is nothing to pin to", iface)
	}
	deny, err := e.gatherDeny(iface)
	if err != nil {
		return err
	}
	if e.cfg.Demo {
		return nil
	}
	e.clearPins("superseded by a pin")
	_, err = e.runPin(&pinOp{to: iface, deny: deny}, []string{m}, iface, iface, durSec,
		"pinning %d client(s) onto %s: denied on %s, so a rescan has one access "+
			"point left to choose. The ban lifts on arrival, or after %.0fs. "+
			"Not a request — it cannot refuse what it was not asked")
	return err
}

/*
 * EvictClient pushes ONE client off the radio it is on. The mirror of
 * GatherClientTo, and the same pair the radio lane already has.
 *
 * NOT A DUPLICATE OF deadzone WITH ScopeCurrent, though the two look alike and
 * an earlier draft of this dropped it as one. Both deny the client on the radio
 * it is sitting on; what happens next differs, and for a pattern the difference
 * is the whole point:
 *
 *   - a DEADZONE holds the ban for its full duration whatever the client does.
 *     That is what makes it an outage, and why the block on the timeline has a
 *     width you can read.
 *   - an EVICT lifts the moment the client lands somewhere else. It is a move,
 *     not an outage, and its duration is a deadline rather than a dose.
 *
 * So a five-second deadzone costs five seconds of service. A five-second evict
 * usually costs a fraction of one, and only a client that refuses to go
 * anywhere experiences the five.
 *
 * Where it goes is explicitly not this box's decision -- that is what separates
 * an evict from a pin, at either scope.
 */
func (e *Engine) EvictClient(mac string, durSec float64) error {
	if !e.LinkControlAvailable() {
		return fmt.Errorf("link control unavailable: hostapd is not serving the AP")
	}
	m := normMAC(mac)
	if !validMAC(m) {
		return fmt.Errorf("not a MAC address: %s", mac)
	}
	if durSec <= 0 {
		durSec = gatherPinSec
	}
	if durSec < 1 || durSec > 300 {
		return fmt.Errorf("pin duration must be 1-300 seconds")
	}
	from := e.radioFor(m)
	if from == "" {
		return fmt.Errorf("cannot evict %s: it is not on a radio this box serves", m)
	}

	// Somewhere to go, or this is an outage wearing an evict's name.
	var elsewhere []string
	for _, w := range e.cfg.WlanPorts {
		if w != from && hostapdReachable(w) {
			elsewhere = append(elsewhere, w)
		}
	}
	if len(elsewhere) == 0 {
		return fmt.Errorf(
			"%s is the only radio serving, so an evict would put %s off the box "+
				"altogether rather than onto another radio", from, m)
	}
	if e.cfg.Demo {
		return nil
	}
	e.clearPins("superseded by an evict")
	// to is deliberately empty: this ban says where it may NOT go.
	_, err := e.runPin(&pinOp{deny: []string{from}}, []string{m}, from, elsewhere[0], durSec,
		"evicting %d client(s) off %s: denied there so it cannot come back, and "+
			"free to pick any other radio (%s is only the hint in the request). The "+
			"ban lifts once it has landed, or after %.0fs. Where it goes is its own "+
			"choice — that is what an evict is")
	return err
}

// EvictFrom empties one radio and makes the departure stick.
//
// The mirror of GatherTo, built from the same parts. A gather denies every radio
// but the destination, so a client has one legal choice. An evict denies only
// the radio being emptied, so a client has every choice EXCEPT coming back --
// which is exactly what "get off this radio" means, and is why the two need
// different bans rather than different wording.
//
// The hole this closes: an evict was a transition request plus a
// disassociation, and nothing stopped a disassociated client from
// re-associating to the radio it had just been pushed off. It usually left,
// because a client that was asked to move generally does, but "usually" is not
// what the button says. With the source denied, leaving is the only thing
// available.
//
// Where each client goes is still not this box's decision, and the control still
// does not claim otherwise.
func (e *Engine) EvictFrom(iface string, durSec float64) (int, error) {
	if !e.LinkControlAvailable() {
		return 0, fmt.Errorf("link control unavailable: hostapd is not serving the AP")
	}
	if durSec < 1 || durSec > 300 {
		return 0, fmt.Errorf("pin duration must be 1-300 seconds")
	}
	if !e.cfg.Demo && !hostapdReachable(iface) {
		return 0, fmt.Errorf("%s is not serving an access point, so it has nobody to evict", iface)
	}

	// Somewhere for them to GO. Denying the only serving radio would put every
	// client off the box entirely, which is a deadzone for the whole network
	// rather than an evict, and is not what this button offers.
	var elsewhere []string
	for _, w := range e.cfg.WlanPorts {
		if w != iface && hostapdReachable(w) {
			elsewhere = append(elsewhere, w)
		}
	}
	if len(elsewhere) == 0 {
		return 0, fmt.Errorf(
			"%s is the only radio serving, so an evict would put its clients off "+
				"the box altogether rather than onto another radio", iface)
	}
	if e.cfg.Demo {
		return 0, nil
	}
	e.clearPins("superseded by an evict")

	var move []string
	for mac := range StationDump(iface) {
		move = append(move, normMAC(mac))
	}
	if len(move) == 0 {
		return 0, nil
	}
	// to is deliberately empty: this ban says where they may NOT go.
	return e.runPin(&pinOp{deny: []string{iface}}, move, iface, elsewhere[0], durSec,
		"evicting %d client(s) off %s: denied there so they cannot come back, and "+
			"free to pick any other radio (%s is only the hint in the request). The "+
			"bans lift together once they have all landed, or after %.0fs. Where "+
			"each one goes is its own choice — that is what an evict is")
}

// runPin applies one operation: deny, announce, move, and arm the fallback.
//
// ACLs FIRST, for every client, before anything is moved. The other order races:
// a client dropped before the bans land can re-associate to the radio it was
// just removed from, and the operation silently does nothing. LinkDeadzone
// applies them in the same order for the same reason.
func (e *Engine) runPin(
	op *pinOp, macs []string, iface, hint string, durSec float64, format string,
) (int, error) {
	// REGISTERED BEFORE ANYTHING IS APPLIED.
	//
	// hostapd's DENY_ACL ADD_MAC disconnects a station that is already
	// associated, so a client can be gone and back before this function reaches
	// its own deauth loop. MEASURED 2026-09-06: a MacBook was disconnected from
	// wlan-usb2 at 16:12:50.439 by the ACL add and was associated to wlan0 at
	// 16:12:50.589 -- 150ms later, and before the operation existed to notice
	// it. Its arrival found no pin, so it stayed "pending" forever and the whole
	// operation ran to its timeout even though both clients had landed within a
	// second.
	//
	// You cannot miss an event you are already listening for, so listen first.
	// A client whose ACL then fails is removed again below.
	op.pending = map[string]bool{}
	for _, mac := range macs {
		op.pending[mac] = true
	}
	e.notePins(op)

	var firstErr error
	var pinned []string
	for _, mac := range macs {
		ok := true
		for i, w := range op.deny {
			if err := e.denyACLOn(w, "ADD", mac); err != nil {
				// Unwind this client. A ban covering some of the radios is worse
				// than none: the client is barred from part of the box and free
				// to sit on the rest, which is neither the old behaviour nor the
				// new one.
				for _, done := range op.deny[:i] {
					if e2 := e.denyACLOn(done, "DEL", mac); e2 != nil {
						log.Printf("pin unwind %s on %s: %v", mac, done, e2)
					}
				}
				if firstErr == nil {
					firstErr = fmt.Errorf("could not deny %s on %s: %w", mac, w, err)
				}
				ok = false
				break
			}
		}
		if !ok {
			// Never pinned, so it must not hold the operation open waiting for
			// an arrival nothing asked for.
			e.mu.Lock()
			delete(op.pending, mac)
			delete(e.pins, mac)
			e.mu.Unlock()
			continue
		}
		pinned = append(pinned, mac)
	}
	if len(pinned) == 0 {
		e.liftPins(op, "no client could be pinned")
		if firstErr != nil {
			return 0, firstErr
		}
		return 0, fmt.Errorf("no client could be pinned")
	}
	// Ask politely first. A client that honours the transition moves without
	// ever losing its association, which is cleaner than being dropped -- and
	// the ACLs mean the outcome is settled either way.
	//
	// Best effort: BTM is not deliverable from every radio here (no client has
	// ever answered one sent from the brcmfmac onboard radio), so a failure to
	// send is expected rather than exceptional, and the deauth is what actually
	// guarantees the move.
	for _, mac := range pinned {
		from := e.radioFor(mac)
		if from != "" && from != hint {
			if err := e.SteerClient(mac, from, hint, steerSuggest); err != nil {
				log.Printf("pin steer %s %s->%s: %v", mac, from, hint, err)
			}
		}
		if err := e.LinkDeauth(mac, 0); err != nil {
			log.Printf("pin deauth %s: %v", mac, err)
		}
	}

	go func() {
		time.Sleep(time.Duration(durSec * float64(time.Second)))
		e.liftPins(op, fmt.Sprintf("not everyone had landed after %.0fs", durSec))
	}()

	moved := len(pinned)
	e.logEvent(EventAction, iface, "", format, moved, iface, joinRadios(op.deny), durSec)
	return moved, firstErr
}

// joinRadios lists radios the way the activity log reads best: "a and b", not
// a bare comma-separated list, because these appear mid-sentence.
func joinRadios(rs []string) string {
	switch len(rs) {
	case 0:
		return "no radio"
	case 1:
		return rs[0]
	case 2:
		return rs[0] + " and " + rs[1]
	}
	out := ""
	for i, r := range rs[:len(rs)-1] {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out + " and " + rs[len(rs)-1]
}
