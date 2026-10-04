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

/*
 * Turning a radio into a listen-only instrument, and back.
 *
 * WHY IT IS NOT JUST A SETTING. A scanner is a radio that serves nobody and
 * sweeps continuously, so the serving radios never leave their channel. boa
 * already understands one -- ScanPorts, reconcileRoles, raiseScanners -- but
 * only as configuration read at startup, written by hand into two files.
 *
 * THE AWKWARD PART, measured on a Cudy TR3000 2026-09-22. OpenWrt owns the
 * radios: disabling a wifi-iface removes its netdev, and a scan needs one, so
 * "stop serving" and "start listening" are two acts rather than one switch:
 *
 *	uci set wireless.default_radioN.disabled=1   # phy3-ap0 disappears
 *	iw phy phy3 interface add phy3-scan type managed
 *	ip link set phy3-scan up                      # 22 BSSes on the first sweep
 *
 * Measured with it: an AP and a managed interface may coexist on these phys
 * (`#{ managed } <= 19, #{ AP } <= 16` on mt798x, `<= 2` and `<= 1` on
 * mt7921u) but both carry `#channels <= 1`, so a second interface cannot sit
 * on another channel while the AP serves. That is why a scanner needs the AP
 * OFF rather than a listening interface beside it -- and why the box's current
 * behaviour, sweeping with a serving radio, takes that radio off channel.
 *
 * Measured too: an interface made this way SURVIVES `wifi reload`, because
 * netifd leaves interfaces it did not create alone. It does not survive a
 * reboot, so ensureScanIfaces recreates it at startup -- otherwise the setting
 * outlives the interface it names, which is the silent half-working state this
 * repository keeps finding.
 */

// scanIfaceFor is the listen-only interface this box makes on a phy. Named
// after the phy so the pairing is legible from the name alone, and so
// ensureScanIfaces can rebuild it from the name after a reboot.
func scanIfaceFor(phy string) string { return phy + "-scan" }

// phyOfScanIface is the inverse, and returns "" for a name this box did not
// make -- a scan port named in the config by hand is left alone.
func phyOfScanIface(iface string) string {
	phy, ok := strings.CutSuffix(iface, "-scan")
	if !ok || !strings.HasPrefix(phy, "phy") {
		return ""
	}
	return phy
}

// ensureScanIfaces recreates the listen-only interfaces this box made, which a
// reboot takes with it.
//
// Best effort and loud: a scanner that is configured and absent is a box that
// will never sweep, and the reason has to reach the log rather than showing up
// as an empty neighbourhood.
func (e *Engine) ensureScanIfaces() {
	if e.cfg.Demo {
		return
	}
	for _, s := range e.cfg.ScanPorts {
		if s == "" || LinkExists(s) {
			continue
		}
		phy := phyOfScanIface(s)
		if phy == "" {
			e.logEvent(EventWarning, s, "",
				"the listen-only radio %s does not exist and is not named after a "+
					"phy, so it cannot be recreated: name it <phy>-scan, or create it", s)
			continue
		}
		if out, err := exec.Command("iw", "phy", phy, "interface", "add", s, "type", "managed").CombinedOutput(); err != nil {
			e.logEvent(EventWarning, s, "", "could not recreate the listen-only radio %s on %s: %v: %s",
				s, phy, err, strings.TrimSpace(string(out)))
			continue
		}
		// UP, OR IT SWEEPS NOTHING. `iw interface add` leaves the netdev
		// administratively down, and a down interface cannot scan -- so this
		// created the instrument and left it deaf. MEASURED on a Pi 5
		// 2026-09-29, straight out of the wizard: the rack showed phy0-scan as
		// the scanner, `up: false`, and the neighbourhood stayed empty with
		// nothing reporting why.
		//
		// rebuildMissingScanIfaces, the timer path, has always done this; only
		// the startup path did not. It stayed hidden because every other way of
		// making a scanner -- SetRadioRole from the button or the API -- brings
		// the link up itself. The wizard is the first that relies on this one.
		_ = exec.Command("ip", "link", "set", s, "up").Run()
		e.logEvent(EventRadio, s, "", "%s recreated on %s: listen-only, and a reboot does not keep one", s, phy)
	}
	e.warnOrphanScanIfaces()
}

