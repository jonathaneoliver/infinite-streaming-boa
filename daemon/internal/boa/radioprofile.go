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

// PHY and power-save impairments, and steering a client from one radio to the
// other.
//
// These were the last of issue #122's groups B and C, and they were blocked on
// what looked like a hard constraint: changing them means changing hostapd's
// configuration, and the daemon cannot write /etc under ProtectSystem=strict.
//
// It turns out not to need the file. hostapd's control interface takes SET for
// the same parameters the config file carries, and DISABLE/ENABLE restarts the
// BSS with them applied -- the identical trick the channel scan uses to move
// the AP on hardware that refuses CHAN_SWITCH. No file write, no systemctl, no
// unit change, no reflash.
//
// Two of these do not even need that: RTS and fragmentation thresholds are phy
// properties set through `iw`, and take effect on the next frame with nothing
// restarted and nobody dropped.

// The IEEE "threshold disabled" encodings. Named because the numbers are
// meaningless otherwise, and because the word "off" is not portable across
// these two drivers -- see SetPhyThreshold.
const (
	fragDisabled = 2346 // largest MSDU
	rtsDisabled  = 2347 // largest frame
)

// phyName returns the phy behind an interface, e.g. "phy1". `iw phy <name> set`
// wants the phy, not the interface, and the two are not interchangeable.
func phyName(iface string) (string, error) {
	n := strings.TrimSpace(readSysfs("/sys/class/net/" + iface + "/phy80211/name"))
	if n == "" {
		return "", fmt.Errorf("%s has no phy: it may not be a wireless interface", iface)
	}
	return n, nil
}

// --- thresholds -----------------------------------------------------------

// SetPhyThreshold sets the RTS or fragmentation threshold on a radio.
//
// Live, and the only impairment in this file that costs nothing: no BSS
// restart, no client dropped, effective on the next frame.
//
//	rts 0     -- RTS/CTS before EVERY frame. Roughly halves throughput and adds
//	             two control frames of latency per data frame. Exactly what a
//	             real access point does in a dense environment, and the airtime
//	             cost is genuine rather than simulated.
//	frag 256  -- every frame fragmented. With any error rate at all the retry
//	             cost explodes superlinearly, because losing one fragment costs
//	             the whole frame.
//
// A value of 0 for rts means "every frame"; -1 (or "off") disables both.
func (e *Engine) SetPhyThreshold(iface, kind string, val int) error {
	if kind != "rts" && kind != "frag" {
		return fmt.Errorf("threshold must be rts or frag (got %q)", kind)
	}
	if err := e.radioExists(iface); err != nil {
		return err
	}
	if e.cfg.Demo {
		return nil
	}
	phy, err := phyName(iface)
	if err != nil {
		return err
	}
	// Disabling is NUMERIC, not the word "off".
	//
	// Measured on both radios 2026-09-03: `iw phy phy0 set frag off` fails on
	// brcmfmac with "command failed: Invalid exchange (-52)" and the threshold
	// stays where it was, while the identical command succeeds on mt7921u. So
	// on the onboard radio fragmentation could be turned ON and never off --
	// an impairment with no way back, which is the worst kind to ship.
	//
	// The IEEE "disabled" encodings are accepted by both: 2346 is the largest
	// MSDU and 2347 the largest frame, so a threshold there can never be
	// reached and the feature is off. Verified by reading the value back.
	if val < 0 {
		val = rtsDisabled
		if kind == "frag" {
			val = fragDisabled
		}
	}
	arg := strconv.Itoa(val)
	out, err := exec.Command("iw", "phy", phy, "set", kind, arg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iw phy %s set %s %s: %v: %s",
			phy, kind, arg, err, strings.TrimSpace(string(out)))
	}
	if val >= fragDisabled {
		e.logEvent(EventRadio, iface, "", "%s %s threshold off", iface, kind)
	} else {
		e.logEvent(EventRadio, iface, "", "%s conditioned: %s threshold %d bytes", iface, kind, val)
	}
	return nil
}

