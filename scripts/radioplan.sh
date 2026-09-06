#!/bin/sh
# Discover every wireless interface, give each one a channel that does not clash
# with the others, and write it a hostapd config.
#
# Replaces a decision tree that matched hardware by NAME -- wlan-usb, wlan-usb2,
# wlan0 -- and needed a branch, a config and a hand-chosen channel added for
# every adapter. Three things that had to agree, and on 2026-09-06 they did not:
# a second dongle served nothing until all three were edited.
#
# Instance name IS the interface name, so hostapd@wlan-usb reads
# /etc/hostapd/boa-wlan-usb.conf and there is no usb -> wlan-usb mapping left to
# fall out of step. The unit template uses %i, which is literal, so a dash in
# the name is safe.

set -u

HOSTAPD_DIR=${HOSTAPD_DIR:-/etc/hostapd}
# Where an existing SSID and passphrase may be scavenged from. Separate from the
# write directory so the planner can be exercised into a scratch dir against the
# real box, which is how it is tested before it is trusted at boot.
HOSTAPD_SRC=${HOSTAPD_SRC:-/etc/hostapd}
APENV=/etc/infinite-streaming-boa/ap.env

# STDERR, not stdout. The caller reads this script's stdout to learn which
# interfaces were planned, so anything chatty on stdout gets captured as part of
# the answer -- which it was, producing systemd unit names like
# "hostapd@(phy12):" and a BOA_WLAN_PORT containing three lines of log text.
log() { echo "radioplan: $*" >&2; }

# --- what the access points are called, and how they are secured -------------
#
# From ap.env when the image wrote one; otherwise scavenged from whatever
# config already exists, so this works on a box built before ap.env existed.
# Refuses rather than guesses: an AP brought up with the wrong SSID or no
# passphrase is worse than one that did not come up, because it looks fine.
SSID=""; PASSPHRASE=""; COUNTRY=""
if [ -r "$APENV" ]; then
  . "$APENV"
  SSID="${AP_SSID:-}"; PASSPHRASE="${AP_PASSPHRASE:-}"; COUNTRY="${AP_COUNTRY:-}"
fi
if [ -z "$SSID" ] || [ -z "$PASSPHRASE" ]; then
  for f in "$HOSTAPD_SRC"/boa-*.conf; do
    [ -r "$f" ] || continue
    [ -n "$SSID" ]       || SSID=$(sed -n 's/^ssid=//p' "$f" | head -1)
    [ -n "$PASSPHRASE" ] || PASSPHRASE=$(sed -n 's/^wpa_passphrase=//p' "$f" | head -1)
    [ -n "$COUNTRY" ]    || COUNTRY=$(sed -n 's/^country_code=//p' "$f" | head -1)
  done
fi
[ -n "$COUNTRY" ] || COUNTRY=US
if [ -z "$SSID" ] || [ -z "$PASSPHRASE" ]; then
  log "no SSID or passphrase available; leaving the radios alone"
  exit 1
fi

# --- what each radio can actually do -----------------------------------------
#
# Asked of the phy, never assumed from the driver or the name. The onboard
# brcmfmac here is 2.4GHz only while the mt7921u dongles do both, and a plan
# built on the interface name would have to know that in advance.
#
# Channels marked "no IR" or "disabled" are excluded: no-IR means the radio may
# not transmit there without first hearing someone else, which an access point
# cannot rely on.
phy_channels() {   # $1=phy  $2=band (24|5)
  iw phy "$1" info 2>/dev/null | awk -v want="$2" '
    /^\t*Band 1:/ { band="24"; next }
    /^\t*Band 2:/ { band="5";  next }
    /^\t*Band [0-9]+:/ { band="other"; next }
    band == want && /\* [0-9.]+ MHz \[[0-9]+\]/ && !/disabled/ && !/no IR/ {
      match($0, /\[[0-9]+\]/)
      print substr($0, RSTART+1, RLENGTH-2)
    }'
}

# --- the plan ----------------------------------------------------------------
#
# 5GHz first and in this order: 36 and 149 are the two non-overlapping non-DFS
# 80MHz blocks in this regulatory domain, so the first two 5GHz-capable radios
# get a clean 80MHz each. Anything after that falls back to 2.4GHz, where the
# usual non-overlapping trio is 1, 6 and 11.
#
# Non-DFS on purpose: a DFS channel makes the radio listen for radar before it
# may beacon, so an access point can take a minute to appear -- and on a box
# whose whole subject is how fast a client reconnects, that is a minute of
# measurement that is really about the radar check.
PLAN_5="36 149"
PLAN_24="6 1 11"

taken=""
is_taken() { case " $taken " in *" $1 "*) return 0;; esac; return 1; }

