#!/usr/bin/env bash
# Prepares a Linux host to run the boa container: puts the host's ethernet into
# a bridge, and takes NetworkManager's hands off the USB adapters.
#
# WHY THE HOST NIC HAS TO BE BRIDGED. boa is a transparent bridge, and that is
# not a detail of its implementation -- it is the reason the box is useful. A
# client on the access point keeps its own MAC and its own DHCP lease from the
# real router, and boa never appears as a hop. For that to hold, the container's
# uplink must be a layer-2 port on the same segment as the router, carrying
# arbitrary source MACs. A macvlan cannot: in bridge mode it filters ingress by
# destination MAC, and in passthru mode it consumes the lower device's frames
# entirely, which would take the host off the network. A bridge plus a veth is
# the only shape that gives the container real layer 2 AND leaves the host on it.
#
# THIS SCRIPT WILL BRIEFLY DISCONNECT THE HOST. The address is preserved by
# pinning the bridge's MAC to the NIC's, so the router hands back the same lease
# -- but there are a few seconds in the middle with no route. Run it detached:
#
#   sudo systemd-run --unit=boa-netswitch --collect \
#        /path/to/docker-host-net.sh apply
#
# so an SSH session dying takes the script with it. If the gateway is still
# unreachable after the settle window, the script puts everything back by itself
# rather than leaving a host that needs a keyboard.
set -euo pipefail

WAN_IF=${WAN_IF:-enp4s0}
BR=${BR:-br-wan}
BR_CON=boa-br-wan
PORT_CON=boa-wan-port
NM_CONF=/etc/NetworkManager/conf.d/99-boa-unmanaged.conf
STATE=/var/lib/boa-host-net
SETTLE=${SETTLE:-45}

# The adapters the container takes outright, by MAC. By MAC and not by name
# because the attach step renames them, and a name-based rule would stop
# matching the moment it worked.
# Kept only for a device the enx*/wlx* globs below cannot match. Empty is the
# normal case: the container takes what is on the USB bus, and those two names
# cover it without naming a single device.
BOA_MACS=${BOA_MACS:-}

