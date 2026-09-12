#!/usr/bin/env bash
# Builds the container and puts it on a Linux host. The docker equivalent of
# scripts/deploy.sh, which does the same job for the Pi.
#
# The Vue interface and the Go binary are built HERE, on the workstation, and
# only the finished artefacts are shipped -- the same split deploy.sh uses, and
# for the same reason: the target does not need node or a Go toolchain, and the
# version string comes from this checkout's git history rather than from
# whatever happens to be on the box.
#
#   scripts/docker-deploy.sh jonathanoliver-ubuntu.local
#
# The first run on a host also needs the network prepared, which is a separate
# and reversible step because it briefly disconnects the machine:
#
#   scripts/docker-deploy.sh <host> --setup-network
#
# The uplink interface is discovered from the host's default route. Name it only
# if that cannot resolve, which the network script says when it happens:
#
#   scripts/docker-deploy.sh <host> --setup-network --wan-if enp1s0
set -euo pipefail

HOST=${1:?usage: $0 <host> [--setup-network] [--wan-if <name>]}
shift || true
SETUP_NETWORK=0
# Empty means "let the host work it out", which is the normal case: the network
# script finds the interface carrying the default route. This is here for the
# hosts it cannot resolve -- two default routes, or an uplink that is not the
# route currently in use.
WAN_IF=""
while [ $# -gt 0 ]; do
  case "$1" in
    --setup-network) SETUP_NETWORK=1 ;;
    --wan-if) shift; WAN_IF=${1:?--wan-if needs an interface name} ;;
    --wan-if=*) WAN_IF=${1#*=} ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

REPO=$(cd "$(dirname "$0")/.." && pwd)
REMOTE_DIR=/opt/infinite-streaming-boa
VER=$("$REPO/scripts/version.sh")

log() { echo "==> $*"; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- access point settings ---------------------------------------------------
#
# From .env, sourced exactly as build.sh sources it, so the container and the Pi
# image cannot disagree about the passphrase. The alternative -- a second copy
# of the credential kept on the target -- is how two boxes end up on one SSID
# with two different passwords, which presents as a radio that will not accept
# a client rather than as a mismatch.
#
# The SSID is the deliberate exception. Both boxes broadcasting
# "infinite-streaming-boa" is worse than useless: a client cannot tell them
# apart, will roam to whichever is louder, and a measurement then belongs to
# whichever box happened to win. AP_SSID_DOCKER names this one separately.
# .env is gitignored, so a WORKTREE never has one -- it lives only in the
# checkout the worktree was made from. Falling back to that one keeps a deploy
# from a feature worktree using the same credential as a deploy from main,
# instead of failing or, worse, quietly using a second copy that has drifted.
# The main checkout is the parent of the common git directory, which is where
# `git rev-parse --git-common-dir` points from anywhere in the repository.
ENV_FILE="${ENV_FILE:-$REPO/.env}"
if [ ! -f "$ENV_FILE" ]; then
  common=$(git -C "$REPO" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
  if [ -n "$common" ] && [ -f "$(dirname "$common")/.env" ]; then
    ENV_FILE="$(dirname "$common")/.env"
    log "no .env in this worktree; using the main checkout's $ENV_FILE"
  fi
fi
[ -f "$ENV_FILE" ] || die "no $ENV_FILE — copy .env.example to .env and edit it"
set -a; . "$ENV_FILE"; set +a

: "${AP_PASSWORD:?AP_PASSWORD must be set in $ENV_FILE}"
AP_COUNTRY="${AP_COUNTRY:-US}"
# Falls back to AP_SSID rather than to the repository default, so a checkout
# that has not adopted AP_SSID_DOCKER still gets the operator's own name.
CONTAINER_SSID="${AP_SSID_DOCKER:-${AP_SSID:?AP_SSID must be set in $ENV_FILE}}"
log "access point: SSID '$CONTAINER_SSID', country $AP_COUNTRY (from $ENV_FILE)"

# --- build here --------------------------------------------------------------
log "building the interface"
( cd "$REPO/ui" && { [ -d node_modules ] || npm install --silent; } && npm run build --silent )

# amd64, statically linked, no cgo. The Pi build differs in exactly one flag.
log "building boad $VER for linux/amd64"
( cd "$REPO/daemon" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o ../docker/boad . )

# --- ship --------------------------------------------------------------------
log "shipping the build context to $HOST:$REMOTE_DIR"
ssh "$HOST" "sudo install -d -o \$(id -u) -g \$(id -g) $REMOTE_DIR"
# The context is small and specific: the Dockerfile needs docker/ for its own
# pieces and overlay/ for radioplan, and nothing else.
tar -C "$REPO" -czf - docker overlay scripts/docker-attach.sh scripts/docker-host-net.sh \
  | ssh "$HOST" "tar -C $REMOTE_DIR -xzf -"

# --- host-side plumbing ------------------------------------------------------
log "installing the attach helpers on $HOST"
ssh "$HOST" "sudo install -m 0755 $REMOTE_DIR/scripts/docker-attach.sh /usr/local/sbin/boa-attach \
  && sudo install -m 0755 $REMOTE_DIR/docker/boa-attach-watch /usr/local/sbin/boa-attach-watch \
  && sudo install -m 0644 $REMOTE_DIR/docker/boa-attach.service /etc/systemd/system/boa-attach.service \
  && sudo systemctl daemon-reload"

# The attach unit's environment, written every deploy from .env for the same
# reason docker/.env is: the host must not drift away from the checkout that
# deployed it.
#
# WRITTEN EVEN WHEN EMPTY, and that is the point. This is the file that decides
# whether the host gives its own Wi-Fi card away, so leaving a previous deploy's
# value in place after it was removed from .env would keep taking the card
# indefinitely with nothing in the checkout saying so.
log "rendering the attach settings on $HOST${BOA_SCAN_IF:+ (listen-only radio: $BOA_SCAN_IF)}"
ssh "$HOST" "sudo tee /etc/default/boa-attach >/dev/null" <<ATTACHENV
# GENERATED by scripts/docker-deploy.sh from $ENV_FILE. Edits are lost on the
# next deploy -- change .env in the checkout instead.
#
# The HOST's name for a wireless card to hand to the container as an instrument
# rather than an access point. Empty means hand over nothing, which is the
# default: this card is the host's own, and the host may well be using it.
SCAN_IF=${BOA_SCAN_IF:-}
ATTACHENV

if [ "$SETUP_NETWORK" = 1 ]; then
  # Detached, via systemd-run, because this drops the host's connectivity for a
  # few seconds and an SSH session that dies mid-switch would take an
  # un-detached script down with it -- leaving a half-built bridge and no way
  # back in. The script rolls itself back if the gateway does not return.
  log "bridging the host NIC (connectivity will drop briefly)"
  # --setenv, not an environment prefix: systemd-run starts the unit from the
  # service manager's own environment, so `WAN_IF=x systemd-run ...` sets the
  # variable for systemd-run and not for the script it launches.
  SETENV=""
  [ -n "$WAN_IF" ] && SETENV="--setenv=WAN_IF=$WAN_IF"
  ssh "$HOST" "sudo systemd-run --unit=boa-netswitch --collect $SETENV \
      $REMOTE_DIR/scripts/docker-host-net.sh apply" || true
  log "waiting for $HOST to come back"
  for _ in $(seq 60); do
    sleep 2
    ssh -o ConnectTimeout=4 -o BatchMode=yes "$HOST" true 2>/dev/null && break
  done
  ssh "$HOST" "sudo journalctl -u boa-netswitch --no-pager -o cat | tail -20"
fi

# --- build and run there -----------------------------------------------------
# compose reads this for the environment: block in compose.yml. Written every
# deploy from .env, never edited on the target, so the box cannot drift away
# from the checkout that deployed it. 0600 because it carries the passphrase.
log "rendering the access point settings on $HOST"
ssh "$HOST" "umask 077 && cat > $REMOTE_DIR/docker/.env" <<ENVFILE
# GENERATED by scripts/docker-deploy.sh from $ENV_FILE. Edits are lost on the
# next deploy -- change .env in the checkout instead.
AP_SSID=$CONTAINER_SSID
AP_PASSWORD=$AP_PASSWORD
AP_COUNTRY=$AP_COUNTRY
ENVFILE

log "building the image on $HOST"
ssh "$HOST" "cd $REMOTE_DIR/docker && docker compose build --quiet"

log "starting the container"
ssh "$HOST" "cd $REMOTE_DIR/docker && docker compose up -d --force-recreate"

log "attaching the adapters"
ssh "$HOST" "sudo systemctl enable --now boa-attach.service && sleep 3 && sudo systemctl restart boa-attach.service"

sleep 5
log "container log:"
ssh "$HOST" "docker logs --tail 40 boa 2>&1" || true

log "done. Interface: http://$HOST:8080/"
