package boa

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Box-wide radio controls: airtime survey, channel switch, and clearing every
// station off a radio.
//
// These are AP-WIDE, which is what separates them from the per-client link
// events in hostapd.go. A channel switch moves every associated client at once
// and a broadcast deauthentication drops all of them, so nothing here belongs
// on a device card -- see issue #122's "the collision". The interface's job is
// to say so before the button is pressed; this file's job is to refuse loudly
// rather than half-succeed.
//
// Everything goes through hostapd's control socket, the transport already built
// in hostapd.go, so no new machinery is needed and a radio without a socket
// simply reports that it has none.

// apChannel is one channel the access point is permitted to use.
//
// Deliberately a closed table rather than an arithmetic conversion. The
// frequency reaches hostapd as a bare word in a socket message -- the same
// exposure validMAC exists to close -- and the non-DFS restriction is a
// regulatory fact, not a preference: the Pi cannot act as an AP on a DFS
// channel, so offering one produces a radio that refuses to start. Mirrors the
// allowlist in build.sh and .env.example.
type apChannel struct {
	Channel int
	FreqMHz int
	// SecOffset is which side of the primary a 40MHz secondary channel sits:
	// +1 above, -1 below. Naming the wrong side is the same failure as naming
	// no side -- hostapd refuses with an error about the kernel driver that
	// says nothing about the cause.
	SecOffset int
	// Center80 is the centre frequency of the 80MHz block containing this
	// channel. 5210 MHz (channel 42) covers all four permitted 5GHz channels.
	// Zero on 2.4GHz, where 80MHz does not exist.
	Center80 int
}

// apChannels is every channel the AP may be moved to. 2.4GHz is 1/6/11 because
// they are the only non-overlapping trio; 5GHz is the four non-DFS channels.
var apChannels = map[int]apChannel{
	1:  {Channel: 1, FreqMHz: 2412},
	6:  {Channel: 6, FreqMHz: 2437},
	11: {Channel: 11, FreqMHz: 2462},
	36: {Channel: 36, FreqMHz: 5180, SecOffset: 1, Center80: 5210},
	40: {Channel: 40, FreqMHz: 5200, SecOffset: -1, Center80: 5210},
	44: {Channel: 44, FreqMHz: 5220, SecOffset: 1, Center80: 5210},
	48: {Channel: 48, FreqMHz: 5240, SecOffset: -1, Center80: 5210},
}

// is24 reports whether a channel is in the 2.4GHz band, where neither 40MHz
// (antisocial in a crowded band) nor 80MHz (does not exist) is offered.
func (c apChannel) is24() bool { return c.FreqMHz < 3000 }

// chanSwitchCount is how many beacons of warning clients get. Five is hostapd's
// own common default: long enough that a client has seen the announcement in at
// least one beacon, short enough that the gap is not itself the impairment.
const chanSwitchCount = 5

// chanSwitchCommand builds the CHAN_SWITCH control verb.
//
// Pure and separately tested, because every argument here is one hostapd
// rejects wholesale rather than partially: a missing center_freq1 on an 80MHz
// switch, or a secondary offset naming the wrong side, fails the whole command.
func chanSwitchCommand(ch apChannel, widthMHz int) (string, error) {
	if ch.FreqMHz == 0 {
		return "", fmt.Errorf("unknown channel")
	}
	switch widthMHz {
	case 20, 40, 80:
	default:
		return "", fmt.Errorf("width must be 20, 40 or 80 MHz (got %d)", widthMHz)
	}
	if ch.is24() && widthMHz != 20 {
		return "", fmt.Errorf(
			"channel %d is 2.4GHz, where only 20MHz is offered (asked for %d)",
			ch.Channel, widthMHz)
	}

	parts := []string{"CHAN_SWITCH", fmt.Sprint(chanSwitchCount), fmt.Sprint(ch.FreqMHz)}
	switch widthMHz {
	case 80:
		parts = append(parts,
			fmt.Sprintf("center_freq1=%d", ch.Center80),
			"bandwidth=80",
			fmt.Sprintf("sec_channel_offset=%d", ch.SecOffset),
			"vht", "he")
	case 40:
		parts = append(parts,
			"bandwidth=40",
			fmt.Sprintf("sec_channel_offset=%d", ch.SecOffset),
			"ht")
	case 20:
		parts = append(parts, "bandwidth=20", "ht")
	}
	return strings.Join(parts, " "), nil
}