log() { echo "boa-host-net: $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

[ "$(id -u)" = 0 ] || die "must run as root"

gateway_reachable() {
  local gw
  gw=$(ip route show default 2>/dev/null | awk '/^default/ {print $3; exit}')
  [ -n "$gw" ] || return 1
  ping -c1 -W2 "$gw" >/dev/null 2>&1
}

# --- what NetworkManager must not touch --------------------------------------
#
# Written before anything moves. NM claiming a USB adapter a moment after the
# container is handed it produces the worst version of this failure: the netdev
# is in the container's namespace, NM's record of it is not, and the adapter
# oscillates between the two.
write_unmanaged() {
  # enx*/wlx* are the names NetworkManager gives USB network devices, and the
  # host's own NICs are enp*/wlp*, so these two globs mean "every USB adapter"
  # without naming one. The MAC list below is now only for the non-USB radios
  # the container is given explicitly.
  local spec="interface-name:boa-wan-c;interface-name:boa-mgmt-h"
  spec="$spec;interface-name:enx*;interface-name:wlx*"
  spec="$spec;interface-name:lan-usb-*;interface-name:wlan-usb-*"
  local mac
  for mac in $BOA_MACS; do spec="$spec;mac:$mac"; done
  install -D -m 0644 /dev/stdin "$NM_CONF" <<EOF
# Written by scripts/docker-host-net.sh. The boa container owns these devices
# outright; NetworkManager must not configure, rename or carry them.
[keyfile]
unmanaged-devices=$spec
EOF
  log "NetworkManager told to leave the boa adapters alone"
  nmcli general reload conf 2>/dev/null || systemctl reload NetworkManager
}

# --- hotplug, the host half --------------------------------------------------
#
# udev runs on the HOST and knows nothing about container namespaces, so an
# adapter that is unplugged and replugged comes back HERE, not inside the
# container -- named enx<mac> again, with the container still holding a bridge
# that no longer has it. Nothing in the container can see that happen.
#
# This is the container's version of the Pi's 77-infinite-streaming-boa-radio
# rules, and it does the same thing for the same reason: re-run the selection
# step when a network device arrives or leaves. Here that step is the attach.
#
# --no-block matters for the same reason it does on the Pi: udev waits for RUN
# programs, and this one starts a unit that talks to the docker daemon, which
# would deadlock against udev settling.
write_udev() {
  install -D -m 0644 /dev/stdin /etc/udev/rules.d/77-boa-container.rules <<'UDEV'
# Written by scripts/docker-host-net.sh. Re-attaches USB network adapters to the
# boa container when they are plugged in, because a container gets no udev
# events of its own and an unplugged adapter re-enumerates in the host namespace.
SUBSYSTEM=="net", ENV{ID_BUS}=="usb", ACTION=="add", \
  RUN+="/usr/bin/systemctl restart --no-block boa-attach.service"
UDEV
  udevadm control --reload-rules 2>/dev/null || true
  log "udev will re-attach a replugged adapter to the container"
}

case "${1:-}" in

apply)
  ip link show "$WAN_IF" >/dev/null 2>&1 || die "$WAN_IF does not exist"

  if ip link show "$BR" >/dev/null 2>&1 \
     && [ "$(cat "/sys/class/net/$WAN_IF/master/uevent" 2>/dev/null | sed -n 's/^INTERFACE=//p')" = "$BR" ]; then
    log "$WAN_IF is already a port of $BR; nothing to do"
    write_unmanaged
    write_udev
    exit 0
  fi

  MAC=$(cat "/sys/class/net/$WAN_IF/address")
  OLD_CON=$(nmcli -t -g GENERAL.CONNECTION device show "$WAN_IF" 2>/dev/null || true)
  [ -n "$OLD_CON" ] || die "no NetworkManager connection is active on $WAN_IF; refusing to guess how to put it back"

  mkdir -p "$STATE"
  printf '%s\n' "$OLD_CON" >"$STATE/old-connection"
  log "current connection on $WAN_IF is '$OLD_CON'; recorded for rollback"

  write_unmanaged
  write_udev

  # The bridge carries the NIC's own MAC, so the router returns the same lease
  # and the host keeps
  # the address SSH is already using. STP off and forward-delay 0 for the reason
  # the container's entrypoint gives: there is one path out and no loop to find,
  # and a forwarding delay is dead time a DHCP transaction can land in.
  nmcli con delete "$BR_CON" >/dev/null 2>&1 || true
  nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
  nmcli con add type bridge con-name "$BR_CON" ifname "$BR" \
    bridge.stp no bridge.forward-delay 0 bridge.mac-address "$MAC" \
    ipv4.method auto ipv6.method auto connection.autoconnect yes >/dev/null
  nmcli con add type ethernet con-name "$PORT_CON" ifname "$WAN_IF" \
    master "$BR" slave-type bridge connection.autoconnect yes >/dev/null

  # Autoconnect off on the old profile, or NM races the new one back down on the
  # next carrier event.
  nmcli con mod "$OLD_CON" connection.autoconnect no >/dev/null 2>&1 || true

  log "switching $WAN_IF into $BR (MAC $MAC) -- connectivity drops here"
  nmcli con down "$OLD_CON" >/dev/null 2>&1 || true
  nmcli con up "$BR_CON" >/dev/null
  nmcli con up "$PORT_CON" >/dev/null

  # --- did it work? --------------------------------------------------------
  #
  # A bridge that is up with no lease looks healthy in every way except the one
  # that matters, so the test is a packet to the gateway and nothing weaker.
  ok=0
  for _ in $(seq "$SETTLE"); do
    if gateway_reachable; then ok=1; break; fi
    sleep 1
  done

  if [ "$ok" != 1 ]; then
    log "no gateway after ${SETTLE}s -- rolling back so the host stays reachable"
    nmcli con down "$PORT_CON" >/dev/null 2>&1 || true
    nmcli con down "$BR_CON" >/dev/null 2>&1 || true
    nmcli con mod "$OLD_CON" connection.autoconnect yes >/dev/null 2>&1 || true
    nmcli con up "$OLD_CON" >/dev/null 2>&1 || true
    die "bridging $WAN_IF did not produce a working uplink; original connection restored"
  fi

  log "gateway reachable through $BR: $(ip -4 -br addr show "$BR" | tr -s ' ')"

  # --- let bridged client traffic through ----------------------------------
  #
  # br_netfilter is loaded on this host and bridge-nf-call-iptables is 1, so
  # frames FORWARDED across br-wan are seen by iptables -- where Docker's own
  # policy is DROP. Without this, every conditioned client would be bridged
  # perfectly and then dropped by a firewall that thinks it is looking at
  # routed traffic.
  #
  # DOCKER-USER, because Docker jumps to it first from FORWARD and does not
  # rewrite it. Scoped to this bridge rather than flipping bridge-nf globally,
  # which would change how every other container on this host is filtered.
  for chain_rule in "-i $BR -j ACCEPT" "-o $BR -j ACCEPT"; do
    # shellcheck disable=SC2086
    iptables -C DOCKER-USER $chain_rule 2>/dev/null \
      || iptables -I DOCKER-USER $chain_rule
    # shellcheck disable=SC2086
    ip6tables -C DOCKER-USER $chain_rule 2>/dev/null \
      || ip6tables -I DOCKER-USER $chain_rule
  done
  log "DOCKER-USER now accepts traffic across $BR"
  log "done. Start the container, then run scripts/docker-attach.sh."
  ;;

revert)
  OLD_CON=$(cat "$STATE/old-connection" 2>/dev/null || true)
  nmcli con down "$PORT_CON" >/dev/null 2>&1 || true
  nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
  nmcli con down "$BR_CON" >/dev/null 2>&1 || true
  nmcli con delete "$BR_CON" >/dev/null 2>&1 || true
  rm -f "$NM_CONF" /etc/udev/rules.d/77-boa-container.rules
  udevadm control --reload-rules 2>/dev/null || true
  nmcli general reload conf 2>/dev/null || systemctl reload NetworkManager
  if [ -n "$OLD_CON" ]; then
    nmcli con mod "$OLD_CON" connection.autoconnect yes >/dev/null 2>&1 || true
    nmcli con up "$OLD_CON" >/dev/null 2>&1 || true
    log "restored '$OLD_CON' on $WAN_IF"
  else
    log "WARNING: no recorded connection to restore; NetworkManager will pick one"
  fi
  for chain_rule in "-i $BR -j ACCEPT" "-o $BR -j ACCEPT"; do
    # shellcheck disable=SC2086
    iptables -D DOCKER-USER $chain_rule 2>/dev/null || true
    # shellcheck disable=SC2086
    ip6tables -D DOCKER-USER $chain_rule 2>/dev/null || true
  done
  ;;

status)
  echo "== $BR =="
  ip -br addr show "$BR" 2>/dev/null || echo "  (absent)"
  echo "== ports =="
  ls /sys/class/net/"$BR"/brif 2>/dev/null || echo "  (none)"
  echo "== default route =="
  ip route show default
  echo "== NetworkManager unmanaged =="
  cat "$NM_CONF" 2>/dev/null || echo "  (no rule written)"
  ;;

*)
  echo "usage: $0 {apply|revert|status}" >&2
  exit 2
  ;;
esac
