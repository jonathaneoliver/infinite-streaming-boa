package main

// probe is the "verify against the kernel, don't reason about it" rule from
// CLAUDE.md, expressed as a command with an exit code.
//
// The .claude/skills/verify-on-hardware skill carries the same checks as prose,
// and prose can only ASK the reader to remember that tc class ids are
// hexadecimal, that /usr/sbin is off a non-login shell's PATH, that
// `systemctl is-active` says nothing about whether a radio is serving, and that
// pairing any of it with 2>/dev/null turns "command not found" into an empty
// result that reads exactly like a real, healthy, empty answer. A binary cannot
// forget any of that.
//
// Most of it needs no SSH, because the daemon already reads several of these
// values back FROM the kernel rather than echoing what was requested --
// Counters.CapMbps most importantly. Comparing that against the policy is a
// real enforcement check over plain HTTP. -ssh adds the checks the API cannot
// answer: which filters exist, and whether hostapd's BSS is actually ENABLED.

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

type result int

const (
	pass result = iota
	warn
	fail
)

func (r result) String() string {
	switch r {
	case pass:
		return "PASS"
	case warn:
		return "WARN"
	default:
		return "FAIL"
	}
}

type report struct {
	results []result
}

func (rep *report) add(r result, check, detail string) {
	rep.results = append(rep.results, r)
	fmt.Printf("%-4s  %-22s  %s\n", r, check, detail)
}

func (rep *report) failed() bool {
	for _, r := range rep.results {
		if r == fail {
			return true
		}
	}
	return false
}

func (rep *report) summary() string {
	var p, w, f int
	for _, r := range rep.results {
		switch r {
		case pass:
			p++
		case warn:
			w++
		case fail:
			f++
		}
	}
	return fmt.Sprintf("%d passed, %d warnings, %d failed", p, w, f)
}

func cmdProbe(c *client, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	useSSH := fs.Bool("ssh", false, "also read the kernel back over SSH (filters, hostapd state)")
	sshUser := fs.String("ssh-user", "boa", "user for the SSH read-back")
	settle := fs.Duration("settle", 5*time.Second, "gap between the two counter samples")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Two samples zero apart measure nothing, and the rate they imply is a
	// division by zero. Accepting it printed "no client passed traffic in 0s",
	// which reads as a finding about the box rather than about the request.
	if *settle <= 0 {
		return fmt.Errorf("-settle must be positive; %s leaves no interval to measure over", *settle)
	}

	rep := &report{}

	// 1. The box answers, and says it can shape at all.
	var health struct {
		OK   bool             `json:"ok"`
		Caps boa.Capabilities `json:"caps"`
	}
	if err := c.get("/api/health", &health); err != nil {
		rep.add(fail, "reachable", err.Error())
		fmt.Printf("\n%s\n", rep.summary())
		return errors.New("the box did not answer; nothing else could be checked")
	}
	rep.add(boolResult(health.OK), "shaping capability", fmt.Sprintf("caps.shaping=%v, uplink=%v on %s",
		health.Caps.Shaping, health.Caps.Uplink, orDash(health.Caps.UplinkIf)))

	first, err := snapshot(c)
	if err != nil {
		return err
	}

	// 2. Notices carry operational truth the box wants surfaced. An error-level
	//    one means the box is not doing its job and says so.
	errs := 0
	for _, n := range first.Notices {
		if n.Level == "error" {
			errs++
			rep.add(fail, "notice", n.Text)
		}
	}
	if errs == 0 {
		rep.add(pass, "notices", "no error-level notices")
	}

	// 3. Enforcement. CapMbps is read back from tc, so a policy asking for a
	//    rate the kernel is not enforcing is visible without leaving HTTP.
	//    This is the check that catches a shape applied to nothing.
	checkEnforcement(rep, first)

	// 4. Counters have to be MOVING. Zero bytes on a class means the filter is
	//    not matching, whatever the interface shows.
	time.Sleep(*settle)
	second, err := snapshot(c)
	if err != nil {
		return err
	}
	checkCounters(rep, first, second, *settle)

	// 5. A wireless client's radio must actually be serving. hostapd stays
	//    running and retries after a failed ENABLE, so a unit that reads
	//    "active" proves nothing -- see issue #164.
	checkRadios(rep, first)

	// 6. Every routable address of a conditioned client needs its own filter.
	//    Privacy extensions mean a device usually holds several v6 addresses,
	//    and one filter is a PARTIAL shape, which looks like a working one.
	checkAddressCoverage(rep, first, *useSSH)

	if *useSSH {
		host := sshHost(c.base, *sshUser)
		checkKernelOverSSH(rep, host, first)
	}

	fmt.Printf("\n%s\n", rep.summary())
	if rep.failed() {
		return errors.New("the box is not doing what it claims")
	}
	return nil
}

