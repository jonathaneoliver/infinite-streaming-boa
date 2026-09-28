#!/usr/bin/env bash
#
# Brings the two boa targets on a Linux host up and down: the container
# (target 4) and the OpenWrt VM (target 5), plus the handover between them.
# docker-deploy.sh builds and ships; this starts, stops and resets what is
# already there. Issue #415.
#
#   scripts/target.sh status
#   scripts/target.sh container up|down
#   scripts/target.sh vm setup        # once per host: the libvirt hook (vm-host-net.sh)
#   scripts/target.sh vm up|down
#   scripts/target.sh vm reset [--no-install]
#   scripts/target.sh swap vm|container
#   scripts/target.sh cudy status
#   scripts/target.sh cudy reset [--no-install]
#
# The host comes from BOA_TARGET_HOST in .env. The VM settings are
# BOA_VM_DOMAIN, BOA_VM_BASE_IMAGE and BOA_VM_SDK_IMAGE; see .env.example.
#
# ONE AT A TIME, AND THAT IS ENFORCED. Both targets want the same hardware: the
# container moves the radios and the USB ethernet into its namespace, and the VM
# takes the PCI radio away from the host driver and the USB ethernet into
# br-client. Starting one while the other holds the hardware does not fail. It
# starts, and whichever side lost fails later in a way that looks like a driver
# fault. So `up` refuses while the other target is running, and `down` does not
# return until the hardware is back on the host.
#
# THE VM DISK IS AN OVERLAY on a vanilla base image, so `vm reset` just
# recreates the overlay. It used to be a full copy followed by a chmod and a
# chown, typed again before every wizard run. The previous overlay is kept
# as <disk>.prev, one generation, so a reset does not destroy the only copy of
# a state someone was still looking at.
#
# THE SCRIPT SHIPS ITSELF. Everything that runs on the host is in this file,
# sent with `ssh host bash -s -- --on-host ...`, so the host needs nothing
# installed and cannot run an older copy.
set -euo pipefail

