package main

// The radio verbs, which are AP-WIDE: none of them names a client, and every
// one of them acts on everybody associated to that radio. The blast radius is
// the reason they live here rather than under `link`, and the reason each one
// says how many stations it is about to affect before it acts.
//
// Three of these look interchangeable and are not, which is exactly why the
// daemon keeps them apart:
//
//	power off   rfkill. The transmitter stops. Clients are told NOTHING and
//	            discover the absence for themselves.
//	ap off      hostapd closes the BSS with the transmitter still on, so the
//	            departure is announced (and -deauth decides whether a frame
//	            goes out at all).
//	deauth-all  the radio keeps serving; every station is dropped and free to
//	            come straight back.
//
// Folding them into one control would hide the only variable an operator is
// trying to change.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

func radioUsage() {
	fmt.Fprint(os.Stderr, `boactl radio <iface> <verb> [flags] -- act on one radio, and everyone on it

  scan [-apply]           listen to the band; -apply moves to the quietest
                          channel found, which drops clients on some hardware
  channel -to N [-width M]
                          move by taking the AP down and back; DROPS everyone
  power on|off [-dur S]   rfkill. Clients are told NOTHING -- the transmitter
                          simply stops. -dur turns it off and back after S
  ap on|off [-deauth]     hostapd closes the BSS, transmitter still on, so the
                          departure IS announced. -deauth sends the frame
  deauth-all              drop every station; the radio keeps serving
  gather [-pin S]         move every other radio's clients ONTO this one and
                          pin them by denying the alternatives
  evict [-pin S]          the mirror: empty this radio and deny it

boactl bridge lists the radios and what each is serving.
`)
}

func cmdRadio(c *client, args []string) error {
	if helpWanted(args) || len(args) == 0 {
		radioUsage()
		return nil
	}
	if len(args) < 2 {
		return errors.New("radio needs an interface and a verb, e.g. boactl radio wlan-usb scan")
	}
	iface, verb := args[0], args[1]

	fs := flag.NewFlagSet("radio "+verb, flag.ExitOnError)
	apply := fs.Bool("apply", false, "scan: move to the quietest channel found")
	to := fs.Int("to", 0, "channel: the channel to move to")
	width := fs.Int("width", 0, "channel: width in MHz (20, 40, 80); default 20")
	dur := fs.Float64("dur", 0, "power: seconds to stay off before coming back")
	deauth := fs.Bool("deauth", false, "ap: send a deauthentication so clients are told")
	pin := fs.Float64("pin", 0, "gather/evict: seconds to hold the deny lists")

	// on|off is a positional for power and ap, because "power off" reads as the
	// thing it does and "power -on=false" does not.
	var onOff string
	rest := args[2:]
	if len(rest) > 0 && (rest[0] == "on" || rest[0] == "off") {
		onOff, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}

	// How many stations are on this radio right now. Every verb here affects
	// all of them, so it is said before acting rather than discovered after.
	serving := radioStations(c, iface)

	switch verb {
	case "scan":
		return radioScan(c, iface, *apply, serving)
	case "channel":
		if *to == 0 {
			return errors.New("radio channel needs -to, e.g. boactl radio wlan-usb channel -to 149")
		}
		return radioChannel(c, iface, *to, *width, serving)
	case "power":
		return radioPower(c, iface, onOff, *dur, serving)
	case "ap":
		return radioAP(c, iface, onOff, *deauth, serving)
	case "deauth-all":
		return radioDeauthAll(c, iface, serving)
	case "gather", "evict":
		return radioGatherEvict(c, iface, verb, *pin, serving)
	default:
		return fmt.Errorf("unknown radio verb %q (try: scan, channel, power, ap, deauth-all, gather, evict)", verb)
	}
}

// radioStations counts the clients the box currently sees on one radio. Best
// effort: a count that cannot be read is reported as unknown rather than as
// zero, because "nobody is affected" is the one wrong answer that would make an
// operator run the command without thinking.
func radioStations(c *client, iface string) int {
	var s boa.Snapshot
	if err := c.get("/api/state", &s); err != nil {
		return -1
	}
	n := 0
	for _, cl := range s.Clients {
		if cl.Present && cl.RadioOn != nil && cl.RadioOn.Iface == iface {
			n++
		}
	}
	return n
}

func warnServing(iface string, n int, what string) {
	switch {
	case n < 0:
		fmt.Fprintf(os.Stderr, "could not read how many clients %s is serving; %s\n", iface, what)
	case n > 0:
		fmt.Fprintf(os.Stderr, "%s is serving %d client(s); %s\n", iface, n, what)
	}
}

