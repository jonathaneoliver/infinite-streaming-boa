#!/usr/bin/env bash
#
# Runs INSIDE a privileged arm64 Linux container. macOS cannot mount ext4 or
# loop-mount a partition table, so every operation that touches the image
# filesystem lives here.
#
# Because the Mac is arm64 and Raspberry Pi OS is arm64, we can chroot into
# the mounted rootfs and run its native apt/dpkg with no qemu emulation.
#
# Inputs: AP_*/BOA_* environment variables, /cache (downloads), /work
# (scratch), /out (finished images), /overlay (files to graft into rootfs),
# /packages.txt (extra apt packages).
set -euo pipefail

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m warn:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

XZ_NAME="${XZ_NAME:?}"
OUT_NAME="${OUT_NAME:?}"
IMG="/work/${OUT_NAME}"
BOOT=/mnt/boot
ROOT=/mnt/root
LOOP=""

# Unwind in reverse order no matter how we exit, otherwise a failed build
# leaves the loop device attached and the next run cannot re-attach it.
cleanup() {
  set +e
  for m in "$ROOT/dev/pts" "$ROOT/dev" "$ROOT/proc" "$ROOT/sys" "$BOOT" "$ROOT"; do
    mountpoint -q "$m" && umount -l "$m"
  done
  [ -n "$LOOP" ] && losetup -d "$LOOP" 2>/dev/null
}
trap cleanup EXIT

## 1. Verify and decompress -------------------------------------------------
cd /cache
if [ -f "${XZ_NAME}.sha256" ]; then
  log "Verifying checksum of ${XZ_NAME}"
  sha256sum -c "${XZ_NAME}.sha256" --status \
    || die "checksum mismatch on ${XZ_NAME} — delete cache/ and retry"
else
  warn "no .sha256 alongside ${XZ_NAME}; skipping verification"
fi

log "Decompressing to ${OUT_NAME} (takes a minute)"
rm -f "$IMG"
xz -dc "/cache/${XZ_NAME}" > "$IMG"

## 2. Grow, attach and mount -------------------------------------------------
# Raspberry Pi OS Lite ships with only tens of megabytes free in its root
# filesystem -- sized to be small, on the assumption it expands on first boot.
# That is too late for us: everything installed here happens at BUILD time, and
# without headroom apt fails with "not enough free space in
# /var/cache/apt/archives" partway through, leaving a half-populated image.
#
# Growing costs nothing at runtime. The Pi still expands the partition to fill
# the card on first boot regardless of the size it started at.
GROW_MB="${GROW_MB:-1600}"
log "Growing image by ${GROW_MB}MB for build-time installs"
truncate -s "+${GROW_MB}M" "$IMG"

LOOP=$(losetup -f --show -P "$IMG")
log "Attached $IMG at $LOOP"

# Partition nodes are created asynchronously by the kernel; without this wait
# the mount below intermittently fails on an otherwise-correct build.
for _ in $(seq 1 50); do
  [ -b "${LOOP}p2" ] && break
  sleep 0.1
done
[ -b "${LOOP}p2" ] || die "partitions never appeared under $LOOP"

# Extend the root partition into the space just added, then the filesystem
# inside it. resize2fs insists on a clean check first, and both must happen
# while the filesystem is unmounted.
parted -s "$LOOP" resizepart 2 100%
partprobe "$LOOP" 2>/dev/null || true
losetup -c "$LOOP" 2>/dev/null || true
for _ in $(seq 1 50); do
  [ -b "${LOOP}p2" ] && break
  sleep 0.1
done
e2fsck -fy "${LOOP}p2" >/dev/null 2>&1 || true
resize2fs "${LOOP}p2" >/dev/null 2>&1 \
  || warn "could not grow the root filesystem; build-time installs may fail"

mkdir -p "$BOOT" "$ROOT"
mount "${LOOP}p2" "$ROOT"
mount "${LOOP}p1" "$BOOT"
log "Mounted boot ($(findmnt -no FSTYPE "$BOOT")) and root ($(findmnt -no FSTYPE "$ROOT"))"
log "Root filesystem free space: $(df -h "$ROOT" | awk 'NR==2{print $4}')"

## 3. Headless access -------------------------------------------------------
# An empty /ssh on the boot partition switches sshd on at first boot.
touch "$BOOT/ssh"

# Recent Raspberry Pi OS ships with NO default user; if userconf.txt is absent
# the first boot stops at an interactive account-creation prompt, which on a
# headless box means a device that never comes up.
if [ -n "${BOA_PASSWORD:-}" ]; then
  HASH=$(openssl passwd -6 "$BOA_PASSWORD")
else
  HASH='*'   # locked password — key-only login
fi
printf '%s:%s\n' "$BOA_USER" "$HASH" > "$BOOT/userconf.txt"
log "User '$BOA_USER' will be created on first boot"

# Passwordless sudo for that account. scripts/deploy.sh is the normal loop --
# many times an hour -- and it installs a binary and restarts a unit over a
# NON-INTERACTIVE ssh session, where sudo has no terminal to prompt on. It dies
# with "a terminal is required to read the password" AFTER copying the binary,
# so the box goes on running the old one while the deploy looks like it merely
# stumbled at the last step.
#
# Raspberry Pi OS grants this to its own first user through a 010_pi-nopasswd
# drop-in. userconf.txt creates an account but carries no such rule, and stock
# /etc/sudoers has %sudo requiring a password -- so a freshly flashed box cannot
# be deployed to at all. That is easy to misdiagnose, because 010_global-tty
# makes sudo's timestamp global across sessions: one interactive sudo makes
# deploys work for about fifteen minutes, which looks like a fixed box rather
# than a borrowed credential about to expire.
#
# NOPASSWD: ALL rather than the four commands deploy.sh runs. A narrower rule
# would buy nothing -- `install` to an arbitrary path is already root -- while
# breaking the `tc`/`ip`/`nft` verification loop this repository runs on.
SUDOERS="$ROOT/etc/sudoers.d/010_${BOA_USER}-nopasswd"
install -d -m 0755 "$ROOT/etc/sudoers.d"
printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$BOA_USER" > "$SUDOERS"
# 0440 is required, not tidiness: sudo ignores a group- or world-writable
# drop-in and says nothing, leaving the box prompting again with no clue why.
chmod 0440 "$SUDOERS"
# A malformed drop-in breaks sudo outright, on a headless box, after a reflash.
if command -v visudo >/dev/null 2>&1; then
  visudo -c -f "$SUDOERS" >/dev/null || die "generated sudoers file is invalid"
else
  warn "visudo unavailable in the build image; sudoers drop-in not validated"
fi
log "Passwordless sudo enabled for '$BOA_USER' (deploy.sh needs it)"

if [ -n "${BOA_SSH_PUBKEY:-}" ]; then
  # Keys go under /etc/ssh rather than ~/.ssh because the user's home does not
  # exist yet — it is created by the first-boot account step.
  #
  # BOA_SSH_PUBKEY may hold SEVERAL keys, one per line, so a spare on another
  # machine survives losing the primary. With password login disabled the key is
  # the only way in over the network, and a single key means one lost laptop is a
  # reflash.
  #
  # Lines are copied through individually rather than dumping the variable
  # whole: a trailing blank line, an indented paste or a CR from a Windows
  # clipboard all produce an authorized_keys entry sshd silently ignores, and
  # "silently ignores" here means locked out of a headless box.
  AK="$ROOT/etc/ssh/authorized_keys.d/$BOA_USER"
  install -d -m 0755 "$ROOT/etc/ssh/authorized_keys.d"
  : > "$AK"
  NKEYS=0
  while IFS= read -r line; do
    line="${line%$'\r'}"                       # CRLF paste
    line="${line#"${line%%[![:space:]]*}"}"    # leading whitespace
    [ -z "$line" ] && continue
    case "$line" in \#*) continue ;; esac
    printf '%s\n' "$line" >> "$AK"
    NKEYS=$((NKEYS + 1))
  done <<EOF
