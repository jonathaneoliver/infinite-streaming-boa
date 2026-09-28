#!/usr/bin/env bash
# Prepares a Linux host to run the OpenWrt VM (target 5): installs the libvirt
# hook that builds br-client, and takes NetworkManager's hands off the USB
# adapters. The VM's equivalent of scripts/docker-host-net.sh. Issue #414.
#
#   sudo scripts/vm-host-net.sh install      # or: scripts/target.sh vm setup
#   sudo scripts/vm-host-net.sh uninstall
#
# Settings, from the environment at install time, written to /etc/default/boa-vm
# where the hook reads them on every domain start:
#
#   BOA_VM_DOMAIN     the libvirt domain the hook acts for (openwrt-x86)
#   BOA_VM_HOST_ADDR  the host's address on the guest's LAN (192.168.1.2/24)
#
# WHY THIS IS IN THE REPOSITORY. The hook is the VM's whole wired client side,
# and it lived only in /etc/libvirt/hooks/qemu on the one machine that ran it.
# The first version there named its adapter by a MAC-derived name, so a second
# dongle plugged in beside it was present, unmanaged and never bridged, and
# nothing said so.
#
# IDEMPOTENT. Running install twice leaves the same files. A hand-written boa
# hook found at /etc/libvirt/hooks/qemu is moved aside rather than deleted, and
# anybody else's hook there is left alone.
set -euo pipefail

HOOK=/etc/libvirt/hooks/qemu.d/boa-br-client
LEGACY_HOOK=/etc/libvirt/hooks/qemu
DEFAULTS=/etc/default/boa-vm
# Its own file, APPENDING with +=, not a rewrite of 99-boa-unmanaged.conf.
# docker-host-net.sh owns that name, and the VM once replaced it with a narrower
# copy that dropped the container's devices. With += both lists hold whichever
# file is present. Measured on NetworkManager 1.46: the entry is appended to
# the list the earlier file set.
NM_CONF=/etc/NetworkManager/conf.d/99-boa-vm-unmanaged.conf

log() { echo "boa-vm-host-net: $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

[ "$(id -u)" = 0 ] || die "must run as root"

write_hook() {
	install -d -m 0755 "$(dirname "$HOOK")"
	install -m 0755 /dev/stdin "$HOOK" <<'HOOK'
#!/bin/sh
# Written by scripts/vm-host-net.sh. Builds the OpenWrt VM's client-side bridge
# at domain start and removes it at release.
#
# WHY A HOOK AND NOT A BOOT-TIME SERVICE. The bridge only joins USB ethernet to
# one domain's NIC. Tying it to the domain's lifecycle means it cannot drift: it
# is rebuilt from scratch on every start, and nothing is left behind on a host
# that is not running the VM.
#
# NEVER FATAL. A missing dongle must cost only itself. Exiting non-zero here
# would abort the domain start, so an unplugged cable would look like a broken
# VM. Everything is logged instead, to the journal as libvirt-hook-boa.
set -u

DOMAIN_ARG=$1
OP=$2
BOA_VM_DOMAIN=openwrt-x86
BOA_VM_HOST_ADDR=192.168.1.2/24
[ -r /etc/default/boa-vm ] && . /etc/default/boa-vm
[ "$DOMAIN_ARG" = "$BOA_VM_DOMAIN" ] || exit 0

BRIDGE=br-client
log() { logger -t libvirt-hook-boa "$*"; }

# The wired client ports are DISCOVERED, not listed: any network device on the
# USB bus, the rule scripts/docker-attach.sh settled on after a second adapter
# was silently skipped for not being in a list. The host keeps its own NIC by
# construction, because that is on PCI. Radios are excluded: they are passed
# through to the guest, not bridged to it.
usb_eth_devices() {
	for path in /sys/class/net/*; do
		nic=$(basename "$path")
		[ -e "$path/device" ] || continue
		[ -e "$path/phy80211" ] && continue
		case "$(readlink -f "$path/device")" in
		*/usb[0-9]*) printf '%s\n' "$nic" ;;
		esac
	done
}

case "$OP" in
prepare)
	if ! ip link show "$BRIDGE" >/dev/null 2>&1; then
		ip link add name "$BRIDGE" type bridge || { log "could not create $BRIDGE"; exit 0; }
		log "created $BRIDGE"
	fi
	# NO IPv6 ON THE BRIDGE, set before it comes up. Once the guest bridges it to
	# the LAN, the host would take a SLAAC address here, mDNS would publish it,
	# and a Mac's SSH to the host would prefer it. Release deletes the bridge,
	# and every such session then hangs with no error. While the address
	# existed, IPv4 SSH to the host also stalled after key exchange. Measured
	# 2026-09-28.
	sysctl -qw "net.ipv6.conf.$BRIDGE.disable_ipv6=1" || log "WARNING: could not disable IPv6 on $BRIDGE"
	# The host's only business on this bridge: an address on the guest's LAN,
	# which reaches OpenWrt's own 192.168.1.1 before and after it is converted.
	ip addr replace "$BOA_VM_HOST_ADDR" dev "$BRIDGE" || log "WARNING: could not add $BOA_VM_HOST_ADDR to $BRIDGE"
	ip link set "$BRIDGE" up
	enslaved=0
	for nic in $(usb_eth_devices); do
		# LOUDLY, not silently: NetworkManager holding a port fights the bridge
		# for it, and the symptom is a client port that looks attached and
		# never carries anything.
		if ! nmcli -t -f DEVICE,STATE device 2>/dev/null | grep -q "^$nic:unmanaged$"; then
			log "WARNING: NetworkManager manages $nic; run scripts/vm-host-net.sh install"
		fi
		ip link set "$nic" up
		if ip link set "$nic" master "$BRIDGE"; then
			log "enslaved $nic to $BRIDGE"
			enslaved=$((enslaved + 1))
		else
			log "WARNING: could not enslave $nic to $BRIDGE"
		fi
	done
	[ "$enslaved" -gt 0 ] || log "WARNING: no wired client port; $DOMAIN_ARG starts without one"
	;;
