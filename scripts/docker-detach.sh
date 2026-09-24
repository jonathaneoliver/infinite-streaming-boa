#!/usr/bin/env bash
# Undoes scripts/docker-attach.sh: removes everything boa put on the HOST.
#
# WHY THIS HAD TO EXIST. Attach was written without an inverse, and everything
# it did outside the container's network namespace therefore outlived the
# container. Stopping boa left a DNAT still pointing at 10.123.0.2 -- an address
# that no longer answered -- so a port went from hijacked to black-holed for
# every container on the host, and the adapters stayed unmanaged by
# NetworkManager. Issue #329.
#
# The veth pairs and the moved adapters are NOT undone here, and do not need to
# be: they die with the container's network namespace, and a netdev returns to
# the host by itself. What does not is anything in iptables or in
# NetworkManager's opinion of a device.
#
# IDEMPOTENT, like attach. Running it twice, or on a host that never ran attach,
# must be silent and successful -- it is called from a container-stop event,
# which fires in situations nobody planned for.
set -euo pipefail

log() { echo "boa-detach: $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

[ "$(id -u)" = 0 ] || die "must run as root"

# --- iptables ---------------------------------------------------------------
#
# The jump rules first, then the chains they point at: iptables refuses to
# delete a chain anything still references, and the refusal would abort this
# script under `set -e` before the rest of the cleanup ran.
removed=0
if iptables -t nat -C PREROUTING -m addrtype --dst-type LOCAL -j BOA-DNAT 2>/dev/null; then
  iptables -t nat -D PREROUTING -m addrtype --dst-type LOCAL -j BOA-DNAT
  removed=1
fi
if iptables -t nat -C OUTPUT -o lo -m addrtype --dst-type LOCAL -j BOA-DNAT 2>/dev/null; then
  iptables -t nat -D OUTPUT -o lo -m addrtype --dst-type LOCAL -j BOA-DNAT
  removed=1
fi
if iptables -t nat -L BOA-DNAT -n >/dev/null 2>&1; then
  iptables -t nat -F BOA-DNAT
  iptables -t nat -X BOA-DNAT
  removed=1
fi

if iptables -C DOCKER-USER -j BOA-FILTER 2>/dev/null; then
  iptables -D DOCKER-USER -j BOA-FILTER
  removed=1
fi
if iptables -L BOA-FILTER -n >/dev/null 2>&1; then
  iptables -F BOA-FILTER
  iptables -X BOA-FILTER
  removed=1
fi
[ "$removed" = 1 ] && log "removed boa's iptables chains" || log "no boa iptables rules present"

# --- the rules attach used to leave behind unscoped --------------------------
#
# A host that ran the OLD attach carries rules this script's chain-based
# teardown knows nothing about, and they are exactly the ones doing damage.
# Removed by their full argument list, which is the only way to name them.
for chain in PREROUTING OUTPUT; do
  while read -r port dest; do
    [ -n "$port" ] || continue
    if [ "$chain" = OUTPUT ]; then
      iptables -t nat -D OUTPUT -o lo -p tcp --dport "$port" -j DNAT --to-destination "$dest" 2>/dev/null &&
        log "removed a pre-#329 unscoped rule in $chain (port $port)" || true
    else
      iptables -t nat -D PREROUTING -p tcp --dport "$port" -j DNAT --to-destination "$dest" 2>/dev/null &&
        log "removed a pre-#329 unscoped rule in $chain (port $port)" || true
    fi
  done < <(iptables -t nat -S "$chain" 2>/dev/null |
             sed -n 's/.*--dport \([0-9]*\).*--to-destination \([0-9.:]*\).*/\1 \2/p')
done

# --- NetworkManager ----------------------------------------------------------
#
# Attach sets every USB adapter unmanaged so NetworkManager cannot rename or
# address a device the container owns. Handing them back matters: an unmanaged
# adapter after boa has stopped is a port that silently does nothing.
if command -v nmcli >/dev/null 2>&1; then
  for path in /sys/class/net/*; do
    iface=$(basename "$path")
    [ -e "$path/device" ] || continue
    case "$(readlink -f "$path/device")" in *usb*) ;; *) continue ;; esac
    nmcli device set "$iface" managed yes >/dev/null 2>&1 &&
      log "$iface handed back to NetworkManager" || true
  done
fi

log "detached. The veths and any moved adapters went with the container's namespace."
