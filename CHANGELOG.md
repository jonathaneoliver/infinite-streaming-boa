# Changelog

All notable changes to boa are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the version is stamped
into the daemon at build time from the git tag (see `scripts/version.sh`), and a
running box shows it in the header of the web interface, linked to the
project page, and prints it with
`boad -version`.

This is a bench appliance, not a certified instrument. The limitations below are
deliberate and documented so they are not mistaken for defects — see
[`docs/BACKLOG.md`](docs/BACKLOG.md) and the constraints in [`PRD.md`](PRD.md).

## [Unreleased]

Nothing yet.

## [0.5.0] — 2026-09-23

**boa installs on a router now — and on a router's own radios, it stops being
told no.**

Until this release boa needed hardware of its own: a Raspberry Pi flashed from
an image, or a Linux host running a container. 0.5.0 makes it **two packages you
install beside LuCI** on an OpenWrt device, from a signed feed, integrated with
UCI, ubus and procd — so the box that conditions your test clients can be the
router that was already serving them.

That change is what made the second one reachable. Every release before this ran
on client silicon with AP mode bolted on, and a long list of things the
interface offered were things the hardware declined: a channel move was always
an outage, transmit power was a setting the driver discarded, surveying the band
meant dropping everyone. Installing on a router means installing on **access-
point silicon**, and there the refusals stop. A channel move is announced in the
beacons and the clients follow it; transmit power is honoured live, so distance
becomes something the box *imposes* rather than models; a radio surveys the band
while still serving.

All of it measured on one Cudy TR3000 over two days, with what did not work
written down beside what did.

21 pull requests.

### Two packages, beside LuCI

- **OpenWrt**, as two packages beside LuCI — `boa` and `luci-app-boa`, with a
  **Services → infinite-streaming-boa** page — integrating with UCI, ubus and
  procd, published as a **signed apk feed** on GitHub Pages for both
  `aarch64_cortex-a76` (a Pi 5 on OpenWrt) and `aarch64_cortex-a53` (Filogic
  routers such as the Cudy).
- **Debian**, as `infinite-streaming-boa` in a signed apt repository, for a Pi
  OS or Debian machine already set up as a bridge.
- **`boa-setup check`**, a read-only walk of every prerequisite a device still
  needs, printing each as OK, WARN or FAIL with the command that fixes it.

### Moving a radio without moving its clients

A channel change now **announces** itself where the radio can do it (802.11h)
and restarts where it cannot, falling back inside the same request — the
operator asked for a channel, not for a mechanism — and the result reports which
one happened rather than leaving it to be inferred from an outage figure.

Measured: six announced switches on the Cudy's 5 GHz radio, every one
`method: announce` with `outage_sec: 0`, and the box's own before-and-after
station comparison counted **3 of 3 clients following** the last one — a
MacBook, an iPhone and a Watch together. A forced teardown of the same radio
cost 1.0 s and 1.1 s of service on two runs and **84.7 s** on a third, when the
access point came back with no BSS and had to be rebuilt: the outage a restart
costs is not a constant, which is the second reason the mechanism is stated.

Which radios can announce is learned by **attempting**, never by asking.
`iw phy info` lists `channel_switch` on the `mt7921u`, which refuses every form
of it, so the capability bit returns the opposite of the truth. A refusal is
recorded against the *driver* only when the failed attempt was an ordinary
in-band move that the fallback then completed — the combination that proves the
target was legal rather than the driver at fault.

### Distance you can impose, not model

Transmit power is set on the phy while the access point keeps serving. On the
Cudy's built-in radios: 23 / 10 / 3 dBm moved a MacBook's received signal
−38 / −48 / −55 dBm, about 7 dB a step, with nobody reassociated and no ping
lost. Where a driver accepts the setting and discards it — the `mt7921u` reports
3.00 dBm whatever you ask — the box catches that by reading the level back and
**disables the control with the reason** rather than offering a slider that
does nothing.

It also produced the first real roaming measurement here. An iPhone one room
away, walked down and back up in 2 dB steps, **left 5 GHz at 11 dBm** and
**returned at 19 dBm**: 8 dB of hysteresis, so leaving and returning are two
measurements rather than one threshold. It kept streaming across both moves.

A pattern can drive it: a transmit-power lane, and a *txpower valley* preset
that walks a radio down and back up while leaving the others alone.

### A radio that only listens, from the interface

Any radio can be switched to **listen-only** and back from its own row. It
serves nobody and sweeps both bands continuously, which is where the band plan's
colours come from — one pass saw 17 access points and 64 clients, and rated
channel 36 at 56% busy against channel 149 at 3%.

That reading then predicted the throughput: same radio, same client, same PHY,
**164 Mbit/s down on channel 36 against 622 on 149**. The neighbourhood view
moved out of one radio's fold into its own, since it was never about a
particular radio.

### Asking a client to move, in four escalating words

`steer`, `warn`, `term` and `force` — one control with a mode rather than four
buttons — on each radio row **and on each client's own card**. The same frame
either way; the difference is how many clients receive it.

Sending them one at a time to one device is the point. Measured 2026-09-23,
three requests to a MacBook three seconds apart with nothing else changed: the
plain steer came back `status_code=6` (declined), and the identical request
carrying Disassociation Imminent came back `status_code=0` with the named radio
as `target_bssid` — and it went there. **Only the sentence changed.**

A honoured steer is still not a client that stays: none of the four writes a
deny entry, and an iPhone that accepted a move to 2.4 GHz returned to 5 GHz of
its own accord 23 seconds later. Placement that must persist is what `gather`
is for.

### Changed

- The running **version moved from the footer to the header**, where it is a
  link to the project page: the two things wanted when reporting a result.
- The README describes **four targets** rather than three, the Quickstart
  chooses between them instead of walking one, and every measurement section
  names the target and radio it was taken on.
- Sections that describe a USB adapter no longer say "this box".

### Fixed

- **Making a second radio listen-only silently un-made the first.**
  `boa.main.scan` is a list and was being written as a single value, so turning
  one radio into an instrument dropped another out of the config — and taking
  any one back to serving removed them all.
- The neighbourhood fold, rack counts that froze, controls greyed out on a
  target with no rfkill, and a scan-and-apply choice that was not remembered.
- `boa` no longer stays pinned in apk's world after an install.

### Measured on hardware

