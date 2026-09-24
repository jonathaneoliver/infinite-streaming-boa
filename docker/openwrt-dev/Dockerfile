# OpenWrt userspace in a container: a fast loop for the uci, LuCI, procd and
# boa-setup half of the OpenWrt target.
#
# NOT A DEPLOYMENT, AND NOT A SUBSTITUTE FOR THE VM. It shares the host's
# kernel, so `kmod-netem`, `kmod-ifb` and `kmod-sched-core` -- boa's own
# declared dependencies -- cannot load here, and `boa-setup check` will report
# "dependencies are installed" on a box where `modprobe sch_netem` fails. Every
# driver and every firmware blob is the host's. OpenWrt's own documentation is
# blunt about this: "the OpenWrt runtime uses multiple active services to work
# and isn't really suited as a container", and the rootfs is for "special cases
# like CI testing". Use `openwrt-x86` under KVM to test anything kernel-shaped.
#
# What it is good for: iterating on uci config, the LuCI app, the init script,
# `boa-setup`, and first-boot logic (#370), where the kernel is irrelevant and a
# three-second start beats a twenty-five-second boot. It is also the only one of
# the three targets that can use the host's onboard AX200, which the VM cannot
# reach because of its IOMMU group -- see docs/BACKLOG.md.
#
# The base image is `docker import` of the official x86-64 rootfs tarball:
#
#   curl -sS -O https://downloads.openwrt.org/releases/25.12.5/targets/x86/64/openwrt-25.12.5-x86-64-rootfs.tar.gz
#   docker import openwrt-25.12.5-x86-64-rootfs.tar.gz openwrt-userspace:25.12.5
#
# `openwrt/rootfs` on Docker Hub is the published equivalent and would remove
# that step; it is not used here only because this image was proven against the
# imported tarball and the tag naming has not been verified.
FROM openwrt-userspace:25.12.5

# FIX 1 of 5 -- the minimal rootfs has no wireless userspace at all.
#
# `/sbin/wifi` comes from wifi-scripts, `hostapd` from wpad, `hostapd_cli` from
# hostapd-utils. Without wifi-scripts, netifd logs "Wireless module not found"
# and never creates an AP interface; without wpad there is no hostapd log at
# all, which reads as nothing happening rather than as a missing package.
#
# iw-full rather than the default iw: the tiny variant lacks commands boa uses.
# The apk index is deliberately LEFT IN PLACE. Clearing /var/cache/apk after
# the install saves a few megabytes and breaks the next `apk add` in a way that
# blames the wrong thing: installing boa then fails with
# "libxtables12 (no such package): required by tc-full", which reads as a
# missing dependency and is actually a missing index. Measured 2026-09-24.
RUN apk update \
 && apk add wpad-mbedtls hostapd-utils wifi-scripts iw-full

# FIX 2 of 5 -- wpad does not start on its own here.
#
# procd starts enabled services from /etc/rc.d. The package's post-install does
# not enable it in this environment, so hostapd never runs and every radio stays
# down with nothing logged.
#
# The symlink is made by hand rather than with `/etc/init.d/wpad enable`, which
# needs a running procd and ubus and therefore cannot work in a build step -- it
# exits 1 with no useful message. START is read from the script so the ordering
# stays whatever the package intended.
RUN S=$(sed -n 's/^START=\([0-9]*\).*/\1/p' /etc/init.d/wpad | head -1) \
 && [ -n "$S" ] || S=19 \
 && mkdir -p /etc/rc.d \
 && ln -sf ../init.d/wpad "/etc/rc.d/S${S}wpad" \
 && ls -l "/etc/rc.d/S${S}wpad"

# FIX 3 of 5 -- ujail prevents hostapd from starting in a container.
#
# /etc/init.d/wpad jails hostapd when BOTH /sbin/ujail and this capabilities
# file exist. The jail wants namespaces Docker already owns, so hostapd never
# comes up -- silently: no hostapd log, radios reporting up:false, netdevs torn
# down and not recreated. Moving the capabilities file aside is the documented
# way to skip the jail without editing the init script.
#
# THE COST IS REAL: hostapd then runs unsandboxed, as root, in a container that
# must already be --privileged. Acceptable for a development box on a trusted
# network, and not something to carry anywhere else.
RUN mv /etc/capabilities/wpad.json /etc/capabilities/wpad.json.disabled

# FIX 4 of 5 -- config_generate discards anything written before procd starts.
#
# The entrypoint records Docker's address and the uci-defaults script applies
# it, because config_generate regenerates /etc/config/network from board
# detection at first boot. Writing the config in the entrypoint looks like it
# works and does not: measured, the address was set correctly and the container
# still came up on OpenWrt's stock 192.168.1.1, with no route out.
COPY entrypoint.sh /usr/libexec/owrt-dev-entrypoint
COPY uci-defaults-docker-net /etc/uci-defaults/99-docker-net
RUN chmod +x /usr/libexec/owrt-dev-entrypoint /etc/uci-defaults/99-docker-net

ENTRYPOINT ["/usr/libexec/owrt-dev-entrypoint"]
