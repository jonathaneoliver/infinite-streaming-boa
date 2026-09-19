#!/usr/bin/env bash
#
# Install or update boa on an OpenWrt device (tested: OpenWrt 25.12, Pi 5).
#
# The device must already be a transparent bridge: the uplink port and the APs
# in one bridge, DHCP left to the upstream router. See openwrt/README.md.
#
#   ./scripts/openwrt-deploy.sh root@192.168.0.200
#   ./scripts/openwrt-deploy.sh root@192.168.0.200 --ui-only
#
# Idempotent. /etc/config/boa is installed only when absent, so local edits to
# it survive a redeploy; the init script and binary are always replaced.
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
[ -n "$TARGET" ] || die "usage: $0 root@<openwrt-host> [--ui-only]"
HOST="${TARGET#*@}"

log "Checking $TARGET"
ssh -o ConnectTimeout=8 -o BatchMode=yes "$TARGET" 'test -f /etc/openwrt_release' 2>/dev/null \
  || die "cannot reach $TARGET over SSH with a key, or it is not OpenWrt."

log "Building interface"
( cd ui && { [ -d node_modules ] || npm install --silent; } && npm run build --silent )

# The interface is embedded in the binary, so --ui-only still rebuilds it; it
# only skips the package install below.
VER="$(bash scripts/version.sh)"
OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT
log "Building boad $VER for linux/arm64"
( cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o "$OUT/boad" . )

if [ "$UI_ONLY" -eq 0 ]; then
  # tc-full, not tc-tiny: boad's u32 filters and netem's rate/loss options
  # need the full build. ip-bridge provides `bridge` for the forwarding table.
  # kmod-nft-bridge is the nftables bridge family the flow view counts in
  # (portpairs.go); without it that view reports itself unavailable.
  log "Installing packages"
  ssh "$TARGET" 'apk add -q tc-full kmod-netem kmod-ifb kmod-sched-core kmod-nft-bridge ip-full ip-bridge iw iperf3 >/dev/null'
fi

log "Installing files"
# Write beside the running binary, then rename: replacing it in place fails
# with "text file busy" while the service is up.
ssh "$TARGET" 'mkdir -p /usr/libexec/boa && cat > /usr/libexec/boa/boad.new && chmod 0755 /usr/libexec/boa/boad.new && mv /usr/libexec/boa/boad.new /usr/libexec/boa/boad' < "$OUT/boad"
ssh "$TARGET" 'cat > /etc/init.d/boa && chmod 0755 /etc/init.d/boa' < openwrt/files/etc/init.d/boa
ssh "$TARGET" '[ -f /etc/config/boa ] || cat > /etc/config/boa' < openwrt/files/etc/config/boa

# LuCI's Services -> Boa page. LuCI caches its menu and rpcd reads ACLs only at
# start, so a new or changed entry appears only after both are refreshed.
log "Installing LuCI page"
ssh "$TARGET" 'mkdir -p /www/luci-static/resources/view/boa && cat > /www/luci-static/resources/view/boa/boa.js' < openwrt/files/www/luci-static/resources/view/boa/boa.js
ssh "$TARGET" 'cat > /usr/share/luci/menu.d/luci-app-boa.json' < openwrt/files/usr/share/luci/menu.d/luci-app-boa.json
ssh "$TARGET" 'cat > /usr/share/rpcd/acl.d/luci-app-boa.json' < openwrt/files/usr/share/rpcd/acl.d/luci-app-boa.json
ssh "$TARGET" 'rm -rf /tmp/luci-indexcache* /tmp/luci-modulecache; /etc/init.d/rpcd restart'

log "Restarting service"
# `restart` on a service that has never run makes procd print a "Command
# failed ... Not found" that reads as an error on a first install.
ssh "$TARGET" '/etc/init.d/boa enable && if /etc/init.d/boa running; then /etc/init.d/boa restart; else /etc/init.d/boa start; fi'

for _ in 1 2 3 4 5 6 7 8 9 10; do
  if H="$(curl -fsS --max-time 3 "http://${HOST}:8080/api/health" 2>/dev/null)"; then
    log "boad is up: $(printf '%s' "$H" | grep -o '"version":"[^"]*"')"
    log "Open http://${HOST}:8080/"
    exit 0
  fi
  sleep 1
done
die "boad did not answer on http://${HOST}:8080/api/health -- check: ssh $TARGET logread -e boad"
