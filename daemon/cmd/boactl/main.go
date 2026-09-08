// Command boactl drives a box's HTTP API from a terminal.
//
// It exists because the recipes it replaces were curl one-liners carried in
// README.md and in the .claude skills, and each of them had a trap in it: a URL
// assembled by hand, a POST whose body had to be exactly right, a read-back
// whose empty output was indistinguishable from a real empty answer. Those are
// the same class of silent failure CLAUDE.md warns about, and a curl pipeline
// reports none of them.
//
// It lives in the daemon's own module on purpose. The response types come from
// internal/boa directly, so this cannot drift from what the daemon actually
// sends -- there is no second copy of the contract to keep in step, which is
// the cost a generated client would have added.
//
//	boactl state                    what the box is doing right now
//	boactl devices                  one line per client, with what is imposed
//	boactl bridge                   radios, channels and how contested they are
//	boactl events -follow           the event stream, as it happens
//	boactl survey wlan-usb          a radio's airtime counters
//	boactl shape "Apple TV" -down 5 impose conditioning on one device
//	boactl config get -o boa.json   export; config apply reads one back
//	boactl probe                    assert the box is really doing its job
//
// The box defaults to $BOA_BOX, then infinite-streaming-boa.local.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

const defaultBox = "infinite-streaming-boa.local"

type client struct {
	base string
	http *http.Client
}