$BOA_SSH_PUBKEY
EOF
  chmod 0644 "$AK"
  [ "$NKEYS" -gt 0 ] || die "BOA_SSH_PUBKEY is set but contained no usable key lines"
  log "Authorised $NKEYS SSH key(s) for '$BOA_USER'"
  # PasswordAuthentication is disabled whenever a key is present, INDEPENDENT of
  # whether an account password is set. Those are two different doors and were
  # previously conflated: setting a password for console access silently left
  # password login enabled over the network too, so the box accepted an
  # eight-character secret from anyone associated to a broadcasting AP.
  #
  # The account password stays. It is what the physical console asks for, and it
  # is the way back in if the key is ever lost -- emptying BOA_PASSWORD locks the
  # account with '*' and makes a lost key mean reflashing the card.
  #
  # This is also what makes the passwordless sudo rule above defensible rather
  # than a shortcut: the credential guarding the box becomes the key, not a short
  # password, and anyone who gets a shell as $BOA_USER already has the box
  # regardless of what sudo asks for.
  #
  # BOA_SSH_PASSWORD_LOGIN=true opts back into password authentication, for the
  # operator who wants a way in from a machine that has no key on it. It is off
  # by default because that fallback is reachable by everyone else too: sshd will
  # accept BOA_PASSWORD from anything associated to the AP. Keep it deliberate
  # and visible rather than implied by whether a password happens to be set,
  # which is the coupling that hid this for so long.
  if [ "${BOA_SSH_PASSWORD_LOGIN:-false}" = "true" ]; then
    PW_AUTH="yes"
  else
    PW_AUTH="no"
  fi
  {
    echo "# boa: key-based login for the preconfigured account"
    echo "AuthorizedKeysFile .ssh/authorized_keys /etc/ssh/authorized_keys.d/%u"
    echo "PasswordAuthentication $PW_AUTH"
    echo "KbdInteractiveAuthentication no"
  } > "$ROOT/etc/ssh/sshd_config.d/boa.conf"
  if [ "$PW_AUTH" = "yes" ]; then
    log "SSH keys installed; password login ALSO enabled (BOA_SSH_PASSWORD_LOGIN)"
  else
    log "SSH key installed; password login over the network disabled"
  fi
  [ -n "${BOA_PASSWORD:-}" ] \
    && log "Account password kept for the console and for recovery"
else
  warn "No BOA_SSH_PUBKEY: the box will accept password logins over SSH."
  warn "Set one in .env — it is the credential the sudo rule relies on."
fi

## 3b. USB power budget (Pi 5) ---------------------------------------------
# The Pi 5 caps TOTAL USB peripheral current to 600mA unless it detects a
# 5V/5A supply. That is enough to brown out two hungry dongles at once -- a
# USB Wi-Fi radio plus a USB ethernet adapter -- which presents as an adapter
# that "does not work when the other is plugged in" and, tellingly, never trips
# the undervoltage flag: the cap is a proactive limit, not a measured sag.
# usb_max_current_enable=1 tells the firmware to allow the full 1.6A budget.
#
# OFF by default, and deliberately so: on a supply that genuinely cannot deliver
# 1.6A this lets the Pi draw into a sagging rail and makes brownouts WORSE.
# Enable it only with the official 27W (5A) PSU or a powered USB hub.
if [ "${BOA_USB_MAX_CURRENT:-0}" = "1" ]; then
  CONFIG="$BOOT/config.txt"
  if [ ! -f "$CONFIG" ]; then
    warn "BOA_USB_MAX_CURRENT=1 but $CONFIG is absent; not applied"
  elif grep -q '^usb_max_current_enable=' "$CONFIG"; then
    sed -i 's/^usb_max_current_enable=.*/usb_max_current_enable=1/' "$CONFIG"
    log "USB max current: full 1.6A budget (updated existing line)"
  else
    printf '\n# boa: allow the full 1.6A USB current budget (BOA_USB_MAX_CURRENT).\nusb_max_current_enable=1\n' >> "$CONFIG"
    log "USB max current: full 1.6A budget (needs a 5A PSU or powered hub)"
  fi
else
  log "USB max current: default (Pi 5 caps USB to 600mA without a 5A PSU)"
fi

## 4. Identity --------------------------------------------------------------
echo "$BOA_HOSTNAME" > "$ROOT/etc/hostname"
sed -i "s/^127\.0\.1\.1.*/127.0.1.1\t${BOA_HOSTNAME}/" "$ROOT/etc/hosts"

if [ -n "${BOA_TIMEZONE:-}" ]; then
  echo "$BOA_TIMEZONE" > "$ROOT/etc/timezone"
  ln -sf "/usr/share/zoneinfo/$BOA_TIMEZONE" "$ROOT/etc/localtime"
fi

## 5. Transparent bridge ---------------------------------------------------
# boa operates as a transparent bridge (layer 2): the WAN port, the wireless
# AP and the USB wired port all sit in ONE bridge, so clients get their
# addresses from the EXISTING upstream router and land on the existing subnet.
# Nothing under test can tell the Pi is in the path -- it is not a hop and does
# not appear in traceroute. That is the whole point of an impairment box.
#
# Consequences that drive the config below:
#   * No ipv4.method=shared anywhere. The Pi hands out no addresses and does no
#     NAT; doing either would make it a router and destroy transparency.
#   * The bridge takes a management address by DHCP from upstream, plus a fixed
#     rescue address (below) so a headless box is never unreachable just
#     because upstream DHCP was absent.
#   * The USB NIC is renamed to a stable name by udev, because a USB ethernet
#     adapter enumerates as eth1 or enx<mac> depending on kernel and udev
#     version, and a connection profile cannot reference a name that varies.
CONN_DIR="$ROOT/etc/NetworkManager/system-connections"
install -d -m 0755 "$CONN_DIR"

# NetworkManager silently ignores any profile that is group- or world-readable,
# because these files carry the passphrase in clear text.
write_conn() {
  local f="$CONN_DIR/$1.nmconnection"
  cat > "$f"
  chmod 0600 "$f"
  chown 0:0 "$f"
}

# The bridge itself. STP stays on: boa has two downstream ports plus a WAN
# port, so miscabling can physically create a loop, and a bridging loop takes
# down the entire upstream network rather than just this box. forward-delay is
# trimmed to the spec minimum so boot-to-traffic is ~4s rather than ~30s.
write_conn infinite-streaming-boa-br <<EOF
[connection]
id=infinite-streaming-boa-br
uuid=$(uuidgen)
type=bridge
interface-name=br-lan
autoconnect=true
autoconnect-priority=100

[bridge]
stp=true
forward-delay=2
priority=32768

[ipv4]
method=auto
# The rescue address is HERE, on the profile, not applied afterwards by a
# separate service.
#
# It used to be assigned by infinite-streaming-boa-rescue-ip.service once br-lan
# appeared, and that raced NetworkManager: putting an address on the bridge
# before NM activated its own profile made NM conclude the connection was
# externally managed, generate a throwaway connection named "br-lan", adopt it,
# and NEVER RUN DHCP. Measured: rescue-ip finished at 20:00:31.438, NM logged
# 'connection-assumed' at 20:00:31.483, and the box came up with a working
# bridge, a working AP and no lease.
#
# NM applies a static address alongside a DHCP one perfectly well, so letting it
# own both removes the race by construction rather than by ordering two units
# and hoping. Verified live: managed-type 'full', both addresses present.
address1=${BOA_RESCUE_IP}/24
# A missing upstream DHCP server must never block the bridge from coming up:
# the data path has to work even when management addressing does not.
may-fail=true

