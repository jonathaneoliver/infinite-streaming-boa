#!/usr/bin/env bash
#
# Install boa on an OpenWrt device from the published feed. Nothing is built
# and nothing is copied from this checkout: the device fetches the packages
# itself, so this needs no Docker, no SDK, and no signing key of your own.
#
#   ./scripts/openwrt-boa-feed.sh root@<host>
#   SSH_OPTS="-o ProxyJump=<jump>" ./scripts/openwrt-boa-feed.sh root@<host>
#
# This is the same four commands README.md documents for doing it by hand, with
# the parts that bite made safe: the architecture is read from the device rather
# than guessed, the feed line is added once rather than every run, and a device
# that cannot reach the feed says so instead of failing inside apk.
#
# WHICH SCRIPT TO USE. openwrt-boa-build.sh + openwrt-boa-install.sh put THIS
# checkout on a device, which is what testing a change needs. This one installs
# the last tagged release, which is what a user wants. They are not
# interchangeable: the feed publishes on `v*` tags, so a fix merged an hour ago
# is not in it.
#
# THE KEY IS THE WHOLE REASON THIS CANNOT BE DONE FROM LuCI. The Software page
# can add a repository and install a package, but it has no interface for
# /etc/apk/keys/ and no --allow-untrusted, so apk rejects the signed index and
# the page simply fails. Until boa is in the official feed (#359) this step
# needs a shell, which is what this script is.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

TARGET=""
CONFIGURE_WAN=1
HOLD_WIZARD=1
for a in "$@"; do
  case "$a" in
    --no-configure-wan) CONFIGURE_WAN=0 ;;
    --unattended) HOLD_WIZARD=0 ;;
    -*) die "unknown option $a" ;;
    *)  TARGET="$a" ;;
  esac
done
[ -n "$TARGET" ] || die "usage: $0 [--no-configure-wan] [--unattended] root@<host>"

BASE="${BOA_FEED_BASE:-https://jonathaneoliver.github.io/infinite-streaming-boa}"
RELEASE="${OPENWRT_RELEASE:-25.12}"
KEY_URL="$BASE/openwrt/boa-packages.pem"

SSH_OPTS="${SSH_OPTS:-}"
# shellcheck disable=SC2086
ssh() { command ssh $SSH_OPTS "$@"; }
box() { ssh "$TARGET" "$@"; }
# shellcheck source=scripts/openwrt-boa-common.sh
. "$(dirname "${BASH_SOURCE[0]}")/openwrt-boa-common.sh"

ssh -o BatchMode=yes "$TARGET" 'test -f /etc/openwrt_release' ||
  die "cannot reach $TARGET, or it is not OpenWrt"

# READ THE ARCHITECTURE, DO NOT GUESS IT. Every target this repo runs on is a
# different one -- aarch64_cortex-a76 on a Pi 5, aarch64_cortex-a53 on a Cudy,
# x86_64 in a VM -- and the feed publishes a directory per architecture.
ARCH="$(ssh "$TARGET" '. /etc/openwrt_release; printf %s "$DISTRIB_ARCH"')"
[ -n "$ARCH" ] || die "$TARGET did not report a DISTRIB_ARCH"
FEED="$BASE/openwrt/$RELEASE/$ARCH/packages.adb"
log "$TARGET is $ARCH; feed is $FEED"

# The device fetches the key itself when it can. uclient-fetch needs a TLS
# backend for an https URL and not every image carries one, so a failure here
# is ordinary rather than fatal: send the key over the SSH connection instead,
# which is already trusted and already open.
log "installing the signing key"
ssh "$TARGET" "wget -q -O /etc/apk/keys/boa-packages.pem '$KEY_URL'" 2>/dev/null || {
  log "  the device could not fetch it; sending the key from here"
  curl -fsS "$KEY_URL" | ssh "$TARGET" 'cat > /etc/apk/keys/boa-packages.pem' ||
    die "could not install the key from $KEY_URL"
}

# ONCE, NOT ONCE PER RUN. Appending unconditionally leaves a file with the same
# feed in it n times after n runs; apk tolerates that and it still reads wrong.
log "pointing apk at the feed"
ssh "$TARGET" "mkdir -p /etc/apk/repositories.d
  grep -qxF '$FEED' /etc/apk/repositories.d/customfeeds.list 2>/dev/null ||
    printf '%s\n' '$FEED' >> /etc/apk/repositories.d/customfeeds.list"

# EVERYTHING here comes over the device's own uplink -- this script copies no
# packages -- so a Raspberry Pi fresh from a flash cannot use it at all until
# it has a WAN, which OpenWrt's board profile never gives it.
log "updating the package index"
if ! ssh "$TARGET" 'apk update' >/dev/null 2>&1; then
  if [ "$CONFIGURE_WAN" = 0 ]; then
    die "$TARGET cannot reach the feed, and --no-configure-wan was given"
  elif ssh "$TARGET" 'uci -q get network.wan >/dev/null 2>&1'; then
    die "$TARGET has a wan configured but cannot reach the feed; fix its uplink first"
  fi
  log "no wan on this device and no route to the feed -- finding one"
  WAN_IF="$(boa_configure_wan)" || die "could not give $TARGET a working uplink"
  log "  configured wan on $WAN_IF (USB, live cable); boa-setup convert removes it later"
  ssh "$TARGET" 'apk update' >/dev/null 2>&1 ||
    die "$TARGET still cannot reach $FEED after configuring wan on $WAN_IF"
fi

log "installing luci-app-boa (which brings boa with it)"
ssh "$TARGET" 'apk add luci-app-boa'
log "Installed from the feed. Open http://<device>/ -- an unconfigured box"
log "shows the setup wizard there; otherwise LuCI -> Services -> boa setup."

# HOLD THE WIZARD OPEN. Installing arms an unattended first run that brings the
# box up by itself after about two minutes, with a generated SSID and no root
# password -- measured on a Pi 5 2026-09-28, which had done exactly that two
# minutes after a reset. That countdown is for a box nobody is standing at.
# Somebody ran this script, so somebody is. --unattended leaves it armed.
if [ "$HOLD_WIZARD" = 1 ]; then
  if boa_hold_wizard; then
    log "the unattended countdown is off; the wizard waits for you"
  else
    log "WARNING: could not stop the unattended countdown. This box will set"
    log "         itself up in about two minutes, with a generated SSID and"
    log "         no root password, unless you open the wizard before then."
  fi
fi
