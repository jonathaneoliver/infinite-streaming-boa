# Data Contract — boa monitoring and enforcement

Written **before** the daemon's transform code, per the project convention that
data semantics get agreed up front rather than discovered during QA.

Each source below lists: where the data comes from, the exact fields, **what
they mean** (the semantics that bite), edge cases, and a confidence level.

- **VERIFIED** — checked empirically against iproute2 6.15 in a container on
  2026-08-28. Field names and units below are copied from real output.
- **HIGH** — stable documented interface, not yet run on this hardware.
- **NEEDS-TEST** — must be confirmed on a booted Pi before trusting it.

---

## Source A — DHCP leases · NOT USED IN BRIDGE MODE

Retained for reference only. As a transparent bridge boa runs no DHCP server,
so there is no lease file to read: addresses come from the upstream router and
are learned by ARP (Source F) instead.

Whitespace-separated, one lease per line:

| Field | Meaning |
|---|---|
| 1 | Lease expiry, **absolute** Unix epoch seconds — not a duration |
| 2 | Client MAC |
| 3 | Assigned IPv4 |
| 4 | Hostname the client volunteered, or `*` if it sent none |
| 5 | DHCP client id, or `*` |

**Semantics that bite**

- A lease is **not proof of presence**. It survives the client disconnecting,
  right up to expiry. Never render "connected" from a lease alone.
- Hostname is client-supplied, absent on privacy-conscious phones, and not
  unique. It is a display label, never an identity.
- A client with a static IP never appears here at all.

## Source B — `iw dev wlan0 station dump` · presence and radio truth

**HIGH.** Text output, parsed per `Station <mac>` stanza. This is the
**authority on who is actually associated.**

| Field | Meaning |
|---|---|
| `Station <mac>` | Client MAC. No IP here — join to A or C to get one |
| `signal` | Instantaneous RSSI in dBm, negative; noisy by ±5 dB |
| `tx bytes` / `tx packets` | AP → client. This is **downlink** |
| `rx bytes` / `rx packets` | client → AP. This is **uplink** |
| `tx bitrate` | Negotiated PHY rate in Mbps |
| `connected time` | Seconds since association |
| `inactive time` | Milliseconds since last frame |

**Semantics that bite**

- Direction is **from the AP's perspective**. The AP's `tx` is the client's
  download. Getting this backwards inverts every graph in the UI.
- Counters **reset to zero on re-association**. A naive `now - last` rate
  calculation yields a large negative number across a reconnect. Detect
  `current < previous` and start a new counter epoch instead of emitting a spike.
- `tx bitrate` is the **modulation rate the radio negotiated**, not achieved
  throughput. It routinely reads 400+ Mbps on a link moving 2 Mbps. Label it in
  the UI as "PHY rate" and never let it be mistaken for bandwidth.
- `inactive time` is the practical liveness signal; a station can remain listed
  briefly after walking out of range.

## Source C — `ip -j neigh show dev wlan0` · MAC↔IP fallback

**HIGH.** Native JSON (`-j`), so no text parsing.

| Field | Meaning |
|---|---|
| `dst` | IPv4 address |
| `lladdr` | MAC |
| `state` | `REACHABLE`, `STALE`, `DELAY`, `PROBE`, `FAILED` |

Exists to catch static-IP clients invisible to Source A. `STALE` entries may be
long dead; `FAILED` means gone. Treat as a hint for address resolution only,
never as presence.

## Source D — `tc -s -j class show dev <iface>` · enforcement counters

**VERIFIED.** Real output from the stack boa builds:

```json
{"class":"htb","handle":"1:10","leaf":"0x110","rate":1500000,"ceil":1500000,
 "stats":{"bytes":0,"packets":0,"drops":0,"overlimits":0,
          "backlog":0,"qlen":0,"lended":0,"borrowed":0}}
```

| Field | Meaning |
|---|---|
| `handle` | Class id, e.g. `1:10`. boa's own map binds this to a device + sub-class |
| `rate` / `ceil` | **BYTES per second** — see below |
| `stats.bytes` / `stats.packets` | Cumulative since the class was **created** |
| `stats.drops` | Packets dropped because the queue was full |
| `stats.overlimits` | Times the class hit its rate ceiling |
| `stats.backlog` / `stats.qlen` | Bytes / packets queued *right now* |
| `stats.lended` / `stats.borrowed` | HTB bandwidth lending between classes |

