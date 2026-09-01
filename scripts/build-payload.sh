#!/usr/bin/env bash
#
# Builds everything that ships on the Pi and stages it in overlay/, which
# customize.sh grafts into the image root.
#
# The result is a SINGLE binary: the Vue interface is compiled and embedded into
# the Go executable, so the appliance carries no node runtime, no web server, no
# virtualenv and nothing to install on first boot.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log() { printf '\033[36m    ->\033[0m %s\n' "$*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null   || die "go is required to build the daemon"
command -v npm >/dev/null  || die "npm is required to build the web interface"

log "Building web interface"
(
  cd ui
  # `npm ci` when a lockfile exists so builds are reproducible; fall back to
  # install on a fresh checkout that has not resolved dependencies yet.
  if [ -f package-lock.json ]; then npm ci --silent; else npm install --silent; fi
  npm run build --silent
)

VER="$(bash scripts/version.sh)"
log "Cross-compiling daemon for the Pi (linux/arm64), version ${VER}"
mkdir -p overlay/usr/local/bin
(
  cd daemon
  # -s -w strips the symbol table and DWARF data: the binary lives on an SD
  # card and nobody debugs it with gdb. -X stamps the git-derived version.
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o ../overlay/usr/local/bin/boad .
)
chmod 0755 overlay/usr/local/bin/boad

log "Payload ready: $(du -h overlay/usr/local/bin/boad | cut -f1) binary"
