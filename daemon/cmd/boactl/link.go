package main

// link acts on the ASSOCIATION rather than on the traffic: knock a client off,
// hold it off, ask it to move, ask it what it hears.
//
// This is what closes the loop on the test plan README already describes --
// "t+30s steer to the other radio, t+90s disable AP for 8s, t+150s deauth,
// t+210s hold at 25 metres" -- against which a QoE report is diffed. Every step
// of it could be captured with `events -follow` and `history` and none of it
// could be CAUSED without a browser, which is most of the way to an unattended
// run and then stopping.
//
// Two of these are REQUESTS, not instructions. A steer and a measurement ask
// the client to do something and it is free to decline; whether a given device
// honours one is the behaviour under test, so a refusal is a RESULT and not an
// error. What these report is whether the request was delivered.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func cmdLink(c *client, args []string) error {
	if helpWanted(args) || len(args) == 0 {
		fmt.Fprint(os.Stderr, `boactl link <mac|label> <action> [flags] -- act on a client's association

  deauth [-reason N]     knock it off; it reconnects on its own schedule
  disassoc [-reason N]   as deauth, but the weaker of the two 802.11 frames
  deadzone [-dur S]      hold it off the AP for S seconds (default 10, 1-300),
                         a sustained outage long enough to stall a stream
  steer                  ASK it to move to the box's other radio
  measure                ASK it to report what it hears from the other radios
                         (802.11k beacon request)

steer and measure are requests the client may refuse, and a refusal is a result
worth recording, not a failure. Both answer asynchronously: what comes back
here is whether the request was delivered, and the answer lands on the device's
own record a few seconds later.

Link control needs hostapd to be serving the radio the client is on.
`)
		return nil
	}
	if len(args) < 2 {
		return errors.New("link needs a device and an action, e.g. boactl link \"Apple TV\" deauth")
	}
	target, action := args[0], args[1]

	fs := flag.NewFlagSet("link "+action, flag.ExitOnError)
	reason := fs.Int("reason", 0, "802.11 reason code, 1-65535 (deauth and disassoc)")
	dur := fs.Float64("dur", 0, "seconds to hold the client off, 1-300 (deadzone)")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}

	cl, err := findClient(c, target)
	if err != nil {
		return err
	}
	// Every one of these acts on an association, so a device that is not
	// present has none. The daemon would refuse, but naming it here says which
	// of the two things is wrong.
	if !cl.Present {
		return fmt.Errorf("%s is not currently associated; there is no link to act on", cl.MAC)
	}

	path := "/api/devices/" + cl.MAC + "/link/"
	var query []string
	switch action {
	case "deauth", "disassoc":
		path += action
		if *reason > 0 {
			query = append(query, fmt.Sprintf("reason=%d", *reason))
		}
	case "deadzone":
		path += "deadzone"
		if *dur > 0 {
			query = append(query, fmt.Sprintf("dur=%v", *dur))
		}
	case "steer":
		path += "steer"
		// The box computes both ends itself, but it can only steer somewhere:
		// saying so before the request beats a 400 that reads like a fault.
		if cl.SteerTo == "" {
			return fmt.Errorf("%s has nowhere to be steered to -- it is on the only radio serving, "+
				"or the box has one radio", cl.MAC)
		}
	case "measure":
		path += "measure"
	default:
		return fmt.Errorf("unknown link action %q (try: deauth, disassoc, deadzone, steer, measure)", action)
	}
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}

	// The reply carries what actually happened -- which radio a steer was aimed
	// at, how many measurements were requested -- and printing it is the point:
	// these are asynchronous, so this is the only synchronous evidence there is.
	var res map[string]any
	if err := c.postJSON(path, nil, &res); err != nil {
		return err
	}
	fmt.Printf("%s  %s", cl.MAC, action)
	for _, k := range []string{"from", "to", "dur_sec", "requested", "reason", "delivered"} {
		if v, ok := res[k]; ok {
			fmt.Printf("  %s=%v", k, v)
		}
	}
	fmt.Println()

	switch action {
	case "steer", "measure":
		fmt.Fprintln(os.Stderr, "the client answers on its own schedule and may decline; a refusal is a\n"+
			"result, not a failure. Watch for it with: boactl events -follow")
	default:
		fmt.Fprintln(os.Stderr, "watch the client leave and return with: boactl events -follow")
	}
	return nil
}
