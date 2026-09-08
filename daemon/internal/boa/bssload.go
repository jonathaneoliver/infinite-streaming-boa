package boa

import (
	"fmt"
	"sync"
	"time"
)

/*
 * Telling clients the channel is busier than it is.
 *
 * Every other impairment here acts on the LINK -- the rate, the delay, the loss
 * a client's packets actually meet. This one acts on the client's INFORMATION:
 * the BSS Load element in our beacon, which carries a station count and a
 * channel utilisation, and which some clients weigh when choosing between
 * access points. It is the difference between making a client slow and making
 * it decide.
 *
 * That is a genuinely different instrument, and it is the only way to ask the
 * question directly: given two radios at the same signal strength, will a
 * device prefer the one that says it is quieter? 802.11v BSS Transition can
 * ORDER a client to move; nothing else here can offer it a reason and watch
 * what it does with one.
 *
 * MEASURED 2026-09-08, and the mechanism is not obvious:
 *
 *	SET bss_load_test 3:40:0        OK -- and the beacon does not change
 *	update_beacon                   OK -- and now it does
 *	stations stayed associated      connected time 259s -> 261s, no drop
 *	SET bss_load_test 0:0:0         the element is STILL THERE, reading 0/255
 *
 * So the pair is required, and the pair is FREE: no restart, no client dropped,
 * no fork of hostapd. bss_load_test is a CONFIG_TESTING_OPTIONS parameter that
 * Debian's build happens to enable, which is the only reason this is a config
 * change rather than a patched daemon.
 */

// bssLoadOverride is what one radio has been ASKED to advertise.
//
// The ask, deliberately, and not the number that reaches the air. The floor
// moves -- it is our own airtime, second by second -- so the two have to be
// stored apart: applyBSSLoad raises the ask to the floor at the moment it
// sends, and this keeps what the operator actually chose.
//
// Storing the raised value instead was tried and is a RATCHET. The floor is
// re-asserted every 15s, so one iperf burst would lift a 20% claim to 80% and
// nothing would ever bring it back down; by the end of an afternoon every radio
// would be permanently claiming its own worst moment. What an operator set has
// to survive the traffic they set it to watch.
//
// Absolute values rather than an increment, for the same reason: an override
// expressed as "+20%" would wander with the floor it is measured against.
type bssLoadOverride struct {
	// Stations to claim, and UtilPct as a percentage of the channel. What was
	// ASKED for -- see effectiveBSSLoad for what is actually advertised.
	Stations int     `json:"stations"`
	UtilPct  float64 `json:"util_pct"`
	// On is false when the radio should stop overriding, which on this build is
	// NOT silence.
	//
	// MEASURED 2026-09-08, and it is the opposite of what this code first
	// claimed. `0:0:0` was expected to withdraw the element; the beacon still
	// carries a BSS Load reading 0 stations and 0/255, and so does a radio that
	// has never been sent a bss_load_test at all -- wlan-usb2, running since the
	// previous evening off a config with no bss_load line in it. On this hostapd
	// the element is simply always present.
	//
	// So "off" means the beacon goes back to the zeros the box already
	// advertises, not to saying nothing. Which is worth naming plainly: those
	// zeros are themselves an understatement, and the box cannot fix them,
	// because hostapd derives the honest figure from the survey counter and that
	// counter reads near zero on mt7921u while the radio is 80% busy (see
	// DATA-CONTRACT Source T). The choices here are a deliberate claim or a
	// default that is already wrong in the direction this control is forbidden
	// to move in.
	On bool `json:"on"`
}

// BSSLoadState is one radio's override and the floor it may not go below.
type BSSLoadState struct {
	On bool `json:"on"`
	// Stations and UtilPct are what was ASKED for, which is where the handle
	// sits. What actually goes into the beacon is this raised to the floor
	// below, because the floor moves after the ask is made -- so a reader wanting
	// the advertised figure takes the larger of the two, exactly as the interface
	// draws it.
	Stations int     `json:"stations"`
	UtilPct  float64 `json:"util_pct"`

	// FloorStations and FloorUtilPct are what is REALLY there, and the lowest
	// the controls may be set to.
	//
	// The constraint is the whole safety property. Overstating load pushes
	// devices away, which is what a genuinely busy access point does anyway;
	// understating it PULLS them in, and that lands on neighbours' equipment we
	// do not own and cannot observe. A box that can lie should only ever lie in
	// the direction that costs other people nothing.
	//
	// The utilisation floor is our own measured airtime -- the figure verified
	// against iperf3 in Source T -- because the channel is at least as busy as
	// we are making it. It is a lower bound rather than the true utilisation,
	// which would also include neighbours we can only see when we scan.
	FloorStations int     `json:"floor_stations"`
	FloorUtilPct  float64 `json:"floor_util_pct"`
	// FloorKnown is false where the driver cannot report per-client airtime, so
	// the utilisation floor is a guess rather than a measurement. The onboard
	// brcmfmac radio is the case: no per-station duration counters at all.
	FloorKnown bool `json:"floor_known"`
}

