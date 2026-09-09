# The appliance as a container, for a Linux host that already has the adapters.
#
# This is NOT the image-surgery builder at the repository root -- that one
# assembles a Raspberry Pi SD card. This one runs boad itself on an x86_64 host,
# in its own network namespace, owning the USB adapters outright.
#
# What the Pi gets from systemd, udev and NetworkManager, this image gets from
# its entrypoint: the bridge is built by hand, radioplan still writes the hostapd
# configs, and a systemctl shim answers the two questions the daemon asks of
# systemd. Nothing under daemon/ is modified to run here.
FROM debian:trixie-slim

# hostapd  -- serves the access point, exactly as on the Pi.
# iproute2 -- ip, tc and bridge; the daemon shells out to all three.
# iw       -- station dumps and phy capabilities; radioplan reads the channels.
# busybox  -- udhcpc, so the bridge can take a lease from the real router the
#             same way NetworkManager gives it one on the Pi.
# python3  -- radioplan's channel-memory step is a python heredoc.
# iperf3   -- measures the UNSHAPED ceiling; traffic to this box is exempt.
RUN apt-get update && apt-get install -y --no-install-recommends \
      hostapd iproute2 iw busybox python3 iperf3 \
      nftables rfkill ethtool iputils-ping procps ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /etc/hostapd /etc/infinite-streaming-boa \
             /var/lib/infinite-streaming-boa /run/boa/hostapd

# hostapd's Debian package ships a sample config and a unit that would fight
# ours. There is no init here to start the unit, but removing the config keeps a
# stray `hostapd` invocation from finding one.
RUN rm -f /etc/hostapd/hostapd.conf


# radioplan is taken from the overlay UNCHANGED. It is the one piece of the Pi's
# boot that this container reuses verbatim, so a channel plan made here and a
# channel plan made on the Pi cannot drift apart.
COPY overlay/usr/local/sbin/infinite-streaming-boa-radioplan /usr/local/sbin/
COPY docker/systemctl /usr/local/bin/systemctl
COPY docker/boa-hostapd-supervise /usr/local/sbin/
COPY docker/entrypoint.sh /usr/local/sbin/boa-entrypoint
# Debian's busybox package ships udhcpc but NOT the lease script every guide
# assumes it does -- verified absent in a built image, not taken on trust. A
# client with no script gets an offer and configures nothing.
COPY docker/udhcpc.script /usr/share/udhcpc/default.script
COPY docker/boad /usr/local/bin/boad
RUN chmod 0755 /usr/local/sbin/infinite-streaming-boa-radioplan \
               /usr/local/bin/systemctl \
               /usr/local/sbin/boa-hostapd-supervise \
               /usr/local/sbin/boa-entrypoint \
               /usr/share/udhcpc/default.script \
               /usr/local/bin/boad

# /usr/local/bin first, so the daemon's exec.Command("systemctl", ...) finds the
# shim rather than nothing at all. /usr/sbin is named explicitly for the same
# reason the Pi's non-login SSH shells need the absolute path: tc, bridge and iw
# live there and a bare invocation would otherwise fail as "command not found".
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

ENTRYPOINT ["/usr/local/sbin/boa-entrypoint"]