release)
	if ip link show "$BRIDGE" >/dev/null 2>&1; then
		ip link set "$BRIDGE" down
		ip link delete "$BRIDGE" type bridge && log "removed $BRIDGE"
	fi
	;;
esac
exit 0
HOOK
	log "installed the hook at $HOOK"
}

write_defaults() {
	install -m 0644 /dev/stdin "$DEFAULTS" <<EOF
# Written by scripts/vm-host-net.sh; read by $HOOK on every domain start.
BOA_VM_DOMAIN=${BOA_VM_DOMAIN:-openwrt-x86}
BOA_VM_HOST_ADDR=${BOA_VM_HOST_ADDR:-192.168.1.2/24}
EOF
	log "settings in $DEFAULTS: domain ${BOA_VM_DOMAIN:-openwrt-x86}, host address ${BOA_VM_HOST_ADDR:-192.168.1.2/24}"
}

write_unmanaged() {
	# enx*/wlx* are the names NetworkManager gives USB network devices, and the
	# host's own NICs are enp*/wlp*, so these globs mean "every USB adapter"
	# without naming one -- the same spec docker-host-net.sh writes. br-client
	# too: otherwise NM reports it "connected (externally)" and treats it as
	# one of its own.
	install -D -m 0644 /dev/stdin "$NM_CONF" <<'EOF'
# Written by scripts/vm-host-net.sh. The OpenWrt VM owns these devices through
# br-client; NetworkManager must not configure them. += appends to any list set
# by an earlier file, such as docker-host-net.sh's 99-boa-unmanaged.conf.
[keyfile]
unmanaged-devices+=interface-name:enx*;interface-name:wlx*;interface-name:br-client
EOF
	log "NetworkManager told to leave the USB adapters and br-client alone"
	if command -v nmcli >/dev/null 2>&1; then
		nmcli general reload conf 2>/dev/null || systemctl reload NetworkManager
	fi
}

# libvirt reads the hooks directory when the daemon starts, so a new hook is not
# run until it restarts. Restarting it does not touch running domains.
restart_libvirt() {
	local unit
	for unit in libvirtd virtqemud; do
		if systemctl is-active --quiet "$unit"; then
			systemctl restart "$unit"
			log "restarted $unit so it picks up the hook"
		fi
	done
}

case "${1:-}" in
install)
	command -v virsh >/dev/null 2>&1 || die "libvirt is not installed"
	if [ -f "$LEGACY_HOOK" ] && grep -q libvirt-hook-boa "$LEGACY_HOOK"; then
		aside="$LEGACY_HOOK.boa-replaced-$(date +%Y%m%d%H%M%S)"
		mv "$LEGACY_HOOK" "$aside"
		log "moved the hand-written boa hook aside to $aside, so it does not run twice"
	fi
	write_defaults
	write_hook
	write_unmanaged
	restart_libvirt
	log "done. The next start of ${BOA_VM_DOMAIN:-openwrt-x86} builds br-client from this hook."
	;;
uninstall)
	rm -f "$HOOK" "$DEFAULTS" "$NM_CONF"
	command -v nmcli >/dev/null 2>&1 && { nmcli general reload conf 2>/dev/null || true; }
	restart_libvirt
	log "removed the hook, its settings and the NetworkManager config"
	;;
*)
	echo "usage: $0 install|uninstall" >&2
	exit 2
	;;
esac
