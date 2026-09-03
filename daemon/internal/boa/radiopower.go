package boa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cutting a radio's power, and scanning the band from a radio taken briefly out
// of service.
//
// Both are AP-wide and both are deliberately disruptive, which is why they live
// here beside the other box-wide controls rather than on a device card.
//
// The distinction that makes the power cut worth having: every other impairment
// in this codebase TELLS the client something. A deauthentication is a frame
// saying "you are no longer associated", and a client that receives one
// reconnects in a second or two because it knows. An access point that loses
// power says nothing at all -- the beacons simply stop, and the client goes on
// believing it is connected until its own beacon-miss timeout expires, which is
// tens of seconds of a working-looking network that carries nothing.
//
// Nothing netem can do produces that, and neither can a deauthentication.

// --- power ----------------------------------------------------------------

// rfkillSoftPath finds the soft-block switch for the radio behind an interface.
//
// Through the interface's OWN phy80211 link rather than by walking
// /sys/class/rfkill and guessing from the device path, which is what
// select-radio has to do in shell. This is exact: the node under
// /sys/class/net/<iface>/phy80211/rfkill* belongs to that interface's phy and
// no other, so a two-radio box cannot cut power to the wrong one.
func rfkillSoftPath(iface string) (string, error) {
	if iface == "" {
		return "", fmt.Errorf("no radio named")
	}
	glob := filepath.Join("/sys/class/net", iface, "phy80211", "rfkill*", "soft")
	m, err := filepath.Glob(glob)
	if err != nil || len(m) == 0 {
		return "", fmt.Errorf("no rfkill switch for %s: it may not be a wireless interface", iface)
	}
	return m[0], nil
}

// radioPowered reports whether a radio is currently on. The second return is
// false when the question could not be asked at all -- an unknown state must
// not be rendered as "off", which would show a healthy radio as dead.
func radioPowered(iface string) (on bool, known bool) {
	p, err := rfkillSoftPath(iface)
	if err != nil {
		return false, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false, false
	}
	// soft = 1 means BLOCKED, so powered is the inverse.
	return strings.TrimSpace(string(b)) == "0", true
}

// SetRadioPower cuts or restores a radio's power, saying nothing to its clients.
//
// rfkill rather than stopping hostapd, and the difference is the whole feature:
// `systemctl stop` and hostapd's own DISABLE both tear the BSS down cleanly,
// which means deauthenticating every station on the way out -- exactly the
// announcement this is meant to withhold. Blocking at the rfkill level switches
// the transmitter off, so even if hostapd tries to say goodbye the frame cannot
// leave. Silence is guaranteed by the hardware rather than by hoping.
func (e *Engine) SetRadioPower(iface string, on bool) error {
	if err := e.radioExists(iface); err != nil {
		return err
	}
	if e.cfg.Demo {
		e.notePower(iface, on)
		return nil
	}
	p, err := rfkillSoftPath(iface)
	if err != nil {
		return err
	}
	v := "1" // 1 = soft blocked = powered off
	if on {
		v = "0"
	}
	if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
		return fmt.Errorf("rfkill %s: %w", iface, err)
	}
	if on {
		// The switch RETURNS NOW; the access point comes back on its own time.
		//
		// Power is restored the instant that write lands, and that is what this
		// call promises. Waiting for the BSS as well made the button take 25
		// seconds on the USB adapter: measured 2026-09-03, hostapd's control
		// socket on mt7921u is unresponsive for 10-25s after an unblock while
		// the driver re-initialises -- one STATUS took 14 seconds to answer --
		// and the wait was on that socket, not on the radio.
		//
		// Blocking for it also misrepresents the thing being simulated. This is
		// a mains switch. The client's job is to NOTICE the outage and NOTICE
		// the recovery; the operator's is to flip the switch, and a switch that
		// holds your hand down for half a minute is not one.
		//
		// The guarantee is kept, just moved: a background watch confirms the
		// access point actually came back, and says so loudly if it does not.
		// An unblocked radio serving nobody is exactly the silent failure this
		// codebase keeps being bitten by, and it is still caught.
		go e.confirmAPBack(iface)
	}
	e.notePower(iface, on)
	return nil
}

// confirmAPBack watches for the access point to re-form after a power-on, and
// reports either way. Runs in the background; the switch itself has returned.
func (e *Engine) confirmAPBack(iface string) {
	if !hostapdAvailable(iface) {
		return
	}
	started := time.Now()
	e.reenableAP(iface)
	took := time.Since(started).Round(time.Second)
	if apEnabled(iface) {
		// Logged with the duration because it is the number that answers "why
		// did my client take so long to come back", and it differs by an order
		// of magnitude between the two radios on this box.
		e.logEvent(EventRadio, iface, "", "%s access point back after %s", iface, took)
		return
	}
	e.logEvent(EventWarning, iface, "",
		"%s is powered on but its access point did not come back — it is serving nobody",
		iface)
}

