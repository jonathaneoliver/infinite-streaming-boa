package boa

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Group A link events: per-client Wi-Fi association control, driven through
// hostapd's control socket. These act on the CLIENT rather than on its packets
// -- a deauthentication takes the link down, which netem cannot express -- so
// they are the one impairment that reaches a player's path-state logic
// (NWPathMonitor and the like) rather than only its throughput estimate. See
// issue #135.
//
// Only the hostapd-served radio exposes a control socket. When the onboard
// Broadcom radio serves the AP it is run by NetworkManager, which offers no
// such control, so LinkControlAvailable reports false and every action fails
// loudly rather than silently doing nothing -- a silent no-op is exactly the
// failure this codebase keeps getting bitten by.

// hostapdCtrlDir must match ctrl_interface in scripts/customize.sh's boa.conf.
// hostapd names one socket per AP interface inside it, e.g. /var/run/hostapd/wlan-usb.
const hostapdCtrlDir = "/var/run/hostapd"

func hostapdSocket(iface string) string { return filepath.Join(hostapdCtrlDir, iface) }

var macRE = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// validMAC guards the control command: the MAC reaches hostapd as a bare word
// in a socket message, so a value with a space or newline in it could smuggle a
// second command. Only a canonical lower-case MAC is ever sent.
func validMAC(mac string) bool { return macRE.MatchString(mac) }

