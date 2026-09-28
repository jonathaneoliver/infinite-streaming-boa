# Shared by openwrt-boa-install.sh, openwrt-boa-feed.sh and target.sh.
# Sourced, never run.
#
# CONTRACT: the caller defines `box`, which runs a shell command on the device.
# That is the only thing these functions know about how the device is reached,
# so the same code serves an SSH connection, a jump through a VM host, and a
# connection pinned to one interface.
#
# WHY ANY OF THIS EXISTS. OpenWrt's board profile for a Raspberry Pi sets LAN on
# eth0 and NO WAN at all -- `/etc/board.json` on a Pi 5 reads
# `"network": {"lan": {"device": "eth0"}}` and nothing else. A Cudy and a
# generic x86 both get a WAN from their profiles, so this is the one target
# where a box fresh from a flash can reach nothing, and every install needs a
# package feed. Measured on raspberrypi,5-model-b, OpenWrt 25.12.5, 2026-09-28.

# THE ONBOARD PORT IS NEVER THE WAN, and that is forced rather than chosen:
# board.d makes eth0 the LAN, LuCI is reachable only from LAN, so that is where
# the operator's computer has to be. The WAN is therefore a USB adapter.
#
# CARRIER IS ONLY READABLE ON AN INTERFACE THAT IS UP. An unconfigured one is
# administratively down and reads carrier=0 -- or fails the read outright --
# whatever the cable is doing. That cost an hour on 2026-09-28: two live cables
# were reported as unplugged, and only the link lights on the adapters said
# otherwise. So bring every USB port up before believing any of them.
boa_wan_candidates() {
	box '
		for p in /sys/class/net/eth*; do
			n=${p##*/}
			readlink -f "$p/device" 2>/dev/null | grep -q usb || continue
			ip link set "$n" up 2>/dev/null
		done
		sleep 2
		for p in /sys/class/net/eth*; do
			n=${p##*/}
			readlink -f "$p/device" 2>/dev/null | grep -q usb || continue
			[ "$(cat "$p/carrier" 2>/dev/null)" = 1 ] && echo "$n"
		done' 2>/dev/null
}

# Does the device have a usable route to a package feed? This is the question
# that actually matters -- an address is not connectivity, and apk needs DNS.
boa_has_feed_access() {
	box 'apk update >/dev/null 2>&1' 2>/dev/null
}

boa_wan_advice() {
	cat >&2 <<'EOF'
    Plug the cable from your router into one of the USB ethernet adapters.
    The Pi's BUILT-IN socket is the LAN: that is where your computer goes, and
    where the setup page is served. OpenWrt gives a Pi no WAN of its own.
EOF
}

# Configure `wan` on one interface and say whether it actually worked. Leaves
# the interface configured on success; removes it again on failure, so probing
# a second candidate starts from a clean config rather than two wans.
boa_try_wan() {
	local iface="$1"
	box "uci set network.wan=interface
	     uci set network.wan.device='$iface'
	     uci set network.wan.proto='dhcp'
	     uci commit network
	     /etc/init.d/network reload >/dev/null 2>&1" >/dev/null 2>&1 || return 1
	# A lease, a default route and a name that resolves -- all three, because
	# any one of them alone has been true on a box that could not reach a feed.
	local i
	for i in 1 2 3 4 5 6 7 8 9 10; do
		if box "ip -4 addr show '$iface' 2>/dev/null | grep -q 'inet ' &&
		        ip route | grep -q '^default' &&
		        nslookup downloads.openwrt.org >/dev/null 2>&1"; then
			return 0
		fi
		sleep 2
	done
	box 'uci -q delete network.wan; uci commit network
	     /etc/init.d/network reload >/dev/null 2>&1' >/dev/null 2>&1 || true
	return 1
}

# Find a WAN and configure it. Echoes the interface it settled on.
#
# ONE CANDIDATE IS NOT A GUESS -- it is the only USB port with a live cable, on
# a board whose other port must be the LAN. TWO ARE NOT GUESSED EITHER: each is
# tried, and the one that reaches a feed wins. Nothing here depends on
# interface NUMBERING, which follows USB enumeration order and moves when a
# dongle is replugged -- measured, moving an adapter to the onboard USB3 port
# renamed it.
boa_configure_wan() {
	local cands iface
	cands="$(boa_wan_candidates)"
	[ -n "$cands" ] || {
		printf 'no USB ethernet adapter on this device has a live cable.\n' >&2
		boa_wan_advice
		return 1
	}
	for iface in $cands; do
		if boa_try_wan "$iface"; then
			printf '%s\n' "$iface"
			return 0
		fi
	done
	printf 'none of these reached a package feed: %s\n' "$(echo "$cands" | tr '\n' ' ')" >&2
	boa_wan_advice
	return 1
}