// notePower records a power change in the event log. Worth a line of its own
// because it is the one action clients are told NOTHING about: the log is the
// only place the cut and the client's eventual reaction to it appear together.
func (e *Engine) notePower(iface string, on bool) {
	defer e.syncRadioState(iface)
	if on {
		e.logEvent(EventRadio, iface, "", "%s switched back on", iface)
		return
	}
	e.logEvent(EventRadio, iface, "",
		"%s switched OFF — clients are told nothing and must notice", iface)
}

// reenableAP nudges hostapd to serve again after its radio comes back.
// Best-effort and idempotent: ENABLE on an already-enabled BSS is refused
// harmlessly, and a radio with no hostapd has nothing to re-enable.
func (e *Engine) reenableAP(iface string) {
	if !hostapdAvailable(iface) {
		return
	}
	// WAIT FIRST. Do not command what is already happening.
	//
	// hostapd watches rfkill itself. Measured on both radios 2026-09-03, its
	// own log shows the unblock and the recovery in the same second:
	//
	//	22:30:45 rfkill: WLAN unblocked
	//	22:30:45 wlan0: INTERFACE-ENABLED
	//
	// So the access point comes back on its own and ENABLE has nothing to do.
	// Sending it anyway is not merely redundant: on mt7921u an ENABLE aimed at
	// a BSS hostapd had already restored took the switch-on from about a second
	// to 25, presumably tearing the interface down to build it again.
	//
	// Two earlier versions of this function got that backwards -- one slept
	// 500ms and fired ENABLE blind, the other retried ENABLE six times and
	// turned 4.5s into 27.8s. Both were commanding a recovery that was already
	// under way. Watch for it instead, and only intervene if it does not come.
	if waitAPEnabled(iface, 4*time.Second) {
		return
	}
	// It did not come back by itself, which is the case ENABLE is actually for.
	// Its reply is not the answer either: hostapd acknowledges only once the
	// BSS is up, and on mt7921u at 80MHz that outlasts the control socket's 2s
	// deadline, so a perfectly healthy recovery reports "i/o timeout".
	_, _ = hostapdCmd(iface, "ENABLE")
	if waitAPEnabled(iface, 20*time.Second) {
		return
	}
	// Loud, because this is the silent failure the wait exists to prevent: a
	// radio powered on with no access point on it.
	fmt.Printf("infinite-streaming-boa: %s came back but its access point was "+
		"still not enabled 20s later\n", iface)
}

// apEnabled reports whether hostapd is actually serving on this radio.
//
// The state of the BSS is a question with an answer, which is why every wait
// here is built on it rather than on whether a command was acknowledged.
func apEnabled(iface string) bool {
	st, err := hostapdCmd(iface, "STATUS")
	return err == nil && strings.Contains(st, "state=ENABLED")
}