[ipv6]
method=auto
may-fail=true
EOF

# --- Bridge MAC: stable and per-board-unique -------------------------------
# A Linux bridge with no MAC set adopts the LOWEST MAC among its ports. With
# only the onboard NIC and onboard radio, that is eth0 (both are d8:3a:dd:...,
# eth0 numerically lower), so the bridge is already stable at eth0's MAC and
# nothing here is strictly needed. The instability is introduced by EXTERNAL
# USB adapters: a USB Wi-Fi or ethernet dongle usually has a lower OUI (e.g.
# 9c:ef:d5:...), so the bridge adopts the DONGLE's MAC and then CHANGES it every
# time a dongle hotplugs or browns out -- stranding remote ARP caches and making
# the box's DHCP address intermittently unreachable while IPv6 kept working.
#
# Fix: pin br-lan to the WAN port's burned-in MAC (the onboard NIC), which is
# globally unique per board so two boxes on one LAN never collide. That MAC is
# not knowable at build time, so unless BOA_BRIDGE_MAC is given we set it at
# first boot, BEFORE NetworkManager starts (the oneshot below).
#
# Why before NetworkManager and not after: NM's FIRST activation of br-lan then
# already reads the pinned MAC, so there is no reactivation and no bridge bounce
# -- avoiding by construction the concurrent-with-NM race that the rescue-IP
# service hit (see the [ipv4] note above). The unit is ordered strictly before
# NetworkManager.service and after the WAN device exists; it is idempotent, so
# running it every boot is safe.
BR_PROFILE="$CONN_DIR/infinite-streaming-boa-br.nmconnection"
if [ -n "${BOA_BRIDGE_MAC:-}" ]; then
  sed -i "/^\[bridge\]/a mac-address=${BOA_BRIDGE_MAC}" "$BR_PROFILE"
  log "Bridge MAC pinned to ${BOA_BRIDGE_MAC} (from BOA_BRIDGE_MAC)"
else
  install -D -m 0755 /dev/stdin "$ROOT/usr/local/sbin/infinite-streaming-boa-bridge-mac" <<'SCRIPT'
#!/bin/sh
# Pin br-lan's MAC to the WAN port's burned-in, per-board-unique MAC. Runs once
# before NetworkManager each boot; idempotent (the value never changes).
set -u
DEFAULTS=/etc/default/infinite-streaming-boa
WAN=$(sed -n 's/^BOA_WAN_PORT=//p' "$DEFAULTS" 2>/dev/null)
WAN=${WAN:-eth0}
PROFILE=/etc/NetworkManager/system-connections/infinite-streaming-boa-br.nmconnection
MAC=$(cat "/sys/class/net/$WAN/address" 2>/dev/null || true)
case "$MAC" in
  ??:??:??:??:??:??) : ;;
  *) echo "boa-bridge-mac: no usable MAC on $WAN; leaving br-lan at its default" >&2; exit 0 ;;
esac
[ -f "$PROFILE" ] || { echo "boa-bridge-mac: $PROFILE missing" >&2; exit 0; }
if grep -q '^mac-address=' "$PROFILE"; then
  sed -i "s/^mac-address=.*/mac-address=$MAC/" "$PROFILE"
else
  sed -i '/^\[bridge\]/a mac-address='"$MAC" "$PROFILE"
fi
chmod 0600 "$PROFILE"
echo "boa-bridge-mac: br-lan pinned to $WAN MAC $MAC" >&2
SCRIPT

  install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-bridge-mac.service" <<UNIT
[Unit]
Description=Pin br-lan MAC to the ${BOA_WAN_PORT} (WAN) MAC
Wants=sys-subsystem-net-devices-${BOA_WAN_PORT}.device
After=sys-subsystem-net-devices-${BOA_WAN_PORT}.device
Before=NetworkManager.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/infinite-streaming-boa-bridge-mac

[Install]
WantedBy=NetworkManager.service
UNIT

  install -d -m 0755 "$ROOT/etc/systemd/system/NetworkManager.service.wants"
  ln -sf /etc/systemd/system/infinite-streaming-boa-bridge-mac.service \
    "$ROOT/etc/systemd/system/NetworkManager.service.wants/infinite-streaming-boa-bridge-mac.service"
  log "Bridge MAC will be pinned to ${BOA_WAN_PORT}'s MAC before NetworkManager starts"
fi

# WAN port: the link to the existing network. Enslaved to the bridge, so it
# carries no address of its own.
write_conn infinite-streaming-boa-wan <<EOF
[connection]
id=infinite-streaming-boa-wan
uuid=$(uuidgen)
type=ethernet
interface-name=${BOA_WAN_PORT}
master=br-lan
slave-type=bridge
autoconnect=true
autoconnect-priority=100
EOF

# Downstream wired port (the USB adapter), renamed to lan0 by the udev rule
# below. Absent hardware simply means this profile never activates.
write_conn infinite-streaming-boa-lan <<EOF
[connection]
id=infinite-streaming-boa-lan
uuid=$(uuidgen)
type=ethernet
interface-name=lan0
master=br-lan
slave-type=bridge
autoconnect=true
autoconnect-priority=90
EOF

# The access point is driven by hostapd on BOTH radios (see 5b) -- the onboard
# wlan0 the same way as the USB adapter -- so there is no NetworkManager AP
# profile any more. wlan0, like wlan-usb, is left unmanaged (the unmanaged-devices
# conf in 5b) so hostapd can own it; hostapd adds the radio to br-lan itself
# (bridge=br-lan in its config).

log "Bridge br-lan: ${BOA_WAN_PORT} (wan) + wlan0/wlan-usb (ap '${AP_SSID}', hostapd) + lan0 (usb)"

# Stable names for USB network adapters. On a Pi 4/5 the onboard NIC is not a
# USB device, so ID_BUS==usb identifies an add-on adapter unambiguously.
# (On a Pi 3 the onboard NIC *is* USB and this heuristic would not hold.)
#
# DEVTYPE is what separates them, and its absence was a real bug: the rule said
# "first USB ethernet adapter" but only tested ID_BUS, so it matched ANY USB
# network device. Plugging in a USB Wi-Fi dongle renamed the RADIO to lan0 --
# the name reserved for the wired downstream port -- and NetworkManager then
# declined to bind its 802-3-ethernet profile to a wireless interface, leaving
# the wired port nameless and the dongle unusable. Nothing logged an error.
#
# Wireless devices set DEVTYPE=wlan; wired ones set no DEVTYPE at all, and an
# unset key compares unequal, so the first rule still matches USB ethernet.
cat > "$ROOT/etc/udev/rules.d/76-infinite-streaming-boa-usb-lan.rules" <<'UDEV'
# USB ethernet becomes lan0, the downstream wired port.
SUBSYSTEM=="net", ACTION=="add", ENV{ID_BUS}=="usb", ENV{DEVTYPE}!="wlan", ATTR{address}!="", NAME="lan0"
# A USB Wi-Fi adapter gets its own stable name, so it can never take lan0 and
# so the access point can be pointed at it by name.
SUBSYSTEM=="net", ACTION=="add", ENV{ID_BUS}=="usb", ENV{DEVTYPE}=="wlan", ATTR{address}!="", NAME="wlan-usb"
UDEV

