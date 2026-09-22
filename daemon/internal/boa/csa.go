package boa

import (
	"fmt"
	"strings"
	"time"
)

/*
 * The channel switch announcement, and which radios really do it.
 *
 * 802.11h lets an access point count a move down in its beacons so clients
 * FOLLOW it without reassociating. The alternative is to take the access point
 * down, set the channel and bring it back: clients are told nothing, notice the
 * beacons stopped, rescan and rejoin. The difference is the whole of what a
 * client experiences, which is why the result says which one happened.
 *
 * MEASURED 2026-09-22 on a Cudy TR3000 (192.168.0.23, OpenWrt 25.12.5, hostapd
 * v2.12-devel, both built-in radios on mt798x-wmac, three real clients):
 *
 *	phy1  40 -> 149, 80MHz   AP-CSA-FINISHED freq=5745 dfs=0   3/3 kept
 *	phy1  149 -> 40, 80MHz   AP-CSA-FINISHED freq=5200         2/3 kept
 *	phy0  1 -> 11, 20MHz     AP-CSA-FINISHED freq=2462         --
 *	phy1  x6 alternating     all OK, all landed                3/3 kept each
 *
 * 8 switches, 24 client-crossings, 1 drop -- and that one client reassociated
 * four seconds later and then rode six consecutive switches cleanly. 23/24,
 * recorded rather than rounded up. hostapd logged "driver starting channel
 * switch" and "driver had channel switch" every time, which the mt7921u never
 * produces.
 *
 * ATTEMPT AND LEARN, NEVER ASK. `iw phy phy1 info` lists channel_switch among
 * the supported commands ON THE mt7921u, which refuses every form of it (#154).
 * The capability bit returns the opposite of the truth, so only issuing the
 * command answers the question -- the same conclusion txpower.go reached about
 * transmit power, for the same reason.
 */

// csaRefusedBy names drivers that answer FAIL to CHAN_SWITCH despite
// advertising channel_switch in `iw phy info`.
//
// mt7921u, #154: every form of the verb is refused. Attempting it there costs
// a failed command before the fallback, which fails fast, but starting from
// what is already known saves even that.
var csaRefusedBy = map[string]string{
	"mt7921u": "the mt7921u driver refuses CHAN_SWITCH despite advertising it (#154)",
}

// ChanSwitchAbility is what a channel move will do to the clients on a radio,
// so the interface can say so BEFORE the button is pressed rather than after.
type ChanSwitchAbility struct {
	// Announces is false only where a refusal is KNOWN -- from the list above
	// or from an attempt that failed while the fallback proved the target
	// legal. Unknown reads as true, because the move will in fact be attempted
	// by announcement and will fall back by itself if it cannot be.
	Announces bool   `json:"announces"`
	Why       string `json:"why,omitempty"`
}

// csaRefusedReason says why this driver cannot announce a switch: from the
// static list, or from an attempt that proved it.
func (e *Engine) csaRefusedReason(driver string) string {
	if why, bad := csaRefusedBy[driver]; bad {
		return why
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.csaRefused[driver]
}

// noteCSARefused records a driver that answered FAIL to a switch it should
// have been able to make.
//
// KEYED BY DRIVER, not by interface, for the reason noteTxPowerIgnored already
// gives: the fault is in the code handling the request, so a second adapter on
// the same driver has it too.
//
// In memory, so it is forgotten on a restart. A learned refusal written to disk
// would outlive the driver bug that caused it and keep a capable radio on the
// slow path with no way back anyone would think to look for.
func (e *Engine) noteCSARefused(driver, why string) {
	if driver == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.csaRefused == nil {
		e.csaRefused = map[string]string{}
	}
	e.csaRefused[driver] = why
}

// chanSwitchAbility is the badge's fact for one radio.
func (e *Engine) chanSwitchAbility(driver string) *ChanSwitchAbility {
	why := e.csaRefusedReason(driver)
	return &ChanSwitchAbility{Announces: why == "", Why: why}
}

// csaSettle is how long the switch is given to finish before the channel is
// read back. Five beacons at the usual 100ms interval is half a second;
// measured on the Cudy, STATUS already reported the new channel by then. The
// rest is slack.
const csaSettle = 2 * time.Second

// followWait is how long clients are given to be counted as having followed.
// The one drop measured on the Cudy showed up three seconds after the switch
// and had reassociated four seconds later, so a shorter window would have
// called a returning client a lost one.
const followWait = 10 * time.Second

// announceChannel asks a radio to move by announcement, and CONFIRMS it.
//
// An OK from hostapd means the command was accepted, not that the radio
// arrived: a switch can be acknowledged and then abandoned by the driver, and
// that failure looks exactly like a client that simply followed. So the
// channel is read back, and anything else is an error the caller falls back
// from.
func (e *Engine) announceChannel(iface string, ch apChannel, widthMHz int) error {
	cmd, err := chanSwitchCommand(ch, widthMHz)
	if err != nil {
		return err
	}
	reply, err := hostapdCmd(iface, cmd)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return fmt.Errorf("hostapd refused %q: %s", cmd, strings.TrimSpace(reply))
	}
	time.Sleep(csaSettle)
	st, err := hostapdCmd(iface, "STATUS")
	if err != nil {
		return fmt.Errorf("%s announced the switch and then could not be asked where it is: %w", iface, err)
	}
	if now := atoiSafe(parseHostapdKV(st)["channel"]); now != ch.Channel {
		return fmt.Errorf("%s accepted the announcement and stayed on channel %d", iface, now)
	}
	return nil
}

// watchFollowers reports, per client, who rode the switch through.
//
// The observation this box exists to make, and it is free: a station the access
// point still knows AND whose connected time did not reset never left. One that
// is present with a fresh connected time noticed the beacons move and rejoined
// -- which is what an announcement is supposed to avoid -- and one that is gone
// did not come back at all.
//
// PER CLIENT rather than per radio, because they genuinely differ: measured on
// the Cudy, one client of three dropped a switch that the other two rode, and
// then rode the next six itself.
//
// Runs in the background: the operator asked for a channel, and holding the
// response open for ten seconds to report on clients would make a seamless move
// feel slower than the outage it replaced.
func (e *Engine) watchFollowers(iface string, before map[string]*Station, channel int) {
	if len(before) == 0 {
		return
	}
	time.Sleep(followWait)
	after := StationDump(iface)
	rode, rejoined, lost := 0, 0, 0
	for mac, was := range before {
		switch now, still := after[mac]; {
		case !still:
			lost++
			e.logEvent(EventWarning, iface, mac,
				"%s did not follow %s to channel %d and has not come back", mac, iface, channel)
		case now.ConnectedSec < was.ConnectedSec:
			rejoined++
			e.logEvent(EventWarning, iface, mac,
				"%s lost the announced switch to channel %d and reassociated %ds ago",
				mac, channel, now.ConnectedSec)
		default:
			rode++
		}
	}
	msg := fmt.Sprintf("%s: %d of %d clients followed the switch to channel %d",
		iface, rode, len(before), channel)
	if rejoined > 0 {
		msg += fmt.Sprintf(", %d reassociated", rejoined)
	}
	if lost > 0 {
		msg += fmt.Sprintf(", %d did not return", lost)
	}
	kind := EventRadio
	if rode < len(before) {
		kind = EventWarning
	}
	e.logEvent(kind, iface, "", "%s", msg)
}