// waitAPEnabled polls until the access point is serving, or the budget runs
// out. Returns whether it came back.
func waitAPEnabled(iface string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if apEnabled(iface) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// RadioOutage cuts a radio's power for durSec and restores it, in the
// background. The call returns once the radio is actually off.
//
// The timed form is the useful one: an outage is interesting because of what a
// player does DURING it and how it recovers after, and a manual off/on pair
// makes the duration whatever the operator's reflexes were.
func (e *Engine) RadioOutage(iface string, durSec float64) error {
	if durSec < 1 || durSec > 300 {
		return fmt.Errorf("outage must be 1-300 seconds (got %.0f)", durSec)
	}
	if err := e.SetRadioPower(iface, false); err != nil {
		return err
	}
	e.logEvent(EventAction, iface, "", "timed outage on %s: %.0fs", iface, durSec)
	go func() {
		time.Sleep(time.Duration(durSec * float64(time.Second)))
		if err := e.SetRadioPower(iface, true); err != nil {
			fmt.Printf("infinite-streaming-boa: restoring %s after outage: %v\n", iface, err)
		}
	}()
	return nil
}

// restoreRadioPower turns every watched radio back on at startup.
//
// Same reasoning as clearDenyACL: an outage in force when the daemon died would
// otherwise leave the access point off the air permanently, with nothing on
// screen to say why and no obvious way back short of a reboot. A cut that
// outlives the process that made it is not an impairment, it is a broken box.
func (e *Engine) restoreRadioPower() {
	if e.cfg.Demo {
		return
	}
	for _, w := range e.cfg.WlanPorts {
		if on, known := radioPowered(w); known && !on {
			fmt.Printf("infinite-streaming-boa: %s was powered off at startup; restoring\n", w)
			if err := e.SetRadioPower(w, true); err != nil {
				fmt.Printf("infinite-streaming-boa: restore %s: %v\n", w, err)
			}
		}
	}
}

// radioExists is the lighter gate for actions that do NOT need hostapd -- power
// is cut at the rfkill level, which works whether or not anything is serving.
func (e *Engine) radioExists(iface string) error {
	if iface == "" {
		return fmt.Errorf("no radio named")
	}
	if e.cfg.Demo {
		return nil
	}
	if !LinkExists(iface) {
		return fmt.Errorf("no interface named %s on this box", iface)
	}
	return nil
}

// radioOnFor describes the access point behind a radio, for the device list.
//
// CACHED, and refreshed on a slow timer rather than per tick. Channel, width
// and mode change only when something deliberately changes them, while asking
// hostapd costs a control round-trip per radio -- at 1Hz across two radios that
// is pure waste on a value that is almost always the same as last time.
//
// The cache is dropped whenever this daemon moves a radio, so the one case that
// would go stale fastest cannot.
func (e *Engine) radioOnFor(iface string) *RadioOn {
	if iface == "" {
		return nil
	}
	e.mu.RLock()
	r, ok := e.radioOn[iface]
	fresh := time.Since(e.radioOnAt) < 15*time.Second
	e.mu.RUnlock()
	if ok && fresh {
		return r
	}

	out := &RadioOn{Iface: iface}
	if e.cfg.Demo {
		out.Channel, out.WidthMHz, out.Mode, out.Band = 36, 80, "802.11ax", "5GHz"
	} else if hostapdAvailable(iface) {
		if st, err := hostapdCmd(iface, "STATUS"); err == nil {
			kv := parseHostapdKV(st)
			out.Channel = atoiSafe(kv["channel"])
			out.WidthMHz = apWidth(kv)
			out.Mode = apMode(kv)
		}
	}
	if out.Channel > 0 {
		out.Band = "2.4GHz"
		if out.Channel > 14 {
			out.Band = "5GHz"
		}
	}
	e.mu.Lock()
	if e.radioOn == nil {
		e.radioOn = map[string]*RadioOn{}
	}
	e.radioOn[iface] = out
	e.radioOnAt = time.Now()
	e.mu.Unlock()
	return out
}

// forgetRadioOn drops the cache after this daemon changes a radio, so the
// device list does not go on reporting the channel it used to be on.
func (e *Engine) forgetRadioOn() {
	e.mu.Lock()
	e.radioOn = nil
	e.radioOnAt = time.Time{}
	e.mu.Unlock()
}

// MoveChannel puts a radio on a chosen channel by taking it down and bringing
// it back up there.
//
// The working half of two ways to change channel, and the difference matters:
//
//	CHAN_SWITCH (802.11h)  the AP counts down in its beacons and clients FOLLOW,
//	                       staying associated -- seamless, and refused by both
//	                       drivers on this box (issue #154)
//	down and up            the AP vanishes and reappears elsewhere; clients are
//	                       told nothing and must notice, rescan and rejoin
//
// So this is the one that works here, and it is also what most consumer routers
// actually do when their channel changes. The cost is a reconnect for every
// client on the radio, which is why it says so before it runs.
//
// Returns the channel the radio is actually on afterwards, READ BACK rather
// than assumed -- a refused SET partway through does not undo the ones before
// it, and reporting the requested channel would be confidently wrong.
func (e *Engine) MoveChannel(iface string, channel, widthMHz int) (int, error) {
	ch, ok := apChannels[channel]
	if !ok {
		return 0, fmt.Errorf(
			"channel %d is not offered: 2.4GHz 1/6/11, 5GHz 36/40/44/48 "+
				"(DFS is excluded -- the Pi cannot serve an AP on one)", channel)
	}
	cmds := setChannelCommands(ch, widthMHz)
	if err := e.radioReady(iface); err != nil {
		return 0, err
	}
	if e.cfg.Demo {
		e.noteMoveChannel(iface, channel)
		return channel, nil
	}

	wasEnabled := false
	if st, err := hostapdCmd(iface, "STATUS"); err == nil {
		wasEnabled = strings.Contains(st, "state=ENABLED")
	}
	if wasEnabled {
		if _, err := hostapdCmd(iface, "DISABLE"); err != nil {
			return 0, fmt.Errorf("could not take %s down to move it: %w", iface, err)
		}
	}
	var refused string
	for _, cmd := range cmds {
		reply, err := hostapdCmd(iface, cmd)
		if err != nil || !strings.HasPrefix(reply, "OK") {
			refused = cmd
			break
		}
	}
	if wasEnabled {
		e.reenableAP(iface)
	}
	e.forgetRadioOn()

	now := 0
	if st, err := hostapdCmd(iface, "STATUS"); err == nil {
		now = atoiSafe(parseHostapdKV(st)["channel"])
	}
	// ANY mismatch is reported, not just one caused by a refusal.
	//
	// This used to require refused != "", so the most common failure on this box
	// passed silently: every SET returns OK, and the radio comes back somewhere
	// else anyway. Asking for 36 and being told "now on channel 40" with no
	// explanation is precisely the confidently-wrong readout the rest of this
	// file exists to avoid.
	if now != channel {
		e.logEvent(EventWarning, iface, "",
			"%s was asked for channel %d and came back on %d", iface, channel, now)
		if refused != "" {
			return now, fmt.Errorf(
				"%q was refused, so %s came back on channel %d", refused, iface, now)
		}
		return now, coexError(iface, channel, now, widthMHz)
	}
	e.noteMoveChannel(iface, now)
	e.syncRadioState(iface)
	return now, nil
}

// coexError explains a move that hostapd accepted, applied, and then undid.
//
// MEASURED on hardware 2026-09-03, from hostapd's own log:
//
//	wlan-usb: interface state COUNTRY_UPDATE->HT_SCAN
//	Switch own primary and secondary channel to get secondary channel
//	  with no Beacons from other BSSes
//	wlan-usb: interface state HT_SCAN->ENABLED
//
// That is the 802.11 20/40MHz coexistence scan. Before enabling a 40 or 80MHz
// BSS, hostapd looks for neighbours on the channel it intends to use as the
// SECONDARY, and if it finds any it swaps primary and secondary rather than
// interfering with them. So a config saying "channel 36, HT40+" -- primary 36,
// secondary 40 -- comes up as primary 40, secondary 36.
//
// This box has been on channel 40 all along for that reason, with
// /etc/hostapd/boa-usb.conf saying 36. It is not a fault and it is not
// overridable through the control socket: SET ht_capab [HT40+] is accepted and
// changes nothing, because secondary_channel is derived during the scan, and
// SET secondary_channel is refused outright as derived state.
//
// The practical consequence is the useful half of this message: at 40 or 80MHz
// the primary can only land where the coex scan puts it, so only one of each
// adjacent pair is reachable. At 20MHz there is no secondary, no scan, and
// every channel is exact.
func coexError(iface string, want, got, widthMHz int) error {
	if widthMHz < 40 {
		return fmt.Errorf(
			"%s was asked for channel %d but came back on %d, and hostapd "+
				"refused nothing along the way -- the driver overrode it",
			iface, want, got)
	}
	return fmt.Errorf(
		"%s came back on channel %d, not %d. Every setting was accepted; "+
			"hostapd's 20/40MHz coexistence scan then found neighbouring "+
			"access points on the secondary channel and swapped its primary "+
			"and secondary to avoid them. At %dMHz the primary can only land "+
			"where that scan puts it -- move at 20MHz to choose exactly",
		iface, got, want, widthMHz)
}

// --- scanning -------------------------------------------------------------

// ScanAP is one access point the scan found.
type ScanAP struct {
	BSSID     string  `json:"bssid"`
	SSID      string  `json:"ssid,omitempty"`
	FreqMHz   int     `json:"freq_mhz"`
	Channel   int     `json:"channel"`
	SignalDBm float64 `json:"signal_dbm"`
	// Ours marks an AP served by this box, so a channel does not look busy
	// because of the very radio asking the question.
	Ours bool `json:"ours,omitempty"`
}

// ScanChannel is the per-channel summary the recommendation is made from.
type ScanChannel struct {
	Channel int `json:"channel"`
	FreqMHz int `json:"freq_mhz"`
	// APs found on this channel, excluding our own.
	APs int `json:"aps"`
	// Strongest neighbour, dBm. Closer to zero is louder, so a channel with one
	// very loud neighbour is worse than one with three faint ones.
	StrongestDBm float64 `json:"strongest_dbm,omitempty"`
	Recommended  bool    `json:"recommended,omitempty"`
}

// ScanResult is a full band scan taken from a radio briefly out of service.
type ScanResult struct {
	Iface    string        `json:"iface"`
	Band     string        `json:"band"`
	Channels []ScanChannel `json:"channels"`
	APs      []ScanAP      `json:"aps"`
	// Best is the channel with the least competition, from the non-overlapping
	// set of the SCANNED BAND only. Empty when nothing was found to choose
	// between.
	Best int `json:"best_channel,omitempty"`
	// Was and Now are the operating channel before and after. They differ only
	// when the caller asked for the recommendation to be applied.
	Was int `json:"was_channel,omitempty"`
	Now int `json:"now_channel,omitempty"`
	// Applied says the radio came back up on Best rather than where it started.
	Applied bool `json:"applied,omitempty"`
	// ScanSec is how long the scan took. NOT an outage: the radio keeps serving
	// throughout, so this is a cost in beacon gaps rather than in downtime.
	ScanSec float64 `json:"scan_sec"`
	// OutageSec is how long the BSS was actually down, which is nonzero ONLY
	// when a channel move was applied -- that is the part that needs the radio
	// taken out of service. Reporting the scan's duration here would have
	// claimed a cost that was not paid.
	OutageSec float64 `json:"outage_sec,omitempty"`
	Note      string  `json:"note"`
}

// ScanBand takes a radio out of service, scans, and puts it back.
//
// A beaconing radio cannot survey other channels -- it is sitting on its own,
// which is why `iw survey dump` reports zeroes everywhere else and why the
// airtime readout can only ever describe the channel already in use. Choosing a
// better channel needs to actually LOOK at the others, and looking means not
// beaconing for a few seconds.
//
// DISABLE rather than stopping the unit: it drops the BSS through the control
// socket the daemon already holds, needs no systemctl (which the daemon cannot
// reach under ProtectSystem=strict), and ENABLE puts it back. On a box serving
// two radios the clients dropped here land on the other band and come back
// afterwards, so the network does not actually go away -- which is what makes
// this affordable at all.
// When apply is true the radio comes back up on the quietest channel found
// rather than the one it left. That is done while it is still DISABLED --
// SET channel, then ENABLE -- which is why it works on hardware that refuses
// CHAN_SWITCH (see issue #154): nothing is announced to anyone, the access
// point simply reappears somewhere else and clients rediscover it. Most
// consumer routers change channel exactly this way.
func (e *Engine) ScanBand(iface string, apply bool) (ScanResult, error) {
	if err := e.radioReady(iface); err != nil {
		return ScanResult{}, err
	}
	started := time.Now()
	if e.cfg.Demo {
		e.logEvent(EventAction, iface, "", "%s scan requested (demo: no radio to scan)", iface)
		return ScanResult{Iface: iface, Note: "demo mode: no radio to scan"}, nil
	}

	// Remember what it was doing, so it can be put back exactly.
	wasEnabled, wasChannel, wasWidth := false, 0, 20
	if st, err := hostapdCmd(iface, "STATUS"); err == nil {
		kv := parseHostapdKV(st)
		wasEnabled = kv["state"] == "ENABLED"
		wasChannel = atoiSafe(kv["channel"])
		if w := apWidth(kv); w > 0 {
			wasWidth = w
		}
	}
	ourBSSIDs := e.ownBSSIDs()

	// Scan with the access point STILL UP.
	//
	// This was written the other way round -- take the BSS down, scan, put it
	// back -- on the reasoning that a beaconing radio cannot survey other
	// channels. That is true of `survey dump` and false of `scan`, and the
	// difference is the whole design. Measured 2026-09-03 on brcmfmac:
	//
	//	AP beaconing   -- scan succeeds, 4s, 12 access points found
	//	BSS DISABLEd   -- scan fails instantly, "Network is down (-100)"
	//
	// DISABLE takes the interface down, and a down interface cannot scan. So
	// the teardown built to make scanning possible was the thing preventing
	// it. Both drivers here do off-channel scanning while in AP mode, which is
	// how enterprise access points evaluate channels continuously.
	//
	// The cost is a few beacon gaps rather than a disconnection: clients stay
	// associated throughout and nobody is dropped.
	raw, err := exec.Command("iw", "dev", iface, "scan").Output()
	disrupted := false
	if err != nil {
		// This driver will not scan while it is serving. Take the BSS down,
		// bring the interface back up, scan, and put it back.
		//
		// The two radios are exact opposites, measured 2026-09-03:
		//
		//	brcmfmac  scan while beaconing WORKS (4s); with the BSS disabled it
		//	          fails, "Network is down (-100)"
		//	mt7921u   scan while beaconing fails, "Operation not supported
		//	          (-95)", passive too; with the BSS disabled it WORKS (3s)
		//
		// So neither order suits both, and trying the free one first is what
		// makes the onboard radio's scan cost nothing while still letting the
		// adapter scan at all. The `ip link set up` matters: DISABLE takes the
		// interface down, and a down interface cannot scan.
		disrupted = true
		if wasEnabled {
			if _, derr := hostapdCmd(iface, "DISABLE"); derr != nil {
				return ScanResult{}, fmt.Errorf(
					"%s will not scan while serving (%v) and could not be taken down: %w",
					iface, err, derr)
			}
			// Back up whatever happens below, including a scan that still fails.
			defer e.reenableAP(iface)
		}
		_ = exec.Command("ip", "link", "set", iface, "up").Run()
		raw, err = exec.Command("iw", "dev", iface, "scan").Output()
		if err != nil {
			return ScanResult{}, fmt.Errorf(
				"scan on %s failed even with the access point stopped: %w", iface, err)
		}
	}

	aps := parseScan(string(raw))
	for i := range aps {
		aps[i].Ours = ourBSSIDs[strings.ToLower(aps[i].BSSID)]
	}
	// The band comes from the channel the radio is actually on.
	band := ""
	if wasChannel > 0 {
		band = "2.4GHz"
		if wasChannel > 14 {
			band = "5GHz"
		}
	}
	res := summariseScan(iface, band, aps)
	res.ScanSec = time.Since(started).Seconds()
	if disrupted {
		// This driver needed the access point stopped, so the scan DID cost an
		// outage and says so. On the other radio the same button costs nothing,
		// and claiming otherwise either way would be a lie about what happened.
		res.OutageSec = res.ScanSec
		res.Note += " This radio will not scan while it is serving, so the " +
			"access point was stopped for the duration and its clients were dropped."
	} else {
		res.Note += " This radio scans off-channel while still serving, so " +
			"nobody was dropped -- the cost was a few beacon gaps."
	}
	res.Was, res.Now = wasChannel, wasChannel

	// MOVING is the part that costs an outage, and only that part.
	//
	// The channel can only be changed while the BSS is down -- once it is
	// enabled the only route is CHAN_SWITCH, which this adapter refuses (#154).
	// So the teardown happens here, after the scan, and only when the caller
	// actually asked to move. A scan on its own now drops nobody.
	if apply && res.Best != 0 && res.Best != wasChannel {
		if ch, ok := apChannels[res.Best]; ok {
			if wasEnabled {
				if _, err := hostapdCmd(iface, "DISABLE"); err != nil {
					res.Note += fmt.Sprintf(
						" Could not take %s down to move it: %v.", iface, err)
					return res, nil
				}
			}
			// The AP comes back whatever happens below. Leaving a radio down
			// because a SET was refused would turn a diagnostic into an outage
			// nobody asked for.
			defer func() {
				if wasEnabled {
					e.reenableAP(iface)
				}
			}()

			moved := true
			for _, cmd := range setChannelCommands(ch, wasWidth) {
				reply, err := hostapdCmd(iface, cmd)
				if err != nil || !strings.HasPrefix(reply, "OK") {
					// Reported, not swallowed: a half-applied channel change is
					// how a radio comes back on a configuration nobody chose.
					res.Note += fmt.Sprintf(
						" Could not apply channel %d (%q refused), so it came back on %d.",
						res.Best, cmd, wasChannel)
					moved = false
					break
				}
			}
			// ASK, do not assume. The first attempt at this reported "came
			// back on 6" while the radio was demonstrably on 11: one SET in the
			// middle was refused, the code broke out of the loop and claimed
			// the old channel, but the SET that mattered had already landed. A
			// confidently wrong readout is worse than the failure it hides.
			//
			// reenableAP runs in the deferred call AFTER this, so the BSS is
			// brought up first and then asked where it actually is.
			if wasEnabled {
				e.reenableAP(iface)
				wasEnabled = false // the deferred re-enable is now redundant
			}
			if st, serr := hostapdCmd(iface, "STATUS"); serr == nil {
				if now := atoiSafe(parseHostapdKV(st)["channel"]); now > 0 {
					res.Now = now
					res.Applied = now == res.Best && now != res.Was
				}
			} else if moved {
				res.Applied, res.Now = true, res.Best
			}
			// The device list caches each radio's channel; this just changed it.
			e.forgetRadioOn()
			// Only now is there an outage to report: the BSS was down from the
			// DISABLE above until reenableAP runs in the deferred call.
			res.OutageSec = time.Since(started).Seconds() - res.ScanSec
		}
	}

	// One line, saying what it cost as well as what it found: a scan that
	// dropped every client and one that cost a few beacon gaps look identical
	// in the result otherwise.
	switch {
	case res.Applied:
		e.logEvent(EventRadio, iface, "", "%s scanned %s (%d APs) and moved %d → %d",
			iface, band, len(aps), res.Was, res.Now)
	case res.OutageSec > 0:
		e.logEvent(EventAction, iface, "", "%s scanned %s (%d APs, %.0fs off the air), stayed on %d",
			iface, band, len(aps), res.OutageSec, res.Now)
	default:
		e.logEvent(EventAction, iface, "", "%s scanned %s (%d APs) while serving, stayed on %d",
			iface, band, len(aps), res.Now)
	}
	e.syncRadioState(iface)
	return res, nil
}

// ownBSSIDs collects the BSSIDs this box serves, so its own access points are
// not counted as competition on the channel it is asking about.
func (e *Engine) ownBSSIDs() map[string]bool {
	out := map[string]bool{}
	for _, w := range e.cfg.WlanPorts {
		if !hostapdAvailable(w) {
			continue
		}
		if st, err := hostapdCmd(w, "STATUS"); err == nil {
			for k, v := range parseHostapdKV(st) {
				if strings.HasPrefix(k, "bssid[") && v != "" {
					out[strings.ToLower(v)] = true
				}
			}
		}
	}
	return out
}

// parseScan reads `iw dev <if> scan` output. One record per "BSS <mac>" stanza.
func parseScan(raw string) []ScanAP {
	var out []ScanAP
	var cur *ScanAP
	flush := func() {
		if cur != nil && cur.BSSID != "" {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "BSS "):
			flush()
			f := strings.Fields(t)
			if len(f) < 2 {
				continue
			}
			// "BSS aa:bb:cc:dd:ee:ff(on wlan0)" -- the interface is glued on.
			mac := f[1]
			if i := strings.Index(mac, "("); i > 0 {
				mac = mac[:i]
			}
			cur = &ScanAP{BSSID: strings.ToLower(mac)}
		case cur == nil:
		case strings.HasPrefix(t, "freq:"):
			// A FLOAT here, unlike everywhere else. Measured: `iw dev wlan0
			// scan` prints "freq: 2417.0" while `survey dump` prints
			// "frequency: 2412 MHz" as an integer -- the two commands disagree,
			// and atoiSafe on "2417.0" returns 0.
			//
			// Zero was the worst possible failure: bandOf(0) is "2.4GHz", so
			// every access point looked like a 2.4GHz one, the band filter
			// excluded nothing, and 5GHz neighbours were listed in a 2.4GHz
			// table. It only stayed invisible because the channel happened to
			// arrive separately from the DS Parameter set line.
			cur.FreqMHz = atoiLoose(surveyValue(t))
			cur.Channel = channelForFreq(cur.FreqMHz)
		case strings.HasPrefix(t, "signal:"):
			cur.SignalDBm = atofSafe(surveyValue(t))
		case strings.HasPrefix(t, "SSID:"):
			_, v, _ := strings.Cut(t, ":")
			cur.SSID = strings.TrimSpace(v)
		case strings.HasPrefix(t, "DS Parameter set: channel"):
			// Authoritative where present; freq alone is enough otherwise.
			f := strings.Fields(t)
			if len(f) > 0 {
				if n := atoiSafe(f[len(f)-1]); n > 0 {
					cur.Channel = n
				}
			}
		}
	}
	flush()
	return out
}