# Rescue address. A transparent bridge has no address of its own by design, so
# if upstream DHCP is missing -- or the box is bench-tested with nothing in the
# WAN port -- there would be no way to reach the UI on a headless device. This
# secondary address is present regardless of DHCP.
log "Rescue address ${BOA_RESCUE_IP}/24 carried by the bridge profile"

## 5a. A console that answers "what is its address?" ------------------------
# Twice in one afternoon this box came up with a working bridge, a working
# access point and NO DHCP lease -- reachable by nobody, and the only thing on
# the screen was a login banner rendered seconds into boot, before DHCP had
# finished, showing 127.0.0.1 and the rescue address. It looked like a box with
# no network. It was a box whose banner was older than its network.
#
# Two separate faults, fixed separately below.
#
# 1. /etc/issue is rendered ONCE, when getty starts. Whatever is true a second
#    into boot is what stays on the screen. So the address is refreshed on a
#    timer, and reprinted to the console whenever it CHANGES -- which is the
#    moment an operator standing at the box actually needs to see.
#
# 2. The virtual terminal is wiped when getty starts, taking the boot messages
#    with it. TTYVTDisallocate=no keeps them, so whatever failed on the way up
#    is still on screen to read.
install -D -m 0644 /dev/stdin \
  "$ROOT/etc/systemd/system/getty@tty1.service.d/10-boa-keep-console.conf" <<'GETTY'
[Service]
# Keep the boot messages. The default wipes the VT, which discards exactly the
# output you need when the box did not come up the way you expected.
TTYVTDisallocate=no
GETTY

install -D -m 0755 /dev/stdin "$ROOT/usr/local/sbin/infinite-streaming-boa-console" <<CONSOLE
#!/bin/sh
# Keeps the console honest about how to reach this box.
#
# Prints ADDRESSES, not interfaces. ${BOA_WAN_PORT} has no address of its own --
# it is a bridge port, which is the whole design -- so "the eth0 IP" does not
# exist. What an operator wants is the address that answers, which lives on
# br-lan, plus whether the WAN port has a cable in it.
last=""
while :; do
  # Split them: the DHCP address is the one to type, the rescue address only
  # works from a machine cabled to this box with an address on that subnet.
  # Showing them as one undifferentiated list is how an operator ends up
  # pinging the unroutable one and concluding the box is dead.
  lan=\$(ip -4 -br addr show scope global br-lan 2>/dev/null | awk '{\$1="";\$2="";print}' | tr -s ' ')
  # The link-local address is the recovery path that costs the operator
  # nothing: it exists with or without DHCP, needs no router, and no
  # reconfiguration at the far end. It is derived by EUI-64 from the bridge
  # MAC, which is pinned above precisely so that it does not move.
  ll=\$(ip -6 -br addr show br-lan 2>/dev/null | tr ' ' '\n' | grep -m1 '^fe80::' | cut -d/ -f1)
  rescue=\$(ip -4 -br addr show scope link br-lan 2>/dev/null | awk '{\$1="";\$2="";print}' | tr -s ' ')
  [ -z "\$lan" ] && lan=" NONE -- no DHCP lease"
  carrier=\$(cat /sys/class/net/${BOA_WAN_PORT}/carrier 2>/dev/null)
  case "\$carrier" in 1) wan="up" ;; 0) wan="NO CABLE" ;; *) wan="?" ;; esac
  radio=\$(cat /etc/default/infinite-streaming-boa 2>/dev/null | sed -n 's/^BOA_WLAN_PORT=//p')
  ssid=\$(iw dev "\$radio" info 2>/dev/null | awk '/ssid/{print \$2}')

  now="\$lan|\$wan|\$radio|\$ssid"
  ip4=\$(echo \$lan | awk '{print \$1}' | cut -d/ -f1)
  {
    echo ""
    echo "  ${BOA_HOSTNAME}"
    echo "  address:  \$lan\${ip4:+     http://\$ip4/}"
    echo "  rescue:  \$rescue   (needs an address on that subnet at your end)"
    echo "  no DHCP?  ssh ${BOA_USER}@\${ll}%<your-interface>"
    echo "            IPv6 link-local: always present, needs no configuration"
    echo "  ${BOA_WAN_PORT} (wan): \$wan     radio: \${radio:-none} \${ssid:+(\$ssid)}"
    echo ""
  } > /etc/issue

  # Reprint on change, so it appears without anyone pressing a key. This is the
  # case that matters: an address arriving late, or going away.
  if [ "\$now" != "\$last" ]; then
    cat /etc/issue > /dev/tty1 2>/dev/null
    last="\$now"
  fi
  sleep 10
done
CONSOLE

install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-console.service" <<'UNIT'
[Unit]
Description=boa console address readout
After=NetworkManager.service

[Service]
ExecStart=/usr/local/sbin/infinite-streaming-boa-console
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

ln -sf /etc/systemd/system/infinite-streaming-boa-console.service \
  "$ROOT/etc/systemd/system/multi-user.target.wants/infinite-streaming-boa-console.service"
log "Console will show live addresses and keep boot messages"

## 5b. The access point radio: USB adapter preferred ------------------------
# The onboard radio runs the AP through NetworkManager, which drives AP mode via
# wpa_supplicant. That path does not work with a MediaTek mt7921u: activation
# ends in "Hotspot network creation took too long, failing activation", with
# wpa_supplicant noting "nl80211 driver interface is not designed to be used
# with ap_scan=2". Measured on a Panda/mt7921u adapter, on both bands.
#
# hostapd drives the same radio without complaint, and gets 80MHz where the
# onboard chip is running 20MHz -- which is the point of plugging one in, since
# the AP's ceiling is what bounds the top of a measured ladder.
#
# So both radios are driven by hostapd: the USB adapter when present, otherwise
# the onboard wlan0. NetworkManager used to run the onboard AP, but hostapd drives
# brcmfmac too -- and only hostapd exposes the ctrl_interface the daemon needs for
# per-client link events (issue #135), so unifying gives the onboard radio
# deauth/disassoc/deadzone as well. Exactly ONE radio runs, because the daemon has
# a single BOA_WLAN_PORT -- StationDump() and the bridge FDB scan both key off it,
# so a client on a second AP would be invisible to conditioning.
NL_UNMANAGED="$ROOT/etc/NetworkManager/conf.d/10-boa-unmanaged-usb-wifi.conf"
install -D -m 0644 /dev/stdin "$NL_UNMANAGED" <<'NMCONF'
# hostapd owns whichever radio serves the AP -- the USB adapter and the onboard
# wlan0 alike. Without this NetworkManager also claims them and the two fight
# over the interface; NM wins the race often enough to look random.
[keyfile]
unmanaged-devices=interface-name:wlan-usb,wlan0
NMCONF

# hostapd's own config, generated once per radio role. The AP settings come from
# .env, so both radios publish the same SSID and passphrase and a client sees
# ONE network across the two -- and, because both are bridged onto the same
# segment, keeps its address when it moves between them. select-radio (5b)
# starts one templated unit per config it wants running.
#
# The two radios differ in what hostapd may ask of them:
#   * USB mt7921u      -- 80MHz + 802.11ac/ax, and ACS (survey-based auto
#                         channel) when AP_CHANNEL=0. Verified AP-ENABLED at
#                         80MHz on mt7921u.
#   * onboard brcmfmac -- 20MHz, 802.11n only. It has NO survey support, so ACS
#                         is impossible: AP_CHANNEL=0 falls back to a fixed
#                         channel, and the VHT/HE 80MHz block is fatal ("Could
#                         not set channel for kernel driver") so it is omitted.
#
# The USB radio may publish a different SSID (AP_SSID_USB) -- same name as the
# onboard radio is the point in normal use, but a distinct one makes it visible
# which radio a device actually joined.
USB_SSID="${AP_SSID_USB:-$AP_SSID}"

