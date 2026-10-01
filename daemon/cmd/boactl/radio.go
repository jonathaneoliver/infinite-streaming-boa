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
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

func radioUsage() {
	fmt.Fprint(os.Stderr, `boactl radio <iface> <verb> [flags] -- act on one radio, and everyone on it

  scan [-apply]           listen to the band; -apply moves to the quietest
                          channel found, which drops clients on some hardware
  channel -to N [-width M] [-restart]
                          move the radio. Announced (802.11h) where the driver
                          does it, so clients FOLLOW; -restart forces the
                          teardown, which drops everyone without telling them
  power on|off [-dur S]   rfkill. Clients are told NOTHING -- the transmitter
                          simply stops. -dur turns it off and back after S
  ap on|off [-deauth]     hostapd closes the BSS, transmitter still on, so the
                          departure IS announced. -deauth sends the frame
  deauth-all              drop every station; the radio keeps serving
  link-all deauth|disassoc
                          the same, choosing the 802.11 frame: disassoc is the
                          weaker of the two
  steer [-to IFACE] [-mode M]
                          ASK every client to move to another radio (802.11v).
                          -to defaults to the box's other radio. -mode is what
                          the request says: suggest (default), imminent,
                          terminate, or insist -- the only one that drops a
                          client, disassociating any still here after 5s
  gather [-pin S]         move every other radio's clients ONTO this one and
                          pin them by denying the alternatives
  evict [-pin S]          the mirror: empty this radio and deny it
  txpower <dBm|auto>      set transmit power live; nobody is dropped. Refused
                          on drivers known to ignore it
  role scanner|ap         make the radio listen-only, or give it back to
                          serving. Going listen-only drops its clients

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
	// A string because it names two kinds of thing: a channel number for
	// channel, a radio for steer. Parsed per verb below.
	to := fs.String("to", "", "channel: the channel to move to; steer: the radio to steer to")
	mode := fs.String("mode", "", "steer: suggest (default), imminent, terminate or insist")
	width := fs.Int("width", 0, "channel: width in MHz (20, 40, 80); default 20")
	restart := fs.Bool("restart", false,
		"channel: force the down-and-up move even where the radio could announce it")
	dur := fs.Float64("dur", 0, "power: seconds to stay off before coming back")
	deauth := fs.Bool("deauth", false, "ap: send a deauthentication so clients are told")
	pin := fs.Float64("pin", 0, "gather/evict: seconds to hold the deny lists")

	// The verb's value is a positional, because "power off", "txpower 15" and
	// "role scanner" read as the thing they do and "power -on=false" does not.
	var arg string
	rest := args[2:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		arg, rest = rest[0], rest[1:]
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
		ch, err := strconv.Atoi(*to)
		if err != nil || ch <= 0 {
			return errors.New("radio channel needs -to N, e.g. boactl radio wlan-usb channel -to 149")
		}
		return radioChannel(c, iface, ch, *width, serving, *restart)
	case "power":
		return radioPower(c, iface, onOffArg(arg), *dur, serving)
	case "ap":
		return radioAP(c, iface, onOffArg(arg), *deauth, serving)
	case "deauth-all":
		return radioDeauthAll(c, iface, serving)
	case "link-all":
		return radioLinkAll(c, iface, arg, serving)
	case "steer":
		return radioSteer(c, iface, *to, *mode, serving)
	case "gather", "evict":
		return radioGatherEvict(c, iface, verb, *pin, serving)
	case "txpower":
		return radioTxPower(c, iface, arg)
	case "role":
		return radioRole(c, iface, arg, serving)
	default:
		return fmt.Errorf("unknown radio verb %q (try: scan, channel, power, ap, deauth-all, "+
			"link-all, steer, gather, evict, txpower, role)", verb)
	}
}

// onOffArg keeps power and ap to the two values they take, so "power of" is
// refused as a missing argument rather than read as "on".
func onOffArg(arg string) string {
	if arg == "on" || arg == "off" {
		return arg
	}
	return ""
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

// radioChannel moves a radio, and says which of the two ways it went.
//
// The warning before the request is CONDITIONAL now, because the cost is: a
// driver that announces the switch keeps every client, and one that cannot
// drops them all. Warning about a drop that will not happen trains the reader
// to ignore the warning that matters.
func radioChannel(c *client, iface string, ch, width, serving int, restart bool) error {
	if restart {
		warnServing(iface, serving, "they will be dropped and must rediscover the AP")
	}
	path := fmt.Sprintf("/api/bridge/radios/%s/move-channel?channel=%d", iface, ch)
	if width > 0 {
		path += fmt.Sprintf("&width=%d", width)
	}
	if restart {
		path += "&mode=restart"
	}
	var res struct {
		Iface           string  `json:"iface"`
		Channel         int     `json:"channel"`
		WidthMHz        int     `json:"width_mhz"`
		Method          string  `json:"method"`
		OutageSec       float64 `json:"outage_sec"`
		Stations        int     `json:"stations"`
		StationsDropped int     `json:"stations_dropped"`
	}
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	if res.Method == "announce" {
		fmt.Printf("%s  channel %d at %d MHz  (announced; %d station(s) asked to follow)\n",
			res.Iface, res.Channel, res.WidthMHz, res.Stations)
		fmt.Fprintln(os.Stderr, "802.11h CHAN_SWITCH: nobody was dropped. Which clients actually\n"+
			"followed is in the event log about ten seconds later")
		return nil
	}
	fmt.Printf("%s  channel %d at %d MHz  (restarted; %d station(s) dropped, %.1fs out)\n",
		res.Iface, res.Channel, res.WidthMHz, res.StationsDropped, res.OutageSec)
	fmt.Fprintln(os.Stderr, "down, set, up: the clients were told nothing and must rediscover the AP")
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

// radioLinkAll is deauth-all with the frame chosen. Disassociation is the
// weaker of the two and some clients treat it differently -- which is the
// point of being able to send either.
func radioLinkAll(c *client, iface, kind string, serving int) error {
	if kind == "" {
		return errors.New("radio link-all needs deauth or disassoc, e.g. boactl radio wlan-usb link-all disassoc")
	}
	warnServing(iface, serving, "all of them are dropped, and may come straight back")
	// The kind goes through unchecked: the daemon names the values it accepts,
	// and a second list here would be one more thing to keep in step.
	var res struct {
		Kind     string `json:"kind"`
		Stations int    `json:"stations"`
	}
	if err := c.postJSON("/api/bridge/radios/"+iface+"/link-all?kind="+url.QueryEscape(kind), nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  link-all %s  %d station(s)\n", iface, res.Kind, res.Stations)
	fmt.Fprintln(os.Stderr, "the radio is still serving; watch them return with: boactl events -follow")
	return nil
}

// radioSteer asks every client on a radio to move. A REQUEST in three of its
// four modes, exactly as link steer is for one client: a refusal is a result.
// Only insist disconnects anybody, and then the client picks where it lands,
// so `to` is what was asked for rather than where anyone went.
func radioSteer(c *client, iface, to, mode string, serving int) error {
	if mode == "insist" {
		warnServing(iface, serving, "any still here after 5s are disassociated and choose their own AP")
	}
	q := url.Values{}
	if to != "" {
		q.Set("to", to)
	}
	if mode != "" {
		q.Set("mode", mode)
	}
	path := "/api/bridge/radios/" + iface + "/steer"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var res struct {
		To    string `json:"to"`
		Asked int    `json:"asked"`
		Mode  string `json:"mode"`
	}
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  steer → %s  mode=%s  asked=%d\n", iface, res.To, res.Mode, res.Asked)
	fmt.Fprintln(os.Stderr, "each client answers on its own schedule and may decline; a refusal is a\n"+
		"result, not a failure. Watch for it with: boactl events -follow")
	return nil
}

// radioTxPower sets transmit power on the phy. Nobody is dropped, and a driver
// known to ignore the setting is refused by the daemon rather than reported as
// done -- the mt7921u case, where it would otherwise read as working.
func radioTxPower(c *client, iface, dbm string) error {
	if dbm == "" {
		return errors.New("radio txpower needs dBm or auto, e.g. boactl radio wlan-usb txpower 10")
	}
	var res struct {
		Auto bool     `json:"auto"`
		DBm  *float64 `json:"dbm"`
	}
	if err := c.postJSON("/api/bridge/radios/"+iface+"/txpower?dbm="+url.QueryEscape(dbm), nil, &res); err != nil {
		return err
	}
	if res.Auto || res.DBm == nil {
		fmt.Printf("%s  txpower auto (the driver decides)\n", iface)
		return nil
	}
	fmt.Printf("%s  txpower %g dBm\n", iface, *res.DBm)
	return nil
}

// radioRole turns a radio into a listen-only scanner or back into an access
// point. A scanner serves nobody, so making one drops its clients; either way
// the daemon restarts to pick up the new port, which takes about a second.
func radioRole(c *client, iface, as string, serving int) error {
	if as != "scanner" && as != "ap" {
		return errors.New("radio role needs scanner or ap, e.g. boactl radio wlan-usb role scanner")
	}
	if as == "scanner" {
		warnServing(iface, serving, "its access point stops and they are dropped")
	}
	var res struct {
		Role string `json:"role"`
		Now  string `json:"now"`
	}
	if err := c.postJSON("/api/bridge/radios/"+iface+"/role?as="+as, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  role %s  (now %s)\n", iface, res.Role, res.Now)
	fmt.Fprintln(os.Stderr, "the daemon restarts to pick up the new port; boactl bridge shows it in a second or two")
	return nil
}