type bssLoadStore struct {
	mu sync.RWMutex
	by map[string]bssLoadOverride
}

// SetBSSLoad records what a radio has been asked to advertise and applies it
// now.
//
// The ASK is what gets stored, capped to each field's range and nothing more.
// Raising it to the floor happens at send time, in applyBSSLoad, because the
// floor is a moving measurement and this is a decision made once -- see
// bssLoadOverride on why storing the raised value ratchets.
//
// The override is stored even when `on` is false, so switching the element back
// on does not lose the numbers the operator had already dialled in.
func (e *Engine) SetBSSLoad(iface string, on bool, stations int, utilPct float64) (BSSLoadState, error) {
	if err := e.radioReady(iface); err != nil {
		return BSSLoadState{}, err
	}
	stations, utilPct = capToRange(stations, utilPct)
	e.bssLoad.mu.Lock()
	if e.bssLoad.by == nil {
		e.bssLoad.by = map[string]bssLoadOverride{}
	}
	e.bssLoad.by[iface] = bssLoadOverride{Stations: stations, UtilPct: utilPct, On: on}
	e.bssLoad.mu.Unlock()

	if err := e.applyBSSLoad(iface); err != nil {
		return BSSLoadState{}, err
	}
	st := e.bssLoadFloor(iface)
	st.On, st.Stations, st.UtilPct = on, stations, utilPct
	if on {
		// The EFFECTIVE figures, because that is what went on the air. Logging
		// the ask would leave the record disagreeing with the beacon whenever the
		// radio was busier than the operator claimed.
		gotSt, gotUtil := raiseToFloor(stations, utilPct, st)
		e.logEvent(EventRadio, iface, "",
			"%s now advertises %d station(s) and %.0f%% channel utilisation in its "+
				"beacon — a claim, not a measurement, and at or above the %.0f%% it "+
				"is really using",
			iface, gotSt, gotUtil, st.FloorUtilPct)
	} else {
		e.logEvent(EventRadio, iface, "",
			"%s stopped overriding its BSS Load, so its beacon is back to the "+
				"0 stations and 0%% utilisation hostapd advertises by default — "+
				"which is itself an understatement, not a measurement", iface)
	}
	return st, nil
}

// capToRange holds a claim inside what each field can carry.
//
// Not a policy, the FIELD. Utilisation is a percentage, and hostapd's
// bss_load_test takes the station count as a byte -- a larger number would be
// silently truncated into a SMALLER claim than was asked for, which is a lie in
// the one direction this control must never tell.
func capToRange(stations int, utilPct float64) (int, float64) {
	if utilPct > 100 {
		utilPct = 100
	}
	if utilPct < 0 {
		utilPct = 0
	}
	if stations > 255 {
		stations = 255
	}
	if stations < 0 {
		stations = 0
	}
	return stations, utilPct
}

// raiseToFloor lifts a claim to what is really happening, and never lowers it.
//
// UP only, and that is the safety property rather than a nicety. Overstating
// load pushes devices away, which is what a genuinely busy access point does
// anyway; understating it PULLS them in, and that lands on neighbours'
// equipment nobody here owns or can observe. A box that can lie should only
// ever lie in the direction that costs other people nothing.
//
// Applied at SEND time rather than when the operator chooses, because the floor
// is a live measurement: a claim that was honest when it was made stops being
// honest the moment the radio gets busier. The result is not stored -- see
// bssLoadOverride.
func raiseToFloor(stations int, utilPct float64, floor BSSLoadState) (int, float64) {
	if stations < floor.FloorStations {
		stations = floor.FloorStations
	}
	if utilPct < floor.FloorUtilPct {
		utilPct = floor.FloorUtilPct
	}
	return capToRange(stations, utilPct)
}

// bssLoadArg is the bss_load_test parameter for one override.
//
// TWO conversions live here and both have bitten this repo before:
//
//	0:0:0     stops overriding. NOT silence: measured 2026-09-08, the beacon
//	          still carries a BSS Load element reading 0 stations and 0/255
//	          afterwards, exactly as a radio that was never sent one does. The
//	          element is always in the beacon on this build.
//	0-255     is what the wire carries for what the interface shows as a
//	          percentage. 60 on the wire is 23.5%, not 60%. This is the single
//	          place that conversion happens, which is the whole point of
//	          docs/DATA-CONTRACT.md existing.
func bssLoadArg(ov bssLoadOverride) string {
	if !ov.On {
		return "0:0:0"
	}
	return fmt.Sprintf("%d:%d:0", ov.Stations, int(ov.UtilPct/100*255+0.5))
}

