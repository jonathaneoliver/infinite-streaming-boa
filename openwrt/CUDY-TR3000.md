# OpenWrt and boa on the Cudy TR3000

Measured on a single unit, 2026-09-22 and 2026-09-23. Every command and every
number below came off that box.

A pocket travel router running OpenWrt 25.12.5 does things the Raspberry Pi
appliance cannot: it announces a channel change so clients follow it without
dropping (3 of 3 did), honours a live transmit-power setting, scans while it
serves, and carries 745 Mbit/s of Wi-Fi traffic through its bridge. The
difference is the silicon — an MT7981B built for access points rather than a
client chip pressed into service.

## The box, and what it took

A Cudy TR3000 v1 running stock firmware became an OpenWrt appliance carrying
real traffic in an afternoon.

| | |
| --- | --- |
| Model | Cudy TR3000 v1 (`cudy,tr3000-v1`) |
| SoC | MediaTek MT7981B, 2 x ARM Cortex-A53 |
| Memory / overlay | 486 MB RAM, 44 MB writable overlay (UBI) |
| OpenWrt | 25.12.5 r33051-f5dae5ece4, `mediatek/filogic`, kernel 6.12.94 |
| Radios | MT7981 built-in: `phy0` 2.4 GHz, `phy1` 5 GHz, both `mt798x-wmac` on one device path |
| Ports | `eth0` 2.5 GbE (uplink), `eth1` 1 GbE (LAN) |
| Package arch | `aarch64_cortex-a53` |

**Flashing.** Stock to OpenWrt goes through an intermediate image, then
`sysupgrade`. A unit with the newer F50L1G41LC flash (serial from week 2543 on)
needs OpenWrt 24.10.5 or later or it will not boot. 25.12 was chosen over 24.10
for a second reason: it uses `apk`, which is what the boa feed publishes.

**The package.** The published feed was Pi-only (`aarch64_cortex-a76`), so
nothing there would install. The daemon itself is a static Go binary and
architecture-agnostic; only the package label was wrong. Built against the
Filogic SDK:

```sh
SDK_IMAGE=openwrt/sdk:mediatek-filogic-25.12.5 ./scripts/openwrt-package.sh
```

The CI workflow now publishes both feeds from one release, so `$DISTRIB_ARCH`
picks the right one on either box.

**Two prerequisites that are not optional.** The device must be a *transparent
bridge*, not a router: boa shapes each client's uplink by its own address, and
behind NAT every client leaves wearing the router's. And it needs the full
`wpad-mbedtls` rather than the default `wpad-basic-mbedtls`, with 802.11k and
BSS transition management switched on per AP — without them steer answers
`UNKNOWN COMMAND` and measure is refused outright. Both are covered in detail in
[`README.md`](README.md).

`boa-setup check` then walks every prerequisite read-only and prints each as OK,
WARN or FAIL. On the converted Cudy: 0 failed.

## How to, start to finish

This is a record of what **one specific unit** took, not a general OpenWrt
guide. Every command below was run on it. That is the whole value of it — and
the whole of its limits.

> **Check your box against this one before you flash anything.** A wrong image
> on the wrong revision is the one mistake here that is not recoverable from the
> web interface.
>
> | | This unit |
> | --- | --- |
> | Board | Cudy TR3000 **v1** (`cudy,tr3000-v1`) |
> | NAND | **128 MB**. A 256 MB variant exists, takes **different images**, and has its own intermediary firmware |
> | Flash chip | **F50L1G41LC** — the newer part, fitted from serial week 2543 on; this one is week 2609 |
> | Stock firmware | **2.4.22**, Nov 2025 |
> | Flashed to | OpenWrt **25.12.5** r33051-f5dae5ece4, standard `squashfs-sysupgrade` (**not** `ubootmod`) |
>
> **Three ways this goes wrong on a box that is not this one:**
>
> - **The flash chip.** A unit with the F50L1G41LC needs OpenWrt **24.10.5 or
>   later**. Older images do not recognise the part and the box will not boot —
>   and you will not know which chip you have until it fails.
> - **The NAND size.** The 128 MB and 256 MB variants are not interchangeable.
>   The intermediary firmware differs too; the 256 MB one is a community build
>   discussed in its own OpenWrt forum thread.
> - **`ubootmod` versus standard.** Different flash layout, not a preference.
>   Pick one path and stay on it; this unit took the standard image.
>
> Cudy also ships new stock versions that change the upgrade page and what it
> will accept. If your stock version is not 2.4.22, expect step 2 to look
> different.

