#!/usr/bin/env bash
#
# Build signed OpenWrt packages: boa (the daemon) and luci-app-boa (the LuCI
# page), for OpenWrt 25.12 on bcm27xx/bcm2712 (Raspberry Pi 5).
#
#   ./scripts/openwrt-package.sh              -> dist/openwrt/*.apk
#   ./scripts/openwrt-package.sh root@<host>  -> and install them there
#
# boad is cross-compiled here, as deploy.sh does, with the interface embedded;
# the OpenWrt SDK then only packages and signs. The SDK is x86-64 only, so on
# an arm64 host Docker runs it under emulation -- slower, but these packages
# compile nothing.
#
# The signing key lives in cache/openwrt-keys/ (gitignored) and is made once.
# A device trusts it after its public half is put in /etc/apk/keys/, which the
# install step does. Losing the key means re-trusting a new one, not a rebuild
# of anything else.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

SDK_IMAGE="${SDK_IMAGE:-openwrt/sdk:bcm27xx-bcm2712-25.12.5}"
TARGET="${1:-}"
KEYS=cache/openwrt-keys
BUILD=cache/openwrt-build
OUT=dist/openwrt

command -v docker >/dev/null || die "docker is required to run the OpenWrt SDK"

# apk wants a plain version, so the tag's numbers and a build stamp as the
# release: v0.4.0-3-gabc123-dirty becomes 0.4.0-r202609181830. The stamp is
# what lets a rebuild of the same tag install as an upgrade.
VER="$(bash scripts/version.sh)"
BOA_VERSION="$(printf '%s' "$VER" | sed -E 's/^v//; s/[^0-9.].*$//')"
[ -n "$BOA_VERSION" ] || BOA_VERSION=0.0.0
BOA_RELEASE="$(date -u +%Y%m%d%H%M)"

log "Building interface"
( cd ui && { [ -d node_modules ] || npm install --silent; } && npm run build --silent )

mkdir -p "$BUILD" "$OUT"
log "Building boad $VER for linux/arm64"
( cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o "../$BUILD/boad" . )

if [ ! -f "$KEYS/private-key.pem" ]; then
  log "Creating the package signing key in $KEYS"
  mkdir -p "$KEYS"
  ( umask 077; openssl ecparam -name prime256v1 -genkey -noout -out "$KEYS/private-key.pem" )
  openssl ec -in "$KEYS/private-key.pem" -pubout -out "$KEYS/public-key.pem" 2>/dev/null
fi

log "Packaging $BOA_VERSION-r$BOA_RELEASE in $SDK_IMAGE"
rm -f "$OUT"/*.apk "$OUT"/packages.adb
# As root, so the packaged files are owned by root on the device.
# openwrt/mkpkg.sh says why this is not `make package/boa/compile`.
docker run --rm --platform linux/amd64 --user 0 \
  -v "$PWD/openwrt:/src:ro" \
  -v "$PWD/$KEYS:/keys:ro" \
  -v "$PWD/$BUILD:/in:ro" \
  -v "$PWD/$OUT:/out" \
  "$SDK_IMAGE" bash /src/mkpkg.sh "$BOA_VERSION-r$BOA_RELEASE"
[ -f "$OUT/packages.adb" ] || die "the SDK produced no signed index"
log "Built $OUT/:"; ls -1 "$OUT" | sed 's/^/    /'

[ -n "$TARGET" ] || exit 0

log "Installing on $TARGET"
ssh -o BatchMode=yes "$TARGET" 'test -f /etc/openwrt_release' || die "cannot reach $TARGET, or it is not OpenWrt"
# Trusted by name: apk reads every key in /etc/apk/keys, and this one signs
# nothing but boa's index.
ssh "$TARGET" 'cat > /etc/apk/keys/boa-packages.pem' < "$KEYS/public-key.pem"
ssh "$TARGET" 'rm -rf /tmp/boa-repo && mkdir -p /tmp/boa-repo'
for f in "$OUT"/*.apk "$OUT"/packages.adb; do
  ssh "$TARGET" "cat > /tmp/boa-repo/$(basename "$f")" < "$f"
done
# From the signed index, so apk verifies the packages against the key above.
# Pinned to this build. A bare `apk add` leaves an installed package at its old
# version, and `apk add --upgrade` upgrades its dependencies too -- measured:
# a boa reinstall also moved rpcd, luci-base and ten other system packages.
# A version constraint moves only these two.
V="$BOA_VERSION-r$BOA_RELEASE"
ssh "$TARGET" "apk add --repository /tmp/boa-repo/packages.adb boa=$V luci-app-boa=$V"
log "Installed. LuCI: Services -> infinite-streaming-boa"
