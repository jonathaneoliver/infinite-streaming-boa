# Backlog

Candidate work, with the reasoning that produced it. The titles are the cheap
part — what matters here is *why* each item exists, what mechanism it would use,
and which constraint it runs into. That context is what stops a decision being
re-litigated from scratch in three months.

Sizes are rough: **S** ≈ an hour, **M** ≈ half a day, **L** ≈ a day or more.

Kernel capability notes below were **verified on this hardware** (Raspberry Pi 5,
Debian 13 trixie, kernel 6.18) on 2026-08-29, not assumed.

---

## Pending — started, not finished

### Capture the ntopng build as a reusable artifact
**Size: S.** `scripts/package-ntopng.sh` is written but has not been run.
ntopng had to be compiled from source because ntop publishes x86-64 binaries
only, Docker Hub's image is amd64 only, and Debian dropped the package after
buster. The build takes ~7 minutes on a Pi 5, but a reflash currently loses it.
The script stages `make install DESTDIR=…` into `cache/ntopng-arm64.tar.gz` and
resolves runtime library packages from the built binary with `ldd`;
`customize.sh` already grafts that into any image it builds. Run it while the
build tree at `/opt/ntop` is still fresh.

### Measure shaping accuracy with ntopng running
**Size: S.** ntopng now runs permanently, doing deep packet inspection on every
packet on the same box that produces the measurements. The earlier sweep (caps
1.5→50 Mbps landing within −4.5%, which is framing overhead) was taken with it
stopped. Re-run and compare. If the numbers hold, always-on is free and can stop
being a consideration; if they drift, that has to be known before trusting any
measurement taken while it is up.

---

## Open decisions — blocked on a choice, not on work

### Shared bottleneck budget
**Size: M either way.** Every client currently has an independent cap, so three
devices capped at 25 Mbps get 75 Mbps between them. A real household shares one
uplink. HTB supports this natively: per-client classes become children of a
parent holding the total, borrowing up to their own ceiling.

The constraint is real. **HTB works per interface**, and downlink is deliberately
shaped on each client's *own* port because that is what delivered the pacing
accuracy (netem's per-packet serialisation, no token-bucket burst at segment
start). Wireless and wired clients therefore sit on different qdiscs and no
single one sees them all.

- **Per-port budget** — works today, keeps the accuracy, but is two budgets
  ("the Wi-Fi shares 30 Mbps", "the wired port shares 30 Mbps") rather than one.
- **Global budget** — needs downlink moved back to an `ifb` at the WAN port, so
  the packet then crosses the bridge and the client port's own queue after being
  paced, which can re-bunch what the shaper spaced.

For video-player testing, per-port is the better trade. For modelling a shared
household link end to end, global is more faithful.

### Segment-cadence / rendition-switch analyser
**Size: L.** Manifests are unreadable in practice (see *Known limitations*), but
encrypted segment fetches still have an obvious signature: periodic requests, a
few seconds apart, of characteristic size. From that, the delivered rendition
bitrate and the moment of a switch can be derived — which is the actual question
behind "I throttled to 3 Mbps, did the player step down, and how long did it
take?". pifi already reads every packet, so the raw material is there. Decide
whether this is the product or a distraction.

---

## Correctness gaps

### IPv6 traffic is neither shaped nor counted
**Size: M. Highest-priority defect.** Every `tc` filter is installed as
`protocol ip`, which matches IPv4 only. A client using IPv6 is therefore
completely unconditioned and reports zero throughput, while the interface shows
its policy applied — the same silent failure mode as the earlier discovery and
counter bugs, and the worst kind this box can have.

Confirmed on 2026-08-29: all nine filters on wlan0 were `protocol ip`, and the
test network carries IPv6 (ULA prefix `fdd5:…/64` with router advertisements).

The fix is a parallel set of `protocol ipv6` filters matching on the IPv6
source/destination address, pointing at the same classes — so one policy covers
both families. The address is 16 bytes rather than 4, at a different header
offset, so `matchArgs` needs an IPv6 branch. Client discovery also currently
learns IPv4 bindings only; a dual-stack client would need its v6 address learned
alongside its v4 one.

Until this is fixed, a client that prefers IPv6 will appear to be doing nothing.

## Impairment realism

All mechanisms below were confirmed present on this kernel.

### Time-varying profiles (pattern engine)
**Size: L. Highest value.** The single biggest functional gap. Commercial
impairment boxes call these *scenarios*: drive rate/delay/loss along a timeline —
30s clean, 10s dead, 20s degraded, loop — modelling a tunnel, a lift, a congested
cell. pifi's presets are static snapshots by comparison, and for ABR testing
watching a player adapt *through* a transition is the whole point; a fixed cap
only ever tests steady state.

