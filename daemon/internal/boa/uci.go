package boa

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

/*
 * Where OpenWrt keeps a radio's channel, and why a move has to be written there.
 *
 * OpenWrt applies /etc/config/wireless on every boot, `wifi reload` and LuCI
 * "Save & Apply". A move made through hostapd's control socket never reaches
 * that file, so the next reload puts the radio back where UCI says -- measured
 * on a Pi 5 running 25.12: a radio moved from 2.4GHz ch 6 to 5GHz ch 40 was
 * still `band '2g'`, `channel 'auto'` in UCI. The restore loop
 * (channelrestore.go) would then move it again, and two owners of one setting
 * trade it back and forth on every reload.
 *
 * Writing the remembered choice into UCI as well gives both owners the same
 * answer. Committed but NOT reloaded: hostapd is already serving on the
 * channel, so the file only has to agree with it, and a reload would drop
 * every client for nothing.
 *
 * Only with -openwrt (Config.OpenWrt), which /etc/init.d/boa passes. Decided
 * by the flag rather than by finding `uci` on the PATH, so a missing uci on a
 * box that says it is OpenWrt is an error that gets logged, not a silent skip.
 */

// persistChannelUCI records a channel the radio behind iface has come back up
// on in OpenWrt's wireless config.
func persistChannelUCI(iface string, channel, widthMHz int) error {
	raw, err := exec.Command("ubus", "call", "network.wireless", "status").Output()
	if err != nil {
		return fmt.Errorf("reading network.wireless status: %w", err)
	}
	radio, err := uciRadioFor(raw, iface)
	if err != nil {
		return err
	}
	cur, _ := exec.Command("uci", "-q", "get", "wireless."+radio+".htmode").Output()
	sets, err := uciChannelSets(radio, channel, widthMHz, strings.TrimSpace(string(cur)))
	if err != nil {
		return err
	}
	for _, s := range sets {
		if out, err := exec.Command("uci", "set", s).CombinedOutput(); err != nil {
			// Discard the half-written change rather than leave it staged for
			// whoever commits next.
			_ = exec.Command("uci", "revert", "wireless."+radio).Run()
			return fmt.Errorf("uci set %s: %v: %s", s, err, strings.TrimSpace(string(out)))
		}
	}
	if out, err := exec.Command("uci", "commit", "wireless").CombinedOutput(); err != nil {
		return fmt.Errorf("uci commit wireless: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// uciRadioFor finds the wifi-device section serving iface, from the output of
// `ubus call network.wireless status`.
//
// Asked of netifd rather than derived from the name. OpenWrt names an AP after
// its phy (phy1-ap0), but the phy number follows USB enumeration order and the
// radio section number follows whatever `wifi config` generated, so the two
// agree only by coincidence.
func uciRadioFor(status []byte, iface string) (string, error) {
	var radios map[string]struct {
		Interfaces []struct {
			Ifname string `json:"ifname"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(status, &radios); err != nil {
		return "", fmt.Errorf("parsing network.wireless status: %w", err)
	}
	for name, r := range radios {
		for _, in := range r.Interfaces {
			if in.Ifname == iface {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("no wifi-device in network.wireless serves %s", iface)
}

// uciChannelSets is the `uci set` arguments that put radio on channel at
// widthMHz, given its current htmode.
//
// band is written with the channel because a move may cross bands, and a 5GHz
// channel under `band '2g'` is refused at the next reload. htmode keeps the
// PHY generation it had (HE, VHT, HT) and takes the new width; VHT does not
// exist at 2.4GHz, so it falls back to HT there. A width of 0 leaves htmode
// alone.
func uciChannelSets(radio string, channel, widthMHz int, htmode string) ([]string, error) {
	freq := freqForChannel(channel)
	var band string
	switch {
	case freq >= 2400 && freq < 2500:
		band = "2g"
	case freq >= 5000 && freq < 5900:
		band = "5g"
	default:
		return nil, fmt.Errorf("channel %d is in neither the 2.4GHz nor the 5GHz band", channel)
	}
	p := "wireless." + radio + "."
	sets := []string{p + "band=" + band, p + "channel=" + strconv.Itoa(channel)}
	if widthMHz > 0 && htmode != "" {
		gen := strings.TrimRight(htmode, "0123456789+-")
		if gen == "VHT" && band == "2g" {
			gen = "HT"
		}
		if gen != "" {
			sets = append(sets, p+"htmode="+gen+strconv.Itoa(widthMHz))
		}
	}
	return sets, nil
}