// delta subtracts two readings of the same monotonic counter, reporting false
// when the counter went BACKWARDS -- which means the class was destroyed and
// recreated, not that traffic was negative. Unsigned subtraction across that
// boundary wraps to an enormous positive number rather than failing.
func delta(now, was uint64) (uint64, bool) {
	if now < was {
		return 0, false
	}
	return now - was, true
}

func snapshot(c *client) (boa.Snapshot, error) {
	var s boa.Snapshot
	err := c.get("/api/state", &s)
	return s, err
}

func boolResult(b bool) result {
	if b {
		return pass
	}
	return fail
}

// checkEnforcement compares what policy asks for against what the kernel says
// it is doing. A tolerance is needed because the rate is converted through
// bits/sec on the way down and back on the way up.
func checkEnforcement(rep *report, s boa.Snapshot) {
	const tolerance = 0.02 // 2%
	checked := 0
	for _, cl := range s.Clients {
		if !cl.Policy.Enabled || !cl.Present {
			continue
		}
		// The shapes in force, which are NOT Policy.Down when a distance model,
		// a sweep or a pattern is driving -- those are derived per tick and
		// never written back, so reading policy here skipped every such client
		// and reported "nothing to verify" on a box that was conditioning all
		// of them.
		wantDown, wantUp, source := effectiveShapes(cl)
		for _, d := range []struct {
			name string
			want float64
			got  float64
		}{
			{"down", wantDown.RateMbps, cl.DownCounters.CapMbps},
			{"up", wantUp.RateMbps, cl.UpCounters.CapMbps},
		} {
			if d.want == 0 {
				continue
			}
			checked++
			diff := d.got - d.want
			if diff < 0 {
				diff = -diff
			}
			label := fmt.Sprintf("%s %s (%s)", shortMAC(cl.MAC), d.name, source)
			switch {
			case d.got == 0:
				rep.add(fail, "cap enforced", fmt.Sprintf(
					"%s asks %.4g Mbps, the kernel is enforcing nothing", label, d.want))
			case diff/d.want > tolerance:
				rep.add(fail, "cap enforced", fmt.Sprintf(
					"%s asks %.4g Mbps, the kernel is enforcing %.4g", label, d.want, d.got))
			default:
				rep.add(pass, "cap enforced", fmt.Sprintf(
					"%s: %.4g Mbps, read back from tc", label, d.got))
			}
		}
	}
	if checked == 0 {
		rep.add(warn, "cap enforced", "no present client has a rate cap from any source; nothing to verify")
	}
}