// applyBSSLoad pushes the stored override at the running access point.
//
// Two commands, and both are needed. SET alone returns OK and changes nothing
// -- the beacon is only rebuilt when something asks it to be, which is what
// update_beacon is for. Measured 2026-09-08; the same shape as the transmit
// power control that "validates and then ignores" in Source Q, except that here
// there is a second verb that finishes the job.
func (e *Engine) applyBSSLoad(iface string) error {
	e.bssLoad.mu.RLock()
	ov, ok := e.bssLoad.by[iface]
	e.bssLoad.mu.RUnlock()
	if !ok {
		return nil
	}
	// Raised to the floor HERE, on every send, so the beacon can never sit below
	// what the radio is really doing. MEASURED 2026-09-08: our own airtime went
	// 0% to 80% within four seconds of an iperf3 starting, so a claim checked
	// only when it was made is stale almost immediately -- and the interface was
	// already drawing the raised figure, which left the screen and the air
	// disagreeing whenever traffic outran the claim.
	if ov.On {
		ov.Stations, ov.UtilPct = raiseToFloor(ov.Stations, ov.UtilPct, e.bssLoadFloor(iface))
	}
	if _, err := hostapdSend(iface, "SET bss_load_test "+bssLoadArg(ov)); err != nil {
		return fmt.Errorf("setting bss_load_test on %s: %w", iface, err)
	}
	if _, err := hostapdSend(iface, "UPDATE_BEACON"); err != nil {
		return fmt.Errorf(
			"%s accepted the BSS Load value but would not rebuild its beacon, so "+
				"nothing changed on the air: %w", iface, err)
	}
	return nil
}

// reapplyBSSLoad puts every override back after hostapd has restarted.
//
// Needed because the value lives in the running process and nothing else. A
// width change, a profile, a USB re-enumeration or a driver reload all restart
// hostapd, and each one silently drops the override -- the same class of
// disappearance ChannelStore exists to fix for the channel.
func (e *Engine) reapplyBSSLoad() {
	e.bssLoad.mu.RLock()
	ifaces := make([]string, 0, len(e.bssLoad.by))
	for w, ov := range e.bssLoad.by {
		if ov.On {
			ifaces = append(ifaces, w)
		}
	}
	e.bssLoad.mu.RUnlock()
	for _, w := range ifaces {
		if e.radioReady(w) != nil {
			continue
		}
		if err := e.applyBSSLoad(w); err != nil {
			e.logEvent(EventRadio, w, "",
				"could not restore %s's advertised BSS Load after a restart, so it "+
					"is telling clients nothing: %v", w, err)
		}
	}
}

// bssFloorFor is the seam a test replaces, so the floor can be moved under a
// claim without needing a radio and real traffic to move it. In production it
// is the method immediately below.
var bssFloorFor = (*Engine).measuredBSSLoadFloor

// bssLoadFloor is what the radio is really doing, and therefore the lowest the
// controls may claim.
func (e *Engine) bssLoadFloor(iface string) BSSLoadState {
	return bssFloorFor(e, iface)
}

// measuredBSSLoadFloor reads the floor off the radio itself.
func (e *Engine) measuredBSSLoadFloor(iface string) BSSLoadState {
	out := BSSLoadState{FloorStations: len(StationDump(iface))}
	// Our own airtime over the last few seconds, which is a LOWER BOUND on the
	// channel's utilisation: whatever the neighbours are adding, the channel is
	// at least as busy as we are making it.
	if own := e.ownAirtime(time.Now(), e.airtimeSeen()); own != nil {
		if v, ok := own[iface]; ok {
			out.FloorUtilPct, out.FloorKnown = v, true
		}
	}
	return out
}

// BSSLoadStates is every radio's override and floor, for the inventory.
func (e *Engine) BSSLoadStates(ifaces []string) map[string]BSSLoadState {
	if len(ifaces) == 0 {
		return nil
	}
	out := map[string]BSSLoadState{}
	for _, w := range ifaces {
		st := e.bssLoadFloor(w)
		e.bssLoad.mu.RLock()
		ov, ok := e.bssLoad.by[w]
		e.bssLoad.mu.RUnlock()
		if ok {
			st.On, st.Stations, st.UtilPct = ov.On, ov.Stations, ov.UtilPct
		}
		out[w] = st
	}
	return out
}
