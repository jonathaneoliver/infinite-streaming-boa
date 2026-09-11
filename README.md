# boa

[![Release](https://img.shields.io/github/v/release/jonathaneoliver/infinite-streaming-boa?sort=semver)](https://github.com/jonathaneoliver/infinite-streaming-boa/releases/latest)
[![License: MIT](https://img.shields.io/github/license/jonathaneoliver/infinite-streaming-boa)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/jonathaneoliver/infinite-streaming-boa?filename=daemon%2Fgo.mod)](daemon/go.mod)
[![Platform: Raspberry Pi 5](https://img.shields.io/badge/platform-Raspberry%20Pi%205-c51a4a)](#1-a-raspberry-pi-5-from-an-image)
[![Platform: Linux container](https://img.shields.io/badge/platform-Linux%20container-2496ed)](#2-a-linux-host-from-a-container)
[![Sponsor](https://img.shields.io/badge/support%20this%20project-ea4aaa?logo=githubsponsors&logoColor=white)](https://github.com/sponsors/jonathaneoliver)

Part of the infinite-streaming family. The repository is
`infinite-streaming-boa`; `boa` is the appliance itself — the binary, the
hostname, the SSID. The name is apt: a boa constricts and releases, and the box
does the same to a link — tightening the cap and easing it off over time, most
visibly in the `valley` pattern below.

[`PRD.md`](PRD.md) is the product behaviour source of truth.

An appliance that sits invisibly in your network and conditions each client's
internet connection independently — rate, latency, jitter and packet loss, per
device, in each direction, adjustable live from a web interface.

It also **drives the radios those clients are associated to**, from the same
page: move one to another channel, take its access point down, deauthenticate or
disassociate a device, or push it onto a different radio. Conditioning the link
and disturbing the radio are separate axes, and a run can use either or both.

It runs on **either of two targets, on equal terms**: a Raspberry Pi 5 flashed
from an image, or a container on an ordinary x86_64 Linux host. The same daemon
binary and the same interface serve both, and no code under `daemon/` differs
between them — see [Two ways to run it](#two-ways-to-run-it).

It is a **transparent bridge**, not a router. Devices under test keep their
normal addresses on your normal network, discovery protocols keep working, and
the box never appears as a hop in `traceroute`. Nothing being tested can tell it
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
                               eth0                  ← WAN port, conditioning
                        ┌────────────────┐             is applied here
                        │      boa       │
                        └────────────────┘
                          br-lan  (one layer-2 segment)
              ╱                    │                    ╲
        wlan-usb-46c7            wlan0            lan-usb-6518
          Wi-Fi 5GHz          Wi-Fi 2.4GHz        USB ethernet
              ╲                    │                    ╱
                     wireless clients        wired client
                              ╲             ╱
                        conditioned identically
```

The port names above are the Pi's. On a container host the shape is identical
and the names differ: the uplink is `wan0`, a veth whose other end sits in a
bridge on the host, and the downstream ports are named after the device rather
than the socket — `wlan-usb-<last four of the MAC>` and `lan-usb-<…>`.

![The 0.2.0 interface: three adapters with their own timeline, an iPhone being
walked away from the router by the walkabout pattern, and the activity log
recording two clients refusing a steer](docs/images/interface-0.2.0.png)

Above: **0.2.0**, and the whole page at once. Three radios in the rack, each with
its own controls and a shared timeline; an iPhone 676 s into a 900 s `walkabout`,
being handed steadily worse conditions as it is walked away from the router; and
the activity log at the top recording what the radios were asked to do and what
the clients said back — including *"refused, offering its own list of candidates
instead"*, which is a client declining a steer and is the honest outcome rather
than a failure.

The two adapters not carrying clients are still fully controllable, which is the
point of the rack: `disable AP`, `deauth + disable AP`, `deauth`, `disassoc`,
`evict`, `gather` and `scan` sit on every radio whether or not anything is
associated to it.

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
- **Conditions the Wi-Fi link itself, not only the packets** — deauth,
  disassoc and a timed deadzone, per client, as
  one-shot buttons or as a lane on a pattern. A phone's path monitor and a
  player's throughput estimator react to the link going *down*, which netem
  cannot express. Needs the USB radio (hostapd); the onboard radio has no
  control interface, so the buttons only appear when it can act. **Do not run
  these inside a building with wireless IPS containment** — it transmits the
  same deauthentication frames uninvited, and a run cannot tell its drops from
  yours. See the warning under [Hardware](#hardware).
- **Moves clients between its radios, and is honest about which moves are
  guaranteed.** The box serves one SSID from every radio it has, so a client can
  be pushed around the box the way a real network pushes it around a building.
  Three controls, three different promises:

  | control | what it does | can the client refuse? |
  |---|---|---|
  | **steer** | asks one client to move (802.11v BSS transition) | **yes** — and whether it does is the measurement |
  | **gather** | denies it on every radio but the destination, then moves it | no — there is nothing to refuse |
  | **evict** | denies it on the radio being emptied, then moves it | no, but *where* it lands is its own choice |

  802.11 has no request that *places* a station on a BSS, so `gather` and
  `evict` do not ask: they remove the alternatives with hostapd's deny ACL and
  let the client's own rescan reach the only answer left. That is how band
  steering works on real controllers. The bans are timed, lift as soon as every
  affected client has landed, and a new command supersedes the last one rather
  than combining with it.
  `steer` deliberately stays a request, because "does this phone honour a
  transition?" is a question worth answering and a control that removes the
  choice cannot answer it.
- **Takes an access point down and back, silently or with a goodbye.** A router
  losing power tells nobody, so that is the default: the box suppresses
  hostapd's start/stop broadcasts, which would otherwise land on exactly the
  clients a measurement is watching. The `deauth +` variants send one
  deliberately — individually to associated stations on the way down, by
  broadcast on the way up to clients still holding a stale association.
- **Stays invisible.** Clients keep their existing addresses on your existing
  subnet; the Pi is not a hop and does not appear in `traceroute`.
- **Names devices from mDNS**, so the list reads as devices rather than MACs.
- **Folds the list** when there are several, keeping a sparkline and current
  throughput per direction on each folded row.
- **Keeps five minutes of history server-side**, so a browser refresh does not
  start from a blank chart.
- **Ships ntopng** on `:3000`, watching the bridge, with per-device deep links
  from each card for traffic breakdown and nDPI-labelled flows. **Pi image
  only** — the container omits it, and the interface says why.
- **Ships glances** on `:61208`, linked from the header — the appliance
  watching itself rather than the traffic: CPU, memory, SoC temperature, disk
  and per-process load, for when a throughput number is wrong because the Pi is
  throttling rather than because the policy says so. **Pi image only**, on the
  same terms.
- **Ships an iperf3 server** on `:5201`, so the ceiling a cap has to sit under
  can be measured without installing anything on the device under test. It
  measures the link **unshaped** — see below.

## Who this is for, and why the Wi-Fi control matters

Anyone building **a mobile app, or a Wi-Fi connected device**, who needs to know
how the client's relationship with the access point affects it — not only how
much bandwidth it gets. Those are different questions, and until this box could
drive its own radios only the second one was testable.

**It is a bench tool for one engineer at a time.** That is a design position
rather than a missing feature, and it decides a lot: there is no login on any
port, no notion of a user, no tenancy, and no way to give two people different
views of the same box. Policies, patterns and radio state are all *the box's*
state, so a second person changing something changes it for the run you are in
the middle of. `deploy.sh` takes a short-lived claim on the hardware before it
pushes, which stops two deploys colliding — it is a courtesy between colleagues
sharing a box, not access control, and nothing stops a browser doing whatever it
likes meanwhile.

So: one box per engineer, on a network they control. It is not shared lab
infrastructure, not a CI runner, and not a certified instrument (see
[Non-Goals](PRD.md#3-non-goals)). If several people need it at once, the answer
is several Pis — which is most of why it targets a £60 board and a build script
rather than a rack appliance.

The link is not a dial. A real client is continuously deciding *which* access
point to be on, whether to roam, and what to do when the one it is using stops
answering. Those decisions surface as a stall, a re-buffer, a dropped upload, a
silent switch to cellular, or a device that simply never comes back — and none
of them reproduce by lowering a rate limit.

**For a device rather than an app, the case is stronger, for two reasons.**

The first is that the Wi-Fi behaviour *is* the product. How quickly a streaming
stick reconnects after the access point disappears, whether a camera backs off
sensibly or hammers the network, whether a speaker honours a transition request
or clings to a radio it can barely hear, how a thermostat behaves when the band
it prefers goes away — those live in firmware, in the driver and supplicant, and
partly in the silicon's own limits.

Firmware does ship over the air; that is not the problem. The problem is the
route it takes. **A fix for a device's Wi-Fi has to travel over that device's
Wi-Fi**, so the population most in need of it is the population least able to
receive it — a device that drops its association every few minutes may never
finish the download that would stop it. Fleet-wide rollouts are staged over
weeks or months, some units never take one, and a bad radio update is close to
unrecoverable in a way a bad app release is not, which makes vendors
appropriately slow to ship them.

So it is not that a doorbell cannot be fixed after it ships. It is that fixing
it is slower, riskier, and reaches fewer of the devices that need it — which
makes catching the behaviour beforehand worth much more than it is for an app
that can be replaced on Thursday.

The second is that **you usually cannot instrument the thing at all.** A TV, a
console, a set-top box, a camera or a smart speaker takes no proxy setting, no
installed certificate and no test harness — which is the constraint this whole
appliance is designed around. It conditions forwarded frames, so it needs
nothing from the device, and it drives the radios the device is associated to,
so it can ask the questions above of hardware that offers no other way in.

The awkward part of testing a device is that its worst behaviour tends to be
the behaviour it only exhibits in a customer's house six weeks later. Roaming
between two access points, an AP that vanishes without warning, a band that
gets crowded at 8pm — these are ordinary domestic events that a bench with one
router and good signal never produces. This box produces them on demand, on a
schedule, and records what it did.

### Testing the telemetry, not just the product

The case this was built for. Video **QoE** systems increasingly collect Wi-Fi
data alongside playback events — signal level, roams, disconnects — so that a
re-buffer can be attributed to the network rather than to the CDN or the player,
and so that a badly installed device can be spotted from its behaviour: one that
roams constantly, or drops its association several times an hour.

That attribution is only as good as the telemetry underneath it, **and the
telemetry itself is almost never tested.** Not because nobody wants to, but
because a real network will not produce a known sequence of Wi-Fi events on
demand. You cannot ask your office AP to deauthenticate one phone at 30s, go
away for eight seconds at 90s, and hand it to a different radio at 150s — so the
usual practice is to trust the field data and hope.

boa is that missing instrument. Script the radios, then compare:

```
  what boa did                          what the QoE report says
  ────────────────────────────────      ──────────────────────────────
  t+30s   steer to the other radio  →   roam recorded? same second?
  t+90s   disable AP for 8s         →   disconnect recorded, or a gap?
  t+150s  deauth                    →   counted once, or as three?
  t+210s  hold at 25 metres         →   signal drop reflected at all?
```

Both directions of error become visible, and both matter:

- **events the telemetry missed** — a roam that never reached the report, so the
  field data understates how often this happens;
- **events it invented, mistimed or double-counted** — worse, because it makes a
  healthy install look faulty and sends somebody to a customer's house.

The box keeps its own event log with millisecond timestamps, and a run can be
captured off it as it happens:

```sh
curl -sN http://infinite-streaming-boa.local/api/events/stream > run.ndjson
```

That file is the ground truth to diff a QoE report against. It is recorded by
the thing that *caused* the events rather than by the device experiencing them,
which is the whole point — a client's own account of what happened to it is the
thing under test.

### Driving it from a script, not from the page

The interface can author quite involved behaviour — lanes, keyframes, loops, a
pattern that runs for fifteen minutes and repeats — and while you are working
out what a device *does*, clicking is the fastest way to find out.

For anything you intend to compare, drive it from a script instead. Everything
the interface does is an HTTP call to the same API you have, with **no
privileged second interface behind it**: the page is a client like any other.
In practice this box is driven as often from a shell — increasingly from Claude
Code on the host, which can read the run back and decide what to do next — as
it is from the browser.

**A/B is where this stops being a convenience.** A comparison is worth only as
much as the sameness of everything you did not deliberately change, and hand
driving introduces variance in precisely the places that matter: when the
impairment started, how long it ran, whether the device had settled first, how
long you took to click the second button. A script makes run A and run B
identical except for the one line that differs.

**Use `boactl`, not `curl`.** It imports the daemon's own types, so it cannot
drift from what the box sends, and it reports a bad status line instead of
handing you an HTML error page that decodes into a zero-valued struct and reads
exactly like a healthy empty answer. That failure is silent, and in an A/B it
means comparing two nothings.

```sh
export BOA_BOX=infinite-streaming-boa.local
DEV="Apple TV"
RADIO=$(boactl -json bridge | jq -r '.ifaces[] | select(.wireless and .serving) | .name' | head -1)

run () {                                    # $1 is A or B
  boactl shape "$DEV" -clear
  boactl events -follow > "run-$1.ndjson" & # ground truth, from the box itself
  cap=$!
  sleep 30                                  # let the device settle
  if [ "$1" = B ]; then                     # the ONLY difference
    boactl radio "$RADIO" gather
  fi
  sleep 120
  kill $cap
}

run A && run B
diff <(jq -r 'select(.kind).kind' run-A.ndjson) \
     <(jq -r 'select(.kind).kind' run-B.ndjson)
```

**Ask the box for the interface name, never hardcode it.** Radios are named
after the device rather than the socket — `wlan-usb-46c7`, from the last four
of its MAC — so a name is stable across replugging and across which hole it is
in, and is different on every box. `boactl bridge` is where it comes from.

`boactl -h` lists what exists and `boactl <command> -h` the flags. It does not
cover the whole API; [#263](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/263)
tracks the gap. For anything it does not cover, `curl` against the same API is
fine — the page is a client like any other — but check the status code.

**Claude Code drives this through skills rather than by hand.** The
`verify-on-hardware` skill reads back the queueing disciplines, classes and
filters from the kernel and asserts against them, which is the same discipline
`boactl probe` applies: assert, exit non-zero, never print numbers to be
eyeballed.

Two things follow from scripting that are hard to get any other way: a run can
be **repeated exactly**, weeks later, by someone else; and the box's own event
capture gives you a machine-readable record of what it did, alongside whatever
the device under test reported. That pairing is what makes the QoE comparison
above tractable rather than a matter of watching two screens.

### Other uses this shape supports

- **Roaming behaviour**: does the app survive a mid-stream move between access
  points, and how long does it take to recover?
- **The captive-portal and cellular-failover paths**: what an app does when Wi-Fi
  is associated but useless is rarely tested, and is often where the worst
  behaviour lives (see the iOS probing note in `docs/BACKLOG.md`).
- **Poor-install signatures**: reproduce the frequent-switching or repeated-drop
  pattern deliberately, and confirm whatever is supposed to detect it does.
- **A weak link without a faraday cage**: the distance model degrades a device's
  link the way distance would, uplink first, without moving anything.

## Saving and restoring a configuration

**Do this before a reflash.** Writing a new card replaces the whole filesystem,
`/var/lib/infinite-streaming-boa/` included — every device's policy, sub-classes,
measured ladders and patterns. The interface has **export config** and **import
config** buttons in the header, and the same document is available over HTTP:

```sh
curl -s http://infinite-streaming-boa.local/api/config > boa-config.json
# ... reflash, rejoin the AP ...
curl -X POST --data-binary @boa-config.json \
     http://infinite-streaming-boa.local/api/config
```

`scripts/config.sh` wraps the same two calls. The whole document is validated
before anything is written, so a partial or malformed import changes nothing.

It is also how a scenario travels: a configuration exported from one box
reproduces the same policies on another, which is what makes a scripted
comparison repeatable by somebody else.

## The controls, one by one

The screenshot above has more in it than the walkthrough so far implies. Working
down:

### Presets, before the sliders

Eight starting points, each a plausible whole link rather than a single number —
click one, then adjust. Their notes are the summary; the exact figures live in
`ui/src/types.ts`.

| Preset | Downlink | Shape |
|---|---|---|
| Clean | — | no conditioning; the way back from anything |
| Fibre | 100 Mbps, 8 ms | fast and boring, near-zero loss |
| Cable | 30 Mbps, 30 ms | a little bursty loss |
| 4G good | 20 Mbps, 50 ms | 0.1% loss in bursts of 4 |
| 4G weak | 3 Mbps, 120 ms | 1% loss in bursts of 10 |
| 3G | 1.5 Mbps, 200 ms | 1.5% loss, the classic bad-network test |
| Satellite | 25 Mbps, 600 ms | fast but very late — geostationary latency |
| Lossy | 10 Mbps, 5% | loss in bursts of 20, a radio fade rather than a full queue |

Uplink is set separately by each preset and is always the smaller number, as it
is on a real access link.

**The loss shapes are chosen, not fitted.** Congestion-like presets keep loss
near uniform, because a router dropping from a full queue loses single packets;
the mobile ones use long bursts, because a radio fade loses a run of them. No
preset has been fitted to a measured link, and none claims to be.

### Distance, and what kind of device is at that distance

The **distance** slider tells a device to behave as though it were further away,
handing it the rate, delay, jitter and corruption that signal level implies —
uplink degrading first, as a real client's does.

The **device** selector next to it says what is at the far end, because that
changes the answer. The model works in dB relative to a laptop:

| | Hears the AP | Is heard by the AP |
|---|---|---|
| **laptop** | reference | reference |
| **phone** | −2 dB | −6 dB |
| **watch** | −5 dB | −11 dB |

Bigger radios with more antennas are heard better at the same distance, so a
watch's uplink dies several metres before a laptop's does. The signal readout
shows both directions and marks them **modelled**, because the radio has not
moved and the real RSSI beside it has not changed.

### Making the radio itself worse

Under each adapter, separate from per-client conditioning, because these affect
every client on that radio:

- **A generation ladder** — `clean` (the way back), then `11ac`, `11n` and
  plain OFDM, each dropping the radio a rung. **Which rungs appear depends on
  the radio and the band it is on**, because a rung above its ceiling cannot be
  reached and VHT does not exist on 2.4 GHz at all — so `11ac` is simply absent
  there, and the OFDM rung calls itself `11a` on 5 GHz and `11g` on 2.4 GHz.

  There is no `11ax` button: on a capable radio that is what `clean` already
  returns you to, and on one that cannot do ax it would be a control that
  fails. Width is not among them either — it lives in the channel plan, where a
  channel and a width are picked together.

  **`11n` also caps the width at 40 MHz**, because HT has no 80 MHz channel, so
  a run against `clean` moves two things rather than one.
- **Power save**, in two rungs. `power-save` is DTIM 3 at a 100 ms beacon with
  U-APSD off, which is a common access point default and delivers buffered
  downlink about every 300 ms. `power-save deep` is DTIM 10 at 300 ms — roughly
  ten times any real access point, and openly a stress test rather than a
  mimicry. You cannot control a client's power management from outside, so the
  only lever is the half of the contract the access point owns, and exaggerating
  it is what makes a sleeping client's behaviour visible instead of buried in
  variance.
- **RTS/CTS** — request-to-send before every frame, or above 512 or 1000 bytes.
  Every frame roughly halves throughput and adds per-frame latency; it is what
  a radio does when it believes there are hidden nodes, and the control frames
  go at a basic rate every station can hear, so the cost does not shrink as your
  data rate grows. The higher thresholds are where a real access point sits when
  it uses RTS at all.
- **Fragmentation** — split frames at 256, 512 or 1024 bytes, so each one needs
  more airtime and more acknowledgements. With any error rate the retry cost
  explodes superlinearly, because losing one fragment costs the whole frame.

Both thresholds are phy-level settings that apply without restarting the access
point, so nobody is dropped when you change them.

### Asking the client what it sees

**measure** sends an 802.11k beacon request for each radio the box could steer
to, so a refusal can be explained by what the client can actually hear rather
than guessed at from the wrong end of the link. It walks a mode ladder — active,
then passive, then the client's scan table — because a client need not support
all three and hostapd refuses to transmit a mode the device has not advertised.

**No client tested here has returned a report.** The path ships and is exercised
by the button; whether any given device participates is up to its firmware. See
[#228](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/228).

### Reading the charts

The toolbar above the device list applies to every chart at once:

- **Range** — 1m / 5m / 15m / 1h. Shortening never refetches; the client already
  holds an hour, so a shorter window is a full-resolution slice of it.
- **Y-axis** — `auto` scales to the data, `to cap` to the policy being enforced,
  `to PHY` to what the link could carry, and `fixed` to a number you give. `to
  PHY` uses the highest ceiling in the visible window rather than the current
  one, so the axis does not jump every time the MCS changes.
- **Mean over** — 10s / 30s / 60s, the window behind the smoothed line. Compare
  an `iperf3` whole-run figure against this, not against the live trace.
- **Series** — which of live, mean and PHY are drawn.

### walkabout

A built-in pattern that walks a device away from the router and back on a clock
— 900 seconds, 25 keyframes — stepping the modelled distance out and in again
while **pinning the device to a band at each step** rather than asking it to
move. It is the closest thing here to carrying a device around a building, and
it repeats, so a player can be watched through several laps.

> **Not calibrated. Your mileage will vary.**
>
> The distances are indicative, not measured. The path-loss exponents are
> ITU-R P.1238's tabulated values — N = 28 residential, N = 31 for a 5 GHz
> office — and that recommendation says plainly that *site-calibrated* values
> are needed for real link planning. Beyond the rate anchors, the mapping from
> signal to delay, jitter and corruption is plausible rather than fitted: it has
> the right shape (uplink degrades before downlink, corruption arrives before
> loss, both negligible until the headroom is nearly gone) without the right
> magnitudes for *your* building.
>
> This is why every output of the model is labelled **typed** rather than
> **measured**, and why the real RSSI is shown beside the modelled one and
> allowed to disagree with it. Use walkabout to compare two runs, or two
> firmware builds, against the same synthetic journey — not to claim a device
> works at 25 metres.
>
> [#221](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/221)
> is the open work to replace the assumed curve with a measured one.
> `docs/DATA-CONTRACT.md` Source S carries the confidence of each number
> individually.

### What this replaces, and what it does not

There are three ways to test a device on a weak link. This box is the cheapest
of them, and the least faithful.

**1. A programmable step attenuator, in an RF-shielded enclosure.** What a device
lab uses, and better than the other two in every way that matters to a
measurement: the level is calibrated in dB, it repeats exactly, and the enclosure
removes the neighbours — which, per the warning under [Hardware](#hardware), is
most of what makes testing over the air hard. It also costs thousands, needs coax
to antenna ports many consumer devices do not have, and puts the device in a
metal box where nobody can see the screen or touch it.

**2. A radio whose driver actually implements transmit power control.** Then the
access point can genuinely turn itself down, the client really does receive less
signal, and everything the client decides from that signal responds. Cheap, and
it would be a real improvement on what is here.

It is not available on this hardware. The `mt7921u` reports `3.00 dBm` whatever
it is set to — a known driver bug — and the *control* is inert as well: a 30 dB
request across the adapter's whole legal range moved received signal by nothing
at all ([measured](#access-point-performance)).
[#202](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/202)
carries the one-line check to re-run after any kernel change, and what to do if
the upstream patch ever lands.

Worth knowing even then: lowering the **AP's** power weakens the downlink only.
The client still transmits at full power, so the uplink stays strong — which is
the opposite asymmetry to real distance, where the smaller radio's uplink fails
*first*. A working txpower control would be a better instrument than the model,
not an equivalent one.

**3. The distance model — what this box does.** It does not weaken the radio; it
applies the *consequences* of a weaker signal to the traffic: lower rate, more
delay and jitter, corruption before loss, uplink degrading first.

| | Attenuator + enclosure | Real txpower control | boa's distance model |
|---|---|---|---|
| Cost | thousands | cheap | already in the box |
| Setup | coax, sealed chamber | a driver that works | a slider |
| Calibrated | yes, in dB | roughly | **no** — see above |
| Isolated from other networks | yes | no | no |
| Device usable while testing | not really | yes | yes |
| Degrades both directions | yes | **downlink only** | yes (modelled) |
| **Changes what the radio experiences** | **yes** | **yes** | **no** |
| Available here today | — | no ([#202](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/202)) | yes |

**That last-but-one row is a hard limit, not a rough edge.** You can see it in
the screenshot at the top of this page: the iPhone is being walked to a modelled
**55 ft / −64 dBm**, while its actual signal reads **−27 dBm** and its PHY rate
is still **1201 Mbit/s**. Both numbers are shown side by side and allowed to
disagree; the modelled one is labelled as such.

So anything the device decides **from its own measurement of the signal** is
untouched:

- roaming and band-steering triggers, which fire on RSSI thresholds
- rate adaptation and MCS selection
- antenna diversity, beamforming, transmit power control
- power-save behaviour that depends on link margin

If you are testing **how an application behaves on a poor link**, the model is
the right tool and an enclosure is overkill. If you are testing **the device's
own radio decisions** — when it roams, how it picks a band, what its firmware
does as margin disappears — you need real attenuation, and this will quietly
tell you nothing.

**The one thing boa has that an attenuator does not:** it can *force* the roam
instead of waiting for the device to choose one. `steer`, `gather`, `evict` and
`deadzone` change which access point a client is on directly — so the roam
**outcome** can be exercised even though the roam **trigger** cannot.


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

### Isn't this RaspAP, or a Pi access point?

The question the table above does not answer, because
[RaspAP](https://github.com/RaspAP/raspap-webgui) is not a link conditioner and
does not belong in a list of them — but it *is* the first thing "Raspberry Pi,
Wi-Fi, web interface" brings to mind, so the difference is worth stating.

RaspAP describes itself as *"the easiest, full-featured wireless router setup
for Debian-based devices"*, and that is exactly what it is: a management
interface over `hostapd`, `dnsmasq` and the rest, with DHCP settings,
WireGuard/Tailscale/OpenVPN, SSL, ad blocking, captive-portal integration and
themes. It supports a bridged AP as well as the default routed one, so the
distinction is **not** bridge-versus-router.

The distinction is what the access point is *for*.

| | RaspAP | boa |
|---|---|---|
| Purpose | build and run a good access point | exercise and impair a client's connection |
| The AP is | the product — you want it up | an instrument — half the features exist to take it *down* |
| Per-device impairment | none | rate, delay, jitter, loss, reorder, corrupt, per direction |
| Per-device link control | none | deauth, disassoc, steer, gather, evict, deadzone |
| Scripted behaviour over time | none | patterns and scenarios on a clock |
| Wants to be your network | yes | emphatically not |

**They are complements, not competitors.** If you want a Raspberry Pi to serve
Wi-Fi well — for a workshop, a camper van, a spare room — RaspAP is a better
tool than this one and is not trying to do what this does. boa runs `hostapd`
too, but everything built on top of it is in service of making a client's life
difficult on purpose and recording what the client did about it. Its access
point is not something you would want to depend on, and several of its controls
exist specifically to destroy it mid-run.

So: if the access point is the goal, use RaspAP. If the access point is the
apparatus and the client's behaviour is the measurement, that is this.

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

## Two ways to run it

boa runs on a Raspberry Pi 5 flashed from an image, or as a container on an
ordinary x86_64 Linux host. **Neither is the reference and neither is a port.**
The same `boad` binary and the same embedded interface serve both, and nothing
under `daemon/` is conditional on the target — the container reuses even
`radioplan`, copied out of the Pi overlay unchanged, so a channel plan made on
one cannot drift from a plan made on the other.

What differs is only where the box gets four things a bridge needs: the bridge
itself, the hostapd configs, hotplug handling, and process supervision. The Pi
takes them from the distribution. The container brings its own.

| | Raspberry Pi 5 | Linux container |
|---|---|---|
| Install | Flash an image, once | `scripts/docker-deploy.sh <host>` |
| Update the daemon | `scripts/deploy.sh`, ~10 s | `scripts/docker-deploy.sh`, rebuild and restart |
| Reflash needed for | Units, packages, kernel settings, network profiles | Nothing — the image is rebuilt every deploy |
| Bridge built by | NetworkManager | The container entrypoint |
| AP configs written by | `radioplan`, at boot | `radioplan`, identical, at start and on hotplug |
| Hotplug handled by | A udev rule | A udev rule on the host, plus a poll in the entrypoint |
| Supervision by | systemd units | The entrypoint, plus a `systemctl` shim for the two calls the daemon makes |
| ntopng and glances | Included | Absent by decision; the interface offers to start them and says why it cannot |
| Host is left | Dedicated to boa | Still itself, with its NIC bridged and the USB adapters given away |

Throughput is comparable so far, which is the point of listing both. These are
separate machines with separate radios, so read the table as "neither target is
obviously the bottleneck" rather than as a benchmark of one against the other.

Each `not measured` below is a run nobody has done yet, not a figure too dull to
record.

| Unshaped, `iperf3` **to** the box | Raspberry Pi 5 | Linux container |
|---|---|---|
| Wired downlink, 2.5 GbE | 1.91 Gbit/s | 1.95 Gbit/s |
| Wired uplink, 2.5 GbE | 2.35 Gbit/s | 2.35 Gbit/s |
| One radio, 80 MHz 802.11ax | 495–683 Mbit/s | 454 Mbit/s |
| Two radios carrying clients at once | *not measured* | *not measured* |

The wired figures agree to within 2%, on two machines with different CPUs, which
says the 2.5 GbE adapter rather than the target is the limit in both.

| Through the box, or under a cap | Raspberry Pi 5 | Linux container |
|---|---|---|
| Wired through to an external host, down / up | *not measured* | 1.95 / 2.35 Gbit/s |
| Wireless through to an external host | *not measured* | 423 Mbit/s |
| Wired and wireless concurrently | *not measured* | 1819 + 586, and 1778 + 449 Mbit/s |
| Enforcement, against a 90/40 Mbit/s cap | *not measured* | 86.0 / 38.1 Mbit/s |
| Enforcement over the radio, 60/20 cap | *not measured* | 57 down / 13.6 up Mbit/s |

**The second table is the container's, and the gap there runs the other way.**
Conditioning has only ever been measured end to end on the container host: the
Pi's published figures are all ceilings taken against the box itself, which is
the measurement that cannot show a cap working. Running that set on the Pi is
the more valuable missing work of the two.

### 1. A Raspberry Pi 5, from an image

The self-contained option. One card, one power supply, nothing installed on any
other machine, and the box is disposable — a bad state is a reflash away. It
boots to a working appliance on an air-gapped bench, because the image carries
every package it will ever need.

Choose it when the box should be a fixed piece of bench equipment, when you want
ntopng and glances alongside the conditioner, or when the machine that runs it
should not also be doing anything else.

See [Hardware](#hardware) for the parts and [Build an image](#build-an-image)
for the build.

### 2. A Linux host, from a container

The option that uses hardware you already have. An x86_64 machine with two USB
adapters becomes the same appliance, with more CPU behind the transmit path and
no card to flash. The daemon and the interface are built on your workstation and
only the finished artefacts are shipped, so the host needs neither Go nor node.

Choose it when you have a spare Linux box, when you want faster deploy cycles
than a card allows, or when the conditioning has to sit next to something else
already running on that machine.

It is **not** a zero-footprint option, and the trade should be made with open
eyes. Docker cannot hand a physical network device to a container, so a set of
root-owned helpers on the host does the work instead — see
[What this puts on your host](#what-this-puts-on-your-host).
Issue [#286](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/286)
tracks two simpler arrangements that would remove most of them.

See [Run it as a container on a Linux host](#run-it-as-a-container-on-a-linux-host)
for the walkthrough.

## Hardware

> ### ⚠️ Put `eth0` on your own network, never a corporate LAN
>
> A transparent bridge putting many MACs onto one switch port is, to enterprise
> network security, indistinguishable from the thing that security exists to
> stop. Cable the WAN port to your own upstream, a home router, or a lab VLAN.
>
> - **Port security / 802.1X** err-disables the port on seeing a second MAC.
>   That usually needs a network admin to clear, so the person who plugged the
>   box in cannot undo it.
> - **BPDU guard and DHCP snooping** exist to stop exactly this: an unexpected
>   layer-2 device on the port, and — since clients here depend on upstream
>   DHCP crossing the bridge — exactly the traffic snooping blocks.
> - **Wireless IDS** sees an unknown BSSID bridging to the wired side, which is
>   the textbook rogue-AP signature.
>
> **And it corrupts your measurements, which is the part that wastes an
> afternoon.** Wireless IPS *containment* works by transmitting deauthentication
> frames at the rogue AP's clients — so a corporate WIPS produces, uninvited and
> untimed, the same impairment this box produces deliberately. A run inside a
> contained office shows association drops that look like your pattern firing
> and are not, with nothing on the box able to tell the two apart. See
> [Security](#security) for what else stops being true there.
>
> **If you need these features at work, put the box on an isolated lab network
> — and know that a VLAN isolates your bridge, not the air.** The wired side is
> a policy problem with a policy answer; the radio side is a physics problem
> that no network configuration touches. Offices are awash in Wi-Fi, and an
> office belonging to a company that *builds streaming devices* is the worst
> case there is: every desk carries test hardware, most of it associated to
> something, much of it on 2.4 GHz.
>
> That is not a small correction to a measurement. Airtime is shared, so one
> near-idle 802.11n client moved a measured downlink between **356 and
> 717 Mbit/s** on this box while transferring 4 KB of its own traffic — a
> station linked at 65 Mbit/s holds the channel roughly 18× longer per byte
> than an 802.11ax one. A room full of them is not a quieter version of that
> effect; it is the same effect, continuously, from devices you do not control
> and cannot quiesce.
>
> What actually helps, in order:
>
> - **Use the wired downstream port when the radio is not the subject.** It is
>   repeatable to within 1%; nothing over the air comes close.
> - **Check how busy your channel actually is**, with
>   `GET /api/bridge/radios/<iface>/survey`. It reports `busy_ms` against
>   `active_ms` for the **operating channel only** — a radio that is beaconing
>   never visits the others, so their counters read zero and are omitted. Cheap,
>   non-disruptive, and the number to quote beside a result.
> - **To pick a better channel, use `scan and move to the quietest`**, which
>   rates candidates on *measured airtime* rather than on a count of visible
>   networks — the distinction that matters when one loud neighbour beats five
>   idle ones. It **takes the radio down and back up**, so it cannot be done
>   mid-run and it will disconnect that radio's clients. Do it before a run,
>   never during one.
> - **Prefer UNII-3 (149–165) and 80 MHz**, and treat 2.4 GHz in an office as
>   unusable for measurement rather than merely busy.
> - **Re-survey between runs of an A/B.** The environment drifts on its own; the
>   0.1.0 notes recorded the radio baseline moving ~100 Mbit/s over 90 s, which
>   is larger than most effects worth measuring.
> - **Capture the box's own event log alongside every run**, so a drop you did
>   not cause is at least visible as one you did not cause.

The warning above applies to **both targets**. A transparent bridge is a
transparent bridge whether the daemon is running on a Pi or in a container, and
a corporate switch port objects to it identically.

### Parts for the Pi build

What this box was built and measured on. Nothing here is required — it is a
Raspberry Pi 5 and a USB Wi-Fi adapter — but these are the exact parts behind
every Pi number in this document.

| Part | What was used | Why it matters |
|---|---|---|
| Board | [Raspberry Pi 5 Model B, 4 GB](https://www.amazon.com/dp/B0CK3L9WD3?tag=jonathaneoliv-20) — or the cheaper [2 GB](https://www.amazon.com/dp/B0DDL91V2R?tag=jonathaneoliv-20), see below | A Pi 4 works; the onboard NIC must not be USB, which is why a Pi 3 does not — see the udev rule in `scripts/customize.sh` |
| Power | [Official Raspberry Pi 27 W USB-C PSU](https://www.amazon.com/dp/B0CW7XCY75?tag=jonathaneoliv-20), or the [CanaKit 45 W USB-C PD supply](https://www.amazon.com/dp/B07H125ZRL?tag=jonathaneoliv-20) which also delivers 5 A | A SuperSpeed Wi-Fi adapter is a real load. **5 A is what `BOA_USB_MAX_CURRENT` needs** — under it the Pi 5 caps every USB port at 600 mA between them, which reads as a flaky adapter rather than a power problem. Check `vcgencmd get_throttled` reads `0x0` — and if it does not, or a radio keeps dropping off the bus, see [Power](#power) |
| Storage | [SanDisk Ultra 16 GB microSDHC](https://www.amazon.com/dp/B074B4P7KD?tag=jonathaneoliv-20) | What was used, and enough — the finished image is ~4.6 GB. A 32 GB card costs little more and leaves room for `ntopng` data |
| Wi-Fi adapter | [Panda Wireless PAU0F AXE3000 (mt7921u)](https://www.amazon.com/dp/B0D972VY9B?tag=jonathaneoliv-20) | Optional, and the single biggest change to what the box can test — see below. **A client part, and it does not do everything this box would like**: see [The radios here are client parts](#the-radios-here-are-client-parts-and-that-is-the-ceiling) |
| Wired downstream | Any USB ethernet adapter — e.g. [UGREEN USB-C 2.5 GbE](https://www.amazon.com/dp/B0CD1FDKT1?tag=jonathaneoliv-20); the figures below are a Realtek RTL8156 at both ends | Becomes `lan0`. Optional. 2.5 GbE needs a SuperSpeed link end to end, and a USB-C part reaches the Pi's USB-A socket through a converter that is usually the weak point — see [The cable decides whether you get 2.5 GbE at all](#the-cable-decides-whether-you-get-25-gbe-at-all) |

The product links above are Amazon affiliate links. **As an Amazon Associate I
earn from qualifying purchases.** No part was chosen for that reason — each one
is what the numbers in this document were measured on, and buying it anywhere
else works identically.

**These are the parts this was built on, not the parts it deserves.** The list
above is a record of what produced the figures in this document, and the Wi-Fi
adapters in particular are the compromise the rest of the box is shaped around.
Buy them to reproduce what is written here. Do not read the list as a
recommendation for the best boa anyone could build, because nobody has built
that one yet — see [Hardware worth trying, none of it
tried](#hardware-worth-trying-none-of-it-tried).

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

### Requirements for the container host

The container was built and measured on an x86_64 Ubuntu desktop. The adapters
are the same ones the Pi uses — they are moved off the host and into the
container's network namespace, so any USB adapter that works on the Pi works
here.

| Requirement | Why |
|---|---|
| x86_64 Linux with Docker and the compose plugin | The image is `debian:trixie-slim` and is built on the host |
| **NetworkManager managing the uplink NIC** | `scripts/docker-host-net.sh` moves the NIC into a bridge with `nmcli`, and refuses to guess if no NM connection is active. Ubuntu Desktop qualifies; **Ubuntu Server does not** — it defaults to netplan with systemd-networkd |
| An ethernet NIC facing your router | It gets bridged, so the container's uplink is a real layer-2 port |
| One or more USB Wi-Fi adapters | Given to the container outright. Optional, as on the Pi |
| A USB ethernet adapter | Becomes a wired downstream port. Optional |
| `root` on the host | Only root can move a netdev or an 802.11 phy between namespaces |
| A workstation with `go`, `npm` and `ssh` | Builds the daemon and interface; the host needs neither toolchain |

The uplink interface is discovered rather than named: the setup script takes the
interface carrying the host's default route, ignoring the USB adapters and any
radio, and on a re-run recovers it from the bridge it already built. It refuses
rather than guesses when that is ambiguous, and tells you to pass `--wan-if`.

One real gap remains: ntopng and glances are not in the container image, so it
has the conditioner and the interface but neither of the two optional analysis
tools. The interface reports them inactive and says why, rather than failing
quietly. That and the host footprint are tracked in
[#286](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/286).

**Give the USB adapters their own powered hub, or their own root ports.** The
hub ceiling measured here was 5 Gbit/s shared across three adapters, which is
the same constraint the Pi has and the same one the
[powered hub figures](#what-a-hub-costs-a-radio-separated-from-what-the-channel-is-worth)
below quantify.

### The radios here are client parts, and that is the ceiling

Every radio this box has ever run is a **station chip with AP mode bolted on**.
Not one is an access-point part.

| Radio | Where | What it is |
|---|---|---|
| mt7921u (Panda PAU0F) | USB, both targets | Client sibling of the mt7915/mt7916 AP line |
| BCM43455 | Onboard, Pi | Embedded client chip |
| Intel AX200 | PCIe, the container host | Laptop client card, self-managed regulatory domain |

That single fact explains most of the limits catalogued in
[what the radios will not do](#channel-manipulation-what-client-class-silicon-will-not-do):
no channel switch announcement, so every channel move is an outage; no DFS, so
16 of the 25 usable 5 GHz channels are off the table; one BSS per radio; no rate
pinning, which is why distance is *modelled* rather than imposed.

The AX200 has bitten in its own way. It carries its own regulatory domain rather
than the global one, and it is not final the moment the phy lands in the
container's namespace — the channel plan was correct against what it could see
and wrong a second later. The entrypoint waits for the channel set to stop
moving because of it.

### Wi-Fi features not yet exercised

Distinct from the list of things the silicon **refuses**. These are implemented,
or believed to work, and have never been confirmed doing their job on hardware.
An unexercised feature is recorded as unexercised rather than assumed working.

| | Status |
|---|---|
| **A client accepting a steer onto 5 GHz** | The one direction ever seen to work is onto 2.4 GHz. A malformed operating class made every 5 GHz request describe a block that does not exist; it was fixed and **no client has been observed moving onto 5 GHz since** |
| **Steer as a way to place a client** | Every recorded attempt on a real device was declined. An iPhone ignored a same-band request and refused a cross-band one, offering its own candidate list. Two Apple clients ignored an evict outright and had to be disassociated |
| **RSSI-driven client behaviour** | The distance model does not move real signal, so a client's radio still reads an excellent link while the box reports it as distant. Anything that depends on a device *reacting to* its own RSSI has never been truly tested here |
| **Per-station signal on the onboard Pi radio** | Absent from `iw station dump` entirely. The USB adapters report it, with per-antenna values, and the interface shows it |
| **Two radios carrying clients at once** | Never measured on either target, despite the rack being the point |
| **6 GHz** | The adapter is an AXE3000 and the phy offers 59 usable channels with AP mode. The box neither scans nor serves there |
| **WPA3/SAE, PMF, 802.11r, mesh, multi-BSS** | Supported by hostapd or advertised by the phy; none configured |
| **Airtime fairness** | Measured, never enforced |

**The steer rows are the ones worth dwelling on**, because they are easy to
misread as a bug. The control is working: it sends a well-formed request and
reports honestly what came back, including a refusal with the client's own
candidate list. 802.11v simply has no request that *places* a station on a BSS.
That is why the controls that reliably move a client are the ones that remove
the alternatives — `gather` and `evict` — rather than the one that asks.

### Hardware worth trying, none of it tried

**None of this has been bought, run or measured.** It is where the limits above
would go if someone did.

OpenWrt's split is the useful frame. The mt7915 and mt7916 are purpose-built AP
parts on the same `mt76` driver already in use here, where the mt7921 in these
adapters is the client sibling — it works in AP mode but was never optimised for
it. Qualcomm's `ath11k`, and the older `ath9k`/`ath10k`, are the other AP-class
family, and `ath9k`/`ath10k` are the ones that expose raw
[spectral scan](#what-ap-class-silicon-would-add-in-order-of-what-it-changes)
data, which would let this box see interference that does not beacon.

| Candidate | Form factor | What it would unlock here |
|---|---|---|
| **mt7916** (e.g. AW7916-NPD, 3×3 DBDC) | mPCIe / M.2 | AP-class `mt76`: CSA so a channel move stops being an outage, DFS, multiple BSS per radio, off-channel scan while beaconing, and possibly **OFDMA** |
| **mt7915** (AW7915-NP1 4×4, or NPD-2X 2×2) | mPCIe / M.2 | As above, and 4×4 doubles the two-stream ceiling |
| **ath11k** (e.g. QCN9074) | M.2 / PCIe | The non-MediaTek AP family, for a second opinion on driver-specific behaviour |
| **ath9k / ath10k** | mPCIe / PCIe | Old and slow, but the only realistic route to **spectral scan** and mature **airtime fairness** |

**OFDMA is the one to select on, and the datasheet will not answer it.** This
box serves a purely time-shared radio, which
[bounds every throughput figure in this document](#this-box-does-not-do-ofdma-and-that-bounds-every-figure-above)
— and OFDMA attacks precisely the weakness the width sweep found, because the
fixed overhead a wide channel wastes on one client is shared out when several
are served in the same transmission. It is the difference between measuring a
radio that behaves like a modern router and one that does not.

The current adapter is exactly why the datasheet is worthless as evidence. It
advertises HE and `Full Bandwidth UL MU-MIMO` and delivers neither: `mt7921`
exposes no MU counters at all, and every frame on the air is single-user
aggregation of at most two MSDUs.

**The AP-class driver does have them**, which is the encouraging half.
`mt7915/debugfs.c` carries a `muru_debug` switch and a `muru_stats` file
reporting downlink MU-MIMO, downlink OFDMA and trigger-based uplink MU-MIMO and
OFDMA as per-PPDU counts; `mt7921/debugfs.c` contains none of those words. So
the acceptance test for any candidate is on the box, not on the box it came in —
enable `muru_debug` first, since `muru_stats` reports nothing until you do, then
look for multi-user transmissions under load.

**The other catch is form factor, not price.** AP-class silicon is essentially
not sold as USB. Every card above is mPCIe or M.2, which decides where each
target can go next.

**The container host can take one today.** It has an Intel AX200 on PCIe and
three free slots: a PCIe x16, a PCIe x4, and an M.2. An mPCIe or M.2 card on a
cheap adapter drops straight into the x4 or x16, alongside or instead of the
AX200. No power problem, no enclosure problem. This is the shortest path from
here to AP-class behaviour, and it is a strong argument for the container target
independent of anything else.

**The Pi needs a HAT, and power is the constraint.** The Pi 5 exposes a single
PCIe lane on its FFC connector, so an AP-class card needs a PCIe HAT — an M.2 or
mini-PCIe adapter. Supernetworks builds one specifically for Wi-Fi 6 AP cards
and states the reason plainly: **the FFC connector is limited to about 5 W**, so
they built a HAT that can draw over 10 W. A 3×3 or 4×4 AP radio is a real load,
and this repository has already lost a day to
[a Pi 5 browning out two USB radios](#power). The same lesson, one connector
over.

### Would PCIe beat USB here? Nobody has checked

Worth benchmarking on its own, separately from what the chip can do, because
three different things could move and they matter for different reasons.

| | Why it might change | Why it matters here |
|---|---|---|
| **Throughput** | The Pi 5's lane is PCIe Gen 2 ×1, about 500 MB/s, roughly 4 Gbit/s. USB 3.0 offers 5 Gbit/s **shared by every adapter on the bus** — and the hub ceiling measured here was exactly that, across three | A dedicated lane per radio, instead of a bus three radios contend for |
| **Latency** | USB is a polled, packetised transport with host-controller round trips in the path. PCIe is memory-mapped | **This is the one that could change a measurement.** Bus latency lands on top of every conditioned delay, and adaptive bitrate decisions are made on buffer level and round-trip time |
| **CPU overhead** | Every USB frame costs the host controller driver work. The wired path here is already CPU-bound: anything above roughly 1.9 Gbit/s with `-R` is measuring a saturated core | The box is the instrument as well as the subject. CPU spent on the bus is CPU not spent conditioning |

The Pi 5's lane can be pushed to Gen 3, about 1 GB/s, with `dtparam=pciex1_gen=3`.
It is not certified for it and may be unstable, so treat that as part of the
experiment rather than a baseline.

The latency row is the one to design the test around. Throughput is already
adequate on USB for every rendition this box serves, and CPU headroom on the
container host is generous. A repeatable difference in round-trip time under
load would change what the ladder measurements mean, and nothing here has ever
isolated the bus from the radio.

## Access point performance

The AP's ceiling bounds the top of a measured ladder, so it decides which
renditions can be tested at all. Measured with `iperf3` **to the box**, which
means the link **unshaped** — the ceiling a cap must sit under, never evidence
that a cap is working.

**Every figure in this section was measured on the Pi**, on the parts listed
above. The radio is the limit in almost all of them, so they carry over to the
container host running the same adapter — see
[Measured on the container host](#measured-on-the-container-host) for what was
confirmed there.

| Link | Downlink | Uplink |
|---|---|---|
| Onboard radio (brcmfmac), 20 MHz | 55 Mbit/s | 57 Mbit/s |
| USB adapter on **USB 2.0**, 80 MHz ax | 162 Mbit/s | 146 Mbit/s |
| USB adapter on **USB 3.0**, 80 MHz ax | **536–691 Mbit/s** | ~150 Mbit/s |
| Wired 1 GbE, for reference | 924 Mbit/s | — |
| Wired 2.5 GbE, for reference | **1.91 Gbit/s** | **2.35 Gbit/s** |

Two things to take from it. **The USB adapter on a SuperSpeed port is the only
radio here worth testing a modern ladder against**, at roughly ten times the
onboard one; and **the wired port is not a substitute for it**, because the
question is usually what a radio does, not what a cable does.

**The spread on that third row is the whole subject of the sections below.** It
is not noise. The same adapter, the same client and the same transfer land
anywhere in that range depending on three things, each isolated with a
controlled comparison rather than left as a range:

- the **channel** — 683 against 536 Mbit/s, and it is contention rather than
  link quality: [What a channel is worth](#what-a-channel-is-worth)
- the **channel width** — 691, 379 and 198 Mbit/s across 80, 40 and 20 MHz:
  [Channel width, and what it is worth](#channel-width-and-what-it-is-worth)
- the **USB topology** — a root port against a powered hub, measured on both
  channels at once because the two effects had been cancelling out:
  [What a hub costs a radio](#what-a-hub-costs-a-radio-separated-from-what-the-channel-is-worth)

All of it was measured after the power fault described under
[Power](#power) was found and fixed. Figures taken before that, between
2026-09-03 and the morning of 09-07, ran lower and are not reproduced here.

**Read the three comparisons below as controlled experiments, not as a list of
results.** Each holds everything constant but one variable, which is why they
are kept apart from the ceiling table rather than folded into it — the hub
grid in particular is a single measurement repeated across two channels and two
USB topologies, precisely because those two effects had been cancelling each
other out, and reading any one of its four numbers alone will mislead.

### What a channel is worth

Two runs: the same radio, the same client and the same 70-second transfer,
minutes apart, on a box with its power fixed. Nothing differs but the channel:

| | ch 149 | ch 40 |
|---|---|---|
| Throughput | **683 Mbit/s** | **536 Mbit/s** |
| Our airtime | 91.2% | 74.6% |
| PHY rate | 1200.9 Mbit/s | 1200.9 Mbit/s |
| Retransmits | 0 | 0 |
| Efficiency | **798 Mbit/s per 100% airtime** | **722** |
| Neighbours on the channel | none in earshot | **75–82%, four reporting** |
| Sample-to-sample spread | ±3% | ±33% |

**It is not link quality.** The PHY rate is identical and neither run dropped a
retransmit — the radio negotiates exactly the same rate either way. The
difference is contention, and it lands in two places: we get 16 points less of
the air, and each slice we do get is worth 10% less because backoff eats into
the TXOPs.

The best evidence arrived mid-run, when the neighbours got busy:

```
 0s   609 Mbit/s   others 18-38% x4
12s   256 Mbit/s   others 78-82% x4     <- neighbours ramp, our throughput halves
18s   561 Mbit/s   others 78-82% x4
```

Four independent access points reported the channel going from 18–38% to
78–82%, agreeing within four points, in the same sample where our own
throughput collapsed from 565 to 256 Mbit/s. A throughput chart alone shows an
unexplained crater there; the contention figures name the cause.

**The variance may matter more than the mean.** ch 149 held ±3% for seventy
seconds while ch 40 swung ±33%. For a box whose whole purpose is measuring what
an impairment does to a link, an unshaped baseline that moves by a third
between samples makes every conditioned measurement above it noisier.

That last row is the same radio and channel as the 544–552 Mbit/s above and
came out **80 Mbit/s lower**, which is what a shared radio costs. The per-client
airtime series says so directly: the box's own stack sat pinned near **77%**
throughout, and every time the second client took 13–19% of the air the
MacBook's throughput fell by about 100 Mbit/s. A ceiling measured with another
device on the radio is a ceiling for that pair, not for the radio.

**Uplink costs about four times the airtime per bit that downlink does.** From
the same run:

| Direction | Throughput | Airtime | Per 100% airtime |
|---|---|---|---|
| Downlink | 546 Mbit/s | 77.3% | **707 Mbit/s** |
| Uplink | 145 Mbit/s | ~82% | **177 Mbit/s** |

The client transmits with less aggregation and a weaker radio than the access
point does, so the same air buys far fewer bits going up. Worth knowing before
reading an uplink number as though it were a downlink one.

A second measurement on `mt7921u`, 2026-09-08, one MacBook on ch 149 at 80 MHz,
−24 dBm, PHY negotiated at 1200.9 Mbit/s, no conditioning in force: **578 Mbit/s
down, 182 up**, zero retransmits either way. The same 3:1 shape as above, at a
signal level where the radio is not the limit -- so the asymmetry is the client's
transmitter rather than the link.

> **An `iperf3` UPLINK run against the box does not appear on the device's own
> throughput chart.** It terminates *on* the box rather than being forwarded, so
> it never reaches the uplink shaper: during a 145 Mbit/s uplink the client
> card read `up` **0.0 Mbit/s** and `down` 3.4 Mbit/s — that being the TCP ACK
> stream — while the airtime chart read **80–86%**. The device looks idle. Use
> the airtime chart, or `iperf3` between two devices *through* the box, if you
> need the traffic to show up in the readouts as well as on the air.
>
> **The downlink direction is different, and `iperf3 -R` IS conditioned.**
> Downlink shaping lives on the egress of the client's own port, and a packet
> the box originates leaves by that same port — so it meets the same qdisc a
> forwarded one would. Measured 2026-09-08 over Wi-Fi on `wlan-usb`: the same
> client read **4.72 Mbit/s** under a 5 Mbps cap and **504 Mbit/s** with the cap
> cleared. So `-R` is a valid way to prove a downlink policy is real, and the
> "terminates on the box" caveat above applies to uplink only.

### Channel width, and what it is worth

Measured 2026-09-08 on `wlan-usb2` (mt7921u), ch 149, one MacBook as the **only**
associated station, `iperf3` downlink for 60s at each width, on a box with its
power fixed. Rate control settled on **HE-MCS 9 at every width**, which makes
this a controlled comparison the earlier runs were not.

| Width | Throughput | PHY | Airtime | As a fraction of PHY | Step |
|---|---|---|---|---|---|
| 20 MHz | **198 Mbit/s** | 229.4 | 94.3% | **86%** | — |
| 40 MHz | **379 Mbit/s** | 458.8 | 92.7% | **83%** | 1.91× |
| 80 MHz | **691 Mbit/s** | 960.7 | 90.9% | **72%** | 1.82× |

**20 → 80 is 3.49× against a theoretical 4.0.** Doubling the channel nearly
doubles the throughput, and the entire shortfall is in the top step.

**A wider channel converts less of its PHY rate into throughput.** 86% at
20 MHz, 83% at 40, **72% at 80** — with the MCS held constant, so this is the
width and nothing else. A 960 Mbit/s PHY drains an A-MPDU in roughly half the
time a 458 Mbit/s one does, so the fixed per-frame overhead — preamble, IFS,
block ACK — occupies a larger share of every transmission. That is the protocol
behaving as designed, not a fault.

**It is not the bus, the CPU or the driver**, and the airtime column is what
rules them out. At every width the radio is busy **90–94%** of the time with
zero retries and zero failures, while the Pi's busiest core sits at ~55% used
(19% softirq) and the same USB subsystem carries 2.35 Gbit/s through an ethernet
adapter. A radio starved from behind would show high throughput and *low*
airtime — the PHY idle, waiting for data. The opposite is true here: at 20 MHz
the box moves under a third of what it does at 80, and the air is *busier*.

> **An earlier revision of this table reported 85% / 82% / 56%** from runs on
> 2026-09-04. The shape was right and the 80 MHz figure was too harsh: those
> runs drifted between HE-MCS 9 and 11 between widths, and were taken on a Pi
> that was browning out. The 72% above is the same effect measured with the MCS
> held constant. See [Power](#power).

### What a hub costs a radio, separated from what the channel is worth

A radio behind a powered USB hub is **14% slower than the same model of adapter
in a port directly on the Pi**, and that is separable from the channel it is on.

The two effects had to be untangled because they were pointing in opposite
directions: the hub-connected radio was on the *clear* channel and the
root-port radio on the *congested* one, so a first pass showed the hub-connected
radio winning and concluded the hub was free. Swapping the channel allocation
between the two radios and repeating every run gives the full grid.

Measured 2026-09-08, 60s `iperf3` downlink at each cell, 80 MHz, one MacBook as
the only associated station, box power fixed:

| | ch 40 (congested) | ch 149 (clear) | **channel is worth** |
|---|---|---|---|
| **Root port** `2-1` | 546 Mbit/s | **672 Mbit/s** | **+23%** |
| **Behind a hub** `4-1.3` | 472 Mbit/s | 575 Mbit/s | **+22%** |
| **hub costs** | **−14%** | **−14%** | |

**The consistency is the result.** The hub costs about a seventh on *both*
channels and the clear channel is worth about a fifth on *both* topologies, so
neither figure is an artefact of the other. Either one measured alone would have
been wrong by the size of the other.

**It reproduces the historical ceilings.** The root-port figures land within 1–3%
of the 677 / 683 / 691 Mbit/s recorded for ch 149 and within 2% of the 536 for
ch 40 — so those were measured with the adapter in a root port, and nothing has
regressed since. The lower numbers are the hub, not drift.

**And it corrects the wired note further down.** That one attributed the hub's
cost to contention with the radios sharing it, which was a guess. This says
otherwise: 0.6 Gbit/s of Wi-Fi has an order of magnitude of headroom on a 5 Gb/s
bus, nothing else on the hub was transferring, and the cost is *the same
proportion* as it was at 2.35 Gbit/s on the wire. A proportional cost that
survives an idle bus is not contention for bandwidth.

> **Both "ch 36" runs are really ch 40.** hostapd's 20/40 MHz coexistence scan
> found neighbours on the secondary channel and swapped its primary and
> secondary to avoid them, so a radio asked for 36 at 80 MHz came back on 40.
> Same 80 MHz block, same centre (5210), so the comparison holds — but the box
> reported the substitution rather than silently serving a different channel,
> which is the only reason the label above is right.

**An idle client is not free.** Measured twice on 2026-09-08, at two widths: an iPhone
associated and transferring *nothing* — 0.0 Mbit/s, 0.0% airtime by its own
counters — cost the MacBook **37 Mbit/s** both times, 697 → 660 at 80 MHz and
379 → 342 at 40 MHz. Around 10%, for a device doing nothing but existing on the
radio. Beacons, block-ack sessions and the scheduler's per-station bookkeeping
are not zero. A ceiling measured with a second station associated is a ceiling
for that pair.

### One 80 MHz channel, or two 40 MHz ones?

For **two clients**, the ladder above answers it arithmetically:

```
2 x 40 MHz, one client on each radio   379 + 379 = 758 Mbit/s
1 x 80 MHz, both clients sharing it                691
                        ... and with the second client merely associated: ~654
```

**Two 40 MHz bands win by about 10%, or 16% once the idle-client cost is
counted** — and the reason is exactly the poor top step. 40 → 80 buys 1.82×
rather than 2.0, so splitting the spectrum hands back the 18% the wide channel
wastes, and two radios transmit genuinely simultaneously where two clients on
one radio time-share and pay CSMA backoff to each other.

Three things qualify that:

- **The two blocks must be far apart.** 36–40 and 44–48 are adjacent, and two
  radios inches apart on adjacent blocks will desense each other. Use **36–40
  and 149–153** — 565 MHz apart, which is why UNII-3 is in the channel table.
- **Bursty traffic favours the shared 80 MHz.** Two 40s hard-partition the
  spectrum: when one client is idle, half the air is wasted. One 80 lets either
  client take the full width when the other is quiet. For ABR players — which
  are bimodal by nature, and are what this box exists to test — that statistical
  multiplexing may well be worth more than 10%.
- **This has not been measured with both radios transmitting at once.** Every
  figure above is one radio at a time. Whether two mt7921u adapters in the same
  chassis reach 758 Mbit/s together, or desense each other, needs two
  iperf-capable clients and has not been tried.

### This box does not do OFDMA, and that bounds every figure above

802.11ax subdivides a channel in **frequency** as well as time: an 80 MHz
channel is carved into Resource Units, and an access point can serve several
clients **simultaneously in one transmission**, each on its own slice. That is
how a modern router gives a device a narrow effective channel without narrowing
the radio, and it is why every 5 GHz neighbour here sits at the full 80 MHz
rather than splitting the band.

**boa does not do it**, and this is verifiable on the box rather than assumed:

```sh
# hostapd sets width and centre only -- no MU options are configured
grep -hE 'he_|mu_|ofdma' /etc/hostapd/*.conf

# mt7921 exposes no MU or OFDMA counters at all (mt7915 has muru_stats)
ls /sys/kernel/debug/ieee80211/phy0/mt76/

# and every transmission is single-user aggregation
cat /sys/kernel/debug/ieee80211/phy0/mt76/tx_stats
```

Measured 2026-09-08 on `wlan-usb2`:

```
AMSDU pack count of 2 MSDU in TXD:  12,449,086  (96%)
AMSDU pack count of 1 MSDU in TXD:     389,591   (3%)
AMSDU pack count of 3+ MSDU:                 0   (0%)
```

Every frame is A-MSDU aggregation to **one** station, and never more than two
MSDUs — a hard ceiling rather than a distribution. Deeper aggregation is exactly
what amortises the per-frame overhead a fast PHY drains through so quickly, so
this is a plausible contributor to the 72% figure at 80 MHz.

**Two consequences, and they pull in opposite directions.**

Every number in this section describes a **purely time-shared radio**, because
that is the only kind this box has. Nothing here was ever going to show OFDMA,
so the ladder and the two-radio arithmetic are sound for what they measured —
and their scope is now a measured fact rather than an assumption.

But OFDMA attacks precisely the weakness the ladder found. The 72% conversion at
80 MHz is fixed overhead — preamble, IFS, block ACK — amortised over one
client's data. Serve four clients in that same transmission and it is shared
four ways, so **a wide channel's disadvantage shrinks as clients are added**,
which is the opposite of what the two-client arithmetic above assumes. An access
point that can do it has no reason to narrow anything.

So "two 40 MHz radios beat one shared 80" is a conclusion about **this
hardware**. Do not carry it across to a router that can schedule OFDMA; the
neighbours' choice of 80 MHz everywhere is the clue that they need not.

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
air — and it sits **above 677 Mbit/s**, not at 550.

> **Resolved 2026-09-08: that 80 MHz figure is the protocol, not a ceiling.**
> Re-measured with rate control holding HE-MCS 9 at every width, 80 MHz converts
> **72%** of its PHY rather than the 56% above — the effect is real but smaller,
> and the earlier number was depressed both by the MCS drifting between runs and
> by a Pi that was browning out. A wider channel simply amortises fixed per-frame
> overhead worse, because a faster PHY drains an A-MPDU in less time.
>
> And the airtime figures rule the alternatives out directly: at every width the
> radio is busy **90–94%** with zero retries, while the busiest core sits at ~55%.
> A radio starved by the bus or the CPU would show *low* airtime, not high. See
> [Channel width, and what it is worth](#channel-width-and-what-it-is-worth).
>
> What remains untested is a client that can pull harder than one MacBook, and
> whether two adapters transmitting at once reach the sum of their parts.

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

  What the box offers instead is a **distance model** — tell a device to behave
  as though it were further away, and it is handed the rate, delay, jitter and
  corruption that signal level implies. It does not move the radio, so the
  reported signal and PHY rate keep describing the real one and will disagree
  with it on screen. That is stated rather than hidden, and it is survivable
  because players adapt on throughput and buffer, not on signal strength. See
  [Source S](docs/DATA-CONTRACT.md) for what is taken from a standard and what
  is asserted.

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

**A powered hub costs about a fifth of that, in both directions.** Same adapter,
same cable, same laptop; the only change is that the adapter sits on a powered
USB 3 hub shared with two Wi-Fi radios rather than in the Pi's own SuperSpeed
socket:

| Direction | Direct to the Pi | Behind a shared hub | Change |
|---|---|---|---|
| Uplink, device → box | 2.35 Gbit/s | **1.91 Gbit/s** | −19% |
| Downlink, box → device | 1.91 Gbit/s | **1.58 Gbit/s** | −17% |

Measured 2026-09-08, 15s and 60s runs agreeing to within 1%, two radios serving
five clients throughout. The hub itself is not the ceiling -- every device on it
enumerated at 5 Gb/s.

Worth reading alongside the CPU note below: the direct downlink figure is
CPU-bound, and a CPU limit would not move because a hub was added. Both
directions falling by a similar proportion is what says the constraint is not
the transmit path alone.

**The same cost applies to a radio, which rules out the obvious explanation.**
This note first put the loss down to contention with the radios sharing the hub.
[What a hub costs a radio](#what-a-hub-costs-a-radio-separated-from-what-the-channel-is-worth)
measures the same **14%** on a Wi-Fi adapter moving 0.6 Gbit/s with nothing else
on the hub transferring -- an order of magnitude of headroom on a 5 Gb/s bus.
A cost that stays the same proportion whether the bus is nearly full or nearly
idle is not competition for bandwidth. Plan for roughly a seventh off anything
behind a hub, wired or wireless, rather than only where the bus is busy.

**And the socket matters far more than the hub.** The same adapter in one of the
Pi 5's 480 Mb/s sockets managed 943 Mbit/s up and 469 down -- it negotiates
`1000baseT` there rather than 2500, and the USB bus caps it well below even
that. Measured the same day. `usb2` and `usb4` are the SuperSpeed sockets;
`usb1` and `usb3` are not, and nothing in the interface says which one an
adapter is in -- `boactl state` reports the negotiated `link_mbps` and
`usb_version`, which is how this was found.

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

### Measured on the container host

Taken on the x86_64 Ubuntu host on 2026-09-09. A different machine with
different radios from the Pi above, so treat these as evidence that the
container arrangement is not itself a bottleneck rather than as a head-to-head.

**Ceilings, taken against the box itself.** What a cap must sit under, and never
evidence that a cap is working.

| | Downlink | Uplink |
|---|---|---|
| Wired, 2.5 GbE | **1945 Mbit/s** | **2353 Mbit/s** |
| Wireless, 80 MHz 802.11ax | **454 Mbit/s** | 144 Mbit/s |

**Through the box to a host beyond it.** The only arrangement that shows both
directions conditioned, and the one the Pi has never been run in.

| | Downlink | Uplink |
|---|---|---|
| Wired, 2.5 GbE, unshaped | **1945 Mbit/s** | **2353 Mbit/s** |
| Wireless, unshaped | **423 Mbit/s** | — |
| Wireless, under a 60/20 Mbit/s cap | **57 Mbit/s** | **13.6 Mbit/s** |
| Wired, under a 90/40 Mbit/s cap | **86.0 Mbit/s** | **38.1 Mbit/s** |

The wired path forwards through the bridge at the same rate it terminates at the
box, in both directions. That is the useful thing this pair of tables says: the
bridge and the shaper are not costing anything the adapter was not already
costing.

**Both media at once**, run twice with the radio on different channels:

| Wired, 2.5 GbE | Wireless | Radio |
|---|---|---|
| 1819 Mbit/s | 586 Mbit/s | Channel 149 |
| 1778 Mbit/s | 449 Mbit/s | The lower 5 GHz block |

Neither starves the other, which is the question worth asking of a box that
conditions both at once.

The USB hub ceiling shared by three adapters is 5 Gbit/s. That is a bus limit
rather than a boa limit, and it bounds every figure above.

**What has not been measured here**, listed so an absent run reads as absent
rather than as a result:

- **Two radios carrying clients simultaneously.** Every wireless figure above is
  a single radio. The Pi has never been measured this way either, so it is the
  most interesting missing number on both targets.
- **A per-radio breakdown by channel and width.** The Pi has a table of these;
  the container has two channels and does not name either precisely.

The wired ceiling is close enough to the Pi's that the same caution applies:
anything measured above roughly 1.9 Gbit/s with `-R` is measuring a saturated
CPU core rather than the shaper.

**The direction caveat, which is the easiest way to publish a wrong number, and
the reason the two tables above are kept apart.** Traffic terminating **at** the
box is exempt from conditioning on the uplink only, so the forward direction
reports an unconditioned ceiling while `-R` reports the downlink cap actually
being enforced. Over the radio against a 60/20 cap, a test against the box gave
143 Mbit/s forward and 56.9 Mbit/s reversed. **Only the second is real.**

Repeating that same run **through** the box to a host beyond it gave 13.6 Mbit/s
uplink, against a 20 Mbit/s cap — the number the 143 was hiding. If you take one
thing from this section, take that: a target on the far side of the box is the
only place both directions are true at once.

The method is the one in [Measuring it yourself](#measuring-it-yourself). It is
not Pi-specific; `<pi>` is just the box's address either way.

## Power

**Pi-specific.** A desktop host has a real power supply and none of this
applies to it — but the underlying lesson does, in the form of the hub ceiling
noted above: starve a USB radio and the symptoms point everywhere except the
power.

**A Pi 5 that is not offered USB-PD falls back to 900 mA, and two Wi-Fi
adapters will brown it out.** This cost most of a day on 2026-09-07, and every
symptom pointed somewhere other than the supply.

What it looks like from above: a USB radio *unregisters mid-transfer*, hostapd
loses the interface, `select-radio` re-plans around the missing adapter and
restarts the daemon, clients scatter onto whichever radio is left, and an
`iperf3` run that was doing 725 Mbit/s finishes at 398 with retransmits and a
PHY rate that collapsed to 6.0. It reads exactly like a Wi-Fi fault. It is not.

**It was the cable.** No USB-PD objects were exchanged at all, so the firmware
fell back to the 900 mA USB default while two mt7921u adapters drew against it:
269 under-voltage events in a day, and adapters dropping off the bus
*mid-transfer* three times. Replacing the cable restored negotiation
(`max_current` 900 → 5000) and the events stopped dead.

**What it cost the measurements was variance, not throughput.** Re-measured on
healthy power, the same channel and client went 556 → 660 Mbit/s, about +19% —
so the older figures were depressed rather than wrong. Individual samples had
been reaching 725–751 Mbit/s even while the box was failing, because the radio
was always capable and simply could not *sustain* it.

The number that mattered was the spread. Runs swung between 398 and 725 Mbit/s
for reasons unconnected to anything under test. **On a box whose whole purpose
is measuring what an impairment does to a link, an unshaped baseline that moves
by a factor of two is a worse fault than any single wrong figure** — it makes
every comparison drawn against it unsound, and says nothing about which ones.

### Ask the firmware what it negotiated

```sh
for f in /proc/device-tree/chosen/power/*; do
  printf '%-28s ' "$(basename "$f")"; od -An -tu4 --endian=big "$f"; done
vcgencmd get_throttled
journalctl -b | grep -ci undervoltage
```

| Reading | Healthy | Measured while failing |
|---|---|---|
| `max_current` | **5000** | **900** |
| `usbpd_power_data_objects` | several non-zero words | **all zero** |
| `vcgencmd get_throttled` | `0x0` | `0x50000` |
| under-voltage events | 0 | 269 in a day |

`usbpd_power_data_objects` being entirely zero is the tell: the Pi did not
receive a *rejected* or *small* PD offer, it received **nothing**, and fell back
to the plain USB default of 900 mA. `max_current` then reads 900 instead of the
5000 an official 27 W supply reports.

**The supply was fine; the path to it was not.** Replacing the cable restored
negotiation — `max_current` 900 → 5000, PD objects populated, `get_throttled`
back to `0x0` — and under-voltage stopped instantly. A USB-A-to-C cable cannot
carry PD at all, and a charge-only or degraded C-to-C will not either. This is
the same lesson as [the 2.5 GbE cable](#the-cable-decides-whether-you-get-25-gbe-at-all),
one layer down.

### `BOA_USB_MAX_CURRENT` is applied at IMAGE BUILD time only

`usb_max_current_enable=1` lifts the Pi 5's 600 mA cap on *peripheral* current.
`scripts/customize.sh` writes it into `config.txt` when `BOA_USB_MAX_CURRENT=1`
— **during the build**, and nowhere else.

So a `.env` that has said `1` for months does not mean the running box has it.
Ours did, and did not: the image predated the setting, and the box had been
capped at 600 mA since June without anything saying so. Check the box, never the
`.env`:

```sh
grep usb_max_current_enable /boot/firmware/config.txt
```

It can be set live — `config.txt` is on the boot partition and takes effect on
the next reboot — which is far cheaper than a reflash.

> **Do not enable it to fix a brownout.** The 600 mA cap is *proactive* and
> never trips the under-voltage flag. If you are seeing under-voltage, the rail
> is already sagging and lifting the cap lets the Pi draw harder into it. Fix
> the negotiation first, confirm `max_current` reads 5000, and only then raise
> the cap.

## Build an image

**For the Pi.** The container path needs no image and no card; skip to
[Run it as a container on a Linux host](#run-it-as-a-container-on-a-linux-host).

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

There is deliberately **no flashing helper in this repository**. One shipped
briefly — a raw `dd` write to a block device named by hand — and it was removed:
a mistyped identifier erases that disk in seconds, guards or not, and no
convenience is worth handing someone a loaded tool aimed at their own SSD. The
imagers above verify the write and refuse your system disk, which is exactly the
work a helper here would have to duplicate to be safe.

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

## Run it as a container on a Linux host

The other way to get a boa. No card, no image, and the daemon and interface are
built on your workstation so the host needs neither Go nor node. Check
[Requirements for the container host](#requirements-for-the-container-host)
first — the NetworkManager requirement rules out a default Ubuntu Server
install.

Throughout, `<host>` is the Ubuntu machine and every command runs on your
workstation unless it says otherwise.

### 1. Configure, once

The same `.env` serves both targets, so a checkout that already builds Pi images
needs one addition.

```sh
cp .env.example .env      # if you have not already
```

Set `AP_SSID_DOCKER` to something distinct from `AP_SSID`. **Do this if both
boxes can ever be powered on at once**, which is the normal case while you are
working on a container deployment. Two appliances broadcasting one name is not a
cosmetic clash: a client cannot tell them apart, roams to whichever is louder,
and your measurement then belongs to whichever box happened to win, with nothing
in either interface saying so.

```sh
AP_SSID="infinite-streaming-boa"
AP_SSID_DOCKER="boa-container"
AP_PASSWORD="a-strong-passphrase"
AP_COUNTRY="US"
```

The passphrase is deliberately shared between the two. One credential is what
keeps them from drifting apart, and a device that already knows one box then
associates with the other without being re-taught.

### 2. Deploy, once, with the network step

```sh
scripts/docker-deploy.sh <host> --setup-network
```

This builds the interface and an amd64 `boad`, copies the build context to
`/opt/infinite-streaming-boa` on the host, installs the attach helpers, prepares
the host's network, then builds and starts the container and hands it the
adapters.

`--setup-network` belongs on the **first** run only. Every run after it is the
bare command, and the network is already prepared.

The network step **briefly disconnects the machine**. The script is launched detached through `systemd-run`, so an SSH
session dropping mid-switch cannot leave the host half-bridged, and it rolls
itself back if the gateway does not answer afterwards. You do not need a
keyboard on the box to recover from a failure here.

The host disappears for a few seconds and comes back on the same address: the
bridge is pinned to the NIC's own MAC, so the router hands back the same lease.

The uplink interface is worked out from the host's default route. If two
candidates are equally plausible the script refuses rather than bridging the
wrong port, and says so — name it yourself in that case:

```sh
scripts/docker-deploy.sh <host> --setup-network --wan-if enp1s0
```

Check what it did:

```sh
ssh <host> "sudo /opt/infinite-streaming-boa/scripts/docker-host-net.sh status"
ssh <host> "sudo journalctl -u boa-netswitch --no-pager -o cat | tail -20"
```

The deploy prints the container's log tail when it finishes. That should show
the bridge coming up and an access point starting on each radio.

### 3. Use it

```
http://<host>:8080/
```

The container publishes no Docker ports, because it owns an otherwise empty
network namespace and there is no interface for Docker to bind. The attach step
installs a DNAT from port 8080 on the host to the container's management
address instead. That management path is a private veth pair, deliberately
independent of the bridge having taken a DHCP lease, so the interface is still
reachable when the uplink is unplugged.

`iperf3` answers on port 5201 as it does on the Pi, with
[the same caveat](#measured-on-the-container-host) about which direction is
conditioned.

### Day to day

One command, and it is the container equivalent of `scripts/deploy.sh`:

```sh
scripts/docker-deploy.sh <host>
```

Rebuilds, recreates the container and re-attaches. There is no equivalent of
reflashing, because the image is rebuilt every time — the reason the Pi needs a
reflash for units, packages and kernel settings does not arise here.

Step 1 and the `--setup-network` flag are the only parts you do once.

### What this puts on your host

Worth knowing before you start, and all of it is removable.

| Installed | Purpose |
|---|---|
| `/opt/infinite-streaming-boa/` | The build context: `docker/`, `overlay/`, the two scripts |
| `/usr/local/sbin/boa-attach` | Moves the adapters and the uplink into the container |
| `/usr/local/sbin/boa-attach-watch` | Watches `docker events`, re-attaches on every container start |
| `/etc/systemd/system/boa-attach.service` | Keeps the watcher running |
| `/etc/NetworkManager/conf.d/99-boa-unmanaged.conf` | Stops NetworkManager touching the adapters |
| A udev rule | Re-attaches an adapter that is unplugged and replugged |
| `br-wan`, and two `nmcli` connections | The bridge the container's uplink hangs off |
| Docker volume `boa-state` | Operator policy. Chart history stays on the container's writable layer |

The attach helpers exist because Docker cannot hand a physical network device to
a container: netdevs do not live in `/dev`, so there is no `--device` for them,
and only something holding `CAP_NET_ADMIN` in the host namespace can move one. A
namespace also dies with its container, so the move has to be redone on every
restart — hence a watcher rather than a one-shot.

### Undoing it

```sh
ssh <host> "cd /opt/infinite-streaming-boa/docker && docker compose down"
ssh <host> "sudo systemctl disable --now boa-attach.service"
ssh <host> "sudo /opt/infinite-streaming-boa/scripts/docker-host-net.sh revert"
```

`revert` restores the NetworkManager connection that was active on the NIC
before `apply` ran, which the script recorded at the time rather than guessing
at afterwards.

### When it does not work

| Symptom | Cause |
|---|---|
| `br-wan does not exist` | The deploy ran without `--setup-network`, or that step failed. Check `journalctl -u boa-netswitch` |
| `could not identify the uplink interface` | Detection found no candidate or more than one. The message lists what it saw; pass `--wan-if <name>` |
| `nmcli not found`, or NetworkManager not running | The host uses netplan with systemd-networkd, the Ubuntu Server default. Not supported by this script; nothing was changed |
| `no NetworkManager connection is active on <if>` | The named interface is real but unmanaged. Usually the wrong NIC |
| `wan0 never appeared after 120s` | The attach never ran. Check `journalctl -u boa-attach` |
| Container up, but no client ports | Look for `no lan-usb-* port was handed over` in `docker logs boa`. The adapters are discovered on the USB bus, so a device on a PCIe slot is not picked up |
| No access point | `no radio this box can serve`, or a country code the radio will not accept. `AP_COUNTRY` must match where the machine physically is |
| The box's IP changed after a redeploy | Should not happen — the uplink MAC is derived from the host NIC precisely so the lease survives. Report it |

## Configuration

Everything lives in `.env`; see `.env.example` for the full annotated list. **One
file serves both targets.** The container deployment reads the same access-point
settings from it, and ignores the rest — the login account, the SSH key, the
rescue address and the Pi's USB current setting are all properties of an image
that the container does not build.

| Variable | Meaning |
|---|---|
| `AP_SSID`, `AP_PASSWORD` | The wireless network the box publishes |
| `AP_COUNTRY` | Regulatory domain. **The radio stays blocked until this is right** |
| `AP_BAND`, `AP_CHANNEL` | `bg` (2.4GHz) or `a` (5GHz); 5GHz AP mode is limited to the non-DFS channels 36/40/44/48 and 149/153/157/161/165 |
| `BOA_WAN_PORT` | The port cabled to your existing network. Conditioning is applied here |
| `BOA_RESCUE_IP` | A fixed address on the bridge so the box is reachable even with no upstream DHCP |
| `BOA_USB_MAX_CURRENT` | `1` lifts the Pi 5's 600mA USB cap to the full 1.6A — **only with a 5A PSU or powered hub** |
| `BOA_USER`, `BOA_PASSWORD`, `BOA_SSH_PUBKEY` | Headless login — see below |
| `BOA_NTOPNG_PASSWORD` | ntopng's admin password. **Keep it different from `BOA_PASSWORD`** — leaving it empty falls back to that, which stores your login password on the box a second time as an unsalted MD5 |
| `AP_SSID_USB` | A different SSID for the USB radio while testing it. Empty means both publish `AP_SSID` |
| `AP_SSID_DOCKER` | The SSID used by the **container** deployment. Empty falls back to `AP_SSID`. Set it whenever both a Pi and a container host can be powered on at once |

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
A **second USB adapter** used to land in exactly that state, because the udev
rule renamed only the first one and no hostapd instance was configured for the
rest. That was [#227](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/227)
and it is fixed: every USB radio is named after its own MAC, and `radioplan`
walks each phy at boot and on hotplug and writes a config for whatever it
finds. Two adapters is now the ordinary case.

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

One thing that will mislead you: `iw dev <radio> info` reports
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

**On a corporate LAN that assumption is simply false**, and every consequence
below stops being contained: every host on that network inherits the ability to
re-shape or black-hole every device behind the box. None of the items below are
new bugs; what changes is that the containment argument they rest on evaporates.
See the warning under [Hardware](#hardware).

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
- **The box announces itself to the devices it is testing.** avahi publishes
  `<hostname>.local` on every interface it finds, and `br-lan` is one interface
  spanning the uplink *and* every client port — that is what makes the bridge
  transparent. So a device under test can resolve the box by name and reach the
  unauthenticated interface above. There is no interface list that separates the
  two sides, because by design they are the same side. The `_workstation._tcp`
  advertisement, which carried the bridge MAC and is not needed for name
  resolution, is switched off
  ([#272](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/272)).

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

## What it is not, and what the radios will not do

boa is a **link conditioner that happens to serve Wi-Fi**, not an access point
that happens to condition. Several things you would expect of an ordinary router
— or of OpenWrt on the same silicon — are missing, and the useful question about
each is *why*, because only one of the three answers is fixable here.

**It is not a router at all.** No NAT, no DHCP server, no firewall, no routing:
`dnsmasq`, `nftables` and `iptables` are all inactive by design. It is a
transparent bridge, your existing router keeps every one of those jobs, and that
is the whole point — devices under test keep their normal addresses and cannot
tell the box is there.

### Channel manipulation: what client-class silicon will not do

Both chips here are **station parts with AP mode bolted on** — mt7921 is the
client sibling of the mt7915/mt7916 access-point line, and the Pi's BCM43455 is
an embedded client chip. Everything below is an AP-side responsibility their
firmware never had to implement, and each one constrains how this box can move
a radio.

| Behaviour | Here | What an AP-class part does |
|---|---|---|
| **Channel Switch Announcement (802.11h)** | **refused by both drivers** ([#154](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/154)) | Counts the move down in its beacons; associated clients **follow, staying associated**. boa must take the BSS down and bring it up elsewhere — every client is told nothing, and must notice, rescan and rejoin |
| **DFS with radar detection** | **cannot serve there at all** | Operates on 52–144 after a channel-availability check. That is **16 of the 25** non-6 GHz 5 GHz channels, and the emptiest ones — excluded from `apChannels` because the Pi refuses to start an AP on them |
| **Off-channel scan while beaconing** | **mt7921u refuses** (`-95`) | Evaluates other channels continuously without dropping the BSS. Only the onboard radio manages it here, and that is the weak one |
| **Automatic channel selection** | **not possible on the onboard radio** | Picks a channel at startup from survey data. brcmfmac returns no survey at all, so there is nothing to choose from |
| **20/40 coexistence** | **applied to us, not by us** | Chooses its primary and secondary deliberately. Here hostapd's coex scan swaps them out from under the request: 36 at 80 MHz comes back up on **40**, and the box records where it *landed* rather than where it was sent |

**The first row is the one that shapes the product.** Because CSA is refused,
**there is no such thing as a cheap channel change on this box** — moving a
radio is an outage, and every control that moves one says so before it runs. A
real access point re-homes its clients in a few beacon intervals; this one drops
them and waits for them to come back. It is also, in fairness, what most
consumer routers actually do.

**The second row is why the band plan looks so sparse.** Non-DFS 5 GHz is two
blocks with the whole DFS range between them — 36/40/44/48 and
149/153/157/161/165 — so with two 5 GHz radios to place, and each 80 MHz block
consuming four channels, there are exactly two 80 MHz homes on the whole band.
That is the entire reason UNII-3 is in the channel table
([#162](https://github.com/jonathaneoliver/infinite-streaming-boa/issues/162)).
An AP-class part with DFS would have five more.

### The radio features, and why each is absent

| | Status | Why |
|---|---|---|
| **OFDMA / MU-MIMO scheduling** | not done | Driver, and specific to this chip. The hardware advertises HE and `Full Bandwidth UL MU-MIMO`, but `mt7921` exposes no MU counters and every frame is single-user. `mt7915` does expose them — see [above](#this-box-does-not-do-ofdma-and-that-bounds-every-figure-above) |
| **160 MHz channels** | not possible | Hardware. `iw phy` lists no 160 MHz capability on either adapter |
| **6 GHz (Wi-Fi 6E)** | **not implemented** | **Ours.** The adapter is an AX**E**3000 and the PHY offers 59 usable 6 GHz channels with AP mode among its HE Iftypes. boa neither scans nor serves there because `scanFreqs()` and `apChannels` stop at 5 GHz |
| **Mesh / 802.11s** | not used | Ours. Both adapters list `mesh point` among their interface modes; nothing here builds on it |
| **WPA3 / SAE, and PMF** | not configured | Ours. hostapd supports `sae_password`; the generated config is `wpa=2`, `WPA-PSK`, `CCMP`, with no `ieee80211w`. Two neighbours here already run WPA3 transition mode |
| **Band steering** | manual | Ours. 802.11v BSS Transition is advertised and the controls exist, but nothing steers on its own — deliberately, since a destination that moves with transient state is one you cannot run the same test against twice |

**The 6 GHz row is the one worth acting on.** Nothing about the hardware or the
regulatory domain prevents it: `iw reg get` allows 5925–7125 MHz at 12 dBm as
low-power indoor. It is simply not wired up. Adding it would widen the
contention picture considerably, since 6 GHz is where a modern router puts its
quietest, fastest BSS.

There was no 6 GHz traffic to hear when this was written — a scan of 5955–6215
MHz found nothing, and no neighbour's 5 GHz beacon carried a **Reduced Neighbor
Report**, which is how 6 GHz access points are actually discovered. The
6E-capable TP-Link nearby is heard at −19 dBm on 5 GHz with no RNR, so its
6 GHz radio is simply switched off.

### What AP-class silicon would add, in order of what it changes

Not throughput — **instruments**. Each of these was checked on this hardware.

**Fixed MCS, or rate pinning.** `iw dev wlan-usb set bitrates he-mcs-5` returns
`Invalid argument (-22)` here. That one gap is why `Policy.Rssi` and the whole
distance model exist: boa cannot *make* a weak link, so it **models** one with
netem and says plainly that the mapping is "plausible rather than fitted" — see
[DATA-CONTRACT](docs/DATA-CONTRACT.md) Source S. On silicon that honours rate
pinning you would force a client to MCS 2 and get a genuinely degraded PHY: real
retries, real aggregation collapse, real airtime inflation. That is the
difference between *simulating* a distant client and *having* one, and it is the
single biggest upgrade available to this box.

**Spectral scan.** `ls /sys/kernel/debug/ieee80211/phy0/` offers nothing
spectral. ath9k and ath10k expose raw FFT data; this box can only see things
that **beacon**. A microwave, a baby monitor, a wireless camera or a failing
power supply are all invisible to it, and would surface only as airtime nobody
claims. It is the missing answer to "the channel is busy and no access point is
there".

**More than one BSS per radio.** No hostapd config here carries a `bss=` line —
one SSID per radio, where AP-class parts run eight to sixteen. With several you
could serve an 802.11n-only BSS and an ax BSS **on the same radio** and steer a
client between them, testing capability negotiation without touching the
channel. Today the `profile` control does it by restarting the access point,
which drops every client on it, because it is changing the radio rather than
offering a choice.

**Airtime fairness enforcement.** boa now *measures* per-client airtime; it
cannot shape it. ATF on the AP-class drivers caps a client's **airtime share**
rather than its bitrate — a conditioning axis this box does not have, and
arguably the more honest one for Wi-Fi, since airtime is what clients actually
contend for and bitrate is only its consequence.

Beyond those: **802.11r** fast transition is unconfigured, so roaming tests can
exercise only the 11v steer and the client's own decision; **4×4** parts double
the two-stream ceiling and enable real downlink MU-MIMO; and per-VAP transmit
power is actually *applied*, where here it is
[a control that validates and then ignores](docs/DATA-CONTRACT.md).

### The two radios are not equivalent, and the gaps are asymmetric

| | Onboard (brcmfmac) | USB (mt7921u) |
|---|---|---|
| Scan while serving | **yes**, both bands, ~1.3 s, no outage | **no** — refuses; needs the BSS taken down |
| Per-station signal (RSSI) | **absent from `iw station dump` entirely** | yes, with per-antenna values |
| Per-station airtime | **absent** | yes — `tx/rx duration` |
| Channel survey / airtime | **returns nothing at all**, not zero | yes, though `busy` reads ~5× low |
| Monitor mode | **refused** (`-95`), not in supported modes | yes, and a monitor vif coexists with a live AP |

So the onboard radio is the only one that can *look*, and the USB adapters are
the only ones that can *report*. That asymmetry is why one free scan on the
onboard radio answers for every radio on the box, and why the per-client airtime
chart is blank on the onboard one — see
[DATA-CONTRACT](docs/DATA-CONTRACT.md) Sources L, O and T.

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

Shared by both targets:

```
daemon/               Go daemon; embeds the compiled UI, ships as one binary
daemon/cmd/boactl/    terminal client for the API, and the `probe` assertions
ui/                   Vue 3 + TypeScript interface
PRD.md                product behaviour source of truth
docs/API.md           generated HTTP reference; regenerate with -update, never by hand
docs/DATA-CONTRACT.md where every displayed number comes from and what it means
docs/LICENSING.md     what may be redistributed, and what may not
docs/BACKLOG.md       accepted limitations; candidate work lives in issues
```

The Pi image:

```
build.sh              orchestrates the build; validates .env
scripts/customize.sh  all image surgery; runs in a privileged arm64 container
scripts/build-payload.sh  builds the UI and cross-compiles the daemon
scripts/deploy.sh     pushes a new binary to a running Pi
overlay/              staged files grafted into the image root
```

The container:

```
docker/Dockerfile     debian:trixie-slim, plus radioplan copied from overlay/
docker/compose.yml    one service, network_mode: none, three capabilities
docker/entrypoint.sh  the bridge, the radios, iperf3, boad, and the hotplug loop
docker/systemctl      shim answering the two calls the daemon makes of systemd
docker/boa-hostapd-supervise  keeps one hostapd per radio alive
docker/udhcpc.script  the lease script Debian's busybox does not ship
scripts/docker-deploy.sh    builds here, ships and restarts there
scripts/docker-host-net.sh  prepares the host's network; reversible
scripts/docker-attach.sh    moves the adapters into the container's namespace
```

## Development

Five loops, fastest first. Pick the slowest one you actually need. Loops 1 and 2
are target-agnostic; loop 3 is the Pi and loop 4 the container.

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

### 4. Full deploy to a container host — about a minute

```sh
scripts/docker-deploy.sh <host>
```

The container equivalent of loop 3, and the same split: the interface and the
daemon are built here, and only the finished artefacts are shipped. It is slower
than `deploy.sh` because the image is rebuilt and the container recreated rather
than one binary being replaced, and correspondingly there is nothing that needs
a reflash.

`scripts/dev.sh <host>` from loop 2 works against a container host as well, so
interface work does not need this loop either way.

First-run setup is separate and documented in
[Run it as a container on a Linux host](#run-it-as-a-container-on-a-linux-host).

### 5. Driving a box from the terminal — `boactl`

```sh
cd daemon && go build -ldflags "-X main.version=$(../scripts/version.sh)" \
    -o ~/.local/bin/boactl ./cmd/boactl
```

The API has 55 endpoints and they were previously reached with hand-assembled
`curl`, which is fine until it isn't: a typo in a path returns an HTML error
page that decodes into a zero-valued struct and reads exactly like a healthy
empty answer. `boactl` reports the status line instead.

```sh
boactl state                     # what the box is doing right now
boactl devices                   # a line per client, with what the KERNEL enforces
boactl bridge                    # radios, channels, how contested each one is
boactl shape "Apple TV" -down 5 -delay 40 -loss 0.5
boactl sweep "Apple TV" -service netflix  # measure its rendition ladder
boactl pattern play "Apple TV" -name ramp_down   # and: pattern stop, pattern list
boactl radio wlan-usb-46c7 scan           # free on the onboard radio
boactl radio wlan-usb-46c7 channel -to 149  # DROPS every client on that radio
boactl link "Apple TV" deauth             # and: disassoc, deadzone, steer, measure
boactl events -follow > run.ndjson        # what HAPPENED, as it happens
boactl history -window 10m -o run.csv     # what the link was DOING, per second
boactl config get -o boa-config.json      # and: boactl config apply <file>
boactl probe                     # assert the box is really doing its job
```

The box defaults to `$BOA_BOX`, then `infinite-streaming-boa.local`. The help
groups commands by whether they only look, change a live network, or assert —
`boactl -h`, and `boactl <command> -h` for one command's flags.

**The two halves of a captured run.** `events` says what happened — a roam, a
deauth, a pattern step. `history` says what the link was doing while it
happened, with the cap that was in force at each point, as CSV one row per
client per bucket. Neither is much use alone: lining a player's behaviour up
against the cap that caused it is the whole point of the box. `bucket_ms` is a
column rather than a header so a redirected file still says what resolution it
carries — on a long window the box means several ticks together, and a row is
then not one second.

It lives inside the daemon's own module and imports `internal/boa` directly, so
the response types are the daemon's own — there is no second copy of the wire
contract to keep in step. `shape` reads the device's current revision and sends
it as `base_revision`, so a stale terminal loses the race rather than silently
clobbering somebody editing the same device in the interface.

**`boactl probe`** is the part worth having. It asserts rather than prints, and
exits non-zero when the box is not doing what it claims:

```
PASS  cap enforced            6b:80:11 down: 5 Mbps, read back from tc
PASS  cap is capping          6b:80:11: 4.9 Mbps down, overlimits +271775 in 5s
WARN  filter coverage         1 conditioned client(s) hold several addresses
```

Most of it needs no SSH, because `Counters.CapMbps` is read back from `tc`
rather than echoed from the request — so comparing it against the policy is a
real enforcement check over plain HTTP, and catches a shape applied to nothing.
`-ssh` adds what the API cannot answer: whether every routable address of a
conditioned client has its own filter (privacy extensions mean a device usually
holds several, and one filter is a *partial* shape that looks like a working
one), and whether hostapd's BSS is genuinely `ENABLED` rather than merely
`active`.

`docs/API.md` is the generated reference for every endpoint. It is a golden
file — `TestAPIDocsUpToDate` fails if it and the source disagree — so unlike a
spec that has to be regenerated by hand, it cannot quietly go stale.

### Useful

```sh
cd ui && npm run typecheck              # vue-tsc, no build
cd daemon && go vet ./...               # daemon also compiles on macOS
cd daemon && go test ./internal/boa/    # includes the wire-contract and API-doc checks
ssh boa@infinite-streaming-boa.local 'journalctl -u infinite-streaming-boa -f'
# Absolute path: /usr/sbin is NOT on the PATH of a non-login SSH shell, and a
# bare `tc` fails with "command not found", which reads as a missing package.
ssh boa@infinite-streaming-boa.local '/usr/sbin/tc -s class show dev wlan0'
```

## How this was built

boa is a reimagining of a link conditioner I had built by hand once before. The
idea is the same; the implementation is not. This version was written end to end
with [Claude Code](https://claude.com/claude-code) — the Go daemon, the Vue
interface, the image build, the systemd and network plumbing, the docs and the
tests — over about four days. The hand-built original took roughly four weeks.

## Licence

MIT — see `LICENSE`. Every dependency is permissive; `docs/LICENSING.md` records
the audit and the one rule that keeps it that way: **ship the build scripts, not
built images.** A built image is a derivative of Raspberry Pi OS and carries
several hundred packages' worth of obligations that this repository does not.
