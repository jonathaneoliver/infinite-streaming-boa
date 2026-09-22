package boa

import (
	"fmt"
	"os/exec"
	"strings"
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
		e.logEvent(EventRadio, s, "", "%s recreated on %s: listen-only, and a reboot does not keep one", s, phy)
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
	raw, err := exec.Command("ubus", "call", "network.wireless", "status").Output()
	if err != nil {
		return "", fmt.Errorf("reading network.wireless status: %w", err)
	}
	radio, err := uciRadioFor(raw, iface)
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
		if err := uciSet("boa.main.scan=" + scan); err != nil {
			return "", err
		}
	} else {
		if err := uciSet("wireless." + sec + ".disabled=0"); err != nil {
			return "", err
		}
		if err := uciCommit("wireless"); err != nil {
			return "", err
		}
		if LinkExists(scan) {
			if out, err := exec.Command("iw", "dev", scan, "del").CombinedOutput(); err != nil {
				return "", fmt.Errorf("removing %s: %v: %s", scan, err, strings.TrimSpace(string(out)))
			}
		}
		if err := uciDelete("boa.main.scan"); err != nil {
			return "", err
		}
		if out, err := exec.Command("wifi", "reload").CombinedOutput(); err != nil {
			return "", fmt.Errorf("wifi reload: %v: %s", err, strings.TrimSpace(string(out)))
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
	// The ports are read at startup. Detached, and after the reply: procd
	// restarts the service while this process is being replaced.
	_ = exec.Command("start-stop-daemon", "-S", "-b", "-x", "/bin/sh", "--",
		"-c", "sleep 1; /etc/init.d/boa restart").Run()
	if scanner {
		return scan, nil
	}
	return iface, nil
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
