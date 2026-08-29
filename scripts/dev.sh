#!/usr/bin/env bash
#
# One-command development environment for the web interface.
#
# Starts the daemon in demo mode (synthetic clients, no kernel access, no root)
# and Vite with hot module replacement in front of it. Editing a .vue file
# updates the browser in well under a second, with no Pi involved and no image
# to rebuild.
#
#   ./scripts/dev.sh              synthetic clients
#   ./scripts/dev.sh infinite-streaming-pifi.local   live data from a real Pi (read-write: writes
#                                 to the UI really do condition its traffic)
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }

PI="${1:-}"
API_PORT=8099

if [ -n "$PI" ]; then
  export PIFI_API="http://${PI}"
  log "UI will talk to the real Pi at $PIFI_API"
  log "Changes you make WILL condition that device's traffic"
else
  log "Starting daemon in demo mode on :${API_PORT} (synthetic clients)"
  ( cd daemon && go run . -demo -addr ":${API_PORT}" -state /tmp/infinite-streaming-pifi-demo.json ) &
  DAEMON=$!
  # Kill the daemon whenever this script ends, however it ends -- otherwise a
  # stale one holds the port and the next run silently talks to old code.
  trap 'kill $DAEMON 2>/dev/null || true' EXIT INT TERM
  export PIFI_API="http://localhost:${API_PORT}"
  sleep 1
fi

log "Starting Vite with hot reload"
cd ui
[ -d node_modules ] || npm install --silent
exec npm run dev