Every headline figure in this release came off one Cudy TR3000 on 2026-09-22
and 09-23, and the full record — including what could not be made to work — is
in [`openwrt/CUDY-TR3000.md`](openwrt/CUDY-TR3000.md).

| | |
|---|---|
| Announced channel switch | 3 of 3 clients followed; `outage_sec: 0` |
| Forced restart, same radio | 1.0 s, 1.1 s, and once 84.7 s |
| Transmit power | ~7 dB per step, nobody reassociated |
| Roaming thresholds | left at 11 dBm, returned at 19 — 8 dB hysteresis |
| Channel choice | 164 Mbit/s on ch 36 against 622 on ch 149 |
| Wi-Fi through the bridge | 745 Mbit/s |
| Scanning while serving | keeps its clients, and costs 18 consecutive pings |

### Known limitations

- **No silent power cut on OpenWrt.** Its kernel has no rfkill. The hardware can
  still do it — `ip link set <ap-iface> down` takes the BSS off air with no
  frame sent, and a MacBook took six seconds to notice — but there is no control
  for it, and boa's own wedge watchdog rebuilds the BSS within seconds.
- **DFS, 160 MHz and multiple BSSes are advertised and untested.** The Cudy's
  radios claim radar detection on 52–144, `HE160/5GHz`, and 16 access points per
  radio. Nothing here has exercised any of them.
- **The Cudy is a router, not a small server.** Two Cortex-A53 cores, 485 MB of
  RAM and a 44 MB writable overlay, with the cores 65% busy carrying
  745 Mbit/s. No ntopng, no glances, little room for packages.
- **No millisecond figure for an announced switch.** "Kept the association" is
  not "lost no packets"; a ping held across a switch would give the number.
- **The `aarch64_cortex-a53` feed has not been installed from.** The packages
  are built and published; this box was installed from a local SDK build.

## [0.4.0] — 2026-09-14

**The box can now say where the traffic actually went — and it stops lying about
its own hardware.**

A transparent bridge appears in no client's own view of the network, so the two
questions anyone asks of it first — *is the uplink the bottleneck*, and *is this
client talking to the internet or to the machine beside it* — had no answer on
screen. 0.3.0 could chart each conditioned client and nothing else, and measured
on the WAN port of a running box that is not most of the traffic: the default
class carried 112,693,736 bytes while the busiest client's class carried
12,268,732. Nine tenths of what crossed that port belonged to no client at all,
so summing the client series understated the port by an order of magnitude.

0.4.0 counts at three new places — every bridge port, every adapter pair, every
device pair — and draws the result as a flow diagram. The second theme is the
box being honest about itself: the listen-only radio reports what it actually
measures instead of five blanks where a client's figures would go, and an
adapter attached below USB 3 says so on its own row rather than capping every
measurement on the box while every other number looks healthy.

**If you build images, take this release whatever you think of the features:
0.3.0 cannot build one.** An unescaped comment in `scripts/customize.sh`
aborted every build under `set -u` from the day 0.3.0 was tagged.

15 pull requests.

### Where the traffic actually went

![The traffic panel: stacked download and upload for the whole box, above a
pair of Sankey diagrams naming which device sent to which, with a 152 Mbit/s
ribbon running between two of them](docs/images/traffic-routing.png)

The same second, twice. Above, every adapter's throughput. Below, where it
went: one ribbon per counted pair, thickness by rate, both directions on one
scale. The fat ribbon is the point — a MacBook on Wi-Fi sending 152 Mbit/s to a
Mac Mini on a wired port, and neither end of it is the uplink, so that traffic
never left the box. Nothing in 0.3.0 could say that.

