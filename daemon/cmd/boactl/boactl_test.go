package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

// These cover the pure decisions -- which shape is in force, whether a filter
// exists, whether a counter went backwards -- because every one of them has
// already been wrong once against real hardware, and each was found by eye
// rather than by anything that would catch it again.

func TestEffectiveShapesFollowsTheDaemonsPrecedence(t *testing.T) {
	// Engine.desired() applies policy, then RSSI over both directions, then a
	// sweep over the downlink only, then a pattern over both. Anything else
	// here would be a second opinion about what the box is doing.
	policy := boa.Policy{Enabled: true, Down: boa.Shape{RateMbps: 1}, Up: boa.Shape{RateMbps: 2}}

	for _, tc := range []struct {
		name             string
		client           boa.Client
		wantDown, wantUp float64
		wantSource       string
	}{
		{
			name:     "policy alone",
			client:   boa.Client{Policy: policy},
			wantDown: 1, wantUp: 2, wantSource: "policy",
		},
		{
			// The bug this whole helper exists for: a device driven by a
			// distance model has a policy that reads clean while the kernel
			// holds it at a real rate.
			name: "a distance model beats a clean policy",
			client: boa.Client{
				Policy:  boa.Policy{Enabled: true},
				RssiRun: &boa.RssiView{Down: boa.Shape{RateMbps: 171.1}, Up: boa.Shape{RateMbps: 114.1}},
			},
			wantDown: 171.1, wantUp: 114.1, wantSource: "rssi",
		},
		{
			name: "a sweep takes the downlink only",
			client: boa.Client{
				Policy: policy,
				Sweep:  &boa.SweepView{State: "running", CapMbps: 8},
			},
			wantDown: 8, wantUp: 2, wantSource: "sweep",
		},
		{
			name: "a finished sweep takes nothing",
			client: boa.Client{
				Policy: policy,
				Sweep:  &boa.SweepView{State: "done", CapMbps: 8},
			},
			wantDown: 1, wantUp: 2, wantSource: "policy",
		},
		{
			name: "a running pattern wins over everything",
			client: boa.Client{
				Policy:     policy,
				RssiRun:    &boa.RssiView{Down: boa.Shape{RateMbps: 171}},
				Sweep:      &boa.SweepView{State: "running", CapMbps: 8},
				PatternRun: &boa.PatternView{State: "running", Down: boa.Shape{RateMbps: 3}, Up: boa.Shape{RateMbps: 4}},
			},
			wantDown: 3, wantUp: 4, wantSource: "pattern",
		},
		{
			// "Disabled means do not condition, not do not measure."
			name:     "disabled imposes nothing",
			client:   boa.Client{Policy: boa.Policy{Enabled: false, Down: boa.Shape{RateMbps: 5}}},
			wantDown: 0, wantUp: 0, wantSource: "off",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			down, up, source := effectiveShapes(tc.client)
			if down.RateMbps != tc.wantDown || up.RateMbps != tc.wantUp {
				t.Errorf("shapes = down %v up %v, want down %v up %v",
					down.RateMbps, up.RateMbps, tc.wantDown, tc.wantUp)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

func TestFilterMatchesReadsTcHex(t *testing.T) {
	// tc prints the 32-bit word a filter compares against, in hex, never the
	// address. Searching for the dotted quad found nothing and reported a
	// working filter as missing.
	const v4 = `filter parent 1: protocol ip pref 49 u32 chain 0 fh 800::800 flowid 1:10
  match c0a80034/ffffffff at 16`

	if !filterMatches(v4, "192.168.0.52") {
		t.Error("192.168.0.52 is c0a80034 and is present; reported missing")
	}
	if filterMatches(v4, "192.168.0.53") {
		t.Error("192.168.0.53 is c0a80035 and is absent; reported present")
	}
	if filterMatches(v4, "not-an-address") {
		t.Error("an unparseable address must not match anything")
	}

	// A v6 address is compared as four separate 32-bit words. All four must be
	// present: matching only the prefix would accept a filter for a different
	// address in the same /64, which under privacy extensions is the address
	// most likely to be there.
	const v6Prefix = `match 20010db8/ffffffff at 24
  match 00000000/ffffffff at 28`
	if filterMatches(v6Prefix, "2001:db8::dead:beef:1:2") {
		t.Error("only the prefix words are present; a partial match must not pass")
	}
	full := v6Prefix + `
  match deadbeef/ffffffff at 32
  match 00010002/ffffffff at 36`
	if !filterMatches(full, "2001:db8::dead:beef:1:2") {
		t.Error("all four words are present; reported missing")
	}
}

func TestDeltaRefusesToWrapWhenAClassIsRecreated(t *testing.T) {
	if got, ok := delta(500, 100); !ok || got != 400 {
		t.Errorf("delta(500,100) = %d,%v; want 400,true", got, ok)
	}
	// A recreated class restarts at zero. Unsigned subtraction turns that into
	// roughly 1.8e19, which rendered as a throughput of billions of Mbps.
	if _, ok := delta(5, 1000); ok {
		t.Error("a counter that went backwards must be reported, not subtracted")
	}
}

func TestTruncateDoesNotSplitARune(t *testing.T) {
	// Device labels are operator-set and routinely non-ASCII. Slicing bytes
	// cuts a multi-byte rune in half and emits invalid UTF-8.
	got := truncate("Bjørn's Fjärrkontroll", 10)
	if !strings.ContainsRune(got, '…') {
		t.Fatalf("truncate did not shorten: %q", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if n := len([]rune(got)); n != 10 {
		t.Errorf("truncate returned %d runes, want 10: %q", n, got)
	}
	if short := truncate("ok", 10); short != "ok" {
		t.Errorf("a short string must be returned unchanged, got %q", short)
	}
}

func TestDescribeExportRefusesAnEmptyBackup(t *testing.T) {
	// The file an operator writes before a reflash, which destroys
	// /var/lib/infinite-streaming-boa. An export carrying nothing reports
	// success, looks like a backup, and restores nothing.
	empty, _ := json.Marshal(boa.ConfigExport{Version: 2, ExportedAt: 1})
	if err := describeExport(empty); err == nil {
		t.Error("an export with no ladder, patterns or devices must fail loudly")
	}

	populated, _ := json.Marshal(boa.ConfigExport{
		Version: 2,
		Ladder:  &boa.Ladder{Rungs: []boa.Rung{{}, {}}},
	})
	if err := describeExport(populated); err != nil {
		t.Errorf("an export carrying a ladder is a real backup: %v", err)
	}
}

func TestSSHHostFromEveryFormOfBox(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"http://infinite-streaming-boa.local", "boa@infinite-streaming-boa.local"},
		{"http://infinite-streaming-boa.local:8099", "boa@infinite-streaming-boa.local"},
		{"http://pi@infinite-streaming-boa.local", "pi@infinite-streaming-boa.local"},
		{"https://192.168.0.200", "boa@192.168.0.200"},
		// The box is reached over IPv6 mDNS, so a literal is not far-fetched.
		// Splitting on ":" to drop a port cuts an IPv6 literal to "[fe80".
		{"http://[fe80::1]", "boa@fe80::1"},
		{"http://[fe80::1]:8099", "boa@fe80::1"},
	} {
		if got := sshHost(tc.base, "boa"); got != tc.want {
			t.Errorf("sshHost(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}
