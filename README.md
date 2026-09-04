# boa

[![Release](https://img.shields.io/github/v/release/jonathaneoliver/infinite-streaming-boa?sort=semver)](https://github.com/jonathaneoliver/infinite-streaming-boa/releases/latest)
[![License: MIT](https://img.shields.io/github/license/jonathaneoliver/infinite-streaming-boa)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/jonathaneoliver/infinite-streaming-boa?filename=daemon%2Fgo.mod)](daemon/go.mod)
[![Platform: Raspberry Pi 5](https://img.shields.io/badge/platform-Raspberry%20Pi%205-c51a4a)](#build-an-image)

Part of the infinite-streaming family. The repository is
`infinite-streaming-boa`; `boa` is the appliance itself — the binary, the
hostname, the SSID. The name is apt: a boa constricts and releases, and the box
does the same to a link — tightening the cap and easing it off over time, most
visibly in the `valley` pattern below.

[`PRD.md`](PRD.md) is the product behaviour source of truth.

A Raspberry Pi that sits invisibly in your network and conditions each client's
internet connection independently — rate, latency, jitter and packet loss, per
device, in each direction, adjustable live from a web interface.

It is a **transparent bridge**, not a router. Devices under test keep their
normal addresses on your normal network, discovery protocols keep working, and
the Pi never appears as a hop in `traceroute`. Nothing being tested can tell it
is there.

What that buys, and it is the whole point: **neither end has to cooperate.**
Conditioning happens to forwarded frames, so the device under test gets no proxy
setting, no installed certificate and no software — and the far end gets nothing
either, because there is no endpoint to point it at. Any client talking to any
server on any provider is conditioned alike: a streaming service, a game server,
a firmware update and a DNS lookup at once, over TCP, UDP or QUIC, including
destinations you could never configure and do not control. A proxy can do none
of that, and the reasons are structural rather than a matter of features — see
[Why a proxy is a different instrument](#why-a-proxy-is-a-different-instrument).

The matching cost: traffic is told apart by destination network, port and
protocol — see [Sub-classes](#sub-classes) — and never by application or by
name. This box sees ciphertext, so two apps on one device, or two services
behind one CDN, look the same to it.

And it conditions the link, never the content. Returning an HTTP 500, stalling
or truncating a response, corrupting a segment, rewriting a manifest — all of
that lives above the transport and none of it happens here. That work belongs on
the origin path, which is what the infinite-streamer harness in this family
already is. The two compose rather than compete: degrade the link with boa,
manipulate what travels over it there.

```
                    [ your existing router ]
                              │
                            eth0                       ← WAN port, conditioning
                       ┌──────────────┐                  is applied here
                       │     boa     │
                       └──────────────┘
                         br-lan  (one layer-2 segment)
                    ╱          │          ╲
              wlan-usb       wlan0        lan0
             Wi-Fi 5GHz   Wi-Fi 2.4GHz  USB ethernet
                    ╲          │          ╱
                  wireless clients    wired client
                         ╲            ╱
                    conditioned identically
```

![The boa interface: an iPhone streaming while the valley pattern walks the
downlink cap down through a measured rendition ladder](docs/images/interface.png)

Above: one client streaming, five minutes of history. The blue trace is real
downlink throughput; the dashed `cap` line is what boa is enforcing. The
`valley` pattern is 315s into its 660s run, stepping the cap down rung by rung
— and the player is following it down, which is the thing worth watching. The
lane editor underneath is the pattern itself: 23 keyframes on a rate lane, with
delay, jitter and loss lanes unused in this run.

## What it does

- **Conditions each client independently** — rate, latency, jitter and loss, per
  device and per direction, live from a web interface. Both IPv4 and IPv6.
- **Drives the timeline, not just a fixed cap.** A per-client *pattern* walks the
  conditioning through a scripted sequence — a rate ladder, a loss burst, an
  outage at a chosen second — so you can watch what a player does *through* a
  transition, not only at steady state.
- **Conditions the Wi-Fi link itself, not only the packets** — drop
  (deauthenticate), nudge (disassociate) and a timed deadzone, per client, as
  one-shot buttons or as a lane on a pattern. A phone's path monitor and a
  player's throughput estimator react to the link going *down*, which netem
  cannot express. Needs the USB radio (hostapd); the onboard radio has no
  control interface, so the buttons only appear when it can act.
- **Stays invisible.** Clients keep their existing addresses on your existing
  subnet; the Pi is not a hop and does not appear in `traceroute`.
- **Names devices from mDNS**, so the list reads as devices rather than MACs.
- **Folds the list** when there are several, keeping a sparkline and current
  throughput per direction on each folded row.
- **Keeps five minutes of history server-side**, so a browser refresh does not
  start from a blank chart.
- **Measures a player's rendition ladder** by sweeping the cap downward and
  recording where throughput settles — no manifest, no payload inspection. Kept
  per service, because no two streaming services share a ladder.
- **Ships ntopng** on `:3000`, watching the bridge, with per-device deep links
  from each card for traffic breakdown and nDPI-labelled flows.
- **Ships glances** on `:61208`, linked from the header — the appliance
  watching itself rather than the traffic: CPU, memory, SoC temperature, disk
  and per-process load, for when a throughput number is wrong because the Pi is
  throttling rather than because the policy says so.
- **Ships an iperf3 server** on `:5201`, so the ceiling a cap has to sit under
  can be measured without installing anything on the device under test. It
  measures the link **unshaped** — see below.

## How this compares to what already exists

Deliberately degrading a link is a well-worn idea, and most of the tools below
do it with the same kernel machinery boa does. What differs is **where the
impairment sits** — and therefore what has to cooperate for it to work.

| | Runs where | Reaches a TV, console or set-top box | Per device | Cost |
|---|---|---|---|---|
| **boa** | a transparent bridge in the path | yes | yes | a Pi 5 |
| [Network Link Conditioner](https://nshipster.com/network-link-conditioner/) | on the Mac or iOS device under test | no — macOS and iOS only | it *is* the device | free with Xcode's Additional Tools |
| `tc` / `netem` by hand | a Linux host, or a router you assemble yourself | only via that router | you write the filters | free |
| [WANem](https://wanem.sourceforge.net/) | a Linux VM/live-CD you route traffic through | yes, as the gateway | via source/destination rules | free |
| [Toxiproxy](https://github.com/Shopify/toxiproxy) | between an application and its backend | no | per proxy, not per device | free |
| [Charles](https://www.charlesproxy.com/) / Proxyman throttling | a proxy the device is pointed at | only if it honours a proxy and a custom CA | per proxied device | commercial licence |
| [pfSense / OPNsense limiters](https://docs.netgate.com/pfsense/en/latest/trafficshaper/limiters.html) | your router | yes | yes, via source/destination-masked limiters | free, plus a box |
| [Facebook ATC](https://github.com/facebookarchive/augmented-traffic-control) | your gateway | yes | yes, per source IP | free; archived October 2018 |
| [Netropy](https://apposite-tech.com/products/netropy-network-emulation/) / Linktropy and similar | a rack appliance in the path | yes | per emulated WAN link | thousands to tens of thousands |

**What boa is actually for.** Every other entry in that table asks for
cooperation of some kind. Network Link Conditioner needs to run on the device,
which rules out anything you cannot install on, and conditions your debugging
tools along with the app. Toxiproxy needs the application pointed at it. Charles
needs the device to honour a proxy and trust an installed CA, which streaming
apps increasingly refuse. pfSense, a WANem appliance, and a hand-rolled `tc`
router all work — WANem is the closest kin, the same netem engine behind a web
interface — and all make the box a hop with its own subnet and its own DHCP,
which the device under test has to be re-homed behind. boa asks for nothing: cable it in, and a device keeps
its address, its DHCP lease, its mDNS discovery, and its view of the network.
That is the whole design, and the rest of this README is the consequences of it.

**ATC is the closest prior art, and the closest miss.** Facebook's Augmented
Traffic Control had the same goal and much of the same shape: condition real
devices, install nothing on them, and let a tester shape their own device from a
web page — "traffic can be shaped/unshaped using a web interface allowing any
devices with a web browser to use ATC without the need for a client
application" — across bandwidth, latency, packet loss, corruption and packet
ordering. The whole difference is one sentence of its README: "ATC must be
running on a device that routes the traffic and sees the real IP address of the
device, like your network gateway for instance." Routing the traffic is *how it
identifies a device*, and that is also its price — adopting it meant re-homing
the network under test behind it, with the new subnet, the new DHCP authority
and the changed addresses that implies. boa identifies a device by the MAC on a
frame it is already forwarding, which requires no address of its own and no
routing role. ATC was archived on 30 October 2018 and is read-only.

**Where the others are better.** A rack emulator is calibrated, repeatable and
certified; boa is explicitly none of those (see [Non-Goals](PRD.md#3-non-goals)).
Caps here are verified from 0.25 to 50 Mbps; above that a cap exceeds what the
radio itself can carry, so a high cap was measured only for the ~1.5 % overhead
of putting netem in the path (below), not as a calibrated rate. Loss is
deliberately not reproducible run to run. Over Wi-Fi the
conditioning is additive on top of a shared, variable radio baseline rather than
absolute — a wired emulator gives you a number you can put in a report, and boa
does not. If you need a per-application policy on one device rather than a
per-device one, Toxiproxy or a proxy is the right tool and composes with this
one. And if the device under test is a Mac you already control, Network Link
Conditioner is free and takes thirty seconds.

### Why a proxy is a different instrument

Charles and Toxiproxy are the two tools most often suggested in place of a box
like this, and both are good. Neither does what this one does, for reasons that
are structural rather than a matter of features.

**A proxy terminates the connection.** Toxiproxy is "a TCP proxy to simulate
network and system conditions": it accepts the client's connection and opens its
own to the upstream, so there are two TCP connections with two independent
congestion-control loops. Charles occupies the same position for HTTP. Either
way, the throughput a player measures, the round-trip time it estimates and the
retransmits it counts are formed against a local proxy socket and that proxy's
userspace buffer — not against a constrained link. boa shapes the frames it
forwards and terminates nothing, so the client's own congestion control meets
the path directly. For a player deciding which rendition to fetch next, that
difference is the entire measurement.

**A proxy conditions only what it proxies.** Toxiproxy is TCP only; Charles
carries HTTP and HTTPS. In both cases QUIC and HTTP/3 over UDP, DNS, discovery
and anything on a raw socket travel unimpaired alongside the throttled traffic —
so the device under test sees a network degraded in one protocol and pristine in
every other. That is not a coverage gap to be filled in later, it is a different
network from the one being simulated, and a player that quietly prefers QUIC is
not being tested at all. netem on a bridge holds no opinion about protocol.

**A proxy has to be adopted by the thing under test.** Toxiproxy's own example
is editing `Redis.new(port: 6380)` into `Redis.new(port: 22220)` — reasonable for
a service you own, impossible for a shipping app. Charles needs the device to
expose a proxy setting and honour it, and for HTTPS it needs its root
certificate trusted: "If you add the Charles CA Certificate to your trusted
certificates you will no longer see any warnings." A television or a console
often exposes no proxy field at all, and an app that pins its certificate
rejects an installed CA by design — which is the case for most streaming apps
worth testing. boa is addressed by MAC and asks the device for nothing.

**They are scoped along a different axis, and it is a real one.** Charles can
throttle selected hosts, and a Toxiproxy proxy is defined per upstream service;
both give per-application resolution on a machine you administer. boa gives
per-device resolution on a machine you cannot touch, and cannot tell two apps on
one device apart. If the question is "what is this app requesting, and what came
back", Charles answers it and boa cannot — this box sees ciphertext and flow
metadata. The tools compose. They do not substitute.

Toxiproxy is also deterministic where this box deliberately is not: an HTTP API
on `:8474` lets a test set up and tear down its own faults, and its toxics —
`latency`, `bandwidth`, `slicer`, `timeout`, `reset_peer`, `limit_data`,
`packet_loss` — reach failure modes that live above the link layer and that
netem cannot produce. For proving a service survives a flaky dependency in CI,
it is the right tool and this one is not.

## Hardware

What this box was built and measured on. Nothing here is required — it is a
Raspberry Pi 5 and a USB Wi-Fi adapter — but these are the exact parts behind
every number in this document.

| Part | What was used | Why it matters |
|---|---|---|
| Board | [Raspberry Pi 5 Model B, 4 GB](https://www.amazon.com/dp/B0CK3L9WD3?tag=jonathaneoliv-20) — or the cheaper [2 GB](https://www.amazon.com/dp/B0DDL91V2R?tag=jonathaneoliv-20), see below | A Pi 4 works; the onboard NIC must not be USB, which is why a Pi 3 does not — see the udev rule in `scripts/customize.sh` |
| Power | [Official Raspberry Pi 27 W USB-C PSU](https://www.amazon.com/dp/B0CW7XCY75?tag=jonathaneoliv-20) | A SuperSpeed Wi-Fi adapter is a real load. Check `vcgencmd get_throttled` reads `0x0` |
| Storage | [SanDisk Ultra 16 GB microSDHC](https://www.amazon.com/dp/B074B4P7KD?tag=jonathaneoliv-20) | What was used, and enough — the finished image is ~4.6 GB. A 32 GB card costs little more and leaves room for `ntopng` data |
| Wi-Fi adapter | [Panda Wireless PAU0F AXE3000 (mt7921u)](https://www.amazon.com/dp/B0D972VY9B?tag=jonathaneoliv-20) | Optional, and the single biggest change to what the box can test — see below |
| Wired downstream | Any USB ethernet adapter — e.g. [UGREEN USB-C 2.5 GbE](https://www.amazon.com/dp/B0CD1FDKT1?tag=jonathaneoliv-20); the figures below are a Realtek RTL8156 at both ends | Becomes `lan0`. Optional. 2.5 GbE needs a SuperSpeed link end to end, and a USB-C part reaches the Pi's USB-A socket through a converter that is usually the weak point — see [The cable decides whether you get 2.5 GbE at all](#the-cable-decides-whether-you-get-25-gbe-at-all) |

The product links above are Amazon affiliate links. **As an Amazon Associate I
earn from qualifying purchases.** No part was chosen for that reason — each one
is what the numbers in this document were measured on, and buying it anywhere
else works identically.

### How much RAM this actually needs

Measured on a running box: **762 MB used of 4 GB**, 3.2 GB available, swap
untouched, and the kernel's own `Committed_AS` estimate at 1156 MB.

| | RSS |
|---|---|
| ntopng | 386 MB |
| glances | 72 MB |
| NetworkManager | 21 MB |
| redis (for ntopng) | 17 MB |
| **boad** — the conditioner itself | **14 MB** |
| hostapd | 9 MB |

The appliance's own work is 14 MB. Over half the footprint is ntopng, and
another 72 MB is glances; both are optional to the conditioning and there only
for visibility. **A 2 GB Pi 5 has ample headroom**, and the 4 GB board this was
built on is not a requirement — it is what happened to be to hand.

An earlier reading of this same box put ntopng at 278 MB and the total at
539 MB. Nothing was done to it in between; it simply ran for longer, which is
the caveat below arriving on schedule rather than a separate measurement.

One caveat before buying the smaller board. Nothing bounds ntopng's growth:
its config sets no memory limit and redis runs with `maxmemory 0`. It holds
per-host and per-flow state, so any figure here is a floor measured on a quiet
segment, not a ceiling. On a busy network over days it will be larger. If you
run 2 GB and ntopng grows into it, the answer is a retention limit rather than a
bigger board — unbounded growth eventually fills a 16 GB card whatever the RAM.

glances has no such problem: it holds a short in-memory window and persists
nothing, so its 72 MB is flat and it writes nothing to the card. That last part
is worth having deliberately on an appliance that boots from SD.

There is **no 3 GB Pi 5**; that variant is a Pi 4. The Pi 5 ships in 2, 4, 8
and 16 GB.

## Access point performance

The AP's ceiling bounds the top of a measured ladder, so it decides which
renditions can be tested at all. Measured with `iperf3` **to the box**, which
means the link **unshaped** — the ceiling a cap must sit under, never evidence
that a cap is working.

| Radio | Downlink | Uplink | Channel | Run |
|---|---|---|---|---|
| Onboard (brcmfmac) | 55 Mbit/s | 57 Mbit/s | 20 MHz | 60s, sole client |
| PAU0F on **USB 2.0** | 162 Mbit/s | 146 Mbit/s | 80 MHz, 802.11ax | 15s, 2 clients |
| PAU0F on **USB 3.0** | **~540 Mbit/s** | ~156 Mbit/s | 80 MHz, 802.11ax | 15s, 2 clients |
| PAU0F on **USB 3.0** | **544–552 Mbit/s** | — | 80 MHz, 802.11ax, ch 40 | 20–30s, 2–3 clients, 2026-09-03 |
| Wired 1 GbE, for reference | 924 Mbit/s | — | — | 8s |
| Wired 2.5 GbE, for reference | **1.91 Gbit/s** | **2.35 Gbit/s** | — | 30s, 2026-09-03 |
| PAU0F on **USB 3.0** | **677 Mbit/s** | — | 80 MHz, 802.11ax, **ch 149** | 12s, sole client, 2026-09-04 |

### Channel width, and what it is worth

Measured 2026-09-04 on `wlan-usb` (mt7921u), one MacBook as the only client,
`iperf3` downlink, moving the same radio between widths on the same channel.
Run twice: once on **ch 40**, inside the UNII-1 block where six 80 MHz
neighbours sit at 18% measured airtime, and once on **ch 149**, where the scan
found 1.2%.

| Width | ch 149 (quiet) | ch 40 (busy) | What the busy channel cost |
|---|---|---|---|
| 20 MHz | **194 Mbit/s** | **110 Mbit/s** | −43% |
| 40 MHz | **378 Mbit/s** | 289 / 230 Mbit/s | −24 … −39% |
| 80 MHz | **677 Mbit/s** | **399 Mbit/s** | −41% |

**On quiet air, width scales almost exactly as the subcarrier counts predict.**
20 → 40 measured 1.95× against a theoretical 2.00, and 40 → 80 measured 1.79×
against 2.09. Doubling the channel really does roughly double the throughput.

**On busy air, the top step largely evaporates.** 40 → 80 bought only 1.38× on
ch 40 against 1.79× on ch 149, because a wider channel spans more interferers:
at 80 MHz on ch 40 the box overlaps all six of those neighbours, where at 40 MHz
it overlaps fewer. Widening into a crowded block gives back most of what it
gains, which is the argument for choosing the emptier block over the wider
channel when both are on offer.

The 289 / 230 pair at 40 MHz on ch 40 is not an error — it is two runs of the
same configuration, and the spread is what run-to-run variance looks like on a
contended channel. The quiet-channel figures repeated to within 2 Mbit/s
(677 and 679 across two runs).

### Predicting the best case from one number

The negotiated **PHY rate** — the `tx bitrate` a client reports, shown on its
row — predicts the best case to within a few percent, on a channel that is not
busy:

> **best case ≈ 85% of PHY, capped at ~680 Mbit/s**

| Width | PHY | 85% of PHY | Measured (ch 149) |
|---|---|---|---|
| 20 MHz | 229.4 | 195 | **194** |
| 40 MHz | 458.8 | 390 | **378** |
| 80 MHz | 1200.9 | 1021 → capped | **677** |

It is a **ceiling, not a forecast**, and the distinction is the whole of it: the
same PHY of 229.4 delivered 194 Mbit/s on ch 149 and 110 on ch 40. PHY cannot
see congestion. It says what the link could carry, never what it will — which
is also why removing an idle slow client above lifted the PHY 25% and moved
throughput 0.2%: that run was nowhere near its ceiling, so raising the ceiling
changed nothing.

For the realised figure it takes two numbers: PHY for the ceiling, and the
channel's **measured airtime** for how much of it survives. Congestion cost
24–43% here at every width.

### The ~550 Mbit/s figure was a congested channel, not a bus limit

An earlier revision of this section concluded that the adapter's ceiling "is in
the `mt7921u` USB transmit path". **That does not survive a quiet channel: the
same adapter measured 677 Mbit/s on ch 149.**

The reasoning that ruled out the CPU still holds — CPU0 was 54% idle with
softirq peaking at 27.8% during a Wi-Fi run. What was missed is that every one
of those runs was on **ch 40**, the box's default, which the scan now measures
at 18% airtime with six 80 MHz neighbours covering the whole UNII-1 block. The
~550 figure was reproducible because the congestion was constant, not because
the bus was the constraint.

Efficiency makes the same point. As a fraction of the negotiated PHY rate:

| Width | ch 149 | ch 40 |
|---|---|---|
| 20 MHz | 85% | 48% |
| 40 MHz | 82% | 63% |
| 80 MHz | 56% | 33% |

Only the 80 MHz quiet-channel case shows any sign of a limit that is not the
air — and it sits **above 677 Mbit/s**, not at 550. Where that ceiling actually
is has not been established; it needs a client that can pull harder than one
MacBook.

The first four rows are the same MacBook on the same afternoon; the dated rows
are later runs on the same box. The adapter on a SuperSpeed port is **~10x the
onboard radio downlink** — the difference between a ladder whose top rung means
"uncapped" and one that can be measured.

**The run column is not decoration.** The same onboard radio measured 39 Mbit/s
over 15s with three clients associated and 55 Mbit/s over 60s alone. Neither is
wrong; a figure here without its conditions is.

What costs you is not the NUMBER of associated clients but how *active* and how
*slow* they are: dropping from two clients to one moved downlink 54.4 -> 54.9,
because the second was idle. A single station linked at 65 Mbit/s, or one
actually transferring, is worth far more than a headcount.

**An idle slow client costs the PHY rate, not the throughput.** Removing that
same 65 Mbit/s 802.11n station while it was idle lifted the MacBook from
`HE-MCS 9` to `HE-MCS 11` (960.7 to 1200.9 Mbit/s) and cleared HT protection
(`num_sta_ht_20_mhz` 1 → 0) — and moved measured downlink from 551 to
552 Mbit/s. A 25% PHY increase bought 0.2%. Where airtime is the binding
constraint, as on the onboard radio at 55 Mbit/s, a slow client costs real
throughput; at ~550 Mbit/s on the USB adapter something else binds first. Both
are true, and which applies depends on where the bottleneck already sits.

That "something else" is not the Pi's CPU. Sampling per-core utilisation during
a Wi-Fi run leaves CPU0 at 54% idle with softirq peaking at 27.8%, against the
same core saturating at 1.6% idle on the wired path — the box pushes 3.5× more
traffic through that core over ethernet. It holds across 20s and 30s runs with zero
retransmits, which is what made it look like a hardware ceiling — but see
[the ~550 figure](#the-550-mbits-figure-was-a-congested-channel-not-a-bus-limit)
below: every one of those runs was on ch 40, and the same adapter reaches
677 Mbit/s on a quiet channel. The constant was the congestion, not the bus.

The daemon's own numbers agree with iperf3, which is worth knowing given how
much rests on them: sampling `station dump` counters during a run gave a mean of
55.2 Mbit/s against iperf3's 56.2 over the same interval. The chart will still
show a higher **peak** — 63 Mbit/s in that run — because it plots per-sample
throughput while iperf3 reports a whole-run mean. Compare against the chart's
`MEAN OVER` line, not the live trace.

Both adapter rows negotiated the same PHY rate (1200 Mbit/s, `HE-MCS 11
HE-NSS 2`) with the same clients. **The bus is the only difference.** Downlink
falls 3.3× on USB 2.0 while uplink barely moves, because uplink was already
limited by something other than the bus.

**Check the adapter got a SuperSpeed port.** A USB 3.0 adapter that is not
fully seated, or on a cable without SuperSpeed pins, enumerates as USB 2.0 in a
blue port and is otherwise indistinguishable — same channel, same 802.11ax,
same PHY rate, no error anywhere. The interface reports it in the header
(`radio: wlan-usb · USB 2` in amber), and from a shell:

```sh
lsusb -t                             # the adapter's line: 5000M good, 480M not
lsusb -v -d 0e8d:7961 | grep bcdUSB  # 3.20 good, 2.10 means High-Speed only
```

### Measuring it yourself

`iperf3` runs on the box already. From a Mac **joined to the box's SSID**:

```sh
# 1. What address did the Wi-Fi interface get? (en0 is usually Wi-Fi)
ipconfig getifaddr en0

# 2. Downlink -- the direction that matters for a player
iperf3 -c infinite-streaming-boa.local -B "$(ipconfig getifaddr en0)" -t 15 -f m -R

# 3. Uplink
iperf3 -c infinite-streaming-boa.local -B "$(ipconfig getifaddr en0)" -t 15 -f m
```

**`-B` is the part that is easy to miss.** If the Mac is also on ethernet, both
interfaces sit on the same subnet and macOS will route to the box over the
cable — reporting a wired figure while you believe you are testing Wi-Fi.
Binding the client to the Wi-Fi address forces the traffic out of the radio.
Without it, expect a suspiciously excellent number.

Two more things that will mislead you here:

- **Airtime is shared, so who else is associated changes the answer.** One
  802.11n client linked at 65 Mbit/s moved measured downlink between 356 and
  717 Mbit/s while transferring 4 KB of its own — it holds the channel roughly
  18× longer per byte than an 802.11ax client. Check `iw dev <iface> station
  dump` before trusting a number, and quote a range with conditions.
- **`txpower` on the mt7921u is inert, not merely misreported.** The adapter
  reports `3.00 dBm` whatever it is set to — a known driver bug with patches in
  flight upstream — but the *control* does not work either. Measured
  2026-09-04 against the phy's own 30 dBm ceiling on channel 149, with a client
  a few inches away:

  | `iw ... set txpower` | client-side RSSI | 8s downlink |
  |---|---|---|
  | `fixed 3000` (30 dBm, the ceiling) | −22 dBm | 612 Mbit/s |
  | `fixed 0` (0 dBm, the floor) | −22 dBm | 604 Mbit/s |
  | `fixed 3000` again | −22 dBm | 587 Mbit/s |

  A 30 dB request across the adapter's whole legal range moves the received
  signal by nothing at all. **So attenuation is not an available impairment on
  this box**: to test a weak link, move the device or put something in the way.
  See [Source Q](docs/DATA-CONTRACT.md) for the full method and the trap in it.

## Wired downstream performance

A USB ethernet adapter becomes `lan0` and is conditioned exactly like a wireless
client. With a 2.5 GbE adapter at both ends the cable stops being the limit and
the box's own transmit path becomes it.

| Direction | Command | Result | Limited by |
|---|---|---|---|
| Uplink, device → box | `iperf3 -c <pi> -B <addr>` | **2.35 Gbit/s** | ~94% of 2.5 GbE line rate |
| Downlink, box → device | `iperf3 -c <pi> -B <addr> -R` | **1.91 Gbit/s** | one saturated CPU core |

Realtek RTL8156 (`0bda:8156`) at both ends, direct cable, SuperSpeed both ends,
30s runs, 2026-09-03. Repeatable to within 1% across four runs.

**The box sends more slowly than it receives, and the asymmetry is structural.**
Per-core sampling during the downlink run shows CPU0 saturated — idle bottoming
at 1.6%, softirq peaking at 93.6% — while the other three cores sit 65–100%
idle. The uplink direction, where the box only receives, reaches line rate. Note
this is the opposite shape to Wi-Fi, where uplink is the weaker direction.

It cannot be tuned away. A USB NIC exposes a single rx/tx queue pair, and USB
transfer completions run on the core servicing the xHCI interrupt — CPU0 for
every USB device on the box:

```
131:  1436513  0  0  0   xhci-hcd:usb1
136:  8267195  0  0  0   xhci-hcd:usb3
```

RPS was tried and changes nothing here, for three compounding reasons: it steers
receive only and this is the transmit path; it hashes by flow, so one TCP
connection lands wholly on one core; and there is no second queue to steer to.
Measured 1.91 Gbit/s plain, 1.92 Gbit/s with `rps_cpus=e`, and 1.91 Gbit/s with
four parallel flows and RPS on. With four flows `NET_RX` did spread across all
four cores while `NET_TX` stayed on CPU0 — receive was never the constraint.

**What this means for a ladder.** Any rung above ~1.9 Gbit/s measured with `-R`
is measuring CPU0, not the shaper. That is the wired equivalent of mistaking a
PHY rate for throughput, and it fails the same way: a plausible number from the
wrong instrument.

### The cable decides whether you get 2.5 GbE at all

A 2.5 GbE adapter on a USB 2.0 link does not advertise 2.5 Gbit/s — it cannot
fit through a 480 Mbit/s bus — so it negotiates 1000 Mbit/s and looks like an
ordinary gigabit adapter. Both ends read `1000baseT`, nothing errors, and the
only trace is the enumeration speed.

**A USB-C adapter adds a converter to the path, and that is where SuperSpeed is
most easily lost.** The Pi's sockets are USB-A, so a USB-C NIC reaches them
through a C-to-A cable or a stubby C-to-A dongle — and most of those are USB 2.0
only, carrying four pins where SuperSpeed needs nine. A USB-A plug at least
advertises itself with a blue tongue; a USB-C plug looks identical either way,
so the only way to know is to plug it in and read the enumeration speed below.
SuperSpeed C-to-A cables exist and are cheap — the failure is reaching for
whichever converter was already in the drawer.

**Before blaming the cable, unplug the USB-C end, turn it over, and plug it back
in.** If the link comes up at 5000 Mbit/s one way up and 480 the other, the pins
were there all along. A C-to-A cable has only one SuperSpeed lane pair to give —
USB-A 3.0 has one TX and one RX pair, so there is no second set to fall back on
the way a C-to-C cable has — and the standard expects the device end to carry a
mux that routes SuperSpeed to whichever orientation the CC pins say is live.
Cheap parts omit that mux or wire it to one side only. It costs five seconds and
it separates a bad cable from a badly built one, which the enumeration speed
alone cannot. Not observed on this box; the failure here was a cable with no
SuperSpeed pins in either orientation.

The same adapter here was moved through two different USB 3.0 ports and
enumerated at 480 Mbit/s in both, so the port was never at fault; a cable change
fixed it. What separates a bad cable from a bad port:

```sh
cat /sys/bus/usb/devices/<dev>/speed   # 5000 good, 480 means High-Speed only
sudo ethtool lan0 | grep Speed         # 2500Mb/s once the bus is right
dmesg | grep -i "new .* USB device"    # "new SuperSpeed USB device" is the one you want
```

The **absence** of `Cannot enable. Maybe the USB cable is bad?` in `dmesg` is
itself the signal. The kernel logs that when a device attempts SuperSpeed and
fails to train. Nothing at all means the SuperSpeed pins were never present — a
USB 2.0 cable, not a marginal one.

`ethtool` is not trustworthy as a capability report here. On this adapter in
USB 2.0 mode it printed `Supported link modes: 10baseT/Half 10baseT/Full` while
simultaneously reporting `Speed: 1000Mb/s`. Trust the speed line and the USB
descriptor, not the mode table.

## Build an image

Needs `curl`, `docker`, `go` and `npm`. On macOS the Docker engine can come from
Rancher Desktop, Docker Desktop, colima or OrbStack — it exists only to supply a
Linux kernel, since macOS can neither mount ext4 nor loop-mount a partition
table. On Linux no container runtime is needed at all.

```sh
cp .env.example .env      # set your SSID, passphrase and country
./build.sh                # ~5 min first time, then cached
```

`build.sh` leaves a `.img` in `dist/`. Write it to a card with a proven imager —
**[Raspberry Pi Imager](https://www.raspberrypi.com/software/)** (choose *Use
custom* and select the file) or **[balenaEtcher](https://etcher.balena.io/)**.
Both verify the write, refuse your system disk, and run on macOS, Linux and
Windows.

A `./flash.sh` helper is included for macOS, but it is **not the recommended
path**: it is a raw `dd` write to a block device you name by hand, and one
mistyped identifier erases that disk in seconds. It has guards, but reach for an
imager instead unless you know exactly why you are not.

Cable the Pi's `eth0` to your existing network, optionally plug a USB ethernet
adapter in for a wired device under test, then:

| | |
|---|---|
| Web interface | `http://infinite-streaming-boa.local/` |
| ntopng | `http://infinite-streaming-boa.local:3000/` — no login |
| glances | `http://infinite-streaming-boa.local:61208/` — no login |
| iperf3 | `iperf3 -c infinite-streaming-boa.local` from a device under test |
| SSH | `ssh boa@infinite-streaming-boa.local` |
| Rescue | `http://<BOA_RESCUE_IP>/` when upstream DHCP is absent |

**The two directions measure different things.** `iperf3 -c <pi> -R` sends from
the box to the device, which is that device's downlink — conditioned by its
policy, so this is the cap being enforced. Without `-R` you are measuring upload
*to* the box, which terminates there and never reaches the WAN port where uplink
shaping lives; that reports what the link can do, not what the policy allows.
Verifying uplink needs load from a host beyond `eth0`.

Only the box's management ports — the interface, SSH, ntopng and glances — are
exempt from shaping, so a cap can never throttle the dashboard needed to undo
it.
Everything else the box sends is conditioned like any other traffic.

**Capture a run before the ring eats it.** The activity log is 500 events in
memory, cleared by every restart, and its own sizing note reckons a few hundred
covers "several minutes of a device flapping" — which is exactly what a drop or
bounce experiment is, times however many clients are in it. Stream it to a file
for the duration instead:

```sh
curl -sN http://infinite-streaming-boa.local/api/events/stream > run.ndjson
```

One JSON object per line, so `jq` reads it directly. Every event carries `at` as
unix milliseconds, timed by hostapd where it saw the transition rather than by
the poll that noticed it, so two clients reacting to the same stimulus can be
told apart. Marker lines (`{"marker":…}`) record the things a file cannot
otherwise show: the capture opening, a heartbeat while nothing happens, a daemon
restart, and any gap where the ring outran the reader.

The box is NTP-synced, so those timestamps line up with client-side logs. Two
cautions if you correlate at sub-second resolution: NTP is good to tens of
milliseconds, not better; and a device in a total outage loses NTP too, so record
each client's offset against the box at the start of a run rather than assuming
zero.

**Bind the test to the path you mean.** A laptop on both Wi-Fi and ethernet has
two addresses, and only the one boa lists as a client is conditioned by that
client's policy: `iperf3 -c <pi> -R -B <the address on that card>`.

**The WAN port must be connected.** Being invisible means boa issues no
addresses: with no live upstream, clients associate to the Wi-Fi and then sit
there without one.

The build bakes in everything the Pi needs. It never downloads anything on first
boot, so it works on an air-gapped bench.

**ntopng is optional and prebuilt.** It has to be compiled for arm64 — ntop ships
x86-64 binaries only, Docker Hub's image is amd64 only, and Debian dropped the
package after buster. `scripts/package-ntopng.sh` captures a source build into
`cache/`, which `build.sh` then grafts into every image, so a reflash costs
seconds rather than a recompile. Without that artifact the image simply builds
without it.

**glances comes from PyPI, not apt, and that is deliberate.** Debian ships it
as `+dfsg` with the webpack-built frontend removed — their packaging carries a
patch named `006_indicate_user_webserver_static_files_not_included` — so
`glances -w` from the `.deb` aborts at startup on a missing
`outputs/static/public` and serves nothing at all. The build therefore installs
a pinned upstream wheel into a venv at `/opt/glances`, and then asserts the
frontend is on disk before it enables the unit, because an install that
succeeds and still cannot serve is exactly how the `.deb` fails. Going through
a venv also keeps the Debian package's dependencies — matplotlib, tk, PIL,
fonttools, about 90 packages of desktop plotting stack — off a headless box.

## Configuration

Everything lives in `.env`; see `.env.example` for the full annotated list.

| Variable | Meaning |
|---|---|
| `AP_SSID`, `AP_PASSWORD` | The wireless network the Pi publishes |
| `AP_COUNTRY` | Regulatory domain. **The radio stays blocked until this is right** |
| `AP_BAND`, `AP_CHANNEL` | `bg` (2.4GHz) or `a` (5GHz); 5GHz AP mode is limited to the non-DFS channels 36/40/44/48 and 149/153/157/161/165 |
| `BOA_WAN_PORT` | The port cabled to your existing network. Conditioning is applied here |
| `BOA_RESCUE_IP` | A fixed address on the bridge so the box is reachable even with no upstream DHCP |
| `BOA_USB_MAX_CURRENT` | `1` lifts the Pi 5's 600mA USB cap to the full 1.6A — **only with a 5A PSU or powered hub** |
| `BOA_USER`, `BOA_PASSWORD`, `BOA_SSH_PUBKEY` | Headless login — see below |
| `BOA_NTOPNG_PASSWORD` | ntopng's admin password. **Keep it different from `BOA_PASSWORD`** — leaving it empty falls back to that, which stores your login password on the box a second time as an unsalted MD5 |
| `AP_SSID_USB` | A different SSID for the USB radio while testing it. Empty means both publish `AP_SSID` |

### Which radio serves the access point

boa's Wi-Fi runs in **Bridged AP mode**: the access point is bridged straight
onto your existing LAN, so associated clients keep their real addresses on your
real subnet — no separate Wi-Fi network, no DHCP server, no NAT. It is the
transparent bridge of the introduction seen from the radio, and it is what lets
conditioning treat a Wi-Fi client exactly like a wired one. Which *radio*
provides that AP depends on what is plugged in.

**With both radios present, both serve** — the box is a dual-band router. The
USB adapter takes 5 GHz, where its 80 MHz and 802.11ax are the reason to fit
one; the onboard chip takes 2.4 GHz (`AP_CHANNEL_24`), where its 20 MHz /
802.11n ceiling costs nothing it could have delivered anyway, and where the
range is. Both publish `AP_SSID` onto the same bridged segment, so a client
sees one network and keeps its address moving between them.

Either radio on its own still serves alone: unplug the adapter and the onboard
chip carries the AP on `AP_BAND`/`AP_CHANNEL`.

**Clients on both radios are conditioned.** The daemon follows a list of
interfaces (`BOA_WLAN_PORT` holds one or more, space separated, written by
`select-radio`), reads a station dump per radio, and sends a per-client link
event to the control socket of the radio that client is actually associated to.

A radio the daemon is *not* watching still appears in the Bridge tab, named
whatever the kernel called it and marked *not conditioned*, with a standing
notice saying its clients pass traffic without appearing in the Clients tab.
That is the case to know about if you fit a **second USB adapter**: the udev
rule renames only the first USB wlan device to `wlan-usb`, so a second one
keeps its kernel name and no hostapd instance is configured for it.

**Both radios are driven by hostapd** — the onboard one the same way as the USB
adapter. For the USB `mt7921u` hostapd is not optional: NetworkManager's AP mode
goes through wpa_supplicant, which fails on it (`Hotspot network creation took
too long`; `nl80211 driver interface is not designed to be used with
ap_scan=2`). hostapd drives that radio without complaint — and the onboard
Broadcom radio too, so there is one codepath, not two. Unifying matters because
only hostapd exposes the control socket the daemon uses for per-client link
events (deauth/disassoc/deadzone): under NetworkManager the onboard radio had
none, so those worked only on a USB adapter. The onboard radio stays 20 MHz /
802.11n and, lacking survey support, uses a fixed channel rather than
auto-selecting — but it now gains the same link-event conditioning.

It is worth the trouble because the AP's ceiling bounds the top of a measured
ladder. On a Pi 5 the onboard radio runs the AP at 20 MHz. Measured here with a
Panda PAU0F (mt7921u) at 80 MHz and 802.11ax, iperf3 to the box over Wi-Fi:

| | Downlink | Uplink |
|---|---|---|
| USB 3.0 port | **717 Mbit/s** | 375 Mbit/s |
| Same adapter, USB 2.0 | 117 Mbit/s | 135 Mbit/s |
| Wired, for reference | 924 Mbit/s | — |

**Put the adapter in a SuperSpeed port and check that it got one.** At USB 2.0
it still works, still reports 80 MHz and 802.11ax, and still shows a PHY rate
over 1 Gbit/s — it just quietly delivers a sixth of the throughput, and nothing
says why. A USB 3.0 adapter that is not fully seated, or is on an extension
cable without SuperSpeed pins, enumerates as USB 2.0 in a blue port and looks
identical from every angle except the descriptor:

```sh
lsusb -v -d <id> | grep bcdUSB       # 3.20 is right; 2.10 means High-Speed only
lsusb -t                             # the adapter's line should read 5000M, not 480M
dmesg | grep -i "new .* USB device"  # "new SuperSpeed USB device" is the one you want
```

One thing that will mislead you: `iw dev wlan-usb info` reports
`txpower 3.00 dBm` on this adapter no matter what it is set to. It is a driver
misreport, not the radio — clients see −27 to −38 dBm and negotiate full rates.
Trust the client-side signal, not that field.

And do not reach for that field to *change* the power either: setting it does
nothing on this adapter, measured across its entire legal range. The readout bug
is documented upstream and being fixed; the control being inert is a separate
finding and is measured here rather than reported.

### Logging in, and why sudo has no password

**Set `BOA_SSH_PUBKEY`.** With a key present the image disables SSH password
authentication, and the key becomes the only way in over the network.
`BOA_PASSWORD` stays for the physical console and for getting back in if the
key is lost — leaving it empty locks the account outright, which turns a lost
key into a reflash.

Give it **more than one key**, one per line, if you have a second machine. A
single key is a single point of failure once password login is off. `build.sh`
parses each one with `ssh-keygen` and prints its fingerprint, so a truncated
paste fails the build rather than producing a box nobody can log into — a
malformed `authorized_keys` line is ignored by sshd without any complaint.

The image also grants `BOA_USER` passwordless sudo. That is deliberate, and it
is safe *because* of the above rather than in spite of it. A shell as
`BOA_USER` is already the whole box, so a sudo prompt asking a second time for
the same short password protects nothing — while genuinely breaking
`scripts/deploy.sh`, which runs over a non-interactive ssh session that has no
terminal to answer a prompt on. The credential worth strengthening is the key,
not the prompt.

Without a key, sshd accepts `BOA_PASSWORD` from anyone associated to the AP,
and those characters are all the security there is. `build.sh` warns when it
sees that combination.

`BOA_SSH_PASSWORD_LOGIN="true"` keeps password logins available *alongside* the
keys, for getting in from a machine that has no key on it. It is off by default,
because the fallback is reachable by everyone else on the network too — the box
becomes only as strong as `BOA_PASSWORD` however good the key is.

`BOA_NTOPNG_PASSWORD` is separate on purpose. ntopng runs with login disabled
(`-l=1`), so nothing checks it; it exists so the account is not left on ntopng's
default if login is ever re-enabled. Keep it *different from* `BOA_PASSWORD`:
ntopng stores its secret as an unsalted MD5, and reusing your login password
would put that password on the box in a second, weaker form.

### The Wi-Fi passphrase is the whole perimeter

`AP_PASSWORD` is not just Wi-Fi security — it is the *only* thing standing between
a stranger in radio range and your network. The AP runs in **Bridged AP mode**
— a transparent bridge onto your existing LAN, not an isolated guest network
with its own subnet, so a device that associates lands on the real segment
beside everything else. And the
interface on `:80` and ntopng on `:3000` have **no login** ([PRD §5](PRD.md)), so
whoever associates can also re-shape or **black-hole any device on the network** —
a one-line denial of service — and read ntopng's per-device traffic breakdown.

WPA2-PSK with a weak passphrase is crackable offline: capture one handshake and
run a dictionary against it at leisure. So use a **strong, random** passphrase,
treat it as the credential it is, and keep the box on a network where a person who
gets past it is someone you would have let on anyway.

## Security

boa is a **bench appliance for a network you already control**, and its whole
security model is that one assumption — stated here so it is a choice rather than
a surprise.

- **No login, and plain HTTP.** The interface on `:80` and ntopng on `:3000` have
  no authentication, and neither uses TLS — the box has no domain, so any
  certificate would be self-signed. On a shared layer-2 segment an on-path
  attacker can read a management session, and the mDNS `.local` name it answers to
  can be spoofed. Reach it over a network you trust.
- **The daemon runs as root** — shaping and the packet socket require it — and its
  API is unauthenticated and reachable by anything on the bridge. The box is only
  as contained as the network it sits on.
- **Cross-site requests are not blocked.** A page a browser on the network loads
  can trigger some state-changing `POST`s, up to black-holing a device. Tracked in
  [#130](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/130); the
  trusted-network assumption is what stands in until it is fixed.
- **ntopng and iperf3 are open.** Anyone on the network can read ntopng's
  per-device traffic breakdown on `:3000` and load the box with iperf3 on `:5201`.
  Both are deliberate and neither is gated.
- **Secrets live in the image.** The AP and login passwords are baked into the SD
  card (the NetworkManager and hostapd files are mode 0600). Treat the card — and
  any image written from it — as carrying them; it is another reason images are
  never redistributed (see [`docs/LICENSING.md`](docs/LICENSING.md)).
- **It is not a firewall.** A transparent bridge forwards everything and gives the
  devices it conditions no protection they did not already have (see
  [Non-Goals](PRD.md#3-non-goals)).

The Wi-Fi passphrase is what enforces all of this — see [The Wi-Fi passphrase is
the whole perimeter](#the-wi-fi-passphrase-is-the-whole-perimeter) above.

## How the conditioning works

Both directions are shaped on a **true egress queue** — the last interface the
packet crosses before leaving boa:

| Direction | Where | Filter matches |
|---|---|---|
| Downlink (internet → client) | egress of the **client's own port** | destination IP |
| Uplink (client → internet) | egress of the **WAN port** | source IP |

Downlink accuracy is the priority, since the main use is throttling streaming
video on its way to a player. Shaping on the client's own port makes the shaper
the last thing to touch the packet, so the inter-packet spacing the player
measures is exactly what was configured.

**netem enforces the rate, not HTB.** HTB is a token bucket: while idle it
accumulates credit and then releases a burst at line rate when traffic resumes —
exactly when a player starts a segment and measures throughput, systematically
inflating its bandwidth estimate. netem instead computes each packet's
serialisation time from its length, which is what a real slow link does. HTB is
kept only as a classifier and per-client byte counter.

Measured on real forwarded traffic, a downlink cap lands within 6 % of target
across the whole verified range, 0.25 to 50 Mbps. That shortfall is not error:
the kernel's own class counters read the configured rate exactly, and the 4–6 %
a client sees missing is the Ethernet, IP and TCP framing the cap counts and a
payload byte-count does not — the same overhead a real link of that speed would
impose. **Uplink is untested at any rate.** A configured 200 ms one-way delay
measured 200.6 ms RTT.

**A cap above the link's own ceiling costs about 1.5 %.** Set well past what the
medium can carry — 700 Mbps and 1 Gbps over a Wi-Fi link that tops out near
510 Mbps — the cap is not the binding constraint, so this measures what merely
having netem in the path costs, not rate accuracy. The radio's own baseline
drifts ~100 Mbps across a 90 s run, far more than the effect, so a single
capped-vs-uncapped comparison is useless and even gets the sign wrong;
interleaving capped and uncapped 15 s runs, the paired mean was −1.5 % (505.8
against 513.7 Mbps downlink). Measured 2026-09-01 on a Pi 5 with the mt7921u
USB-3 radio, one client, over IPv6. netem adds no meaningful ceiling of its own
at these rates; whether it holds nearer a gigabit on the wired path, where the
baseline is steady, is untested.

Policies are keyed by **MAC**, not IP, so they survive a DHCP renewal, a reboot,
and a client roaming between the wireless and wired ports.

**Both address families are conditioned by one policy.** A device usually holds
several routable IPv6 addresses at once under privacy extensions; each gets its
own filter, because shaping one of them would shape only part of its traffic.

### Sub-classes

Each device can carry rules that condition part of its traffic differently —
"video from this CDN gets 1.5 Mbps and 200 ms, everything else stays clean."
Match on destination port, network, and/or protocol.

Note what this can and cannot do. A sub-class distinguishes traffic by *service*,
not by application: a phone's ephemeral source ports change per connection, so
there is nothing stable to bind a per-app policy to. If you need true per-player
separation on a single device, put a port-allocating proxy in the path and match
on the ports it hands out — the two compose.

## Things that will mislead you if nobody says them

- **Wi-Fi airtime is shared.** Conditioning is *additive on top of* a variable
  radio baseline. One client's traffic still affects another's achievable rate
  no matter what the sliders say. This is the fundamental difference from a
  wired lab, and it is why the UI states it on screen.
- **Delay is per direction.** 100 ms each way is ~200 ms of round trip. The UI
  shows the computed total next to the inputs.
- **`overlimits` is not an error.** It counts how often a class hit its ceiling
  — a healthy throttled client shows it climbing constantly.
- **PHY rate is not throughput.** The radio routinely negotiates 400+ Mbps on a
  link carrying 2 Mbps.
- **The queue is sized from rate × delay.** netem's default 1000-packet queue
  would silently drop half the traffic on a "50 Mbps, 500 ms, 0 % loss" profile.
  boa computes the queue depth instead, so configured loss is the only loss.
- **A phone's MAC is not stable.** Policy is keyed by MAC, but iOS and macOS
  present a randomised, per-SSID address that changes when the network is
  rejoined or the setting is toggled — and on iOS 18, on its own schedule. When
  it rotates, the device arrives as a brand-new client with no policy, and its
  configuration and its *measured ladder* — half an hour of real playback —
  are stranded on a row that will never return. On any device you control, set
  **Settings → Wi-Fi → (the network) → Private Wi-Fi Address** to **Off** (or
  **Fixed** on iOS 18) before a long measurement. A device you cannot
  instrument — most TVs and set-top boxes — is covered only once the hostname
  adoption in [#45](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/45)
  is built.

## Layout

```
build.sh              orchestrates the build; validates .env
flash.sh              optional raw dd write to a card (macOS); prefer an imager
scripts/customize.sh  all image surgery; runs in a privileged arm64 container
scripts/build-payload.sh  builds the UI and cross-compiles the daemon
daemon/               Go daemon; embeds the compiled UI, ships as one binary
ui/                   Vue 3 + TypeScript interface
PRD.md                product behaviour source of truth
docs/DATA-CONTRACT.md where every displayed number comes from and what it means
docs/LICENSING.md     what may be redistributed, and what may not
docs/BACKLOG.md       accepted limitations; candidate work lives in issues
overlay/              staged files grafted into the image root
```

## Development

Three loops, fastest first. Pick the slowest one you actually need.

### 1. Interface only, no hardware — sub-second

```sh
./scripts/dev.sh
```

Starts the daemon in **demo mode** and Vite with hot module replacement in front
of it. Editing a `.vue` file updates the browser immediately. No Pi, no root, no
image.

Demo mode is not a mock server — it is the real daemon with synthetic clients,
so it serves the same types through the same JSON encoding and the same SSE
transport. A separate mock would drift from production the first time a field
changed; this cannot.

The synthetic fleet deliberately includes the states that are tedious to
reproduce on real hardware and therefore never get styled: a client associated
but without an address yet, and a client configured but currently absent.
Throughput responds to the sliders, so the controls feel live.

### 2. Interface against a real Pi — sub-second, real data

```sh
./scripts/dev.sh infinite-streaming-boa.local
```

Same hot reload, but the API calls proxy to a running Pi. Note this is
read-write: moving a slider really does condition that device's traffic.

### 3. Full deploy to a Pi — about ten seconds

```sh
./scripts/deploy.sh                    # boa@infinite-streaming-boa.local
./scripts/deploy.sh boa@192.168.1.9
```

Builds the interface, cross-compiles the daemon, copies one binary, restarts one
service, and prints the health endpoint. Use this for changes to the daemon
itself, or to confirm a UI change on the real device.

**Reflashing the SD card is only needed when something outside the binary
changes** — network profiles, systemd units, packages, kernel settings. Day-to-day
work never touches the card.

Set up a key first, since this runs often:

```sh
ssh-copy-id boa@infinite-streaming-boa.local
```

### Useful

```sh
cd ui && npm run typecheck              # vue-tsc, no build
cd daemon && go vet ./...               # daemon also compiles on macOS
ssh boa@infinite-streaming-boa.local 'journalctl -u infinite-streaming-boa -f'
ssh boa@infinite-streaming-boa.local 'tc -s class show dev wlan0'   # what the kernel really has
```

## How this was built

boa is a reimagining of a link conditioner I had built by hand once before. The
idea is the same; the implementation is not. This version was written end to end
with [Claude Code](https://claude.com/claude-code) — the Go daemon, the Vue
interface, the image build, the systemd and network plumbing, the docs and the
tests — over about four days. The hand-built original took roughly four weeks.
The commit history is the record of it.

## Licence

MIT — see `LICENSE`. Every dependency is permissive; `docs/LICENSING.md` records
the audit and the one rule that keeps it that way: **ship the build scripts, not
built images.** A built image is a derivative of Raspberry Pi OS and carries
several hundred packages' worth of obligations that this repository does not.