**Semantics that bite**

- **`rate` is in bytes per second, not bits.** Verified: `tc ... rate 12mbit`
  reports `"rate":1500000`, and `rate 1500kbit` reports `"rate":187500`.
  Multiply by 8 to display Mbps. Getting this wrong under-reports every
  configured cap by a factor of 8 while looking entirely plausible.
- `stats.bytes` **resets to zero whenever the class is recreated** — which
  happens on every policy edit that changes the rate. Treat a policy write as a
  counter-epoch boundary, or throughput graphs will show a cliff to zero on
  every slider move.
- **`overlimits` is not an error.** It is HTB doing its job — the count of times
  traffic wanted to exceed the cap. A healthy throttled client shows this rising
  constantly. It must not be surfaced as a fault.
- `backlog` is the honest real-time congestion signal: sustained non-zero
  backlog means the client is actively being held back right now.

## Source E — `tc -s -j qdisc show dev <iface>` · netem parameters

**VERIFIED.** Real output:

```json
{"kind":"netem","handle":"110:","parent":"1:10",
 "options":{"limit":2000,"delay":{"delay":0.04,"jitter":0.005},
            "loss-random":{"loss":0.005}}}
```

| Field | Meaning |
|---|---|
| `options.delay.delay` | **SECONDS.** `0.04` = 40 ms |
| `options.delay.jitter` | **SECONDS.** `0.005` = 5 ms |
| `options["loss-random"].loss` | **FRACTION.** `0.005` = 0.5 % |
| `options.limit` | Queue depth in **packets** |

**Bursty loss reports under a different key.** When a shape asks for correlated
loss, boa writes `loss gemodel` instead of `loss`, and the readback carries
`options["loss-gemodel"]` with `p`, `r`, `h`, `k` — *not* `loss-random`, and no
`loss` field at all. Anything reading loss back has to handle both shapes, or a
bursty client silently reports zero configured loss.

The four are the Gilbert-Elliott transition and error probabilities, not the two
numbers the operator set. Recover those with

```
mean loss   L = p / (p + r)
mean burst  B = 1 / r          (packets)
```

which holds because boa always writes `1-h = 100%` and `1-k = 0%`. Report the
derived pair rather than the raw four: `p = 0.204%` answers no question anyone
asked, and the round trip is what would catch a kernel clamping something.

**Semantics that bite**

- Three different units from the three fields a user sets in one row of the UI.
  Milliseconds and percent exist only in the API and the UI; the kernel boundary
  is seconds and fractions. Convert in exactly one place.
- **`limit` is a silent-loss trap.** netem's default queue is 1000 packets. The
  bandwidth-delay product of a 50 Mbps link at 500 ms is roughly 2100 full-size
  packets — so a "50 Mbps, 500 ms, 0 % loss" profile would drop ~50 % of traffic
  while reporting zero configured loss, and the tester would blame the
  application. boa must size `limit` from the configured rate and delay
  (`rate_bps x delay_s / (8 x 1500)`, with headroom) rather than accept the
  default.

---

## Derived model — the join

```
identity      = MAC                       (stable across DHCP renewal)
ip            = resolved attribute        (F, else C; may be absent)
port          = from B (wifi) or C2 (wired); REQUIRED to shape downlink
present       = appears in B, C2 or F     (NOT "has a lease")
label         = user-set nickname, else G.name, else MAC
policy        = keyed by MAC, persisted   (survives IP change and disconnect)
counters      = D + E, keyed by classid   (epoch-bounded per policy write)
throughput    = counter delta / measured elapsed, per poll  (a RATE, not a total)
sustained     = SUM(rate_j x interval_j) / SUM(interval_j)  over the trailing 30s
```

**`sustained` is a byte delta over a time delta, not a mean of rates.** Each
sample is a rate the daemon derived over its own interval, so `rate x interval`
is the bytes that sample stands for; summing those and dividing by the summed
intervals answers "how much arrived, over how long".

