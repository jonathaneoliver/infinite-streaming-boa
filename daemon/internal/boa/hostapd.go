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
func (e *Engine) LinkControlAvailable() bool {
	return e.cfg.Demo || hostapdAvailable(e.cfg.WlanPort)
}

// LinkDeauth deauthenticates a client, taking its link down; it reassociates on
// its own. LinkDisassoc is the softer 802.11 disassociation. reason is an
// optional IEEE 802.11 reason code (0 to omit).
func (e *Engine) LinkDeauth(mac string, reason int) error {
	return e.linkAction(deauthCommand(normMAC(mac), reason))
}

func (e *Engine) LinkDisassoc(mac string, reason int) error {
	return e.linkAction(disassocCommand(normMAC(mac), reason))
}

// fireLink executes one LinkFire the Player scheduled (drop/nudge, or a
// deadzone tick which is a repeated deauth). Best-effort: a failure is logged
// once rather than silently swallowed, but a pattern keeps running.
func (e *Engine) fireLink(f LinkFire) {
	if !e.LinkControlAvailable() {
		return
	}
	var err error
	if f.Kind == LinkNudge {
		err = e.LinkDisassoc(f.MAC, 0)
	} else {
		err = e.LinkDeauth(f.MAC, 0) // drop and deadzone both deauth
	}
	if err != nil {
		log.Printf("link %s %s: %v", f.Kind, f.MAC, err)
	}
}

// LinkDeadzone holds a client off the AP for durSec by deauthenticating it
// every second -- the single-radio implementation of a sustained outage, which
// (unlike a single drop) lasts long enough to drain a player's buffer. It runs
// in the background; the call returns as soon as the outage has started.
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
	go func() {
		deadline := time.Now().Add(time.Duration(durSec * float64(time.Second)))
		for time.Now().Before(deadline) {
			if err := e.LinkDeauth(mac, 0); err != nil {
				log.Printf("deadzone %s: %v", mac, err)
			}
			time.Sleep(time.Second)
		}
	}()
	return nil
}

func (e *Engine) linkAction(cmd string) error {
	if e.cfg.Demo {
		return nil // no radio here; demo exists to develop the interface
	}
	reply, err := hostapdCmd(e.cfg.WlanPort, cmd)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return fmt.Errorf("hostapd rejected %q: %s", cmd, strings.TrimSpace(reply))
	}
	return nil
}