// --- profiles -------------------------------------------------------------

// radioProfile is a named set of hostapd parameters applied together.
//
// Named rather than a pile of individual knobs because these only mean anything
// as combinations -- "802.11n only" is three settings that have to agree, and a
// half-applied one is a radio in a state nobody chose.
type radioProfile struct {
	Name string
	Desc string
	// Sets are hostapd SET commands, applied while the BSS is disabled.
	Sets []string
	// Restart says the BSS has to be torn down and rebuilt for these to take,
	// which drops every client on the radio.
	Restart bool
}

// runningConfigFor finds the hostapd config file actually in force for an
// interface, and returns its key/value pairs.
//
// Through the RUNNING processes rather than by guessing at a filename: two
// config files can name the same interface -- boa-onboard.conf and
// boa-onboard24.conf both say "interface=wlan0", for the solo and dual-band
// cases -- so picking by name would restore whichever happened to sort first
// and could put a radio on the wrong band.
func runningConfigFor(iface string) map[string]string {
	paths, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(args) == 0 || !strings.HasSuffix(args[0], "hostapd") {
			continue
		}
		for _, a := range args[1:] {
			if !strings.HasSuffix(a, ".conf") {
				continue
			}
			b, err := os.ReadFile(a)
			if err != nil {
				continue
			}
			kv := map[string]string{}
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				if k, v, ok := strings.Cut(line, "="); ok {
					kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			if kv["interface"] == iface {
				return kv
			}
		}
	}
	return nil
}

// cleanSetsFor builds the "clean" profile for a SPECIFIC radio, from the config
// the image actually gave it.
//
// One hardcoded set was wrong, and visibly so: it applied ieee80211ax=1 to both
// radios, but the image configures the onboard chip as 802.11n only (ac=0,
// ax=0) because that is all brcmfmac can do at 20MHz. Restoring "clean" there
// therefore left the card claiming a 20MHz 802.11n radio was 802.11ax -- the
// interface asserting something the hardware cannot do, which is exactly the
// confident wrongness this codebase exists to avoid.
//
// Falls back to the timing parameters alone when the config cannot be read, so
// clean still undoes a power-save profile rather than doing nothing.
func cleanSetsFor(iface string) []string {
	sets := []string{
		"SET beacon_int 100", "SET dtim_period 2",
		"SET uapsd_advertisement_enabled 1",
	}
	kv := runningConfigFor(iface)
	if kv == nil {
		return sets
	}
	for _, k := range []string{"ieee80211n", "ieee80211ac", "ieee80211ax"} {
		if v, ok := kv[k]; ok {
			sets = append(sets, "SET "+k+" "+v)
		}
	}
	// Width too: "narrow" clears these, and only the config knows whether this
	// radio was ever wide.
	for _, k := range []string{"vht_oper_chwidth", "he_oper_chwidth"} {
		v := kv[k]
		if v == "" {
			v = "0"
		}
		sets = append(sets, "SET "+k+" "+v)
	}
	if bi := kv["beacon_int"]; bi != "" {
		sets[0] = "SET beacon_int " + bi
	}
	if dp := kv["dtim_period"]; dp != "" {
		sets[1] = "SET dtim_period " + dp
	}
	return sets
}

