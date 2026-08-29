#!/usr/bin/env bash
#
# Push the current code to a running Pi and restart the service.
#
# Rebuilding and reflashing a 2.8 GB image to change one line is a ten-minute
# round trip. This is about ten seconds: build the interface, cross-compile the
# daemon, copy one binary, restart one unit.
#
#   ./scripts/deploy.sh                  -> pifi@infinite-streaming-pifi.local
#   ./scripts/deploy.sh pifi@192.168.1.9
#   ./scripts/deploy.sh --ui-only        -> skip the Go build when only the
#                                           interface changed
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log()  { printf '\033[36m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

UI_ONLY=0
TARGET=""
for a in "$@"; do
  case "$a" in
    --ui-only) UI_ONLY=1 ;;
    -*) die "unknown option: $a" ;;
    *) TARGET="$a" ;;
  esac
done
TARGET="${TARGET:-pifi@infinite-streaming-pifi.local}"
NEW=infinite-streaming-pifi

# Fail early with a clear message rather than midway through a build.
log "Checking $TARGET"
ssh -o ConnectTimeout=8 -o BatchMode=yes "$TARGET" true 2>/dev/null \
  || die "cannot reach $TARGET over SSH.
  If it asks for a password, set up a key first:
      ssh-copy-id $TARGET
  A key is worth it -- this script runs many times an hour."

log "Building web interface"
( cd ui && { [ -d node_modules ] || npm install --silent; } && npm run build --silent )

# The interface is embedded in the binary, so a UI-only change still needs the
# Go build. --ui-only exists only to skip the typecheck-and-bundle when nothing
# in ui/ changed; the link step is unavoidable either way.
if [ "$UI_ONLY" = "0" ]; then
  log "Cross-compiling daemon for the Pi"
fi
( cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags='-s -w' -o ../overlay/usr/local/bin/infinite-streaming-pifid . )

SIZE=$(du -h overlay/usr/local/bin/infinite-streaming-pifid | cut -f1)
log "Copying ${SIZE} binary"
# To a temp path first: overwriting a running executable in place fails with
# ETXTBSY, and a partial copy over the real path would leave the Pi with a
# broken binary if the transfer were interrupted.
scp -q overlay/usr/local/bin/infinite-streaming-pifid "$TARGET:/tmp/infinite-streaming-pifid.new"

# The systemd unit is part of the deployable payload too. Shipping only the
# binary meant a unit change silently required a reflash to take effect -- which
# is exactly how a RuntimeDirectory fix sat inert while its feature looked
# broken.
UNIT="overlay/etc/systemd/system/${NEW}.service"
if [ -f "$UNIT" ]; then
  if ! ssh "$TARGET" "cmp -s /etc/systemd/system/${NEW}.service -" < "$UNIT" 2>/dev/null; then
    log "Unit file changed; updating it"
    scp -q "$UNIT" "$TARGET:/tmp/${NEW}.service"
    ssh "$TARGET" "sudo install -m 0644 /tmp/${NEW}.service \
      /etc/systemd/system/${NEW}.service && sudo systemctl daemon-reload \
      && rm -f /tmp/${NEW}.service"
  fi
fi

log "Installing and restarting"
ssh "$TARGET" 'sudo install -m 0755 /tmp/infinite-streaming-pifid.new /usr/local/bin/infinite-streaming-pifid \
  && sudo systemctl restart infinite-streaming-pifi \
  && rm -f /tmp/infinite-streaming-pifid.new \
  && sleep 1 \
  && systemctl is-active infinite-streaming-pifi'

# Ask the daemon itself, not just systemd: the unit can be "active" while the
# daemon is failing to configure the kernel, and that distinction is the whole
# point of the capability flags.
log "Health:"
ssh "$TARGET" 'curl -s --max-time 5 http://localhost/api/health' \
  | python3 -m json.tool 2>/dev/null \
  || log "(daemon is running but did not answer /api/health yet)"

HOSTONLY="${TARGET#*@}"
echo
log "Live at http://${HOSTONLY}/"
log "Logs:   ssh $TARGET 'journalctl -u infinite-streaming-pifi -f'"