- **The whole box, not just the devices on it** (#311). Per-port kernel
  interface counters, read on the same tick that already samples the client
  classes, so a port band and a client band share an x-axis. Interface counters
  have no attribution step to go wrong: every frame counts, including the box's
  own traffic, untracked devices, broadcast and multicast.
- **Which adapter forwarded to which** (#314), and **which device talked to
  which** (#313), both drawn as a flow diagram. The adapter matrix answers
  "this radio forwarded to that wired port"; with several devices per adapter
  that is a hint rather than an answer, so the device matrix names the pair.
  The existing `by adapter` / `by client` toggle drives the diagram, which until
  now it did not touch.

### The listening radio earns its place

- **It says what it measures** (#320). Its fold held two empty charts and its
  row five blanks, because both were built for a radio that carries clients —
  and it carries none by design. It now shows its survey instead: what it heard
  and when, the busiest and quietest channel, a table of neighbouring networks
  with channel, the channels each one occupies, signal, clients and airtime, and
  a second table of our own access points as the scanner hears them.
- **Its first scan works** (#321). After every container recreate the first
  sweep failed, twice out of two, and had been written off as a flaky driver.
  Nothing raised the interface: a scanner has no hostapd and NetworkManager is
  told to leave it alone, so the only `ip link set up` ran at the moment the
  first scan was already being attempted. Probed once a second: `ENODEV` at
  +0s, `ENETDOWN` +2s to +19s, `EBUSY` +20s, working at +24s. After the fix,
  three cold starts gave first sweeps at +7.9s, +7.8s and +3.7s.
- **VERIFIED ON THE PI**, which 0.3.0's notes could not claim — that release
  shipped this as shared code exercised only on the container host. Measured
  2026-09-14 with `BOA_SCAN_PORT="wlan0"`, the onboard brcmfmac radio: raised
  for scanning at 11:24:50 and its first sweep returned at 11:24:51, one second
  later, heard 16 access points across both bands and kept 15 neighbours.

### It tells you when its own bus is the problem

- **An adapter below USB 3 rates warns on its own row, in red** (#325) — wired
  ports included, since those are USB devices too. This is the fault that looks
  like nothing: the interface reported a 1000 Mbit/s ethernet link and a
  961 Mbit/s negotiated Wi-Fi rate while four adapters behind a USB 2 hub held
  the Wi-Fi downlink to 92 Mbit/s where a USB 3 port gave 632, and the bridged
  path to 87 against 203. Every cap above roughly 90 Mbit/s was silently
  unenforceable.
- **The advice matches the board.** The box reports how many USB 3 **ports** it
  has, counted through `maxchild` rather than by counting root hubs — this Pi's
  four hubs are two USB 3 sockets, not four — so it never sends you looking for
  a socket the board does not have, and on a board with none it says plainly
  that only different hardware will help. `boactl probe` fails on the fault.

### Changed

- **`by device` → `by client`**, which is what a router calls them (#320).
- **One toolbar governs every chart** (#320), rather than a set of controls per
  chart.
- **The scan line in the activity log is shorter, quieter and more accurate**
  (#326). It leads with airtime, the only figure in it that predicted
  congestion here; a background poll round now speaks only when a band's
  busiest channel changes, when the recommendation changes, or when that
  channel's airtime moves a wide margin; and it names the bands it actually
  swept rather than the band its radio sits on, which on this box meant it said
  "2.4GHz" for ever while quoting 5GHz channels in the same sentence. Measured
  on a serving radio, sixteen lines per four minutes became one.

### Fixed

- **No image could be built at all** between 0.3.0 and this release (#322).
- **The adapter line under a device name was cut off** (#317).
- **A caret on the traffic heading that folded nothing** (#316).
- **The README named `SCAN_IF` where the documented variable is `BOA_SCAN_IF`**,
  which reads as a third variable beside `BOA_SCAN_PORT` when you meet all
  three on one page.

### Upgrading from 0.3.0

**`deploy.sh` is enough.** No new `.env` variables, no package changes, and the
only change below the binary is the build fix — so an existing box takes this
in ten seconds. A reflash is needed only for a fresh card, and if you tried to
build one from 0.3.0 it failed; this is the fix.

To give the Pi a listen-only radio, which is the feature most worth turning on
here, name the onboard radio and reflash — it is an image-time setting:

```sh
BOA_SCAN_PORT="wlan0"
```

It costs the 2.4GHz network and leaves the USB adapters serving alone. On a
container host the equivalent is `BOA_SCAN_IF`, named as the **host** calls the
card.

### Known limitations

- **A genuinely USB 2 adapter carries a standing underspeed warning.** sysfs
  cannot tell it from a USB 3 adapter on a bad cable: `version` reports the
  bcdUSB of the *connection*, not of the hardware, so the same adapter reads
  3.20 at 5000 Mbit/s and 2.10 at 480. The rule is therefore the negotiated
  rate alone.
- **A listen-only radio logs more scan lines than a serving one.** It is free to
  scan, so the poll picks it every round, and the airtime-jump rule then fires
  on real 2.4GHz swings: measured on the Pi, ch1 moved 35% → 65% → 34% inside a
  minute and each step was reported. Every line is honest; the band may want
  widening for 2.4GHz.
- **Enforcement has never been measured end to end on the Pi.** The figures for
  a cap being enforced through the box (86.0 and 38.1 Mbit/s against a 90/40
  cap) are the container host's.
- **No client tested here has returned an 802.11k beacon report.** The path
  ships and the **measure** button exercises it, but participation is up to the
  device's firmware. #228 was closed as completed rather than working: the
  mechanism it proposed does not survive contact with real clients, and the
  question it asked is answered instead by hostapd's management-frame
  interface (#254), which surfaces the candidate list a refusal already carries.
- **The channel colouring overstates how clear a channel is.** A channel the
  scan produced no entry for is rated clear, which holds on 5GHz where an 80 MHz
  neighbour is spread across its whole block, and fails on 2.4GHz where a
  neighbour is recorded against its own channel only. The ±4 overlap window the
  data contract describes is not implemented.
- **`boactl` does not cover the whole API.** #263 tracks the gap and what is
  deliberately not planned.

## [0.3.0] — 2026-09-11

**The box no longer has to be a Raspberry Pi — and it can now tell you what the
air is doing rather than leaving you to infer it.**

Every release before this one assumed one shape of hardware: a Pi 5, a card to
flash, and a machine doing nothing else. That is a fair ask of somebody who
already owns one and a poor ask of everybody else, and it was the first thing
standing between this box and most of the benches that could use it. 0.3.0 runs
the same `boad` binary and the same embedded interface as a container on an
ordinary x86_64 Linux host, owning its adapters outright, with the transparent
bridge intact end to end. No card, no flashing, no dedicated machine — the host
carries on being itself, with its NIC bridged and the USB adapters given away.

The second theme is measurement. 0.2.0 could make a client do things to its
link, but it could not say what the medium cost while it happened, so a
throughput figure that moved had no explanation attached to it: the same client,
the same cap and the same channel could differ by a factor of two while every
instrument on the box reported a healthy radio. 0.3.0 reads the neighbours' own
account of how busy the channel is, attributes airtime to individual clients,
counts retries against a denominator that makes them mean something, watches the
USB bus underneath the radio, and can dedicate a radio to listening so that none
of those readings costs an outage.

39 pull requests.

### It runs on a Linux box you already have

What it needs: an x86_64 machine with Docker and the compose plugin, an ethernet
NIC facing your router, root, and a USB Wi-Fi adapter or two. Nothing is
installed on the host but the attach helpers — the interface and the binary are
built on your workstation, so the host needs neither Go nor node. One command,
once:

```sh
scripts/docker-deploy.sh <host> --setup-network
```

**One requirement will rule out a machine, so it is worth stating plainly:
NetworkManager has to be managing the uplink NIC.** The setup script moves that
NIC into a bridge with `nmcli` and refuses rather than guessing if no
NetworkManager connection is active on it. Ubuntu Desktop qualifies; **Ubuntu
Server does not**, because it defaults to netplan with systemd-networkd.

**The Pi is not the reference and the container is not a port.** The same `boad`
binary and the same embedded interface serve both, and `radioplan` is copied
into the container image unchanged, so a channel plan made on one cannot drift
from a plan made on the other. What differs is only where the box gets a bridge,
hostapd configs, hotplug handling and process supervision: the Pi takes them
from the distribution, the container brings its own.

What it costs the host, and all of it is reversible: its ethernet NIC joins a
bridge, and the USB adapters are handed to the container outright rather than
shared. What the container does not get is ntopng and glances, which are absent
from that image by decision — the interface reports them inactive and says why
rather than failing quietly.

| Unshaped, `iperf3` **to** the box | Raspberry Pi 5 | Linux container |
|---|---|---|
| Wired downlink, 2.5 GbE | 1.91 Gbit/s | 1.95 Gbit/s |
| Wired uplink, 2.5 GbE | 2.35 Gbit/s | 2.35 Gbit/s |
| One radio, 80 MHz 802.11ax | 495–683 Mbit/s | 454 Mbit/s |

The wired figures agree to within 2% on two machines with different CPUs, which
says the 2.5 GbE adapter rather than the target is the limit in both.

**Enforcement has only ever been measured end to end on the container**, and
that gap runs the other way: 86.0 / 38.1 Mbit/s against a 90/40 cap, and 57 /
13.6 against a 60/20 cap over the air. The Pi's published figures are all
ceilings taken against the box itself, which is the measurement that cannot show
a cap working. Running that set on the Pi is the more valuable missing work of
the two.

Why the host's NIC has to be bridged, since it is the part that looks
heavy-handed: a transparent bridge needs a layer-2 uplink carrying arbitrary
source MACs. A macvlan cannot provide one — in bridge mode it filters ingress by
destination MAC, and in passthru mode it consumes the lower device's frames and
takes the host off the network. A bridge plus a veth is the only shape that
gives the container real layer 2 and leaves the host on it. Verified: a client
behind the container holds a lease from the operator's own router.

### What the box can now tell you

| Fact | Where it comes from | What it replaces |
|---|---|---|
| How busy this channel is, as a **measurement** | Neighbouring access points' BSS Load elements, aggregated as the maximum and shown as a range with a reporter count | A headcount of access points, which ranks channels wrong: one AP with no clients sat in 37% utilisation while one with ten sat in 8.6% |
| What **this** radio spent on its own clients | Our own station airtime counters, verified against `iperf3` | Throughput, which says what crossed the link and not what it cost |
| What **each client** spent | Per-station airtime, where the driver attributes it — asked by observing a dump rather than by driver name | Nothing. One client was measured holding 46% of a radio |
| Whether a link is retrying | `tx retries` with the denominator `tx_failed` never had | A bare counter, which answers "since when" rather than "how is it doing" |
| That the radio is off the air despite every other sign of health | The kernel's own USB error stream | Four instruments all reporting a healthy radio while nothing was transmitting |

The last row is worth its own note, because it is the failure this release
exists to make impossible. A dongle stopped transmitting; hostapd reported
`state=ENABLED` with the right SSID on the right channel, the bridge was
forwarding, the interface was `UP LOWER_UP`, and clients steered onto it simply
vanished. A scan from the box's other dongle, ten centimetres away, found
eleven networks and not this one. The kernel had been saying so 182 times
(`mt7921u tx urb failed: -71`) and nothing was listening.

**Channel choice now rests on this rather than on guesswork.** Measured on
repaired power, one client, 70s per channel: ch149 gave 683 Mbit/s at 91.2%
airtime against ch40's 536 Mbit/s at 74.6%, with identical PHY rates. The band
plan is coloured from whichever scan covers each channel, and hovering a cell
says which rule produced its colour and what was measured behind it.

### A radio that only listens

Every contention reading used to cost an access point. The mt7921u adapters
refuse to scan while they are serving — `Operation not supported (-95)`, passive
scans included — so on a box whose radios are all mt7921u, the choice was a
stale figure or an outage. Name a radio in `BOA_SCAN_PORT` and it becomes an
instrument instead: planned no channel, written no hostapd config, given no
clients, and scanned on the background timer.

It is a distinct state from a radio that is not serving, and the interface says
so — drawn dotted rather than dashed, labelled `scanning` rather than `idle`,
and offered none of the access-point controls, because every one of those acts
through a hostapd it does not have.

Measured on the container host with the motherboard's Intel AX200 as the
scanner: 19 access points across both bands in 1.2 seconds, zero outage, and
both serving radios carrying a contention figure taken by a radio that serves
nobody. Before it, that box's air-readings map was **empty** — not stale,
absent.

Two things about that card are worth recording for anyone repeating it. It sits
at operstate `down`, exits zero when asked to come up, stays down, and scans
perfectly anyway — so the state of the interface is not evidence of anything.
And its first scan after a container restart hangs, twice out of two attempts,
which is why every scan is now bounded and a timeout names itself.

### Driving it from a terminal

`boactl` builds from `daemon/cmd/boactl` and imports the daemon's own types, so
it cannot drift from what the box sends.

```sh
cd daemon && go build -o ~/.local/bin/boactl ./cmd/boactl
boactl devices && boactl probe -ssh
```

`probe` **asserts** rather than printing numbers to be eyeballed, and exits
non-zero when the box is not doing what it claims. The traps a hand-assembled
`curl` or `ssh` has to remember — the absolute paths, the hexadecimal `tc` class
ids, the templated unit names — are already in it. It does not cover the whole
API; `boactl -h` lists what exists and #263 tracks the gap.

### Upgrading from 0.2.0

**A reflash is required. `deploy.sh` is not enough.** As with 0.2.0, the fast
loop pushes the binary and its unit, and this release depends on things below
that line: `scripts/customize.sh` and the overlay grew by about 340 lines and
now write a new adapter-naming helper, a channel planner that honours a
listen-only radio, avahi configuration, and `tcpdump` in the package list.

**Adapter names change, and this is the migration that will surprise you.**
0.2.0 named radios after the USB socket they were plugged into — `wlan-usb`,
`wlan-usb2`. They are now named after the adapter itself, from the last four
hex digits of its MAC: `wlan-usb-46c7`. Swap two dongles between sockets and
the names follow the hardware rather than the port, which is the right
behaviour for a measurement and the opposite of what 0.2.0 did.

- **Policy survives**, because it is keyed by MAC and not by interface name.
- **Anything that names an interface does not.** Scripts, saved commands and
  notes referring to `wlan-usb` will find nothing. `BOA_WLAN_PORT` is rewritten
  by the radio selector at boot, so it needs no hand edit.
- **Export your configuration before reflashing**, as a reflash replaces the
  whole filesystem including `/var/lib/infinite-streaming-boa/`:

```sh
./scripts/config.sh export > before.json     # keep this
./build.sh                                   # then write the card with an imager
./scripts/config.sh import before.json
```

**Three new `.env` variables**, all optional and all defaulting to previous
behaviour:

| Variable | Meaning |
|---|---|
| `BOA_SCAN_PORT` | A radio to keep as an instrument rather than an access point, by interface name. The Pi's onboard radio is the obvious choice |
| `BOA_SCAN_IF` | The same thing for a container host, named as the **host** calls the card, because the handover happens before the container has a name for anything |
| `AP_SSID_DOCKER` | The container's own SSID. Two boxes broadcasting one name is worse than useless: a client cannot tell them apart and a measurement belongs to whichever was louder |

**One control was removed.** The 20 MHz conditioning profile is gone (#297).
The band plan now marks which cell inside a block is the primary — the channel
the radio actually beacons on — which is what that profile was being used to
infer.

### Added

- **A container target.** `scripts/docker-deploy.sh <host>` builds the
  interface and an amd64 binary locally and ships only the artefacts, so the
  host needs neither Go nor node. The adapters are moved into the container's
  own namespace outright: USB ethernet netdevs wholesale, and 802.11 phys with
  `iw phy set netns`, because moving a radio's netdev alone leaves the wiphy
  behind and nl80211 then refuses everything hostapd needs. (#285)
- **`boactl`**, and the radio verbs behind it: `scan`, `channel`, `power`, `ap`,
  `deauth-all`, `gather`, `evict`. All access-point-wide, none naming a client.
  (#268, #269)
- **A listen-only radio**, named in `BOA_SCAN_PORT` or handed to a container
  with `BOA_SCAN_IF`. (#308)
- **Contention on every radio's title bar** from one free scan, with both bands
  kept so a 5GHz plan can be coloured without taking a 5GHz radio down. (#259)
- **A BSS Load a client can act on**, advertised floored at the truth. Values
  may only be raised above what is really happening: overstating load pushes
  devices away, which is what a busy access point does anyway, while
  understating it pulls them onto neighbours nobody here can see. (#270)
- **Per-client airtime**, stacked under the adapter throughput charts. (#255)
- **`tx retries` and a denominator**, so a clean link and a driver that does not
  count are distinguishable. (#300)
- **A capability-aware generation ladder**, and a conditioning row that states
  what it costs. (#299)
- **Several downstream wired ports**, rather than exactly one. (#275)
- **A USB fault watch** reading the kernel's own error stream. (#276)
- **A generated API reference** (`docs/API.md`) and a test asserting that every
  field the wire carries is named somewhere in the interface — a field that is
  computed and thrown away now fails the build. (#265)
- **A script that reads a DRM stream's segments from the shape of its
  traffic**, which is the only route to rungs for a service whose manifest
  cannot be read. (#278)
- **Steer refusals that say where the client wants to go**, read from the
  management frames themselves. (#256)

### Changed

- **Adapters are named after the device, not the socket.** (#275)
- **A steer waits 12 seconds for an answer**, not 5. The protocol suggests a
  response should be immediate; measured clients take nine. (#257)
- **mDNS publishes only what it should.** avahi ran with package defaults,
  which advertised a `_workstation._tcp` service exposing the bridge MAC. (#273)
- **A background scan may never cost an outage.** It attempts only the
  non-disruptive path and records a refusal, since `-95` is a complete answer at
  no cost. (#259)
- **The 20 MHz conditioning profile was removed.** (#297)
- **`docs/BACKLOG.md` is no longer where candidate work lives** — that is GitHub
  issues. The file holds accepted constraints, so they are not rediscovered.
  (#301)

### Fixed

- **A channel move between bands.** A move never set `hw_mode`, so asking a
  5GHz radio for channel 6 set the channel, was acknowledged, and then failed
  the `ENABLE` with "Unable to setup interface". (#280, #284)
- **Four faults in how the box treats an operator's radio**, including every
  channel move waiting four seconds for a recovery that could not happen.
  (#283)
- **Container uplink detection** picked the veth into the container rather than
  the host's NIC. Found by deploying to the Ubuntu box and asking it what it had
  detected. (#287)
- **The adapter chart drew a straight line across time it never watched**,
  which read as a transfer winding down rather than as a gap. (#274)
- **An unbounded `iw scan` could wedge the contention poll silently.** One hung
  child process stopped every figure on the box refreshing, for the life of the
  daemon, with nothing reported. Scans are now bounded and a timeout names
  itself. (#308)
- **A missing `python3` was reported as a malformed profile**, sending the
  reader to inspect a file that was fine. (#303)
- **An ellipsis turned `802.11ax` into `802.11a`** — the one value in those rows
  that degrades into a different true-looking value rather than a visibly
  incomplete one. (#307)
- **Client folds now carry the standing facts their header drops** as the window
  narrows. (#305)
- **The adapter facts strip no longer changes height under a screen reader.**
  (#298)
- **The band plan's heading named half the choice it offers**: a cell in the 40
  or 80 row is a channel *and* a width together. (#253)

### Known limitations

- **Enforcement has never been measured end to end on the Pi.** (above)
- **The listen-only radio has only ever run on the container host.** Its Pi path
  is shared code that needs a reflash nobody has done.
- **The channel colouring overstates how clear a channel is.** A channel the
  scan produced no entry for is rated clear, which holds on 5GHz where an 80 MHz
  neighbour is spread across its whole block, and fails on 2.4GHz where a
  neighbour is recorded against its own channel only — so channels 2 to 4 read
  clear beside a channel 1 at 34% busy with a neighbour at −19 dBm. The ±4
  overlap window the data contract describes is not implemented.
- **No 802.11k beacon report has ever come back.** #228 stays open.
- **`boactl` does not cover the whole API.** #263 tracks the gap and what is
  deliberately not planned.

## [0.2.0] — 2026-09-06

**Wi-Fi control, so that every interaction between a client and a wireless
network can be exercised.**

0.1.0 conditioned the packets a device sent and received. It could not touch the
link those packets crossed: the device associated where it liked, stayed there,
and the box shaped whatever arrived. 0.2.0 adds the network side — association
and roaming, an access point going away, the signal fading with distance — so a
test can cover what a client *does about its link*, not only what its throughput
does while the link holds still.

### What you can make a client do

| Make it… | How | Can it refuse? |
|---|---|---|
| Move to a radio you name | `steer` — an 802.11v transition request | **Yes**, and it reports the refusal with its own candidate list |
| Move to a radio you name, or stay where it is — but land nowhere else | `gather` — the request, plus timed deny lists on every other radio | It can ignore the request; it cannot land on a denied radio |
| Leave a radio, and go anywhere but there | `evict` — the request, plus a deny list on the radio it is leaving | No |
| Re-associate immediately | `deauth` | No |
| Re-associate, less abruptly | `disassoc` | No |
| Lose this network for a stated time — and decide what to do about it: another SSID, or cellular | `deadzone`, with a stated reach | No |
| Cope with an access point that vanishes without warning | `disable AP` (with `broadcast_deauth=0`, nothing is announced) | No |
| Cope with an access point that says goodbye first | `deauth + disable AP` | No |
| Behave as though it were N metres further away — lower rate, more delay, jitter and corruption, uplink degrading first | distance model | n/a — this is shaping, not a request |
| Walk away from the router and back, on a clock, changing band as it goes | `walkabout` pattern | n/a |

The first row is an honest "yes", and it matters: a steer is a *request*, and a
device that declines it is behaving correctly. The deny lists exist precisely
because a request alone cannot choose the destination — see the limitations
below for where even those do not reach.

**Asking a client what it can see is NOT on this list.** The 802.11k beacon
request shipped (see below) and no client tested here has returned a report, so
it is not something you can currently make a device do. #228 stays open for
that reason.

### And the case for NOT using Wi-Fi at all

Everything above is conditioning applied on top of a radio baseline that moves
on its own. That is fine when the radio is the subject, and a poor trade when
it is not: a cap measured over the air inherits the air's variance, and a run
that cannot be repeated cannot be compared.

The wired downstream port is the answer when repeatability matters more than
convenience. Measured on this box, same adapter pair, 30s runs:

| Path | Result | Repeatability |
|---|---|---|
| **Wired, 2.5 GbE** | 2.35 Gbit/s up, 1.91 Gbit/s down | **within 1% across four runs** |
| Wi-Fi, 80 MHz, 802.11ax | 677 Mbit/s, sole client, ch 149 | a second, near-idle client moved a comparable measurement between 356 and 717 Mbit/s |

The second row is not a bad run — it is what shared airtime does. One 802.11n
station linked at 65 Mbit/s holds the channel roughly 18× longer per byte than
an 802.11ax one, so its presence halved the number **while transferring 4 KB of
its own traffic**. The 0.1.0 notes recorded the same effect from the
other side: the radio baseline drifts ~100 Mbit/s over 90s, which is larger
than most effects worth measuring.

So: **cable the device under test when it has a port, and use Wi-Fi when it
does not** — which for phones, tablets, watches and most streaming sticks is
always. The wireless path exists because those devices exist, not because it is
the better instrument. A `lan0` client is conditioned by exactly the same
policy, pattern and sub-class machinery as a wireless one, so a test can be
authored on the cable and repeated over the air, and the difference between the
two runs is then attributable to the radio.

Note the asymmetry reverses between them, which is worth knowing before reading
a result: the box **receives** faster than it sends on the cable (one saturated
CPU core on the transmit path, a USB NIC having a single queue pair), while on
Wi-Fi the uplink is the weaker direction.

110 commits. The interface was rebuilt around the change: one scrolling view of
fabric, adapters and devices, streaming rather than polling.

### Upgrading from 0.1.0

**A reflash is required. `deploy.sh` is not enough.**

The fast loop pushes the binary and its unit; everything this release depends on
below that line lives in the image. `scripts/customize.sh` grew by ~725 lines
and now writes:

- **udev rules naming radios by USB socket** (`KERNELS=="2-1"`), which is where
  `wlan-usb` and `wlan-usb2` come from. Without them the daemon looks for radios
  that do not exist under those names.
- **templated `hostapd@` units**, one per radio, replacing the single instance.
- **`rrm_neighbor_report` / `rrm_beacon_report`** in every generated config.
- **`unmanaged-devices=interface-name:wlan*`** so NetworkManager stops racing
  hostapd for the adapters.
- **`BOA_WLAN_PORT`** listing the radios actually found.
- `python3-venv`, for glances.

Deploying 0.2.0 onto a 0.1.0 card gives you a daemon that cannot find its
hardware. Build a fresh image, and **export your configuration first** — a
reflash replaces the whole filesystem, including
`/var/lib/infinite-streaming-boa/`:

```sh
curl -s http://infinite-streaming-boa.local/api/config > before.json   # keep this
./build.sh && ./flash.sh
curl -X POST --data-binary @before.json http://infinite-streaming-boa.local/api/config
```

**Downgrading is not clean, because the pattern migration is one-way.** On its
first load, 0.2.0 rewrites any pattern using the old kind names and **saves the
file back** (`patternstore.go`), so the upgrade is not redone every boot and an
exported file carries current names. After one boot, `patterns.json` holds
`deauth` / `disassoc` / `radio-off` / `disable-ap`; 0.1.0 does not know those
words and would skip those lanes **in silence**. Keep the pre-upgrade export if
going back is a possibility.

**Two new `.env` variables**, both optional and both defaulting to previous
behaviour:

| Variable | Meaning |
|---|---|
| `BOA_USB_MAX_CURRENT` | `1` lifts the Pi 5's 600 mA USB cap — **only with a 5A PSU or a powered hub** |
| `BOA_BRIDGE_MAC` | Pins the bridge MAC explicitly, rather than inheriting the WAN port's |

### Added

#### Several radios, discovered rather than assumed

- **Both radios serve at once**, dual-band like a router, instead of one radio
  chosen and the other left idle.
- **Radios are discovered and their channels planned**, replacing the hardcoded
  interface names. A third adapter is picked up on its own; `BOA_WLAN_PORT`
  carries the list the box actually found.
- **UNII-3 (channels 149–165) opened**, so two 5 GHz radios have somewhere to go
  that does not overlap.
- **A radio remembers its channel** and is put back when it drifts.
- **Channel choice is rated on measured airtime**, not on a headcount of
  neighbouring networks: a busy channel with one loud AP is worse than a quiet
  one with five idle ones, and a headcount cannot see that.
- **Per-radio detail** on each adapter — PHY profile, width, mode, MAC, BSSID,
  bridge port, country and beacon interval.

#### Moving a client between radios

- **steer** — ask one client to move (802.11v BSS transition). The box now
  advertises `bss_transition`, and **reports what the client actually said**:
  accepted, refused with its own candidate list, or no answer at all.
- **gather** — bring every client onto one named radio. Backed by **timed deny
  lists** on the radios it must not use, because a polite request alone does not
  choose the destination. Lifted when everyone has landed, or after 10s.
- **evict** — clear a radio, with the client free to land anywhere else.
- **deadzone** — take a client off a radio for a stated time, with a stated
  reach.
- The deny lists **OR with deadzones rather than replacing them**: an ACL is
  released only when neither the deadzone nor the pin still calls for it.
- **deauth** and **disassoc** as separate link events, in both directions.

#### Access point control, separate from the radio

- **Disable AP** takes the access point down while the radio stays powered —
  distinct from a power cut, which is now behind `developer=1`.
- **deauth + disable AP** announces the shutdown first; plain **disable AP**
  does not. With `broadcast_deauth=0` that difference is the whole point: a
  silent teardown is a different test from an announced one.
- **Radio power intent survives a hotplug** — a radio switched off stays off.
- **hostapd is restarted when its socket cannot answer**, keeping the bans, with
  a backed-off recovery poll rather than hammering a dead socket.

#### A device that behaves as though it were further away

- **Distance model**: tell a device to act as though it were N metres away and
  it is handed the rate, delay, jitter and corruption that signal level implies,
  in both directions — the uplink degrading first, as a real client's does.
- **walkabout**: walk a device away from the router and back on a clock, pinning
  it to a band at each step rather than asking.

#### Patterns

- **Link events as a pattern lane** — deauth, disassoc and deadzone alongside
  the rate and delay lanes, on the same clock.
- **Adapter patterns**: author AP blocks and band scans on per-radio lanes, with
  presets that rotate across however many radios the box has.
- **Scenarios**: play several devices on one clock, so two runs are comparable.
- **Per-field show/hide chips**, shared by the sliders and the lanes.

#### Interface

- **One scrolling view** — fabric, then adapters, then devices — replacing the
  Clients/Bridge tab split that preceded it.
- **Streaming instead of polling** for bridge state and the activity log.
- **The link's ceiling is visible**: PHY rate per radio, and a "to PHY" y-axis
  that scales a chart to what the link could actually carry.
- **The activity log** reads forwards, follows its live end, shows milliseconds,
  and sorts by the time it displays rather than the order it recorded.
- **Start and stop the box's own services** from the interface.

#### The box itself

- **glances on `:61208`**, linked from the header beside ntopng, for the box's
  own health — CPU, memory, SoC temperature, disk and per-process load. ntopng
  answers what the traffic is doing; nothing answered whether the appliance was
  keeping up, which is the question when a throughput figure is wrong because
  the Pi is thermally throttled rather than because the policy says so. Its port
  joins the management exemption, so a cap can no more throttle it than the
  dashboard. The link is hidden unless the port is actually listening.

  Installed from a pinned upstream wheel into a venv at `/opt/glances` rather
  than from apt: Debian ships glances `+dfsg` with the built web frontend
  stripped out, so `glances -w` from the `.deb` aborts on a missing
  `outputs/static/public`. The build asserts the frontend is present before
  enabling the unit. The venv also keeps ~90 packages of desktop plotting stack
  (matplotlib, tk, PIL, fonttools) that the Debian package depends on off a
  headless appliance.
- **`BOA_USB_MAX_CURRENT`** lifts the Pi 5's 600 mA USB cap, which a non-official
  PSU otherwise imposes — the root cause of USB adapter flakiness.
- **`br-lan` is pinned to the WAN port's per-board-unique MAC** before DHCP.
- **`deploy.sh` claims the box** before deploying and records who deployed what,
  so two people do not overwrite each other.

### Changed

- **Link event names unified.** `drop` → `deauth`, `nudge` → `disassoc`, `off` →
  `radio-off`, `apdown`/`ap-down` → `disable-ap`. Wi-Fi-specific names are kept
  where the action is a 1:1 mapping to something a Wi-Fi expert would expect.
  Patterns on disk are migrated on load, including all three historical
  spellings of the AP-down kind.
- **Patterns follow the adapters.** Add a radio and a lane appears; swap one for
  another and the pattern moves to the new adapter; remove one and its lane is
  shown greyed rather than silently dropped.
- **`AP.Enabled` requires the kernel link to be up**, not just hostapd's opinion
  of itself — a radio reported `ENABLED` for 20 minutes with its interface down.
- The measured RAM figures in `README.md` were re-taken on the same box. ntopng
  had grown from 278 MB to 386 MB with no change made to it, which is the
  unbounded-growth caveat already documented there showing up in practice.

### Fixed

Highlights of roughly 45 fixes; most of the interface ones exist because
something moved under the reader.

- **A gather's deny list came off while a client was still scanning**, so it
  landed on a radio the gather had denied. The 5s deadline that preceded it was
  a guess; re-association was then measured at 270 ms for one device and 41s for
  another, so no single number covers both.
- **A deadzone never lifted**, stranding a client off that radio.
- **A steer named 5 GHz destinations with an operating class that excludes
  them**, so the request was malformed.
- **Narrowing 80 MHz to 40 MHz left the 80 MHz centre behind**, and dropping to
  20 MHz left the HT40 side set.
- **A quiet radio could freeze the whole web interface** — the bridge view is
  now built on a timer, so no single radio can block it.
- **The activity panel went blank at every deploy** and stayed blank.
- **The adapter editor drew one pattern and played another.**
- **A merge dropped the link lane**, so layering `drop_1m` did nothing.
- **A pattern's radio blocks left no gap**, so the next radio went down before
  the previous one was beaconing again.
- **The stream could die without the page noticing** — `EventSource.onerror`
  does not fire for a socket that is simply never answered again, so the charts
  emptied while the daemon was still sending. A staleness watchdog now falls
  back to polling.

### Measured

- **The USB bus, not the radio, is the difference.** The same adapter at the
  same PHY rate (1200 Mbit/s, HE-MCS 11 HE-NSS 2) delivers **162 Mbit/s** on
  USB 2.0 and **~540 Mbit/s** on USB 3.0 — downlink falls 3.3× while uplink
  barely moves, because uplink was already limited by something else. A USB 3.0
  adapter in a blue port on a cable without SuperSpeed pins is otherwise
  indistinguishable from a working one.
- **The ~550 Mbit/s figure was a congested channel, not a bus limit.** The same
  adapter measured **677 Mbit/s** on channel 149, so the real ceiling sits above
  677 rather than at 550. Best case lands near **85% of PHY**.
- **Channel width, on the same radio:** 20 MHz 194 Mbit/s, 40 MHz 378 Mbit/s,
  80 MHz 677 Mbit/s.
- **Wired downstream, 2.5 GbE at both ends** (Realtek RTL8156, direct cable,
  SuperSpeed both ends, 30s runs): **2.35 Gbit/s** device → box, ~94% of line
  rate, and **1.91 Gbit/s** box → device. **Repeatable to within 1% across four
  runs** — the reason to prefer the cable when the radio is not the subject.
  1 GbE for reference: 924 Mbit/s.
- **The box sends more slowly than it receives, and it is structural.** CPU0
  saturates on the downlink run (idle bottoming at 1.6%, softirq peaking at
  93.6%) while the other three cores sit 65–100% idle. A USB NIC exposes a
  single rx/tx queue pair and USB completions run on the core servicing the
  xHCI interrupt, which is CPU0 for every USB device on the box. This is the
  opposite shape to Wi-Fi, where uplink is the weaker direction.

### Known limitations

Carried forward from 0.1.0, plus what this release measured:

- **Transmit power cannot be set on the mt7921u.** It reports `3.00 dBm`
  whatever it is set to — a known driver bug — but the *control* is inert too: a
  30 dB request across the adapter's whole legal range moved received signal by
  **nothing at all** (−22 dBm at both the ceiling and the floor). Attenuation is
  therefore not an available impairment, which is why the distance model exists.
  To test a weak link for real, move the device.
- **An 802.11v transition request from the onboard brcmfmac radio reaches
  nobody.** Counted over three hours: 0 responses from `wlan0`, 30 from the USB
  adapters, with the capability advertised and hostapd accepting the request
  without complaint. `steer`, and the polite half of `gather` and `evict`, are
  silent no-ops from that radio.
- **No client tested here has returned a beacon report.** The 802.11k request
  path ships and walks a mode ladder (active → passive → table), because a
  client need not support all three and hostapd refuses to transmit a mode the
  device has not advertised — measured 2026-09-06 on a MacBook:
  `does not support active beacon report`. The feature is therefore built but
  unproven against real clients, and the question it exists to answer — *why*
  a steer was refused — is still unanswered. **#228 is deliberately left open.**
- **A hotplug restarts the daemon**, which kills a running pattern without
  saying so.
- **Wi-Fi airtime is shared**, so conditioning is additive on top of a variable
  radio baseline, not absolute.
- **Uplink still cannot be verified with the on-box iperf3** — traffic to the
  box never crosses the WAN egress where uplink shaping lives.
- **A rotating (private) MAC still strands a device's policy and its measured
  ladder.**

## [0.1.0] — 2026-09-01

First tagged release. boa is a Raspberry Pi that sits invisibly in a network as a
transparent layer-2 bridge and conditions each client's connection independently,
in each direction, live from a web interface — with nothing installed on the
device under test and no cooperation from either end.

### Added

- **Per-device conditioning**, keyed by MAC so a policy survives a DHCP renewal,
  a reboot, and a client roaming between the wireless and wired ports. Rate,
  one-way delay, jitter and packet loss, set independently for downlink and
  uplink. Both IPv4 and IPv6 are conditioned by the one policy.
- **netem enforces the rate, not HTB**, so the inter-packet spacing a player
  measures is that of a real slow link rather than a token-bucket burst at
  segment start. HTB is kept only as a classifier and per-client byte counter.
  The netem queue depth is computed from rate × delay, so configured loss is the
  only loss.
- **Bursty packet loss** via netem's Gilbert–Elliott model (mean loss + mean
  burst length), probed once at startup and disclosed as a capability rather than
  silently falling back to uniform loss.
- **Reorder and corrupt** impairments, disclosed by use.
- **Sub-classes**: condition part of a device's traffic differently, matched on
  destination port, network and/or protocol — "video from this CDN gets 1.5 Mbps
  and 200 ms, everything else stays clean."
- **Time-varying patterns** composed on one clock, edited as lanes (rate, delay,
  jitter, loss) with draggable keyframes. Built-in scenarios: `valley`,
  `pyramid`, `ramp_up`, `ramp_down`, `square_wave`, `transient_shock`,
  `blackhole`, and delay/loss/reorder/corrupt climbs.
- **Rendition-ladder measurement**: sweeps the cap downward and records where a
  player's throughput settles, with no manifest and no payload inspection. Kept
  per service, and carries its provenance (`measured` vs `typed`).
- **Passive discovery**: clients are learned from forwarded traffic; names come
  from mDNS announcements the device already makes. Presence comes from the radio
  and the bridge, never from a DHCP lease.
- **Web interface** (`:80`): live per-device throughput with five minutes of
  server-side history, a folding device list, and controls that state their own
  limits (shared Wi-Fi airtime, per-direction delay). The footer now shows the
  running build's version.
- **ntopng** (`:3000`) watching the bridge, with per-device deep links; and an
  **iperf3** server (`:5201`) for measuring the unshaped ceiling a cap sits under.
- **Config export/import** (`scripts/config.sh`, `GET`/`POST /api/config`):
  every device's policy, sub-classes, ladders and pattern in one document,
  validated in full before anything is written.
- **Radio selection**: prefers a USB Wi-Fi adapter (mt7921u, run by hostapd) over
  the Pi 5's onboard radio (brcmfmac, run by NetworkManager), failing back
  automatically when the adapter is unplugged.
- **Versioning**: the build stamps a git-derived version into the binary
  (`scripts/version.sh`), surfaced through `/api/state`, `boad -version`, and the
  interface footer.

### Measured

- A downlink cap lands within ~6% of target across the verified range
  **0.25–50 Mbps** — the shortfall being the Ethernet/IP/TCP framing a cap counts
  and a payload byte-count does not, the same overhead a real link imposes.
- A cap set **above** the link's own ceiling (700 Mbps and 1 Gbps over a
  ~510 Mbps Wi-Fi link) costs about **1.5%** — i.e. putting netem in the path is
  nearly free at these rates. Measured by interleaving capped and uncapped runs,
  because the radio baseline drifts ~100 Mbps over 90 s, more than the effect.
- A configured 200 ms one-way delay measured 200.6 ms RTT.

### Known limitations

- **Wi-Fi airtime is shared**, so conditioning is additive on top of a variable
  radio baseline, not absolute. A wired emulator gives a number for a report;
  boa does not.
- **Uplink is untested at any rate**, and cannot be measured with the on-box
  iperf3 (traffic to the box never crosses the WAN egress where uplink shaping
  lives).
- **No per-station signal level** on the Pi 5's onboard radio in AP mode;
  transmit failures stand in. The USB mt7921u radio does report signal.
- **Encrypted payloads stay encrypted** — no manifest inspection; that belongs on
  the origin path, which composes with boa.
- **A rotating (private) MAC strands a device's policy and its measured ladder.**
  Pin the address on any device you control before a long measurement.

[Unreleased]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/releases/tag/v0.1.0