// checkCounters proves traffic is reaching the shaper, and that a cap under
// load is actually capping. Bytes climbing on a class is what says the filter
// is matching at all: zero bytes means it is not, whatever the interface shows.
func checkCounters(rep *report, a, b boa.Snapshot, gap time.Duration) {
	before := map[string]boa.Client{}
	for _, cl := range a.Clients {
		before[cl.MAC] = cl
	}
	moving := 0
	for _, now := range b.Clients {
		was, ok := before[now.MAC]
		if !ok || !now.Present || !now.Policy.Enabled {
			continue
		}
		// A class that was recreated between the two samples restarts its
		// counters at zero, so the raw subtraction underflows uint64 into
		// something around 1.8e19 and renders as a throughput of billions of
		// Mbps -- stated with total confidence by a tool whose whole job is to
		// be trusted. The daemon guards the identical hazard in Engine.rate();
		// a shaper reinstall or a policy write during -settle is enough to
		// trigger it.
		bytes, ok := delta(now.DownCounters.Bytes, was.DownCounters.Bytes)
		if !ok {
			rep.add(warn, "counters moving", fmt.Sprintf(
				"%s: the class was recreated mid-sample, so no rate can be derived; probe again",
				shortMAC(now.MAC)))
			continue
		}
		effDown, _, _ := effectiveShapes(now)
		capped := effDown.RateMbps > 0
		if bytes == 0 {
			continue
		}
		moving++
		mbps := float64(bytes) * 8 / gap.Seconds() / 1e6

		// What proves a cap is biting is NOT HTB's overlimits. netem enforces
		// the rate here and HTB is kept only as a classifier and byte counter,
		// with its ceiling left at 10Gbit -- so a perfectly healthy capped
		// class reads `overlimits 0` forever. Measured on the box on
		// 2026-09-08: a client held at 4.72 Mbit/s under a 5 Mbps cap showed
		// overlimits 0 alongside `dropped 548, backlog 1091140b 730p`.
		//
		// The queue is the evidence. Packets waiting or being dropped is what
		// a rate limiter does; overlimits is what the layer NOT doing the
		// limiting reports.
		drops, _ := delta(now.DownCounters.Drops, was.DownCounters.Drops)
		over, _ := delta(now.DownCounters.Overlimits, was.DownCounters.Overlimits)
		queued := now.DownCounters.Backlog > 0 || now.DownCounters.Qlen > 0
		// A cap can only be shown to work while something is PUSHING against
		// it. A client drawing 28 Mbps through a 171 Mbps cap tells you nothing
		// about the cap, and saying PASS there is a check reporting success
		// having tested nothing -- the same false confidence as a filter check
		// that matched no clients. Demand real evidence: a queue, drops, or
		// throughput actually up at the ceiling.
		// The check name carries the claim, so the two are kept apart: a brief
		// queue proves the qdisc is in the path and handling this client's
		// traffic; only drops or throughput up at the ceiling prove the cap is
		// actually holding anything back.
		nearCeiling := effDown.RateMbps > 0 && mbps >= 0.9*effDown.RateMbps
		switch {
		case capped && (drops > 0 || nearCeiling):
			rep.add(pass, "cap is capping", fmt.Sprintf(
				"%s: %.3g Mbps down at a %.4g Mbps cap; +%d dropped, +%d overlimits in %s",
				shortMAC(now.MAC), mbps, effDown.RateMbps, drops, over, gap))
		case capped && queued:
			rep.add(pass, "shaper in path", fmt.Sprintf(
				"%s: %.3g Mbps down, %d queued on a %.4g Mbps cap -- the qdisc is handling it, "+
					"but nothing reached the ceiling, so the cap itself is untested",
				shortMAC(now.MAC), mbps, now.DownCounters.Qlen, effDown.RateMbps))
		case capped:
			rep.add(warn, "cap is capping", fmt.Sprintf(
				"%s: only %.3g Mbps through a %.4g Mbps cap, nothing queued or dropped -- "+
					"the cap is installed but nothing tested it; run a transfer to be sure",
				shortMAC(now.MAC), mbps, effDown.RateMbps))
		default:
			rep.add(pass, "counters moving", fmt.Sprintf(
				"%s: %.3g Mbps down, unconditioned", shortMAC(now.MAC), mbps))
		}
	}
	if moving == 0 {
		rep.add(warn, "counters moving", fmt.Sprintf(
			"no client passed traffic in %s; run a transfer through the box and probe again", gap))
	}
}

