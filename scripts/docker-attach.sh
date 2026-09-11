#!/usr/bin/env bash
# Hands the physical adapters to a running boa container, and gives it an uplink.
#
# Run it AFTER `docker start`, and again after every restart: a network
# namespace dies with the container, and the interfaces move back to the host
# when it does. This is the one part of the arrangement that cannot live in the
# image, because only the host can move a netdev or an 802.11 phy between
# namespaces.
#
# WHAT GOES IN, AND HOW:
#
#   uplink   a veth pair. The host end joins br-wan, so the container's end is a
#            real layer-2 port on the operator's own LAN and can carry a client's
#            own MAC out to the router. This is what keeps the bridge transparent.
#
#   wired    the USB ethernet netdevs, moved wholesale. `ip link set ... netns`
#            takes the device away from the host entirely, which is the point:
#            shared ownership of a bridge port is not a thing.
#
#   radio    `iw phy ... set netns`, NOT `ip link set`. Moving the netdev alone
#            leaves the wiphy in the host's namespace, and nl80211 then refuses
#            every operation hostapd needs -- an error that reads like a driver
#            fault rather than a namespace one.
#
#   mgmt     a second veth pair on a private /30, so the web interface has an
#            address that does not depend on the bridge having taken a lease.
#            When DHCP has not landed or the uplink is unplugged, this is still
#            the way in.
set -euo pipefail

CONTAINER=${CONTAINER:-boa}
BR=${BR:-br-wan}
NETNS=boa

# A radio to hand in as an INSTRUMENT rather than an access point, named by the
# host's interface name -- "wlp5s0" for the motherboard's AX200.
#
# NAMED, not discovered, and it is the one adapter here that is. Everything else
# in this script is found on the USB bus precisely so that plugging a dongle in
# is the whole of adding a radio. This one is not on that bus: it is the host's
# own onboard card, which the host may well be using, and taking it away on the
# strength of "it is a radio and it is here" would disconnect the machine the
# container runs on. Opting in is the only safe default.
#
# Why this radio is worth the trouble: #279 measured the AX200 to be a poor
# access point here, because it is a self-managed regulatory device that loses
# its country through the namespace handover. All of that is about
# TRANSMITTING. A radio may receive in the world domain -- every 5GHz channel
# it cannot beacon on is still marked PASSIVE-SCAN, which is exactly the
# permission to hear -- so the card's one real limitation does not apply to
# listening, and one scan of it heard 15 access points across both bands with
# BSS Load from 12 of them. See #288.
SCAN_IF=${SCAN_IF:-}

# EVERY USB NETWORK DEVICE, discovered, not a list.
#
# This began as a hardcoded MAC allowlist and that was wrong in the way this
# repository has already learned once. #227 removed the last place that held a
# list of adapter names, precisely so a dongle could be plugged into any socket
# and be the same radio; net-name identifies the DEVICE, and radioplan writes a
# config for whatever it finds. An allowlist here put that list straight back,
# one layer down.
#
# Measured 2026-09-09: a second mt7921u was plugged into the hub, udev fired, the
# attach ran, and the adapter was silently skipped because its MAC was not in the
# list. It sat on the host, absent from the bridge and from the interface, with
# nothing saying why.
#
# So the rule is the Pi's udev rule: any network device on the USB bus. The host
# keeps its own PCIe NICs by construction, since they are not on it.
usb_net_devices() {
  local path iface
  for path in /sys/class/net/*; do
    iface=$(basename "$path")
    [ -e "$path/device" ] || continue
    case "$(readlink -f "$path/device")" in
      *//usb*|*/usb[0-9]*) ;;
      *) continue ;;
    esac
    printf '%s\n' "$iface"
  done
}

MGMT_HOST_IP=${MGMT_HOST_IP:-10.123.0.1}
MGMT_CONT_IP=${MGMT_CONT_IP:-10.123.0.2}
MGMT_PREFIX=30
UI_PORT=${UI_PORT:-8080}

