# OpenWrt userspace in a container — exploratory, not a supported target

A third place to run boa's OpenWrt code, beside the Cudy and the x86 VM. It is
**not** a deployment and **not** a substitute for either, and this directory is
not wired into any build or CI.

## What it is for

A fast loop for the half of the OpenWrt target that is userspace: uci config,
the LuCI app, the init script, `boa-setup`, and first-boot logic (#370). Three
seconds to start against the VM's twenty-five, and a 20 MB image.

It is also the only one of the three targets that can use the host's onboard
**AX200**, because a container shares the kernel and a radio moves with
`iw phy … set netns`. The VM cannot: that card shares an IOMMU group with the
host's NVMe, SATA and USB controllers, so VFIO would have to hand all of them
over. See `docs/BACKLOG.md`.

## What it cannot do, and why the VM still exists

It shares the host's kernel. `kmod-netem`, `kmod-ifb` and `kmod-sched-core` —
boa's own declared dependencies — cannot load, and **`boa-setup check` reports
"dependencies are installed" on a box where `modprobe sch_netem` fails**. Every
driver and every firmware blob is the host's, so driver behaviour measured here
is Ubuntu's, not OpenWrt's.

OpenWrt's own documentation is blunt: *"the OpenWrt runtime uses multiple active
services to work and isn't really suited as a container"*, and the rootfs is for
*"special cases like CI testing"*. That was earned seven times while building
this, each one silent:

| | What happened |
|---|---|
| `wifi-scripts` absent | netifd logged "Wireless module not found" and never made an AP |
| `wpad` not enabled | no hostapd log at all — reads as nothing happening |
| `ujail` | hostapd never started; radios `up:false`, netdevs torn down |
| `config_generate` | overwrote the network config written before procd started |
| `br_netfilter` | clients associated, got a lease, and no traffic passed |
| `bridge_empty` | netifd would not create a bridge with no ports yet |
| cleared apk cache | `apk add` blamed a missing dependency for a missing index |

The first five are fixed in the image. The last two are fixed here.

## Use

```sh
# base image, once
curl -sS -O https://downloads.openwrt.org/releases/25.12.5/targets/x86/64/openwrt-25.12.5-x86-64-rootfs.tar.gz
docker import openwrt-25.12.5-x86-64-rootfs.tar.gz openwrt-userspace:25.12.5

docker build -t openwrt-dev:25.12.5 .
docker run -d --name openwrt-dev --privileged --hostname openwrt-dev \
    -p 8080:8080 -p 18081:80 openwrt-dev:25.12.5

# hand over the hardware; re-run after every start
sudo SCAN_PHY=phy0 ./attach.sh
```

Then install boa the usual way (`apk add --repository …`), configure the radio,
and **re-run the scanner block of `attach.sh` after `wifi up`** — see the note
in the script.

`--privileged` is required: procd, ubus and an unsandboxed hostapd all need it.
Combined with the disabled `ujail`, this container is appropriate for a
development machine on a trusted network and nowhere else.

## Known rough edges

- Nothing installs boa into the image; the package is built locally and would
  go stale if baked in.
- `wifi up` destroys a scanner interface created before it.
- The LuCI Services → boa tab points at port 8080 on the browser's own
  hostname, so the published port must be 8080 or the tab is blank (#367).
