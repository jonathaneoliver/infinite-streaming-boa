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
| `BOA_USER`, `BOA_PASSWORD`, `BOA_SSH_PUBKEY` | Headless login |

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