// rebuildMissingScanIfaces is ensureScanIfaces on a timer: it recreates a
// listen-only interface whose phy is THERE, and says nothing when it is not.
//
// Quiet on purpose, and only on the repeating path. A radio that has genuinely
// gone -- unplugged, or back at a different index -- would otherwise repeat the
// same warning every couple of minutes for as long as it stayed away, which
// teaches an operator to scroll past the log rather than read it. That absence
// is reported instead as a notice on the snapshot, which states a condition
// rather than appending a line, so it is visible the whole time it is true and
// gone the moment it is not.
//
// It exists because a hotplug does not restart the daemon: measured on a Cudy
// TR3000, 2026-09-25, boad kept the same pid across an unplug and a replug, so
// nothing reached ensureScanIfaces and the box silently stopped sweeping. See
// #387.
func (e *Engine) rebuildMissingScanIfaces() {
	if e.cfg.Demo {
		return
	}
	for _, s := range e.cfg.ScanPorts {
		if s == "" || LinkExists(s) || e.scanIsRetired(s) {
			continue
		}
		phy := phyOfScanIface(s)
		if phy == "" || !phyExists(phy) {
			continue
		}
		if out, err := exec.Command("iw", "phy", phy, "interface", "add", s, "type", "managed").CombinedOutput(); err != nil {
			e.logEvent(EventWarning, s, "", "could not rebuild the listen-only radio %s on %s: %v: %s",
				s, phy, err, strings.TrimSpace(string(out)))
			continue
		}
		_ = exec.Command("ip", "link", "set", s, "up").Run()
		e.logEvent(EventRadio, s, "", "%s rebuilt on %s: the radio came back and a listen-only interface does not survive with it", s, phy)
	}
}

// phyExists reports whether a radio is present under that name right now.
func phyExists(phy string) bool {
	_, err := os.Stat(filepath.Join("/sys/class/ieee80211", phy))
	return err == nil
}

// missingScanPorts are the listen-only radios that are configured and absent.
//
// The condition behind the notice. A box in this state keeps serving and
// conditioning -- nothing an operator would notice breaks -- and quietly stops
// sweeping for free, taking a serving radio off channel for about 1.3s per
// survey instead, with the neighbourhood view empty. Measured 2026-09-25: the
// only thing on the box that reported it was `boa-setup check`, from a shell.
func (e *Engine) missingScanPorts() []string {
	return missingPorts(e.cfg.ScanPorts, LinkExists)
}

// missingPorts is that rule as a value, so it can be tested without a box to
// run `ip link` on -- the same reason scanPortsAfter above is separate. The
// daemon builds on macOS, where LinkExists answers false for everything
// because there is no `ip`, so a test that called it directly would assert the
// platform rather than the rule.
func missingPorts(ports []string, exists func(string) bool) []string {
	var out []string
	for _, s := range ports {
		if s != "" && !exists(s) {
			out = append(out, s)
		}
	}
	return out
}

// warnOrphanScanIfaces names a listen-only interface this box made and no
// longer claims.
//
// The other direction of the same disagreement ensureScanIfaces fixes, and it
// is NOT fixed here: the interface is harmless, and deleting one somebody made
// on purpose would be this box tidying away hardware it does not own. What is
// harmful is the silence -- an orphan reads as an ordinary radio in the rack,
// down, not serving, offering every access point control there is. So it is
// said once, at the start, in terms that name the repair.
func (e *Engine) warnOrphanScanIfaces() {
	claimed := map[string]bool{}
	for _, s := range e.cfg.ScanPorts {
		claimed[s] = true
	}
	ents, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return
	}
	for _, ent := range ents {
		name := ent.Name()
		phy := phyOfScanIface(name)
		if claimed[name] || phy == "" {
			continue
		}
		// LEFT OVER FROM A SERVE, SO TAKEN AWAY. A listen-only interface boa
		// no longer claims, on a radio that is serving an access point, is the
		// residue of that radio being switched back to serving -- not hardware
		// somebody made on purpose, because the radio it sits on is already
		// doing another job. Deleted, and said so, rather than warned about and
		// left for the operator to type `iw dev ... del`.
		if orphanIsLeftOverFromServe(phy, name, phyServesAP(phy, name)) {
			if out, err := exec.Command("iw", "dev", name, "del").CombinedOutput(); err != nil {
				e.logEvent(EventWarning, name, "",
					"%s is left over from %s being switched to serve, and could not be removed: %v: %s",
					name, phy, err, strings.TrimSpace(string(out)))
			} else {
				e.logEvent(EventRadio, name, "",
					"%s removed: left over from %s being switched back to serving, and nothing listens on it",
					name, phy)
			}
			continue
		}
		want := strings.TrimSpace(strings.Join(e.cfg.ScanPorts, " ") + " " + name)
		e.logEvent(EventWarning, name, "",
			"%s is a listen-only interface this box made and no longer watches, so it "+
				"shows in the rack as a radio serving nobody. Watch it again with "+
				"`uci set boa.main.scan='%s' && uci commit boa && /etc/init.d/boa restart`, "+
				"or take it away with `iw dev %s del`",
			name, want, name)
	}
}