func checkRadios(rep *report, s boa.Snapshot) {
	// Counted separately on purpose. "No wireless client is present" and "every
	// wireless client is present but none reports a radio" are different
	// findings, and reporting the second as the first would hide the case where
	// the radio read failed for all of them.
	wifi, withRadio, serving := 0, 0, 0
	for _, cl := range s.Clients {
		if !cl.Present || cl.Medium != "wifi" {
			continue
		}
		wifi++
		if cl.RadioOn == nil {
			continue
		}
		withRadio++
		if cl.RadioOn.Serving {
			serving++
			continue
		}
		rep.add(fail, "radio serving", fmt.Sprintf(
			"%s is associated to %s but its BSS is not ENABLED -- a DISABLED BSS still reports its channel",
			shortMAC(cl.MAC), cl.RadioOn.Iface))
	}
	switch {
	case wifi == 0:
		rep.add(warn, "radio serving", "no wireless client is present to check")
	case withRadio == 0:
		rep.add(warn, "radio serving", fmt.Sprintf(
			"%d wireless client(s) present but none reports a radio; the radio read may have failed", wifi))
	case serving == withRadio:
		rep.add(pass, "radio serving", fmt.Sprintf("%d of %d wireless client(s) report a radio, every BSS ENABLED",
			withRadio, wifi))
	}
}

// checkAddressCoverage reports how many addresses each conditioned client holds.
// Without -ssh this cannot verify the filters exist, and says so rather than
// implying it checked.
func checkAddressCoverage(rep *report, s boa.Snapshot, usingSSH bool) {
	multi := 0
	for _, cl := range s.Clients {
		if !cl.Present || !cl.Policy.Enabled {
			continue
		}
		effDown, effUp, _ := effectiveShapes(cl)
		if effDown.IsClean() && effUp.IsClean() {
			continue
		}
		n := len(cl.IPv6)
		if cl.IP != "" {
			n++
		}
		if n > 1 {
			multi++
		}
		if n == 0 {
			rep.add(fail, "has an address", fmt.Sprintf(
				"%s is conditioned but holds no address; a filter matches on IP, so this shapes nothing",
				shortMAC(cl.MAC)))
		}
	}
	if multi > 0 && !usingSSH {
		rep.add(warn, "filter coverage", fmt.Sprintf(
			"%d conditioned client(s) hold several addresses; re-run with -ssh to confirm each has its own filter",
			multi))
	}
}

// --- the kernel read-back ---------------------------------------------------

// sshHost turns the API base into an SSH destination.
//
// Parsed rather than chopped. Splitting on ":" to drop a port destroys an IPv6
// literal -- "http://[fe80::1]" became "[fe80", and ssh would then fail with a
// name it was never given. This box is reached over IPv6 mDNS, so a literal is
// not a hypothetical.
func sshHost(base, user string) string {
	h := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		h = u.Host
		if u.User != nil {
			// A user in the URL is the one the operator meant.
			return u.User.Username() + "@" + stripPort(u.Host)
		}
	}
	if name, _, found := strings.Cut(h, "@"); found {
		_ = name
		return h
	}
	return user + "@" + stripPort(h)
}

