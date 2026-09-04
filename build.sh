#!/usr/bin/env bash
#
# boa image builder.
#
# Produces a ready-to-burn Raspberry Pi OS image with the access point and the
# boa link conditioner baked in. Nothing is fetched on first boot.
#
# The host needs only curl + docker: macOS can neither mount ext4 nor
# loop-mount a partition table, so all image surgery happens in a privileged
# arm64 Linux container (see scripts/customize.sh).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m warn:\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

ENV_FILE="${ENV_FILE:-.env}"
[ -f "$ENV_FILE" ] || die "no $ENV_FILE — copy .env.example to .env and edit it"

set -a; . "./$ENV_FILE"; set +a

## Validation ---------------------------------------------------------------
# Every one of these failure modes produces a Pi that boots but has no working
# access point, which is slow and annoying to debug on a headless box. Cheaper
# to catch them here.
: "${AP_SSID:?AP_SSID must be set in $ENV_FILE}"
: "${AP_PASSWORD:?AP_PASSWORD must be set in $ENV_FILE}"
AP_COUNTRY="${AP_COUNTRY:-US}"
AP_BAND="${AP_BAND:-bg}"
AP_CHANNEL="${AP_CHANNEL:-6}"
# The 2.4GHz channel the ONBOARD radio takes when it serves alongside a USB
# adapter. Only used in that dual-band case; on its own the onboard radio
# honours AP_BAND/AP_CHANNEL like anything else.
AP_CHANNEL_24="${AP_CHANNEL_24:-6}"
BOA_WAN_PORT="${BOA_WAN_PORT:-eth0}"
BOA_RESCUE_IP="${BOA_RESCUE_IP:-192.168.99.1}"
AP_HIDDEN="${AP_HIDDEN:-false}"
AP_SSID_USB="${AP_SSID_USB:-}"
BOA_HOSTNAME="${BOA_HOSTNAME:-boa}"
BOA_USER="${BOA_USER:-boa}"
BOA_PASSWORD="${BOA_PASSWORD:-}"
BOA_SSH_PUBKEY="${BOA_SSH_PUBKEY:-}"
BOA_NTOPNG_PASSWORD="${BOA_NTOPNG_PASSWORD:-}"
BOA_SSH_PASSWORD_LOGIN="${BOA_SSH_PASSWORD_LOGIN:-false}"
BOA_TIMEZONE="${BOA_TIMEZONE:-}"