// SetRadioRole makes a radio listen-only, or gives it back to OpenWrt to serve.
//
// It changes two configurations that have to agree -- OpenWrt's wireless, which
// owns the access point, and boa's own, which owns what the daemon treats as an
// instrument -- and then the daemon re-reads its ports. ScanPorts is read at
// startup, so the honest way to apply it is a restart of the service: about a
// second, policies survive it, and no client is touched by it.
func (e *Engine) SetRadioRole(iface string, scanner bool) (string, error) {
	if !e.cfg.OpenWrt {
		return "", fmt.Errorf("changing a radio's role needs OpenWrt's wireless config; " +
			"on the image, name the radio with -scan and restart")
	}
	if e.cfg.Demo {
		return iface, nil
	}
	phy, err := phyName(iface)
	if err != nil {
		return "", err
	}
	radio, err := uciRadioForPhy(iface, phy)
	if err != nil {
		return "", err
	}
	// The wifi-iface section, which is what carries `disabled`. netifd names
	// it after the radio, and `wifi config` has generated it that way on every
	// box this has been run on; asked of uci rather than assumed.
	sec, err := uciWifiIfaceFor(radio)
	if err != nil {
		return "", err
	}

	scan := scanIfaceFor(phy)
	if scanner {
		if err := uciSet("wireless." + sec + ".disabled=1"); err != nil {
			return "", err
		}
		if err := uciCommit("wireless"); err != nil {
			return "", err
		}
		// Reload so the access point actually goes, then make the instrument.
		if out, err := exec.Command("wifi", "reload").CombinedOutput(); err != nil {
			return "", fmt.Errorf("wifi reload: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if !LinkExists(scan) {
			if out, err := exec.Command("iw", "phy", phy, "interface", "add", scan, "type", "managed").CombinedOutput(); err != nil {
				return "", fmt.Errorf("creating %s on %s: %v: %s", scan, phy, err, strings.TrimSpace(string(out)))
			}
		}
		_ = exec.Command("ip", "link", "set", scan, "up").Run()
		if err := setScanPort(scan, true); err != nil {
			return "", err
		}
		if err := setScanMAC(phyMAC(phy), true); err != nil {
			return "", err
		}
	} else {
		if err := uciSet("wireless." + sec + ".disabled=0"); err != nil {
			return "", err
		}
		// A RADIO COMING BACK TO SERVING NEEDS A CHANNEL IT CAN USE.
		//
		// MEASURED on a Pi 5 2026-09-28. The onboard brcmfmac served happily on
		// ch36, assigned by the wizard. Made a scanner, plan-channels then
		// skipped it -- "serves no access point" -- and its channel fell back to
		// `auto`. Brought back to serving, automatic selection chose channel 34,
		// an 802.11j channel invalid under country=US, and the firmware refused
		// it:
		//
		//	brcmf_cfg80211_start_ap: Set Channel failed: chspec=53282, -52
		//
		// The interface existed, uci said it should serve, and nothing beaconed.
		// No error reached the operator. One `uci set ...channel=36` brought it
		// up instantly. Issue #429.
		//
		// plan-channels cannot cover this: it works from the radios that are
		// ALREADY serving, so a radio being brought INTO service is exactly the
		// case it cannot see.
		if err := ensureServingChannel(radio); err != nil {
			return "", err
		}
		if err := uciCommit("wireless"); err != nil {
			return "", err
		}
		// RETIRED BEFORE IT IS DELETED. The rebuild timer recreates any
		// listening interface in ScanPorts that is missing, and ScanPorts is
		// only re-read by the restart that ends this function -- seconds away,
		// past a wifi reload and a wait for the access point. Seen on the Cudy
		// 2026-10-04: a radio set to serve came back with both phy3-ap0 and a
		// phy3-scan beside it, which the rack showed as a radio serving nobody.
		e.retireScan(scan)
		if LinkExists(scan) {
			if out, err := exec.Command("iw", "dev", scan, "del").CombinedOutput(); err != nil {
				return "", fmt.Errorf("removing %s: %v: %s", scan, err, strings.TrimSpace(string(out)))
			}
		}
		if err := setScanPort(scan, false); err != nil {
			return "", err
		}
		if err := setScanMAC(phyMAC(phy), false); err != nil {
			return "", err
		}
		// COMMIT BEFORE THE SLOW PART, so there is never a window where uci
		// holds staged-but-uncommitted changes. Waiting for an access point
		// takes seconds, and this daemon can be killed during them -- the role
		// change ends by spawning a detached restart, and a second request
		// arriving while that is pending dies with it. Observed on the Pi,
		// 2026-09-29: the wireless config was committed as serving while the
		// boa deletions were still staged, so /etc/config/boa went on naming
		// the radio a scanner and it reverted on the next read. Committing
		// here means the worst case is a config that is merely WRONG, not one
		// that disagrees with itself.
		if err := uciCommit("boa"); err != nil {
			return "", err
		}
		if out, err := exec.Command("wifi", "reload").CombinedOutput(); err != nil {
			return "", fmt.Errorf("wifi reload: %v: %s", err, strings.TrimSpace(string(out)))
		}
		// SAY SO WHEN IT DOES NOT COME UP.
		//
		// `wifi reload` succeeds whether or not the access point starts, so a
		// radio that the driver refuses looks identical to one that worked:
		// the netdev is there, uci says it serves, and nothing beacons.
		// MEASURED on a Pi 5 2026-09-28, where an illegal channel left exactly
		// that state and the operator had no way to know. Issue #429.
		if !apCameUpOn(phy) {
			// PUT IT BACK, because a half-applied change is worse than a
			// refused one. Reporting the failure and returning here left the
			// wireless config committed as SERVING while the boa config's
			// deletions were still staged, so /etc/config/boa went on naming
			// this radio a scanner: the box had a radio that served nobody,
			// listened to nobody, and came back as a scanner on the next read.
			// Observed on the Pi, 2026-09-29, by an operator who pressed the
			// button and watched the row never populate and then revert.
			band, ch := uciGet("wireless."+radio+".band"), uciGet("wireless."+radio+".channel")
			_ = uciSet("wireless." + sec + ".disabled=1")
			_ = uciCommit("wireless")
			_ = setScanPort(scan, true)
			_ = setScanMAC(phyMAC(phy), true)
			_ = uciCommit("boa")
			_ = exec.Command("wifi", "reload").Run()
			if !LinkExists(scan) {
				_ = exec.Command("iw", "phy", phy, "interface", "add", scan, "type", "managed").Run()
			}
			_ = exec.Command("ip", "link", "set", scan, "up").Run()
			return "", fmt.Errorf("%s was set to serve but no access point came up on %s, "+
				"so it has been left listening: the driver may have refused its channel "+
				"(band %s, channel %s) -- check `logread` for start_ap failures",
				iface, phy, band, ch)
		}
		// NOW GIVE IT A CHANNEL THAT DOES NOT CLASH.
		//
		// ensureServingChannel above only rescues a radio with no usable
		// channel at all, and it does that from the bottom of the band because
		// it cannot see the other radios. On a box already serving, that is a
		// collision: MEASURED 2026-09-29, a radio promoted here came up on 36
		// beside one on 40 at 80MHz -- the same 36-48 block, completely
		// overlapping -- and the operator had to move it by hand.
		//
		// plan-channels is what knows the allocation, and it works from the
		// radios that are SERVING, so it has to run after the access point is
		// actually up. That is why it is here and not beside
		// ensureServingChannel. It reloads Wi-Fi and associated clients
		// reconnect, which is the right trade at the moment a radio is being
		// brought into service: the alternative is leaving two radios on one
		// channel until somebody notices.
		if out, err := exec.Command("/usr/sbin/boa-setup", "plan-channels").CombinedOutput(); err != nil {
			// Not fatal: the radio IS serving, which is what was asked. A
			// clash is worth saying out loud rather than failing over.
			e.logEvent(EventWarning, iface, "",
				"%s is serving, but planning its channel failed, so it may share one "+
					"with another radio: %v: %s", iface, err, strings.TrimSpace(string(out)))
		}
	}
	if err := uciCommit("boa"); err != nil {
		return "", err
	}
	if scanner {
		e.logEvent(EventRadio, iface, "", "%s is listening only now, as %s: it serves nobody "+
			"and sweeps continuously, so no serving radio has to leave its channel", iface, scan)
	} else {
		e.logEvent(EventRadio, iface, "", "%s is serving again: the listen-only interface is gone, "+
			"and the sweep goes back to whichever radio can take it", iface)
	}
	// The ports are read at startup, so the change lands on a restart -- and
	// the restart has to OUTLIVE this process.
	//
	// SETSID, and that is the whole point. The first version spawned the
	// helper with start-stop-daemon, which left it a child of boad in boad'''s
	// process group; procd stops a service by killing that group, so the
	// helper died in the same signal that stopped the daemon and the restart
	// never ran. MEASURED on the Cudy: the config said scan=phy0-scan while
	// the daemon carried on with its old arguments, which is the worst of both
	// -- a box configured one way and behaving another, with the interface
	// reporting the behaviour.
	//
	// A new session survives that. The second between spawning and restarting
	// is for this reply to reach the browser.
	_ = exec.Command("setsid", "sh", "-c", "sleep 1; /etc/init.d/boa restart").Start()
	if scanner {
		return scan, nil
	}
	// The name it serves under now, not the one it was asked by: a radio
	// promoted from phy3-scan serves as phy3-ap0, and the reply said
	// "now": "phy3-scan" -- the interface this function had just deleted.
	if ap := apIfaceOn(phy, scan); ap != "" {
		return ap, nil
	}
	return iface, nil
}

/*
 * boa.main.scan IS A LIST, and treating it as a single value is how making one
 * radio listen-only silently un-made another.
 *
 * MEASURED on the Cudy TR3000, 2026-09-22: phy0 had been listening for hours;
 * turning phy3 listen-only from the rack left `boa.main.scan=phy3-scan` and
 * nothing else. The phy0-scan interface was still there and still worked -- a
 * hand-run scan on it returned BSSes -- but the daemon no longer knew it was an
 * instrument, so the row drew it as an ordinary access point and offered
 * deauth, disassoc, steer, evict and gather on a BSS that does not exist. The
 * delete was the same mistake pointed the other way: taking ONE radio back to
 * serving removed every scanner from the config.
 *
 * The daemon has always accepted several -- main.go hands the flag to
 * SplitPorts, which splits on commas and spaces -- so only the writer was
 * wrong. See issue #351.
 */

// setScanPort adds or removes one port in boa.main.scan, leaving the others
// alone. Removing the last one deletes the option, which is what "no scanners"
// looks like in UCI.
func setScanPort(port string, want bool) error {
	have := SplitPorts(uciGet("boa.main.scan"))
	next := scanPortsAfter(have, port, want)
	if len(next) == 0 {
		return uciDelete("boa.main.scan")
	}
	return uciSet("boa.main.scan=" + strings.Join(next, " "))
}

// apCameUpOn waits briefly for an access point to actually start on a phy.
//
// ASKED OF THE PHY, NOT AN INTERFACE NAME. The first version of this checked
// the interface the REQUEST named, which on the way back from scanner is the
// listen-only interface being deleted -- so it asked after something that was
// supposed to be gone, and would have named the wrong thing even when it
// fired. netifd also picks the AP's name itself, so the only thing this code
// can be sure of is which radio it asked to serve.
//
// Carrier is the signal: an AP netdev exists as soon as netifd makes it, and
// only gains carrier once hostapd is beaconing on it. Measured on the Pi, a
// refused channel leaves the interface UP with NO-CARRIER indefinitely.
//
// The wait is generous because a radio that surveys before it settles takes a
// few seconds, and being wrong in the impatient direction would report a
// working radio as broken.
func apCameUpOn(phy string) bool {
	for i := 0; i < 20; i++ {
		if phyHasCarrier(phy) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// phyHasCarrier reports whether any netdev on a phy is carrying -- which for
// an AP means beaconing.
func phyHasCarrier(phy string) bool {
	ents, err := os.ReadDir(filepath.Join("/sys/class/ieee80211", phy, "device", "net"))
	if err != nil {
		return false
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join("/sys/class/net", ent.Name(), "carrier"))
		if err == nil && strings.TrimSpace(string(b)) == "1" {
			return true
		}
	}
	return false
}

// ensureServingChannel gives a radio an explicit channel when it has none it
// can use, so an access point does not depend on automatic selection choosing
// legally. It leaves a channel that is already set alone -- a deliberate one
// is not ours to overwrite -- and only fills in `auto`, empty, or a channel
// from the wrong band.
//
// The defaults are the bottom of each band rather than anything clever:
// plan-channels redistributes once the radio is up and can be seen serving,
// and its job is to avoid collisions, not to rescue an illegal channel.
func ensureServingChannel(radio string) error {
	want, need := servingChannelFor(
		uciGet("wireless."+radio+".band"),
		uciGet("wireless."+radio+".channel"))
	if !need {
		return nil
	}
	return uciSet("wireless." + radio + ".channel=" + strconv.Itoa(want))
}

// servingChannelFor is that decision as a value, so the rule can be tested
// without a box to run uci on -- as scanPortsAfter is.
//
// Reports the channel to write and whether one is needed at all. A channel
// already set and legal for the band is left alone; a deliberate choice is not
// ours to overwrite.
//
// IT CHECKS THE BAND, NOT THE REGULATORY DOMAIN, and the difference is the bug
// that prompted it: channel 34 is a perfectly real 5GHz channel (5170 MHz,
// 802.11j) which this leaves alone, and which the Pi's firmware refused under
// country=US. Encoding regulatory rules here would be guessing at a table the
// kernel already holds. What this removes is the `auto` that let an illegal
// channel be CHOSEN; what catches one anyway is apCameUp, which makes the
// failure loud instead of silent.
func servingChannelFor(band, channel string) (int, bool) {
	var want int
	switch band {
	case "2g":
		want = 6
	case "5g":
		want = 36
	default:
		// 6g, or a band this does not know. Leave it: guessing a channel for a
		// band whose rules are not encoded here is how channel 34 happened.
		return 0, false
	}

	ch := strings.TrimSpace(channel)
	if ch != "" && ch != "auto" && ch != "0" {
		if n, err := strconv.Atoi(ch); err == nil {
			freq := freqForChannel(n)
			if (band == "2g" && freq >= 2400 && freq < 2500) ||
				(band == "5g" && freq >= 5000 && freq < 5900) {
				return 0, false // already usable
			}
			// A channel from the wrong band, which a reload refuses. Seen on
			// this Pi: band '5g' with channel '1'.
		}
	}
	return want, true
}

/*
 * AND THE SAME LIST AGAIN, AS HARDWARE.
 *
 * boa.main.scan holds interface NAMES, and phy indices renumber whenever USB
 * radios come and go: MEASURED on a Pi 5 2026-09-28, one reboot moved the
 * onboard brcmfmac from phy0 to phy1 and gave phy0 to a USB mt7921u, so a
 * saved `phy0-scan` came back meaning the DONGLE. boa then built its
 * listen-only interface on a radio that was supposed to serve, and no access
 * point came up at all. Issue #387.
 *
 * So the same set is kept a second time as MACs, which are the hardware, and
 * /etc/init.d/boa re-derives the names from them before boad is started -- see
 * /lib/boa/radio.sh. A SET rather than a list parallel to boa.main.scan: two
 * lists that have to stay aligned by index is a bug waiting for the first
 * radio that fails to resolve.
 *
 * This was half-shipped at first: boa-setup and the hotplug hook wrote the MAC
 * and this did not, so a scanner set from the wizard survived a renumber and
 * one set from the UI button did not, with nothing to tell an operator which
 * they had. Issue #428.
 */

// phyMAC is a phy's permanent address, lower case, or "" when it cannot be
// read -- in which case the name-only behaviour is what is left, which is
// what this box did before the MAC was recorded at all.
func phyMAC(phy string) string {
	b, err := os.ReadFile(filepath.Join("/sys/class/ieee80211", phy, "macaddress"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(b)))
}

// setScanMAC adds or removes one MAC in boa.main.scan_mac, the same edit
// setScanPort makes to the names.
func setScanMAC(mac string, want bool) error {
	if mac == "" {
		return nil
	}
	have := SplitPorts(uciGet("boa.main.scan_mac"))
	next := scanPortsAfter(have, mac, want)
	if len(next) == 0 {
		return uciDelete("boa.main.scan_mac")
	}
	return uciSet("boa.main.scan_mac=" + strings.Join(next, " "))
}

// scanPortsAfter is that edit as a value, so the rule can be tested without a
// box to run uci on.
//
// Order is preserved and duplicates are dropped: the list is read back at every
// start, and a port named twice would have the daemon poll one radio twice
// while reporting it once.
func scanPortsAfter(have []string, port string, want bool) []string {
	out := make([]string, 0, len(have)+1)
	seen := map[string]bool{}
	for _, p := range have {
		if p == "" || seen[p] || (p == port && !want) {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if want && !seen[port] && port != "" {
		out = append(out, port)
	}
	return out
}

// uciGet reads one option, empty for anything unset. Errors are the same
// answer as unset here: an option that cannot be read is one this box is about
// to write.
func uciGet(key string) string {
	out, err := exec.Command("uci", "-q", "get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// uciRadioForPhy finds the wifi-device behind an interface, by two routes.
//
// NETIFD FIRST, because it is authoritative while it owns the interface: it
// says which wifi-device produced phy1-ap0, and OpenWrt names APs after the phy
// while the section number follows whatever `wifi config` generated, so the two
// agree only by coincidence.
//
// AND SYSFS SECOND, which is the whole reason this exists. A listen-only
// interface was made by this box, not by netifd, so netifd has never heard of
// it -- and its radio's access point is disabled, so nothing else of that
// radio's is in netifd's status either. Asking only netifd made the way BACK
// from listen-only impossible: measured on the Cudy, "no wifi-device in
// network.wireless serves phy3-scan", with the radio stuck listening.
//
// THE SUFFIX IS NOT DECORATION, and getting that wrong is how this went from a
// missing feature to a dangerous one. Two phys can share one device path --
// measured on the Cudy, whose 2.4GHz and 5GHz chains are both
// platform/soc/18000000.wifi -- and UCI tells them apart with `+N`, the phy's
// ordinal on that device:
//
//	wireless.radio0.path='platform/soc/18000000.wifi'
//	wireless.radio1.path='platform/soc/18000000.wifi+1'
//
// A match on the path alone therefore resolved phy1 to radio0: the revert
// re-enabled a radio that was already serving and left phy1 disabled, and the
// same mistake in the other direction would have taken the WRONG radio off the
// air. So the ordinal is computed from sysfs -- the phys on that device, in
// index order -- and compared with the suffix. No match, or two, is an error
// rather than a guess.
func uciRadioForPhy(iface, phy string) (string, error) {
	if raw, err := exec.Command("ubus", "call", "network.wireless", "status").Output(); err == nil {
		if radio, err := uciRadioFor(raw, iface); err == nil {
			return radio, nil
		}
	}
	dev, err := os.Readlink("/sys/class/ieee80211/" + phy + "/device")
	if err != nil {
		return "", fmt.Errorf("no wifi-device serves %s, and %s has no device path: %w",
			iface, phy, err)
	}
	want := filepath.Base(dev)
	ord, err := phyOrdinalOnDevice(phy, want)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("uci", "show", "wireless").Output()
	if err != nil {
		return "", fmt.Errorf("reading wireless config: %w", err)
	}
	var hits []string
	for _, line := range strings.Split(string(out), "\n") {
		name, val, ok := strings.Cut(line, "=")
		if !ok || !strings.HasSuffix(name, ".path") {
			continue
		}
		base, n := splitUCIPath(strings.Trim(val, "'"))
		if filepath.Base(base) != want || n != ord {
			continue
		}
		hits = append(hits, strings.TrimPrefix(strings.TrimSuffix(name, ".path"), "wireless."))
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no wifi-device in the wireless config is %s (%s, ordinal %d)",
			phy, want, ord)
	default:
		return "", fmt.Errorf("%d wifi-devices claim %s (%s, ordinal %d): %s -- refusing to guess",
			len(hits), phy, want, ord, strings.Join(hits, ", "))
	}
}

// splitUCIPath separates a radio's device path from the `+N` that says which
// phy on that device it is. No suffix is ordinal 0.
func splitUCIPath(path string) (string, int) {
	base, num, ok := strings.Cut(path, "+")
	if !ok {
		return path, 0
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return path, 0
	}
	return base, n
}

// phyOrdinalOnDevice is where this phy sits among the phys of one device, in
// phy index order -- which is what UCI's `+N` counts.
func phyOrdinalOnDevice(phy, device string) (int, error) {
	ents, err := os.ReadDir("/sys/class/ieee80211")
	if err != nil {
		return 0, fmt.Errorf("listing phys: %w", err)
	}
	var sharing []string
	for _, e := range ents {
		dev, err := os.Readlink("/sys/class/ieee80211/" + e.Name() + "/device")
		if err != nil || filepath.Base(dev) != device {
			continue
		}
		sharing = append(sharing, e.Name())
	}
	// Index order, not name order: phy10 must not sort before phy2.
	sort.Slice(sharing, func(a, b int) bool {
		return phyIndex(sharing[a]) < phyIndex(sharing[b])
	})
	for i, name := range sharing {
		if name == phy {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%s is not among the phys on %s", phy, device)
}

// phyIndex reads the kernel's own number for a phy, falling back to the digits
// in its name.
func phyIndex(phy string) int {
	if b, err := os.ReadFile("/sys/class/ieee80211/" + phy + "/index"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			return n
		}
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(phy, "phy"))
	return n
}

// uciWifiIfaceFor finds the wifi-iface section serving a wifi-device.
func uciWifiIfaceFor(radio string) (string, error) {
	out, err := exec.Command("uci", "show", "wireless").Output()
	if err != nil {
		return "", fmt.Errorf("reading wireless config: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, val, ok := strings.Cut(line, "=")
		if !ok || !strings.HasSuffix(name, ".device") {
			continue
		}
		if strings.Trim(val, "'") != radio {
			continue
		}
		return strings.TrimPrefix(strings.TrimSuffix(name, ".device"), "wireless."), nil
	}
	return "", fmt.Errorf("no wifi-iface in the wireless config serves %s", radio)
}

func uciSet(kv string) error {
	if out, err := exec.Command("uci", "set", kv).CombinedOutput(); err != nil {
		return fmt.Errorf("uci set %s: %v: %s", kv, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uciDelete(key string) error {
	out, err := exec.Command("uci", "-q", "delete", key).CombinedOutput()
	// -q delete of something unset exits 1 with no output: that is the state
	// being asked for, not a failure.
	if err != nil && strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("uci delete %s: %v: %s", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uciCommit(pkg string) error {
	if out, err := exec.Command("uci", "commit", pkg).CombinedOutput(); err != nil {
		return fmt.Errorf("uci commit %s: %v: %s", pkg, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// retireScan marks a listen-only interface as taken away by a role change, so
// the rebuild timer leaves it gone until the restart re-reads ScanPorts.
func (e *Engine) retireScan(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scanRetired == nil {
		e.scanRetired = map[string]bool{}
	}
	e.scanRetired[name] = true
}

// scanIsRetired reports whether a role change has taken this interface away.
func (e *Engine) scanIsRetired(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.scanRetired[name]
}

// orphanIsLeftOverFromServe is the rule for deleting an unclaimed listen-only
// interface: only when the radio it sits on also serves an access point. An
// orphan on a radio doing nothing else may be somebody's on purpose, and is
// still only warned about.
func orphanIsLeftOverFromServe(phy, name string, servesAP bool) bool {
	return phy != "" && phyOfScanIface(name) == phy && servesAP
}

// phyServesAP reports whether any interface on phy other than skip is an
// access point, read from the kernel rather than from uci, since a stale
// interface is exactly the case where the two disagree.
func phyServesAP(phy, skip string) bool { return apIfaceOn(phy, skip) != "" }

// apIfaceOn names an access-point interface on phy other than skip, or "".
func apIfaceOn(phy, skip string) string {
	ents, err := os.ReadDir(filepath.Join("/sys/class/ieee80211", phy, "device", "net"))
	if err != nil {
		return ""
	}
	for _, ent := range ents {
		name := ent.Name()
		if name == skip {
			continue
		}
		out, err := exec.Command("iw", "dev", name, "info").Output()
		if err == nil && strings.Contains(string(out), "type AP") {
			return name
		}
	}
	return ""
}
