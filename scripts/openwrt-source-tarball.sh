#!/usr/bin/env bash
#
# The source tarball openwrt/packages would fetch, built the way a release
# workflow would build it.
#
#   ./scripts/openwrt-source-tarball.sh [version]   -> dist/boa-<version>.tar.gz
#
# WHY THIS EXISTS AT ALL, and it is the whole argument of issue #359: a
# distribution buildbot compiles from a tarball it fetched by hash, with no
# network and no Node. The Go daemon is fine -- daemon/go.mod has no
# dependencies, so nothing is downloaded to build it. The web interface is not:
# it is Vite and npm, neither of which exists on a builder.
#
# So the interface is built HERE and shipped inside the tarball, already
# compiled, for Go to embed. That is the same bargain an autotools release
# makes by shipping a generated ./configure, and it is the point a reviewer may
# challenge.
#
# What the tarball must therefore contain: the daemon's Go source, the built
# daemon/web/dist, the OpenWrt files the package installs, and the licence.
# Nothing else -- not the UI sources, not the image build, not the tests --
# because everything in it is source a distribution takes responsibility for.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

VERSION="${1:-$(./scripts/version.sh 2>/dev/null || echo 0.0.0)}"
OUT="dist/boa-${VERSION}.tar.gz"
STAGE="$(mktemp -d)/boa-${VERSION}"
trap 'rm -rf "$(dirname "$STAGE")"' EXIT

command -v npm >/dev/null || die "npm is needed to build the interface this tarball ships"

log "building the interface (the builder cannot)"
( cd ui && npm ci --silent >/dev/null 2>&1 || npm install --silent >/dev/null 2>&1; npm run build >/dev/null )
[ -f daemon/web/dist/index.html ] || die "no daemon/web/dist/index.html after the build"

# ARCHIVE THE TRACKED TREE, not the working one, and the reason is not
# tidiness. A working tree carries what a release must not: the test suite
# leaves hostapd control sockets in daemon/internal/boa -- 100 of them here,
# gitignored, and tar fails outright on a socket -- and any local scratch file
# would otherwise ship to a distribution. Tracked files are also exactly what a
# reviewer can see, which is the standard a source tarball is held to.
# THE MODULE GOES AT THE ROOT, not under daemon/ as it sits in the checkout.
# OpenWrt's Build/Prepare unpacks into $(PKG_BUILD_DIR)/.. and expects the
# tarball's top-level directory to be the build directory, so a module one
# level down ends up at boa-<v>/boa-<v>/daemon and the builder reports
# "go.mod file not found" while go.mod is plainly in the tarball. Arranging the
# tarball for the builder is cheaper than fighting that, and it is what a Go
# release tarball normally looks like anyway.
log "staging tracked source, and nothing a working tree accumulates"
mkdir -p "$STAGE"
git archive --format=tar HEAD daemon | tar -xf - -C "$STAGE" --strip-components=1
git archive --format=tar HEAD LICENSE openwrt/files | tar -xf - -C "$STAGE"
# The built interface is the one thing NOT tracked that must still ship: Go
# embeds it, and the builder cannot make it.
mkdir -p "$STAGE/web/dist"
cp -R daemon/web/dist/. "$STAGE/web/dist/"

# REPRODUCIBLE, because PKG_HASH is a promise about bytes: the same source must
# produce the same hash on the next machine, or the Makefile has to be edited to
# match and the hash stops meaning anything.
#
# That needs sorted names, fixed mtimes and no owner, which is GNU tar. macOS
# ships bsdtar, whose -cf takes none of those options -- so GNU tar is used
# where it exists (gtar from brew, or tar on Linux) and the fallback SAYS the
# hash may not match CI rather than producing a different tarball quietly.
mkdir -p dist
TAR=tar
command -v gtar >/dev/null && TAR=gtar
if $TAR --version 2>/dev/null | head -1 | grep -qi gnu; then
  log "packing reproducibly with $TAR"
  ( cd "$(dirname "$STAGE")" && $TAR --sort=name --format=gnu \
      --owner=0 --group=0 --numeric-owner --mtime='2026-01-01 00:00:00 UTC' \
      -cf - "boa-${VERSION}" ) | gzip -n > "$OUT"
else
  log "packing with bsdtar: file order and mtimes are this machine's"
  printf '    \033[33mnote:\033[0m install GNU tar (brew install gnu-tar) for a hash that\n'
  printf '    matches a Linux release build; this one is for local testing.\n'
  find "$STAGE" -exec touch -t 202601010000 {} +
  ( cd "$(dirname "$STAGE")" && COPYFILE_DISABLE=1 $TAR \
      --uid 0 --gid 0 -cf - "boa-${VERSION}" ) | gzip -n > "$OUT"
fi

SHA=$(shasum -a 256 "$OUT" | awk '{print $1}')
log "wrote $OUT"
log "PKG_HASH:=$SHA"
printf '\nPut that hash in openwrt/upstream/boa/Makefile, and the tarball where\n'
printf 'PKG_SOURCE_URL points -- a GitHub release asset for v%s.\n' "$VERSION"
