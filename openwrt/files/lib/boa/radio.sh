# Radio identity and roles, for the hotplug hook and the init script.
# Sourced, never run:  . /lib/boa/radio.sh
#
# PHY INDICES ARE NOT IDENTITY, and that is the whole reason this file exists.
# MEASURED on a Raspberry Pi 5, 2026-09-28: across one reboot the onboard
# brcmfmac moved from phy0 to phy1 and a USB mt7921u took phy0. Everything
# pinned to a phy NAME then pointed at different hardware --
# `boa.main.scan='phy0-scan'` had been the onboard radio and was now a dongle,
# so boa built its listen-only interface on a radio that was supposed to be
# serving, and no access point came up at all. Issue #387.
#
# Two things survive that renumbering, and both are used here:
#
#   the PATH   OpenWrt already binds each wireless.radioN to one
#              (`platform/.../mmc...` for the Pi's onboard radio,
#              `.../usb4/4-1/4-1.2/...` for a dongle). uci stayed correct
#              across the reboot above; only the phy index moved.
#   the MAC    a phy's permanent address, which is the hardware itself.
#
# So a role is RECORDED against a MAC and RESOLVED to a phy at use time.

# The permanent MAC of a phy, lower case, or "" if there is none.
boa_phy_mac() {
	local mac
	mac=$(cat "/sys/class/ieee80211/$1/macaddress" 2>/dev/null) || return 0
	printf '%s' "$mac" | tr 'A-Z' 'a-z'
}

# The phy that currently carries a MAC. The inverse of the above, and the one
# that makes a recorded role survive a renumbering.
boa_phy_for_mac() {
	local want="$1" p
	[ -n "$want" ] || return 1
	want=$(printf '%s' "$want" | tr 'A-Z' 'a-z')
	for p in /sys/class/ieee80211/*; do
		[ -e "$p" ] || continue
		if [ "$(boa_phy_mac "$(basename "$p")")" = "$want" ]; then
			basename "$p"
			return 0
		fi
	done
	return 1
}

boa_phy_driver() {
	basename "$(readlink -f "/sys/class/ieee80211/$1/device/driver" 2>/dev/null)" 2>/dev/null
}

# Is this phy on the USB bus? A dongle, rather than something soldered down.
boa_phy_is_usb() {
	readlink -f "/sys/class/ieee80211/$1/device" 2>/dev/null | grep -q '/usb[0-9]'
}

# The uci wifi-device section bound to a phy, matched on the path OpenWrt
# itself recorded. Empty when uci does not know this radio yet -- which is the
# normal state for a few seconds after a dongle is plugged in, before
# OpenWrt's own wifi-detect has run.
boa_radio_for_phy() {
	local phy="$1" want r p
	want=$(readlink -f "/sys/class/ieee80211/$phy/device" 2>/dev/null) || return 1
	want=${want#/sys/devices/}
	for r in $(uci show wireless 2>/dev/null | sed -n 's/^wireless\.\(radio[0-9]*\)=wifi-device/\1/p'); do
		p=$(uci -q get "wireless.$r.path")
		[ -n "$p" ] || continue
		case "$want" in
		*"$p"*) printf '%s' "$r"; return 0 ;;
		esac
	done
	return 1
}

# WHICH RADIO SHOULD SCAN.
#
# A scanner serves nobody and sweeps continuously, so the serving radios never
# leave their channel -- see scannerrole.go, which measured what that costs:
# an AP and a listening interface can coexist on these phys but share ONE
# channel, so sweeping with a serving radio drags it off channel. MEASURED
# here 2026-09-28: a client on the Pi's onboard radio while it swept both
# bands got 51 Mbit/s, against 442 on a dongle that was only serving.
#
# THE RULE IS NARROW ON PURPOSE. Only brcmfmac -- the radio soldered to a
# Raspberry Pi -- and only when something else can serve. The tempting general
# rule, "the non-USB radio scans", is WRONG on two of this repository's other
# targets: a Cudy's radios are all soldered down and all serve, and the x86
# guest's MT7915E is its BEST radio and its USB mt7921u its worst, so that rule
# would retire the good one. A driver this narrow can be widened by measurement
# later; a wrong general rule quietly ruins a box.
boa_scanner_phy() {
	local p phy n_serving=0 candidate=
	for p in /sys/class/ieee80211/*; do
		[ -e "$p" ] || continue
		phy=$(basename "$p")
		if [ "$(boa_phy_driver "$phy")" = brcmfmac ]; then
			candidate=$phy
		else
			n_serving=$((n_serving + 1))
		fi
	done
	# Nothing else can serve: a lone radio serves rather than scans, because a
	# box with no access point is not a box anyone can use.
	[ "$n_serving" -gt 0 ] || return 1
	[ -n "$candidate" ] || return 1
	printf '%s' "$candidate"
}

# Re-point boa.main.scan at the hardware it was recorded against. Called
# before boad starts; prints the list it settled on.
#
# boa.main.scan_mac is what makes this possible: boa.main.scan alone holds phy
# NAMES, and a name is exactly what moves.
#
# BOTH ARE LISTS. A box can have several scanners -- issue #351 found that
# treating boa.main.scan as a single value silently un-made one radio while
# making another -- so this resolves every recorded MAC and rebuilds the whole
# list. The MACs are a SET rather than a list aligned with the names by index:
# two lists that must stay in step is a bug waiting for the first radio that
# does not resolve.
#
# A MAC THAT RESOLVES TO NOTHING IS DROPPED, LOUDLY. That is a radio which was
# a scanner and is now unplugged, and keeping its name would have boad poll an
# interface that cannot exist. Silence here would be the half-working state
# this repository keeps finding.
# Record a phy as a scanner, in both lists, WITHOUT removing any other.
#
# `uci set` replaces, and replacing is how issue #351 un-made one radio while
# making another: a box with two scanners kept only the one set last. Both
# writers this file has are additive for that reason.
boa_scan_add() {
	local phy="$1" mac name have
	mac=$(boa_phy_mac "$phy")
	name="$phy-scan"
	have=$(uci -q get boa.main.scan)
	case " $have " in *" $name "*) ;; *) uci set boa.main.scan="${have:+$have }$name" ;; esac
	if [ -n "$mac" ]; then
		have=$(uci -q get boa.main.scan_mac)
		case " $have " in *" $mac "*) ;; *) uci set boa.main.scan_mac="${have:+$have }$mac" ;; esac
	fi
	uci commit boa
}

boa_resolve_scan() {
	local macs mac phy want= changed=0
	macs=$(uci -q get boa.main.scan_mac)
	[ -n "$macs" ] || return 1
	for mac in $macs; do
		if phy=$(boa_phy_for_mac "$mac"); then
			want="${want:+$want }$phy-scan"
		else
			logger -t boa "scanner $mac is not on this box any more; dropping it"
			changed=1
		fi
	done
	[ -n "$want" ] || return 1
	[ "$(uci -q get boa.main.scan)" = "$want" ] || changed=1
	if [ "$changed" = 1 ]; then
		uci set boa.main.scan="$want"
		uci commit boa
	fi
	printf '%s' "$want"
}