func newClient(box string) *client {
	base := strings.TrimSuffix(box, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &client{
		base: base,
		// Generous, because a radio holding rtnl_lock through a firmware
		// reload can stall a response for tens of seconds, and a timeout there
		// would report a working box as unreachable.
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// get decodes a GET into v, and reports the status line rather than swallowing
// it: an HTML error page decoded into a struct yields a zero value that reads
// exactly like a healthy empty answer.
func (c *client) get(path string, v any) error {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if v == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("GET %s: decoding: %w", path, err)
	}
	return nil
}

// patch sends a JSON merge onto a device. Separate from post because the
// policy surface is PATCH: every field is a pointer server-side so that
// "absent" and "set to zero" stay distinguishable, and sending a whole object
// would clear whatever it omitted.
func (c *client) patch(path string, body []byte) error {
	req, err := http.NewRequest(http.MethodPatch, c.base+path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("PATCH %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("PATCH %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *client) post(path string, body []byte) error {
	var r io.Reader
	if body != nil {
		r = strings.NewReader(string(body))
	}
	resp, err := c.http.Post(c.base+path, "application/json", r)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// stream calls fn for each line of an SSE-ish newline-delimited body until the
// connection ends or fn returns false.
func (c *client) stream(path string, fn func(line string) bool) error {
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !fn(string(raw)) {
			return nil
		}
	}
}

var (
	boxFlag     = flag.String("box", "", "box hostname or URL (default $BOA_BOX, then "+defaultBox+")")
	jsonFlag    = flag.Bool("json", false, "emit the raw JSON payload instead of a summary")
	versionFlag = flag.Bool("version", false, "print which build of boactl this is, and exit")
)

// version is stamped at link time, exactly as the daemon's is:
//
//	go build -ldflags "-X main.version=$(scripts/version.sh)" ./cmd/boactl
//
// Deliberately NOT taken from debug.ReadBuildInfo's vcs.revision, which is the
// obvious approach and is wrong here. Measured 2026-09-08 in a worktree under
// .claude/worktrees: with HEAD at bb6f5b4948af and the tree modified, a build
// with a cleared cache stamped 1a6a91d68b6b from four days earlier and set
// vcs.modified to false. A version that reports the wrong build with confidence
// is worse than one that admits it does not know, and this repository has
// already lost a testing session to not knowing which build was running.
var version = "dev"

func buildStamp() string {
	if version == "dev" {
		return "dev (no version stamped at build time; see the comment on version)"
	}
	return version
}

func main() {
	flag.Usage = usage
	flag.Parse()

	if *versionFlag {
		fmt.Printf("boactl %s\n", buildStamp())
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	box := *boxFlag
	if box == "" {
		box = os.Getenv("BOA_BOX")
	}
	if box == "" {
		box = defaultBox
	}
	c := newClient(box)

	if err := run(c, args); err != nil {
		fmt.Fprintf(os.Stderr, "boactl: %v\n", err)
		os.Exit(1)
	}
}

// noArgs rejects anything trailing a command that takes none.
//
// Silently ignoring it is the trap: global flags have to precede the command,
// so `boactl devices -json` is a natural thing to type and used to print the
// plain table and exit 0 -- the flag disregarded, with nothing said. A tool
// whose whole purpose is to stop failures being quiet cannot do that.
func noArgs(cmd string, rest []string) error {
	if len(rest) == 0 {
		return nil
	}
	if strings.HasPrefix(rest[0], "-") {
		return fmt.Errorf("%s takes no flags; global flags go BEFORE the command: boactl %s %s",
			cmd, rest[0], cmd)
	}
	return fmt.Errorf("%s takes no arguments, got %q", cmd, rest[0])
}

// jsonCapable is the set of commands -json actually changes. survey is absent
// because it is always JSON, and the three that write or assert have no
// payload to reformat.
var jsonCapable = map[string]bool{"state": true, "devices": true, "bridge": true, "events": true}

func run(c *client, args []string) error {
	// -json on a command that cannot honour it was accepted and ignored, which
	// is the same silent no-op as a misplaced flag: the caller believes they
	// asked for JSON and gets prose, with nothing said. Refuse instead.
	if *jsonFlag && !jsonCapable[args[0]] {
		if args[0] == "survey" {
			return errors.New("survey always emits JSON; drop -json")
		}
		return fmt.Errorf("-json does not apply to %s (only state, devices, bridge, events)", args[0])
	}
	switch args[0] {
	case "state":
		if err := noArgs("state", args[1:]); err != nil {
			return err
		}
		return cmdState(c)
	case "devices":
		if err := noArgs("devices", args[1:]); err != nil {
			return err
		}
		return cmdDevices(c)
	case "bridge":
		if err := noArgs("bridge", args[1:]); err != nil {
			return err
		}
		return cmdBridge(c)
	case "events":
		return cmdEvents(c, args[1:])
	case "survey":
		if len(args) < 2 {
			return errors.New("survey needs a radio interface, e.g. boactl survey wlan-usb")
		}
		return cmdSurvey(c, args[1])
	case "shape":
		return cmdShape(c, args[1:])
	case "config":
		return cmdConfig(c, args[1:])
	case "probe":
		return cmdProbe(c, args[1:])
	default:
		return fmt.Errorf("unknown command %q (try: state, devices, bridge, events, survey, shape, config, probe)", args[0])
	}
}

// usage is grouped by what a command DOES, not alphabetically. Two of these
// change a live network -- shape conditions a real device's traffic and config
// apply rewrites every policy on the box -- and a flat list gives a reader no
// way to tell those from the ones that only look.
func usage() {
	fmt.Fprintf(os.Stderr, `boactl -- drive an infinite-streaming-boa appliance

  boactl [-box HOST] [-json] <command> [args]

Look:
  state                        what the box is doing right now
  devices                      a line per client: what is imposed, and by what
  bridge                       radios, channels, how contested each one is
  survey <iface>               one radio's airtime counters (always JSON)
  events [-since N] [-follow]  what has happened, newest last

Change -- these act on a live network:
  shape <mac|label> [flags]    condition one device's traffic
  config apply <file>          replace every policy on the box
  config get [-o file]         export them first

Check:
  probe [-ssh] [-settle D]     assert the box is really doing its job;
                               exits non-zero when it is not

Global flags, which must come BEFORE the command:
  -box HOST   box hostname or URL (default $BOA_BOX, then %s)
  -json       raw JSON instead of the summary

A command's own flags come after it, and %s <command> -h lists them.

Examples:
  boactl devices
  boactl shape "Apple TV" -down 5 -delay 40   # 5 Mbps, 40 ms one way
  boactl shape "Apple TV" -clear
  boactl probe -ssh                           # also reads the kernel over SSH
  boactl events -follow > run.ndjson          # ground truth for a test run
`, defaultBox, "boactl")
}

// helpWanted reports whether the first argument is asking for help rather than
// naming a thing. Without it `boactl shape -h` looked for a device called "-h"
// and reported that no device matched, and `config -h` called it an unknown
// subcommand -- while `probe -h` and `events -h` worked, because those parse a
// FlagSet before touching their arguments.
func helpWanted(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "-help", "help":
		return true
	}
	return false
}

// --- commands ---------------------------------------------------------------

func cmdState(c *client) error {
	var s boa.Snapshot
	if err := c.get("/api/state", &s); err != nil {
		return err
	}
	if *jsonFlag {
		return emitJSON(s)
	}
	fmt.Printf("version   %s\n", orDash(s.Version))
	fmt.Printf("revision  %d\n", s.Revision)
	fmt.Printf("shaping   %s\n", yesNo(s.Caps.Shaping))
	fmt.Printf("uplink    %s on %s\n", yesNo(s.Caps.Uplink), orDash(s.Caps.UplinkIf))
	fmt.Printf("radios    %s\n", orDash(strings.Join(s.Caps.WlanIfaces, ", ")))

	present, conditioned := 0, 0
	for _, cl := range s.Clients {
		if cl.Present {
			present++
		}
		if cl.Policy.Enabled && (!cl.Policy.Down.IsClean() || !cl.Policy.Up.IsClean()) {
			conditioned++
		}
	}
	fmt.Printf("clients   %d present, %d conditioned (%d known)\n", present, conditioned, len(s.Clients))

	for _, n := range s.Notices {
		fmt.Printf("%-9s %s\n", n.Level, n.Text)
	}
	return nil
}

func cmdDevices(c *client) error {
	var s boa.Snapshot
	if err := c.get("/api/state", &s); err != nil {
		return err
	}
	if *jsonFlag {
		return emitJSON(s.Clients)
	}
	if len(s.Clients) == 0 {
		fmt.Println("no clients known")
		return nil
	}
	fmt.Printf("%-17s %-18s %-7s %-8s %-8s %-16s %-16s %s\n",
		"MAC", "LABEL", "MEDIUM", "PRESENT", "FROM", "DOWN", "UP", "ENFORCED")
	for _, cl := range s.Clients {
		label := cl.Label
		if label == "" {
			label = cl.Hostname
		}
		// The shapes actually in force, not the stored policy -- see
		// effectiveShapes. FROM names what decided them, so a device held by a
		// distance model or a running pattern cannot read as unconditioned.
		down, up, source := effectiveShapes(cl)
		fmt.Printf("%-17s %-18s %-7s %-8s %-8s %-16s %-16s %s\n",
			cl.MAC, truncate(orDash(label), 18), cl.Medium, yesNo(cl.Present),
			source, shapeSummary(down), shapeSummary(up), enforced(cl))
	}
	return nil
}

func cmdBridge(c *client) error {
	var b boa.BridgeInfo
	if err := c.get("/api/bridge", &b); err != nil {
		return err
	}
	if *jsonFlag {
		return emitJSON(b)
	}
	fmt.Printf("bridge %s (view %dms old)\n\n", orDash(b.Bridge), b.ReadAgeMs)
	for _, i := range b.Ifaces {
		fmt.Printf("%s\n", i.Name)
		if air, ok := b.Air[i.Name]; ok {
			util := "unknown"
			if air.UtilKnown {
				util = fmt.Sprintf("%.0f%%", air.UtilPct)
			}
			fmt.Printf("  channel %d, airtime %s (measured by %s)\n", air.Channel, util, orDash(air.From))
		}
	}
	for _, n := range b.Notes {
		fmt.Printf("\n%-6s %s\n", n.Level, n.Text)
	}
	return nil
}

func cmdEvents(c *client, args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	since := fs.Uint64("since", 0, "only events after this sequence number")
	follow := fs.Bool("follow", false, "stream events as they happen")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *follow {
		// Deliberately raw: the stream is what README's ground-truth capture
		// records, and reformatting it would make the saved file something
		// other than what the box said.
		return c.stream("/api/events/stream", func(line string) bool {
			fmt.Println(line)
			return true
		})
	}
	// The endpoint answers with an envelope, not a bare array: `latest` is the
	// sequence to pass as -since next time, and is not derivable from the
	// events when the poll returns none.
	var envelope struct {
		Events []boa.Event `json:"events"`
		Latest uint64      `json:"latest"`
	}
	if err := c.get(fmt.Sprintf("/api/events?since=%d", *since), &envelope); err != nil {
		return err
	}
	if *jsonFlag {
		return emitJSON(envelope)
	}
	// An empty result printed nothing at all and exited 0, which is
	// indistinguishable from a command that did not run. Say so, and give the
	// sequence to poll from next -- `latest` is in the envelope precisely
	// because it cannot be derived from an empty list.
	if len(envelope.Events) == 0 {
		fmt.Fprintf(os.Stderr, "no events after %d; the newest the box holds is %d\n",
			*since, envelope.Latest)
		return nil
	}
	for _, e := range envelope.Events {
		fmt.Printf("%d\t%s\t%s\n", e.Seq, time.UnixMilli(e.At).Format("15:04:05"), e.Text)
	}
	fmt.Fprintf(os.Stderr, "%d event(s); next poll: boactl events -since %d\n",
		len(envelope.Events), envelope.Latest)
	return nil
}

func cmdSurvey(c *client, iface string) error {
	var r boa.SurveyResult
	if err := c.get("/api/bridge/radios/"+iface+"/survey", &r); err != nil {
		return err
	}
	return emitJSON(r)
}

func cmdConfig(c *client, args []string) error {
	if helpWanted(args) {
		fmt.Fprint(os.Stderr, "boactl config get [-o file]   export the box's configuration\n"+
			"boactl config apply <file>    send one back, replacing every policy\n")
		return nil
	}
	if len(args) == 0 {
		return errors.New("config needs get or apply")
	}
	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("config get", flag.ExitOnError)
		out := fs.String("o", "", "write to this file instead of stdout")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := c.get("/api/config", &raw); err != nil {
			return err
		}
		pretty, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		if *out == "" {
			fmt.Println(string(pretty))
			return nil
		}
		if err := os.WriteFile(*out, append(pretty, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *out, len(pretty)+1)
		return nil
	case "apply":
		if len(args) < 2 {
			return errors.New("config apply needs a file")
		}
		body, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		// Rejected early rather than posted and refused, so a typo names its
		// own line instead of coming back as a 400.
		if !json.Valid(body) {
			return fmt.Errorf("%s is not valid JSON", args[1])
		}
		if err := c.post("/api/config", body); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "applied %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

// optFloat is a float flag that knows whether it was given at all.
//
// The daemon draws the same distinction and for the same reason: every field of
// policyPatch is a pointer so that "absent" and "set to zero" cannot be
// confused, because a client sending only a label change would otherwise clear
// every shaping value to 0. A CLI that collapses them has the same bug one
// layer out.
//
// It replaces a -1 sentinel, which had two faults. It printed "(default -1)"
// against every numeric flag in the help, reading as though minus one Mbps were
// the default; and `-down -1` was silently taken as "unset", so a typo did
// nothing at all rather than being refused.
type optFloat struct {
	val float64
	set bool
}

// String returns "" when unset, which is what stops flag.PrintDefaults from
// printing a "(default ...)" for a value that has none.
func (f *optFloat) String() string {
	if f == nil || !f.set {
		return ""
	}
	return strconv.FormatFloat(f.val, 'g', -1, 64)
}

func (f *optFloat) Set(s string) error {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %q", s)
	}
	if v < 0 {
		return fmt.Errorf("must not be negative, got %v", v)
	}
	f.val, f.set = v, true
	return nil
}

// apply writes the value only if the flag was actually given.
func (f *optFloat) apply(dst *float64) {
	if f.set {
		*dst = f.val
	}
}

// cmdShape sets what a device gets. The box's whole purpose, and the one
// command here that changes anything on a live network.
func cmdShape(c *client, args []string) error {
	fs := flag.NewFlagSet("shape", flag.ExitOnError)
	var down, up, delay, jitter, loss, burst optFloat
	fs.Var(&down, "down", "downlink cap in Mbps (0 = unlimited)")
	fs.Var(&up, "up", "uplink cap in Mbps (0 = unlimited)")
	fs.Var(&delay, "delay", "added latency in ms, ONE direction")
	fs.Var(&jitter, "jitter", "randomise delay by +/- this many ms")
	fs.Var(&loss, "loss", "packet loss, 0-100")
	fs.Var(&burst, "burst", "mean loss burst length in packets; 1 is uniform loss")
	dir := fs.String("dir", "down", "which direction -delay/-jitter/-loss/-burst apply to: down, up or both")
	clear := fs.Bool("clear", false, "remove all conditioning, including any distance model")
	if helpWanted(args) {
		fmt.Fprint(os.Stderr, "boactl shape <mac|label> [flags] -- condition one device's traffic\n\n")
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nThe device is matched by MAC, or by any unique part of its label or\n"+
			"hostname; an ambiguous match is refused rather than guessed. Delay is per\n"+
			"direction, so -dir both gives roughly twice the -delay value as round trip.\n")
		return nil
	}
	if len(args) == 0 {
		return errors.New("shape needs a MAC, or part of a device label")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	// A shape call always sends enabled=true, because asking to condition a
	// device implies turning it on. That makes a bare `boactl shape <dev>` a
	// trap: with no flags it would quietly enable a DISABLED device and put its
	// stored policy back into force, having been asked to change nothing.
	changed := *clear
	fs.Visit(func(*flag.Flag) { changed = true })
	if !changed {
		return fmt.Errorf("shape %s: nothing to change; pass a flag, or -clear to remove conditioning "+
			"(boactl shape -h)", args[0])
	}

	cl, err := findClient(c, args[0])
	if err != nil {
		return err
	}

	// Start from what is already imposed, so setting one knob does not silently
	// drop the others.
	d, u := cl.Policy.Down, cl.Policy.Up
	if *clear {
		d, u = boa.Shape{}, boa.Shape{}
	}
	down.apply(&d.RateMbps)
	up.apply(&u.RateMbps)
	// Delay is per direction and a round trip crosses both, so -dir both gives
	// the client roughly TWICE the -delay value as RTT. Named rather than
	// assumed, because that surprise is the reason the flag exists.
	apply := func(s *boa.Shape) {
		delay.apply(&s.DelayMs)
		jitter.apply(&s.JitterMs)
		loss.apply(&s.LossPct)
		burst.apply(&s.LossBurst)
	}
	switch *dir {
	case "down":
		apply(&d)
	case "up":
		apply(&u)
	case "both":
		apply(&d)
		apply(&u)
	default:
		return fmt.Errorf("-dir must be down, up or both, not %q", *dir)
	}

	enabled := true
	rev := cl.Policy.Rev
	patch := map[string]any{
		// The revision this change is based on. The daemon refuses the write if
		// someone else has edited this device since, rather than clobbering
		// them -- so a stale terminal loses the race instead of the operator.
		"base_revision": &rev,
		"enabled":       &enabled,
		"down":          &d,
		"up":            &u,
	}
	if *clear {
		// A distance model IS conditioning, and zeroing Down/Up does not touch
		// it -- the model is the stored input and the shapes it implies are
		// derived per tick. Without this, -clear reported "down - up -" on a
		// device the kernel was still holding at 171 Mbps, which is precisely
		// the lie the FROM column was added to stop.
		//
		// An explicit null, not an omission: the field is RawMessage server-side
		// so that absent means "leave the model alone" and null means "remove
		// it". Omitting it here would have been the former.
		patch["rssi"] = json.RawMessage("null")
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if err := c.patch("/api/devices/"+cl.MAC+"/policy", body); err != nil {
		return err
	}
	fmt.Printf("%s  down %s  up %s\n", cl.MAC, shapeSummary(d), shapeSummary(u))
	if *clear && cl.RssiRun != nil {
		fmt.Fprintf(os.Stderr, "also removed the distance model that was driving it (%.0f dBm)\n",
			cl.RssiRun.Dbm)
	}
	// A run is not policy, and clearing policy does not stop one. Saying so
	// matters because the device will still be conditioned a second later and
	// the obvious conclusion would be that this command failed.
	if cl.PatternRun != nil && cl.PatternRun.State == "running" {
		fmt.Fprintf(os.Stderr, "NOTE: pattern %q is still playing and keeps driving this device; "+
			"stop it before this takes effect\n", cl.PatternRun.Name)
	}
	if cl.Sweep != nil && cl.Sweep.State == "running" {
		fmt.Fprintln(os.Stderr, "NOTE: a ladder sweep is running and owns the downlink cap "+
			"until it finishes")
	}
	fmt.Fprintln(os.Stderr, "verify it reached the kernel with: boactl probe")
	return nil
}

// findClient resolves a MAC, or a case-insensitive fragment of a label or
// hostname. An ambiguous fragment is an error rather than a guess: picking one
// of two devices silently is how the wrong television gets throttled.
func findClient(c *client, want string) (boa.Client, error) {
	var s boa.Snapshot
	if err := c.get("/api/state", &s); err != nil {
		return boa.Client{}, err
	}
	needle := strings.ToLower(want)
	var hits []boa.Client
	for _, cl := range s.Clients {
		if strings.EqualFold(cl.MAC, want) {
			return cl, nil
		}
		// A PARTIAL mac counts too. probe identifies devices by their last four
		// octets to keep its lines readable, so refusing "19:1f:d1" here meant
		// the tool would not accept an identifier it had just printed itself.
		// Copying one command's output into the next is the obvious thing to
		// try, and it failed.
		if strings.Contains(strings.ToLower(cl.MAC), needle) ||
			strings.Contains(strings.ToLower(cl.Label), needle) ||
			strings.Contains(strings.ToLower(cl.Hostname), needle) {
			hits = append(hits, cl)
		}
	}
	switch len(hits) {
	case 0:
		return boa.Client{}, fmt.Errorf("no device matches %q; boactl devices lists them", want)
	case 1:
		return hits[0], nil
	default:
		var names []string
		for _, h := range hits {
			names = append(names, fmt.Sprintf("%s (%s)", h.MAC, orDash(h.Label)))
		}
		return boa.Client{}, fmt.Errorf("%q matches %d devices: %s", want, len(hits), strings.Join(names, ", "))
	}
}

// --- formatting -------------------------------------------------------------

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncate shortens by RUNES, not bytes. Device labels are operator-set and
// routinely non-ASCII, and slicing a byte at a time cuts a multi-byte rune in
// half, emitting invalid UTF-8 that a terminal draws as a replacement box.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// shapeSummary renders one direction the way an operator says it out loud.
func shapeSummary(s boa.Shape) string {
	if s.IsClean() {
		return "-"
	}
	var parts []string
	if s.RateMbps > 0 {
		parts = append(parts, fmt.Sprintf("%.4gM", s.RateMbps))
	}
	if s.DelayMs > 0 {
		d := fmt.Sprintf("%.4gms", s.DelayMs)
		if s.JitterMs > 0 {
			d += fmt.Sprintf("±%.4g", s.JitterMs)
		}
		parts = append(parts, d)
	}
	if s.LossPct > 0 {
		l := fmt.Sprintf("%.3g%%loss", s.LossPct)
		if s.Bursty() {
			l += fmt.Sprintf("/burst%.3g", s.LossBurst)
		}
		parts = append(parts, l)
	}
	if s.ReorderPct > 0 {
		parts = append(parts, fmt.Sprintf("%.3g%%reord", s.ReorderPct))
	}
	if s.CorruptPct > 0 {
		parts = append(parts, fmt.Sprintf("%.3g%%corrupt", s.CorruptPct))
	}
	return strings.Join(parts, " ")
}

// enforced reports what the KERNEL says it is doing, which is not always what
// the policy asked for. CapMbps is read back from tc rather than echoed from
// the request, so a disagreement here is the finding.
func enforced(cl boa.Client) string {
	if cl.DownCounters.CapMbps == 0 && cl.UpCounters.CapMbps == 0 {
		return "-"
	}
	return fmt.Sprintf("down %.4gM up %.4gM", cl.DownCounters.CapMbps, cl.UpCounters.CapMbps)
}

// effectiveShapes is what is ACTUALLY being imposed on a device, and which of
// the four possible sources decided it.
//
// Reading Policy.Down alone is wrong and quietly so. A distance model, a ladder
// sweep and a pattern each drive the shapes per tick and are deliberately never
// written back to the policy -- storing a model's output beside its input is how
// the two come to disagree -- so a device conditioned by any of them has a
// policy that reads perfectly clean while the kernel holds it at 171 Mbps.
// Measured on the box 2026-09-08, where a phone under a -62 dBm distance model
// showed `down -` in this tool and `cap_mbps 171.1` in the same payload.
//
// The precedence mirrors Engine.desired() in state.go, in the same order,
// because any other order would be a second opinion about what the box is
// doing: policy, then RSSI (both directions), then a sweep (downlink only),
// then a pattern (both).
func effectiveShapes(cl boa.Client) (down, up boa.Shape, source string) {
	down, up, source = cl.Policy.Down, cl.Policy.Up, "policy"
	if !cl.Policy.Enabled {
		// Disabled means "do not condition", not "do not measure".
		down, up, source = boa.Shape{}, boa.Shape{}, "off"
	}
	if cl.RssiRun != nil {
		down, up, source = cl.RssiRun.Down, cl.RssiRun.Up, "rssi"
	}
	if cl.Sweep != nil && cl.Sweep.State == "running" {
		down.RateMbps = cl.Sweep.CapMbps
		source = "sweep"
	}
	if cl.PatternRun != nil && cl.PatternRun.State == "running" {
		down, up, source = cl.PatternRun.Down, cl.PatternRun.Up, "pattern"
	}
	if down.IsClean() && up.IsClean() && source == "policy" {
		source = "-"
	}
	return down, up, source
}