The two are identical when every interval is the same width, which is why the
distinction is easy to miss — and they diverge exactly where it matters. Long
ranges are decimated into wider buckets on the way out, so a window near a range
boundary straddles two widths, and any pause leaves one interval far longer than
the rest. An unweighted mean of the rates would quietly over-weight the short
intervals.

It is derived from the rate series rather than from raw counters deliberately:
`rate` already reports 0 across a counter epoch, so a class recreated by a
policy write cannot produce a negative spike. Averaging raw byte totals would
have to solve that again.

Join order is **B LEFT JOIN (A, C)**: presence comes from the radio, addresses
are decoration. A client that is associated but has not completed DHCP is a real
and common state — it must render in the UI as present-but-unaddressed, and it
cannot be shaped yet, because every `tc` filter needs an IP to match on.

## Enforcement semantics

Both directions are shaped on a **true egress queue**, on the last interface the
packet crosses before leaving boa:

| Direction | Where | Filter matches |
|---|---|---|
| Downlink (internet → client) | egress of the **client's own port** (wlan0/lan0) | dest address |
| Uplink (client → internet) | egress of the **WAN port** | source address |

**Both address families.** Every class carries an IPv4 filter plus one IPv6
filter per routable v6 address the client holds. Filtering IPv4 alone left a
client that prefers IPv6 entirely unconditioned and reporting zero throughput
while the interface showed its policy applied. Privacy extensions mean a device
usually holds several v6 addresses at once and rotates them, so the address set
is tracked and each member gets its own filter — shaping one of them would shape
only part of the traffic. Link-local addresses are excluded: they never leave
the segment.

Downlink accuracy is the priority, because the primary use is throttling
streaming video on its way to a player. Shaping on the client's own port means
the shaper is the last thing to touch the packet, so the inter-packet spacing
the player measures is exactly the spacing configured. The alternative — catch
traffic at WAN ingress and redirect it to an `ifb` device — also limits the
rate, but the packet then still crosses the bridge and the client port's own
queue, which can re-bunch what the shaper carefully spaced.

A consequence: the downlink class lives on whichever port the client is
currently attached to, so it **moves** when a device roams between Wi-Fi and the
wired port. The reconciler treats that as an ordinary change.

**The rate is enforced by netem, not HTB.** HTB is a token bucket: while a class
is idle it accumulates credit, and when traffic resumes it releases a burst at
full line rate before pacing takes effect. That is precisely the moment a video
player starts a segment and measures throughput, so a token bucket
systematically inflates the player's bandwidth estimate at the start of every
segment — the exact measurement under test. netem's rate model has no credit to
accumulate: it computes each packet's serialisation time from its length and
spaces packets accordingly, which is what a real slow link does. HTB is
therefore kept purely as a classifier and per-client byte counter, with its rate
set to line rate so it never binds.

`delay_ms` is **per direction**. Setting 100 ms both ways adds ~200 ms to RTT.
The UI shows the computed round-trip figure next to the two inputs, because this
is the single most misread control in a bidirectional conditioner.

### Measured accuracy

Verified against real forwarded traffic on 2026-08-28, client in a separate
network namespace across a bridge:

| Configured cap | Delivered goodput | Difference |
|---|---|---|
| 1.5 Mbps | 1.42 Mbps | −5.3 % |
| 3 Mbps | 2.86 Mbps | −4.6 % |
| 6 Mbps | 5.70 Mbps | −4.9 % |
| 12 Mbps | 11.45 Mbps | −4.5 % |
| 25 Mbps | 23.87 Mbps | −4.5 % |
| 50 Mbps | 47.75 Mbps | −4.5 % |

A configured 200 ms one-way downlink delay measured 200.6 ms RTT.

The systematic −4.5 % is **correct, not error**. netem paces bytes on the wire;
an application counts payload. A 1500-byte frame carries 1448 bytes of payload
after IP, TCP and timestamp headers, and sits inside a 1514-byte Ethernet frame:
1448/1514 = 95.6 %. A real link of the same speed delivers the same goodput. If
a 50 Mbps cap measured a clean 50.0 Mbps at the application, the emulation would
be wrong.