emit_hostapd_conf() {   # $1=name  $2=class(usb|onboard)  $3=interface  $4=ssid
                        # $5=band(a|bg)  $6=channel
  _name=$1; _class=$2; _iface=$3; _ssid=$4; _band=$5; _chan=$6
  _hw=$([ "$_band" = "a" ] && echo a || echo g)
  if [ "$_class" = "usb" ]; then
    _ac=1; _ax=1
    if [ "$_chan" = "0" ]; then
      _wide="acs_num_scans=5"
    elif [ "$_band" = "a" ]; then
      # HT40 side and 80MHz centre, both DERIVED from the channel.
      #
      # There are two 80MHz blocks, not one: 42 centres 36/40/44/48 and 155
      # centres 149/153/157/161. A hardcoded 42 was correct while only the
      # first block was permitted and becomes a config that names a centre
      # 565MHz from its primary -- accepted by the parser, fatal at ENABLE:
      #
      #   20/40 MHz: center segment 0 (=42) and center freq 1 (=5745) not in sync
      #
      # 165 takes neither: the channel above it is "no IR", so it has no 40MHz
      # partner and runs at 20MHz. Mirrors apChannels in radioctl.go.
      case "$_chan" in
        40|48|153|161) _ht="[HT40-]" ;;
        165)           _ht="" ;;
        *)             _ht="[HT40+]" ;;
      esac
      case "$_chan" in
        36|40|44|48)     _c80=42 ;;
        149|153|157|161) _c80=155 ;;
        *)               _c80="" ;;
      esac
      if [ -n "$_c80" ]; then
        _wide="ht_capab=$_ht
vht_oper_chwidth=1
vht_oper_centr_freq_seg0_idx=$_c80
he_oper_chwidth=1
he_oper_centr_freq_seg0_idx=$_c80"
      else
        # 20MHz: no secondary and no centre. Both omitted rather than set to a
        # neutral-looking value, because a stale one is what fails the ENABLE.
        _wide=""
      fi
    else
      _wide=""
    fi
  else
    # onboard brcmfmac: 20MHz, 802.11n, no ACS. Fall back to a sane fixed channel.
    _ac=0; _ax=0; _wide=""
    if [ "$_chan" = "0" ]; then
      _chan=$([ "$_band" = "a" ] && echo 36 || echo 6)
    fi
  fi
  install -D -m 0600 /dev/stdin "$ROOT/etc/hostapd/boa-${_name}.conf" <<EOF
# Managed by infinite-streaming-boa (radio class: ${_class}, band ${_band}).
# Run by infinite-streaming-boa-hostapd@${_name}.service, which select-radio
# starts. More than one may run at once -- see select-radio.
interface=${_iface}
bridge=br-lan
driver=nl80211
# Control socket for per-client link events (deauth/disassoc); the daemon
# attaches to it (see daemon/internal/boa/hostapd.go, issue #135). Without this
# line neither hostapd_cli nor the daemon can drive the AP.
ctrl_interface=/var/run/hostapd
ctrl_interface_group=0
country_code=${AP_COUNTRY}
ieee80211d=1
hw_mode=${_hw}
channel=${_chan}
ssid=${_ssid}
ignore_broadcast_ssid=$([ "$AP_HIDDEN" = "true" ] && echo 1 || echo 0)
wmm_enabled=1
ieee80211n=1
ieee80211ac=${_ac}
ieee80211ax=${_ax}
${_wide}
auth_algs=1
wpa=2
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
wpa_passphrase=${AP_PASSWORD}
EOF
}

# Three configs, because the onboard radio is used two different ways.
#
#   usb       -- the adapter, on AP_BAND/AP_CHANNEL. 80MHz, ac/ax, ACS.
#   onboard   -- the onboard chip when it is the ONLY radio, so it honours
#                AP_BAND/AP_CHANNEL exactly as it did before.
#   onboard24 -- the onboard chip alongside the adapter, pinned to 2.4GHz.
#
# The last one is what makes the box behave like a dual-band router: the
# adapter takes 5GHz where its 80MHz/ax is worth having, and the onboard chip
# takes 2.4GHz where its 20MHz/802.11n ceiling costs nothing it could have
# delivered anyway. Same SSID and passphrase on both, bridged onto the same
# segment, so a client sees one network and keeps its address across a roam.
emit_hostapd_conf usb       usb     wlan-usb "$USB_SSID" "$AP_BAND" "$AP_CHANNEL"
emit_hostapd_conf onboard   onboard wlan0    "$AP_SSID"  "$AP_BAND" "$AP_CHANNEL"
emit_hostapd_conf onboard24 onboard wlan0    "$AP_SSID"  bg         "$AP_CHANNEL_24"

# Our own unit rather than the packaged hostapd.service, which Debian ships
# masked and pointed at /etc/default/hostapd. Ours is started and stopped by the
# selector below, never enabled directly: with no adapter plugged in it would
# fail on every boot and look like a broken box.
install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-hostapd@.service" <<'UNIT'
[Unit]
Description=boa access point on %i (hostapd)
# After, but deliberately NOT BindsTo.
#
# BindsTo= on a .device unit makes systemd TRIGGER this service the moment the
# device appears -- systemctl show reported TriggeredBy=...wlan-usb.device --
# so hostapd started itself at boot without the selector running at all. That
# defeated the entire point of having a selector: the onboard radio was never
# switched off, BOA_WLAN_PORT was never updated, and every ordering constraint
# placed on select-radio was irrelevant because the unit being ordered was not
# the one starting. Measured on a boot that failed: select-radio "not started",
# hostapd active, NetworkManager having assumed br-lan as an external
# connection and never run DHCP.
#
# The selector starts and stops this service. It is the only thing that should.
#
# A TEMPLATE, because two of these run at once in the dual-band case: %i names
# the config, so boa-usb.conf and boa-onboard24.conf are separate instances
# with separate lifetimes. hostapd names its control socket after the
# INTERFACE, not the config, so /var/run/hostapd/wlan-usb and
# /var/run/hostapd/wlan0 appear side by side and the daemon can drive either.
After=NetworkManager.service

[Service]
ExecStart=/usr/sbin/hostapd /etc/hostapd/boa-%i.conf
Restart=on-failure
RestartSec=5
UNIT

install -D -m 0755 /dev/stdin "$ROOT/usr/local/sbin/infinite-streaming-boa-select-radio" <<'SEL'
#!/bin/sh
# Brings up the access point on every radio the box has.
#
#   USB adapter + onboard -> BOTH serve, like a dual-band router: the adapter
#                            on 5GHz (80MHz, ax) and the onboard chip on
#                            2.4GHz (20MHz, n), one SSID across the two.
#   USB adapter only      -> hostapd on wlan-usb.
#   onboard only          -> hostapd on wlan0, honouring AP_BAND/AP_CHANNEL.
#
# The onboard radio used to be rfkilled whenever an adapter was present,
# because the daemon watched a single interface and anything associated to a
# second AP was invisible to conditioning. The daemon now watches a list, so
# the reason for switching it off is gone -- and 2.4GHz coverage that the
# adapter's 5GHz cannot reach is worth having on a box whose subject is what
# real clients do on real radios.
#
# The onboard radio is identified by its rfkill device path NOT containing
# /usb, rather than by phy index or driver name: phy numbering depends on probe
# order, so "phy0 is the built-in" is true until the day it is not.
set -u
DEFAULTS=/etc/default/infinite-streaming-boa
# Read from the daemon's config rather than baked in, so the two cannot disagree.
WAN_PORT=$(sed -n 's/^BOA_WAN_PORT=//p' "$DEFAULTS" 2>/dev/null)
WAN_PORT=${WAN_PORT:-eth0}
log() { logger -t boa-select-radio "$*"; echo "$*"; }

