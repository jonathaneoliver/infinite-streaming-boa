#!/usr/bin/env bash
#
# Push the current code to a running Pi and restart the service.
#
# Rebuilding and reflashing a 2.8 GB image to change one line is a ten-minute
# round trip. This is about ten seconds: build the interface, cross-compile the
# daemon, copy one binary, restart one unit.
#
#   ./scripts/deploy.sh                  -> boa@infinite-streaming-boa.local
#   ./scripts/deploy.sh boa@192.168.1.9
#   ./scripts/deploy.sh --ui-only        -> skip the Go build when only the
#                                           interface changed
#   ./scripts/deploy.sh --force          -> deploy over a running sweep or
#                                           pattern, and over another
#                                           deployer's claim
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
TARGET="${TARGET:-boa@infinite-streaming-boa.local}"
NEW=infinite-streaming-boa

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

# ---------------------------------------------------------------------------
# CLAIM THE BOX BEFORE BUILDING ANYTHING.
#
# Several people -- and several agent sessions -- share one Pi, and deploys were
# landing on top of each other silently. On 2026-09-05 a build was deployed and
# verified, then replaced twenty minutes later by an OLDER tree from another
# session; the only way anyone noticed was that behaviour changed under them
# mid-test. Issue #222.
#
# The lock lives on the Pi, not in this script, because the contention is over
# the HARDWARE. A claim held anywhere else would not be seen by a deploy from
# another machine, a cron, or a person at a terminal.
#
# `mkdir` is the primitive: it is atomic on POSIX and, unlike `flock`, needs no
# file descriptor held open across the many separate ssh invocations below.
#
# Claimed BEFORE the build, not after: the build takes tens of seconds, and
# discovering someone else owns the box only once you are ready to copy wastes
# exactly that time.
CLAIM=/tmp/boa-deploy.lock
WHO="$(id -un)@$(hostname -s)"
CLAIM_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
CLAIM_BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
CLAIMED=0

release_claim() {
  [ "$CLAIMED" = "1" ] || return 0
  ssh -o ConnectTimeout=5 -o BatchMode=yes "$TARGET" "rm -rf $CLAIM" 2>/dev/null || true
  CLAIMED=0
}
# EVERY exit path, including the failures. A claim leaked by a build error would
# lock the box for ten minutes for no reason, and the person it blocks would
# have no idea why.
trap release_claim EXIT INT TERM

# Age in seconds, so a claim left behind by a killed deploy cannot wedge the box
# for ever. Ten minutes is comfortably longer than a deploy (about ten seconds)
# and shorter than anyone's patience.
CLAIM_STALE=600

claim_box() {
  local held age
  held=$(ssh -o ConnectTimeout=8 -o BatchMode=yes "$TARGET" "
    if mkdir $CLAIM 2>/dev/null; then
      printf '%s\n' 'ok'
    else
      age=\$(( \$(date +%s) - \$(stat -c %Y $CLAIM 2>/dev/null || date +%s) ))
      printf 'held\t%s\t%s\n' \"\$age\" \"\$(cat $CLAIM/owner 2>/dev/null || echo unknown)\"
    fi" 2>/dev/null) || die "cannot reach $TARGET to claim it"

  case "$held" in
    ok) CLAIMED=1 ;;
    held*)
      age=$(printf '%s' "$held" | cut -f2)
      owner=$(printf '%s' "$held" | cut -f3-)
      if [ "${age:-0}" -gt "$CLAIM_STALE" ]; then
        # LOUDLY, and never silently. A stale claim is either a crashed deploy
        # or a deploy that is genuinely still going; saying whose it was and how
        # old it is lets the person decide which.
        log "Breaking a stale deploy claim (${age}s old, held by ${owner})"
        ssh "$TARGET" "rm -rf $CLAIM && mkdir $CLAIM" >/dev/null 2>&1 \
          || die "could not break the stale claim on $TARGET"
        CLAIMED=1
      elif [ "$FORCE" = "1" ]; then
        log "Taking the box from ${owner} (claimed ${age}s ago) because --force"
        ssh "$TARGET" "rm -rf $CLAIM && mkdir $CLAIM" >/dev/null 2>&1 \
          || die "could not take the claim on $TARGET"
        CLAIMED=1
      else
        # This one BLOCKS, unlike the sweep and pattern checks above.
        #
        # Those are best-effort by design, because deploying is how you fix a
        # broken daemon and an unreachable one must never stop you. A deploy
        # claim is the opposite case: proceeding does not recover anything, it
        # destroys somebody else's work, and the deploy will still be there in
        # ten seconds.
        die "$TARGET is claimed by ${owner}, ${age}s ago.

  Another deploy is in progress. Deploying now would overwrite their binary
  mid-flight, and they would find out the way everyone has so far: by their
  own change quietly not being on the box any more.

  Wait for it, or take the box deliberately:
      ./scripts/deploy.sh --force"
      fi
      ;;
    *) die "unexpected answer while claiming $TARGET: $held" ;;
  esac

  ssh "$TARGET" "printf '%s\n' '$WHO $CLAIM_BRANCH@$CLAIM_SHA' > $CLAIM/owner" \
    2>/dev/null || true
}
claim_box

log "Building web interface"
( cd ui && { [ -d node_modules ] || npm install --silent; } && npm run build --silent )

# The interface is embedded in the binary, so a UI-only change still needs the
# Go build. --ui-only exists only to skip the typecheck-and-bundle when nothing
# in ui/ changed; the link step is unavoidable either way.
VER="$(bash scripts/version.sh)"
if [ "$UI_ONLY" = "0" ]; then
  log "Cross-compiling daemon for the Pi, version ${VER}"
fi
( cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o ../overlay/usr/local/bin/boad . )

SIZE=$(du -h overlay/usr/local/bin/boad | cut -f1)
log "Copying ${SIZE} binary"
# To a temp path first: overwriting a running executable in place fails with
# ETXTBSY, and a partial copy over the real path would leave the Pi with a
# broken binary if the transfer were interrupted.
scp -q overlay/usr/local/bin/boad "$TARGET:/tmp/boad.new"

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

# WHO deployed WHAT, recorded next to the thing they deployed.
#
# The version string alone does not identify a tree: a dirty build's SHA
# describes its last commit and not what was compiled, and every build in the
# incident that prompted this was dirty. So the branch, the working-tree state
# and the person are written down too. Anyone about to replace this binary can
# then see whose it is first -- which is the whole of #222 that no lock can
# cover, because the lock is gone the moment a deploy finishes.
DIRTY=""
git diff --quiet 2>/dev/null || DIRTY=" (dirty)"
PROV="version: ${VER}
branch:  ${CLAIM_BRANCH}${DIRTY}
commit:  $(git rev-parse HEAD 2>/dev/null || echo unknown)
by:      ${WHO}
at:      $(date -u +%Y-%m-%dT%H:%M:%SZ)"

log "Installing and restarting"
printf '%s\n' "$PROV" | ssh "$TARGET" "cat > /tmp/boa-deploy-info"
ssh "$TARGET" 'sudo install -m 0755 /tmp/boad.new /usr/local/bin/boad \
  && sudo install -m 0644 /tmp/boa-deploy-info /etc/boa-deploy-info \
  && sudo systemctl restart infinite-streaming-boa \
  && rm -f /tmp/boad.new /tmp/boa-deploy-info \
  && sleep 1 \
  && systemctl is-active infinite-streaming-boa'

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
log "Logs:   ssh $TARGET 'journalctl -u infinite-streaming-boa -f'"
