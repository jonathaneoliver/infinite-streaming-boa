package boa

import (
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

/*
 * Transmit power, set live on the phy.
 *
 * Attenuation as an impairment: turning a radio down moves every client's
 * received signal without reassociating anyone. MEASURED 2026-09-22 on a Cudy
 * TR3000's built-in mt798x-wmac radio, a MacBook associated throughout and
 * pinged every 200ms:
 *
 *	set 23 dBm  reports 23.00  Mac hears -32 dBm
 *	set 10 dBm  reports 10.00  Mac hears -44 dBm
 *	set  3 dBm  reports  3.00  Mac hears -51 dBm
 *	set 23 dBm  reports 23.00  Mac hears -33 dBm
 *
 * connected time rose 239 -> 297s through all four, and 379 pings came back
 * with no gap.
 *
 * THE PHY, NOT THE INTERFACE. `iw dev phy1-ap0 set txpower fixed 1000` is
 * accepted, reports success and changes nothing -- measured on the same radio,
 * the reported power and the client's signal both stayed put. Only
 * `iw phy phy1 set txpower ...` reaches the driver. That path is nl80211 ->
 * mac80211 -> the driver -> firmware, and hostapd is not on it, which is why
 * nothing is dropped.
 *
 * UNITS ARE mBm on the command line: `fixed 1000` is 10 dBm.
 */

// txpowerIgnoredBy names drivers that accept a transmit power and discard it,
// with the reason shown in place of the control.
//
// mt7921u, #202: it reports 3.00 dBm on every band and a request across its
// whole range moves the client's signal by nothing. A slider there would be
// present and silently ineffective, which is worse than absent.
var txpowerIgnoredBy = map[string]string{
	"mt7921u": "the mt7921u driver ignores transmit power: it reports 3 dBm and " +
		"discards every setting (#202)",
}

// TxPower is a radio's transmit power as the kernel reports it.
type TxPower struct {
	// DBm is the current setting, from `iw dev <if> info`.
	DBm float64 `json:"dbm"`
	// MaxDBm is the regulatory limit on the channel the radio is on, from the
	// phy's channel list. 0 when it cannot be read, in which case the slider
	// has no top to offer.
	MaxDBm float64 `json:"max_dbm,omitempty"`
	// Settable is false where the driver is known to ignore a setting, and
	// Why then says so.
	Settable bool   `json:"settable"`
	Why      string `json:"why,omitempty"`
}

// txIgnoredReason says why this driver's transmit power cannot be set: from
// the static list, or from a reading taken after an attempt.
func (e *Engine) txIgnoredReason(driver string) string {
	if why, bad := txpowerIgnoredBy[driver]; bad {
		return why
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.txIgnored[driver]
}

// noteTxPowerIgnored records a driver that took a level and did not apply it.
//
// KEYED BY DRIVER, not by interface: the fault is in the code that handles the
// request, so a second adapter on the same driver has it too, and one probe
// that cost a client nothing should not have to be repeated on every radio.
func (e *Engine) noteTxPowerIgnored(driver, why string) {
	if driver == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.txIgnored == nil {
		e.txIgnored = map[string]string{}
	}
	e.txIgnored[driver] = why
}

// txSettleDelay is how long the driver is given to apply a level before it is
// read back. Measured on the Cudy's mt798x: the new level reads back on the
// next command, with no delay at all. This is slack, not a requirement.
const txSettleDelay = 300 * time.Millisecond

// txTolerance is how far the radio may land from what was asked and still
// count as having done it: the driver rounds to its own step and the
// regulatory table it clamps against is in whole dBm. The interface uses the
// same figure.
const txTolerance = 1.0

// readTxPower reports iface's transmit power, or nil when iw has nothing to
// say about it (a scanner that is down, a wired port).
func (e *Engine) readTxPower(iface, driver string) *TxPower {
	raw, err := exec.Command("iw", "dev", iface, "info").Output()
	if err != nil {
		return nil
	}
	dbm, ok := parseTxPower(string(raw))
	if !ok {
		return nil
	}
	tp := &TxPower{DBm: dbm, Settable: true}
	if why := e.txIgnoredReason(driver); why != "" {
		tp.Settable, tp.Why = false, why
	}
	if phy, err := phyName(iface); err == nil {
		if info, err := exec.Command("iw", "phy", phy, "info").Output(); err == nil {
			tp.MaxDBm = channelMaxDBm(string(info), operatingFreq(iface))
		}
	}
	return tp
}

// parseTxPower reads "txpower 23.00 dBm" out of `iw dev <if> info`.
func parseTxPower(devInfo string) (float64, bool) {
	for _, line := range strings.Split(devInfo, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "txpower" && f[2] == "dBm" {
			v, err := strconv.ParseFloat(f[1], 64)
			return v, err == nil
		}
	}
	return 0, false
}

// channelMaxDBm reads the limit for freqMHz out of `iw phy <phy> info`, whose
// channel lines look like "* 5200.0 MHz [40] (23.0 dBm)".
func channelMaxDBm(phyInfo string, freqMHz int) float64 {
	if freqMHz == 0 {
		return 0
	}
	for _, line := range strings.Split(phyInfo, "\n") {
		t := strings.TrimSpace(line)
		var mhz float64
		if _, err := fmt.Sscanf(t, "* %f MHz", &mhz); err != nil || int(mhz) != freqMHz {
			continue
		}
		open, end := strings.Index(t, "("), strings.Index(t, " dBm)")
		if open < 0 || end < open {
			return 0
		}
		v, err := strconv.ParseFloat(t[open+1:end], 64)
		if err != nil {
			return 0
		}
		return v
	}
	return 0
}

// SetTxPower sets the transmit power of the radio behind iface, live.
//
// dbm < 0 hands control back to the driver (`auto`), which is the only honest
// "off": the default is whatever the driver and regulatory domain decide, not
// a number this box picked.
func (e *Engine) SetTxPower(iface string, dbm float64) error {
	if err := e.radioExists(iface); err != nil {
		return err
	}
	if e.cfg.Demo {
		return nil
	}
	driver := Radio(iface).Driver
	if why := e.txIgnoredReason(driver); why != "" {
		return fmt.Errorf("%s: %s", iface, why)
	}
	phy, err := phyName(iface)
	if err != nil {
		return err
	}
	args := []string{"phy", phy, "set", "txpower", "auto"}
	if dbm >= 0 {
		if dbm > 30 {
			return fmt.Errorf("%.0f dBm is above any regulatory limit this radio could have", dbm)
		}
		args = []string{"phy", phy, "set", "txpower", "fixed", strconv.Itoa(int(math.Round(dbm * 100)))}
	}
	if out, err := exec.Command("iw", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("iw %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	// VERIFIED, NOT ASSUMED. `iw` exits 0 on a driver that discards the level
	// -- that is exactly how the mt7921u behaves (#202) -- so success here says
	// only that the request was accepted. Reading the level back is the whole
	// difference between a control that works and one that looks like it does.
	if dbm >= 0 {
		time.Sleep(txSettleDelay)
		back, _ := exec.Command("iw", "dev", iface, "info").Output()
		got, ok := parseTxPower(string(back))
		if ok && math.Abs(got-dbm) > txTolerance {
			why := fmt.Sprintf("the %s driver ignores transmit power: asked for %.0f dBm, "+
				"it reports %.2f", driver, dbm, got)
			e.noteTxPowerIgnored(driver, why)
			e.logEvent(EventRadio, iface, "", "%s: %s — the control is disabled for this driver "+
				"until the daemon restarts", iface, why)
			return fmt.Errorf("%s: %s", iface, why)
		}
	}
	if e.cfg.OpenWrt {
		// The next `wifi reload` or LuCI save applies wireless.radioN.txpower
		// over whatever is live, so the file has to agree -- the same reason
		// channel moves are written there. Logged, not fatal: the power IS
		// set, and only its survival across a reload is in doubt.
		if err := persistTxPowerUCI(iface, dbm); err != nil {
			e.logEvent(EventRadio, iface, "", "%s: transmit power set, but not recorded in UCI, "+
				"so the next wifi reload undoes it: %v", iface, err)
		}
	}
	if dbm < 0 {
		e.logEvent(EventRadio, iface, "", "%s transmit power back to the driver's default", iface)
	} else {
		e.logEvent(EventRadio, iface, "", "%s conditioned: transmit power %.0f dBm", iface, dbm)
	}
	return nil
}

// persistTxPowerUCI records a transmit power in OpenWrt's wireless config,
// committed without a reload, as persistChannelUCI does for channels. A
// negative dbm removes the option, which is UCI's way of saying "the driver
// decides".
func persistTxPowerUCI(iface string, dbm float64) error {
	raw, err := exec.Command("ubus", "call", "network.wireless", "status").Output()
	if err != nil {
		return fmt.Errorf("reading network.wireless status: %w", err)
	}
	radio, err := uciRadioFor(raw, iface)
	if err != nil {
		return err
	}
	opt := "wireless." + radio + ".txpower"
	var out []byte
	if dbm < 0 {
		out, err = exec.Command("uci", "-q", "delete", opt).CombinedOutput()
		// -q delete of an option that was never set exits 1; that is the
		// state being asked for, not a failure.
		if err != nil && strings.TrimSpace(string(out)) == "" {
			err = nil
		}
	} else {
		out, err = exec.Command("uci", "set", opt+"="+strconv.Itoa(int(math.Round(dbm)))).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("uci %s: %v: %s", opt, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("uci", "commit", "wireless").CombinedOutput(); err != nil {
		return fmt.Errorf("uci commit wireless: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