A proven design already exists in the infinite-streamer harness (pattern steps
with a server-side runtime index written back into state for UI visualisation).
Reuse it rather than reinventing: the revision/reconciliation semantics there
were hard-won.

### Bursty loss (Gilbert-Elliott)
**Size: S.** `loss 2%` today is independent per packet, which essentially never
happens in reality — real loss arrives in bursts. netem's `loss gemodel` and
`loss state` (4-state Markov) model that. TCP and QUIC behave very differently
under 2% bursty versus 2% uniform, so this is a correctness issue in the
emulation, not a nicety. Verified available.

### Reorder, duplicate, corrupt
**Size: S.** All three are netem flags, all verified available. `corrupt`
introduces bit errors that fail checksums; `reorder` matters because TCP reacts
badly to out-of-order delivery. Cheap to expose, and they round out the model.

### Buffer depth and queue discipline
**Size: M.** "50 Mbps behind a bloated FIFO" and "50 Mbps behind fq_codel" are
completely different experiences at the same rate, and latency-under-load is
central to video QoE. `cake`, `fq_codel`, `codel` and `sfq` are all present.
Currently the netem queue depth is derived from the bandwidth-delay product and
not otherwise exposed.

### Heavy-tail delay, and rate with packet overhead
**Size: S.** `distribution pareto` / `paretonormal` model real latency tails
better than the normal distribution currently used. `rate RATE [OVERHEAD [CELL]]`
models link-layer framing and cellular cell quantisation. Both verified.

---

## Fault injection

### Blackhole a device for N seconds
**Size: S.** Instant "drove into a tunnel" test — drop everything for a client
for a set duration, then restore. Trivially useful and trivially built.

### DNS and connection-level faults
**Size: M.** Toxiproxy-style: delay or NXDOMAIN a client's DNS, inject TCP RSTs
to simulate carrier resets or captive-portal drops. Needs nftables rather than
tc, so it is a different mechanism from everything else here.

---

## Visibility

### Device hostnames
**Size: S.** The device list shows bare MACs. ntopng already resolves the same
devices to names (it labels the test iPhone `jonathansiphone`) via DHCP and mDNS
snooping. Either snoop DHCP option 12 directly in the learner — pifi already
reads every packet — or read it from ntopng's REST API. The first has no
dependency; the second is less code.

### Per-client packet capture
**Size: M.** A capture button per device, scoped by the MAC→IP→port mapping pifi
already maintains, bounded by size and duration, written to tmpfs to spare the SD
card, downloadable as a `.pcap`. Note ntopng's Live Capture already covers much
of this — check whether it is redundant before building.

### Manifest extraction for plaintext HLS/DASH
**Size: M.** For test content served over HTTP (rather than HTTPS), `.m3u8` and
`.mpd` bodies can be extracted passively from the packet stream pifi already
reads. No interception, no certificates. Only works for content you control.

---

## Verification

### Built-in iperf3 server
**Size: S.** Lets the box prove what it is delivering rather than being trusted.
kaa's Dockerfile already installed `iperf`, so this want predates pifi.

### Continuous latency probe
**Size: M.** Show measured RTT beside configured RTT, so induced latency is
observed rather than assumed. Could also derive per-client RTT passively from
TCP handshakes, which needs no probing at all and fits the transparent-bridge
principle better.

---

## Workflow

### Groups
**Size: M.** Apply one profile across several devices at once. The
infinite-streamer DRD had `group_id` with server-side propagation, specifically
so the result was consistent regardless of which UI initiated the change. Same
reasoning applies.

### Profile library, import/export, scheduling
**Size: M.** Save named scenarios beyond the built-in presets; export and import
them so a test configuration is reproducible and shareable; optionally apply on a
timetable.

---

## Known limitations — accepted, documented so they are not rediscovered

- **No per-station RSSI.** The Pi 5's Broadcom radio reports no `signal` line in
  `iw station dump` in AP mode. The UI shows `tx-fail` as the link-quality proxy
  instead. Not fixable in software.
- **ntopng is a source build.** No apt security updates; rebuild by hand. It is
  deliberately kept out of `customize.sh` as a compile — only the prebuilt
  artifact is grafted in.
- **Netflix manifests are unreadable.** Certificate pinning defeats a MITM proxy,
  and Netflix wraps manifest and licence traffic in its own Message Security
  Layer *above* TLS, so even a successful interception yields encrypted blobs
  rather than an `.m3u8`. Manifest-level work needs a proxy that is the origin
  path — which the infinite-streamer harness already is, and which composes with
  pifi rather than competing with it.
- **A truly global downlink budget conflicts with client-port pacing.** See
  *Shared bottleneck budget* above.