## Honest limitations — surface these in the UI

- **WiFi is not a wired lab.** Airtime is shared: one client's traffic changes
  another's achievable rate no matter what the shaping says. Conditioning is
  **additive on top of a variable baseline**, not absolute.
- The air interface's own latency and loss are neither zero nor constant. A
  "0 ms, 0 %" profile is not a clean link.
- The Pi's uplink (`eth0`) is a shared bottleneck. The sum of client caps can
  exceed it, at which point the caps stop being the binding constraint.
- `tc` counts bytes **at the queue**, including retransmissions and headers. It
  is not application goodput and will read higher.

---

## Source F — passive ARP · MAC→IP in bridge mode

**VERIFIED** on 2026-08-28. An `AF_PACKET`/`SOCK_DGRAM` socket opened with
protocol `ETH_P_ARP`, which makes the kernel deliver ARP frames only — no BPF
program or packet-filter dependency.

A transparent bridge issues no leases and is not the destination of most
traffic, so the kernel's own neighbour table stays largely empty: Linux updates
existing entries from overheard ARP but will not create new ones for exchanges
it is not party to. A bridge does, however, physically receive every ARP
broadcast on the segment, and any active client ARPs for its gateway regularly.
Listening is therefore accurate, immediate and silent — no probing, no scanning,
nothing injected onto a network boa is meant to be invisible on.

**Semantics that bite**

- **The arrival interface is the BRIDGE, not the physical port.** The bridge
  rewrites `skb->dev` before delivering locally, so a frame that physically
  arrived on wlan0 is reported as arriving on br-lan. An early version of boa
  discarded bridge-delivered frames in order to read the port from them, which
  silently discarded *every* frame and left every client undiscovered and
  unconditioned while the interface showed policies applied.
- ARP therefore answers **only** "what address does this MAC hold". The port
  comes from the forwarding database and the wireless station table, which are
  the right tools for that question and were verified to work on real forwarded
  traffic.
- Hosts upstream of boa ARP too. Anything the forwarding database places on the
  WAN port is excluded, or the client list fills with the rest of the network.
- Entries older than 5 minutes are dropped: a stale binding would aim a shaping
  filter at an address that may since have moved to another device.

## Source G — mDNS announcements · MAC→name

**VERIFIED** in a container on 2026-08-29, against a bridge with two namespaces
hanging off it. An `AF_PACKET`/`SOCK_DGRAM` socket opened with `ETH_P_ALL` and a
hand-assembled classic BPF program passing UDP port 5353. Multicast listeners on
224.0.0.251 and ff02::fb are the fallback, and run only when that socket cannot
be opened.

Devices announce themselves unprompted on the mDNS group, and a bridge sees
every one of those frames. Nothing is queried and nothing is injected. Only A
and AAAA answers are read: `name` → the address the record binds it to.

| Field | Meaning |
|---|---|
| Source MAC | From the frame's link-layer header. **The key.** |
| Source IP | Which of the announced addresses the sender is speaking from |
| A / AAAA rdata | An address the sender claims |
| Record name | What it calls that address, minus the `.local` suffix |

**Semantics that bite**

- **A name must be keyed by MAC, not by address.** Measured on a real network on
  2026-08-29: 16 of 22 learned bindings were IPv6 against 6 IPv4, and boa learns
  a client's IPv6 addresses only by observing its traffic. An idle device
  announcing over v6 has no address boa knows, so an address-keyed name can
  never be joined to it. The MAC is on the frame either way.
- **`ETH_P_ALL`, not `ETH_P_IP`.** A protocol-specific packet socket only
  receives frames delivered LOCALLY; the bridge claims a forwarded frame first.
  The BPF program does the protocol selection the socket cannot.
- **BPF offsets start at the IP header**, not the Ethernet header, because
  `SOCK_DGRAM` has already pulled the link-layer header when the filter runs.
  A filter written against a frame — as every tcpdump example is — passes
  nothing at all.
- **Every multicast frame arrives twice**: once at ingress on the physical port,
  once when the bridge delivers a copy locally with `skb->dev` rewritten to
  itself. The bridge's copy is skipped.
