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
# A missing upstream DHCP server must never block the bridge from coming up:
# the data path has to work even when management addressing does not.
may-fail=true

[ipv6]
method=auto
may-fail=true
EOF

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

# Wireless AP, also enslaved to the bridge. An AP-mode interface CAN be bridged
# (a station-mode one cannot: a 3-address 802.11 header has nowhere to carry the
# original sender, which is why repeaters need 4-address/WDS mode).
{
  cat <<EOF
[connection]
id=infinite-streaming-boa-ap
uuid=$(uuidgen)
type=wifi
interface-name=wlan0
master=br-lan
slave-type=bridge
autoconnect=true
autoconnect-priority=100

[wifi]
mode=ap
ssid=${AP_SSID}
band=${AP_BAND}
hidden=${AP_HIDDEN}
EOF
  # channel=0 means "let the driver choose"; NM rejects a literal 0.
  [ "${AP_CHANNEL}" != "0" ] && echo "channel=${AP_CHANNEL}"
  cat <<EOF

[wifi-security]
key-mgmt=wpa-psk
proto=rsn
pairwise=ccmp
group=ccmp
psk=${AP_PASSWORD}
EOF
} | write_conn infinite-streaming-boa-ap

log "Bridge br-lan: ${BOA_WAN_PORT} (wan) + wlan0 (ap '${AP_SSID}') + lan0 (usb)"

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
install -D -m 0644 /dev/stdin "$ROOT/etc/systemd/system/infinite-streaming-boa-rescue-ip.service" <<EOF
[Unit]
Description=boa fixed rescue address on br-lan
After=NetworkManager.service
Wants=NetworkManager.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/infinite-streaming-boa-rescue-ip

[Install]
WantedBy=multi-user.target
EOF

install -D -m 0755 /dev/stdin "$ROOT/usr/local/sbin/infinite-streaming-boa-rescue-ip" <<EOF
#!/bin/sh
# Waits for the bridge, then pins a known address so the box is always
# reachable. "replace" rather than "add" so a re-run is idempotent.
for i in \$(seq 1 30); do
  ip link show br-lan >/dev/null 2>&1 && break
  sleep 2
done
exec ip addr replace ${BOA_RESCUE_IP}/24 dev br-lan
EOF

install -d -m 0755 "$ROOT/etc/systemd/system/multi-user.target.wants"
ln -sf /etc/systemd/system/infinite-streaming-boa-rescue-ip.service \
  "$ROOT/etc/systemd/system/multi-user.target.wants/infinite-streaming-boa-rescue-ip.service"
log "Rescue address ${BOA_RESCUE_IP}/24 on br-lan"

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