// atoiLoose parses an integer that may arrive with a decimal part, truncating
// it. iw is not consistent about this between subcommands.
func atoiLoose(s string) int {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return atoiSafe(s)
}

// channelForFreq converts a frequency to a channel number for the two bands
// this box can serve. Arithmetic rather than a table because the scan sees the
// whole band, including channels the AP itself may not use.
func channelForFreq(mhz int) int {
	switch {
	case mhz == 2484:
		return 14
	case mhz >= 2412 && mhz <= 2472:
		return (mhz - 2407) / 5
	case mhz >= 5160 && mhz <= 5885:
		return (mhz - 5000) / 5
	}
	return 0
}

// summariseScan turns a list of access points into per-channel competition and
// picks the quietest channel worth moving to.
// summariseScan is given the band the RADIO is on rather than inferring it.
//
// Both chips here are dual-band and scan BOTH: a scan from the 2.4GHz radio
// returned 5GHz neighbours too. Taking the band from the first access point
// parsed made it whatever happened to sort first, and mixed 5GHz rows into a
// 2.4GHz table where they mean nothing -- the radio cannot move there.
func summariseScan(iface, band string, aps []ScanAP) ScanResult {
	byChan := map[int]*ScanChannel{}
	for _, a := range aps {
		// Our own access points would make the channel we are already on look
		// permanently busiest. Other bands cannot be moved to, so counting them
		// would put rows in the table that no recommendation can ever use.
		if a.Ours || a.Channel == 0 || (band != "" && bandOf(a.FreqMHz) != band) {
			continue
		}
		c := byChan[a.Channel]
		if c == nil {
			c = &ScanChannel{Channel: a.Channel, FreqMHz: a.FreqMHz}
			byChan[a.Channel] = c
		}
		c.APs++
		if a.SignalDBm != 0 && (c.StrongestDBm == 0 || a.SignalDBm > c.StrongestDBm) {
			c.StrongestDBm = a.SignalDBm
		}
	}
	out := ScanResult{Iface: iface, Band: band}
	for _, c := range byChan {
		out.Channels = append(out.Channels, *c)
	}
	sort.Slice(out.Channels, func(i, j int) bool {
		return out.Channels[i].Channel < out.Channels[j].Channel
	})
	out.APs = aps
	out.Best = pickBestChannel(out.Channels, band)
	for i := range out.Channels {
		if out.Channels[i].Channel == out.Best {
			out.Channels[i].Recommended = true
		}
	}
	// Deliberately says nothing about the cost: whether this scan dropped
	// anyone depends on the driver and is appended by the caller, which is the
	// only place that knows. A fixed claim here was self-contradictory the
	// moment the adaptive path took the disruptive branch.
	out.Note = "A real scan: what other people's access points are doing, which " +
		"is what a channel choice actually depends on and what the airtime " +
		"counters cannot see."
	return out
}