- **Only announcements arriving on a downstream port are recorded.** A bridge
  hears the whole segment, and on a real network most of what announces is
  upstream — measured on 2026-08-29, 11 devices named in ten minutes, of which
  one was a client. Upstream names are useless to a per-client conditioner, they
  are what would evict a real client's name from a full table, and writing the
  neighbouring network's device names to the SD card is not this box's business.
  This is the same downstream test the client list uses, applied at the arrival
  port, which `ETH_P_ALL` reports before the bridge rewrites it.
- The multicast listeners cannot make that distinction: they are handed a
  payload, with no MAC and no arrival port. That is why they are the fallback
  rather than the primary path.
- The name in a packet is not always the sender's. Bonjour Sleep Proxy answers
  on behalf of a sleeping device, so only a record matching the packet's own
  source address is attributed to the sender.
- Announcements are a trickle — a handful of packets every few minutes — so the
  sampled socket of Source F is the wrong instrument: it drops most of what it
  sees by design, which is precisely the one packet carrying a name.
---

## Derived — rendition ladders from a cap sweep

**Source:** the per-client downlink throughput series (Source D, derived between
`tc` class polls), sampled at the tick and held for 300 samples.

Nothing new is collected. A ladder is arithmetic over numbers already on the
page.

| Field | Units | Meaning |
|---|---|---|
| `ladder.rungs[].mbps` | Mbps | Delivered bitrate of one rendition: the **mean** over one observation window |
| `ladder.rungs[].unstable` | bool | The window's two halves disagreed by more than 20% of its mean |
| `ladder.provenance` | enum | `measured` (swept), `typed` (hand-entered), `fetched`, `inferred` |
| `sweep.cap_mbps` | Mbps | Cap held during the current level. **0 means unconditioned**, not zero |
| `sweep.levels[].rate_mbps` | Mbps | The observed plateau at that level: the window mean |
| `sweep.levels[].drift` | fraction | \|mean(first half) − mean(second half)\| **over** the window mean. Not a percentage |
| `sweep.levels[].saturated` | bool | The mean was ≥ 85% of the cap: the client had not dropped below it |

**When a level is measured.** Not after a fixed wait. A player still on a
rendition it can no longer afford fetches back-to-back and stays pinned to the
cap; when it drops, idle gaps appear and throughput falls away. The sweep waits
for that departure, then for two consecutive windows to agree, and only then
opens its observation window.

Measured on a real iPhone at a 26.4 Mbps cap: forty seconds pinned to the cap
with **zero idle samples**, then a fifteen-second transition, then a steady
plateau. A 45 s fixed settle opened the window mid-transition and reported
**16.4 Mbps for a rung that sat at 14.3** — and an under-settled level does not
look noisy, it looks like a confident rung between two real ones.

**Why the mean, and not a median.** A player does not deliver its rendition at a
steady rate: it fetches a segment at line rate and then goes idle, so the 1 Hz
series is **bimodal** — bursts separated by zeroes. A median over a bimodal
sample lands on whichever mode holds more than half the samples, which is a rate
the traffic never carried.

Measured on a real iPhone over 120 samples: **mean 13.52 Mbps, median 16.75
Mbps**, 18% of samples at idle. The mean is the delivered rendition; the median
is an artefact of the duty cycle.

The mean is also what the underlying claim says. "One segment of media per
segment duration" is a statement about *bytes over time*, and bytes over time is
the mean. VBR is handled by making the window span several segments — not by
choosing a different statistic.

**Why drift, and not a spread.** An interquartile spread is meaningless on
bursty data: a duty-cycled fetch sitting rock steady on one rendition has a huge
IQR, so every level would read as unstable. What matters is whether the rate is
*steady across the window*, so the two halves are compared instead. Bursts do
not disturb that — each half averages over its own bursts and idles — while a
player still hunting between renditions does.

**Why 85% is the saturation line.** A configured cap delivers at about −4.5%
(see *Measured accuracy*), so a saturated client sits near 0.955 of its cap. The
remaining gap absorbs the jitter of a mean over a short window. The cost is
that a genuine rung within 15% of the cap reads as saturation — it is found a
level or two later, once the cap has moved further below it.

