# Changelog

All notable changes to boa are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); the version is stamped
into the daemon at build time from the git tag (see `scripts/version.sh`), and a
running box shows it in the footer of the web interface and prints it with
`boad -version`.

This is a bench appliance, not a certified instrument. The limitations below are
deliberate and documented so they are not mistaken for defects — see
[`docs/BACKLOG.md`](docs/BACKLOG.md) and the constraints in [`PRD.md`](PRD.md).

## [Unreleased]

Nothing yet.

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

[Unreleased]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/releases/tag/v0.1.0