func bandOf(mhz int) string {
	if mhz < 3000 {
		return "2.4GHz"
	}
	return "5GHz"
}

// pickBestChannel chooses from the non-overlapping set, IN THE SCANNED BAND.
//
// The band restriction is not a detail: a 5GHz radio handed "channel 1" would
// be told to move somewhere it cannot go, and a scan of the 5GHz band says
// nothing whatever about how busy 2.4GHz is. Only channels of the same band as
// the radio being scanned are candidates.
//
// Within that, only the non-overlapping set. Recommending 2.4GHz channel 3
// because it happens to be empty is a trap: it overlaps 1 and 6, taking
// interference from both and giving it back, which is worse for everyone than
// sitting on a busier channel 1.
func pickBestChannel(chans []ScanChannel, band string) int {
	counts := map[int]int{}
	loudest := map[int]float64{}
	for _, c := range chans {
		counts[c.Channel] = c.APs
		loudest[c.Channel] = c.StrongestDBm
	}
	var candidates []int
	for ch, c := range apChannels {
		if bandOf(c.FreqMHz) == band {
			candidates = append(candidates, ch)
		}
	}
	sort.Ints(candidates)

	best, bestScore := 0, 0.0
	for _, ch := range candidates {
		// 2.4GHz channels overlap their four neighbours either side, so an AP on
		// channel 4 genuinely competes with one on channel 6. Count the whole
		// overlapping window rather than exact matches, or a crowded band looks
		// empty. 5GHz channels at 20MHz do not overlap, so they are counted
		// exactly.
		n := 0
		var loud float64
		if ch <= 14 {
			for off := -4; off <= 4; off++ {
				n += counts[ch+off]
				if l := loudest[ch+off]; l != 0 && (loud == 0 || l > loud) {
					loud = l
				}
			}
		} else {
			n = counts[ch]
			loud = loudest[ch]
		}
		// Fewer neighbours first; a louder neighbour breaks the tie, since one
		// strong AP nearby costs more airtime than several distant ones.
		// Signals are negative dBm, so +100 keeps the tiebreak positive and
		// ordered the same way.
		score := float64(n)*100 + (loud + 100)
		if best == 0 || score < bestScore {
			best, bestScore = ch, score
		}
	}
	return best
}

