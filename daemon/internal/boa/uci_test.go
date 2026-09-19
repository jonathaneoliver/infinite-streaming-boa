package boa

import (
	"slices"
	"testing"
)

// Trimmed from `ubus call network.wireless status` on a Pi 5, OpenWrt 25.12.5:
// radio0 (onboard) has no interface, and the USB radios' section numbers match
// their phy numbers only by coincidence.
const wirelessStatus = `{
	"radio0": {"up": true, "config": {"band": "5g"}, "interfaces": []},
	"radio1": {"up": true, "interfaces": [{"section": "default_radio1", "ifname": "phy1-ap0"}]},
	"radio2": {"up": true, "interfaces": [{"section": "default_radio2", "ifname": "phy2-ap0"}]}
}`

func TestUCIRadioFor(t *testing.T) {
	got, err := uciRadioFor([]byte(wirelessStatus), "phy2-ap0")
	if err != nil || got != "radio2" {
		t.Fatalf("phy2-ap0: got %q, %v; want radio2", got, err)
	}
	if _, err := uciRadioFor([]byte(wirelessStatus), "wlan0"); err == nil {
		t.Fatal("wlan0 serves no AP here, so it must not resolve to a radio")
	}
	if _, err := uciRadioFor([]byte("not json"), "phy1-ap0"); err == nil {
		t.Fatal("unparsable status must be an error, not an empty answer")
	}
}

func TestUCIChannelSets(t *testing.T) {
	cases := []struct {
		name    string
		channel int
		width   int
		htmode  string
		want    []string
	}{
		{"cross from 2.4 to 5GHz keeps HE", 40, 80, "HE20",
			[]string{"wireless.radio2.band=5g", "wireless.radio2.channel=40", "wireless.radio2.htmode=HE80"}},
		{"back to 2.4GHz", 6, 20, "HE80",
			[]string{"wireless.radio2.band=2g", "wireless.radio2.channel=6", "wireless.radio2.htmode=HE20"}},
		{"VHT has no 2.4GHz form", 11, 40, "VHT80",
			[]string{"wireless.radio2.band=2g", "wireless.radio2.channel=11", "wireless.radio2.htmode=HT40"}},
		{"HT40+ keeps its generation", 36, 40, "HT40+",
			[]string{"wireless.radio2.band=5g", "wireless.radio2.channel=36", "wireless.radio2.htmode=HT40"}},
		{"unknown width leaves htmode alone", 149, 0, "HE80",
			[]string{"wireless.radio2.band=5g", "wireless.radio2.channel=149"}},
		{"no htmode set leaves it unset", 149, 80, "",
			[]string{"wireless.radio2.band=5g", "wireless.radio2.channel=149"}},
	}
	for _, c := range cases {
		got, err := uciChannelSets("radio2", c.channel, c.width, c.htmode)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	if _, err := uciChannelSets("radio2", 200, 20, "HE20"); err == nil {
		t.Error("a channel in neither band must be refused, not written")
	}
}
