# boa

Part of the infinite-streaming family. The repository is
`infinite-streaming-boa`; `boa` is the appliance itself — the binary, the
hostname, the SSID.

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
                          ╱          ╲
                      wlan0          lan0
                    Wi-Fi AP     USB ethernet
                        │              │
                  wireless clients   wired client
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
apps increasingly refuse. pfSense and a hand-rolled `tc` router both work, and
both make the box a hop with its own subnet and its own DHCP. boa asks for nothing: cable it in, and a device keeps
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
Caps here are verified from 0.25 to 50 Mbps and nothing above that has been
measured. Loss is deliberately not reproducible run to run. Over Wi-Fi the
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
| Board | [Raspberry Pi 5 Model B, 4 GB](https://www.amazon.com/dp/B0CK3L9WD3) — or the cheaper [2 GB](https://www.amazon.com/dp/B0DDL91V2R), see below | A Pi 4 works; the onboard NIC must not be USB, which is why a Pi 3 does not — see the udev rule in `scripts/customize.sh` |
| Power | [Official Raspberry Pi 27 W USB-C PSU](https://www.amazon.com/dp/B0CW7XCY75) | A SuperSpeed Wi-Fi adapter is a real load. Check `vcgencmd get_throttled` reads `0x0` |
| Storage | [SanDisk Ultra 16 GB microSDHC](https://www.amazon.com/dp/B074B4P7KD) | What was used, and enough — the finished image is ~4.6 GB. A 32 GB card costs little more and leaves room for `ntopng` data |
| Wi-Fi adapter | [Panda Wireless PAU0F AXE3000 (mt7921u)](https://www.amazon.com/dp/B0D972VY9B) | Optional, and the single biggest change to what the box can test — see below |
| Wired downstream | Any USB ethernet adapter | Becomes `lan0`. Optional |

### How much RAM this actually needs

Measured on a running box: **539 MB used of 4 GB**, 3.5 GB available, swap
untouched, and the kernel's own `Committed_AS` estimate at 733 MB.

| | RSS |
|---|---|
| ntopng | 278 MB |
| NetworkManager | 21 MB |
| redis (for ntopng) | 16 MB |
| **boad** — the conditioner itself | **14 MB** |
| hostapd | 9 MB |

The appliance's own work is 14 MB. Over half the footprint is ntopng, which is
optional to the conditioning and only there for flow visibility. **A 2 GB Pi 5
has ample headroom**, and the 4 GB board this was built on is not a
requirement — it is what happened to be to hand.

One caveat before buying the smaller board. Nothing bounds ntopng's growth:
its config sets no memory limit and redis runs with `maxmemory 0`. It holds
per-host and per-flow state, so 278 MB is a floor measured on a quiet segment,
not a ceiling. On a busy network over days it will be larger. If you run 2 GB
and ntopng grows into it, the answer is a retention limit rather than a bigger
board — unbounded growth eventually fills a 16 GB card whatever the RAM.

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
| Wired, for reference | 924 Mbit/s | — | — | 8s |

Same MacBook, same afternoon. The adapter on a SuperSpeed port is **~10x the
onboard radio downlink** — the difference between a ladder whose top rung means
"uncapped" and one that can be measured.

**The run column is not decoration.** The same onboard radio measured 39 Mbit/s
over 15s with three clients associated and 55 Mbit/s over 60s alone. Neither is
wrong; a figure here without its conditions is.

What costs you is not the NUMBER of associated clients but how *active* and how
*slow* they are: dropping from two clients to one moved downlink 54.4 -> 54.9,
because the second was idle. A single station linked at 65 Mbit/s, or one
actually transferring, is worth far more than a headcount.

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
- **`txpower` from `iw` is not reliable on every adapter.** The mt7921u reports
  `3.00 dBm` whatever it is set to, while clients see −27 to −38 dBm and
  negotiate full rates. Trust the client-side signal.

## Build an image

Needs `curl`, `docker`, `go` and `npm`. On macOS the Docker engine can come from
Rancher Desktop, Docker Desktop, colima or OrbStack — it exists only to supply a
Linux kernel, since macOS can neither mount ext4 nor loop-mount a partition
table. On Linux no container runtime is needed at all.

```sh
cp .env.example .env      # set your SSID, passphrase and country
./build.sh                # ~5 min first time, then cached
./flash.sh                # writes the newest image to an SD card
```

Cable the Pi's `eth0` to your existing network, optionally plug a USB ethernet
adapter in for a wired device under test, then:

| | |
|---|---|
| Web interface | `http://infinite-streaming-boa.local/` |
| ntopng | `http://infinite-streaming-boa.local:3000/` — no login |
| iperf3 | `iperf3 -c infinite-streaming-boa.local` from a device under test |
| SSH | `ssh boa@infinite-streaming-boa.local` |
| Rescue | `http://<BOA_RESCUE_IP>/` when upstream DHCP is absent |

**The two directions measure different things.** `iperf3 -c <pi> -R` sends from
the box to the device, which is that device's downlink — conditioned by its
policy, so this is the cap being enforced. Without `-R` you are measuring upload
*to* the box, which terminates there and never reaches the WAN port where uplink
shaping lives; that reports what the link can do, not what the policy allows.
Verifying uplink needs load from a host beyond `eth0`.

Only the box's management ports — the interface, SSH and ntopng — are exempt
from shaping, so a cap can never throttle the dashboard needed to undo it.
Everything else the box sends is conditioned like any other traffic.

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

## Configuration

Everything lives in `.env`; see `.env.example` for the full annotated list.

| Variable | Meaning |
|---|---|
| `AP_SSID`, `AP_PASSWORD` | The wireless network the Pi publishes |
| `AP_COUNTRY` | Regulatory domain. **The radio stays blocked until this is right** |
| `AP_BAND`, `AP_CHANNEL` | `bg` (2.4GHz) or `a` (5GHz); 5GHz AP mode is limited to channels 36/40/44/48 |
| `BOA_WAN_PORT` | The port cabled to your existing network. Conditioning is applied here |
| `BOA_RESCUE_IP` | A fixed address on the bridge so the box is reachable even with no upstream DHCP |
| `BOA_USER`, `BOA_PASSWORD`, `BOA_SSH_PUBKEY` | Headless login — see below |
| `BOA_NTOPNG_PASSWORD` | ntopng's admin password. **Keep it different from `BOA_PASSWORD`** — leaving it empty falls back to that, which stores your login password on the box a second time as an unsalted MD5 |
| `AP_SSID_USB` | A different SSID for the USB radio while testing it. Empty means both publish `AP_SSID` |

### Which radio serves the access point

If a USB Wi-Fi adapter is plugged in, it serves the AP and **the onboard radio
is switched off at the rfkill level**. Unplug it and the onboard radio comes
back. Exactly one runs, because the daemon watches a single interface — a
client associated to a second AP would be invisible to conditioning.

The two are driven differently, and not by choice. NetworkManager runs AP mode
through wpa_supplicant, which does not work with an mt7921u: activation ends in
`Hotspot network creation took too long`, with wpa_supplicant noting `nl80211
driver interface is not designed to be used with ap_scan=2`. hostapd drives the
same radio without complaint. So: hostapd for a USB adapter, NetworkManager for
the onboard radio.

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
flash.sh              writes an image to an SD card (macOS)
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

## Licence

MIT — see `LICENSE`. Every dependency is permissive; `docs/LICENSING.md` records
the audit and the one rule that keeps it that way: **ship the build scripts, not
built images.** A built image is a derivative of Raspberry Pi OS and carries
several hundred packages' worth of obligations that this repository does not.