// radioProfiles is the closed set. Every one of them is reversible by applying
// "clean", which is why that exists as a profile rather than as a reset button
// somewhere else.
//
// clean carries no Sets: they are built per radio by cleanSetsFor, because what
// "as the image configured it" means differs between the two radios.
var radioProfiles = map[string]radioProfile{
	"clean": {
		Name: "clean", Restart: true,
		Desc: "Everything back to how the image configured THIS radio.",
	},
	"legacy": {
		Name: "legacy", Restart: true,
		Desc: "802.11n only -- no ac, no ax. Drops the ceiling to what an older " +
			"device sees, with real MAC-layer cost rather than a rate limit.",
		Sets: []string{"SET ieee80211ax 0", "SET ieee80211ac 0", "SET ieee80211n 1"},
	},
	"narrow": {
		Name: "narrow", Restart: true,
		Desc: "20MHz. A quarter of the spectrum, so airtime contention is real " +
			"and shared rather than imposed per client.",
		Sets: []string{
			"SET vht_oper_chwidth 0", "SET he_oper_chwidth 0",
			"SET secondary_channel 0",
		},
	},
	"dozy": {
		Name: "dozy", Restart: true,
		Desc: "DTIM 10 at a 300ms beacon interval, and U-APSD off. A dozing " +
			"phone then waits up to three seconds for buffered downlink, which " +
			"draws a comb of periodic spikes no netem delay distribution can.",
		Sets: []string{
			"SET beacon_int 300", "SET dtim_period 10",
			"SET uapsd_advertisement_enabled 0",
		},
	},
}

// RadioProfileNames lists the profiles, sorted, with "clean" first because it
// is the way back from every other one.
func RadioProfileNames() []string {
	var out []string
	for n := range radioProfiles {
		if n != "clean" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return append([]string{"clean"}, out...)
}

// ApplyRadioProfile pushes a profile onto a radio, restarting its BSS.
//
// Every client on the radio is dropped: the parameters here are advertised in
// the beacon and negotiated at association, so an associated station cannot be
// told about them. That is why these live on the Bridge tab rather than a
// device card -- they are not per-device impairments and cannot be made into
// them, which is the collision issue #122 describes.
func (e *Engine) ApplyRadioProfile(iface, name string) (int, error) {
	p, ok := radioProfiles[name]
	if !ok {
		return 0, fmt.Errorf("unknown profile %q; have %s",
			name, strings.Join(RadioProfileNames(), ", "))
	}
	if err := e.radioReady(iface); err != nil {
		return 0, err
	}
	dropped := len(StationDump(iface))
	if e.cfg.Demo {
		e.noteProfile(iface, name, dropped)
		return dropped, nil
	}

	wasEnabled := false
	if st, err := hostapdCmd(iface, "STATUS"); err == nil {
		wasEnabled = strings.Contains(st, "state=ENABLED")
	}
	if wasEnabled {
		if _, err := hostapdCmd(iface, "DISABLE"); err != nil {
			return 0, fmt.Errorf("could not take %s down to reconfigure it: %w", iface, err)
		}
	}
	// Whatever happens to the SETs below, the radio comes back. A profile that
	// failed half way and left the AP down would turn an impairment into an
	// outage nobody asked for.
	defer func() {
		if wasEnabled {
			e.reenableAP(iface)
		}
	}()

	sets := p.Sets
	if name == "clean" {
		sets = cleanSetsFor(iface)
	}
	var refused []string
	for _, cmd := range sets {
		reply, err := hostapdCmd(iface, cmd)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", cmd, err)
		}
		if !strings.HasPrefix(reply, "OK") {
			// Collected rather than fatal: hostapd refuses parameters its
			// driver cannot do, and a profile that applied four of five
			// settings is worth having as long as it SAYS so.
			refused = append(refused, cmd)
		}
	}
	e.forgetRadioOn() // width and mode may have changed
	if len(refused) > 0 {
		e.logEvent(EventWarning, iface, "",
			"%s applied profile %q but refused %d of %d settings",
			iface, name, len(refused), len(sets))
		return dropped, fmt.Errorf(
			"applied, but this radio refused %d of %d settings: %s",
			len(refused), len(sets), strings.Join(refused, "; "))
	}
	e.noteProfile(iface, name, dropped)
	e.syncRadioState(iface)
	return dropped, nil
}

// --- steering -------------------------------------------------------------