**What a plateau near zero means.** A rebuffering player consumes its whole cap,
so near-zero is not a starved player: it is a stopped one. The sweep cannot tell
"ended", "paused" and "the app went away" apart, so it stops and says so rather
than recording the silence as a rung.

**How long a window needs to be.** Consecutive 1 Hz throughput samples are not
independent — a single segment fetch spans several of them — so the useful unit
is *segments per window*, not seconds. A 20 s window over 6 s segments is closer
to three independent observations than to twenty, which is why widening it buys
much less than √(seconds) would suggest, and why the default is 45 s (roughly
seven to eleven segments at common segment durations).

**A rung is a WIRE rate, and deliberately so.** It counts bytes as the kernel
counts them — Ethernet, IP and TCP headers included — because that is what a tc
cap limits. The cap needed to sustain a rendition is its wire rate, so measuring
and capping in the same unit is the point. Do not "correct" a rung down to media
bitrate: the pattern engine would only have to convert it straight back.

Against a manifest, expect:

| | overhead |
|---|---|
| Ethernet 14 + IPv4 20 + TCP 32, per 1448-byte payload | **+4.6%** |
| the same over IPv6 (+20 bytes of header) | **+6.0%** |
| TLS 1.3 records (~22 bytes per 16 KB) and HTTP/2 framing | +0.1–0.3% |
| retransmissions | variable; large on a lossy radio |

So `measured ≈ AVERAGE-BANDWIDTH × 1.046` on IPv4. Note the comparison is
against `AVERAGE-BANDWIDTH`, not `BANDWIDTH`: a steady-state mean over several
segments lands near the average, and HLS `BANDWIDTH` is a **peak**. On measured
content the two sat 5–7% apart, which is inside the rung-merge tolerance — so
distinguishing them is not worth the segment-boundary detection it would need.

Note also that `BANDWIDTH` already accounts for the container and, where a
rendition group is declared with the audio codec in `CODECS`, for the audio. The
gap above it is transport framing, not media.

**Confidence.** High for the arithmetic; the units and thresholds above are
exact. Medium for the interpretation — that a steady-state plateau equals the
delivered rendition bitrate holds only once the buffer is full, and rests on the
player fetching one segment of media per segment duration. Startup and buffer
fill both run at line rate and would read high, which is what the settle phase
exists to exclude.

---

## Measured — throttle accuracy across the range

**VERIFIED** on the Pi on 2026-08-30, over 5 GHz Wi-Fi, against a bulk HTTPS
transfer pulled by a laptop associated to the AP. Measured from BOTH ends: the
box's own `tc` class counters, and the bytes the client's application actually
received.

Transfers are sized the way the content is: for each cap, the variant a player
would actually choose, fetched segment by segment on one kept-alive connection.
That matters more than it sounds -- see *the artefact that had to be removed*.

| configured cap | variant fetched | client payload | client ÷ cap | worst gap |
|---|---|---|---|---|
| 0.25 Mbps | 234p | 0.235 | 0.939 | 0.66 s |
| 0.50 Mbps | 360p | 0.474 | 0.949 | 0.44 s |
| 1.00 Mbps | 396p | 0.938 | 0.938 | 0.41 s |
| 2.00 Mbps | 432p | 1.901 | 0.950 | 0.16 s |
| 4.00 Mbps | 594p | 3.756 | 0.939 | 0.31 s |
| unshaped | — | 28.75 | — | — |

The box's own class counters read the configured rate exactly at every level:
0.250, 0.500, 1.048, 2.000, 4.000.

