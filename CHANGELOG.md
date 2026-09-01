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

[Unreleased]: https://github.com/jonathaneoliver/infinite-streaming-boa/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/jonathaneoliver/infinite-streaming-boa/releases/tag/v0.1.0
