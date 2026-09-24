#!/bin/sh
# Record Docker's addressing, disarm bridge netfilter, then start procd.
#
# Only RECORDS the address here -- see FIX 4 in the Dockerfile. The uci-defaults
# script applies it, because config_generate runs at first boot and discards
# anything written beforehand.
set -u

warn() { echo "owrt-dev-entrypoint: $*" >&2; }

# FIX 5 of 5 -- a transparent bridge does not work in a container until bridged
# frames stop traversing the firewall.
#
# Docker requires br_netfilter, which makes frames CROSSING A BRIDGE traverse
# netfilter's forward chain. OpenWrt's fw4 has `policy drop` there and accepts
# only what arrives on the interface in its `lan` zone, so every frame between
# the access point, the uplink and a wired client port is dropped.
#
# The failure is quiet and convincing: clients associate, complete the four-way
# handshake, receive a DHCP lease, and then nothing passes. Measured 2026-09-24
# -- an iPhone sat spinning on "joining" with a valid address.
#
# This cannot happen in the VM: without Docker, br_netfilter is not loaded and
# bridged frames bypass netfilter entirely, which is what OpenWrt assumes.
for k in iptables ip6tables arptables; do
    f="/proc/sys/net/bridge/bridge-nf-call-$k"
    [ -w "$f" ] && echo 0 > "$f" 2>/dev/null
done
[ "$(cat /proc/sys/net/bridge/bridge-nf-call-iptables 2>/dev/null)" = "0" ] \
    || warn "bridge-nf-call-iptables is still on; a transparent bridge will drop traffic"

{
    echo "DOCKER_IP=$(ip -4 -o addr show eth0 2>/dev/null | awk '{print $4}' | head -1)"
    echo "DOCKER_GW=$(ip -4 route show default 2>/dev/null | awk '{print $3}' | head -1)"
} > /etc/docker-net.env

exec /sbin/init