onboard_rfkill() {
  for r in /sys/class/rfkill/rfkill*; do
    [ -e "$r/type" ] || continue
    [ "$(cat "$r/type")" = "wlan" ] || continue
    case "$(readlink -f "$r/device")" in
      */usb*) ;;
      *) echo "$r" ;;
    esac
  done
}

# The onboard radio must be UNBLOCKED for hostapd to bring it up, in every
# case now: it either serves alongside the adapter or serves alone.
for r in $(onboard_rfkill); do echo 0 > "$r/soft" 2>/dev/null; done

HAVE_USB=0; [ -d /sys/class/net/wlan-usb ] && HAVE_USB=1
HAVE_ONBOARD=0; [ -d /sys/class/net/wlan0 ] && HAVE_ONBOARD=1

if [ "$HAVE_USB" = 1 ]; then
  # Pin the bridge MAC before hostapd adds a radio to br-lan. A bridge takes
  # the LOWEST MAC among its members and recalculates as members come and go,
  # so an adapter sorting below the onboard NIC changes the box's identity --
  # and with it the IPv6 link-local address that is the no-configuration way
  # back in when DHCP has failed. Two radios joining makes this more likely,
  # not less.
  #
  # Guarded on a DIFFERENCE: re-setting a MAC to the value it already holds
  # still raises a netlink change event, and doing that while NetworkManager is
  # mid-DHCP is the kind of thing that costs an evening.
  WANMAC=$(cat "/sys/class/net/$WAN_PORT/address" 2>/dev/null)
  HAVEMAC=$(cat /sys/class/net/br-lan/address 2>/dev/null)
  [ -n "$WANMAC" ] && [ "$WANMAC" != "$HAVEMAC" ] \
    && ip link set dev br-lan address "$WANMAC" 2>/dev/null
fi

# Decide which hostapd instances should be running, and which must not be.
# Naming the losers explicitly matters: unplugging the adapter has to STOP the
# instance that was serving it, or hostapd sits restarting forever against an
# interface that is gone.
if [ "$HAVE_USB" = 1 ] && [ "$HAVE_ONBOARD" = 1 ]; then
  RUN="usb onboard24"; STOP="onboard"
  WANT="wlan-usb wlan0"
  log "Both radios: hostapd on wlan-usb (5GHz) and wlan0 (2.4GHz)"
elif [ "$HAVE_USB" = 1 ]; then
  RUN="usb"; STOP="onboard onboard24"
  WANT="wlan-usb"
  log "USB radio only: hostapd on wlan-usb"
else
  RUN="onboard"; STOP="usb onboard24"
  WANT="wlan0"
  log "No USB radio: hostapd on the onboard wlan0"
fi

for i in $STOP; do
  systemctl stop "infinite-streaming-boa-hostapd@$i.service" 2>/dev/null
done
for i in $RUN; do
  # restart, not start, so a radio swap re-reads a config that may have changed
  systemctl restart "infinite-streaming-boa-hostapd@$i.service"
done

# The daemon watches every radio in this list. It is ONE shell word on purpose:
# the unit passes it as ${BOA_WLAN_PORT}, which systemd expands as a single
# argument, and -wlan splits it. Restart only when it actually changed -- a
# restart drops a running sweep.
CUR=$(sed -n 's/^BOA_WLAN_PORT=//p' "$DEFAULTS" 2>/dev/null)
if [ "$CUR" != "$WANT" ]; then
  sed -i "s/^BOA_WLAN_PORT=.*/BOA_WLAN_PORT=$WANT/" "$DEFAULTS"
  log "BOA_WLAN_PORT '$CUR' -> '$WANT'; restarting daemon"
  systemctl restart infinite-streaming-boa.service
fi
SEL

install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-select-radio.service" <<'UNIT'
[Unit]
Description=boa access point radio selection
# After the network is actually UP, not merely after NetworkManager started.
#
# This unit starts hostapd, which adds wlan-usb to br-lan. Adding a port to a
# bridge running STP triggers a topology recalculation, and br-lan can drop
# carrier for a moment. If that lands in the middle of NetworkManager's DHCP
# transaction, the lease never arrives -- the box comes up with a working
# bridge, a working access point, and no address, reachable by nobody. That
# happened twice, and both times the boot before it had worked, because
# After=NetworkManager.service only orders against the daemon STARTING and
# leaves the rest to chance.
#
# Measured on a boot that succeeded: the lease landed at 19:05:43.16 and
# hostapd added the interface at 19:05:44.57 -- a margin of 1.4 seconds. That
# margin is what wait-online makes deterministic instead of lucky.
#
# Wants, not Requires: if wait-online times out because there is genuinely no
# upstream DHCP, the radio must still come up. An access point with no internet
# is a working appliance; no access point is not.
After=NetworkManager.service NetworkManager-wait-online.service infinite-streaming-boa.service
Wants=NetworkManager.service NetworkManager-wait-online.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/infinite-streaming-boa-select-radio

[Install]
WantedBy=multi-user.target
UNIT

# Hotplug. --no-block matters: udev waits for RUN programs, and this one starts
# a unit that restarts the daemon, which would deadlock against udev settling.
cat > "$ROOT/etc/udev/rules.d/77-infinite-streaming-boa-radio.rules" <<'UDEV'
SUBSYSTEM=="net", ENV{DEVTYPE}=="wlan", ENV{ID_BUS}=="usb", ACTION=="add", \
  RUN+="/usr/bin/systemctl restart --no-block infinite-streaming-boa-select-radio.service"
SUBSYSTEM=="net", ENV{DEVTYPE}=="wlan", ACTION=="remove", \
  RUN+="/usr/bin/systemctl restart --no-block infinite-streaming-boa-select-radio.service"
UDEV

ln -sf /etc/systemd/system/infinite-streaming-boa-select-radio.service \
  "$ROOT/etc/systemd/system/multi-user.target.wants/infinite-streaming-boa-select-radio.service"
log "AP radio: hostapd on the USB adapter when present, else the onboard radio"

## 6. Wireless regulatory domain -------------------------------------------
# The radio is rfkill-blocked until a country is set — the single most common
# reason a home-built Pi AP comes up with no wireless at all. Belt and braces:
# the module option applies from the first moment cfg80211 loads, and the
# first-boot script sets the same value through the Pi's own config path.
echo "options cfg80211 ieee80211_regdom=${AP_COUNTRY}" \
  > "$ROOT/etc/modprobe.d/infinite-streaming-boa-regdom.conf"

## 7. Overlay files ---------------------------------------------------------
if [ -d /overlay ] && [ -n "$(ls -A /overlay 2>/dev/null)" ]; then
  log "Grafting overlay/ into rootfs"
  cp -a /overlay/. "$ROOT/"
fi

## 8. Extra packages, installed natively via chroot -------------------------
# Required packages are checked rather than assumed: NetworkManager's shared
# mode needs the dnsmasq binary, and it fails at runtime (no DHCP, clients
# associate but get no address) rather than at config time if it is missing.
mount --bind /dev "$ROOT/dev"
mount --bind /dev/pts "$ROOT/dev/pts"
mount -t proc proc "$ROOT/proc"
mount -t sysfs sys "$ROOT/sys"
cp "$ROOT/etc/resolv.conf" "$ROOT/etc/resolv.conf.boa-bak" 2>/dev/null || true
echo "nameserver 1.1.1.1" > "$ROOT/etc/resolv.conf"