// btmCommand builds an 802.11v BSS Transition Management request.
//
// Pure and separately tested: the neighbour report is five comma-separated
// fields in a fixed order, and hostapd rejects the whole command on any one of
// them being wrong. Getting the operating class wrong is the easy mistake --
// it encodes the band AND the width, so a 5GHz neighbour advertised with a
// 2.4GHz class is a request the client will ignore rather than refuse.
func btmCommand(mac, bssid string, channel, widthMHz int, disassocSec int) string {
	// bssid_info: reachable, plus the capability bits a client checks before
	// bothering. 0x0000040f is the conventional value in hostapd's own
	// examples and means "preauth, spectrum management, QoS, APSD, radio
	// measurement, reachable".
	const bssidInfo = "0x0000040f"
	op, reportCh, phy := opClassAndPhy(channel, widthMHz)
	parts := []string{
		"BSS_TM_REQ", mac,
		"pref=1",
		// abridged: everything not listed is implicitly less preferred, which
		// is what makes a single-neighbour list mean "go here" rather than
		// "here is one option among the ones you already know".
		"abridged=1",
		"disassoc_imminent=1",
		fmt.Sprintf("disassoc_timer=%d", disassocSec),
		fmt.Sprintf("neighbor=%s,%s,%d,%d,%d", bssid, bssidInfo, op, reportCh, phy),
	}
	return strings.Join(parts, " ")
}

// opClassAndPhy maps a channel and width to the 802.11 global operating class,
// the channel number that class expects, and the PHY type a neighbour report
// carries.
//
// The class is not a decoration. A client matches the neighbour report against
// what its own scan can see, and a report naming a channel the class does not
// contain describes a BSS that does not exist -- which the client ignores,
// silently and indistinguishably from a refusal.
//
// MEASURED 2026-09-04. Steering to wlan-usb on 149 at 80MHz sent
// `128,149`: class 128 is the 80MHz class, whose channel field is the channel
// CENTRE index (155 for the 149-161 block), so this described an 80MHz block
// centred on 149 -- 5735-5815 MHz, which is not a block that exists. No client
// ever moved off the onboard radio, whose only destination is that 5GHz one.
// Steering the other way sent `81,6`, which is correct, and is the one steer
// observed to move a client here.
//
// The classes are fixed assignments from IEEE 802.11 Annex E. What decides
// them is which 80MHz block a channel is in and which side its 40MHz secondary
// sits -- both already in apChannels as Center80 and SecOffset, so they are
// read from there rather than restated. Getting them from the same table
// hostapd is configured from is what stops the neighbour report describing a
// radio other than the one running.
//
//	 81  2.4GHz, 20MHz
//	115  5GHz 20MHz, UNII-1 (36-48)
//	116  5GHz 40MHz, UNII-1, primary lower (36, 44)
//	117  5GHz 40MHz, UNII-1, primary upper (40, 48)
//	124  5GHz 20MHz, UNII-3 (149-161)
//	125  5GHz 20MHz, UNII-3 extended (149-169); 165 lives here
//	126  5GHz 40MHz, UNII-3, primary lower (149, 157)
//	127  5GHz 40MHz, UNII-3, primary upper (153, 161)
//	128  80MHz, any band -- CHANNEL FIELD IS THE CENTRE INDEX
func opClassAndPhy(channel, widthMHz int) (opClass, reportChannel, phyType int) {
	if channel <= 14 {
		// 2.4GHz: 81 is the 20MHz class. HT is the most a client will find
		// there on this hardware.
		return 81, channel, 7
	}
	ch, known := apChannels[channel]
	if widthMHz >= 80 && known && ch.Center80 != 0 {
		// The one class whose channel field is not the primary. Naming the
		// primary here is what made every 5GHz steer describe a block that
		// does not exist.
		return 128, channelForFreq(ch.Center80), 9
	}
	// 165 has no 40MHz partner and no 80MHz block, so it can only be 20MHz --
	// and only class 125 contains it.
	if channel == 165 {
		return 125, channel, 9
	}
	unii3 := channel >= 149
	if widthMHz >= 40 && known && ch.SecOffset != 0 {
		lower := ch.SecOffset > 0 // the secondary sits above, so this is the lower half
		switch {
		case unii3 && lower:
			return 126, channel, 9
		case unii3:
			return 127, channel, 9
		case lower:
			return 116, channel, 9
		default:
			return 117, channel, 9
		}
	}
	if unii3 {
		return 124, channel, 9
	}
	return 115, channel, 9
}

