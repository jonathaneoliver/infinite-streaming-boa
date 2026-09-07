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
	boxFlag  = flag.String("box", "", "box hostname or URL (default $BOA_BOX, then "+defaultBox+")")
	jsonFlag = flag.Bool("json", false, "emit the raw JSON payload instead of a summary")
)

func main() {
	flag.Usage = usage
	flag.Parse()

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

func run(c *client, args []string) error {
	switch args[0] {
	case "state":
		return cmdState(c)
	case "devices":
		return cmdDevices(c)
	case "bridge":
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
		return fmt.Errorf("unknown command %q (try: state, devices, bridge, events, survey, config, probe)", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `boactl -- drive an infinite-streaming-boa appliance

  boactl [flags] <command> [args]

Commands:
  state                 what the box is doing right now
  devices               one line per client, with what is imposed on it
  bridge                radios, channels, and how contested each one is
  events [-since N] [-follow]
                        what has happened, newest last
  survey <iface>        a radio's airtime counters
  config get [-o file]  export the box's configuration
  config apply <file>   send a configuration back
  probe [-ssh] [-settle 5s]
                        assert the box is really doing what it claims

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, "\nThe box defaults to $BOA_BOX, then %s.\n", defaultBox)
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
	fmt.Printf("%-17s %-20s %-7s %-9s %-16s %-16s %s\n",
		"MAC", "LABEL", "MEDIUM", "PRESENT", "DOWN", "UP", "ENFORCED")
	for _, cl := range s.Clients {
		label := cl.Label
		if label == "" {
			label = cl.Hostname
		}
		fmt.Printf("%-17s %-20s %-7s %-9s %-16s %-16s %s\n",
			cl.MAC, truncate(orDash(label), 20), cl.Medium, yesNo(cl.Present),
			shapeSummary(cl.Policy.Down), shapeSummary(cl.Policy.Up),
			enforced(cl))
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
	for _, e := range envelope.Events {
		fmt.Printf("%d\t%s\t%s\n", e.Seq, time.UnixMilli(e.At).Format("15:04:05"), e.Text)
	}
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

// cmdShape sets what a device gets. The box's whole purpose, and the one
// command here that changes anything on a live network.
func cmdShape(c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("shape needs a MAC, or part of a device label")
	}
	fs := flag.NewFlagSet("shape", flag.ExitOnError)
	down := fs.Float64("down", -1, "downlink cap in Mbps (0 = unlimited)")
	up := fs.Float64("up", -1, "uplink cap in Mbps (0 = unlimited)")
	delay := fs.Float64("delay", -1, "added latency in ms, ONE direction")
	jitter := fs.Float64("jitter", -1, "randomise delay by +/- this many ms")
	loss := fs.Float64("loss", -1, "packet loss, 0-100")
	burst := fs.Float64("burst", -1, "mean loss burst length in packets; 1 is uniform loss")
	dir := fs.String("dir", "down", "which direction -delay/-jitter/-loss/-burst apply to: down, up or both")
	clear := fs.Bool("clear", false, "remove all conditioning from this device")
	if err := fs.Parse(args[1:]); err != nil {
		return err
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
	if *down >= 0 {
		d.RateMbps = *down
	}
	if *up >= 0 {
		u.RateMbps = *up
	}
	// Delay is per direction and a round trip crosses both, so -dir both gives
	// the client roughly TWICE the -delay value as RTT. Named rather than
	// assumed, because that surprise is the reason the flag exists.
	apply := func(s *boa.Shape) {
		if *delay >= 0 {
			s.DelayMs = *delay
		}
		if *jitter >= 0 {
			s.JitterMs = *jitter
		}
		if *loss >= 0 {
			s.LossPct = *loss
		}
		if *burst >= 0 {
			s.LossBurst = *burst
		}
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
	body, err := json.Marshal(map[string]any{
		// The revision this change is based on. The daemon refuses the write if
		// someone else has edited this device since, rather than clobbering
		// them -- so a stale terminal loses the race instead of the operator.
		"base_revision": &rev,
		"enabled":       &enabled,
		"down":          &d,
		"up":            &u,
	})
	if err != nil {
		return err
	}
	if err := c.patch("/api/devices/"+cl.MAC+"/policy", body); err != nil {
		return err
	}
	fmt.Printf("%s  down %s  up %s\n", cl.MAC, shapeSummary(d), shapeSummary(u))
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
		if strings.Contains(strings.ToLower(cl.Label), needle) ||
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
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
