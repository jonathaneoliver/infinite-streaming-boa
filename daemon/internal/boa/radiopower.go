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
		// Coming back is not merely the inverse of going away. hostapd may have
		// torn its BSS down when the radio disappeared, and an unblocked radio
		// with no beacons is a box that looks powered and serves nobody --
		// precisely the silent failure this codebase keeps being bitten by. Ask
		// hostapd to bring the BSS up again; harmless if it already has.
		e.reenableAP(iface)
	}
	return nil
}

// reenableAP nudges hostapd to serve again after its radio comes back.
// Best-effort and idempotent: ENABLE on an already-enabled BSS is refused
// harmlessly, and a radio with no hostapd has nothing to re-enable.
func (e *Engine) reenableAP(iface string) {
	if !hostapdAvailable(iface) {
		return
	}
	// A moment for the driver to finish bringing the phy back; ENABLE against a
	// phy that is still unblocking fails, and then nothing retries.
	time.Sleep(500 * time.Millisecond)
	if st, err := hostapdCmd(iface, "STATUS"); err == nil {
		if strings.Contains(st, "state=ENABLED") {
			return
		}
	}
	if _, err := hostapdCmd(iface, "ENABLE"); err != nil {
		fmt.Printf("infinite-streaming-boa: re-enable AP on %s: %v\n", iface, err)
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
	// OutageSec is how long the radio was actually off serving clients, so the
	// cost of the answer is reported alongside it.
	OutageSec float64 `json:"outage_sec"`
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

	if wasEnabled {
		if reply, err := hostapdCmd(iface, "DISABLE"); err != nil {
			return ScanResult{}, fmt.Errorf("could not take %s out of service to scan: %w", iface, err)
		} else if !strings.HasPrefix(reply, "OK") {
			return ScanResult{}, fmt.Errorf("hostapd refused to disable %s: %s",
				iface, strings.TrimSpace(reply))
		}
	}
	// The AP goes back up whatever happens below, including a scan that fails or
	// panics. Leaving a radio disabled because a scan errored would turn a
	// diagnostic into an outage nobody asked for.
	defer func() {
		if wasEnabled {
			e.reenableAP(iface)
		}
	}()

	raw, err := exec.Command("iw", "dev", iface, "scan").Output()
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan on %s failed: %w", iface, err)
	}

	aps := parseScan(string(raw))
	for i := range aps {
		aps[i].Ours = ourBSSIDs[strings.ToLower(aps[i].BSSID)]
	}
	res := summariseScan(iface, aps)
	res.Was, res.Now = wasChannel, wasChannel

	// Move it while it is still down. Once ENABLE has run the only way to
	// change channel is CHAN_SWITCH, which this driver refuses.
	if apply && res.Best != 0 && res.Best != wasChannel {
		if ch, ok := apChannels[res.Best]; ok {
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
			if moved {
				res.Applied, res.Now = true, res.Best
			}
		}
	}

	res.OutageSec = time.Since(started).Seconds()
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
			cur.FreqMHz = atoiSafe(surveyValue(t))
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
func summariseScan(iface string, aps []ScanAP) ScanResult {
	byChan := map[int]*ScanChannel{}
	band := ""
	for _, a := range aps {
		if a.Ours || a.Channel == 0 {
			continue
		}
		if band == "" {
			band = bandOf(a.FreqMHz)
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
	out.Note = "A real scan: this is what other people's access points are doing, " +
		"which is the thing a channel choice actually depends on and the thing " +
		"the airtime counters cannot see. The radio was taken out of service to " +
		"take it."
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
		// 20MHz: no secondary, no centre. Clear them, or a radio moving down
		// from 80MHz keeps a centre frequency that no longer contains it.
		return append(cmds,
			"SET vht_oper_chwidth 0",
			"SET he_oper_chwidth 0",
			"SET secondary_channel 0",
		)
	}
	cmds = append(cmds, "SET secondary_channel "+strconv.Itoa(ch.SecOffset))
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
