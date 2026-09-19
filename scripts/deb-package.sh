#!/usr/bin/env bash
#
# Build the infinite-streaming-boa .deb for arm64 (Raspberry Pi OS, 64-bit)
# and amd64, and a signed apt repository holding both.
#
#   ./scripts/deb-package.sh        -> dist/deb/*.deb and dist/apt/
#
# boad is cross-compiled here with the interface embedded, as deploy.sh does;
# a debian:bookworm-slim container then builds the packages and the repository
# (deb/mkrepo.sh). The package installs the daemon and its service only -- no
# bridge, no hostapd configs -- see deb/DEBIAN/control.in.
#
# The archive signing key lives in cache/apt-keys/ (gitignored) and is made
# once. Clients trust it through the keyring published beside the repository.
# CI installs it from the BOA_APT_GPG_PRIVATE_KEY secret and never makes one.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

IMAGE="${DEB_IMAGE:-debian:bookworm-slim}"
KEYS=cache/apt-keys
BUILD=cache/deb-build

command -v docker >/dev/null || die "docker is required"

# A Debian version that sorts after every earlier build of the same tag:
# v0.4.0-3-gabc123-dirty becomes 0.4.0+202609190300.
VER="$(bash scripts/version.sh)"
BOA_VERSION="$(printf '%s' "$VER" | sed -E 's/^v//; s/[^0-9.].*$//')"
DEB_VERSION="${BOA_VERSION:-0.0.0}+$(date -u +%Y%m%d%H%M)"

log "Building interface"
( cd ui && { [ -d node_modules ] || npm install --silent; } && npm run build --silent )

for arch in arm64 amd64; do
  log "Building boad $VER for linux/$arch"
  mkdir -p "$BUILD/$arch"
  ( cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
      go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o "../$BUILD/$arch/boad" . )
done

if [ ! -f "$KEYS/private.asc" ]; then
  # Never in CI: a key made there signs a repository no client trusts.
  [ -z "${CI:-}" ] || die "no archive key in $KEYS, and CI must not make one"
  log "Creating the archive signing key in $KEYS"
  mkdir -p "$KEYS"
  docker run --rm -v "$PWD/$KEYS:/keys" "$IMAGE" bash -euc '
    apt-get -qq update >/dev/null && apt-get -qq install -y --no-install-recommends gnupg >/dev/null
    export GNUPGHOME=$(mktemp -d)
    gpg --batch --pinentry-mode loopback --passphrase "" --quick-gen-key \
      "infinite-streaming-boa archive <jonathaneoliver@users.noreply.github.com>" ed25519 sign never
    ( umask 077; gpg --batch --pinentry-mode loopback --passphrase "" --armor --export-secret-keys > /keys/private.asc )
    gpg --batch --armor --export > /keys/public.asc'
fi

log "Packaging $DEB_VERSION in $IMAGE"
docker run --rm \
  -v "$PWD/deb:/src:ro" \
  -v "$PWD/$BUILD:/in:ro" \
  -v "$PWD/$KEYS:/keys:ro" \
  -v "$PWD/dist:/out" \
  "$IMAGE" bash /src/mkrepo.sh "$DEB_VERSION"
[ -f dist/apt/dists/stable/InRelease ] || die "no signed repository was produced"
log "Built:"
ls -1 dist/deb | sed 's/^/    /'
echo "    dist/apt/ (dists/stable signed, pool/, boa-archive-keyring.gpg)"