WANT=()
[ -f /packages.txt ] && mapfile -t WANT < <(grep -vE '^\s*(#|$)' /packages.txt)

MISSING=()
for pkg in dnsmasq-base "${WANT[@]}"; do
  chroot "$ROOT" dpkg -s "$pkg" >/dev/null 2>&1 || MISSING+=("$pkg")
done

if [ ${#MISSING[@]} -gt 0 ]; then
  log "Installing into image: ${MISSING[*]}"
  chroot "$ROOT" env DEBIAN_FRONTEND=noninteractive apt-get -qq update
  chroot "$ROOT" env DEBIAN_FRONTEND=noninteractive \
    apt-get -qq install -y --no-install-recommends "${MISSING[@]}"
  chroot "$ROOT" apt-get -qq clean
  rm -rf "$ROOT/var/lib/apt/lists/"*
else
  log "All required packages already present in the base image"
fi

# Report what the AP depends on at runtime, so a missing piece is a loud build
# failure rather than a silent no-DHCP mystery on the bench.
log "Runtime dependency check:"
for bin in NetworkManager dnsmasq avahi-daemon tc; do
  if chroot "$ROOT" sh -c "command -v $bin >/dev/null 2>&1"; then
    printf '      \033[32mok\033[0m       %s\n' "$bin"
  else
    printf '      \033[33mMISSING\033[0m  %s\n' "$bin"
  fi
done

mv -f "$ROOT/etc/resolv.conf.boa-bak" "$ROOT/etc/resolv.conf" 2>/dev/null || true

## 8b. Kernel module check --------------------------------------------------
# The conditioner needs sch_htb + sch_netem (queueing disciplines) and ifb, the
# Intermediate Functional Block device — a virtual interface used to shape the
# uplink, because an ingress qdisc can only police (drop) traffic, not queue
# and delay it. If any are absent the daemon can never work, so fail the BUILD
# rather than ship an image that looks fine and silently conditions nothing.
# Raspberry Pi OS ships SEVERAL kernel module trees in one image -- e.g.
# -rpi-2712 for the Pi 5 and -rpi-v8 for the Pi 4/3 -- and the running Pi picks
# one at boot. Checking only the first would pass the build while the module was
# absent on the board the user actually owns, so every tree must satisfy every
# requirement.
MODFAIL=""
for KREL in $(ls "$ROOT/lib/modules"); do
  log "Kernel modules present in $KREL:"
  for m in sch_htb sch_netem sch_prio cls_u32; do
    if find "$ROOT/lib/modules/$KREL" -name "${m}.ko*" 2>/dev/null | grep -q .; then
      printf '      \033[32mok\033[0m       %s\n' "$m"
    elif grep -qw "$m" "$ROOT/lib/modules/$KREL/modules.builtin" 2>/dev/null; then
      printf '      \033[32mok\033[0m       %s (built-in)\n' "$m"
    else
      printf '      \033[31mMISSING\033[0m  %s\n' "$m"
      MODFAIL=1
    fi
  done
done
[ -n "$MODFAIL" ] && die "base image kernel lacks modules the conditioner requires"

# Load at boot so the daemon never races a first modprobe.
printf 'sch_htb\nsch_netem\ncls_u32\n' > "$ROOT/etc/modules-load.d/infinite-streaming-boa.conf"

## 8c. boa daemon ----------------------------------------------------------
# The daemon's runtime configuration is written here rather than baked into the
# unit file, so the same overlay works for any port layout and the values stay
# editable on a running box.
install -d -m 0755 "$ROOT/etc/systemd/system/multi-user.target.wants"
install -D -m 0644 /dev/stdin "$ROOT/etc/default/infinite-streaming-boa" <<EOF
# infinite-streaming-boa daemon configuration. Changing a value here and restarting
# infinite-streaming-boa.service is enough; nothing needs rebuilding.
BOA_ADDR=:80
BOA_BRIDGE=br-lan
BOA_WAN_PORT=${BOA_WAN_PORT}
BOA_WLAN_PORT=wlan0
BOA_LAN_PORT=lan0
BOA_STATE=/var/lib/infinite-streaming-boa/policies.json
EOF

install -d -m 0755 "$ROOT/var/lib/infinite-streaming-boa"

if [ -x "$ROOT/usr/local/bin/boad" ]; then
  ln -sf /etc/systemd/system/infinite-streaming-boa.service \
    "$ROOT/etc/systemd/system/multi-user.target.wants/infinite-streaming-boa.service"
  log "boa daemon enabled ($(du -h "$ROOT/usr/local/bin/boad" | cut -f1))"
else
  warn "overlay/usr/local/bin/boad missing -- image will boot as a plain"
  warn "bridge with no conditioning. Run scripts/build-payload.sh first."
fi

# The iperf3 server is enabled only when the binary is actually in the image.
# The unit is in the overlay either way, and a unit whose ExecStart does not
# exist fails on every boot and every restart -- noise that says nothing, on a
# box where a failed unit is supposed to mean something.
if [ -x "$ROOT/usr/bin/iperf3" ]; then
  ln -sf /etc/systemd/system/iperf3.service \
    "$ROOT/etc/systemd/system/multi-user.target.wants/iperf3.service"
  log "iperf3 server enabled on :5201 (measures the UNSHAPED link)"
else
  warn "iperf3 not installed -- the box will not be able to measure its own link"
fi

## 8d. ntopng (optional, prebuilt) ------------------------------------------
# ntopng has to be compiled on arm64 -- ntop ships x86-64 binaries only, Docker
# Hub's image is amd64 only, and Debian dropped the package after buster. A
# 45-minute compile inside every image build would be intolerable, so the
# artifact is built once by scripts/package-ntopng.sh and simply unpacked here.
# An absent artifact is not an error: the image is just built without it.
if [ -f /cache/ntopng-arm64.tar.gz ]; then
  log "Grafting prebuilt ntopng into the image"
  tar xzf /cache/ntopng-arm64.tar.gz -C "$ROOT"

  # br-lan, not the WAN port: on a transparent bridge the bridge is what sees
  # every client in both directions.
  install -d -m 0755 "$ROOT/etc/ntopng" "$ROOT/var/lib/ntopng"
  cat > "$ROOT/etc/ntopng/ntopng.conf" <<EOF
# Managed by infinite-streaming-boa. ntopng serves :3000; boad owns :80.
-i=br-lan
-w=3000
-d=/var/lib/ntopng
# No login. ntopng logs a session out on inactivity, so a credential here meant
# retyping it on most visits to a dashboard that is opened for ten seconds at a
# time. It also guarded the wrong door: the interface on :80 has no
# authentication at all and can CHANGE a device's conditioning, while :3000 can
# only look. A password on the weaker surface bought nothing and cost a login
# every time. Use -l=0 instead to keep the login for everyone but localhost.
-l=1
EOF

  # Upstream's `make install` ships no systemd unit, so without this ntopng
  # would run until the next reboot and then quietly fail to come back.
  install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/ntopng.service" <<'UNIT'
[Unit]
Description=ntopng traffic analysis
Documentation=https://www.ntop.org/
# redis is a hard dependency: ntopng exits immediately without it.
After=network-online.target redis-server.service NetworkManager.service
Wants=network-online.target
Requires=redis-server.service

[Service]
Type=simple
ExecStart=/usr/local/bin/ntopng /etc/ntopng/ntopng.conf
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  ln -sf /etc/systemd/system/ntopng.service \
    "$ROOT/etc/systemd/system/multi-user.target.wants/ntopng.service"

  # Only the shared libraries, resolved from the binary that was actually built
  # so the list cannot drift from reality. The compile is the expensive part;
  # these are ordinary apt packages.
  if [ -f /cache/ntopng-runtime-deps.txt ]; then
    mapfile -t NTOPDEPS < <(grep -vE '^[[:space:]]*(#|$)' /cache/ntopng-runtime-deps.txt)
    if [ ${#NTOPDEPS[@]} -gt 0 ]; then
      log "Installing ntopng runtime libraries (${#NTOPDEPS[@]} packages)"
      # Refresh the index first. The package step above deletes
      # /var/lib/apt/lists/*, so without this apt works from the base image's
      # index -- months old by the time anyone builds -- and 404s on any
      # version the mirror has since superseded.
      # Tolerant of partial failure: apt exits non-zero when any index file is
      # missing, including the translation catalogues that are routinely absent
      # from a mirror. Under `set -e` that aborted the whole image build over a
      # file nothing needs. The package index is what matters, and the library
      # check below is the real verdict.
      chroot "$ROOT" env DEBIAN_FRONTEND=noninteractive apt-get -qq update \
        || warn "apt index refresh reported errors (usually absent translation files)"
      chroot "$ROOT" env DEBIAN_FRONTEND=noninteractive \
        apt-get -qq install -y --no-install-recommends redis-server "${NTOPDEPS[@]}" \
        || warn "some ntopng runtime packages could not be installed"

      # Verify rather than assume. A missing shared library does not surface
      # until the Pi boots and ntopng silently fails to start, which is a
      # miserable thing to discover from a flashed card.
      # `|| true` is load-bearing: grep exits non-zero when it finds nothing, so
      # under `set -e` this check aborted the build on the SUCCESS path -- the
      # case where every library resolved.
      MISSING_LIBS=$(chroot "$ROOT" ldd /usr/local/bin/ntopng 2>/dev/null \
        | grep "not found" | awk '{print $1}' | tr '\n' ' ' || true)
      if [ -n "$MISSING_LIBS" ]; then
        warn "ntopng is missing shared libraries and will NOT start: $MISSING_LIBS"
        warn "the access point and conditioner are unaffected"
      else
        log "ntopng library check: all shared libraries resolve"
      fi
      chroot "$ROOT" apt-get -qq clean
      rm -rf "$ROOT/var/lib/apt/lists/"*
    fi
  fi
  # ntopng keeps its admin password as an unsalted MD5 hex digest in redis
  # (mg_md5 + strcmp, see src/Ntop.cpp). Seeding it from BOA_PASSWORD means one
  # credential for the box instead of ntopng's admin/admin and a forced change
  # wizard on every reflash.
  #
  # INERT while ntopng.conf carries -l=1, which disables login outright. Kept so
  # that re-enabling the login stays a one-line edit rather than a rebuild of
  # this whole seeding path, and so the admin account is not left on ntopng's
  # default password if it is ever re-enabled.
  #
  # Only the digest goes into the image, never the plaintext. Note this is a
  # WEAKER store of the same secret than the system account, which uses a salted
  # SHA-512 crypt -- an unavoidable consequence of ntopng's scheme, not ours.
  # BOA_NTOPNG_PASSWORD exists so the login password does not have to be the one
  # stored this weakly. Falling back to BOA_PASSWORD keeps existing .env files
  # working, but say so: reusing it means the account secret also exists on the
  # box as an unsalted MD5, which is a strictly worse store than the SHA-512
  # crypt the system account uses.
  NTOP_PW="${BOA_NTOPNG_PASSWORD:-${BOA_PASSWORD}}"
  if [ -n "${BOA_NTOPNG_PASSWORD:-}" ]; then
    log "ntopng admin password set from BOA_NTOPNG_PASSWORD"
  else
    warn "ntopng admin password reuses BOA_PASSWORD, stored as unsalted MD5;"
    warn "set BOA_NTOPNG_PASSWORD in .env to keep the two secrets separate"
  fi
  NTOP_MD5=$(printf '%s' "${NTOP_PW}" | md5sum | cut -d' ' -f1)
  printf '%s\n' "$NTOP_MD5" > "$ROOT/etc/ntopng/admin.md5"
  chmod 0600 "$ROOT/etc/ntopng/admin.md5"

  install -D -m 0755 /dev/stdin "$ROOT/usr/local/sbin/infinite-streaming-boa-ntopng-passwd" <<'SEED'
#!/bin/sh
# Overwrites ntopng's admin password with the one configured for this box.
# Runs after ntopng has created its default admin user: seeding before that
# would simply be overwritten by ntopng's own initialisation.
[ -r /etc/ntopng/admin.md5 ] || exit 0
HASH=$(cat /etc/ntopng/admin.md5)
for i in $(seq 1 60); do
  redis-cli EXISTS ntopng.user.admin.password 2>/dev/null | grep -q '^1$' && break
  sleep 2
done
redis-cli SET ntopng.user.admin.password "$HASH" >/dev/null 2>&1 || exit 0
# Clear the "you are still on the default password" nag, which keys off the
# admin/admin digest rather than a separate flag.
redis-cli SET ntopng.prefs.admin_password_changed 1 >/dev/null 2>&1 || true
SEED

  install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-ntopng-passwd.service" <<'UNIT'
[Unit]
Description=Set ntopng admin password from boa configuration
After=ntopng.service redis-server.service
Requires=redis-server.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/infinite-streaming-boa-ntopng-passwd
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNIT
  ln -sf /etc/systemd/system/infinite-streaming-boa-ntopng-passwd.service \
    "$ROOT/etc/systemd/system/multi-user.target.wants/infinite-streaming-boa-ntopng-passwd.service"

  log "ntopng will serve on :3000"
else
  log "No prebuilt ntopng in cache/ -- image will not include it"
fi

## 9. First-boot service ----------------------------------------------------
install -D -m 0755 /dev/stdin "$ROOT/usr/local/sbin/infinite-streaming-boa-firstboot" <<EOF
#!/bin/sh
# Runs once on first boot, then disables itself.
set -e

# Sets the country in the Pi's own wireless config and clears the rfkill soft
# block that ships enabled on a fresh image.
raspi-config nonint do_wifi_country "${AP_COUNTRY}" || true
rfkill unblock wifi || true

# avahi publishes ${BOA_HOSTNAME}.local so the box is reachable by name.
systemctl enable --now avahi-daemon 2>/dev/null || true

systemctl disable infinite-streaming-boa-firstboot.service || true
EOF

install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-firstboot.service" <<'EOF'
[Unit]
Description=boa one-time first boot setup
After=multi-user.target NetworkManager.service
ConditionPathExists=/usr/local/sbin/infinite-streaming-boa-firstboot

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/infinite-streaming-boa-firstboot
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

install -d -m 0755 "$ROOT/etc/systemd/system/multi-user.target.wants"
ln -sf /etc/systemd/system/infinite-streaming-boa-firstboot.service \
  "$ROOT/etc/systemd/system/multi-user.target.wants/infinite-streaming-boa-firstboot.service"

## 10. Finish ---------------------------------------------------------------
sync
cleanup
trap - EXIT

log "Moving finished image to out/"
mv "$IMG" "/out/${OUT_NAME}"
cd /out && sha256sum "${OUT_NAME}" > "${OUT_NAME}.sha256"
log "Done: ${OUT_NAME} ($(du -h "/out/${OUT_NAME}" | cut -f1))"