// setChannelCommands builds the hostapd SET commands that move a DISABLED BSS
// to another channel, to take effect when it is enabled again.
//
// Pure and separately tested, because the 80MHz case needs three values that
// have to agree -- the primary channel, which side its 40MHz secondary sits,
// and the centre of the 80MHz block -- and hostapd rejects the whole ENABLE if
// they contradict each other, with an error about the kernel driver that says
// nothing about the cause.
func setChannelCommands(ch apChannel, widthMHz int) []string {
	cmds := []string{"SET channel " + strconv.Itoa(ch.Channel)}
	if ch.is24() || widthMHz < 40 {
		// 20MHz: no secondary, no centre.
		return append(cmds,
			"SET vht_oper_chwidth 0",
			"SET he_oper_chwidth 0",
		)
	}
	// The secondary offset is set through ht_capab, NOT through
	// secondary_channel. Measured 2026-09-03: `SET secondary_channel 0` is the
	// one parameter of all of these that hostapd refuses -- it is derived
	// state, reported in STATUS and not settable. Every other SET here is
	// accepted.
	//
	// And it MUST agree with the channel. Setting [HT40-] and then channel 36,
	// whose secondary sits above it, is accepted one command at a time and
	// then fails the whole ENABLE -- measured, and it left the access point
	// down until the offset was corrected. The side comes from the channel
	// table so the two cannot disagree.
	side := "[HT40+]"
	if ch.SecOffset < 0 {
		side = "[HT40-]"
	}
	cmds = append(cmds, "SET ht_capab "+side)
	if widthMHz >= 80 {
		centre := strconv.Itoa(channelForFreq(ch.Center80))
		cmds = append(cmds,
			"SET vht_oper_chwidth 1",
			"SET he_oper_chwidth 1",
			"SET vht_oper_centr_freq_seg0_idx "+centre,
			"SET he_oper_centr_freq_seg0_idx "+centre,
		)
	}
	return cmds
}