# ============================================================================
# On the host. Runs as the SSH user, with passwordless sudo for virsh and ip.
# ============================================================================
if [ "${1:-}" = --on-host ]; then
	shift
	DOMAIN=$1 BASE=$2 COMPOSE_DIR=$3 CMD=$4
	shift 4

	# /usr/sbin is not on a non-login SSH shell's PATH, and iw lives there. A
	# bare `iw` then prints nothing, which passes for "no radios" (CLAUDE.md).
	IW=/usr/sbin/iw
	CONTAINER=boa
	# The container's end of the management veth (docker-attach.sh). It answers
	# whether or not the bridge has a lease.
	CONTAINER_MGMT=10.123.0.2
	# Vanilla OpenWrt puts its LAN on the first NIC, which is br-client, at
	# 192.168.1.1. It is still there after the wizard converts the box to a
	# bridge, as the rescue address. The host needs an address on that subnet
	# to reach it, which the libvirt hook adds from the settings read here.
	VM_LAN=192.168.1.1
	BOA_VM_HOST_ADDR=192.168.1.2/24
	[ -r /etc/default/boa-vm ] && . /etc/default/boa-vm
	HOST_ON_VM_LAN=$BOA_VM_HOST_ADDR

	say() { printf '    %s\n' "$*"; }
	fail() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

	container_state() {
		case "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" in
		true) echo running ;;
		false) echo stopped ;;
		*) echo absent ;;
		esac
	}
	vm_state() {
		sudo -n virsh domstate "$DOMAIN" 2>/dev/null | head -1 || echo absent
	}
	vm_disk() {
		sudo -n virsh domblklist "$DOMAIN" --details 2>/dev/null |
			awk '$1 == "file" && $2 == "disk" { print $4; exit }'
	}
	# The devices the VM takes, as its domain XML declares them: PCI addresses
	# (pci 0000:01:00.0) and USB vendor:product pairs (usb 0e8d:7961).
	vm_hostdevs() {
		sudo -n virsh dumpxml --inactive "$DOMAIN" | python3 -c '
import sys, xml.etree.ElementTree as ET
for h in ET.parse(sys.stdin).getroot().iter("hostdev"):
    s = h.find("source")
    if h.get("type") == "pci":
        a = s.find("address")
        print("pci %04x:%02x:%02x.%x" % tuple(int(a.get(k), 16) for k in ("domain", "bus", "slot", "function")))
    elif h.get("type") == "usb":
        print("usb %s:%s" % (s.find("vendor").get("id")[2:], s.find("product").get("id")[2:]))'
	}
	# A device's driver, or "none". While qemu holds a device it is bound to
	# vfio-pci (PCI) or usbfs (USB).
	driver_of() {
		if [ -e "$1/driver" ]; then basename "$(readlink -f "$1/driver")"; else echo none; fi
	}
	usb_devices() {
		for d in /sys/bus/usb/devices/*; do
			[ -f "$d/idVendor" ] || continue
			[ "$(cat "$d/idVendor"):$(cat "$d/idProduct")" = "$1" ] && echo "$d"
		done
		true
	}

	# WHAT THE HOST HOLDS. Both of these read the CALLER'S network namespace:
	# /sys/class/ieee80211 and the net/ directory under a USB device are
	# namespaced, so a radio or an adapter moved into the container vanishes from
	# the host's sysfs entirely. Measured 2026-09-28: the host listed nothing
	# while the container listed all four phys. Comparing the host's sysfs with
	# the host's `iw` can therefore never show anything missing, so what went
	# away is counted from the side that has it.
	host_phys() { "$IW" phy 2>/dev/null | awk '/^Wiphy/ { print $2 }' | sort; }
	# Ethernet on the USB bus, the rule docker-attach.sh and the libvirt hook use.
	host_usb_eth() {
		for n in /sys/bus/usb/devices/*/net/*; do
			[ -e "$n" ] || continue
			[ -e "$n/phy80211" ] && continue
			basename "$n"
		done | sort
	}
	# The same two counts from inside the container: "<radios> <adapters>".
	container_holds() {
		docker exec "$CONTAINER" sh -c '
			r=$(ls /sys/class/ieee80211 2>/dev/null | wc -l)
			e=0
			for n in /sys/bus/usb/devices/*/net/*; do
				[ -e "$n" ] && [ ! -e "$n/phy80211" ] && e=$((e + 1))
			done
			echo "$r $e"' 2>/dev/null || echo "0 0"
	}
	count() { grep -c . || true; }
	on_host() { echo "$(host_phys | tr '\n' ' ')$(host_usb_eth | tr '\n' ' ')"; }

	# What the VM still holds after it has stopped, one line each. Empty is home.
	vm_still_holds() {
		vm_hostdevs | while read -r kind id; do
			case $kind in
			pci)
				drv=$(driver_of "/sys/bus/pci/devices/$id")
				case $drv in vfio-pci | none) echo "pci $id (driver: $drv)" ;; esac
				;;
			usb)
				# The USB passthrough is optional in the domain XML, so an absent
				# device is not a fault here either. A present one needs a driver
				# of its own on every interface, not usbfs.
				for d in $(usb_devices "$id"); do
					for i in "$d"/*:*; do
						[ -d "$i" ] || continue
						drv=$(driver_of "$i")
						case $drv in usbfs | none) echo "usb $id $(basename "$i") (driver: $drv)" ;; esac
					done
				done
				;;
			esac
		done
		if ip link show br-client >/dev/null 2>&1; then echo "br-client still exists"; fi
	}

	# Runs a check that prints what is still missing, until it prints nothing
	# or 30s pass.
	wait_until_home() {
		local left
		for _ in $(seq 30); do
			left=$("$@")
			[ -z "$left" ] && break
			sleep 1
		done
		if [ -n "$left" ]; then
			printf '%s\n' "$left" | sed 's/^/    NOT BACK: /'
			fail "hardware did not come back to the host within 30s"
		fi
		say "back on the host: $(on_host)"
	}

	refuse_if_container() {
		[ "$(container_state)" != running ] ||
			fail "the container is running and holds the radios; run 'target.sh swap vm', or 'container down' first"
	}
	refuse_if_vm() {
		[ "$(vm_state)" != running ] ||
			fail "the VM is running and holds the radios; run 'target.sh swap container', or 'vm down' first"
	}

	case "$CMD" in
	status)
		say "container: $(container_state)"
		say "vm:        $(vm_state)"
		disk=$(vm_disk)
		if [ -n "$disk" ]; then
			backing=$(sudo -n qemu-img info -U --output=json "$disk" 2>/dev/null |
				python3 -c 'import json,sys; print(json.load(sys.stdin).get("backing-filename") or "none")' 2>/dev/null || echo unknown)
			say "vm disk:   $disk (backing: $backing)"
		fi
		say "on host:   $(on_host)"
		if [ "$(container_state)" = running ]; then
			say "container holds: $(container_holds | awk '{ print $1 " radios, " $2 " USB ethernet" }')"
		fi
		if [ "$(vm_state)" = running ]; then
			say "vm holds:  $(vm_hostdevs | tr '\n' ' ')"
		else
			left=$(vm_still_holds)
			[ -z "$left" ] || printf '%s\n' "$left" | sed 's/^/    NOT BACK:  /'
		fi
		;;

	container-up)
		refuse_if_vm
		# up -d, not --build: this starts what docker-deploy.sh last built. The
		# attach itself is boa-attach.service's job, on the container's start event.
		(cd "$COMPOSE_DIR" && docker compose up -d 2>&1 | tail -3)
		for _ in $(seq 60); do
			curl -sf -m 2 "http://$CONTAINER_MGMT/api/health" >/dev/null 2>&1 && break
			sleep 1
		done
		curl -sf -m 2 "http://$CONTAINER_MGMT/api/health" >/dev/null 2>&1 ||
			fail "the container did not answer on $CONTAINER_MGMT within 60s; see 'journalctl -u boa-attach' and 'docker logs boa'"
		say "container answering on $CONTAINER_MGMT"
		# The management veth comes from the same attach that moves the radios,
		# so the radios should already be in. Allow a moment before calling it.
		for _ in $(seq 15); do
			read -r radios adapters <<<"$(container_holds)"
			[ "$radios" -gt 0 ] && break
			sleep 1
		done
		say "container holds $radios radios and $adapters USB ethernet adapters"
		[ "$radios" -gt 0 ] || fail "the container is up with no radio; see 'journalctl -u boa-attach'"
		;;

	container-down)
		if [ "$(container_state)" = absent ]; then
			say "container: already absent"
			say "on host: $(on_host)"
		else
			# Counted before it goes. Afterwards there is nothing left to ask.
			read -r radios adapters <<<"$(container_holds)"
			want_r=$(($(host_phys | count) + radios))
			want_e=$(($(host_usb_eth | count) + adapters))
			(cd "$COMPOSE_DIR" && docker compose down 2>&1 | tail -3)
			short() {
				local r e
				r=$(host_phys | count)
				e=$(host_usb_eth | count)
				[ "$r" -ge "$want_r" ] || echo "radios: $r of $want_r on the host"
				[ "$e" -ge "$want_e" ] || echo "USB ethernet: $e of $want_e on the host"
			}
			wait_until_home short
		fi
		;;

	vm-up)
		refuse_if_container
		[ "$(vm_state)" = running ] || sudo -n virsh start "$DOMAIN" >/dev/null
		# br-client is the hook's (scripts/vm-host-net.sh), and so are the two
		# things checked here. Without them this script's own SSH hangs at the
		# next `vm down`, when a Mac has been using the host's IPv6 address on
		# the bridge that release deletes. Checked, not patched over: a fix
		# applied here would hide a host whose hook is missing or stale.
		[ "$(cat /proc/sys/net/ipv6/conf/br-client/disable_ipv6 2>/dev/null)" = 1 ] &&
			ip -4 -o addr show dev br-client 2>/dev/null | grep -q " $HOST_ON_VM_LAN " ||
			fail "br-client is missing its IPv4 address or still has IPv6, so the libvirt hook is absent or out of date; run 'scripts/target.sh vm setup'"
		for _ in $(seq 180); do
			curl -s -o /dev/null -m 2 "http://$VM_LAN/" 2>/dev/null && break
			sleep 1
		done
		curl -s -o /dev/null -m 2 "http://$VM_LAN/" 2>/dev/null ||
			fail "the VM did not answer on $VM_LAN within 180s; try 'sudo virsh console $DOMAIN'"
		say "VM answering on $VM_LAN"
		;;

	vm-down)
		if [ "$(vm_state)" = running ]; then
			sudo -n virsh shutdown "$DOMAIN" >/dev/null
			for _ in $(seq 60); do
				[ "$(vm_state)" = "shut off" ] && break
				sleep 1
			done
			if [ "$(vm_state)" != "shut off" ]; then
				say "no clean shutdown within 60s; forcing it off"
				sudo -n virsh destroy "$DOMAIN" >/dev/null
			fi
		fi
		say "vm: $(vm_state)"
		wait_until_home vm_still_holds
		;;

	vm-disk-reset)
		[ "$(vm_state)" != running ] || fail "the VM is running; take it down first"
		disk=$(vm_disk)
		[ -n "$disk" ] || fail "cannot find $DOMAIN's disk"
		sudo -n test -f "$BASE" || fail "no base image at $BASE"
		[ "$disk" != "$BASE" ] || fail "$DOMAIN boots from the base image itself; point it at an overlay"
		if sudo -n test -f "$disk"; then
			sudo -n mv -f "$disk" "$disk.prev"
			say "kept the previous disk as $disk.prev"
		fi
		# -F names the backing format. Without it qemu probes, and libvirt
		# refuses to start a domain whose backing chain was probed.
		sudo -n qemu-img create -q -f qcow2 -b "$BASE" -F qcow2 "$disk"
		# The owner libvirt runs qemu as. A disk it cannot open fails the start
		# with a permission error, which is what the old full-copy step hit.
		sudo -n chown libvirt-qemu:kvm "$disk"
		sudo -n chmod 0644 "$disk"
		# The base is shared by every reset. Nothing may write to it.
		sudo -n chmod 0444 "$BASE"
		say "fresh overlay $disk on $(basename "$BASE")"
		;;

	*) fail "unknown host command: $CMD" ;;
	esac
	exit 0
fi

# ============================================================================
# On the workstation.
# ============================================================================
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
usage() { sed -n '/^#   scripts/p' "$0" | sed 's/^# *//' >&2; exit 2; }

# The same .env lookup as docker-deploy.sh: a worktree has none, and falls back
# to the main checkout's.
ENV_FILE="${ENV_FILE:-$REPO/.env}"
if [ ! -f "$ENV_FILE" ]; then
	common=$(git -C "$REPO" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
	[ -n "$common" ] && [ -f "$(dirname "$common")/.env" ] && ENV_FILE="$(dirname "$common")/.env"
fi
[ -f "$ENV_FILE" ] && { set -a; . "$ENV_FILE"; set +a; }

HOST=${BOA_TARGET_HOST:-}
need_host() {
	[ -n "$HOST" ] || die "set BOA_TARGET_HOST in $ENV_FILE to the host that runs the container and the VM"
}
DOMAIN=${BOA_VM_DOMAIN:-openwrt-x86}
BASE=${BOA_VM_BASE_IMAGE:-/var/lib/libvirt/images/openwrt-25.12.5-x86-64-VANILLA.qcow2}
SDK=${BOA_VM_SDK_IMAGE:-openwrt/sdk:x86-64-25.12.5}
COMPOSE_DIR=/opt/infinite-streaming-boa/docker

# Keepalives, because this script restarts the network it runs over. Without
# them, a connection whose path disappears waits forever instead of failing.
on_host() {
	need_host
	ssh -o BatchMode=yes -o ConnectTimeout=10 \
		-o ServerAliveInterval=5 -o ServerAliveCountMax=6 "$HOST" \
		bash -s -- --on-host "$DOMAIN" "$BASE" "$COMPOSE_DIR" "$@" <"$0"
}

# The VM's LAN is only reachable through the host, so the install jumps.
vm_install() {
	log "installing boa on the VM (through $HOST)"
	SDK_IMAGE=$SDK \
		SSH_OPTS="-o ProxyJump=$HOST -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR" \
		"$REPO/scripts/openwrt-package.sh" root@192.168.1.1
}

# --- the Cudy TR3000 (target 3 in openwrt/CUDY-TR3000.md) -------------------
#
# A physical box that owns its own radios, so it has no up, down or handover.
# What it needs is the same out-of-box reset as the VM: `firstboot` wipes the
# overlay, which takes boa's packages with it because apk installs there, and
# leaves the OpenWrt image that was flashed. It then boots as a ROUTER at
# 192.168.1.1 on its LAN port, so it is reachable only from whatever is cabled
# to that port. That is BOA_CUDY_IF, the workstation's interface on it.
#
# EVERYTHING AFTER THE RESET IS PINNED TO BOA_CUDY_IF. 192.168.1.1 is also the
# VM's address while it is out-of-box, and the workstation may be cabled to both,
# so an unpinned connection may go to the wrong box.
CUDY=${BOA_CUDY_HOST:-192.168.0.23}
CUDY_IF=${BOA_CUDY_IF:-}
CUDY_BOARD=${BOA_CUDY_BOARD:-cudy,tr3000-v1}
CUDY_SDK=${BOA_CUDY_SDK_IMAGE:-openwrt/sdk:mediatek-filogic-25.12.5}
# firstboot regenerates the host key, so no known_hosts entry can be right.
CUDY_SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=8 -o ServerAliveInterval=5 -o ServerAliveCountMax=6"

cudy_ssh() {
	# shellcheck disable=SC2086
	ssh -o BatchMode=yes $CUDY_SSH_OPTS "root@$CUDY" "$@"
}
cudy_rescue_ssh() {
	# shellcheck disable=SC2086
	ssh -o BatchMode=yes $CUDY_SSH_OPTS -o BindInterface="$CUDY_IF" root@192.168.1.1 "$@"
}
need_cudy_if() {
	[ -n "$CUDY_IF" ] ||
		die "set BOA_CUDY_IF in $ENV_FILE to this machine's interface on the Cudy's LAN port (e.g. en12)"
	ifconfig "$CUDY_IF" >/dev/null 2>&1 || die "no interface $CUDY_IF on this machine"
}
cudy_describe() {
	"$@" 'printf "board:   %s\n" "$(cat /tmp/sysinfo/board_name)"
		printf "openwrt: %s\n" "$(cat /etc/openwrt_version)"
		printf "lan:     %s (%s)\n" "$(uci -q get network.lan.proto)" "$(ip -4 -o addr show br-lan | awk "{print \$4}" | tr "\n" " ")"
		printf "boa:     %s\n" "$(apk list -I 2>/dev/null | sed -n "s/^boa-\([0-9][^ ]*\).*/\1/p")"
		printf "stage:   %s\n" "$(cat /etc/infinite-streaming-boa/setup-stage 2>/dev/null || echo none)"' |
		sed 's/^/    /'
}

cudy_reset() {
	local install=$1 board backup stamp
	need_cudy_if
	# NEVER FIRSTBOOT THE WRONG BOX. The board name is read from the device, not
	# assumed from an address that DHCP may since have handed to something else.
	board=$(cudy_ssh 'cat /tmp/sysinfo/board_name' 2>/dev/null) ||
		die "cannot reach root@$CUDY. Set BOA_CUDY_HOST to its current address; it needs this machine's key in /etc/dropbear/authorized_keys"
	[ "$board" = "$CUDY_BOARD" ] || die "root@$CUDY is '$board', not $CUDY_BOARD; refusing to reset it"

	# The reset wipes the bridge conversion, the SSIDs and the channel plan.
	# Keep them, so `sysupgrade -r` can put the box back as it was.
	stamp=$(date +%Y%m%d-%H%M%S)
	backup="$REPO/cache/cudy-backups/cudy-$stamp.tar.gz"
	mkdir -p "$(dirname "$backup")"
	log "saving the Cudy's configuration to ${backup#"$REPO"/}"
	cudy_ssh 'sysupgrade -b /tmp/boa-reset-backup.tar.gz >/dev/null && cat /tmp/boa-reset-backup.tar.gz' >"$backup"
	gzip -t "$backup" 2>/dev/null && [ -s "$backup" ] || die "the backup is empty or corrupt; not resetting"
	log "  $(tar -tzf "$backup" | wc -l | tr -d ' ') files. Restore: copy it to the box and run 'sysupgrade -r <file>'"

	log "factory reset: firstboot, then reboot"
	cudy_ssh 'firstboot -y >/dev/null 2>&1 && (sleep 1; reboot) >/dev/null 2>&1 &' || true

	# The box drops the link as it reboots, and this machine takes a lease from
	# its DHCP server when the link returns, which puts it on 192.168.1.0/24.
	log "waiting for the Cudy at 192.168.1.1 on $CUDY_IF (up to 5 minutes)"
	sleep 20
	for _ in $(seq 140); do
		curl -s -o /dev/null -m 2 --interface "$CUDY_IF" http://192.168.1.1/ && break
		sleep 2
	done
	curl -s -o /dev/null -m 2 --interface "$CUDY_IF" http://192.168.1.1/ ||
		die "no answer from 192.168.1.1 on $CUDY_IF. $CUDY_IF has: $(ipconfig getifaddr "$CUDY_IF" || echo 'no IPv4 address'); if it kept an old lease, 'sudo ipconfig set $CUDY_IF DHCP'"
	log "the Cudy is back, out-of-box"
	cudy_describe cudy_rescue_ssh

	[ "$install" = 1 ] || return 0
	log "installing boa on the Cudy"
	SDK_IMAGE=$CUDY_SDK SSH_OPTS="$CUDY_SSH_OPTS -o BindInterface=$CUDY_IF" \
		"$REPO/scripts/openwrt-package.sh" root@192.168.1.1
	log "done. The wizard is at http://192.168.1.1/ on $CUDY_IF"
}

target=${1:-}
action=${2:-}
case "$target $action" in
"status "*)
	log "$HOST"
	on_host status
	;;
"container up")
	log "container up on $HOST"
	on_host container-up
	;;
