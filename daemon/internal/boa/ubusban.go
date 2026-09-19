package boa

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

/*
 * Mirroring boa's deny list into OpenWrt's own ban list.
 *
 * boa holds a client off a radio with hostapd's runtime DENY_ACL, through the
 * control socket. That works on OpenWrt, but nothing else on the box can see
 * it: OpenWrt's tools read bans from hostapd's ubus object (`list_bans`), and
 * steering daemons such as usteer act through the same object. So every ban
 * boa places is also placed there, with `del_client` and a ban_time.
 *
 * The deny list stays authoritative. The ubus ban cannot be lifted early --
 * the object has no unban -- so it is only ever a mirror, and it is kept
 * short: ubusBanMax, and never longer than boa's own hold. A deadzone of 10s
 * must stay a 10s outage; a mirror that outlived it would lengthen the very
 * thing being measured. A hold boa lifts early (a gather whose clients have
 * all landed) can leave the mirror in force for what remains of it, which is
 * what capping it at ubusBanMax bounds.
 *
 * Only with -openwrt (Config.OpenWrt); anywhere else the deny list is all
 * there is.
 */

// ubusBanMax is the longest a mirrored ban is placed for.
const ubusBanMax = 15 * time.Second

// denyACLAdd holds mac off iface for hold: DENY_ACL, which boa lifts itself,
// then the ubus mirror. Only the DENY_ACL can fail the call.
func (e *Engine) denyACLAdd(iface, mac string, hold time.Duration) error {
	if err := e.denyACLOn(iface, "ADD", mac); err != nil {
		return err
	}
	if !e.cfg.OpenWrt {
		return nil
	}
	if err := ubusBan(iface, mac, hold); err != nil {
		// Loud, once per ban: the client is held off either way, but OpenWrt's
		// view of the radio is now missing a ban boa believes it placed.
		log.Printf("ubus ban mirror %s on %s: %v", mac, iface, err)
	}
	return nil
}

// ubusBan places mac on iface's hostapd ban list through ubus.
func ubusBan(iface, mac string, hold time.Duration) error {
	arg, ok := ubusBanArg(mac, hold)
	if !ok {
		return nil
	}
	out, err := exec.Command("ubus", "call", "hostapd."+iface, "del_client", arg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ubus call hostapd.%s del_client: %v: %s",
			iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ubusBanArg is the del_client argument for a mirrored ban, and false when
// there is nothing worth placing.
//
// deauth is false and deliberately so: DENY_ACL has already disconnected the
// client if it was associated here, so hostapd finds no station and sends
// nothing. Were it still associated, a disassociation is the gentler of the
// two, and the callers kick it themselves in the way they have chosen.
// ban_time is milliseconds, measured on OpenWrt 25.12: a 4000 ban appeared in
// list_bans and was gone 5s later.
func ubusBanArg(mac string, hold time.Duration) (string, bool) {
	d := min(hold, ubusBanMax)
	if d < time.Second {
		return "", false
	}
	return fmt.Sprintf(`{"addr":%q,"deauth":false,"reason":5,"ban_time":%d}`,
		mac, d.Milliseconds()), true
}
