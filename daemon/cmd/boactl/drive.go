package main

// The commands that DRIVE the box rather than describe it: start a ladder
// sweep, play a timeline, move a radio. Every one of them changes a live
// network, and each says what it started and how to watch it, because the
// answer to "did that work" should never be "look at the web page".

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

// --- sweep ------------------------------------------------------------------

func cmdSweep(c *client, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	service := fs.String("service", "", "what the device is streaming, e.g. netflix (required to start)")
	stop := fs.Bool("stop", false, "stop the sweep running on this device")
	if helpWanted(args) {
		fmt.Fprint(os.Stderr, "boactl sweep <mac|label> [flags] -- measure a device's rendition ladder\n\n")
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nA sweep walks the downlink cap down and watches where the player settles,\n"+
			"so it OWNS the cap until it finishes and overrides stored policy for the\n"+
			"duration. It takes real time -- an hour of real streaming is what a good\n"+
			"ladder costs -- and the result is the one genuinely expensive thing in a\n"+
			"config export. A ladder belongs to a service, not to a device, which is\n"+
			"why -service is required.\n\nWatch it with: boactl devices, or boactl history\n")
		return nil
	}
	if len(args) == 0 {
		return errors.New("sweep needs a MAC, or part of a device label")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cl, err := findClient(c, args[0])
	if err != nil {
		return err
	}
	if *stop {
		if err := c.delete("/api/devices/" + cl.MAC + "/sweep"); err != nil {
			return err
		}
		fmt.Printf("%s  sweep stopped\n", cl.MAC)
		fmt.Fprintln(os.Stderr, "the device returns to its stored policy. The snapshot is rebuilt once a\n"+
			"second, so boactl devices checked immediately can still show the old run")
		return nil
	}
	if strings.TrimSpace(*service) == "" {
		return errors.New("sweep needs -service: name what the device is streaming, e.g. -service netflix. " +
			"A ladder belongs to a service, not to a device")
	}
	// Refused here rather than by the daemon, because the daemon's refusal
	// arrives after the caller believes a long-running measurement has begun.
	if cl.Sweep != nil && cl.Sweep.State == "running" {
		return fmt.Errorf("%s already has a sweep running (service %q); -stop it first",
			cl.MAC, cl.Sweep.Service)
	}
	body, err := json.Marshal(map[string]any{"service": *service})
	if err != nil {
		return err
	}
	if err := c.post("/api/devices/"+cl.MAC+"/sweep", body); err != nil {
		return err
	}
	fmt.Printf("%s  sweep started for %q\n", cl.MAC, *service)
	fmt.Fprintln(os.Stderr, "it owns the downlink cap until it finishes. Follow it with:\n"+
		"  boactl devices          the cap as it steps\n"+
		"  boactl events -follow   the rungs as they are found")
	return nil
}

// --- pattern ----------------------------------------------------------------

func cmdPattern(c *client, args []string) error {
	if helpWanted(args) || len(args) == 0 {
		fmt.Fprint(os.Stderr, `boactl pattern <subcommand> -- play a timeline

  pattern list [<mac|label>]        the library; marks the one a device has loaded
  pattern play <mac|label> [-name N] [-service S] [-stretch X]
                                    load a pattern if -name is given, then play
  pattern stop <mac|label>          stop playback, returning to stored policy
  pattern play -bridge              the box's own timeline, applied to everything
  pattern stop -bridge

A pattern drives both directions along its timeline and is applied per tick,
never written to the policy -- so stopping it restores what the operator set,
and a daemon that dies mid-run comes back conditioning the device as it was.
`)
		return nil
	}

	switch args[0] {
	case "list":
		return patternList(c, args[1:])
	case "play", "stop":
		return patternPlay(c, args[0], args[1:])
	default:
		return fmt.Errorf("unknown pattern subcommand %q (try: list, play, stop)", args[0])
	}
}

func patternList(c *client, args []string) error {
	path := "/api/patterns"
	if len(args) > 0 {
		cl, err := findClient(c, args[0])
		if err != nil {
			return err
		}
		// The list is single-select per device, so asking on a device's behalf
		// is what marks the live row.
		path += "?mac=" + cl.MAC
	}
	// An envelope, not a bare array -- as with /api/events.
	var doc struct {
		Patterns []struct {
			Name     string  `json:"name"`
			Builtin  bool    `json:"builtin"`
			DurSec   float64 `json:"dur_sec"`
			Keys     int     `json:"keys"`
			Loop     bool    `json:"loop"`
			Selected bool    `json:"selected"`
			// Unavailable says why a pattern cannot be used as listed -- a
			// built-in is generated from a device's ladder, so it has no
			// duration and cannot be played until a device is named. Dropping
			// this would list rows that look playable and are not.
			Unavailable string `json:"unavailable"`
		} `json:"patterns"`
	}
	if err := c.get(path, &doc); err != nil {
		return err
	}
	if *jsonFlag {
		return emitJSON(doc)
	}
	if len(doc.Patterns) == 0 {
		fmt.Fprintln(os.Stderr, "no patterns; the library is empty")
		return nil
	}
	entries := doc.Patterns
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	fmt.Printf("%-2s %-24s %-9s %8s %5s %-5s %s\n",
		"", "NAME", "KIND", "SECONDS", "KEYS", "LOOP", "NOTE")
	blocked := 0
	for _, e := range entries {
		kind := "saved"
		if e.Builtin {
			kind = "built-in"
		}
		mark := " "
		if e.Selected {
			mark = "*"
		}
		if e.Unavailable != "" {
			blocked++
		}
		fmt.Printf("%-2s %-24s %-9s %8.1f %5d %-5s %s\n",
			mark, truncate(e.Name, 24), kind, e.DurSec, e.Keys, yesNo(e.Loop), e.Unavailable)
	}
	if blocked > 0 && !strings.Contains(path, "mac=") {
		fmt.Fprintf(os.Stderr, "%d pattern(s) need a device before they mean anything: "+
			"boactl pattern list <mac|label>\n", blocked)
	}
	fmt.Fprintln(os.Stderr, "* = loaded on that device")
	return nil
}