**If anything above does not match your box, use these rather than this page:**

- [The TR3000 device page](https://openwrt.org/toh/cudy/tr3000) — the authority
  on which images each revision takes, and the first thing to re-read when a
  detail here looks stale
- [Firmware selector](https://firmware-selector.openwrt.org/?target=mediatek%2Ffilogic&id=cudy_tr3000-v1)
  — current images for `cudy_tr3000-v1`
- [Installation overview](https://openwrt.org/docs/guide-user/installation/start)
  and [generic flashing](https://openwrt.org/docs/guide-user/installation/generic.flashing)
  — the procedure this record is one instance of
- [sysupgrade from the CLI](https://openwrt.org/docs/guide-user/installation/sysupgrade.cli)
  — what `-n` and `-T` actually do
- [Failsafe and factory reset](https://openwrt.org/docs/guide-user/troubleshooting/failsafe_and_factory_reset)
  — read this **before** you need it

### In the web interface (the way to do it)

Only two steps genuinely need a shell. Everything else is a page in LuCI, and
the labels below are this box's own LuCI 25.12 rather than a guess.

| # | Step | Where in LuCI |
| --- | --- | --- |
| 1 | Stock, first contact | Cable to a **LAN** port; stock answers at `http://192.168.10.1`. Set its admin password |
| 2 | Cudy's own OpenWrt build | Stock page → **Advanced Settings → System → Firmware**. Comes back as LuCI at `192.168.1.1` |
| 3 | Official OpenWrt | **System → Backup / Flash Firmware → Flash new firmware image**. Untick **Keep settings** — that is `sysupgrade -n`. It verifies and shows the checksum before writing, which is the `-T` check |
| 4 | Root password, SSH key | **System → Administration**: *Router Password*, then the **SSH-Keys** tab |
| 5 | Bridge the ports | **Network → Interfaces → Devices**, edit `br-lan`, add both ports to *Bridge ports* |
| 6 | Stop serving DHCP | **Network → Interfaces**, edit LAN: *Protocol* → **DHCP client**, then its **DHCP Server** tab → **DHCPv4 Service** → disabled (same for DHCPv6 and RA). Delete `wan` and `wan6` on the same page |
| 7 | Rescue address | **Network → Interfaces → Add new interface**: static, device `br-lan`, `192.168.1.1/24` |
| 8 | Swap wpad | **System → Software**: *Update lists*, remove `wpad-basic-mbedtls`, install `wpad-mbedtls` and `hostapd-utils`. Then **System → Startup** to enable and start `wpad` — the swap leaves it stopped |
| 9 | Radios and SSIDs | **Network → Wireless**: per radio, *Operating frequency* and *Country Code*; in the interface below, *ESSID*, *Encryption* (WPA2/WPA3 mixed), and **Network → lan**, which is the bridged-AP setting. Then **Enable** — radios ship disabled |
| 10 | 802.11k and v | Same SSID dialog → **WLAN roaming** tab → tick **802.11k RRM** and **BSS Transition** |
| 11 | Install boa | **System → Software** → *Install package* `luci-app-boa`, once the feed is configured |
| 12 | Point boa at the hardware | **Services → infinite-streaming-boa** — the settings page the package installs |

**The two that need a shell:** trusting the signing key
(`/etc/apk/keys/boa-packages.pem`) and adding the feed line to
`/etc/apk/repositories.d/customfeeds.list`. LuCI will not write either, and a
feed you cannot verify is one you should not add.

The flash itself is worth doing in LuCI rather than at a prompt: the same
verify-then-write, with the file picker doing the part that is easy to get
wrong.

### The same steps at a shell

What was actually run on this unit, and the only route for the two steps LuCI
cannot do.

> **These commands were run by Claude Code over SSH, not typed by a person.**
> Every one of them executed against this box during the session that produced
> this file -- but they are written up here as a clean sequence, and the session
> was not one. It had false starts, commands run in a different order, and
> several that were corrected after they failed. Nobody has since run this list
> top to bottom on a fresh box to confirm it works as a script.
>
> So read each line before you paste it, particularly the `uci delete` ones,
> which remove configuration and are the only steps here that lose something.
>
> **The LuCI table above was not walked end to end either.** Its labels were
> read out of this box's own LuCI files, and the browser was genuinely used for
> the two firmware flashes and for setting the root password and SSH key -- the
> rest of the configuration was done over SSH and then mapped to the page that
> sets the same option. It is offered as the better route for a person because
> it shows what it will do before doing it, not because it has been proved as a
> sequence.

**1. Stock, first contact.** Cable a machine to a **LAN** port, not WAN; stock
firmware serves its admin page only on LAN. It answers at
`http://192.168.10.1`. Set the admin password it asks for and note the stock
version (this unit: TR3000 V1.0, firmware 2.4.22, Nov 2025).

> **Give it an uplink before you start.** Plug the WAN port into your existing
> network at the same time as the LAN cable. The Cudy then has a route out, and
> a laptop that takes a lease from it reaches the internet through it — double
> NAT, which is harmless for setup and puts your traffic through the box you are
> about to work on.
>
> The trap only exists when that port is dark. macOS ranks a USB LAN above
> Wi-Fi, so the moment the Cudy hands out a lease it becomes the default route;
> with no upstream behind it, everything stops. If you cannot give it an uplink,
> reorder instead: System Settings → Network → ⋯ → **Set Service Order**, Wi-Fi
> above the USB LAN.
>
> One condition either way: the Cudy's own LAN subnet must differ from the
> network you plug its WAN into — stock serves `192.168.10.0/24` and OpenWrt
> `192.168.1.0/24`, so a home network on `192.168.1.x` is the one to watch.
> After step 4 the question disappears entirely: a bridged box routes nothing
> and hands out no leases.

**2. Cudy's own OpenWrt build, as the stepping stone.** Stock will not accept a
vanilla OpenWrt image. Cudy publishes an OpenWrt-based build on its
[TR3000 download page](https://www.cudy.com/en-us/pages/download-center/tr3000);
this unit took `TR3000 V1_20251118.zip`, containing
`cudy_tr3000-v1-sysupgrade_20251112_release.bin` (14.9 MB, identifies itself as
`23.05-SNAPSHOT-CUDY`, board `cudy,tr3000-v1`). Flash it from the stock page's
**Advanced Settings → System → Firmware** dialog. Two minutes, power steady. It
comes back as LuCI at **192.168.1.1**.

**3. Official OpenWrt 25.12.5.** Take
`openwrt-25.12.5-mediatek-filogic-cudy_tr3000-v1-squashfs-sysupgrade.bin` from
[the firmware selector](https://firmware-selector.openwrt.org/?target=mediatek%2Ffilogic&id=cudy_tr3000-v1).
Verify the device accepts it *before* writing:

```sh
sysupgrade -T /tmp/fw.bin     # verify only
sysupgrade -n /tmp/fw.bin     # -n = start with clean config
```

25.12 rather than 24.10 because it uses `apk`, which is what the boa feed
publishes. A unit with the newer F50L1G41LC flash (serial week 2543 on) needs
24.10.5 or later to boot at all. The standard image is enough; `ubootmod` only
buys overlay space. It returns at 192.168.1.1 with **no root password** — set
one and add an SSH key in LuCI before anything else.

**Before anything else, take the box off the open door it boots on.** In LuCI:
**System → Administration**, set the root password, and paste your public key
under **SSH-Keys**. Do it in the browser rather than at a shell —
`ssh root@192.168.1.1 'cat >> /etc/dropbear/authorized_keys'` needs a TTY for
the password prompt and fails in a non-interactive session. If 192.168.1.1
collides with another device in your `known_hosts`, use
`ssh -o UserKnownHostsFile=/dev/null` rather than deleting an entry that belongs
to something else.

**4. Make it a bridge, not a router.** boa shapes each client's uplink by that
client's own address, and behind NAT every client leaves wearing the router's.
So both ports join one bridge and the upstream router stays the only DHCP
server:

```sh
# eth0 = 2.5GbE uplink, eth1 = LAN
uci set network.@device[0].ports='eth0 eth1'
uci set network.lan.proto='dhcp'

# and dhcpv6, ra
uci set dhcp.lan.dhcpv4='disabled'
uci delete network.wan
uci delete network.wan6

# keep a way back in
uci set network.rescue=interface
uci set network.rescue.device='br-lan'
uci set network.rescue.proto='static'
uci set network.rescue.ipaddr='192.168.1.1/24'

uci commit && reload_config
```

The rescue address is not optional in practice: the box's DHCP lease moves, and
without it a box that comes back on an unexpected address is a box you have to
fetch a cable for.

**5. Radios: full wpad, with 802.11k/v on.** The default `wpad-basic-mbedtls`
has no BSS transition management, so steer answers `UNKNOWN COMMAND` and measure
is refused outright:

```sh
apk del wpad-basic-mbedtls && apk add wpad-mbedtls hostapd-utils
/etc/init.d/wpad enable && /etc/init.d/wpad start   # the swap leaves it stopped
uci set wireless.default_radio1.ieee80211k=1
uci set wireless.default_radio1.bss_transition=1    # repeat per wifi-iface
uci commit wireless && wifi reload
```

**Start wpad before `wifi reload`.** A reload with no hostapd running takes every
AP down and leaves them there.

Then the access points themselves — this is what "AP bridged mode" actually is:
every `wifi-iface` on the `lan` network, so the radios are ports of the same
bridge as the wired ones, with no routing or NAT anywhere.

```sh
# required per radio, or the AP will not start
uci set wireless.radio0.country='US'
uci set wireless.radio1.country='US'
uci set wireless.radio1.band='5g'
uci set wireless.radio1.htmode='HE80'

# THE bridged-AP line, per wifi-iface
uci set wireless.default_radio1.network='lan'
uci set wireless.default_radio1.ssid='cudy1263'
uci set wireless.default_radio1.encryption='sae-mixed'

# radios ship disabled
uci set wireless.default_radio1.disabled='0'

uci commit wireless && wifi reload
```

One SSID on both radios is deliberate: band steering, roaming and every 802.11v
experiment need a client that can choose between them without the user picking a
network. `sae-mixed` is WPA2/WPA3 together, which is what a mixed household of
clients needs.

**6. Install boa.** From the signed feed, where `$DISTRIB_ARCH` picks
`aarch64_cortex-a53`:

```sh
FEED=https://jonathaneoliver.github.io/infinite-streaming-boa/openwrt

wget -O /etc/apk/keys/boa-packages.pem $FEED/boa-packages.pem
. /etc/openwrt_release
echo $FEED/25.12/$DISTRIB_ARCH/packages.adb \
  >> /etc/apk/repositories.d/customfeeds.list
apk update && apk add luci-app-boa
```

Or build and push in one step from a clone:
`SDK_IMAGE=openwrt/sdk:mediatek-filogic-25.12.5 ./scripts/openwrt-package.sh root@<ip>`.

**7. Point boa at the hardware**, in `/etc/config/boa` — note `wan` is `eth0`
here, where the Pi uses `eth1`:

```
option bridge 'br-lan'
option wan    'eth0'
option scan   'phy0-scan phy3-scan'   # a list, space separated
option state  '/etc/infinite-streaming-boa/policies.json'
```

`service boa enable && service boa start`, then **`boa-setup check`** —
read-only, walks every prerequisite above and prints each as OK, WARN or FAIL
with the command that fixes it. This unit: 0 failed.

**8. Where the interface is.** boa serves its own page on port 8080, and
registers a LuCI entry:

- **`http://<box>:8080`** — the appliance interface: the adapter rack, band
  plans, per-client shaping, patterns and the event log. `https://<box>:8443` if
  you set `tls_cert` / `tls_key`.
- **LuCI → Services → infinite-streaming-boa** — the settings page from
  `luci-app-boa`. If the menu entry does not appear after install, LuCI has
  cached its index: `rm /tmp/luci-indexcache.*` and restart `rpcd`.
- `boactl devices`, `boactl probe -ssh` from a clone, for the same facts without
  a browser.

> **Caveat on step 6:** this box was installed from a local SDK build, not from
> the feed. The `aarch64_cortex-a53` packages are built and the workflow
> publishes them, but `apk add` straight from the Pages feed has not yet been run
> here — so treat those four lines as untested on this device, unlike everything
> above them.

## What AP-class silicon changes

Every radio the appliance had before was a client chip serving an access point.
The MT7981 was built to be one, and five things follow from that.

Measured means it was done on this box. Advertised means the driver says so and
nothing here has exercised it -- a distinction worth keeping, since this whole
document exists because `iw phy info` lists a channel switch on a radio that
refuses one.

| Capability | Cudy MT7981 | The Pi's radios |
| --- | --- | --- |
| 802.11h channel switch | **Measured** — clients stay associated | `brcmfmac` never had it; `mt7921u` answers `err=-95` |
| Transmit power, live | **Measured** — ~7 dB per step, nobody reassociated | `mt7921u` reports 3.00 dBm whatever you ask |
| Scan while beaconing | **Measured** — 13 BSSes in 3 s, AP stayed up | `brcmfmac` yes; `mt7921u` needs the BSS down |
| 802.11ax PHY | **Measured** — 2x2 HE, 1200.9 Mbit/s at 80 MHz | 2x2 ax, as a client part |
| DFS radar detection | **Advertised, UNTESTED** — ch 52–144 | Neither can serve there |
| Access points (BSSes) per radio | **Advertised, UNTESTED** — 16 SSIDs on one radio, 19 interfaces total; one BSS is all this box has run | **One** |
| Hardware forwarding | **Present, unused** — WED and PPE blocks exist | None |

**"Access points per radio" is the driver's interface-combination limit**, and it
is what lets one radio carry several BSSes at once -- separate SSIDs, each with
its own BSSID, beacons, encryption and network binding, all time-sharing the one
channel:

```
phy1, built-in mt798x:  #{ AP, mesh point } <= 16, #{ managed } <= 19,
                        total <= 19, #channels <= 1
phy3, USB mt7921u:      #{ AP } <= 1, total <= 3, #channels <= 2
```

The use for it here is an experiment the Pi cannot run: two SSIDs off the SAME
radio with different settings -- 802.11k/v on one and off the other, WPA3 against
WPA2 -- compared with the radio, the channel and the room held constant. The
`managed <= 19` half is what lets a `<phy>-scan` interface exist alongside a
serving AP, which is what the scanner role uses.

**None of this has been tried on this box.** One BSS per radio is all it has ever
run; 16 is a number the driver publishes, and the rest of this document is a
sustained argument for not trusting those until they are exercised. Three things
to expect when someone does. Each BSS beacons independently, so overhead grows
with every SSID added. They all share one channel -- 16 access points, not 16
radios. And the second BSS is where per-SSID settings stop being free: they are
negotiated at association, so changing one rebuilds that BSS. The `mt7921u` is not worse in every respect either -- it reports
`#channels <= 2` and can be on two channels at once, which an access point has no
use for.

**"Scans while beaconing" needs a caveat, and it is the same one the channel
switch needs.** The radio has one tuner — `#channels <= 1` — so it does not
listen elsewhere and serve at once; it leaves its channel briefly and mac80211
buffers for the clients. Nobody is disconnected, but the air goes quiet. Pinging
a MacBook's link 5 times a second across a full both-band scan of the serving
5 GHz radio:

- **18 consecutive pings lost**, seq 26–43, a gap of 3.6 s against a 3 s scan
- then the backlog draining: 1861, 1659, 1454, 1250, 1049, 848, 643, 444, 244 ms
  over the next nine
- the association never broke, and `state=ENABLED` throughout

So it keeps its clients where the `mt7921u` would drop them, which is the real
advantage — but a survey is still a three-second hole in the traffic. That is
precisely why the appliance would rather turn a whole radio into a listen-only
instrument than survey from a serving one.

Two details that bite in the config rather than on the air. The regulatory
ceiling is **not** flat across the band — 24.0 dBm on 36–64, 28.0 dBm on 149 —
so the top of a power slider depends on the channel underneath it. And both
built-in radios sit on **one device path**, `platform/soc/18000000.wifi`, told
apart only by a `+1` suffix; matching on the path alone resolves `phy1` to
`radio0` and disables the wrong radio.

## Throughput, measured

The channel mattered four times more than anything else. Same radio, same
client, same −40 to −48 dBm, same 1200.9 Mbit/s PHY both ways: channel 36 gave
164 Mbit/s down, channel 149 gave 622.

All figures Mbit/s, iperf3 over 10 s, a MacBook Pro. *To* the box means iperf3
terminating on the Cudy; *through* means it forwarding to an Ubuntu machine
beyond the uplink.

| Link | To the box, down | To the box, up | Through, down | Through, up |
| --- | --- | --- | --- | --- |
| Wired, 2.5 GbE client into 1 GbE port | 852 | **932** | 853 | 929 |
| 5 GHz, ch 149 @ 80 MHz | 538, 622 | 779 | **669, 725, 745** | **884** |
| 5 GHz, ch 36 @ 80 MHz | 356, 183, 164 | 541, 544 | 253, 167 | 484 |
| 2.4 GHz, ch 1 @ 20 MHz | 7.6 | 10.9 | 4.5 | 11.4 |

**The channel is the whole story on 5 GHz.** The box's own listen-only radio had
already said why: channel 36 at 56% busy against channel 149 at 3%, from a sweep
that saw 17 access points and 64 clients. Moving there was worth roughly 4x on
the downlink — and the move itself was announced, so nobody dropped while it
happened.

**The wired numbers are the client's, not the box's.** 932 Mbit/s up is wire
speed on a 1 GbE port. The 852 down appears identically on both paths, which is
what makes it the MacBook's 2.5 GbE adapter receiving rather than anything the
Cudy did. Note the uplink negotiated at 1 Gbps: the 2.5 GbE port is only as fast
as what it is plugged into.

**2.4 GHz is not a slower 5 GHz, it is a different order of thing** — about 1%
of channel 149. Worth a caveat: the MacBook refused two polite steers, so all
three clients were gathered onto that one 20 MHz channel to take the reading.
`tx failed` equalled `tx retries` at 3788. It is a worst case, not a fair
2.4 GHz figure.

## Channel moves nobody notices

On the Pi, changing channel means taking the access point down and bringing it
back somewhere else; clients are told nothing and must notice the beacons
stopped, rescan and rejoin. The MT7981 counts the move down in its beacons
instead and the clients follow it, still associated.

| Run | Switches | Result |
| --- | --- | --- |
| First measurement pass | 8 | 24 client-crossings, **23 followed**; the one loss reassociated 4 s later and then rode six consecutive switches |
| Second pass, after the feature shipped | 6 | every one `method: announce`, `outage_sec: 0`; **3 of 3** clients followed the last, a MacBook, an iPhone and a Watch together |
| Forced teardown, same radio | 3 | 1.0 s and 1.1 s out of service — and once **84.7 s**, when the AP came back with no BSS and had to be rebuilt |

That last row is why the mechanism is now *reported* rather than inferred from a
number: the outage a restart costs is not a constant.

**Which radios can do it cannot be asked, only attempted.** `iw phy info` lists
`channel_switch` on the `mt7921u` — which refuses every form of it. The
capability bit returns the opposite of the truth. So the box tries the
announcement, falls back to the teardown inside the same request, and caches
what it learned against the *driver*.

The trap in that cache is worth stating, because getting it wrong condemns
working hardware. A `FAIL` means "this driver refuses" **or** "this particular
target is not switchable" — a cross-band move being the everyday example — and
the two are indistinguishable at the moment it arrives. So a refusal is recorded
against the driver only when the failed attempt was an ordinary in-band move
*and* the fallback then landed on exactly that channel. That combination proves
the target was legal, leaving the driver as the only explanation. Forcing a
teardown by hand teaches the cache nothing, which was verified on the box: the
driver still read `announces: true` afterwards.

The box also counts **who** followed, which hostapd will not tell you. Ten
seconds after a switch it compares the station dump with the one taken before:
still there with its connected time intact means it rode through, a reset means
it noticed and rejoined, absent means it never came back.

## Power, distance and roaming

Turning a radio down moves every client's received signal without reassociating
anyone, which makes distance something the appliance can impose rather than
model. On the built-in radios the setting is honoured; on the USB `mt7921u` it
is accepted and discarded.

| Set on the radio | Radio reports | MacBook receives |
| --- | --- | --- |
| 23 dBm | 23.00 | −38 dBm |
| 10 dBm | 10.00 | −48 dBm |
| 3 dBm | 3.00 | −55 dBm |
| back to 23 dBm | 23.00 | −38 dBm |

Connected time rose straight through all four and no ping was lost — the path is
nl80211 to mac80211 to the driver, and hostapd is not on it, so nothing is torn
down.

Two details the driver insists on. It is the **phy**, not the interface:
`iw dev phy1-ap0 set txpower fixed 1000` is accepted, reports success and
changes nothing. And it takes whole dBm only — `fixed 50`, `250` and `270` are
each refused with `Not supported (-95)` while `0` and `100` are taken. Zero is
the floor and is not off: 0 dBm is 1 mW and the radio reports having taken it.

**Then the roaming.** An iPhone one room away, walked down and back up in 2 dB
steps:

- It **left** 5 GHz for the 2.4 GHz AP at **11 dBm** (−57 dBm, 25.8 Mbit/s once
  there)
- It **returned** at **19 dBm** (−72 dBm, 576 Mbit/s)

That is **8 dB of hysteresis**, so leaving and returning are two different
measurements rather than one threshold. It kept streaming across both moves —
13.2 Mbit/s downlink while on 2.4 GHz. No ban and no steer: every move was the
phone's own decision. The AP-side signal stayed near −73 dBm throughout, because
that is the *uplink*, and turning the AP's power down does not touch what the AP
hears.

## Steering, and a radio that only listens

**802.11v asks; it does not tell.** The same frame carries escalating promises,
and the interface offers all four as one control with a mode rather than four
buttons. Measured against a MacBook, an iPhone and a Watch:

| Mode | What the frame carries | What the clients did |
| --- | --- | --- |
| `steer` | A plain transition request | All stayed. Mac `status_code=1`, iPhone `status_code=7` |
| `warn` | Disassociation Imminent, no timer | hostapd never disassociated anyone (a Mac held 16 s). Two of three left on their own — and not to the AP named |
| `term` | BSS Termination Included | The AP stayed `ENABLED`. The one client that left went to 2.4 GHz, not where it was sent |
| `force` | Disassociation Imminent with a timer | Moves the client. It picks where it lands |

None of them writes a deny-list entry — a steer that banned someone would be an
eviction wearing a request's name. During this write-up the MacBook refused two
polite steers and only moved when the alternatives were removed, which is the
honest summary of the whole feature: a request is a result, not a command.

**A radio can be turned into an instrument from the interface.** Any radio can
be made listen-only: its access point stops, a managed `<phy>-scan` interface
goes up in its place, and it sweeps continuously so no serving radio has to
leave its channel. That is where the channel colouring comes from — 17 access
points and 64 clients seen, channel 36 at 56% busy against channel 149 at 3%,
which is the reading that made the throughput difference above predictable
rather than lucky.

It also found two bugs that only appear on hardware, both in the way *back*: the
toggle could not resolve its radio through netifd once the AP was down, and
making a second radio listen-only silently un-made the first, because the config
option holding them is a list that was being written as a single value.

## What still does not work

| Limitation | Why |
| --- | --- |
| No silent power cut | OpenWrt's kernel has no rfkill — `/dev/rfkill` is absent. That is the only control that tells a client nothing, which is how you measure what a player does while it still believes it is connected. Transmit power covers much of it here: fading to 0 dBm is silent too, and stepped rather than binary |
| No advertised BSS Load | `bss_load_test` exists only in hostapd builds with testing options |
| USB `mt7921u` stays client-class | It refuses the channel switch, ignores transmit power, and cannot scan without dropping its BSS. On this box it is the contrast, not the workhorse |
| DFS untested | The silicon advertises radar detection on 52–144, and 16 non-DFS channels are all the band plan currently offers. Whether it will actually serve there after a channel-availability check is unanswered |
| 160 MHz untested | The radio advertises `Supported Channel Width: 160 MHz` and `HE160/5GHz`, and nothing here has run at that width. Everything measured was 80 MHz |
| Multiple BSSes untested | The radio advertises 16 access points on one phy. This box has only ever run one per radio, so the airtime cost of a second SSID, and whether per-SSID 802.11k/v settings behave independently, are both unanswered |
| No millisecond figure for a switch | "Kept the association" is not "lost no packets". A ping held across a switch would give the number; it has not been run |
| Feed not yet exercised | The `aarch64_cortex-a53` packages are built and the workflow publishes them, but `apk add` straight from the Pages feed has not been done on this box — it was installed from a local build |
