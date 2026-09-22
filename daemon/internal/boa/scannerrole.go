package boa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	return iface, nil
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