**Repeatability**, four runs per rate with no other client on the radio (each
run's contention was recorded, not assumed):

| cap | mean | sd | spread | runs |
|---|---|---|---|---|
| 0.25 | 0.900 | 0.076 | 0.181 | 0.767, 0.948, 0.942, 0.942 |
| 0.50 | 0.947 | 0.002 | 0.006 | |
| 1.00 | 0.949 | 0.001 | 0.004 | |
| 2.00 | 0.951 | 0.001 | 0.002 | |
| 4.00 | 0.952 | 0.000 | 0.001 | 0.952, 0.951, 0.952, 0.952 |

The figure rises monotonically with rate and the spread shrinks, which is the
signature of a systematic effect rather than noise: the per-request round trip
at each segment boundary costs relatively less as more bytes move between
requests, so the ratio converges on the 0.956 framing limit instead of
wandering.

0.25 Mbps was chased down separately, because one run in four had come in at
0.767 and two explanations fitted equally well: the shaper stumbling at its
lowest rate, or a short window concentrating a rare transport event. Fifteen
further runs at two window lengths:

| window | runs | mean | sd | min | runs with a >1 s stall |
|---|---|---|---|---|---|
| 20 s | 10 | 0.943 | 0.003 | 0.934 | **0** |
| 60 s | 5 | 0.943 | 0.001 | 0.942 | **0** |

Neither explanation survives. The shaper is steady (0.943 throughout), and
window length changes nothing — 20 s and 60 s agree to three decimals — so it
was not short-window fragility either. The outlier did not reproduce and is
recorded as an unattributed transient.

The largest inter-arrival gap was 0.58 to 0.67 s in every one of the fifteen
runs, against 13.3 s when the same rate was measured with an oversized
transfer. That is the difference the traffic shape makes.

An earlier pass reported 0.942 +/- 0.003 at 0.25 and was about to be written up
as "the shaper repeats to a third of a percent". The next pass at the same rate
spread 0.026. Four samples show a spread; they do not pin one.

The client sits a little under the 0.956 the framing arithmetic predicts because
each segment boundary costs a request round trip, and seven to ten segments
across a 30 s window is two to three percent of idle.

**The shaper itself is exact.** The box counters match the configured rate at
every level. An earlier run with no stalls put the client at 0.953–0.957
uniformly across 0.25–4 Mbps, which is the framing arithmetic: a client counts
TCP payload while the cap counts wire bytes, and 66 bytes of header per
1448-byte payload is +4.6%, so payload ÷ cap should read 1/1.046 = 0.956.

**There is no accuracy penalty at low rates.** The concern that opened this —
that sub-1.5 Mbps was unverified and the netem queue floor predicted trouble —
was unfounded as far as *throughput* goes.

### The artefact that had to be removed

The first version of this measurement pulled a **2160p segment -- 19 MB -- at
every cap**, including 0.25 Mbps. It reported multi-second stalls: 13.3 s at
0.25, 2.8 s at 1, 5.5 s at 4 Mbps, and those were written up as bufferbloat from
the queue floor.

They were the harness. No player fetches 19 MB at 0.25 Mbps; it fetches the
234p segment, about 190 KB. The oversized transfer put roughly ten minutes of
data in flight against a 48-second queue and filled it, which is a pathology the
test created rather than found. Sizing each transfer to its rate, the worst gap
across the whole range is **0.66 s** and the stalls do not occur.

The lesson generalises: a conditioner has to be measured under the traffic shape
it exists to condition. A single enormous bulk transfer is a workload this box
will never see, and testing with one produced a confident, wrong conclusion
about the kernel.

### What a client cannot measure

Sub-second delivery smoothness is **not observable from an HTTPS client**. TLS
delivers whole records, so arrival is quantised at one record however the shaper
paced the packets underneath. Measured median read was 16384 bytes at every
rate, and the inter-arrival gaps matched `16 KB ÷ rate` to within a few percent:

| cap | 16 KB ÷ rate | measured p50 gap |
|---|---|---|
| 0.25 | 524 ms | 538 ms |
| 0.50 | 262 ms | 268 ms |
| 2.00 | 65.5 ms | 65.6 ms |
| 4.00 | 32.8 ms | 32.5 ms |

So any "burstiness" figure taken from a client at bin widths below one record
period is measuring TLS framing, not the shaper.

Past that quantisation the pacing is even. At 1-second bins the 99th-percentile
bin carried 0.99x to 1.45x the nominal rate across 0.25-4 Mbps; at 200 ms,
1.31x to 2.97x, improving as the rate rises and the record period shrinks
relative to the bin. The shaper paces well at every timescale a TLS client can
observe.

Measuring the shaper's own pacing requires a packet capture at the egress
interface, before the radio.