func patternPlay(c *client, verb string, args []string) error {
	fs := flag.NewFlagSet("pattern "+verb, flag.ExitOnError)
	bridge := fs.Bool("bridge", false, "act on the box's own timeline rather than one device's")
	name := fs.String("name", "", "load this pattern before playing it")
	service := fs.String("service", "", "which ladder a built-in is built from")
	stretch := fs.Float64("stretch", 0, "scale the timeline in time, keeping its shape and rates")

	// -bridge takes no device, so the first argument is only a device when it
	// is not a flag.
	var target string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, rest = args[0], args[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}

	if *bridge {
		if target != "" {
			return fmt.Errorf("-bridge acts on the whole box, so it takes no device (got %q)", target)
		}
		path := "/api/bridge/pattern/play"
		if verb == "stop" {
			if err := c.delete(path); err != nil {
				return err
			}
			fmt.Println("bridge pattern stopped")
			return nil
		}
		if err := c.post(path, nil); err != nil {
			return err
		}
		fmt.Println("bridge pattern playing")
		fmt.Fprintln(os.Stderr, "it conditions the box itself; boactl state shows the run")
		return nil
	}

	if target == "" {
		return fmt.Errorf("pattern %s needs a device, or -bridge for the box's own timeline", verb)
	}
	cl, err := findClient(c, target)
	if err != nil {
		return err
	}
	if verb == "stop" {
		if err := c.delete("/api/devices/" + cl.MAC + "/pattern/play"); err != nil {
			return err
		}
		fmt.Printf("%s  pattern stopped\n", cl.MAC)
		fmt.Fprintln(os.Stderr, "the device returns to its stored policy. The snapshot is rebuilt once a\n"+
			"second, so boactl devices checked immediately can still show the old run")
		return nil
	}

	if *name != "" {
		rev := cl.Policy.Rev
		sel := map[string]any{"base_revision": &rev, "name": *name}
		if *service != "" {
			sel["service"] = *service
		}
		if *stretch > 0 {
			sel["stretch"] = *stretch
		}
		body, err := json.Marshal(sel)
		if err != nil {
			return err
		}
		if err := c.post("/api/devices/"+cl.MAC+"/pattern/select", body); err != nil {
			return err
		}
	} else if cl.Policy.Pattern == nil {
		// The daemon says "author one before playing it", which is true and
		// unhelpful from a terminal: name the way out.
		return fmt.Errorf("%s has no pattern loaded; pass -name (boactl pattern list %s)",
			cl.MAC, cl.MAC)
	}
	if err := c.post("/api/devices/"+cl.MAC+"/pattern/play", nil); err != nil {
		return err
	}
	fmt.Printf("%s  pattern playing\n", cl.MAC)
	fmt.Fprintln(os.Stderr, "follow the playhead with: boactl devices, or boactl history")
	return nil
}

// --- radio ------------------------------------------------------------------

func cmdRadio(c *client, args []string) error {
	fs := flag.NewFlagSet("radio", flag.ExitOnError)
	channel := fs.Int("channel", 0, "move to this channel")
	width := fs.Int("width", 0, "channel width in MHz (20, 40, 80); default 20")
	if helpWanted(args) {
		fmt.Fprint(os.Stderr, "boactl radio <iface> [flags] -- move a radio\n\n")
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nThis takes the access point DOWN and brings it back on the new channel.\n"+
			"Every client on that radio is dropped and must rediscover it; they are not\n"+
			"told. That is not a shortcut -- 802.11h CHAN_SWITCH, which would let them\n"+
			"follow, is refused by both drivers on this box (issue #154), and down-up is\n"+
			"what consumer routers do anyway.\n\nboactl bridge lists the radios.\n")
		return nil
	}
	if len(args) == 0 {
		return errors.New("radio needs an interface, e.g. boactl radio wlan-usb -channel 149")
	}
	iface := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *channel == 0 {
		return errors.New("radio needs -channel; nothing else about a radio is settable here yet")
	}

	// Say what it will cost BEFORE doing it, using the box's own count rather
	// than a guess. The response reports how many were actually dropped.
	var s boa.Snapshot
	if err := c.get("/api/state", &s); err == nil {
		on := 0
		for _, cl := range s.Clients {
			if cl.Present && cl.RadioOn != nil && cl.RadioOn.Iface == iface {
				on++
			}
		}
		if on > 0 {
			fmt.Fprintf(os.Stderr, "%s is serving %d client(s); they will be dropped and must rejoin\n",
				iface, on)
		}
	}

	path := fmt.Sprintf("/api/bridge/radios/%s/move-channel?channel=%d", iface, *channel)
	if *width > 0 {
		path += fmt.Sprintf("&width=%d", *width)
	}
	var res struct {
		Iface           string `json:"iface"`
		Channel         int    `json:"channel"`
		WidthMHz        int    `json:"width_mhz"`
		StationsDropped int    `json:"stations_dropped"`
	}
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  channel %d at %d MHz  (%d station(s) dropped)\n",
		res.Iface, res.Channel, res.WidthMHz, res.StationsDropped)
	fmt.Fprintln(os.Stderr, "clients rediscover it on their own schedule; boactl devices to watch them return")
	return nil
}
