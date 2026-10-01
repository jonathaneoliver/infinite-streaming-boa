#!/usr/bin/env bash
#
# Install already-built boa packages on an OpenWrt device. It builds nothing:
# openwrt-boa-build.sh does that, and leaves its output in dist/openwrt/.
#
#   ./scripts/openwrt-boa-install.sh root@<host>
#   SSH_OPTS="-o ProxyJump=<jump>" ./scripts/openwrt-boa-install.sh root@<host>
#
# THE VERSION IS READ, NEVER RECOMPUTED. `apk add` is pinned to an exact
# version, and that version is minted by the build from a clock
# (BOA_RELEASE=$(date -u +%Y%m%d%H%M)). Reading the clock again here would pin
# to a version that was never built, so it comes off the filenames the build
# left behind instead -- which is also why this script cannot be run before one.
#
# NEWEST WINS. Normally the build clears dist/openwrt/ first and there is only
# one version there, but nothing guarantees it: files can be copied in, or kept
# from an earlier build. `sort -V` orders by version rather than lexically, so
# 0.10.0 sorts above 0.9.0 -- plain sort puts 0.10.0 FIRST, which would install
# the oldest and look like a build that had not taken.
#
# ARCHITECTURE IS apk's JOB. It refuses a package whose arch does not match the
# system, so a stale package built from another SDK fails loudly here rather
# than being caught by a check of our own. The one failure that did get through
# -- a package LABELLED x86_64 carrying an arm64 boad, #365 -- was a build-time
# bug, and lives in the build script's GOARCH case.
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
[ -n "$TARGET" ] || die "usage: $0 [--no-configure-wan] [--unattended] root@<host>   (build first with scripts/openwrt-boa-build.sh)"

KEYS=cache/openwrt-keys
OUT=dist/openwrt

[ -d "$OUT" ] || die "no $OUT/ -- run scripts/openwrt-boa-build.sh first"
V="$(ls "$OUT"/boa-*.apk 2>/dev/null | sed -E 's|.*/boa-(.+)\.apk$|\1|' | sort -V | tail -1)"
[ -n "$V" ] || die "no packages in $OUT/ -- run scripts/openwrt-boa-build.sh first"

# Both halves, at the SAME version. A luci-app-boa left over from an earlier
# build would install beside a newer boa and disagree about the wire format.
for f in "$OUT/boa-$V.apk" "$OUT/luci-app-boa-$V.apk" "$OUT/packages.adb"; do
  [ -f "$f" ] || die "$f is missing -- rebuild with scripts/openwrt-boa-build.sh"
done
[ -f "$KEYS/public-key.pem" ] ||
  die "no signing key in $KEYS -- the device cannot verify the index without it"

log "Installing $V on $TARGET"
# SSH_OPTS reaches a device that is only reachable through another host, such
# as the OpenWrt VM behind its host's br-client (scripts/target.sh). Word-split
# on purpose: it is a list of options.
SSH_OPTS="${SSH_OPTS:-}"
# shellcheck disable=SC2086
ssh() { command ssh $SSH_OPTS "$@"; }
ssh -o BatchMode=yes "$TARGET" 'test -f /etc/openwrt_release' || die "cannot reach $TARGET, or it is not OpenWrt"

# THE TWO PACKAGES TRAVEL OVER SSH; THEIR 15 DEPENDENCIES DO NOT. tc-full,
# kmod-netem, kmod-ifb, kmod-sched-core and the rest come from the device's own
# feeds, so an install fails on a box with no route out -- which on a Raspberry
# Pi is every box fresh from a flash, because OpenWrt's board profile gives it
# none. Fix it rather than explain it: this only ever runs on a device that has
# NO wan at all and cannot reach a feed, where the alternative is the install
# failing a minute later inside apk.
box() { ssh "$TARGET" "$@"; }
# shellcheck source=scripts/openwrt-boa-common.sh
. "$(dirname "${BASH_SOURCE[0]}")/openwrt-boa-common.sh"
if ! boa_has_feed_access; then
  if [ "$CONFIGURE_WAN" = 0 ]; then
    die "$TARGET cannot reach a package feed, and --no-configure-wan was given"
  elif box 'uci -q get network.wan >/dev/null 2>&1'; then
    # It HAS a wan and still cannot reach a feed. That is somebody's
    # configuration being wrong, not a missing one, and guessing over it would
    # replace a deliberate setting.
    die "$TARGET has a wan configured but cannot reach a package feed; fix its uplink first"
  else
    log "no wan on this device and no route to a feed -- finding one"
    WAN_IF="$(boa_configure_wan)" || die "could not give $TARGET a working uplink"
    log "  configured wan on $WAN_IF (USB, live cable); boa-setup convert removes it later"
  fi
fi

# Trusted by name: apk reads every key in /etc/apk/keys, and this one signs
# nothing but boa's index.
ssh "$TARGET" 'cat > /etc/apk/keys/boa-packages.pem' < "$KEYS/public-key.pem"
ssh "$TARGET" 'rm -rf /tmp/boa-repo && mkdir -p /tmp/boa-repo'
# Only the chosen version travels. The old loop shipped every .apk in the
# directory, which with two versions present put both on the device and left
# apk to pick.
for f in "$OUT/boa-$V.apk" "$OUT/luci-app-boa-$V.apk" "$OUT/packages.adb"; do
  ssh "$TARGET" "cat > /tmp/boa-repo/$(basename "$f")" < "$f"
done
# From the signed index, so apk verifies the packages against the key above.
# Pinned to this build. A bare `apk add` leaves an installed package at its old
# version, and `apk add --upgrade` upgrades its dependencies too -- measured:
# a boa reinstall also moved rpcd, luci-base and ten other system packages.
# A version constraint moves only these two. apk then records that constraint
# in /etc/apk/world, where it pins the packages for good: measured, a later
# `apk upgrade` from the feed did nothing. Adding them again unversioned only
# rewrites world -- it upgrades nothing -- so the feed can move them on.
ssh "$TARGET" "apk add --repository /tmp/boa-repo/packages.adb boa=$V luci-app-boa=$V && apk add boa luci-app-boa >/dev/null"

# RESTART, BECAUSE apk WILL NOT. A first install runs the package's
# post-install, which starts the service; an UPGRADE replaces the files and
# leaves the running daemon alone. So the binary on disk is the new one, the
# version it prints is the new one, and the process answering requests is the
# old one -- which cost an hour on 2026-09-29 debugging a fix that was
# deployed and not running, with md5sum and --version both agreeing it was
# there. Nothing about the box says otherwise; only the absence of a restart
# line in the log does.
ssh "$TARGET" '/etc/init.d/boa enabled && /etc/init.d/boa restart >/dev/null 2>&1' || true
log "Installed and restarted. LuCI: Services -> infinite-streaming-boa"

# HOLD THE WIZARD OPEN. Installing arms an unattended first run that brings the
# box up by itself after about two minutes, with a generated SSID and no root
# password -- measured on a Pi 5 2026-09-28, which had done exactly that two
# minutes after a reset. That countdown is for a box nobody is standing at.
# Somebody ran this script, so somebody is. --unattended leaves it armed.
if [ "$HOLD_WIZARD" = 1 ]; then
  if held=$(boa_hold_wizard); then
    if [ "$held" = set-up ]; then
      log "already set up: http://<box>/ stays on LuCI"
    else
      log "the unattended countdown is off; the wizard waits for you"
    fi
  else
    log "WARNING: could not stop the unattended countdown. This box will set"
    log "         itself up in about two minutes, with a generated SSID and"
    log "         no root password, unless you open the wizard before then."
  fi
fi
