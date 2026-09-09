#!/bin/bash
# Builds the bridge, brings the radios up, and hands off to boad.
#
# This replaces four things the Pi image gets from the distribution -- systemd
# ordering, NetworkManager's bridge, the select-radio unit and udev hotplug --
# with one script, because a container has none of them. It does NOT replace
# radioplan, which is copied in unchanged and still writes the hostapd configs.
#
# The interfaces are not created here. The host moves them into this network
# namespace after the container starts, which is the only way a physical netdev
# or an 802.11 phy can reach a container: see scripts/docker-attach.sh. So this
# script's first job is to WAIT for them, and to say so loudly when they never
# arrive -- an empty bridge that comes up clean and conditions nothing is the
# exact failure this repository has been bitten by before.
set -euo pipefail

log() { echo "boa-entrypoint: $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

BOA_ADDR=${BOA_ADDR:-:80}
BOA_BRIDGE=${BOA_BRIDGE:-br-lan}
BOA_WAN_PORT=${BOA_WAN_PORT:-wan0}
# How long to wait for the host's attach step. Generous, because a cold host
# boot can have the container up well before the USB adapters have enumerated.
BOA_ATTACH_TIMEOUT=${BOA_ATTACH_TIMEOUT:-120}
BOA_DHCP=${BOA_DHCP:-1}
BOA_EXTRA_ARGS=${BOA_EXTRA_ARGS:-}

AP_SSID=${AP_SSID:-infinite-streaming-boa}
AP_PASSWORD=${AP_PASSWORD:-change-me-please}
AP_COUNTRY=${AP_COUNTRY:-US}

# The daemon writes its time series here and expects the directory to exist;
# on the Pi systemd's RuntimeDirectory= makes it. Chart history is deliberately
# on tmpfs there and is deliberately on the container's writable layer here --
# in both cases it survives a daemon restart and does not survive a reboot.
mkdir -p /run/infinite-streaming-boa /run/boa/hostapd \
         /var/lib/infinite-streaming-boa /var/run/hostapd /etc/hostapd

# ---------------------------------------------------------------------------
# 1. Wait for the host to hand over the interfaces.
# ---------------------------------------------------------------------------

have_iface() { [ -e "/sys/class/net/$1" ]; }

wait_for_iface() {   # $1=name  $2=what it is, for the message
  local i=0
  while [ "$i" -lt "$BOA_ATTACH_TIMEOUT" ]; do
    have_iface "$1" && { log "$1 ($2) arrived after ${i}s"; return 0; }
    sleep 1
    i=$((i + 1))
  done
  return 1
}

wait_for_iface "$BOA_WAN_PORT" "uplink" \
  || die "$BOA_WAN_PORT never appeared after ${BOA_ATTACH_TIMEOUT}s. The host has not run scripts/docker-attach.sh against this container, so there is no path to the network and nothing to condition."

# The radios and the wired client ports are given a shorter grace period of
# their own AFTER the uplink lands, since the attach script moves them in the
# same pass. Their absence is a warning rather than fatal: a box with an uplink
# and no client ports is useless but diagnosable, and refusing to start would
# take the interface that says so away with it.
sleep 2

# ---------------------------------------------------------------------------
# 2. The bridge.
# ---------------------------------------------------------------------------
#
# STP OFF, and forward_delay 0. On the Pi, NetworkManager runs STP and
# customize.sh documents what that costs: adding a radio to the bridge triggers
# a topology recalculation, br-lan drops carrier for a moment, and a DHCP
# transaction caught in that window never completes. There is exactly one path
# out of this bridge -- the veth to the host -- so there is no loop for STP to
# find and nothing to trade for the outage.
#
# The MAC is pinned to the uplink port's, for the reason customize.sh gives: a
# bridge otherwise adopts the lowest MAC among its ports, so it CHANGES when a
# radio joins, and the DHCP lease changes with it.

if ! ip link show "$BOA_BRIDGE" >/dev/null 2>&1; then
  ip link add name "$BOA_BRIDGE" type bridge forward_delay 0 stp_state 0
fi
WAN_MAC=$(cat "/sys/class/net/$BOA_WAN_PORT/address")
ip link set dev "$BOA_BRIDGE" address "$WAN_MAC"
log "bridge $BOA_BRIDGE pinned to $BOA_WAN_PORT MAC $WAN_MAC, STP off"

ip link set dev "$BOA_WAN_PORT" master "$BOA_BRIDGE"
ip link set dev "$BOA_WAN_PORT" up
ip link set dev "$BOA_BRIDGE" up

# sync_lan_ports enslaves every wired client port that is not already a bridge
# port, and leaves the comma-separated list the daemon's -lan wants in SYNC_LAN.
#
# IDEMPOTENT AND RE-RUN, not done once at startup. The Pi gets this from a udev
# rule; a container gets no udev events at all, because udev runs on the host and
# an adapter that is unplugged and replugged re-enumerates THERE. The host's
# attach script moves it back in, and if that lands after this point in the
# script -- which it does on any replug -- the port sits in the namespace,
# outside the bridge, forwarding nothing.
#
# Measured on 2026-09-09: a hub moved to a different socket left all three
# adapters correctly named inside the container with br-lan holding only wan0.
# Nothing was conditioned and nothing said so.
#
# A glob that matches nothing must not abort the script, which is the `set -e` +
# failing-glob trap CLAUDE.md warns about, so each candidate is tested first.

# RESULTS COME BACK IN A GLOBAL, not on stdout, and that is not a style choice.
# `x=$(sync_radios)` runs the function in a SUBSHELL, so every variable it sets
# -- the list of radios already given a supervisor above all -- is discarded when
# it returns. Measured 2026-09-09: the guard against double-starting hostapd was
# written, was correct, and never once took effect, so a second hostapd started
# on a served interface and died with "nl80211: Could not configure driver mode".
# The substitution also blocks on any background job it starts; see sync_radios.
SYNC_LAN=""
SYNC_WLAN=""

sync_lan_ports() {
  local out="" path port
  for path in /sys/class/net/lan-usb-*; do
    [ -e "$path" ] || continue
    port=$(basename "$path")
    if [ ! -e "/sys/class/net/$BOA_BRIDGE/brif/$port" ]; then
      if ip link set dev "$port" master "$BOA_BRIDGE" 2>/dev/null; then
        log "bridged wired client port $port"
      else
        log "WARNING: could not add $port to $BOA_BRIDGE"
        continue
      fi
    fi
    ip link set dev "$port" up 2>/dev/null || true
    out="${out:+$out,}$port"
  done
  # NOT fatal, but never silent. The daemon needs a name for -lan, and the Pi's
  # defaults file uses this same placeholder for the same reason: an empty value
  # would let the next argument be swallowed as -lan's value.
  [ -n "$out" ] || out="lan-usb-none"
  SYNC_LAN="$out"
}

sync_lan_ports
LAN_PORTS="$SYNC_LAN"
[ "$LAN_PORTS" = "lan-usb-none" ] &&
  log "WARNING: no lan-usb-* port was handed over; wired clients cannot be conditioned"

# ---------------------------------------------------------------------------
# 3. An address on the bridge.
# ---------------------------------------------------------------------------
#
# From the real router, over the uplink, the same lease the Pi takes. It is what
# makes the box pingable, lets iperf3 answer, and populates the neighbour table
# the daemon reads to put an IP against a client's MAC.
#
# udhcpc rather than a static address: this bridge is on the operator's own LAN
# and inventing an address on it would collide sooner or later.
if [ "$BOA_DHCP" = 1 ]; then
  busybox udhcpc -i "$BOA_BRIDGE" -s /usr/share/udhcpc/default.script -b -t 5 -T 3 \
    >/dev/null 2>&1 \
    && log "udhcpc requesting a lease on $BOA_BRIDGE" \
    || log "WARNING: udhcpc could not start on $BOA_BRIDGE; the box will have no LAN address"
fi

# ---------------------------------------------------------------------------
# 4. The radios.
# ---------------------------------------------------------------------------
#
# rfkill first: a phy moved between namespaces keeps whatever soft block it had,
# and a soft-blocked radio fails in hostapd as a driver error that reads like
# missing firmware.
rfkill unblock all 2>/dev/null || log "rfkill unavailable (is /dev/rfkill mapped in?)"

install -m 0600 /dev/stdin /etc/infinite-streaming-boa/ap.env <<APENV
AP_SSID=$AP_SSID
AP_PASSPHRASE=$AP_PASSWORD
AP_COUNTRY=$AP_COUNTRY
APENV

# radio_ifaces lists the wireless netdevs present, so the expensive step below
# can be skipped when nothing has changed. radioplan spawns an `iw phy info` per
# radio and a python interpreter; running that every few seconds forever, to
# rediscover the same two radios, is exactly the kind of waste this box cannot
# afford while it is also the instrument.
radio_ifaces() {
  local out="" link
  for link in /sys/class/net/*/phy80211; do
    [ -e "$link" ] || continue
    out="$out $(basename "$(dirname "$link")")"
  done
  printf '%s' "${out# }"
}

# usable_channels is the signature the settle wait below compares. It is the set
# of channels every phy will actually let an AP BEACON on, which is a different
# and smaller set than the channels a phy lists: "no IR" means the radio may not
# transmit until it has heard someone else, and "disabled" needs no explanation.
# radioplan applies the same two exclusions, so this reads the same world its
# decision is made from.
usable_channels() {
  /usr/sbin/iw phy 2>/dev/null | awk '
    /^Wiphy/ { phy = $2 }
    /\* [0-9.]+ MHz \[[0-9]+\]/ && !/disabled/ && !/no IR/ {
      match($0, /\[[0-9]+\]/)
      printf "%s:%s ", phy, substr($0, RSTART+1, RLENGTH-2)
    }'
}

# WAIT FOR THE REGULATORY DOMAIN TO SETTLE BEFORE PLANNING.
#
# A self-managed phy -- iwlwifi is one, and the motherboard's AX200 is iwlwifi --
# carries its OWN regulatory domain rather than the global one, and it is not
# final the instant the phy lands in this namespace. Measured 2026-09-09: at the
# moment radioplan ran, the AX200 still advertised 5GHz as available, so the plan
# gave it channel 36; by the time hostapd started it had settled to country 00,
# where every 5GHz channel is "no IR", and hostapd refused with "Hardware does
# not support configured channel". The plan was correct against what it could see
# and wrong against what was true a second later.
#
# Re-running the planner afterwards produced the right answer immediately --
# 2.4GHz for the AX200, 5GHz for the mt7921u, which is the dual-band arrangement
# customize.sh describes. So this is purely a race, and waiting for the channel
# set to stop changing is the honest fix.
wait_radios_settle() {
  local prev="" now i=0
  [ -n "$(radio_ifaces)" ] || return 0
  while [ "$i" -lt 20 ]; do
    now=$(usable_channels)
    if [ -n "$now" ] && [ "$now" = "$prev" ]; then
      log "radio channel availability settled after ${i}s"
      return 0
    fi
    prev="$now"
    sleep 1
    i=$((i + 1))
  done
  log "WARNING: radio channel availability still changing after ${i}s; planning anyway"
}

# supervised asks whether a radio still has a live supervisor, using the pid the
# supervisor writes for ITSELF. Neither obvious alternative works.
#
# hostapd's own pid file is absent during its five-second backoff, so a radio
# mid-restart would look unserved and be handed a SECOND supervisor, with two
# hostapds then fighting over one interface.
#
# A shell variable listing what this process has started survives nothing that
# ends a supervisor from outside, so a radio stopped through the systemctl shim
# could never be started again. Measured 2026-09-09, stopping the onboard radio
# to re-regulate it: the AP went down and no path existed to bring it back.
supervised() {
  local f=/run/boa/hostapd/$1.sup p
  [ -r "$f" ] || return 1
  p=$(cat "$f" 2>/dev/null) || return 1
  [ -n "$p" ] && [ -d "/proc/$p" ]
}

LAST_RADIO_SET=""

sync_radios() {
  local now plan planned iface out=""
  now=$(radio_ifaces)
  if [ -z "$now" ]; then
    LAST_RADIO_SET=""
    SYNC_WLAN="wlan-none"
    return
  fi
  # radioplan prints its plan on stdout as PLANNED=<names> and everything else
  # on stderr, which is the split its own header insists on -- so capturing
  # stdout alone is safe and capturing both would put log text into the value.
  plan=$(infinite-streaming-boa-radioplan 2>/dev/null || true)
  planned=$(printf '%s\n' "$plan" | sed -n 's/^PLANNED=//p' | tr -s ' ')
  if [ -z "$planned" ]; then
    LAST_RADIO_SET="$now"
    SYNC_WLAN="wlan-none"
    return
  fi
  for iface in $planned; do
    if ! supervised "$iface"; then
      # >&2 IS LOAD-BEARING, not tidiness. sync_radios used to be called inside
      # a command substitution, which captures stdout by reading a pipe until
      # every writer closes it. A background job inherits that pipe, so a
      # supervisor that never exits holds it open forever and the assignment
      # never returns -- boad was never reached and the box served nothing, with
      # the log simply stopping mid-startup. Measured 2026-09-09. The results
      # come back in a global now, but the redirect stays: it costs nothing and
      # the next caller to reach for a substitution should not be punished.
      #
      # stderr is not captured, so the supervisor's output still reaches the
      # container log exactly as before.
      boa-hostapd-supervise "$iface" >&2 &
      log "access point starting on $iface"
    fi
    out="${out:+$out,}$iface"
  done
  LAST_RADIO_SET="$now"
  SYNC_WLAN="$out"
}

wait_radios_settle
sync_radios
WLAN_PORTS="$SYNC_WLAN"
[ "$WLAN_PORTS" = "wlan-none" ] &&
  log "WARNING: no radio this box can serve; there will be no access point"

# ---------------------------------------------------------------------------
# 5. iperf3, for the unshaped ceiling.
# ---------------------------------------------------------------------------
#
# The container's version of overlay/etc/systemd/system/iperf3.service.
#
# WHAT A TEST AGAINST THIS SERVER MEASURES, which is NOT what that unit's header
# still claims. The unit says traffic to and from the box is exempt in both
# directions and so reports the link at full tilt against any policy. That was
# true of a blanket `ip src <box>` exemption and is not true now: shape.go
# deliberately scopes the exemption to the MANAGEMENT PORTS, and says so, "so
# `iperf3 -c <pi> -R` measures the downlink cap actually being enforced".
#
# Measured here on 2026-09-09 over the radio, against a 60/20 cap:
#
#   -R  (downlink, served BY this box)   56.9 Mbit/s  -- CONDITIONED
#       (uplink, terminating AT this box)  143 Mbit/s -- unconditioned
#
# The asymmetry is structural rather than a filter choice. Downlink leaves via
# the client's own port, where the client's policy is attached, and port 5201 is
# not in the exempt set. Uplink terminates here and never reaches the WAN port,
# which is the only place uplink shaping lives.
#
# So: -R against this server verifies the downlink cap. The forward direction
# gives the uplink ceiling. Neither substitutes for a host BEYOND the box, which
# is the only way to see both directions conditioned at once.
#
# Unprivileged, as the unit's DynamicUser= makes it: this process is
# deliberately reachable and deliberately hammered, and it needs nothing but the
# ability to move bytes. No bind address, for the reason the unit gives -- the
# bridge holds the box's only address, and which port a client arrives on is
# what decides whether a wireless or a wired path is being measured.
if [ "${BOA_IPERF:-1}" = 1 ]; then
  (
    while :; do
      setpriv --reuid=nobody --regid=nogroup --clear-groups \
        iperf3 --server --port 5201 >/dev/null 2>&1 || true
      log "iperf3 server exited; restarting in 5s"
      sleep 5
    done
  ) &
  log "iperf3 on :5201 -- measures the UNSHAPED ceiling, never enforcement"
fi

# ---------------------------------------------------------------------------
# 6. The daemon.
# ---------------------------------------------------------------------------
#
# Not exec'd. boad tears its queueing disciplines down on SIGTERM, and leaving a
# stopped daemon still conditioning traffic is called out in main.go as the most
# confusing failure this box can present -- so the signal has to reach it, and
# this shell has to still be here to forward it.
start_boad() {
  log "starting boad: bridge=$BOA_BRIDGE wan=$BOA_WAN_PORT wlan=$WLAN_PORTS lan=$LAN_PORTS"
  boad -addr "$BOA_ADDR" \
       -bridge "$BOA_BRIDGE" \
       -wan "$BOA_WAN_PORT" \
       -wlan "$WLAN_PORTS" \
       -lan "$LAN_PORTS" \
       -state /var/lib/infinite-streaming-boa/policies.json \
       $BOA_EXTRA_ARGS &
  BOAD_PID=$!
}

# SIGTERM has to reach boad, which is why this shell stays around instead of
# exec'ing: boad tears its queueing disciplines down on the way out, and main.go
# names a stopped daemon that is still conditioning traffic as the single most
# confusing failure this box can present.
forward() {
  log "signal received; asking boad to remove its conditioning"
  kill -TERM "$BOAD_PID" 2>/dev/null || true
  wait "$BOAD_PID" 2>/dev/null || true
  exit 0
}
trap forward TERM INT

start_boad

# ---------------------------------------------------------------------------
# 7. Hotplug.
# ---------------------------------------------------------------------------
#
# The container's stand-in for the Pi's udev rules and select-radio unit, and it
# only works with the matching half on the host: an adapter unplugged here
# re-enumerates in the HOST namespace, so the host has to move it back before
# there is anything for this loop to find. See the udev rule installed by
# scripts/docker-host-net.sh.
#
# The daemon is RESTARTED when the port set changes, not merely told about it:
# -wlan and -lan are start-up flags, and the Pi's select-radio restarts the
# daemon for exactly this reason. Restarting drops the queueing disciplines and
# rebuilds them from the persisted policy, which is what should happen when the
# set of ports being conditioned has changed underneath it.
#
# Five seconds, because that is fast enough that a replug feels immediate and
# slow enough that the cheap check below costs nothing. The expensive step,
# radioplan, runs only when the set of radios actually differs.
while :; do
  sleep 5

  if ! kill -0 "$BOAD_PID" 2>/dev/null; then
    log "boad exited; restarting it"
    start_boad
    continue
  fi

  # A PLANNED RADIO THAT NEVER REACHES AP MODE gets the plan redone.
  #
  # The settle wait above closes the race that caused this, but not every reason
  # a channel can become unusable: a self-managed phy can be re-regulated while
  # running, and a DFS event can take a channel away underneath a live AP. The
  # supervisor alone would retry the same rejected config every five seconds
  # forever, which is a radio that is down and a log that says why once a minute
  # and never acts.
  #
  # Three strikes, so a radio merely mid-restart is not re-planned out from under
  # itself. radioplan rewrites the config in place, so killing hostapd is all
  # that is needed: the supervisor's next attempt reads the new file.
  for iface in ${WLAN_PORTS//,/ }; do
    [ "$iface" = wlan-none ] && continue
    [ -e "/sys/class/net/$iface" ] || continue
    # An `ssid` line, NOT `type AP`. The interface keeps reporting type AP after
    # hostapd has given up on it -- measured 2026-09-09, when a cross-band move
    # left the radio as "type AP" with no ssid and no channel, serving nothing.
    # Checking the type therefore reset the strike counter on exactly the radios
    # this is meant to rescue. hostapd only publishes an ssid once the BSS is up.
    if /usr/sbin/iw dev "$iface" info 2>/dev/null | grep -q '^\s*ssid '; then
      eval "strikes_${iface//-/_}=0"
      continue
    fi
    # AN AP THAT HOSTAPD KNOWS IS DOWN IS NOT A WEDGED ONE.
    #
    # `disable AP` in the interface, and the daemon's own outage patterns, take
    # the BSS down through hostapd's control socket. hostapd stays healthy and
    # reports state=DISABLED, and there is no ssid on the interface -- which is
    # indistinguishable, from the kernel alone, from a radio that has died.
    #
    # Measured 2026-09-09: an operator disabling a radio had it restarted and
    # serving again within fifteen seconds, repeatedly, with the log cheerfully
    # announcing the rescue. A loop that undoes a deliberate action is worse
    # than no loop.
    #
    # The wedge this is FOR is the opposite reading and is documented at length
    # in radiopower.go (#182): hostapd asserting a BSS the kernel cannot find.
    # So the strike is only counted when hostapd claims to be ENABLED while the
    # interface has no ssid. hostapd saying DISABLED is it agreeing with the
    # kernel, and agreement is never the fault.
    if hostapd_cli -p /var/run/hostapd -i "$iface" status 2>/dev/null |
       grep -q '^state=DISABLED'; then
      eval "strikes_${iface//-/_}=0"
      continue
    fi
    eval "n=\${strikes_${iface//-/_}:-0}"
    n=$((n + 1))
    eval "strikes_${iface//-/_}=$n"
    if [ "$n" -ge 3 ]; then
      # RESTARTED on its existing config, not re-planned onto a new channel.
      # Re-running radioplan here rewrites the config, which throws away
      # whatever channel the operator chose -- so a radio that was briefly
      # unhealthy came back somewhere else, and a deliberate move looked like it
      # had been ignored. Restarting is the remedy for a wedged radio; choosing
      # a different channel is a decision, and not this loop's to make.
      log "$iface has not reached AP mode in ${n} checks; restarting its access point"
      eval "strikes_${iface//-/_}=0"
      if [ -s "/run/boa/hostapd/$iface.pid" ]; then
        kill "$(cat "/run/boa/hostapd/$iface.pid")" 2>/dev/null || true
      fi
    fi
  done

  sync_lan_ports
  new_lan="$SYNC_LAN"
  # WHICH RADIOS EXIST, and nothing finer.
  #
  # This watched the usable CHANNEL set for a while and that was wrong: a
  # channel set changes when a radio moves band, including when the operator
  # moves it, so the loop re-planned, saw the radio was not where the plan said,
  # and dragged it back. Measured 2026-09-09: a move to channel 149 was undone
  # within five seconds, twice, and the radio ended up down.
  #
  # radioplan is a BOOT-TIME and HOTPLUG-TIME decision on the Pi, never a
  # continuous one, and the daemon remembers an operator's deliberate channel
  # (#189) precisely so it survives. A container has no udev, so this loop is
  # the hotplug event -- and it must fire on the same thing udev would.
  if [ "$(radio_ifaces)" != "$LAST_RADIO_SET" ]; then
    sync_radios
    new_wlan="$SYNC_WLAN"
  else
    new_wlan="$WLAN_PORTS"
  fi

  if [ "$new_lan" != "$LAN_PORTS" ] || [ "$new_wlan" != "$WLAN_PORTS" ]; then
    log "port set changed: lan '$LAN_PORTS' -> '$new_lan', wlan '$WLAN_PORTS' -> '$new_wlan'"
    LAN_PORTS=$new_lan
    WLAN_PORTS=$new_wlan
    kill -TERM "$BOAD_PID" 2>/dev/null || true
    wait "$BOAD_PID" 2>/dev/null || true
    start_boad
  fi
done