(( ${#AP_SSID} >= 1 && ${#AP_SSID} <= 32 )) \
  || die "AP_SSID must be 1-32 characters (got ${#AP_SSID})"
(( ${#AP_PASSWORD} >= 8 && ${#AP_PASSWORD} <= 63 )) \
  || die "AP_PASSWORD must be 8-63 characters (got ${#AP_PASSWORD}) — WPA2 requires it"
[[ "$AP_COUNTRY" =~ ^[A-Z]{2}$ ]] \
  || die "AP_COUNTRY must be a 2-letter ISO country code like US or GB (got '$AP_COUNTRY')"
# Only checked when set; empty means the USB radio publishes AP_SSID too.
[ -z "$AP_SSID_USB" ] || (( ${#AP_SSID_USB} >= 1 && ${#AP_SSID_USB} <= 32 )) \
  || die "AP_SSID_USB must be 1-32 characters (got ${#AP_SSID_USB})"
[[ "$AP_BAND" == "bg" || "$AP_BAND" == "a" ]] \
  || die "AP_BAND must be 'bg' (2.4GHz) or 'a' (5GHz) (got '$AP_BAND')"
[[ "$BOA_RESCUE_IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] \
  || die "BOA_RESCUE_IP must be a plain IPv4 address (got '$BOA_RESCUE_IP')"
[[ "$BOA_WAN_PORT" =~ ^[a-zA-Z0-9_-]+$ ]] \
  || die "BOA_WAN_PORT must be an interface name like eth0 (got '$BOA_WAN_PORT')"
[ -n "$BOA_PASSWORD" ] || [ -n "$BOA_SSH_PUBKEY" ] \
  || die "set BOA_PASSWORD or BOA_SSH_PUBKEY, or you cannot log in to the Pi"
[[ "$BOA_SSH_PASSWORD_LOGIN" == "true" || "$BOA_SSH_PASSWORD_LOGIN" == "false" ]] \
  || die "BOA_SSH_PASSWORD_LOGIN must be 'true' or 'false' (got '$BOA_SSH_PASSWORD_LOGIN')"
# Guard the combination that locks you out: no password to type, and password
# login turned on as the way in. sshd would offer a method that cannot succeed.
[ "$BOA_SSH_PASSWORD_LOGIN" = "true" ] && [ -z "$BOA_PASSWORD" ] \
  && die "BOA_SSH_PASSWORD_LOGIN=true needs BOA_PASSWORD set; an empty password
  locks the account with '*' and password login could never succeed"

# A key is what makes the box's passwordless sudo rule reasonable, so the
# combination with no key gets called out rather than built quietly. Without one,
# sshd accepts BOA_PASSWORD over the network from anyone associated to the AP,
# and that password is the whole of the box's security.
if [ -z "$BOA_SSH_PUBKEY" ]; then
  warn "BOA_SSH_PUBKEY is not set: the Pi will accept SSH password logins."
  warn "  Recommended: ssh-keygen if needed, then put the PUBLIC key in .env as"
  warn "  BOA_SSH_PUBKEY. The image then disables password login over the network"
  warn "  and keeps BOA_PASSWORD for the console and for recovery."
else
  # BOA_SSH_PUBKEY may hold several keys, one per line, and each is PARSED here
  # rather than pattern-matched. A regex only checks shape, and a key truncated
  # by a bad paste -- "ssh-ed25519 AAAAC3Nz" -- keeps the shape perfectly while
  # being useless. sshd then ignores that line WITHOUT COMPLAINING, which on a
  # headless box with no password login is a box nobody can reach. ssh-keygen
  # decodes the blob and fails on it.
  KEYTMP=$(mktemp); trap 'rm -f "$KEYTMP"' EXIT
  KEYCOUNT=0
  while IFS= read -r k; do
    k="${k%$'\r'}"
    k="${k#"${k%%[![:space:]]*}"}"
    [ -z "$k" ] && continue
    case "$k" in \#*) continue ;; esac
    KEYCOUNT=$((KEYCOUNT + 1))
    printf '%s\n' "$k" > "$KEYTMP"
    FPR=$(ssh-keygen -lf "$KEYTMP" 2>/dev/null) \
      || die "BOA_SSH_PUBKEY line $KEYCOUNT is not a usable public key.
  ssh-keygen could not parse it, which usually means a truncated or wrapped
  paste. Expected one line, e.g. the contents of ~/.ssh/id_ed25519.pub.
  A PRIVATE key ('-----BEGIN OPENSSH PRIVATE KEY-----') is the wrong file and
  must never go in .env."
    log "  key $KEYCOUNT: $FPR"
  done <<< "$BOA_SSH_PUBKEY"
  rm -f "$KEYTMP"; trap - EXIT
  [ "$KEYCOUNT" -gt 0 ] \
    || die "BOA_SSH_PUBKEY is set but holds no key lines"
  if [ "$BOA_SSH_PASSWORD_LOGIN" = "true" ]; then
    log "SSH: $KEYCOUNT key(s) authorised"
    warn "BOA_SSH_PASSWORD_LOGIN=true: sshd will ALSO accept BOA_PASSWORD over the
  network, from anything associated to the AP. The key is the stronger
  credential; set this back to false once you no longer need the fallback."
  else
    log "SSH: $KEYCOUNT key(s) authorised; password login will be disabled"
  fi
  [ "$KEYCOUNT" -eq 1 ] \
    && warn "Only one SSH key. Losing it means the console or a reflash — add a
  spare on a second line of BOA_SSH_PUBKEY if you have another machine."
fi

# The Pi's onboard radio cannot run an AP on a DFS channel (radar-avoidance
# channels require a detection capability the chip does not expose in AP mode).
#
# The non-DFS 5GHz channels fall in two blocks either side of the DFS range:
# UNII-1's 36/40/44/48 and UNII-3's 149/153/157/161/165. Both are listed by
# both radios under US: DFS-FCC with neither "radar detection" nor "no IR" --
# verified on the box 2026-09-03. Keep in step with apChannels in radioctl.go.
if [ "$AP_BAND" = "a" ] \
  && [[ ! " 36 40 44 48 149 153 157 161 165 0 " =~ " $AP_CHANNEL " ]]; then
  die "AP_CHANNEL $AP_CHANNEL is DFS or invalid for 5GHz AP mode; use 36, 40, 44, 48, 149, 153, 157, 161, 165, or 0 for auto"
fi
if [ "$AP_BAND" = "bg" ] && [[ ! " 1 2 3 4 5 6 7 8 9 10 11 0 " =~ " $AP_CHANNEL " ]]; then
  die "AP_CHANNEL $AP_CHANNEL is not a valid 2.4GHz channel; use 1, 6, 11, or 0 for auto"
fi
# The onboard radio has no survey, so ACS is impossible there and 0 is not an
# option: it would silently become a fixed channel anyway. Refuse it here
# rather than let a build produce an AP on a channel nobody chose.
if [[ ! " 1 2 3 4 5 6 7 8 9 10 11 " =~ " $AP_CHANNEL_24 " ]]; then
  die "AP_CHANNEL_24 $AP_CHANNEL_24 is not a valid 2.4GHz channel; use 1, 6 or 11 (0/auto is not available on the onboard radio)"
fi

## Source image -------------------------------------------------------------
mkdir -p cache work dist overlay
LATEST="https://downloads.raspberrypi.com/raspios_lite_arm64_latest"

if [ -z "${RPIOS_URL:-}" ]; then
  log "Resolving latest Raspberry Pi OS Lite (arm64)"
  RPIOS_URL=$(curl -sIL -o /dev/null -w '%{url_effective}' "$LATEST") \
    || die "could not reach downloads.raspberrypi.com"
fi
XZ_NAME="$(basename "$RPIOS_URL")"
[ -n "$XZ_NAME" ] || die "could not determine image filename from $RPIOS_URL"
log "Base image: $XZ_NAME"

# -C - resumes a partial download rather than restarting a 500MB transfer.
if [ ! -f "cache/$XZ_NAME" ]; then
  log "Downloading (~500MB, cached in cache/ for future builds)"
  curl -fL -# -C - -o "cache/$XZ_NAME.part" "$RPIOS_URL" \
    || die "download failed"
  mv "cache/$XZ_NAME.part" "cache/$XZ_NAME"
else
  log "Using cached download"
fi

if [ ! -f "cache/$XZ_NAME.sha256" ]; then
  curl -fsL -o "cache/$XZ_NAME.sha256" "$RPIOS_URL.sha256" 2>/dev/null \
    || log "No published checksum for this image; continuing without verification"
fi

## Payload ------------------------------------------------------------------
if [ -x ./scripts/build-payload.sh ]; then
  log "Building boa payload (Vue UI + Go daemon)"
  ./scripts/build-payload.sh
fi

## Build --------------------------------------------------------------------
STAMP=$(date +%Y%m%d)
OUT_NAME="infinite-streaming-boa-${STAMP}.img"

log "Preparing builder container"
docker build -q -t boa-builder . >/dev/null

log "Customizing image"
docker run --rm --privileged \
  -v /dev:/dev \
  -v "$PWD/cache:/cache" \
  -v "$PWD/work:/work" \
  -v "$PWD/dist:/out" \
  -v "$PWD/overlay:/overlay:ro" \
  -v "$PWD/packages.txt:/packages.txt:ro" \
  -e XZ_NAME="$XZ_NAME" \
  -e OUT_NAME="$OUT_NAME" \
  -e AP_SSID -e AP_SSID_USB -e AP_PASSWORD -e AP_COUNTRY -e AP_BAND -e AP_CHANNEL \
  -e AP_CHANNEL_24 \
  -e AP_HIDDEN -e BOA_WAN_PORT -e BOA_RESCUE_IP \
  -e BOA_HOSTNAME -e BOA_USER -e BOA_PASSWORD -e BOA_SSH_PUBKEY \
  -e BOA_NTOPNG_PASSWORD -e BOA_SSH_PASSWORD_LOGIN \
  -e BOA_TIMEZONE \
  boa-builder

BAND_LABEL="2.4GHz"
[ "$AP_BAND" = "a" ] && BAND_LABEL="5GHz"

# What the radios will actually do, which depends on what is plugged in at boot
# rather than on anything decided here -- so the summary describes both cases
# instead of asserting one. It used to name a single "wlan0", which was true
# before the box could serve two radios and quietly wrong afterwards.
#
# The USB adapter's SSID may differ (AP_SSID_USB), and that is worth saying:
# two SSIDs is two networks, so a client will not roam between them.
if [ -n "$AP_SSID_USB" ] && [ "$AP_SSID_USB" != "$AP_SSID" ]; then
  USB_SSID_NOTE="  (SSID '${AP_SSID_USB}' — a separate network; no roaming)"
else
  USB_SSID_NOTE=""
fi

cat <<EOF

  Image:    dist/${OUT_NAME}
  SSID:     ${AP_SSID}   (country ${AP_COUNTRY})
  Radios:   both serve when both are present, dual-band like a router:
              USB adapter  ${BAND_LABEL} ch ${AP_CHANNEL}  (80MHz, 802.11ax)${USB_SSID_NOTE}
              onboard      2.4GHz ch ${AP_CHANNEL_24}  (20MHz, 802.11n)
            Either alone serves alone; on its own the onboard radio takes
            ${BAND_LABEL} ch ${AP_CHANNEL} instead.
  Login:    ssh ${BOA_USER}@${BOA_HOSTNAME}.local
  Mode:     transparent bridge (${BOA_WAN_PORT} + wlan-usb + wlan0 + lan0 -> br-lan)
            clients keep their existing LAN addresses; the Pi is not a hop
            clients on EITHER radio are conditioned
  Web UI:   http://${BOA_HOSTNAME}.local/   or  http://${BOA_RESCUE_IP}/ (rescue)

  Burn it:  ./flash.sh dist/${OUT_NAME}

EOF
