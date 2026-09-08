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
		for _, d := range []struct {
			name string
			want float64
			got  float64
		}{
			{"down", cl.Policy.Down.RateMbps, cl.DownCounters.CapMbps},
			{"up", cl.Policy.Up.RateMbps, cl.UpCounters.CapMbps},
		} {
			if d.want == 0 {
				continue
			}
			checked++
			diff := d.got - d.want
			if diff < 0 {
				diff = -diff
			}
			label := fmt.Sprintf("%s %s", shortMAC(cl.MAC), d.name)
			switch {
			case d.got == 0:
				rep.add(fail, "cap enforced", fmt.Sprintf(
					"%s: policy asks %.4g Mbps, the kernel is enforcing nothing", label, d.want))
			case diff/d.want > tolerance:
				rep.add(fail, "cap enforced", fmt.Sprintf(
					"%s: policy asks %.4g Mbps, the kernel is enforcing %.4g", label, d.want, d.got))
			default:
				rep.add(pass, "cap enforced", fmt.Sprintf(
					"%s: %.4g Mbps, read back from tc", label, d.got))
			}
		}
	}
	if checked == 0 {
		rep.add(warn, "cap enforced", "no present client has a rate cap; nothing to verify")
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
		bytes := now.DownCounters.Bytes - was.DownCounters.Bytes
		capped := now.Policy.Down.RateMbps > 0
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
		drops := now.DownCounters.Drops - was.DownCounters.Drops
		over := now.DownCounters.Overlimits - was.DownCounters.Overlimits
		queued := now.DownCounters.Backlog > 0 || now.DownCounters.Qlen > 0
		switch {
		case capped && drops == 0 && over == 0 && !queued:
			rep.add(warn, "cap is capping", fmt.Sprintf(
				"%s: %.3g Mbps through a %.4g Mbps cap, but nothing is queueing or dropping; not at the ceiling yet",
				shortMAC(now.MAC), mbps, now.Policy.Down.RateMbps))
		case capped:
			rep.add(pass, "cap is capping", fmt.Sprintf(
				"%s: %.3g Mbps down under a %.4g Mbps cap; +%d dropped, %d queued in %s",
				shortMAC(now.MAC), mbps, now.Policy.Down.RateMbps, drops, now.DownCounters.Qlen, gap))
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
		if cl.Policy.Down.IsClean() && cl.Policy.Up.IsClean() {
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
func sshHost(base, user string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	h = strings.SplitN(h, "/", 2)[0]
	h = strings.SplitN(h, ":", 2)[0]
	if strings.Contains(h, "@") {
		return h
	}
	return user + "@" + h
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
	ports := append([]string{}, ifaces...)
	if s.Caps.UplinkIf != "" {
		ports = append(ports, s.Caps.UplinkIf)
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
		if cl.Policy.Down.IsClean() && cl.Policy.Up.IsClean() {
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