// stripPort removes a trailing :port and the brackets an IPv6 literal wears in
// a URL, leaving the bare host ssh expects.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	// No port: still unwrap "[fe80::1]".
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// ssh runs one command on the box. Absolute paths are the caller's job; stderr
// is deliberately captured and returned rather than discarded, because
// "command not found" from a bare `tc` is the exact failure that reads as an
// empty, healthy answer when it is thrown away.
func ssh(host, cmd string) (string, error) {
	out, err := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, cmd).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh %s: %w: %s", host, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func checkKernelOverSSH(rep *report, host string, s boa.Snapshot) {
	ifaces := s.Caps.WlanIfaces
	if len(ifaces) == 0 && s.Caps.WlanIface != "" {
		ifaces = []string{s.Caps.WlanIface}
	}
	// Every port a filter could be on, and the client's OWN port is the one
	// that matters. Downlink shaping lives on the egress of the port the client
	// was learned on -- wlan-usb for a wireless client, lan0 for a wired one --
	// and lan0 is neither a radio nor the uplink, so a list built from
	// WlanIfaces plus UplinkIf misses it entirely. A conditioned wired client
	// would have been reported as having no filter, which is a FAIL saying its
	// traffic escapes conditioning, about a client being conditioned correctly.
	seen := map[string]bool{}
	var ports []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	for _, i := range ifaces {
		add(i)
	}
	add(s.Caps.UplinkIf) // uplink filters, matching on source
	for _, cl := range s.Clients {
		if cl.Present {
			add(cl.Port)
		}
	}

	// Filters, per port. Downlink lives on the client's own port and uplink on
	// the WAN port; looking at the bridge itself finds nothing.
	filters := map[string]string{}
	for _, port := range ports {
		out, err := ssh(host, "/usr/sbin/tc filter show dev "+port)
		if err != nil {
			rep.add(fail, "read filters", fmt.Sprintf("%s: %v", port, err))
			continue
		}
		filters[port] = out
	}
	if len(filters) == 0 {
		return
	}
	all := strings.Join(mapValues(filters), "\n")

	missing, conditioned := 0, 0
	for _, cl := range s.Clients {
		if !cl.Present || !cl.Policy.Enabled {
			continue
		}
		effDown, effUp, _ := effectiveShapes(cl)
		if effDown.IsClean() && effUp.IsClean() {
			continue
		}
		conditioned++
		addrs := append([]string{}, cl.IPv6...)
		if cl.IP != "" {
			addrs = append(addrs, cl.IP)
		}
		var absent []string
		for _, a := range addrs {
			if !filterMatches(all, a) {
				absent = append(absent, a)
			}
		}
		if len(absent) > 0 {
			missing++
			rep.add(fail, "filter per address", fmt.Sprintf(
				"%s: no filter matches %s -- that traffic escapes conditioning entirely",
				shortMAC(cl.MAC), strings.Join(absent, ", ")))
		}
	}
	switch {
	case conditioned == 0:
		// Saying PASS here would be a check that examined nothing reporting
		// success, which is the failure this whole command exists to catch.
		rep.add(warn, "filter per address", "no client is conditioned; no filter was checked")
	case missing == 0:
		rep.add(pass, "filter per address", fmt.Sprintf(
			"every address of %d conditioned client(s) has a filter", conditioned))
	}

	// hostapd's own view. The templated units are the real ones; the bare
	// service is a permanently-inactive legacy leftover, and hostapd_cli needs
	// -p or it reports "Failed to connect" as though nothing were running.
	for _, iface := range ifaces {
		out, err := ssh(host, "sudo hostapd_cli -p /var/run/hostapd -i "+iface+" status")
		if err != nil {
			rep.add(fail, "hostapd state", fmt.Sprintf("%s: %v", iface, err))
			continue
		}
		state := fieldFrom(out, "state=")
		if state == "ENABLED" {
			rep.add(pass, "hostapd state", fmt.Sprintf("%s: state=ENABLED", iface))
		} else {
			rep.add(fail, "hostapd state", fmt.Sprintf(
				"%s: state=%s -- the unit can read active while the BSS serves nobody",
				iface, orDash(state)))
		}
	}
}

// filterMatches reports whether a u32 filter for this address exists in tc's
// output.
//
// tc does NOT print the address. It prints the 32-bit words the filter compares
// against, in HEX: a filter for 192.168.0.52 reads `match c0a80034/ffffffff at
// 12`. Searching the output for the dotted-quad finds nothing and reports a
// working filter as missing -- measured on the box on 2026-09-08, where this
// failed a client whose 5 Mbps cap was demonstrably holding at 4.72 Mbit/s.
//
// The same hexadecimal trap as tc's class ids, one layer down.
func filterMatches(out, addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		return strings.Contains(out, hex.EncodeToString(v4))
	}
	// A v6 address is compared as four 32-bit words, on four separate match
	// lines. All four must be present: finding only the prefix would accept a
	// filter for a different address in the same /64, which for privacy
	// extensions is precisely the address next door.
	full := hex.EncodeToString(ip.To16())
	for i := 0; i < 4; i++ {
		if !strings.Contains(out, full[i*8:(i+1)*8]) {
			return false
		}
	}
	return true
}

func fieldFrom(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func shortMAC(mac string) string {
	if len(mac) >= 8 {
		return mac[len(mac)-8:]
	}
	return mac
}
