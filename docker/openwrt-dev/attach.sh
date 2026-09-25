#!/usr/bin/env bash
# Hands the host's radios, a wired client port and a real uplink to the
# openwrt-dev container. Run it AFTER `docker start`, and again after every
# restart: a network namespace dies with its container.
#
# This is the container sibling of scripts/docker-attach.sh, and it works the
# same way for the same reason -- only the host can move a netdev or an 802.11
# phy between namespaces. A radio moves as a PHY (`iw phy ... set netns`), not
# as a netdev: moving the netdev alone leaves the wiphy behind and nl80211 then
# refuses everything hostapd needs, with an error that reads like a driver
# fault rather than a namespace one.
#
# WHAT THIS CAN DO THAT THE VM CANNOT: take the host's onboard AX200. A
# container shares the kernel, so this is a namespace move; a VM needs VFIO,
# and that card shares an IOMMU group with the host's NVMe, SATA and USB
# controllers. See docs/BACKLOG.md.
set -euo pipefail

CONTAINER=${CONTAINER:-openwrt-dev}
BR=${BR:-br-wan}          # the host bridge holding the uplink NIC
SCAN_PHY=${SCAN_PHY:-}    # optional: the AX200's phy, handed over to listen only

log() { echo "owrt-dev-attach: $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

[ "$(id -u)" = 0 ] || die "must run as root"

PID=$(docker inspect -f '{{.State.Pid}}' "$CONTAINER" 2>/dev/null || true)
[ -n "$PID" ] && [ "$PID" != 0 ] || die "container '$CONTAINER' is not running"
in_ns() { nsenter -t "$PID" -n "$@"; }
ns_has() { in_ns ip link show "$1" >/dev/null 2>&1; }

ip link show "$BR" >/dev/null 2>&1 || die "$BR does not exist; see scripts/docker-host-net.sh"

# --- uplink: a veth with a STABLE MAC ------------------------------------
# Derived from the host bridge rather than random, so the DHCP lease survives a
# recreate. A veth gets a random address at creation, and without this the box
# takes a different address every time -- the same trap docker/compose.yml
# documents for the boa container.
if ns_has wan0; then
  log "wan0 already present"
else
  mac=$(python3 - "$BR" <<'PY'
import sys
mac = open("/sys/class/net/%s/address" % sys.argv[1]).read().strip()
o = [int(x, 16) for x in mac.split(":")]
o[0] = (o[0] | 0x02) & 0xFE          # locally administered, not multicast
o[5] = (o[5] + 1) & 0xFF             # and not the bridge's own address
print(":".join("%02x" % x for x in o))
PY
)
  ip link del owrt-wan-h 2>/dev/null || true
  ip link add owrt-wan-h type veth peer name owrt-wan-c address "$mac"
  ip link set owrt-wan-h master "$BR"
  ip link set owrt-wan-h up
  # No address on the host end, ever: it must forward frames for the clients'
  # own MACs without the host's stack answering for them.
  ip link set owrt-wan-c down
  ip link set owrt-wan-c netns "$PID" name wan0
  in_ns ip link set wan0 up
  in_ns ip link set wan0 master br-lan
  log "uplink: owrt-wan-h on $BR <-> wan0, MAC $mac"
fi

# --- every USB network device: wired ports and radios ---------------------
usb_net_devices() {
  local path iface
  for path in /sys/class/net/*; do
    iface=$(basename "$path")
    [ -e "$path/device" ] || continue
    case "$(readlink -f "$path/device")" in *usb*) printf '%s\n' "$iface" ;; esac
  done
}

rfkill unblock all 2>/dev/null || true

for host_if in $(usb_net_devices); do
  mac=$(cat "/sys/class/net/$host_if/address" 2>/dev/null) || continue
  short=$(printf '%s' "$mac" | tr -d ':' | tail -c 4)
  nmcli device set "$host_if" managed no >/dev/null 2>&1 || true
  if [ -e "/sys/class/net/$host_if/phy80211" ]; then
    phy=$(basename "$(readlink -f "/sys/class/net/$host_if/phy80211")")
    ns_has "wlan-$short" && { log "wlan-$short already in the container"; continue; }
    ip link set "$host_if" down
    iw phy "$phy" set netns "$PID"
    in_ns ip link set "$host_if" name "wlan-$short" 2>/dev/null || true
    log "radio: $host_if ($phy) -> the container"
  else
    ns_has "lan-$short" && { log "lan-$short already in the container"; continue; }
    ip link set "$host_if" down
    ip addr flush dev "$host_if" 2>/dev/null || true
    ip link set "$host_if" netns "$PID" name "lan-$short"
    in_ns ip link set "lan-$short" up
    in_ns ip link set "lan-$short" master br-lan
    log "wired client port: $host_if -> lan-$short"
  fi
done

# --- the listen-only radio, named rather than discovered -------------------
# Opting in is the only safe default: this is the host's own card and taking it
# on the strength of "it is a radio and it is here" could disconnect the
# machine the container runs on. Same reasoning as scripts/docker-attach.sh.
#
# RUN THIS AFTER `wifi up`, NOT BEFORE. wifi-scripts tears down and recreates
# the interfaces on every phy netifd manages -- including a phy whose radio is
# `disabled`, which is how the AX200 is configured here. So a scan0 created
# before `wifi up` is silently destroyed by it, and boa then reports its
# scanner port as absent. Re-run this script, or just this block, afterwards.
if [ -n "$SCAN_PHY" ]; then
  if [ -d "/sys/class/ieee80211/$SCAN_PHY" ]; then
    dev=$(ls "/sys/class/ieee80211/$SCAN_PHY/device/net" 2>/dev/null | head -1 || true)
    [ -n "$dev" ] && { nmcli device set "$dev" managed no >/dev/null 2>&1 || true; ip link set "$dev" down 2>/dev/null || true; }
    iw phy "$SCAN_PHY" set netns "$PID"
    in_ns iw phy "$SCAN_PHY" interface add scan0 type managed 2>/dev/null || true
    in_ns ip link set scan0 up 2>/dev/null || true
    log "scanner: $SCAN_PHY -> scan0 (set boa.main.scan=scan0)"
  else
    log "WARNING: SCAN_PHY=$SCAN_PHY is not a phy on this host; no listen-only radio"
  fi
fi

log "attached. Interfaces in the container:"
in_ns ip -o link show | awk -F': ' '{print "  " $2}' >&2