// ChanSwitch moves a radio, and every client on it, to another channel.
//
// This is the 802.11h channel switch announcement: clients are TOLD to move and
// follow without disassociating -- in theory. Whether a given driver and a given
// phone honour it is exactly what this button exists to find out, so a refusal
// from hostapd is returned rather than swallowed.
func (e *Engine) ChanSwitch(iface string, channel, widthMHz int) error {
	if err := e.radioReady(iface); err != nil {
		return err
	}
	ch, ok := apChannels[channel]
	if !ok {
		return fmt.Errorf(
			"channel %d is not offered: 2.4GHz 1/6/11, 5GHz 36/40/44/48 "+
				"(DFS channels are excluded -- the Pi cannot serve an AP on one)",
			channel)
	}
	cmd, err := chanSwitchCommand(ch, widthMHz)
	if err != nil {
		return err
	}
	if e.cfg.Demo {
		return nil
	}
	reply, err := hostapdCmd(iface, cmd)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return fmt.Errorf("hostapd rejected %q: %s", cmd, strings.TrimSpace(reply))
	}
	return nil
}

// DeauthAll drops every station on a radio at once. They reassociate on their
// own; this is the AP-wide sibling of the per-client drop in hostapd.go.
func (e *Engine) DeauthAll(iface string) (int, error) {
	if err := e.radioReady(iface); err != nil {
		return 0, err
	}
	n := len(StationDump(iface))
	if e.cfg.Demo {
		return n, nil
	}
	reply, err := hostapdCmd(iface, "DEAUTHENTICATE ff:ff:ff:ff:ff:ff")
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(reply, "OK") {
		return 0, fmt.Errorf("hostapd rejected the broadcast deauthentication: %s",
			strings.TrimSpace(reply))
	}
	return n, nil
}

// radioReady is the capability gate every action shares. It names the specific
// reason rather than returning a bare false: "unavailable" with no cause is the
// message that sends someone to read the source.
func (e *Engine) radioReady(iface string) error {
	if iface == "" {
		return fmt.Errorf("no radio named")
	}
	if e.cfg.Demo {
		return nil
	}
	if !LinkExists(iface) {
		return fmt.Errorf("no interface named %s on this box", iface)
	}
	if !hostapdAvailable(iface) {
		return fmt.Errorf(
			"hostapd is not serving %s, so it exposes no control socket "+
				"(the radio may be idle, or run by NetworkManager)", iface)
	}
	return nil
}

// --- airtime survey ------------------------------------------------------

// SurveyChannel is one channel's airtime, as a fraction of the time the radio
// has actually spent on it.
type SurveyChannel struct {
	// FreqMHz is the OPERATING frequency, taken from `iw dev <if> info` rather
	// than from the survey block's own label. See ReportedFreqMHz.
	FreqMHz int `json:"freq_mhz"`
	// ReportedFreqMHz is the frequency `iw survey dump` attached to this block,
	// kept only so a disagreement is visible instead of silently resolved.
	//
	// Measured on mt7921u 2026-09-03: the AP was on 5200 MHz and the one
	// populated block was labelled 5955 MHz -- a 6GHz channel the radio has
	// never tuned to. iw maps the driver's survey INDEX onto its own channel
	// enumeration and on this driver they do not line up. The airtime is real
	// (it matched the AP's uptime to within 21 seconds); only the label is
	// wrong. See Source L.
	ReportedFreqMHz int `json:"reported_freq_mhz,omitempty"`
	// Totals are monotonic since the interface came up, in ms.
	ActiveMs   int64 `json:"active_ms"`
	BusyMs     int64 `json:"busy_ms"`
	ReceiveMs  int64 `json:"receive_ms"`
	TransmitMs int64 `json:"transmit_ms"`
	// BusyPct is derived from the DELTA since the previous call, not from the
	// totals, so it describes the last few seconds rather than an average over
	// the whole uptime of the access point. Absent on the first call, when
	// there is no previous sample to difference against.
	BusyPct *float64 `json:"busy_pct,omitempty"`
	DeltaMs int64    `json:"delta_active_ms,omitempty"`
}

