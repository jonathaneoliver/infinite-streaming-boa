#!/usr/bin/env bash
#
# pifi image builder.
#
# Produces a ready-to-burn Raspberry Pi OS image with the access point and the
# pifi link conditioner baked in. Nothing is fetched on first boot.
#
# The host needs only curl + docker: macOS can neither mount ext4 nor
# loop-mount a partition table, so all image surgery happens in a privileged
# arm64 Linux container (see scripts/customize.sh).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
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
PIFI_WAN_PORT="${PIFI_WAN_PORT:-eth0}"
PIFI_RESCUE_IP="${PIFI_RESCUE_IP:-192.168.99.1}"
AP_HIDDEN="${AP_HIDDEN:-false}"
PIFI_HOSTNAME="${PIFI_HOSTNAME:-pifi}"
PIFI_USER="${PIFI_USER:-pifi}"
PIFI_PASSWORD="${PIFI_PASSWORD:-}"
PIFI_SSH_PUBKEY="${PIFI_SSH_PUBKEY:-}"
PIFI_TIMEZONE="${PIFI_TIMEZONE:-}"

(( ${#AP_SSID} >= 1 && ${#AP_SSID} <= 32 )) \
  || die "AP_SSID must be 1-32 characters (got ${#AP_SSID})"
(( ${#AP_PASSWORD} >= 8 && ${#AP_PASSWORD} <= 63 )) \
  || die "AP_PASSWORD must be 8-63 characters (got ${#AP_PASSWORD}) — WPA2 requires it"
[[ "$AP_COUNTRY" =~ ^[A-Z]{2}$ ]] \
  || die "AP_COUNTRY must be a 2-letter ISO country code like US or GB (got '$AP_COUNTRY')"
[[ "$AP_BAND" == "bg" || "$AP_BAND" == "a" ]] \
  || die "AP_BAND must be 'bg' (2.4GHz) or 'a' (5GHz) (got '$AP_BAND')"
[[ "$PIFI_RESCUE_IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] \
  || die "PIFI_RESCUE_IP must be a plain IPv4 address (got '$PIFI_RESCUE_IP')"
[[ "$PIFI_WAN_PORT" =~ ^[a-zA-Z0-9_-]+$ ]] \
  || die "PIFI_WAN_PORT must be an interface name like eth0 (got '$PIFI_WAN_PORT')"
[ -n "$PIFI_PASSWORD" ] || [ -n "$PIFI_SSH_PUBKEY" ] \
  || die "set PIFI_PASSWORD or PIFI_SSH_PUBKEY, or you cannot log in to the Pi"

# The Pi's onboard radio cannot run an AP on a DFS channel (radar-avoidance
# channels require a detection capability the chip does not expose in AP mode).
if [ "$AP_BAND" = "a" ] && [[ ! " 36 40 44 48 0 " =~ " $AP_CHANNEL " ]]; then
  die "AP_CHANNEL $AP_CHANNEL is DFS or invalid for 5GHz AP mode; use 36, 40, 44, 48, or 0 for auto"
fi
if [ "$AP_BAND" = "bg" ] && [[ ! " 1 2 3 4 5 6 7 8 9 10 11 0 " =~ " $AP_CHANNEL " ]]; then
  die "AP_CHANNEL $AP_CHANNEL is not a valid 2.4GHz channel; use 1, 6, 11, or 0 for auto"
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
  log "Building pifi payload (Vue UI + Go daemon)"
  ./scripts/build-payload.sh
fi

## Build --------------------------------------------------------------------
STAMP=$(date +%Y%m%d)
OUT_NAME="pifi-${STAMP}.img"

log "Preparing builder container"
docker build -q -t pifi-builder . >/dev/null

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
  -e AP_SSID -e AP_PASSWORD -e AP_COUNTRY -e AP_BAND -e AP_CHANNEL \
  -e AP_HIDDEN -e PIFI_WAN_PORT -e PIFI_RESCUE_IP \
  -e PIFI_HOSTNAME -e PIFI_USER -e PIFI_PASSWORD -e PIFI_SSH_PUBKEY \
  -e PIFI_TIMEZONE \
  pifi-builder

BAND_LABEL="2.4GHz"
[ "$AP_BAND" = "a" ] && BAND_LABEL="5GHz"

cat <<EOF

  Image:    dist/${OUT_NAME}
  SSID:     ${AP_SSID}  (${BAND_LABEL} ch ${AP_CHANNEL}, country ${AP_COUNTRY})
  Login:    ssh ${PIFI_USER}@${PIFI_HOSTNAME}.local
  Mode:     transparent bridge (${PIFI_WAN_PORT} + wlan0 + lan0 -> br-lan)
            clients keep their existing LAN addresses; the Pi is not a hop
  Web UI:   http://${PIFI_HOSTNAME}.local/   or  http://${PIFI_RESCUE_IP}/ (rescue)

  Burn it:  ./flash.sh dist/${OUT_NAME}

EOF
