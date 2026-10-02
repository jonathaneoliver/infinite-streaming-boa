# boa

[![Release](https://img.shields.io/github/v/release/jonathaneoliver/infinite-streaming-boa?sort=semver)](https://github.com/jonathaneoliver/infinite-streaming-boa/releases/latest)
[![License: MIT](https://img.shields.io/github/license/jonathaneoliver/infinite-streaming-boa)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/jonathaneoliver/infinite-streaming-boa?filename=daemon%2Fgo.mod)](daemon/go.mod)
[![Platform: OpenWrt](https://img.shields.io/badge/platform-OpenWrt%2025.12-00b5e2)](openwrt/README.md)
[![Sponsor](https://img.shields.io/badge/support%20this%20project-ea4aaa?logo=githubsponsors&logoColor=white)](https://github.com/sponsors/jonathaneoliver)

**The simplest hardware setup for the boa bench appliance: one Cudy TR3000
router running OpenWrt.** For every other way to run it — a Raspberry Pi, a
Linux container, an OpenWrt VM — see the [full README](README-FULL.md).

boa conditions each client's connection independently — rate, latency, jitter
and loss, per device and per direction — and **drives the radios those clients
are associated to**: move a channel under them, turn the power down, take an
access point away, ask a client to roam and record whether it did. On a
[Cudy TR3000][cudy] running OpenWrt it is two packages and a four-question setup
page, on radios that were built to be access points.

Part of the infinite-streaming family. The repository is
`infinite-streaming-boa`; `boa` is the appliance — the binary, the hostname, the
package. The name is apt: a boa constricts and releases, and the box does the
same to a link, tightening the cap and easing it off over time.
[`PRD.md`](PRD.md) is the product behaviour source of truth.

![boa on a Cudy TR3000: the valley pattern steps an iPhone's downlink cap from
10.25 Mbps down to 0.54 Mbps while the iPhone's own screen, inset, shows its
player dropping from 2160p to 360p without stalling](docs/images/cudy-demo-teaser.gif)

The valley pattern at 16×, from the demo below: the dashed line is the cap boa
enforces, the solid line the iPhone's real throughput, and the inset the
iPhone's own screen, playing from infinite-streaming. The full 2:47 demo,
every action a click in the web UI and narrated (turn the sound on), also tries `warn`, `term` and `evict` on
the radio the iPhone is using:

https://github.com/user-attachments/assets/1a4c5d6f-a886-4edd-95c0-68f7552a30d1

```
                 [ your existing router ]
                            │
                          eth0  2.5 GbE uplink    ← uplink shaping here;
                   ┌─────────────────┐              downlink on each client's port
                   │  Cudy TR3000    │
                   │  OpenWrt + boa  │
                   └─────────────────┘
                br-lan  (one layer-2 segment)
          ╱           │            │            ╲
     phy1-ap0     phy0-ap0       eth1        USB 3 port
    Wi-Fi 5 GHz  Wi-Fi 2.4 GHz  1 GbE LAN   2.5 GbE adapter (optional)
          ╲           │            │            ╱
                every client conditioned alike
```

## This page is the Cudy; there are four other ways to run it

**This page is about one box**, because of everything tried it is the simplest
to set up and the one whose radios do the most. The same daemon and the same
interface run on four other targets, each with its own reason to exist:

| Target | What it is | Choose it for |
|---|---|---|
| **A Cudy TR3000** — this page | Two packages on a pocket router with access-point radios | Channel moves that drop nobody, distance imposed rather than modelled, and a box that travels |
| [A Raspberry Pi 5, from an image](README-FULL.md#1-a-raspberry-pi-5-from-an-image) | One card, nothing installed elsewhere | A dedicated bench box that also runs ntopng and glances |
| [A Linux host, as a container](README-FULL.md#2-a-linux-host-from-a-container) | Hardware you already have, nothing to flash | The fastest start, and PCIe slots for better radios |
| [Another OpenWrt device, as packages](README-FULL.md#3-an-openwrt-device-as-packages) | The same packages on any OpenWrt router or Pi | A box that should stay an OpenWrt router |
| [OpenWrt in a VM](README-FULL.md#5-openwrt-in-a-vm-with-an-ap-class-card-passed-through) | A KVM guest with an access-point card passed through | AP radios in a PC you already own, reset to pristine in a minute |

**[`README-FULL.md`](README-FULL.md) is the full reference**: every target,
every measurement and how they compare, the hardware notes for the Pi and the
container, and why client-class radios cannot do what this one does.

## Contents

- [Why you would want it](#why-you-would-want-it)
- [What it does](#what-it-does)
- [Why the Cudy](#why-the-cudy) — and what it will not do
- [How this compares to what already exists](#how-this-compares-to-what-already-exists) — and what it replaces
- [Setting one up](#setting-one-up) — check the unit, flash, install, four questions, and finding it afterwards
- [Where to plug it in](#where-to-plug-it-in)
- [Measured on the Cudy](#measured-on-the-cudy) — throughput, cap accuracy, channel moves, power and roaming, steering, latency
- [Driving it from a script](#driving-it-from-a-script)
- [Things that will mislead you](#things-that-will-mislead-you)
- [What does not work yet](#what-does-not-work-yet)
- [Security](#security)
- [Working on it](#working-on-it) — development, layout, how it was built
- [Further reading](#further-reading)

## Why you would want it

**For anyone building a mobile app or a Wi-Fi-connected device** who needs to
know how the client's relationship with the access point affects it, not only
how much bandwidth it gets.

A real client is continuously deciding which access point to be on, whether to
roam, and what to do when the one it is using stops answering. Those decisions
surface as a stall, a re-buffer, a dropped upload, a silent switch to cellular,
or a device that never comes back — and none of them reproduce by lowering a
rate limit. The awkward part is that a device's worst behaviour tends to appear
in a customer's house six weeks later: a roam mid-stream, an access point that
vanishes, a band that gets crowded at 8pm. A bench with one router and good
signal never produces them. This box produces them on demand, on a schedule,
and records what it did.

**For a device rather than an app the case is stronger.** The Wi-Fi behaviour
*is* the product — how fast a streaming stick reconnects, whether a speaker
honours a transition request — and a fix for a device's Wi-Fi has to travel over
that device's Wi-Fi, so the units that need it most are the least able to
receive it. And you usually cannot instrument the thing at all: a TV, console,
set-top box or camera takes no proxy, no certificate and no test harness.

**Neither end has to cooperate.** boa is a transparent bridge, not a router, so
devices keep their normal addresses on your normal network and the box never
appears in `traceroute`. It conditions forwarded frames, so any client talking
to any server over TCP, UDP or QUIC is conditioned alike — including
destinations you do not control. A proxy can do none of that; the
[full reference](README-FULL.md#why-a-proxy-is-a-different-instrument) explains why the
difference is structural.

**It tests the telemetry, not just the product.** Video QoE systems collect
Wi-Fi data alongside playback — signal, roams, disconnects — to attribute a
re-buffer to the network. That telemetry is almost never tested, because a real
network will not produce a known sequence of Wi-Fi events. boa will:

```
  what boa did                          what the QoE report says
  ────────────────────────────────      ──────────────────────────────
  t+30s   steer to the other radio  →   roam recorded? same second?
  t+90s   disable AP for 8s         →   disconnect recorded, or a gap?
  t+150s  deauth                    →   counted once, or as three?
  t+210s  turn the radio down 20 dB →   signal drop reflected at all?
```

The box keeps its own event log with millisecond timestamps — ground truth
recorded by the thing that *caused* the events, to diff a report against.

**What it is not.** A bench tool for one engineer at a time on a network they
control: no login, no users, not shared lab infrastructure, not a certified
instrument ([Non-Goals](PRD.md#3-non-goals)). It conditions the link, never the
content — returning a 500 or rewriting a manifest belongs on the origin path,
which is what the infinite-streamer harness in this family is for. And it tells
traffic apart by address, port and protocol, never by application: it sees
ciphertext.

## What it does

boa conditions two things, per client and independently:

- **The network** — what happens to the packets: rate, latency, jitter, loss,
  reordering and corruption.
- **The Wi-Fi link** — what happens to the connection: deauthentication, dead
  zones, steering between radios, channel moves, transmit power. A phone's path
  monitor and a player's throughput estimator react to these in ways no amount
  of packet loss triggers.

**The network, per device and per direction**

- **A rate cap, latency, jitter, loss (uniform or bursty), reordering and
  corruption**, live from a web page, IPv4 and IPv6. Caps are
  [measured accurate](#cap-accuracy) from 0.1 to 500 Mbps on this box.
  Eight presets as starting points — fibre, cable, 4G good and weak, 3G,
  satellite, lossy — each a plausible whole link rather than one number.
- **Patterns, not just fixed caps.** A per-client timeline walks the
  conditioning through a scripted sequence — a rate ladder, a loss burst, an
  outage at a chosen second — so you watch what a player does *through* a
  transition. `valley` steps a cap down a rendition ladder; `walkabout` walks a
  device away from the router and back over 15 minutes.
- **Finds the breaking point.** A sweep walks a streaming device up a ladder of
  caps and records the rung where the player stopped keeping up.
- **Sub-classes** condition one destination network, port or protocol
  differently from the rest of a device's traffic.

**The radios, which is where the Cudy earns its place**

- **Moves a channel under the clients, and they stay.** The Cudy's radios
  announce the switch in their beacons (802.11h) and associated clients follow
  it — measured 3 of 3, together. A band plan shows every channel coloured by
  how busy a listening radio found it; pick a cell to pick a channel and width.
- **Turns the power down live, so distance is imposed rather than modelled.**
  23 → 10 → 3 dBm moved a MacBook's received signal about 7 dB a step with
  nobody reassociated and no ping lost. The client's own roaming logic then
  responds to a real signal — which a simulated distance cannot make it do.
- **Moves clients between radios, with honest promises.** One SSID on every
  radio, so a client can be pushed around the box the way a building pushes it:

  | control | what it does | can the client refuse? |
  |---|---|---|
  | **steer** | 802.11v BSS transition request | **yes** — and whether it does is the measurement |
  | **warn** | the same, saying disassociation is imminent (it is not) | **yes** |
  | **term** | the same, saying the AP is shutting down (it is not) | **yes** |
  | **force** | the same with a 5 s deadline, then disassociates | no, but it picks where it lands |
  | **gather** | denies it everywhere but the destination, then moves it | no |
  | **evict** | denies it on the radio being emptied, then moves it | no, but it picks where it lands |

- **Takes an access point down and back**, silently by default (a router losing
  power tells nobody) or with a deauthentication on the way.
- **Deauth, disassoc and timed deadzones** per client, as buttons or as a lane
  in a pattern.
- **Makes the radio itself worse** — a generation ladder (802.11ac, n, legacy
  OFDM), power-save profiles, RTS/CTS and fragmentation thresholds.
- **Turns any radio into a listen-only scanner** that sweeps both bands while
  the others serve. That is where the band plan's colours come from, and it is
  what predicted a 2.7x throughput difference between two channels before
  anything moved.
- **Asks the client what it sees** with an 802.11k beacon request — the only
  reading taken from the client's side of the link. (No client tested has
  answered one yet; a decline is reported as the result it is.)

**Seeing what happened**

- **Charts per device**, with five minutes of history kept on the box and the
  enforced cap drawn against real throughput.
- **The whole box's traffic**, from the kernel's counters, and a routing view of
  which device sent to which — including traffic that never left the room.
- **An event log** of what the radios were asked to do and what clients said
  back, streamable as NDJSON.
- **Names devices from mDNS**, so the list reads as devices rather than MACs.
- **An iperf3 server on the box**, so a link can be measured without installing
  anything on the device under test.
- **A LuCI page** under Services, alongside everything OpenWrt already does.

**What it looks like**

![An iPhone's client card on the Cudy: the valley pattern has stepped its
downlink cap from 10.25 to 1.82 Mbps, and the throughput follows each
step](docs/images/cudy-client.png)

One client streaming, five minutes of history, averaged over 10 s. The blue
trace is real downlink throughput; the dashed `cap` line is what boa is
enforcing. The `valley` pattern has stepped the cap down rung by rung, from
10.25 to 1.82 Mbps so far, and the player has followed it down — which is the
thing worth watching. The row underneath is the pattern playing, 149 s into
420.

![The traffic panel: stacked download and upload for the whole box above a pair
of Sankey diagrams showing which device sent to which](docs/images/cudy-traffic-routing.png)

The whole box at once: every port's throughput from the kernel's counters, and
beneath it who sent to whom. The two fat ribbons are a MacBook on the wired
USB port and a Mac mini on Wi-Fi sending each other about 101 Mbit/s each way
— traffic that never left the room, which interface counters alone cannot
show.

![The whole page on the Cudy: the activity log, the traffic panel by client,
the adapter rack listing every client on each radio, and the client
list](docs/images/cudy-interface.png)

The whole page: the activity log at the top, the traffic panel, the adapter
rack with each radio's controls and every client on it, and the clients.

## Why the Cudy

Every other radio this project has run is a **client chip** with access-point
mode bolted on. The TR3000's built-in MT7981 (`mt798x`) radios are
**access-point silicon**, and the difference is measured, not claimed:

| | Cudy's built-in radios | Client-class radios (the Pi's `mt7921u`) |
|---|---|---|
| Channel move | **Announced** — clients follow, still associated | Refused; the AP is torn down and everyone rejoins |
| Transmit power, live | **Honoured**, ~7 dB per step, nobody dropped | Accepted, then ignored: reports 3.00 dBm whatever you ask |
| Scan while serving | **Keeps its clients** (costs ~3 s of quiet air) | Must take the access point down |
| Access points per radio | Driver allows 16 (one exercised) | One |

And the rest of it suits the job:

- **Two ethernet ports** — 2.5 GbE and 1 GbE — so it bridges with no USB NIC,
  no powered hub and no PSU current cap, which were the largest source of
  flakiness in the Pi build. A third, wired client port is one USB adapter
  away: an RTL8156 in the USB 3 port joins the bridge on its own when plugged
  in (measured 2026-09-30; `boa-setup install-drivers` adds its driver).
- **One mains-powered box that fits in a pocket**, replacing a Pi, two dongles,
  a hub and a power supply. It goes where the network under test is.
- **Already an OpenWrt target** (`mediatek/filogic`), so there is no image to
  build and no SD card to write. LuCI stays; boa installs beside it.
- **It costs less than the USB adapters it replaces.**

**The trade: it is a router, not a computer.** Two Cortex-A53 cores, 485 MB of
RAM and a 44 MB writable overlay (31.6 MB free). boa fits — a 10.9 MB binary
holding 18 MB resident, idling around 0.3 load — but little else will. ntopng
and glances, which the Pi image carries, are absent here, and the CPU was 65%
busy carrying 745 Mbit/s through the bridge. If the box also has to host other
tools, the [Pi or the container](README-FULL.md#five-ways-to-run-it) is the better
fit.

| | |
| --- | --- |
| Model | Cudy TR3000 v1 (`cudy,tr3000-v1`), 128 MB NAND |
| SoC | MediaTek MT7981B, 2 x Cortex-A53 |
| Radios | `phy0` 2.4 GHz, `phy1` 5 GHz, 2x2 802.11ax, up to 1200.9 Mbit/s PHY at 80 MHz |
| Ports | `eth0` 2.5 GbE (uplink), `eth1` 1 GbE (LAN) |
| OpenWrt | 25.12.5, `mediatek/filogic`, packages `aarch64_cortex-a53` |

## How this compares to what already exists

Deliberately degrading a link is a well-worn idea, and most tools do it with the
same kernel machinery boa does. What differs is **where the impairment sits** —
and therefore what has to cooperate for it to work.

| | Runs where | Reaches a TV, console or set-top box | Per device |
|---|---|---|---|
| **boa** | a transparent bridge in the path | yes | yes, per MAC |
| [Network Link Conditioner](https://nshipster.com/network-link-conditioner/) | on the Mac or iPhone under test | no — macOS and iOS only | it *is* the device |
| `tc` / `netem` by hand | a Linux router you assemble | only via that router | you write the filters |
| [Charles](https://www.charlesproxy.com/) / Proxyman throttling | a proxy the device is pointed at | only if it honours a proxy and a custom CA | per proxied device |
| [Toxiproxy](https://github.com/Shopify/toxiproxy) | between an application and its backend | no | per proxy |
| [pfSense / OPNsense limiters](https://docs.netgate.com/pfsense/en/latest/trafficshaper/limiters.html) | your router | yes, once re-homed behind it | per IP |
| [Facebook ATC](https://github.com/facebookarchive/augmented-traffic-control) | your gateway | yes, once re-homed behind it | per IP; archived 2018 |
| Netropy and similar rack emulators | an appliance in the path | yes | per emulated link; thousands of dollars |

Every other entry asks for cooperation of some kind — software on the device, a
proxy setting, a trusted certificate, or a new subnet and DHCP server the device
has to be moved behind. boa asks for nothing: cable it in, and a device keeps
its address, its lease, its mDNS discovery and its view of the network.

**The sharper divide is the radio.** Almost every tool above impairs the
*packets* crossing a link; none touches the link itself. Commercial emulators
sell a "Wi-Fi profile", but that is a canned bandwidth-and-loss curve on an
Ethernet port, with no association and nothing to steer. Nothing found, free or
paid, offers 802.11v steering, eviction or a channel move as a *test
instrument* — a lever pulled at a device you do not own, to watch what it does.

**Where the others are better.** A rack emulator is calibrated, repeatable and
certified; boa is none of those ([Non-Goals](PRD.md#3-non-goals)). Over Wi-Fi
its conditioning is added to a shared, variable radio baseline. For a
per-application policy on one device, use a proxy — it composes with this. And
for a Mac you already control, Network Link Conditioner is free and takes
thirty seconds.

[The full comparison](README-FULL.md#how-this-compares-to-what-already-exists)
dates every entry, covers WANem, piem, WiFry, znail, Octobox and RaspAP, and
explains [why a proxy is a different instrument](README-FULL.md#why-a-proxy-is-a-different-instrument).

### Testing a weak link: what this replaces

There are three ways to put a device on a weak link, and the Cudy is the one
boa target that offers two of them:

| | Attenuator + RF enclosure | Real transmit power (the Cudy) | boa's distance model |
|---|---|---|---|
| Cost | thousands | the box | already in the box |
| Calibrated | yes, in dB | roughly | **no** — indicative only |
| Isolated from other networks | yes | no | no |
| Device usable while testing | not really | yes | yes |
| Degrades both directions | yes | **downlink only** | yes, modelled |
| **Changes what the radio experiences** | **yes** | **yes** | **no** |

**That last row is the one that matters.** The distance model applies the
*consequences* of a weak signal — lower rate, more delay, corruption, uplink
first — but anything the device decides from its own measurement of the signal
is untouched: roaming triggers, band choice, rate adaptation. On client-class
radios the model is all there is. On the Cudy, turning the power down moves the
signal the client actually hears, so its roaming logic responds for real — that
is how the iPhone's 8 dB of hysteresis [below](#power-distance-and-roaming) was
found. Use both: power for the radio's own decisions, the model for the uplink
asymmetry power cannot produce.

**And boa can force the roam an attenuator can only wait for.** `steer`,
`gather`, `evict` and deadzones move a client directly, so the roam *outcome*
can be exercised even when the trigger is not.

## Setting one up

About an afternoon the first time, most of it flashing. Once OpenWrt is on it,
factory reset to a bridged box serving on three radios took **2 m 47 s**
(measured 2026-09-28).

### 1. Check your unit before you flash anything

A wrong image on the wrong revision is the one mistake here the web interface
cannot undo. The unit this was done on:

| | This unit |
| --- | --- |
| Board | Cudy TR3000 **v1** |
| NAND | **128 MB** — a 256 MB variant takes different images |
| Flash chip | **F50L1G41LC** (serial week 2543 on) — needs OpenWrt **24.10.5 or later** or it will not boot |
| Stock firmware | 2.4.22, Nov 2025 |
| Flashed to | OpenWrt **25.12.5**, standard `squashfs-sysupgrade` (not `ubootmod`) |

If yours differs, the [OpenWrt TR3000 device page](https://openwrt.org/toh/cudy/tr3000)
is the authority. Read [failsafe and factory reset](https://openwrt.org/docs/guide-user/troubleshooting/failsafe_and_factory_reset)
*before* you need it.

### 2. Flash OpenWrt

Cable a machine to the **LAN** port, and plug the WAN port into your existing
network so the box has an uplink throughout (the setup page installs packages).

| # | Step | Where |
| --- | --- | --- |
| 1 | Stock firmware, first contact | `http://192.168.10.1`, set its admin password |
| 2 | Cudy's own OpenWrt build, as a stepping stone — stock will not accept vanilla OpenWrt | [Cudy's download page](https://www.cudy.com/en-us/pages/download-center/tr3000) → stock **Advanced Settings → System → Firmware** |
| 3 | Official OpenWrt 25.12 | [Firmware selector](https://firmware-selector.openwrt.org/?target=mediatek%2Ffilogic&id=cudy_tr3000-v1) → LuCI at `192.168.1.1` → **System → Backup / Flash Firmware**, **Keep settings** unticked |
| 4 | Close the open door | LuCI → **System → Administration**: root password and SSH key |

25.12 rather than 24.10 because it uses `apk`, which is what the boa feed
publishes.

### 3. Install boa

Two lines LuCI will not write — trusting the signing key and adding the feed —
then the install:

```sh
wget -O /etc/apk/keys/boa-packages.pem \
  https://jonathaneoliver.github.io/infinite-streaming-boa/openwrt/boa-packages.pem
. /etc/openwrt_release          # DISTRIB_ARCH is aarch64_cortex-a53 here
echo https://jonathaneoliver.github.io/infinite-streaming-boa/openwrt/25.12/$DISTRIB_ARCH/packages.adb \
  >> /etc/apk/repositories.d/customfeeds.list
apk update && apk add luci-app-boa
```

> The unit here was installed from a local build of the same packages
> (`SDK_IMAGE=openwrt/sdk:mediatek-filogic-25.12.5 ./scripts/openwrt-boa-build.sh`,
> then `./scripts/openwrt-boa-install.sh root@<ip>`). `apk add` straight from
> the published feed has not yet been run on a Cudy.

### 4. Answer four questions

Open `http://192.168.1.1/` from a **wired** port. It now shows boa's setup page
instead of LuCI.

![The landing page on a freshly reset Cudy: This device has not been set up
yet, running with factory settings, no root password and its radios switched
off, with a button reading Open the setup page](docs/images/cudy-wizard-1-landing.png)

There is no login on the way in: a box with no root password is open to anyone
who can reach it, which is what the card says and what the last answer fixes.

![The Settings step: Drivers already ticked, network name cudy1263, a masked
passphrase twice, country US, a masked root password twice, and Make this a
transparent bridge ticked](docs/images/cudy-wizard-2-settings.png)

Network name, passphrase, country, root password — then Apply. **Drivers is
already ticked**, because both radios are built into the MT7981, so there is
nothing to install and no reboot. The name is suggested from the board and the
LAN MAC; use your existing network's name if you want devices already on it to
rejoin by themselves.

Applying installs the full `wpad` (the default one cannot steer or measure) and
mDNS, turns 802.11k/v on, enables both radios, plans a channel for each, sets
the root password, and converts the box into a **transparent bridge**: both
ports and every radio on one bridge, the upstream router the only DHCP server,
and `192.168.1.1` kept as a rescue address. On the Cudy the whole run took
seconds — over within 20 s of pressing Apply, countdown included.

![The Done step: cudy1263 is serving on 2 access points, the root password is
set, the device is now a transparent bridge whose address came from the
upstream router, and it is now at openwrt-cudy1263.local, with buttons Go to
boa, Go to OpenWrt and Change these settings](docs/images/cudy-wizard-3-done.png)

**It ends by naming where the box went**, because bridging changes its address:
it is now `openwrt-<ssid>.local`, with an address from your router. Unless you
press **Stay here**, the page opens boa by itself after 15 seconds.

The bridge is not optional: boa shapes each client's uplink by that client's
own address, and behind NAT every client would leave wearing the router's.

Captured on a Cudy TR3000 from a factory reset, 2026-10-01.
[`openwrt/CUDY-TR3000.md`](openwrt/CUDY-TR3000.md#what-step-6-looks-like-on-this-box)
has the run in more detail, and
[`openwrt/README.md`](openwrt/README.md#set-a-device-up-from-a-browser-the-first-run-wizard)
the unattended equivalent.

### 5. Use it

- **`http://<box>:8080`** — the appliance: adapter rack, band plans, per-client
  shaping, patterns and the event log. (`:8443` over https.)
- **LuCI → Services → infinite-streaming-boa** — the same page inside LuCI.
- **`boa-setup check`** on the box — walks every prerequisite read-only and
  prints OK, WARN or FAIL with the fix. The unit here: 0 failed.

![boa inside LuCI on the Cudy: the Services -> infinite-streaming-boa page
framing boa's interface, both in the light theme](docs/images/cudy-luci.png)

Every step, as run on that unit, at a shell as well as in LuCI — and how to put
the box back to out-of-box from a workstation — is in
[`openwrt/CUDY-TR3000.md`](openwrt/CUDY-TR3000.md#how-to-start-to-finish).

**Save your setup before a factory reset.** **export config** in the header, or
`GET /api/config`, holds every device's policy, sub-classes, ladders and
patterns; importing it validates the whole document before writing anything.
It is also how a scenario travels to someone else's box.

### When you cannot find the box

Bridging hands the box a new address from your upstream router, which is the
moment people lose it. In order of what to try:

| Route | Works when |
|---|---|
| `http://openwrt-<ssid>.local:8080/` | Your machine resolves mDNS — macOS does; Linux needs avahi and `nss-mdns`; a VPN often swallows it |
| Your router's lease table | Always — look for the box's hostname or MAC |
| `http://192.168.1.1/` | The rescue address the setup page leaves on the bridge. Put yourself on that subnet first: `sudo ifconfig en10 alias 192.168.1.2 netmask 255.255.255.0` on macOS, where `en10` faces the box |
| IPv6 link-local | Always, on a direct cable: `ping6 ff02::1%en10`, then `ssh root@fe80::…%en10`. It survives the DHCP address changing |

The rescue address collides with any other OpenWrt box at its default, so if
`ssh` complains about a changed host key, use
`-o UserKnownHostsFile=/dev/null` rather than deleting another device's entry.
[Reaching the box](README-FULL.md#reaching-the-box) has the rest, including the
browser syntax for a link-local address.

## Where to plug it in

**Cable the uplink (`eth0`) to your own network — a home router or a lab VLAN,
never a corporate LAN.** A transparent bridge putting many MACs onto one switch
port is indistinguishable, to enterprise network security, from what that
security exists to stop: port security err-disables the port, and a wireless IDS
sees a rogue AP.

**And do not run the radio controls inside a building with wireless IPS
containment.** Containment works by sending deauthentication frames at a rogue
AP's clients — uninvited, untimed, the same impairment this box produces on
purpose, and indistinguishable from it in a run.

**Busy air corrupts measurements.** Airtime is shared, so one near-idle 802.11n
client moved a measured downlink between 356 and 717 Mbit/s while sending 4 KB
of its own. What helps:

- Use the **wired LAN port** when the radio is not the subject — it repeats to
  within 1%.
- **Let the scanner pick the channel.** On this box channel 36 was 56% busy and
  149 was 3%, and that was worth 2.7x (below). Prefer 149–165 at 80 MHz.
- **Treat 2.4 GHz as unusable** for measurement in an office.
- **Capture the event log alongside every run**, so a drop you did not cause is
  at least visible as one you did not cause.

## Measured on the Cudy

> One Cudy TR3000 v1, OpenWrt 25.12.5, 2026-09-22 and 23 unless stated. Client
> a MacBook Pro, with an iPhone and an Apple Watch where named. iperf3 over
> 10 s. *To* the box means iperf3 terminating on the Cudy; *through* means
> forwarded to a host beyond the uplink. Full method and caveats in
> [`openwrt/CUDY-TR3000.md`](openwrt/CUDY-TR3000.md).

### Throughput

| Link | To the box, down | To the box, up | Through, down | Through, up |
| --- | --- | --- | --- | --- |
| Wired, into the 1 GbE port | 852 | **932** | 853 | 929 |
| 5 GHz, ch 149 @ 80 MHz | 538, 622 | 779 | **669, 725, 745** | **884** |
| 5 GHz, ch 36 @ 80 MHz | 356, 183, 164 | 541, 544 | 253, 167 | 484 |
| 2.4 GHz, ch 1 @ 20 MHz | 7.6 | 10.9 | 4.5 | 11.4 |

All Mbit/s. The wired figures are wire speed on a 1 GbE port. The 2.4 GHz row
is a worst case — three clients gathered onto one 20 MHz channel — not a
ceiling.

**The channel is the whole story on 5 GHz.** A controlled sweep, one run each,
every move announced so the sweep dropped nobody:

| Channel | Width | Down | Up | Client signal | PHY tx/rx |
| --- | --- | --- | --- | --- | --- |
| 149 | 80 MHz | **576** | **643** | −44 dBm | 1200.9 / 1080.6 |
| 149 | 40 MHz | 428 | 234 | −43 dBm | 573.5 / 300.0 |
| 149 | 20 MHz | 227 | 117 | −43 dBm | 286.7 / 144.4 |
| 36 | 80 MHz | 210 | 192 | −50 dBm | 1080.6 / 300.0 |
| 36 | 20 MHz | 161 | 96 | −52 dBm | 286.7 / 144.4 |

Same radio, same client, same width: 576 on channel 149 against 210 on 36. The
box's own listen-only radio had already said why — 3% busy against 56%, from a
sweep that saw 17 access points and 64 clients.

**AP-class silicon is not faster at carrying bytes.** 576 Mbit/s down at 80 MHz
is the same ballpark as the Pi's USB adapter. What it buys is what it will do
*while* carrying them — announce a move, honour a power setting, survey the
band — none of which appears in a throughput table.
[Access point performance](README-FULL.md#access-point-performance) compares the
targets.

### Cap accuracy

> boa `main` at #484, 2026-09-30. A MacBook on an RTL8156 2.5 GbE adapter in the
> Cudy's USB 3 port, and on the 5 GHz radio at channel 149, through the box to a
> host beyond the uplink, each direction in turn. **Shaper** is the netem
> qdisc's own counter of whole frames; **goodput** is iperf3.

| Cap (Mbps) | Shaper | Goodput | RTT under load |
|---|---|---|---|
| 0.1–0.4 | exact, ±0.3 % | −4.7 % down; −6 to −20 % up | 860 ms falling to 230 ms |
| 0.6–25 | exact, ±0.1 % | −3.5 to −5.5 % | 143–178 ms |
| 40–100 | exact | −4.3 to −5.5 % | 207–393 ms down, 350–875 ms up |
| 150–500 | exact, ≤0.4 % (Wi-Fi 500: −1.5 %) | −4.3 to −5.3 % | 14–233 ms |

**Every cap from 0.1 to 500 Mbps lands where it should, in both directions,
wired and over Wi-Fi.** Goodput sits about 4.4 % under the cap because a
1514-byte frame carries 1448 bytes of TCP payload — the same framing a real link
of that speed costs. A cap that read a clean 50.0 Mbps at the application would
mean the emulation was wrong.

**Below 30 Mbps latency is the ~200 ms the queue is sized for**, because boa
turns GRO off there (see [things that will mislead you](#things-that-will-mislead-you)).
From 30 Mbps GRO stays on to keep the box's forwarding speed, and the queue
then holds merged packets, which is why the 40–100 row runs above design. At the
very bottom, a 10-packet queue floor and TCP's own acknowledgements dominate.

**Above 500 Mbps the box, not the cap, sets the limit**: uncapped through the
USB adapter it carried 896–936 Mbps, the 1 GbE uplink port's ceiling. The full
per-cap table is in
[`docs/DATA-CONTRACT.md`](docs/DATA-CONTRACT.md#re-measured-after-484-the-cudy-01500-mbps-2026-09-30).

### Channel moves nobody notices

| Run | Switches | Result |
| --- | --- | --- |
| First pass | 8 | 24 client-crossings, **23 followed**; the one loss rejoined 4 s later and then rode six more |
| After the feature shipped | 6 | every one `method: announce`, `outage_sec: 0`; **3 of 3** followed the last — MacBook, iPhone and Watch together |
| Forced teardown, same radio | 3 | 1.0 s and 1.1 s out of service — and once **84.7 s**, when the AP came back with no BSS |

**Seamless for the association is not seamless for the traffic.** Pinging a
client at 100 Hz across five announced moves, the gaps were 555, 1185, 967, 855
and 395 ms — against a single 20 ms gap in 1000 pings with no move. A forced
restart cost the client 4.0 and 23.2 s. So an announced move is half a second
against several, and keeps the association, but "no outage" is the access
point's point of view, not the client's. A second of nothing is a buffer event
for a video player.

**The capability cannot be asked, only attempted.** `iw phy info` advertises a
channel switch on the `mt7921u`, which refuses every one. So boa tries the
announcement, falls back to a teardown inside the same request, reports which
happened, and counts who followed by comparing station dumps before and after.

### Power, distance and roaming

| Set on the radio | Radio reports | MacBook receives |
| --- | --- | --- |
| 23 dBm | 23.00 | −38 dBm |
| 10 dBm | 10.00 | −48 dBm |
| 3 dBm | 3.00 | −55 dBm |
| back to 23 dBm | 23.00 | −38 dBm |

Connected time rose straight through, and no ping was lost: the setting goes to
the phy and never through hostapd, so nothing is torn down.

**Then the roaming.** An iPhone one room away, with the 5 GHz radio walked down
and back up in 2 dB steps, **left for 2.4 GHz at 11 dBm and returned at 19** —
8 dB of hysteresis, so leaving and returning are two measurements, not one
threshold. It kept streaming across both moves, and every move was the phone's
own decision. Nobody could have guessed those numbers; that is why imposing
distance beats modelling it.

One asymmetry to know: turning the *access point* down weakens the downlink
only. The client still transmits at full power, whereas at a real distance the
smaller radio's uplink fails first. For that, boa also has a
[distance model](README-FULL.md#distance-and-what-kind-of-device-is-at-that-distance)
that degrades both directions — modelled, and labelled as such.

### Steering: ask, alarm, then insist

Measured 2026-09-23 against a MacBook, an iPhone and a Watch:

| Mode | Did the client move? | To the radio named? |
| --- | --- | --- |
| `steer` | **No.** Declined every time | — |
| `warn` | **Yes, once escalated.** The same MacBook that had just refused answered `status_code=0` | **Yes** |
| `term` | **Yes** | **Yes** |
| `force` | **Always** — put off the radio and rejoins | Its own choice |

Three requests to one MacBook, three seconds apart: the plain steer was
refused, and the identical request saying disassociation was imminent was
accepted. Nothing about the radios changed — only the sentence did. So the
useful control is the ladder, sent one mode at a time to one client.

**A steer honoured is not a client that stays.** An iPhone moved to 2.4 GHz in
3 s and went back to 5 GHz on its own 23 s later. Anything measuring "did it
honour the steer?" has to sample within seconds; placement that must persist
needs `gather`, which removes the alternatives instead of asking.

### What scanning while serving costs

A full both-band scan from the serving 5 GHz radio, pinging a MacBook 5 times a
second: **18 consecutive pings lost** (3.6 s against a 3 s scan), then a backlog
draining over 1.9 s — and the association never broke. It keeps its clients
where a client-class radio would drop them, but a survey is still a hole in the
traffic. That is why boa would rather make a whole radio listen-only than
survey from a serving one.

### What the box costs in latency

With nothing configured, `ping` at 10 Hz, one MacBook to one Ubuntu host, in ms:

| Path | min | avg | max |
| --- | --- | --- | --- |
| Wired, direct to the switch | 0.301 | 0.561 | 1.233 |
| Wired, through the Cudy | 0.404 | 0.974 | **8.177** |
| Wi-Fi via boa's AP | **2.273** | 11.543 | 90.583 |
| Wi-Fi via the house router | **2.464** | 12.430 | 95.010 |

On the wire the bridge adds about 0.1 ms at the floor; what it really adds is
jitter, up to 8 ms, so treat any configured delay below about 10 ms with care.
Over the air none of it is visible: Wi-Fi's own floor is 2.3 ms and its tail
reaches 90 ms on both access points equally. boa's AP was marginally *faster*
at the floor than the house router while serving a worse link. The
[full reference](README-FULL.md#what-the-box-itself-costs-in-latency) has the direction
asymmetry and what is and is not established about that tail.

### Measuring it yourself

iperf3 runs on the box. From a Mac joined to its SSID:

```sh
BOX=openwrt-<ssid>.local
iperf3 -c $BOX -B "$(ipconfig getifaddr en0)" -t 15 -f m -R   # downlink
iperf3 -c $BOX -B "$(ipconfig getifaddr en0)" -t 15 -f m      # uplink
```

`-B` forces the traffic out of the radio; without it a Mac also on ethernet
routes over the cable and reports a suspiciously excellent number. And note
which direction is shaped: an upload *to* the box stops at the bridge and never
reaches the uplink where uplink shaping lives, so it reports the raw link; a
download leaves by the client's own port, so it reports the cap.

## Driving it from a script

Everything the page does is an HTTP call to the same API, with no privileged
second interface. For anything you mean to compare, script it: a script makes
run A and run B identical except for the one line that differs.

`boactl` is the client — it imports the daemon's own types and fails loudly on
a bad response instead of decoding an error page into an empty answer:

```sh
cd daemon && go build -o ~/.local/bin/boactl ./cmd/boactl   # once, from a clone
export BOA_BOX=openwrt-<ssid>.local:8080
boactl devices && boactl bridge && boactl probe
boactl events -follow > run.ndjson        # the ground-truth event capture
```

The [full reference](README-FULL.md#driving-it-from-a-script-not-from-the-page) has a
full A/B example, and [`docs/API.md`](docs/API.md) every endpoint.

## Things that will mislead you

- **Airtime is shared.** Conditioning is added on top of a variable radio
  baseline; another client's traffic changes the achievable rate whatever the
  sliders say.
- **Delay is per direction.** 100 ms each way is about 200 ms round trip.
- **Below a 30 Mbps cap, boa turns GRO off.** GRO merges arriving frames into
  64 KB lumps that the shaper would release all at once — a 1 Mbps uplink ran
  at 9 s of latency with it and 164 ms without. It comes back on above the
  threshold, because the Cudy's uplink carried 925 Mbps with GRO and 574
  without. It is switched per port — the client's own port for an uplink cap,
  the WAN port for a downlink one — so a low downlink cap on one client turns
  merging off for everything arriving from the uplink. What that costs other
  clients' downlink has not been measured.
- **PHY rate is not throughput.** A link can negotiate 1200 Mbit/s and carry 2.
- **`outage_sec: 0` is the access point's view.** The client still sees a
  half-second gap on an announced move.
- **A phone's MAC is not stable.** Policy is keyed by MAC, and iOS and macOS
  rotate a private address per network. Turn **Private Wi-Fi Address** off (or
  **Fixed** on iOS 18) before a long measurement, or the device returns as a
  stranger with no policy.
- **Power is per phy, in whole dBm.** `iw dev phy1-ap0 set txpower` is accepted
  and does nothing; the regulatory ceiling also varies by channel — 24 dBm on
  36–64, 28 dBm on 149 — so the top of the slider depends on where the radio is.
- **Do not run `usteer` or `dawn` beside boa.** Both steer clients through the
  same hostapd object.

## What does not work yet

| Limitation | Why |
| --- | --- |
| No silent power cut control | OpenWrt's kernel has no rfkill. Transmit power down to 0 dBm covers much of the same ground |
| No advertised BSS Load | Needs a hostapd built with testing options; boa says so per radio instead of offering the control |
| No ntopng or glances | Not packaged for OpenWrt, and no room or CPU for them here |
| DFS, 160 MHz, several SSIDs per radio | Advertised by the silicon, **untested** |
| A USB `mt7921u` added to the box stays client-class | It refuses the channel switch and ignores power. On this box it is the contrast, not the workhorse |
| Restarting a wedged AP, restoring a radio profile | Paths that assume systemd or hostapd's command line; see [`openwrt/README.md`](openwrt/README.md#not-yet-working-on-openwrt) |

Candidate work lives in [GitHub issues](https://github.com/jonathaneoliver/infinite-streaming-boa/issues).

## Security

boa is a **bench appliance for a network you already control**, and its whole
security model is that one assumption.

- **No login on `:8080` or `:8443`**, and the API is reachable by anything on
  the bridge — including the devices under test, which can resolve the box by
  name. The LuCI page asks for LuCI's login; boa's own ports do not.
- **Cross-site requests are not blocked** yet
  ([#130](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/130)).
- **The Wi-Fi passphrase is the whole perimeter** on the radio side.
- **It is not a firewall.** A transparent bridge forwards everything.

The [full reference's Security section](README-FULL.md#security) has the rest.

## Working on it

The daemon is Go and the interface Vue 3 + TypeScript, compiled into one static
binary. The daemon builds and tests on macOS — Linux-only paths sit behind build
tags — so most work needs no hardware at all.

```sh
./scripts/dev.sh                        # the interface, hot-reloading, against synthetic clients
./scripts/dev.sh openwrt-<ssid>.local:8080   # the same, against a real box (read-write!)

SDK_IMAGE=openwrt/sdk:mediatek-filogic-25.12.5 ./scripts/openwrt-boa-build.sh
./scripts/openwrt-boa-install.sh root@<cudy>   # build, sign and install both packages, under a minute

cd daemon && go vet ./... && go test ./internal/boa/ -count=1
cd ui && npm run typecheck
```

Demo mode is the real daemon with synthetic clients, not a mock, so it cannot
drift from what a box sends. The OpenWrt SDK image is x86-64, so on an arm64
workstation Docker emulates it. Building needs `docker`;
[the requirements](README-FULL.md#requirements-for-the-build-and-control-host)
have the toolchain floors, and [Development](README-FULL.md#development) the
other targets' loops.

Before opening a PR, read [`CLAUDE.md`](CLAUDE.md) — the working conventions,
including the ones learned the hard way — and note that
[`docs/API.md`](docs/API.md) is generated from the source and
`docs/DATA-CONTRACT.md` records where every displayed number comes from.
Candidate work lives in
[GitHub issues](https://github.com/jonathaneoliver/infinite-streaming-boa/issues),
each with its own size and priority.

```
daemon/              Go daemon; embeds the compiled UI, ships as one binary
daemon/cmd/boactl/   terminal client for the API, and the probe assertions
ui/                  Vue 3 + TypeScript interface
openwrt/             the boa and luci-app-boa packages, boa-setup, the feed
PRD.md               product behaviour source of truth
docs/                API reference, data contract, licensing, accepted limits
```

### How this was built

boa is a reimagining of a link conditioner built by hand once before. The idea
is the same; the implementation is not. This version was written end to end
with [Claude Code](https://claude.com/claude-code) — the Go daemon, the Vue
interface, the packaging, the network plumbing, the docs and the tests.

## Further reading

| | |
| --- | --- |
| [`openwrt/CUDY-TR3000.md`](openwrt/CUDY-TR3000.md) | The full record of one unit: every command, every measurement, every caveat |
| [`openwrt/README.md`](openwrt/README.md) | boa on any OpenWrt device: prerequisites, the setup page, `boa-setup`, who owns the radios |
| [`README-FULL.md`](README-FULL.md) | The full reference: all five targets — Pi 5, container, OpenWrt, Cudy, OpenWrt VM — and how they compare |
| [`PRD.md`](PRD.md) | What the product does — the behaviour source of truth |
| [`docs/API.md`](docs/API.md) | Every HTTP endpoint and field |
| [`docs/DATA-CONTRACT.md`](docs/DATA-CONTRACT.md) | Where every displayed number comes from, and its units |

## Licence

MIT — see [LICENSE](LICENSE). Every dependency is permissive;
[`docs/LICENSING.md`](docs/LICENSING.md) records the audit, and why the project
ships build scripts and packages but never a built image.

[cudy]: https://www.amazon.com/dp/B0BXNCRVRC?tag=jonathaneoliv-20