// SurveyResult is one radio's airtime reading.
type SurveyResult struct {
	Iface string `json:"iface"`
	// OperatingFreqMHz is what the radio is actually tuned to, from
	// `iw dev <if> info`. Authoritative; the survey's own labels are not.
	OperatingFreqMHz int             `json:"operating_freq_mhz,omitempty"`
	Channels         []SurveyChannel `json:"channels"`
	// Mislabelled records that the driver attributed the airtime to a
	// frequency the radio is not on. Surfaced rather than quietly corrected:
	// silently rewriting a driver's output is how a wrong reading becomes
	// invisible when the driver is later fixed.
	Mislabelled bool `json:"mislabelled,omitempty"`
	// Note is always populated. `iw survey dump` prints a block for every
	// channel the phy knows and this is emphatically NOT a band scan; saying so
	// in the payload keeps the caller from ranking a table of zeroes.
	Note string `json:"note"`
}

// surveySample remembers one channel's totals so the next call can difference
// against them.
type surveySample struct {
	at     time.Time
	totals map[int]SurveyChannel
}

var (
	surveyMu   sync.Mutex
	surveyPrev = map[string]surveySample{}
)

// Survey reads a radio's airtime counters.
//
// Two semantics from Source L, both measured and both capable of producing a
// confident wrong answer:
//
//  1. The command lists EVERY channel the phy knows -- 98 of them on a tri-band
//     adapter -- but a beaconing radio never visits any but its operating one,
//     so exactly one block carries data. Blocks with no active time are dropped
//     here; a caller ranking the raw output by busy time would sort the
//     never-visited channels to the top and recommend one of them.
//
//  2. The frequency LABEL on the populated block is wrong on mt7921u. The
//     airtime is the operating channel's, so the operating frequency is taken
//     from `iw dev <if> info` and the driver's own label is carried alongside
//     rather than trusted or discarded.
func (e *Engine) Survey(iface string) (SurveyResult, error) {
	if iface == "" {
		return SurveyResult{}, fmt.Errorf("no radio named")
	}
	if !e.cfg.Demo && !LinkExists(iface) {
		return SurveyResult{}, fmt.Errorf("no interface named %s on this box", iface)
	}
	raw, err := exec.Command("iw", "dev", iface, "survey", "dump").Output()
	if err != nil {
		return SurveyResult{}, fmt.Errorf("iw survey dump on %s: %w", iface, err)
	}
	chans := parseSurvey(string(raw))
	opFreq := operatingFreq(iface)

	mislabelled := false
	for i := range chans {
		chans[i].ReportedFreqMHz = chans[i].FreqMHz
		if opFreq > 0 {
			if chans[i].FreqMHz != opFreq {
				mislabelled = true
			}
			chans[i].FreqMHz = opFreq
		}
	}

	now := time.Now()
	surveyMu.Lock()
	prev, hadPrev := surveyPrev[iface]
	cur := surveySample{at: now, totals: map[int]SurveyChannel{}}
	for _, c := range chans {
		// Keyed on the DRIVER's label, which is stable across calls even when
		// it is wrong. Keying on the corrected frequency would collide the
		// moment two blocks were populated.
		cur.totals[c.ReportedFreqMHz] = c
	}
	surveyPrev[iface] = cur
	surveyMu.Unlock()

	if hadPrev {
		for i := range chans {
			p, ok := prev.totals[chans[i].ReportedFreqMHz]
			if !ok {
				continue
			}
			dActive := chans[i].ActiveMs - p.ActiveMs
			dBusy := chans[i].BusyMs - p.BusyMs
			// A counter that went backwards means the interface restarted and
			// the epoch is gone. Report nothing rather than a negative
			// percentage, which is the sort of number that gets believed.
			if dActive <= 0 || dBusy < 0 {
				continue
			}
			pct := float64(dBusy) / float64(dActive) * 100
			chans[i].BusyPct = &pct
			chans[i].DeltaMs = dActive
		}
	}

	note := "Airtime for the operating channel only. `iw survey dump` lists " +
		"every channel the adapter knows, but a radio that is beaconing never " +
		"visits the others, so their counters read zero and are omitted here. " +
		"Choosing a quieter channel needs a radio that is not serving."
	if mislabelled {
		note += " This driver labelled the airtime with a frequency the radio " +
			"is not on; the operating frequency here comes from `iw dev info` " +
			"instead, and the driver's own label is kept as reported_freq_mhz."
	}
	return SurveyResult{
		Iface:            iface,
		OperatingFreqMHz: opFreq,
		Channels:         chans,
		Mislabelled:      mislabelled,
		Note:             note,
	}, nil
}

