#!/usr/bin/env bash
#
# Captures a source-built ntopng from the Pi into a reusable tarball.
#
# ntop ships x86-64 binaries only, Docker Hub's image is amd64 only, and Debian
# dropped the package after buster -- so on arm64 ntopng has to be compiled,
# which takes the better part of an hour on a Pi 5. Doing that once and keeping
# the result means a reflash costs seconds instead of a rebuild, and the daily
# UI/daemon loop never touches it at all.
#
# The artifact lands in cache/, which is gitignored: ntopng is GPLv3 and a
# compiled binary is exactly the kind of thing docs/LICENSING.md says not to
# redistribute from this repository. It is built on the user's own machine from
# upstream source, and stays there.
#
#   ./scripts/package-ntopng.sh [boa@infinite-streaming-boa.local]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

TARGET="${1:-boa@infinite-streaming-boa.local}"
OUT="cache/ntopng-arm64.tar.gz"
DEPS="cache/ntopng-runtime-deps.txt"

ssh -o BatchMode=yes -o ConnectTimeout=8 "$TARGET" 'test -d /opt/ntop/ntopng' \
  || die "no build tree at /opt/ntop/ntopng on $TARGET -- run the build first"

mkdir -p cache

# DESTDIR staging gives an exact, relocatable file list without having to guess
# what `make install` scattered across the filesystem.
log "Staging install tree on the Pi"
ssh "$TARGET" '
  set -e
  rm -rf /tmp/ntopng-stage
  cd /opt/ntop/ntopng
  make install DESTDIR=/tmp/ntopng-stage >/dev/null 2>&1
  cd /tmp/ntopng-stage
  tar czf /tmp/ntopng-arm64.tar.gz .
  du -h /tmp/ntopng-arm64.tar.gz | cut -f1
'

log "Fetching artifact"
scp -q "$TARGET:/tmp/ntopng-arm64.tar.gz" "$OUT"

# The shared libraries it links are cheap apt packages; only the compile is
# expensive. Resolving them from the binary means the package list cannot drift
# from what was actually built.
log "Resolving runtime library packages from the built binary"
#
# readlink -f is essential, not tidiness: on a usrmerge system ldd reports
# libraries under /lib/... while dpkg knows the same files as /usr/lib/...,
# so every lookup misses and the dependency list comes back empty -- which
# would graft a binary into an image whose shared libraries are absent.
ssh "$TARGET" '
  BIN=$(command -v ntopng || echo /usr/local/bin/ntopng)
  ldd "$BIN" 2>/dev/null | awk "{print \$3}" | grep "^/" |
    xargs -r readlink -f | sort -u |
    xargs -r dpkg -S 2>/dev/null | cut -d: -f1 | tr -d " " | sort -u
' > "$DEPS"

ssh "$TARGET" 'rm -f /tmp/ntopng-arm64.tar.gz; rm -rf /tmp/ntopng-stage' || true

cat <<EOF

  Artifact : $OUT ($(du -h "$OUT" | cut -f1))
  Runtime  : $(wc -l < "$DEPS" | tr -d ' ') library packages listed in $DEPS

  build.sh will now graft this into every image it builds, so a reflash keeps
  ntopng with no compile. Rebuild it only when you want a newer ntopng:

      ssh $TARGET 'cd /opt/ntop/ntopng && git pull && make -j4 && sudo make install'
      ./scripts/package-ntopng.sh

EOF
