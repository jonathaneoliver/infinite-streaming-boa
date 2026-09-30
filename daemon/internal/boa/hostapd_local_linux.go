//go:build linux

package boa

// hostapdLocalAddr names the socket a control-interface client binds to receive
// hostapd's reply, and returns what to call once it is closed.
//
// On Linux a leading "@" selects the ABSTRACT namespace: the socket has no
// filesystem entry, so release has nothing to do. That is also why it is
// abstract at all -- the daemon runs with systemd PrivateTmp, so a socket it
// created under /tmp would be invisible to hostapd and every reply would be
// dropped. Abstract sockets live in the network namespace, which the daemon
// and hostapd share. See hostapd_local_other.go for the platforms without one.
func hostapdLocalAddr(name string) (addr string, release func()) {
	return "@" + name, func() {}
}