// parseSurvey turns `iw dev <if> survey dump` into per-channel readings,
// keeping only channels the radio has actually spent time on.
func parseSurvey(raw string) []SurveyChannel {
	var out []SurveyChannel
	var cur *SurveyChannel
	flush := func() {
		// Only a channel with active time has been visited. Everything else is
		// a placeholder block, not a measurement of an idle channel.
		if cur != nil && cur.ActiveMs > 0 {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Survey data from"):
			flush()
			cur = &SurveyChannel{}
		case cur == nil:
			// Stray line before the first block header; nothing to attach it to.
		case strings.HasPrefix(t, "frequency:"):
			// Recorded, not believed. On mt7921u this label does not match the
			// channel the radio is on -- see the note on ReportedFreqMHz. There
			// is no "[in use]" marker on this driver either, which is why the
			// operating channel is identified by having non-zero airtime rather
			// than by anything iw says about it.
			cur.FreqMHz = atoiSafe(surveyValue(t))
		case strings.HasPrefix(t, "channel active time:"):
			cur.ActiveMs = int64(atoiSafe(surveyValue(t)))
		case strings.HasPrefix(t, "channel busy time:"):
			cur.BusyMs = int64(atoiSafe(surveyValue(t)))
		case strings.HasPrefix(t, "channel receive time:"):
			cur.ReceiveMs = int64(atoiSafe(surveyValue(t)))
		case strings.HasPrefix(t, "channel transmit time:"):
			cur.TransmitMs = int64(atoiSafe(surveyValue(t)))
		}
	}
	flush()
	return out
}

// operatingFreq reads the frequency a radio is actually tuned to.
//
// From `iw dev <if> info`, which is authoritative and agrees with hostapd
// STATUS -- unlike `survey dump`'s per-block labels. Returns 0 when the
// interface is down or the command is unavailable, in which case the survey
// carries the driver's labels unaltered and says so.
func operatingFreq(iface string) int {
	raw, err := exec.Command("iw", "dev", iface, "info").Output()
	if err != nil {
		return 0
	}
	// "	channel 40 (5200 MHz), width: 80 MHz, center1: 5210 MHz"
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "channel ") {
			continue
		}
		open := strings.Index(t, "(")
		if open < 0 {
			continue
		}
		f := strings.Fields(t[open+1:])
		if len(f) == 0 {
			continue // a "channel" line with nothing after the bracket
		}
		return atoiSafe(f[0])
	}
	return 0
}

// surveyValue takes the first numeric field after the colon, dropping units and
// any trailing marker.
func surveyValue(line string) string {
	_, rest, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
