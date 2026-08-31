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
FORCE=0
TARGET=""
for a in "$@"; do
  case "$a" in
    --ui-only) UI_ONLY=1 ;;
    --force)   FORCE=1 ;;
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

# A deploy restarts the unit, and a ladder sweep or a playing pattern lives only
# in the daemon's
# memory -- deliberately, so a crash cannot leave a device throttled with
# nothing to unwind. The cost is that deploying mid-sweep destroys it silently.
#
# That has happened twice: once to the person running the sweep, and once to
# someone else on a shared box who had no way of knowing a half-hour
# measurement was in progress. A sweep can run for thirty minutes and gives no
# sign from outside, so this asks before throwing one away.
#
# Best effort by design: an unreachable or too-old daemon must not block a
# deploy, since deploying is how you FIX a broken daemon.
#
# It has to say WHICH, because the two cost very different amounts. The first
# version flattened the JSON and counted '"state":"running"', which also matched
# a playing pattern -- so an interrupted two-minute test was reported as a lost
# half-hour measurement, and the only way past it was --force, which would have
# been right for the pattern and wrong for the sweep.
if [ "$FORCE" = "0" ] && command -v python3 >/dev/null 2>&1; then
  busy=$(ssh -o ConnectTimeout=5 -o BatchMode=yes "$TARGET" \
    "curl -s --max-time 4 localhost/api/state" 2>/dev/null \
    | python3 -c '
import json, sys
try:
    st = json.load(sys.stdin)
except Exception:
    sys.exit(0)          # unreachable or too old to ask: never block a deploy
for c in st.get("clients") or []:
    who = c.get("hostname") or c.get("mac") or "?"
    sw = c.get("sweep") or {}
    if sw.get("state") == "running":
        print("sweep\t%s\t%s" % (who, sw.get("service") or "?"))
    pr = c.get("pattern_run") or {}
    if pr.get("state") == "running":
        print("pattern\t%s\t%s" % (who, (c.get("policy") or {}).get("pattern", {}).get("name") or ""))
' 2>/dev/null || true)

  if printf '%s' "$busy" | grep -q '^sweep'; then
    die "a ladder sweep is running on $TARGET:
$(printf '%s' "$busy" | grep '^sweep' | sed 's/^sweep\t/      /;s/\t/ · /')

  Deploying restarts the daemon, which ends the sweep and loses its results --
  it keeps no state on disk until it finishes, and a sweep can run for half an
  hour with no sign from outside.

  Wait for it, stop it from the interface, or:
      ./scripts/deploy.sh --force"
  fi

  if [ -n "$busy" ]; then
    die "a pattern is playing on $TARGET:
$(printf '%s' "$busy" | grep '^pattern' | sed 's/^pattern\t/      /;s/\t/ · /')

  Deploying restarts the daemon, which stops the run and returns the device to
  its stored policy. Cheap to restart, unlike a sweep -- but somebody is
  mid-test, so this asks first.

  Stop it from the interface, or:
      ./scripts/deploy.sh --force"
  fi
fi

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