func radioScan(c *client, iface string, apply bool, serving int) error {
	// A scan is free on a radio that can listen while serving and costs an
	// outage on one that cannot -- the daemon tries the free path first and
	// reports which it took. Only -apply is guaranteed to move anybody.
	if apply {
		warnServing(iface, serving, "moving to the quietest channel drops them")
	}
	path := "/api/bridge/radios/" + iface + "/scan"
	if apply {
		path += "?apply=1"
	}
	// The daemon's own type, not a local guess at its tags. Writing the struct
	// by hand here produced `was`/`best` against a payload that says
	// `was_channel`/`best_channel`, which decodes to zero and prints a
	// confident "was=0 best=0" -- the shape of wrong answer this whole tool
	// exists to stop.
	var res boa.ScanResult
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  scanned %s in %.1fs: %d channel(s), %d AP(s)\n",
		res.Iface, res.Band, res.ScanSec, len(res.Channels), len(res.APs))
	if res.Applied {
		fmt.Printf("%s  moved %d → %d\n", res.Iface, res.Was, res.Now)
	}
	if res.OutageSec > 0 {
		fmt.Fprintf(os.Stderr, "the scan cost a %.1fs outage on this radio\n", res.OutageSec)
	}
	if res.Note != "" {
		fmt.Fprintln(os.Stderr, res.Note)
	}
	if !apply && res.Best != 0 && res.Best != res.Was {
		fmt.Fprintf(os.Stderr, "channel %d is quieter than %d; -apply would move there, dropping clients\n",
			res.Best, res.Was)
	}
	return nil
}

func radioChannel(c *client, iface string, ch, width, serving int) error {
	warnServing(iface, serving, "they will be dropped and must rediscover the AP")
	path := fmt.Sprintf("/api/bridge/radios/%s/move-channel?channel=%d", iface, ch)
	if width > 0 {
		path += fmt.Sprintf("&width=%d", width)
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
	fmt.Fprintln(os.Stderr, "802.11h CHAN_SWITCH would let clients follow and is refused by both\n"+
		"drivers here (#154), so this is the route that works: down, set, up")
	return nil
}

func radioPower(c *client, iface, onOff string, dur float64, serving int) error {
	if onOff == "" && dur == 0 {
		return errors.New("radio power needs on or off, e.g. boactl radio wlan-usb power off")
	}
	path := "/api/bridge/radios/" + iface + "/power"
	switch {
	case dur > 0:
		warnServing(iface, serving, fmt.Sprintf("the transmitter stops for %gs and they are told nothing", dur))
		path += fmt.Sprintf("?dur=%g", dur)
	case onOff == "off":
		warnServing(iface, serving, "the transmitter stops and they are told nothing")
		path += "?on=0"
	default:
		path += "?on=1"
	}
	var res map[string]any
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  %v\n", iface, res["action"])
	return nil
}

func radioAP(c *client, iface, onOff string, deauth bool, serving int) error {
	if onOff == "" {
		return errors.New("radio ap needs on or off, e.g. boactl radio wlan-usb ap off")
	}
	if onOff == "off" {
		warnServing(iface, serving, "the BSS closes with the transmitter still on")
	}
	path := "/api/bridge/radios/" + iface + "/ap?on="
	if onOff == "on" {
		path += "1"
	} else {
		path += "0"
	}
	if deauth {
		path += "&deauth=1"
	}
	var res map[string]any
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  ap %s%s\n", iface, onOff, map[bool]string{true: " (clients deauthenticated)"}[deauth])
	if !deauth && onOff == "off" {
		fmt.Fprintln(os.Stderr, "no deauthentication was sent, so clients discover the absence themselves;\n"+
			"pass -deauth to tell them")
	}
	return nil
}

func radioDeauthAll(c *client, iface string, serving int) error {
	warnServing(iface, serving, "all of them are dropped, and may come straight back")
	var res struct {
		Dropped int `json:"dropped"`
	}
	if err := c.postJSON("/api/bridge/radios/"+iface+"/deauth-all", nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  deauth-all  %d station(s) dropped\n", iface, res.Dropped)
	fmt.Fprintln(os.Stderr, "the radio is still serving; watch them return with: boactl events -follow")
	return nil
}

func radioGatherEvict(c *client, iface, verb string, pin float64, serving int) error {
	if verb == "gather" {
		fmt.Fprintf(os.Stderr, "moving every other radio's clients onto %s and denying them elsewhere\n", iface)
	} else {
		warnServing(iface, serving, "they are moved off and denied this radio")
	}
	path := "/api/bridge/radios/" + iface + "/" + verb
	if pin > 0 {
		path += fmt.Sprintf("?pin=%g", pin)
	}
	var res map[string]any
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	var parts []string
	for _, k := range []string{"moved", "denied", "pin_sec", "note"} {
		if v, ok := res[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	fmt.Printf("%s  %s  %s\n", iface, verb, strings.Join(parts, " "))
	// Neither is a request, so there is nothing for a client to refuse -- but
	// the deny list expires, and saying when is the difference between a
	// measurement and a mystery ten minutes later.
	fmt.Fprintln(os.Stderr, "the deny lists lift on their own; until they do, the clients cannot go back")
	return nil
}