// hostapdAvailable reports whether hostapd is exposing a control socket for
// iface right now.
func hostapdAvailable(iface string) bool {
	if iface == "" {
		return false
	}
	fi, err := os.Stat(hostapdSocket(iface))
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// deauthCommand and disassocCommand build the raw control-interface verbs. A
// reason of 0 omits the code (hostapd uses its own default); a non-zero reason
// is an IEEE 802.11 reason code, e.g. 5 = "AP cannot handle all associated
// stations", which some clients weight more heavily than a plain deauth.
func deauthCommand(mac string, reason int) string   { return linkCommand("DEAUTHENTICATE", mac, reason) }
func disassocCommand(mac string, reason int) string { return linkCommand("DISASSOCIATE", mac, reason) }

func linkCommand(verb, mac string, reason int) string {
	if reason > 0 {
		return fmt.Sprintf("%s %s reason=%d", verb, mac, reason)
	}
	return verb + " " + mac
}

// hostapdCmd sends one command to hostapd's control socket -- an AF_UNIX
// datagram socket -- and returns its reply. The client binds its own temporary
// socket to receive the answer, exactly as hostapd_cli does.
func hostapdCmd(iface, cmd string) (string, error) {
	sock := hostapdSocket(iface)
	// An ABSTRACT local socket (leading "@"), not a /tmp path. The daemon runs
	// with systemd PrivateTmp, so a socket it creates under /tmp is invisible to
	// hostapd -- and hostapd's reply, sent to that path, is silently dropped,
	// which shows up only as a read timeout (and only on hardware; a host build
	// has no PrivateTmp). Abstract sockets live in the NETWORK namespace, which
	// the daemon and hostapd share, so the reply arrives regardless of the
	// private /tmp. Nothing on disk, so nothing to clean up.
	local := fmt.Sprintf("@boa-hostapd-%d-%d", os.Getpid(), time.Now().UnixNano())
	conn, err := net.DialUnix("unixgram",
		&net.UnixAddr{Name: local, Net: "unixgram"},
		&net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		return "", fmt.Errorf("hostapd control socket %s: %w", sock, err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return "", fmt.Errorf("hostapd write: %w", err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("hostapd read: %w", err)
	}
	return string(buf[:n]), nil
}

// LinkControlAvailable reports whether per-client link events can be driven now
// -- hostapd is serving the AP and exposing its control socket. Demo mode
// returns true so the interface can be developed against.
func (e *Engine) LinkControlAvailable() bool { return e.cfg.Demo || e.anyLinkControl() }

// anyLinkControl reports whether ANY watched radio exposes a control socket.
// With two radios serving, one may be hostapd-driven and the other not, and
// hiding the controls because the first one checked cannot drive them would
// withdraw a capability the box actually has.
func (e *Engine) anyLinkControl() bool {
	for _, w := range e.cfg.WlanPorts {
		if hostapdAvailable(w) {
			return true
		}
	}
	return false
}

// radioFor names the radio a client is associated to, which is the one whose
// control socket can act on it.
//
// The tick's map first, then a live probe of each radio: a link action can
// arrive for a station that associated since the last tick, and failing on a
// one-second-old cache would be an intermittent failure of exactly the kind
// that gets blamed on the radio. Falls back to the primary so a single-radio
// box behaves as it always did.
func (e *Engine) radioFor(mac string) string {
	e.mu.RLock()
	w, ok := e.stationRadio[mac]
	e.mu.RUnlock()
	if ok && w != "" {
		return w
	}
	for _, w := range e.cfg.WlanPorts {
		if _, found := StationDump(w)[mac]; found {
			return w
		}
	}
	return e.cfg.PrimaryWlan()
}

// LinkDeauth deauthenticates a client, taking its link down; it reassociates on
// its own. LinkDisassoc is the softer 802.11 disassociation. reason is an
// optional IEEE 802.11 reason code (0 to omit).
func (e *Engine) LinkDeauth(mac string, reason int) error {
	m := normMAC(mac)
	return e.linkAction(m, deauthCommand(m, reason))
}

func (e *Engine) LinkDisassoc(mac string, reason int) error {
	m := normMAC(mac)
	return e.linkAction(m, disassocCommand(m, reason))
}

// fireLink executes one LinkFire the Player scheduled (drop/nudge, or a
// deadzone tick which is a repeated deauth). Best-effort: a failure is logged
// once rather than silently swallowed, but a pattern keeps running.
func (e *Engine) fireLink(f LinkFire) {
	if !e.LinkControlAvailable() {
		return
	}
	var err error
	switch f.Kind {
	case LinkDeadzone:
		err = e.LinkDeadzone(f.MAC, f.DurSec) // clean deny-ACL block
	case LinkNudge:
		if f.DurSec > 0 {
			e.LinkFlap(f.MAC, LinkNudge, f.DurSec)
		} else {
			err = e.LinkDisassoc(f.MAC, 0)
		}
	default: // drop
		if f.DurSec > 0 {
			e.LinkFlap(f.MAC, LinkDrop, f.DurSec)
		} else {
			err = e.LinkDeauth(f.MAC, 0)
		}
	}
	if err != nil {
		log.Printf("link %s %s: %v", f.Kind, f.MAC, err)
	}
}

// LinkFlap repeatedly kicks a client for durSec -- deauth for drop, disassoc for
// nudge -- so the link keeps dropping and reconnecting, LEAKING traffic in the
// gaps. That is the point: a flapping/unstable link, the complement of a
// deadzone's clean deny-ACL block. Runs in the background. See issue #135.
func (e *Engine) LinkFlap(mac, kind string, durSec float64) {
	if e.cfg.Demo || !e.LinkControlAvailable() {
		return
	}
	mac = normMAC(mac)
	if !validMAC(mac) || durSec <= 0 {
		return
	}
	go func() {
		deadline := time.Now().Add(time.Duration(durSec * float64(time.Second)))
		for time.Now().Before(deadline) {
			var err error
			if kind == LinkNudge {
				err = e.LinkDisassoc(mac, 0)
			} else {
				err = e.LinkDeauth(mac, 0)
			}
			if err != nil {
				log.Printf("flap %s %s: %v", kind, mac, err)
			}
			time.Sleep(time.Second)
		}
	}()
}

// denyACL adds or removes a MAC from hostapd's runtime deny list. With the
// default macaddr_acl=0 (accept unless denied), a denied MAC cannot associate.
// op is "ADD" or "DEL".
func (e *Engine) denyACL(op, mac string) error {
	reply, err := hostapdCmd(e.radioFor(mac), "DENY_ACL "+op+"_MAC "+mac)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return fmt.Errorf("DENY_ACL %s %s: %s", op, mac, strings.TrimSpace(reply))
	}
	return nil
}

// clearDenyACL empties the runtime deny list at startup, so a deadzone that was
// in force when the daemon last died does not strand a client off the AP
// forever. Best-effort: no radio, nothing to clear.
func (e *Engine) clearDenyACL() {
	if e.cfg.Demo {
		return
	}
	// Every radio: a deadzone in force when the daemon died could have been
	// applied through either socket, and clearing only one strands the client
	// off that AP for good.
	for _, w := range e.cfg.WlanPorts {
		if !hostapdAvailable(w) {
			continue
		}
		if _, err := hostapdCmd(w, "DENY_ACL CLEAR"); err != nil {
			log.Printf("clear deny ACL on %s: %v", w, err)
		}
	}
}

// LinkDeadzone holds a client off the AP for durSec: it is added to the deny
// ACL (so it CANNOT re-associate for the window) and deauthenticated once to
// kick it off now. This is a true sustained outage -- unlike a repeated deauth,
// no traffic leaks through the reconnect gaps -- long enough to drain a player's
// buffer. The ban is lifted in the background; the call returns once it is in
// force. See issue #135.
func (e *Engine) LinkDeadzone(mac string, durSec float64) error {
	if !e.LinkControlAvailable() {
		return fmt.Errorf("link control unavailable: hostapd is not serving the AP")
	}
	mac = normMAC(mac)
	if !validMAC(mac) {
		return fmt.Errorf("not a MAC address: %s", mac)
	}
	if durSec < 1 || durSec > 300 {
		return fmt.Errorf("deadzone duration must be 1-300 seconds")
	}
	if e.cfg.Demo {
		return nil
	}
	if err := e.denyACL("ADD", mac); err != nil {
		return err
	}
	_ = e.LinkDeauth(mac, 0) // kick it off now; the ACL keeps it off
	go func() {
		time.Sleep(time.Duration(durSec * float64(time.Second)))
		if err := e.denyACL("DEL", mac); err != nil {
			log.Printf("deadzone lift %s: %v", mac, err)
		}
	}()
	return nil
}

// linkAction sends one control command on behalf of a specific client. The MAC
// is passed separately from the command so the right radio's socket is chosen;
// with two radios serving, the command text alone does not say where to send it.
func (e *Engine) linkAction(mac, cmd string) error {
	if e.cfg.Demo {
		return nil // no radio here; demo exists to develop the interface
	}
	reply, err := hostapdCmd(e.radioFor(mac), cmd)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return fmt.Errorf("hostapd rejected %q: %s", cmd, strings.TrimSpace(reply))
	}
	return nil
}
