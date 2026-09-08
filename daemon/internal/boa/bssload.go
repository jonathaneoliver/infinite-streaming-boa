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

// bssLoadOverride is what one radio has been told to advertise.
//
// Absolute values rather than an increment, because the floor moves: our own
// airtime changes second to second, and an override expressed as "+20%" would
// wander with it. The operator picks a number; applyBSSLoad refuses to go below
// what is really happening.
type bssLoadOverride struct {
	// Stations to claim, and UtilPct as a percentage of the channel.
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
	On       bool    `json:"on"`
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

// SetBSSLoad records what a radio should advertise and applies it now.
//
// The values it settles on are RETURNED rather than assumed, because they are
// not necessarily the ones asked for: clampToFloor raises both to what is
// really happening, and the caller's slider has to end up showing the claim the
// box is actually making.
//
// The override is stored even when `on` is false, so switching the element back
// on does not lose the numbers the operator had already dialled in.
func (e *Engine) SetBSSLoad(iface string, on bool, stations int, utilPct float64) (BSSLoadState, error) {
	if err := e.radioReady(iface); err != nil {
		return BSSLoadState{}, err
	}
	st := e.bssLoadFloor(iface)
	stations, utilPct = clampToFloor(stations, utilPct, st)
	e.bssLoad.mu.Lock()
	if e.bssLoad.by == nil {
		e.bssLoad.by = map[string]bssLoadOverride{}
	}
	e.bssLoad.by[iface] = bssLoadOverride{Stations: stations, UtilPct: utilPct, On: on}
	e.bssLoad.mu.Unlock()

	if err := e.applyBSSLoad(iface); err != nil {
		return BSSLoadState{}, err
	}
	st.On, st.Stations, st.UtilPct = on, stations, utilPct
	if on {
		e.logEvent(EventRadio, iface, "",
			"%s now advertises %d station(s) and %.0f%% channel utilisation in its "+
				"beacon — a claim, not a measurement, and at or above the %.0f%% it "+
				"is really using",
			iface, stations, utilPct, st.FloorUtilPct)
	} else {
		e.logEvent(EventRadio, iface, "",
			"%s stopped overriding its BSS Load, so its beacon is back to the "+
				"0 stations and 0%% utilisation hostapd advertises by default — "+
				"which is itself an understatement, not a measurement", iface)
	}
	return st, nil
}

// clampToFloor raises a requested claim to what is really happening, and caps
// it at the top of each field's range.
//
// UP only, and that is the safety property rather than a nicety. Overstating
// load pushes devices away, which is what a genuinely busy access point does
// anyway; understating it PULLS them in, and that lands on neighbours'
// equipment nobody here owns or can observe. A box that can lie should only
// ever lie in the direction that costs other people nothing.
//
// It clamps rather than refuses: a slider dragged below what is really
// happening is an operator asking for the lowest honest value, not an error,
// and snapping to it says so more clearly than a rejection would.
func clampToFloor(stations int, utilPct float64, floor BSSLoadState) (int, float64) {
	if stations < floor.FloorStations {
		stations = floor.FloorStations
	}
	if utilPct < floor.FloorUtilPct {
		utilPct = floor.FloorUtilPct
	}
	if utilPct > 100 {
		utilPct = 100
	}
	// 255 is the field, not a policy: hostapd's bss_load_test takes the station
	// count as a byte, and a larger number would be silently truncated into a
	// smaller claim than the operator asked for.
	if stations > 255 {
		stations = 255
	}
	return stations, utilPct
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
	// 0:0:0 is how the element is withdrawn: hostapd treats an all-zero test
	// value as "stop overriding", and with no bss_load_update_period configured
	// that means no element at all.
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

// bssLoadFloor is what the radio is really doing, and therefore the lowest the
// controls may claim.
func (e *Engine) bssLoadFloor(iface string) BSSLoadState {
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