log() { echo "boa-attach: $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

[ "$(id -u)" = 0 ] || die "must run as root"

PID=$(docker inspect -f '{{.State.Pid}}' "$CONTAINER" 2>/dev/null || true)
[ -n "$PID" ] && [ "$PID" != 0 ] || die "container '$CONTAINER' is not running"
log "container '$CONTAINER' is pid $PID"

# `ip netns exec` needs a name under /var/run/netns; a container's namespace has
# none, so one is bound here. Recreated every run because the pid changes with
# every container restart and a stale link points at nothing.
mkdir -p /var/run/netns
ln -sfn "/proc/$PID/ns/net" "/var/run/netns/$NETNS"

in_ns() { ip netns exec "$NETNS" "$@"; }
ns_has() { in_ns ip link show "$1" >/dev/null 2>&1; }

# iface_for_mac finds the host's current name for a device. Empty when the
# device is not in the host namespace -- which, on a re-run, usually means it is
# already inside the container and nothing needs doing.
iface_for_mac() {
  local mac=$1 path
  for path in /sys/class/net/*/address; do
    [ -r "$path" ] || continue
    if [ "$(cat "$path")" = "$mac" ]; then
      basename "$(dirname "$path")"
      return 0
    fi
  done
  return 1
}

# The Pi's naming rule, reproduced exactly: the last four hex digits of the MAC,
# so a name identifies the DEVICE rather than the socket it is in. See
# overlay/usr/local/sbin/infinite-streaming-boa-net-name for why that choice was
# made and what it costs.
boa_name() {   # $1=prefix  $2=mac
  printf '%s-%s\n' "$1" "$(printf '%s' "$2" | tr -d ':' | tr 'A-Z' 'a-z' | tail -c 4)"
}

# --- uplink ------------------------------------------------------------------
ip link show "$BR" >/dev/null 2>&1 \
  || die "$BR does not exist. Run scripts/docker-host-net.sh apply first."

if ns_has wan0; then
  log "wan0 already present in the container"
else
  # A STABLE MAC on the container end, derived from the host's own NIC.
  #
  # Everything downstream hangs off this address. The entrypoint pins br-lan to
  # the uplink port's MAC -- as the Pi does, so that a radio joining the bridge
  # cannot change it -- and the router's DHCP lease follows the bridge MAC. A
  # veth gets a RANDOM address at creation, so without this the appliance takes
  # a different IP on every `docker compose up`, which was measured here on
  # 2026-09-09: .204 became .17 across one recreate. On the Pi the uplink is a
  # physical NIC and the question never arises.
  #
  # Derived rather than hardcoded, so two hosts running this never collide: the
  # bridge's MAC with the locally-administered bit set and the multicast bit
  # cleared, which is what that bit range is reserved for.
  wan_mac=$(python3 - "$BR" <<'PY'
import sys
mac = open("/sys/class/net/%s/address" % sys.argv[1]).read().strip()
octets = [int(x, 16) for x in mac.split(":")]
octets[0] = (octets[0] | 0x02) & 0xFE
print(":".join("%02x" % o for o in octets))
PY
)
  ip link del boa-wan-h 2>/dev/null || true
  ip link add boa-wan-h type veth peer name boa-wan-c address "$wan_mac"
  ip link set boa-wan-h master "$BR"
  ip link set boa-wan-h up
  log "uplink MAC pinned to $wan_mac, so the DHCP lease survives a recreate"
  # The host end must forward frames for MACs that are not its own -- every
  # client behind the container is one. A veth does that already; what it must
  # NOT do is have the host's stack answer for them, which is why no address is
  # ever put on boa-wan-h.
  ip link set boa-wan-c down
  ip link set boa-wan-c netns "$PID" name wan0
  in_ns ip link set wan0 up
  log "uplink: boa-wan-h on $BR <-> wan0 in the container"
fi

# --- management path ---------------------------------------------------------
if ns_has mgmt0; then
  log "mgmt0 already present in the container"
else
  ip link del boa-mgmt-h 2>/dev/null || true
  ip link add boa-mgmt-h type veth peer name boa-mgmt-c
  ip addr add "$MGMT_HOST_IP/$MGMT_PREFIX" dev boa-mgmt-h
  ip link set boa-mgmt-h up
  ip link set boa-mgmt-c down
  ip link set boa-mgmt-c netns "$PID" name mgmt0
  in_ns ip addr add "$MGMT_CONT_IP/$MGMT_PREFIX" dev mgmt0
  in_ns ip link set mgmt0 up
  log "management: $MGMT_HOST_IP <-> $MGMT_CONT_IP"
fi

# --- everything on the USB bus, sorted into wired ports and radios ----------
#
# One pass, because the only thing that differs between a USB ethernet adapter
# and a USB radio here is the name prefix and whether the PHY has to move
# instead of the netdev.
radio_specs=""
# The named listen-only radio joins the same list, with its own prefix, exactly
# as the header below anticipates: "a radio on another bus can be added later by
# putting a differently-prefixed entry in this list and changing nothing else".
# The move, the rename and the rfkill clear do not care what bus it is on.
#
# Resolved to a MAC here rather than carried as a name, because the loop below
# looks devices up by MAC -- which is what makes a re-run idempotent after the
# interface has already been renamed inside the container.
if [ -n "$SCAN_IF" ]; then
  if scan_mac=$(cat "/sys/class/net/$SCAN_IF/address" 2>/dev/null); then
    if [ -e "/sys/class/net/$SCAN_IF/phy80211" ]; then
      radio_specs="$radio_specs wlan-scan/$scan_mac"
    else
      die "SCAN_IF=$SCAN_IF has no phy80211; it is not a wireless device"
    fi
  else
    # Loud, not skipped. A mistyped name here produces a container with no
    # instrument, and the symptom is contention figures that go on costing an
    # outage for a reason nothing states.
    die "SCAN_IF=$SCAN_IF is not an interface on this host"
  fi
fi
for host_if in $(usb_net_devices); do
  mac=$(cat "/sys/class/net/$host_if/address" 2>/dev/null) || continue
  if [ -e "/sys/class/net/$host_if/phy80211" ]; then
    radio_specs="$radio_specs wlan-usb/$mac"
    continue
  fi
  want=$(boa_name lan-usb "$mac")
  if ns_has "$want"; then
    log "$want already in the container"
    continue
  fi
  nmcli device set "$host_if" managed no >/dev/null 2>&1 || true
  # Down before the move: a running device cannot be renamed, and renaming in
  # the same command is what keeps the container from ever seeing the host's
  # enx<mac> name.
  ip link set "$host_if" down
  ip addr flush dev "$host_if" 2>/dev/null || true
  ip link set "$host_if" netns "$PID" name "$want"
  in_ns ip link set "$want" up
  log "wired: $host_if ($mac) -> $want"
done

# --- radios ------------------------------------------------------------------
#
# The prefix travels with the MAC as "<prefix>/<mac>", so a radio on another bus
# can be added later by putting a differently-prefixed entry in this list and
# changing nothing else: the move, the rename and the rfkill clear do not care
# what the wiphy is attached to.
for spec in $radio_specs; do
  prefix=${spec%%/*}
  mac=${spec#*/}
  want=$(boa_name "$prefix" "$mac")
  if ns_has "$want"; then
    log "$want already in the container"
    continue
  fi
  host_if=$(iface_for_mac "$mac" || true)
  if [ -z "$host_if" ]; then
    log "WARNING: no radio with MAC $mac on this host; $want will be missing"
    continue
  fi
  phy=$(basename "$(readlink -f "/sys/class/net/$host_if/phy80211")")
  [ -n "$phy" ] || die "$host_if has no phy80211; it is not a wireless device"
  nmcli device set "$host_if" managed no >/dev/null 2>&1 || true
  rfkill unblock all 2>/dev/null || true
  ip link set "$host_if" down
  # The PHY, not the netdev. See the header.
  /usr/sbin/iw phy "$phy" set netns "$PID"
  in_ns ip link set "$host_if" name "$want"
  log "radio: $host_if ($phy, $mac) -> $want"
done

# --- reach the interface -----------------------------------------------------
#
# The container has no published ports: it runs with its own namespace and
# nothing Docker manages, so `-p` has nothing to bind. A DNAT gives the web
# interface a door on the host's own address instead.
if ! iptables -t nat -C PREROUTING -p tcp --dport "$UI_PORT" -j DNAT \
        --to-destination "$MGMT_CONT_IP:80" 2>/dev/null; then
  iptables -t nat -A PREROUTING -p tcp --dport "$UI_PORT" -j DNAT \
    --to-destination "$MGMT_CONT_IP:80"
fi
# And from the host itself, which never passes through PREROUTING.
if ! iptables -t nat -C OUTPUT -p tcp -o lo --dport "$UI_PORT" -j DNAT \
        --to-destination "$MGMT_CONT_IP:80" 2>/dev/null; then
  iptables -t nat -A OUTPUT -p tcp -o lo --dport "$UI_PORT" -j DNAT \
    --to-destination "$MGMT_CONT_IP:80"
fi
if ! iptables -C DOCKER-USER -d "$MGMT_CONT_IP" -p tcp --dport 80 -j ACCEPT 2>/dev/null; then
  iptables -I DOCKER-USER -d "$MGMT_CONT_IP" -p tcp --dport 80 -j ACCEPT
fi

log "attached. Interfaces now in the container:"
in_ns ip -br link | sed 's/^/  /' >&2
log "web interface: http://$MGMT_CONT_IP/ or http://$(hostname -f 2>/dev/null || hostname):$UI_PORT/"