// SteerClient asks one client to move to another radio.
//
// A REQUEST, not an instruction: 802.11v leaves the decision with the client,
// and a phone perfectly entitled to ignore it will. That is the point of having
// it -- whether a given device honours a steer is exactly the behaviour worth
// testing, and it cannot be discovered from anything but a real request.
//
// disassoc_imminent is set, so a client that ignores the suggestion is
// disassociated when the timer expires and has to make its own choice. Without
// it a stubborn client simply stays and the test has no outcome either way.
func (e *Engine) SteerClient(mac, fromIface, toIface string) error {
	m := normMAC(mac)
	if !validMAC(m) {
		return fmt.Errorf("not a MAC address: %s", mac)
	}
	if err := e.radioReady(fromIface); err != nil {
		return err
	}
	if err := e.radioReady(toIface); err != nil {
		return fmt.Errorf("no radio to steer to: %w", err)
	}
	if fromIface == toIface {
		return fmt.Errorf("cannot steer %s to the radio it is already on", m)
	}
	if e.cfg.Demo {
		e.noteSteer(m, fromIface, toIface)
		return nil
	}

	// The target's own BSSID and channel, asked of the target rather than
	// assumed. A neighbour report naming the wrong BSSID is one a client
	// silently ignores, which is indistinguishable from a client that refused.
	st, err := hostapdCmd(toIface, "STATUS")
	if err != nil {
		return fmt.Errorf("could not read %s to describe it: %w", toIface, err)
	}
	kv := parseHostapdKV(st)
	bssid, channel := kv["bssid[0]"], atoiSafe(kv["channel"])
	if bssid == "" || channel == 0 {
		return fmt.Errorf("%s is not serving an access point to steer to", toIface)
	}

	cmd := btmCommand(m, bssid, channel, apWidth(kv), 30)
	reply, err := hostapdCmd(fromIface, cmd)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return fmt.Errorf(
			"hostapd rejected the transition request: %s. The radio may lack "+
				"802.11v support, or the client may not have advertised it",
			strings.TrimSpace(reply))
	}
	// Armed BEFORE the event is logged, so an answer that arrives in the same
	// millisecond has something to be matched against. The client's reply is
	// asynchronous and comes back through the monitor connection, not as a
	// reply to this command -- see hostapdmonitor.go.
	e.notePendingSteer(m, fromIface, e.describeRadio(toIface))
	e.noteSteer(m, fromIface, toIface)
	return nil
}

// SteerAll asks every client on one radio to move to the other. Returns how
// many were asked; how many actually went is a question only the Clients tab
// can answer, a few seconds later.
func (e *Engine) SteerAll(fromIface, toIface string) (int, error) {
	if err := e.radioReady(fromIface); err != nil {
		return 0, err
	}
	stations := StationDump(fromIface)
	asked := 0
	var firstErr error
	for mac := range stations {
		if err := e.SteerClient(mac, fromIface, toIface); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		asked++
	}
	if asked == 0 && firstErr != nil {
		return 0, firstErr
	}
	return asked, nil
}

// OtherRadio names the radio a client could be steered to: another watched
// radio that is serving an access point. Empty when there is nowhere to go,
// which is the single-radio case and is why this button did not exist before.
func (e *Engine) OtherRadio(from string) string {
	for _, w := range e.cfg.WlanPorts {
		if w == from {
			continue
		}
		if e.cfg.Demo {
			return w
		}
		if hostapdAvailable(w) {
			return w
		}
	}
	return ""
}