"container down")
	log "container down on $HOST"
	on_host container-down
	;;
"vm setup")
	# Once per host, and again whenever the hook changes. It restarts libvirtd,
	# which leaves running domains alone.
	need_host
	log "installing the VM's libvirt hook and NetworkManager config on $HOST"
	ssh -o BatchMode=yes "$HOST" \
		"sudo -n env BOA_VM_DOMAIN='$DOMAIN' BOA_VM_HOST_ADDR='${BOA_VM_HOST_ADDR:-192.168.1.2/24}' bash -s -- install" \
		<"$REPO/scripts/vm-host-net.sh"
	;;
"vm up")
	log "VM $DOMAIN up on $HOST"
	on_host vm-up
	;;
"vm down")
	log "VM $DOMAIN down on $HOST"
	on_host vm-down
	;;
"vm reset")
	install=1
	[ "${3:-}" = --no-install ] && install=0
	log "VM $DOMAIN back to $(basename "$BASE") on $HOST"
	on_host vm-down
	on_host vm-disk-reset
	on_host vm-up
	[ "$install" = 0 ] || vm_install
	;;
"swap vm")
	log "handing the hardware from the container to the VM"
	on_host container-down
	on_host vm-up
	;;
"swap container")
	log "handing the hardware from the VM to the container"
	on_host vm-down
	on_host container-up
	;;
"cudy status")
	log "Cudy at $CUDY"
	if ! cudy_describe cudy_ssh; then
		log "no answer at $CUDY; trying 192.168.1.1 on ${CUDY_IF:-<BOA_CUDY_IF unset>}"
		need_cudy_if
		cudy_describe cudy_rescue_ssh || die "the Cudy answers at neither address"
	fi
	;;
"cudy reset")
	install=1
	[ "${3:-}" = --no-install ] && install=0
	cudy_reset "$install"
	;;
*) usage ;;
esac