pick_channel() {   # $1=phy -> echoes "channel band" or nothing
  for ch in $PLAN_5; do
    is_taken "$ch" && continue
    if phy_channels "$1" 5 | grep -qx "$ch"; then echo "$ch a"; return; fi
  done
  for ch in $PLAN_24; do
    is_taken "$ch" && continue
    if phy_channels "$1" 24 | grep -qx "$ch"; then echo "$ch g"; return; fi
  done
}

# --- render one config -------------------------------------------------------
write_conf() {   # $1=iface  $2=channel  $3=hw_mode(a|g)
  _if="$1"; _ch="$2"; _hw="$3"
  # 80MHz on 5GHz, 20MHz on 2.4. The centre index is the block's middle
  # channel: 42 covers 36-48, 155 covers 149-161.
  _vht=""
  if [ "$_hw" = a ]; then
    case "$_ch" in
      36|40|44|48)     _seg=42 ;;
      149|153|157|161) _seg=155 ;;
      *)               _seg="" ;;
    esac
    if [ -n "$_seg" ]; then
      _vht="ht_capab=[HT40+]
vht_oper_chwidth=1
vht_oper_centr_freq_seg0_idx=$_seg
he_oper_chwidth=1
he_oper_centr_freq_seg0_idx=$_seg"
    fi
  fi
  cat > "$HOSTAPD_DIR/boa-$_if.conf" <<CONF
# GENERATED by radioplan at boot. Edits are lost on the next run.
#
# One config per interface, named after it, so hostapd@$_if reads this file and
# there is no instance-to-interface mapping to keep in step.
interface=$_if
bridge=br-lan
driver=nl80211
ctrl_interface=/var/run/hostapd
ctrl_interface_group=0
country_code=$COUNTRY
ieee80211d=1
hw_mode=$_hw
channel=$_ch
ssid=$SSID
ignore_broadcast_ssid=0
wmm_enabled=1
ieee80211n=1
ieee80211ac=1
ieee80211ax=1
$_vht
# Advertise 802.11v BSS Transition Management, or a steer request is sent to a
# client that was never told the AP supports one (#192).
bss_transition=1
# Do NOT announce the access point starting or stopping (#224): hostapd
# broadcasts a deauthentication frame at both ends by default, which lands on
# exactly the clients a measurement is watching.
broadcast_deauth=0
auth_algs=1
wpa=2
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
wpa_passphrase=$PASSPHRASE
CONF
  chmod 0600 "$HOSTAPD_DIR/boa-$_if.conf"
}

# --- the plan supersedes a remembered channel ---------------------------------
#
# The daemon remembers the channel a radio settled on and puts it back when it
# drifts (#189), which is right for a radio nudged by a driver or a DFS event
# and wrong for one this script has just deliberately reassigned: the memory
# reads the new channel as drift and pulls it back.
#
# Observed 2026-09-06: the plan put wlan-usb on 36, the memory said 149, and the
# radio ended up on 149 alongside wlan-usb2 -- the exact overlap the plan exists
# to prevent, arrived at by two mechanisms each doing its own job correctly.
#
# So a plan that CHANGES a channel drops the memory of the old one. A plan that
# agrees with it leaves it alone, because an operator's deliberate mid-run move
# is exactly what #189 is for and a re-run of this script should not discard it.
CHANSTORE=${CHANSTORE:-/var/lib/infinite-streaming-boa/channels.json}

forget_remembered_channel() {   # $1=iface  $2=planned channel
  [ -r "$CHANSTORE" ] || return 0
  python3 - "$CHANSTORE" "$1" "$2" <<'PY' || true
import json, sys
path, iface, planned = sys.argv[1], sys.argv[2], int(sys.argv[3])
try:
    with open(path) as fh:
        data = json.load(fh)
except Exception:
    sys.exit(0)
entry = data.get(iface)
if not isinstance(entry, dict) or entry.get("channel") == planned:
    sys.exit(0)
del data[iface]
tmp = path + ".tmp"
with open(tmp, "w") as fh:
    json.dump(data, fh, indent=2)
import os
os.replace(tmp, path)
print("radioplan: %s was remembered on %s; the plan says %d, dropping the memory"
      % (iface, entry.get("channel"), planned), file=sys.stderr)
PY
}

# --- walk every radio --------------------------------------------------------
WANT=""
for link in /sys/class/net/*/phy80211; do
  [ -e "$link" ] || continue
  iface=$(basename "$(dirname "$link")")
  phy=$(basename "$(readlink "$link")")

  plan=$(pick_channel "$phy")
  if [ -z "$plan" ]; then
    log "$iface ($phy): no free channel it can use; leaving it down"
    continue
  fi
  ch=${plan% *}; hw=${plan#* }
  taken="$taken $ch"

  write_conf "$iface" "$ch" "$hw"
  forget_remembered_channel "$iface" "$ch"
  WANT="$WANT $iface"
  log "$iface ($phy): channel $ch, ${hw}"
done
WANT=$(echo "$WANT" | sed 's/^ *//')

echo "PLANNED=$WANT"
