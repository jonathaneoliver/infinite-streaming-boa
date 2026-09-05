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

Retained for reference only. In Bridged AP mode — a transparent bridge — boa
runs no DHCP server,
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

## Source H — DHCP requests · MAC→name

**VERIFIED** on hardware on 2026-09-01, on the device that motivated it: an
Apple Watch joined, took a lease, and named itself. A `UDP` socket bound to
port 67 on the bridge. A client's request is broadcast to 255.255.255.255, so a
transparent bridge sees it without any capture machinery, and nothing is
queried or injected.

Complements Source G rather than replacing it. mDNS is **opt-in advertising**:
a device announces because it has a service to offer, so anything that offers
none — a watch, a plug, a camera, a printer with sharing disabled — stays a
bare MAC forever. DHCP is not opt-in: anything that wants an address asks, and
almost everything puts its own name in option 12.

| Field | Meaning |
|---|---|
| `op` | 1 is a client request. **2 is a server reply and is ignored** — a reply's option 12 is the SERVER's idea of the client's name, not the device's own |
| `chaddr` | Client hardware address. **The key.** |
| Option 12 | The host name the client claims for itself |

**Semantics that bite**

- **Only a lease being NEGOTIATED is seen.** A renewal is unicast to the
  server, not broadcast, so the bridge never sees it. A device that got its
  address before the box started stays anonymous until it rejoins. This is the
  single biggest limitation and it is not fixable from a UDP socket.
- The name is **client-supplied**, arbitrary bytes, and not unique. It labels a
  client and nothing more; shaping is attached by the port traffic actually
  arrives on, which no packet can assert about itself.
- Values are sanitised before display: NUL padding truncates, control
  characters are dropped, trailing dots and spaces are trimmed, and the whole
  thing is capped at 63 characters. A device chooses these bytes and the
  interface has to render them.
- Binding port 67 is safe **only because this box runs no DHCP server** — an
  explicit non-goal in `PRD.md`. If one is ever added, the bind fails, the
  daemon logs it once, and every other source of names carries on.
## Source I — sysfs · which radio is serving, and at what bus speed

**VERIFIED** on hardware on 2026-09-01, against the same adapter on a
High-Speed and a SuperSpeed port. Read from `/sys/class/net/<iface>/device`,
not from `lsusb`, which a minimal image does not have.

| Path | Meaning |
|---|---|
| `device/driver` (symlink) | Driver name, e.g. `mt7921u`, `brcmfmac` |
| `device/../speed` | **Negotiated** link speed in Mbit/s: 5000 SuperSpeed, 480 High-Speed. Absent for a non-USB radio |
| `device/../version` | `bcdUSB` as the device declares it, e.g. `3.20` |
| `device/../product`, `manufacturer` | USB descriptor strings, so the interface can name the adapter |

**Semantics that bite**

- **`speed` is what was NEGOTIATED, not what the device is capable of**, and
  that distinction is the entire reason this exists. A USB 3.0 adapter that is
  not fully seated, or on a cable without SuperSpeed pins, enumerates as
  High-Speed and is then indistinguishable from a working one by every other
  measure: same 80MHz channel, same 802.11ax, same PHY rate over 1 Gbit/s, no
  error logged anywhere. Measured on one adapter minutes apart: 717 Mbit/s on
  USB 3.0 against 117 on USB 2.0.
- The absence of `speed` means **not a USB device**, which for the onboard
  radio is the normal case and not a failure to read. It hangs off SDIO.
- `device` is a **symlink**, and the USB device is its parent. Resolve it
  before taking `..`: a lexical join yields `/sys/class/net/<iface>` instead,
  every read returns empty, and a USB adapter reports as onboard.

## Source J — sysfs + `ip -j addr` · the box's own interfaces

**VERIFIED** on hardware on 2026-09-03. Source I answers "which radio is
serving"; this answers "what interfaces does this box have at all", which is
what the bridge view draws. Read per interface from `/sys/class/net/<iface>/`,
with addresses from `ip -j addr show`.

| Path | Meaning |
|---|---|
| `address` | The interface's MAC |
| `operstate` | `up`, `down`, or `unknown` (`lo` is always `unknown`) |
| `carrier` | 1 = link present. **EINVAL on a down interface** — see below |
| `speed` | Negotiated Mbit/s for a **wired** port. EINVAL when down; absent for wireless |
| `master` (symlink) | The bridge this interface is a port of, e.g. `br-lan`. Absent when it is not bridged |
| `phy80211` (symlink) | Present **iff** the interface is wireless. This is the test for "is this a radio" |

**Semantics that bite**

- **`carrier` and `speed` on a down interface fail with `EINVAL`**, they do not
  read empty and they do not read `-1`. Measured: `cat
  /sys/class/net/wlan0/carrier` prints "Invalid argument" and exits 1 while
  `wlan0` is rfkilled. `readSysfs` returns `""` on any error, so the value
  degrades safely — but *unknown* and *no carrier* are different facts and
  collapsing the read error into `false` reports a working interface as dead.
- **A bridge reports a `speed` and a radio does not.** `br-lan` reads `1000`;
  `wlan-usb` has no `speed` file at all. Speed is meaningful only for a wired
  port, and showing "1000 Mbit/s" against a bridge spanning an 80 MHz radio
  invents a number that describes nothing.
- **`master` is a symlink, so bridge membership needs no `bridge link show`.**
  One fewer subprocess, and unlike the command it still answers for an
  interface that is down.
- **Two interfaces sharing a MAC is normal, and which two depends on the
  image.** Measured on this box: `br-lan` and `wlan-usb` both read
  `9c:ef:d5:f6:3f:f2`, because a bridge with no explicitly-set address takes
  the **lowest** MAC among its members and `9c:ef…` sorts below `eth0`'s
  `d8:3a…`. After commit 9d0d5a3 the bridge MAC is instead pinned to the WAN
  port's, so `br-lan` and `eth0` share it. Either way a MAC appears twice and
  neither case is a fault; rendering it as a duplicate-address warning would
  make correct behaviour look broken.
- `lo` reads an all-zero MAC and `operstate=unknown`. It is not a bridge port
  and is not worth drawing.
- **`br-lan` carries several addresses at once** — the DHCP lease from upstream,
  the fixed rescue address, and an IPv6 link-local — which is why the interface
  cannot pick "the" address of the box. See the note in `App.vue`'s `iperfCmd`:
  whichever address the page was reached on is the one demonstrably reachable.

## Source K — hostapd `STATUS` / `GET_CONFIG` · the live AP settings

**VERIFIED** on hardware on 2026-09-03 against `wlan-usb` (mt7921u), hostapd
v2.10, over the control socket described in `daemon/internal/boa/hostapd.go`.
This is the **only** runtime source for these: SSID, channel, band and width are
baked into `/etc/hostapd/boa.conf` at *image build* time from `.env`, so nothing
in the daemon knows them otherwise.

| Command | Field | Meaning |
|---|---|---|
| `STATUS` | `state` | `ENABLED` when the AP is actually beaconing |
| `STATUS` | `freq`, `channel` | Operating frequency in MHz and its channel number |
| `STATUS` | `secondary_channel` | `0` = none, `-1` = below the primary, `1` = above |
| `STATUS` | `vht_oper_chwidth`, `he_oper_chwidth` | `0` = 20/40 MHz, `1` = 80 MHz |
| `STATUS` | `ieee80211n`, `ieee80211ac`, `ieee80211ax` | Which generations are enabled |
| `STATUS` | `beacon_int`, `dtim_period` | Power-save timing, in beacon intervals |
| `GET_CONFIG` | `ssid[0]`, `bssid[0]` | The BSS's network name and its MAC |
| `GET_CONFIG` | `wpa`, `key_mgmt`, `group_cipher` | Security, e.g. `2` / `WPA-PSK` / `CCMP` |

**Semantics that bite**

- **Neither command reports the country code.** There is no `country_code` field
  in either output, despite `country_code=` being set in `boa.conf`. The
  regulatory domain must come from `iw reg get`, which is **global rather than
  per-interface** — measured here as `country US: DFS-FCC`. Attributing it to a
  radio is a display choice, not a fact the radio reports.
- **There is no channel-width field.** Width is *derived*, and the derivation is
  the whole reason this entry exists:
  `he_oper_chwidth`/`vht_oper_chwidth` of `1` → **80 MHz**; chwidth `0` with a
  non-zero `secondary_channel` → **40 MHz**; both zero → **20 MHz**. The string
  "80" appears nowhere in the output. Measured on this box: `freq=5200`,
  `channel=40`, `secondary_channel=-1`, `he_oper_chwidth=1`, which `iw dev
  wlan-usb info` independently confirms as "channel 40 (5200 MHz), width: 80
  MHz".
- **`ssid` and `bssid` are indexed** — `ssid[0]`, `bssid[0]` — because hostapd
  can serve several BSSes on one interface. Match the prefix, not a bare key, or
  the parse silently finds nothing.
- `he_oper_centr_freq_seg0_idx=42` is the **centre** of the 80 MHz block, not a
  channel the AP is on. Reading it as the operating channel yields a plausible
  number that is wrong by up to two channels.
- Available **only for a radio hostapd is serving**, gated by
  `hostapdAvailable(iface)`. Before #147 the onboard radio ran under
  NetworkManager and exposed no socket at all; after it, whichever radio wins
  is a hostapd radio — but still only one at a time.

## Source L — `iw dev <if> survey dump` · airtime, on the operating channel only

**VERIFIED** on hardware on 2026-09-03, mt7921u serving on channel 40.

| Field | Meaning |
|---|---|
| `frequency` | The channel this block describes, MHz |
| `channel active time` | Total ms the radio has spent on this channel |
| `channel busy time` | Of that, ms the medium was sensed busy |
| `channel receive time` / `channel transmit time` | Of that, ms actually spent receiving / transmitting |

**Semantics that bite**

- **This is not a band survey, however much it looks like one.** The command
  prints a block for *every* channel the phy knows — 98 of them on this
  tri-band adapter, from 2412 MHz to the 6 GHz range — but measured on this box
  **exactly one** block has a non-zero `channel active time`. A beaconing radio
  never visits the others, so there is nothing for it to have measured.
- **The frequency label on that one block is WRONG, and this is the trap.**
  Measured 2026-09-03: the AP was on **5200 MHz** (channel 40 — `iw dev
  wlan-usb info` and hostapd `STATUS` agree), yet the only populated survey
  block was labelled **5955 MHz**, a 6 GHz channel the radio has never been on.
  `iw` maps the driver's survey *index* onto its own channel enumeration, and
  on `mt7921u` those do not line up.

  The numbers themselves are sound — they are the operating channel's airtime.
  Proof: `channel active time` read 5 540 872 ms while the hostapd unit had
  been up 5 562 s, a 21-second match, and the derived figures (4.9% busy, 4.0%
  transmit, 1.4% receive) are plausible for a lightly-loaded AP with two
  stations.

  So: **take the airtime from the single populated block, and take the
  frequency from hostapd `STATUS` or `iw dev <if> info` — never from the survey
  block's own label.** Believing the label reports the box measuring a 6 GHz
  channel it has never tuned to, which is both wrong and completely plausible
  on a tri-band adapter.
- Therefore: **filter to blocks with a non-zero `channel active time`.** A
  "least busy channel" ranked over the raw output sorts a table of zeroes to
  the top and recommends whichever channel the radio has never looked at —
  confidently, and always wrong.
- The counters are **monotonic since the interface came up**, so only
  *differences between two reads* mean anything. A single sample gives a busy
  fraction averaged over the whole uptime of the AP, which is not the question
  anyone is asking.
- **`receive` and `transmit` are not a decomposition of `busy`.** They overlap
  it and can exceed it. Measured on the same sample: busy 271 397 ms against
  receive 79 009 + transmit 219 107 = 298 116 ms.

  So **airtime consumed by other devices cannot be derived as
  `busy - receive - transmit`.** That subtraction is the obvious way to build a
  "how much of this channel is somebody else" readout, and on real hardware it
  produces a negative number. There is no honest contention figure available
  from these four counters alone; `busy` is the whole of what can be said.
- A genuine cross-channel survey needs a radio that is **not** beaconing —
  a second adapter, or the onboard one while the dongle serves. That is why
  scanning for a best channel is not implemented against the serving radio.
- **The onboard `brcmfmac` radio reports no survey data at all.** Measured
  2026-09-03 with it serving a 2.4 GHz AP: `iw dev wlan0 survey dump` prints
  **nothing whatsoever** — no blocks, no error, exit status 0. It is the same
  missing capability that makes ACS impossible there.

  Empty output and a genuinely idle channel are indistinguishable in the
  numbers, so the daemon reports the absence explicitly rather than rendering a
  blank panel under a heading about airtime. This is also why the "idle radio
  measures the air" idea only works when the **USB** adapter is the idle one:
  the onboard chip can neither survey nor go into monitor mode.

## Source M — hostapd control actions · what the radio will actually do

**VERIFIED** on hardware on 2026-09-03: `mt7921u`, hostapd v2.10, AP on channel
40 with two associated stations. These are *actions* rather than readings, but
they belong here for the same reason the rest does — two of the three behave
differently from how they read.

| Command | Result on this hardware |
|---|---|
| `DEAUTHENTICATE ff:ff:ff:ff:ff:ff` | **Works.** Both stations dropped and reassociated within 12 s (connected time 6438 s → 9 s) |
| `CHAN_SWITCH …` | **Refused.** Every form returns `FAIL`; the AP does not move |
| `STATUS` / `GET_CONFIG` | Work — see Source K |

**Semantics that bite**

- **`CHAN_SWITCH` fails on this driver, and everything short of running it says
  otherwise.** `iw phy phy1 info` lists `channel_switch` among its supported
  commands, and hostapd parses the request and logs it
  (`wlan-usb: IEEE 802.11 CHAN_SWITCH HE config 0x1 VHT config 0x1`) before
  returning `FAIL`. Tried at 20/40/80 MHz, with and without `ht`/`vht`/`he`, and
  as a bare `CHAN_SWITCH 5 5180` — all refused, AP unmoved.

  **Advertised capability is not evidence of support.** The only test that
  answers this question is issuing the command and reading the reply, which is
  why the refusal is surfaced as a `502` carrying hostapd's own text rather than
  being reported as success.

- **The two radios have OPPOSITE scanning capabilities**, so neither order of
  operations suits both. Measured 2026-09-03 with both serving:

  | | `iw dev <if> scan` while beaconing | with the BSS `DISABLE`d |
  |---|---|---|
  | `brcmfmac` (onboard) | **works**, 4 s, 23 APs | fails, `Network is down (-100)` |
  | `mt7921u` (USB) | fails, `Operation not supported (-95)` | **works**, 3 s |

  `DISABLE` takes the interface down, which is why the onboard radio then
  cannot scan; the adapter needs `ip link set <if> up` after the disable before
  it will. The daemon therefore tries the free path first and falls back, so a
  scan costs nothing on one radio and an outage on the other — and says which.

- **`iw scan` prints the frequency as a FLOAT.** `freq: 2417.0`, where
  `survey dump` prints `frequency: 2412 MHz` as an integer. Parsing it with a
  plain integer conversion yields **0**, and since 0 is below 3 GHz every access
  point then looks like a 2.4 GHz one — so a band filter silently passes
  everything. It stayed invisible because the channel arrives separately on the
  `DS Parameter set` line.

- **`SET secondary_channel` is refused by hostapd**; it is derived state,
  reported in `STATUS` and not settable. Of the parameters used to move a
  channel it is the only one rejected. The 40 MHz offset is set through
  `ht_capab` (`[HT40+]` / `[HT40-]`) instead, and **it must agree with the
  channel**: setting `[HT40-]` and then channel 36, whose secondary sits above
  it, is accepted one command at a time and then fails the whole `ENABLE`,
  leaving the access point down.

- **A refused `SET` mid-sequence does not undo the ones before it.** Measured:
  a channel move reported "came back on 6" while the radio was demonstrably on
  11 — `SET channel` had landed before a later command was refused. The channel
  is now read back from `STATUS` after `ENABLE` rather than inferred from
  whether every command succeeded.

- **`iw phy set frag off` is not portable.** It fails on `brcmfmac` with
  `Invalid exchange (-52)` and leaves the threshold where it was, while
  succeeding on `mt7921u`. The numeric IEEE disabled values — 2346 for
  fragmentation, 2347 for RTS — are accepted by both. Without that, an
  impairment could be switched on and never off. `iw phy info` reports the
  fragmentation threshold but **never** the RTS threshold, so RTS cannot be
  verified by readback on either driver.

- **A broadcast deauthentication can strand a device's policy.** Observed on the
  same run: of two stations kicked, one reassociated under a **different MAC**
  (`fc:9c:a7:93:7f:ed` → `92:d2:2b:bd:91:b2`) nine seconds later. That is
  iOS/Android private-address randomisation rotating on reassociation.

  Policy is keyed by MAC, so the returning device is a *new* client with no
  conditioning, and the old policy stays behind on an address that will never be
  seen again. This is issue #45, and this action makes that path hot rather than
  incidental — so the interface says so where the button is, not in a document.

## Source N — the activity log · what changed, as opposed to what is

Not a measurement. Every other source here answers "what is true now"; this one
answers "what just happened", and it is **derived entirely from the sources
above** rather than read from anywhere new.

| Field | Where it comes from |
|---|---|
| `join` / `leave` / `roam` | The per-radio `iw dev <if> station dump` of Source A, diffed against the previous tick |
| `radio` | `hostapd STATUS` (Source K) — channel, width and mode, diffed; plus every action this daemon takes to a radio |
| `action` | Raised at the point the action succeeds, never before |
| `warning` | A refusal that left the radio somewhere other than where it was asked to be |

**Semantics that bite**

- **A roam is invisible in state.** A client that moved from 5GHz to 2.4GHz is
  simply *on* 2.4GHz; nothing anywhere records that it moved. The diff is the
  only place that fact exists, which is why the log is derived at the tick and
  not reconstructed by a reader.

- **A radio change is detected at the cache's resolution, not the tick's.**
  `RadioOn` is cached for 15 s (asking hostapd per tick is a round-trip per
  radio for a value that almost never changes), so a channel change made
  *outside* this daemon is noticed within 15 s, not within 1 s. Anything done
  through the interface records itself immediately and re-syncs the cache, so it
  is never reported twice.

- **`join` on a fresh start is not a join.** The first tick has no previous
  association map, so every client currently associated is recorded as having
  joined at that moment. That is a restart artefact, not an event — the daemon's
  own start time is the giveaway.

- **In memory, and deliberately lossy.** A ring of 500, cleared by a restart or
  a deploy. Persisting an association event per client per roam is the kind of
  steady write that wears an SD card out, and the box is a bench instrument that
  is watched while it runs.

- **A silent poll failure looks exactly like a quiet box.** The interface shows
  the fetch error inside the log rather than beside it, because "nothing has
  happened" and "I stopped being able to ask" are the two readings this panel
  must never confuse.

## Source M addendum — what a radio power cycle actually costs

**MEASURED on hardware 2026-09-03**, several cycles per radio, because the first
three attempts to make the switch-on "fast" were all fixes to the wrong thing.

| | rfkill block | back on the air |
|---|---|---|
| `wlan0` (brcmfmac, 2.4GHz 20MHz) | ~0.9 s | **~0 s** |
| `wlan-usb` (mt7921u, 5GHz 80MHz) | ~0.03 s | **~25 s** |

**Semantics that bite**

- **hostapd handles rfkill itself.** Its own log shows the unblock and the
  recovery in the same second — `rfkill: WLAN unblocked` then
  `wlan0: INTERFACE-ENABLED`. Nothing needs to send `ENABLE` after a power-on.

- **Sending `ENABLE` anyway makes it worse.** Aimed at a BSS hostapd had already
  restored, it took an mt7921u switch-on from about a second to 25, apparently
  tearing the interface down to rebuild it.

- **`ENABLE` acknowledges only when the BSS is up**, which on mt7921u at 80MHz
  outlasts the control socket's 2 s deadline. A perfectly healthy recovery
  therefore reports `i/o timeout`. Treating that as failure and retrying turned
  a 4.5 s operation into a 27.8 s one — six attempts each paying the same
  deadline for a reply that was never going to arrive in time.

- **The control socket itself goes unresponsive for 10–25 s** after an mt7921u
  unblock, while the driver re-initialises. One `STATUS` took 14 seconds to
  answer. So a client-side wait is waiting on the socket, not on the radio, and
  no amount of polling makes it faster. During it the whole box is loaded enough
  that SSH banner exchange can time out.

- **Therefore the power endpoint returns as soon as power is restored**, which
  is what a mains switch controls, and a background watch confirms the access
  point re-formed. The duration is recorded as an event
  (`wlan-usb access point back after 25s`), because it is the number that
  answers "why did my client take so long to come back" and it differs by an
  order of magnitude between the two radios on this box.

## Source M addendum 2 — why a channel move lands somewhere else

**MEASURED on hardware 2026-09-03.** Asking for channel 36 at 80 MHz returned
"now on channel 40", with every `SET` having answered `OK`. The readback was
right; the explanation was missing.

hostapd's own log says what happened:

```
wlan-usb: interface state COUNTRY_UPDATE->HT_SCAN
Switch own primary and secondary channel to get secondary channel
  with no Beacons from other BSSes
wlan-usb: interface state HT_SCAN->ENABLED
```

That is the 802.11 **20/40 MHz coexistence scan**. Before enabling a 40 or
80 MHz BSS, hostapd scans for neighbours on the channel it intends to use as the
*secondary*, and if it finds any it swaps primary and secondary rather than
interfering with them.

**Semantics that bite**

- **The configured channel is not the operating channel.**
  `/etc/hostapd/boa-usb.conf` says `channel=36` with `ht_capab=[HT40+]` — primary
  36, secondary 40 — and the radio has served on **40** since the image was
  built. A clean `systemctl restart` of the hostapd unit reproduces it every
  time. Nothing is broken.

- **Only one of each adjacent pair is reachable at 40/80 MHz.** Measured from a
  radio running 40 with `secondary_channel=-1`: asking for 48 (same offset)
  lands on 48; asking for 44 or 36 (opposite offset) lands back on 40. Which one
  is reachable depends on what the scan hears, so it can change.

- **The offset cannot be forced through the control socket.**
  `SET ht_capab [HT40+]` is accepted and changes nothing — `secondary_channel`
  is derived during the scan, not from that string — and `SET secondary_channel`
  is refused outright as derived state. A 20 MHz intermediate step does not help
  either: the swap happens again when the width is restored.

- **At 20 MHz every channel is exact**, because there is no secondary channel
  and therefore no coexistence scan. Verified: 36 at 20 MHz lands on 36.

- **So a mismatch is reported even when nothing was refused.** The check used to
  require a refused `SET`, which made the most common outcome on this box the
  silent one.

`noscan=1` in the hostapd config would skip the scan and honour the configured
channel. It is deliberately not set: the scan exists to avoid clobbering
neighbouring networks, and turning it off is a decision about someone else's
airtime, not a bug fix.

## Source O — `iw dev <if> scan` · neighbours, their width, and measured airtime

**VERIFIED** on hardware 2026-09-04, both radios, 13 BSSes in one 1561-line dump.

| Field | Meaning | Confidence |
|---|---|---|
| `BSS <mac>(on <if>)` | Opens a block. **The only line that does** | certain |
| `freq:` | Frequency, MHz, **as a float** (`2417.0`) | certain |
| `signal:` | Received signal, dBm, negative | certain |
| `SSID:` | Network name; may be empty for a hidden BSS | certain |
| `DS Parameter set: channel` | Primary channel; authoritative where present | certain |
| `* station count:` | Clients associated **to that BSS** (BSS Load) | certain |
| `* channel utilisation:` | **`N/255`** — fraction of time the medium was sensed busy, as observed by that AP (BSS Load) | certain |
| `* channel width:` | VHT operation **enum**: `0`=20/40, `1`=80, `2`=160, `3`=80+80 | certain |
| `* center freq segment 1:` | Channel index at the centre of an 80/160MHz block (42, 155, …) | certain |
| `* secondary channel offset:` | HT: `above` / `below` / `no secondary` — settles 20 vs 40 | certain |

**Semantics that bite**

- **`channel utilisation` is out of 255, not out of 100.** `60/255` is **23.5%**,
  not 60%. Stored raw as `UtilRaw` and converted once, at the point of display,
  precisely because a percentage stored here would be a plausible-looking wrong
  answer for ever. Measured values on this box ranged 22–94 out of 255, i.e.
  8.6%–37%, which read as sensible percentages either way — the units error
  would not have announced itself.
- **Absent BSS Load is NOT zero utilisation.** 10 of 13 neighbours advertised it;
  3 did not. A channel where nobody advertised it must fall back to a headcount,
  because reading the absence as 0% paints the busiest channel green — the exact
  inversion the field was added to fix. Hence `UtilKnown` per AP and `UtilFrom`
  per channel: "nothing measured this" and "this measured zero" are different
  facts and must not share a representation.
- **`BSS Load:` is an element, not a header.** It begins with `BSS ` like the
  block header does. Treating it as a header produced phantom access points
  whose BSSID was the literal `load:` **and** — far worse — ended the real block
  half way through, so every field after it, the BSS Load values included, was
  parsed with no current AP and silently dropped. A `BSS ` line opens a block
  only when its second field is a MAC.
- **`* STA channel width:` is not `* channel width:`.** The first is an HT
  *capability* describing what the AP will accept from a station; the second is
  the VHT *operation* width the BSS is actually running. Measured: an AP running
  80MHz advertised `STA channel width: 20 MHz`. Matching the wrong one reports
  20MHz for an access point that is on 80.
- **`channel width` is an enum, and `surveyValue` returns the first field only.**
  `channel width: 1 (80 MHz)` yields `1`. Reading the bracketed `80` would need
  the whole line, and would have no answer for `3` (`80+80 MHz`), which has no
  single width.
- **Utilisation describes the CHANNEL, not the BSS.** Several APs on one channel
  broadly agree; where they disagree, the higher reading heard something real.
  Aggregated as the maximum, never the mean — an average lets one quiet observer
  hide a busy medium.
- **Headcount does not predict congestion.** Measured on this box, in one scan:

  | BSSID | station count | channel utilisation |
  |---|---|---|
  | `60:32:b1:45:ec:3f` | 0 | 94/255 (**37%**) |
  | `48:22:54:4e:90:76` | 10 | 22/255 (**8.6%**) |

  The access point with **no clients** sits in air four times busier than the one
  with ten. Any rating built on AP or client counts ranks those the wrong way
  round, which is why the measurement is preferred wherever it exists.
- **A neighbour occupies more than its primary channel.** Measured: 7 of 13 APs
  ran 80MHz, six of them centred on channel 42 — covering all of 36/40/44/48.
  Channel 36 therefore showed **0 access points primary on it and 6 covering
  it**. Counted from the CENTRE (`centre ± 2, ± 6` for 80MHz; `± 2, 6, 10, 14`
  for 160), never from the primary: which of the four a neighbour beacons on
  says nothing about the block it fills.
- **2.4GHz is deliberately excluded from width-derived coverage.** That band is
  already counted with a ±4-channel overlap window, and layering a second
  coverage set on top would count the same interference twice.

## Source P — hostapd `BSS-TM-RESP` · what a client said about a steer

**What it is.** A steer sends an 802.11v BSS Transition Management request. The
client answers with a Response frame carrying a status code, and hostapd
forwards that to anything ATTACHed to its control socket:

```
BSS-TM-RESP <mac> status_code=<n> bss_termination_delay=<n> [target_bssid=<mac>]
```

Read by a monitor connection per radio (`hostapdmonitor.go`). Every other use of
the control socket in this codebase is request/reply, which cannot see a message
hostapd sends unasked — so before this, the interface could report only that a
request had been **sent**.

**Units and meaning.** `status_code` is an integer from IEEE 802.11 Table 9-428.
The activity log renders it in words, because a bare number in a log is one
nobody looks up. The mapping, so the phrase can always be traced back to the
code it came from:

| code | rendered as |
|---|---|
| 0 | accepted |
| 1 | refused, giving no reason |
| 2 | refused: it had no recent measurement of the other radio |
| 3 | refused: the other radio has no capacity for it |
| 4 | refused: it does not want this BSS to go away |
| 5 | refused, asking for more time before this BSS goes away |
| 6 | refused, offering its own list of candidates instead |
| 7 | refused: none of the candidates offered suit it |
| 8 | refused: it is leaving this network entirely |

`bss_termination_delay` is in **TBTTs**, not seconds, and is not currently
displayed. `target_bssid` appears only on an accept.

**Confidence: high for what it says, and it does not say much.** The code is the
client's own account of its decision and is exactly as trustworthy as the
client. It describes an intention, not an outcome: a client may answer `0` and
not move, or move without answering at all.

**The edge case that matters, MEASURED 2026-09-04.** An iPhone asked to leave
`wlan-usb` **moved one second later and sent no response frame at all.** Asked
in the other direction from `wlan0` minutes later, it neither answered nor
moved. So:

- **Moving and answering are independent**, and the log reports them as two
  facts. A client that moves in silence must not be described as unresponsive —
  it plainly acted.
- **Silence is reported, not left as an absence** (after 5s). A reader who sees
  "asked to move" and then nothing cannot otherwise tell a refusal from a
  request that went nowhere.

**A caveat on comparing the two directions.** They differ in more than the
capability advertisement: `wlan0` is 2.4GHz/20MHz/802.11n and `wlan-usb` is
5GHz/80MHz/802.11ax, so a client weighing the target's band is a competing
explanation for any difference between them. Neither leg alone supports a
conclusion about advertisement.

## Source Q — `iw ... set txpower` · a control that validates and then ignores

**VERIFIED** on hardware 2026-09-04, mt7921u on channel 149 at 80MHz, with the
measuring client a few inches from the box.

| Field | Meaning | Confidence |
|---|---|---|
| `iw dev <if> info` → `txpower` | **Not usable.** Reports `3.00 dBm` regardless | certain |
| `iw ... set txpower fixed <mBm>` | Accepted, and has no effect | certain |
| `iw phy <phy> info` → per-frequency dBm | The regulatory ceiling, e.g. `30.0 dBm` on 5745 | certain |
| Client-side RSSI | The only instrument that answers the question | certain |

**The measurement**

Anchored at both ends and returned to the start, so drift cannot be mistaken
for an effect:

| Setting | client RSSI | 8s downlink |
|---|---|---|
| `fixed 3000` (30 dBm, the phy's own ceiling) | −22 dBm | 612 Mbit/s |
| `fixed 0` (0 dBm) | −22 dBm | 604 Mbit/s |
| `fixed 3000` again | −22 dBm | 587 Mbit/s |

Also covered: a descending sweep `auto → 1500 → 1000 → 500 → 100 → 0` mBm
(RSSI −24 to −25 throughout), and the **phy path** `iw phy phy2 set txpower`
(no change either) — hostapd owns the interface, so that is a genuinely
different route and is the first thing a reader will ask about.

**Semantics that bite**

- **The units are mBm, not dBm.** `fixed 100` is 1 dBm, not 100. A table
  labelled in dBm must say so, because the argument and the label differ by
  100x and nothing in the output reveals which was meant.
- **It validates and then ignores.** Negative values are refused outright —
  `rc=161`, `Operation not supported` — while every legal value returns `rc=0`.
  So something in the path parses the argument and enforces a bound before
  discarding it. That is a sharper statement than "accepted and ignored": it
  rules out the reading that the call is being dropped somewhere generic, and it
  establishes 0 mBm as a **real** floor rather than an assumed one, which is
  what makes "the entire legal range produces no change" a closed claim.
- **The adapter's own `txpower` field cannot answer this question**, so it must
  not be the instrument. It reads `3.00 dBm` before, during and after. This is a
  known upstream driver bug with patches in flight for `mt7921`/`mt7925`; the
  *control* being inert is a separate finding and is measured here rather than
  inferred from those reports, which are about the readout.
- **Rate is not evidence, and it will look like it is.** Across one sweep the
  AP's tx rate to the client read 960.7 → 960.7 → 286.7 → 1200.9 as power was
  lowered — the *lowest* setting producing the *highest* rate. That is ordinary
  rate-control noise, uncorrelated with the setting, and quoting it as an effect
  is the mistake this note exists to prevent. Sample RSSI alongside, repeatedly.
- **A dropped connection was never the available outcome, at this range.** A few
  inches is roughly 28 dB of free-space loss, so even a working control at its
  floor leaves about −54 dBm at the client — a comfortable signal. Roughly 66 dB
  of attenuation would be needed to break the link and only 30 dB exists. The
  test is therefore "does RSSI move by 30 dB", not "does the client drop", and a
  test that can only fail in one direction should say so before it is run.
- **Check the shaper before trusting a throughput number.** An earlier run of
  this test read 7.02 Mbit/s at every power level and looked like a flat result;
  the client had a 7.4 Mbps cap left on it from unrelated slider testing, so the
  figure was the cap and not the link. A restored display is not a restored
  value — confirm against `/api/state`, not the interface.

**Conclusion for the product:** transmit power cannot be set from this box, so
attenuation is not an impairment boa can offer. Weak-signal testing means
physical distance or obstruction. The per-client controls condition the link
above the radio; `legacy`, `narrow` and `dozy` impose real MAC-layer cost; none
of them changes how loudly the radio talks.

---

## Source R — `Sample.Iface` / `Sample.Channel` · which radio carried each moment

**What it is.** Two fields added to the history sample so a throughput chart can
say *where* the traffic was, not just how much of it there was. Written in
`state.go`'s tick from the client's current `Port` and the channel of whichever
radio is on for that port, and stored alongside the byte counters in
`history.go`.

```go
Iface   string `json:"iface,omitempty"`
Channel int    `json:"channel,omitempty"`
```

**Units and meaning.** `Iface` is a kernel interface name — `wlan0`, `wlan-usb`,
`lan0` — and is the empty string when the client was **not present** in that
second. `Channel` is an 802.11 channel *number*, not a frequency and not an
operating-class centre index: it is whatever `radioOnFor(port).Channel` reports,
which comes from Source K. Zero means unknown, which for a wired port is the
normal case rather than a fault.

**How a bucket carries them.** Every other field in a downsampled bucket is a
mean or a max. These two are **first-in-bucket**, taken when `n == 0` and never
touched again:

```go
if n == 0 {
    ifaceFirst, chanFirst = sm.Iface, sm.Channel
}
```

There is no mean of two interface names, and the alternatives are both worse
than picking one: last-in-bucket makes a roam appear to have happened at the end
of a window it happened at the start of, and most-common silently deletes a
short visit to the other radio — exactly the event the field exists to show. At
1h range a bucket is tens of seconds, so a roam is placed within a bucket-width
of the truth and the interface does not claim better.

**Edge cases, and what they look like on screen.**

- **A gap is not a roam.** `Iface == ""` draws as an absence in the ON ADAPTER
  strip, not as a segment. A client that was off the network for a minute must
  not appear to have been on some third adapter.
- **A channel change inside one run** is a break drawn *within* the segment, not
  a new segment. The client did not move; the radio did, and those are different
  events with different causes.
- **The strip's x-axis is the chart's x-axis, and this was measured rather than
  arranged.** `OnAdapterStrip` repeats `TrafficChart`'s padding exactly
  (`PAD_L = 40`, `PAD_R = 68`) and the two were compared in the browser: track
  71→660 against plot 71→660. A strip a few pixels out would misattribute the
  moment of a roam, which is the one thing it is for.

**Confidence.** High for `Iface` — it is the same station-table membership the
device list is built from (Source C), read in the same tick. Medium for
`Channel`: it is correct at the moment of sampling, but the daemon learns a
channel change from Source K on its own cadence, so a break in the strip is
accurate to within one poll rather than to the beacon that carried the change.

**What it is not.** Not a roam *log*. The activity log (Source N) records the
roam as an event with a time; this field records the state each second and lets
a roam be *seen against the traffic*. They disagree by up to a poll interval and
neither is wrong — one is an event, the other a sampled state.

---

## Source S — the distance model · what a weaker signal would DO

**TYPED, not measured.** This is the one entry here that is not a reading. Every
other source describes something the box observed; this one describes something
it computed, and the distinction is the whole point of recording it.

**Why it exists.** The most useful Wi-Fi test for a player is a walk away from
the router and back, and this box cannot perform one. Transmit power is not
settable (#122, #202: `iw ... set txpower` validates and is then ignored) and
neither is the rate set — measured 2026-09-05, `iw dev <if> set bitrates`
returns `Operation not supported (-95)` on **both** radios, because
`mt76/mt792x_core.c:834` sets `HAS_RATE_CONTROL` and `mt7921_ops` implements no
`.set_bitrate_mask`. The radio cannot be made to look further away, so the model
in `daemon/internal/boa/distance.go` computes what being further away would do
and applies it with the impairments that already exist.

### Input

| Field | Meaning | Confidence |
|---|---|---|
| `Policy.Rssi.Dbm` | The modelled received level. **Stored**; the operator's intent | certain — it is a setting |
| `Policy.Rssi.N` | Path-loss exponent, for rendering metres. 2.2 open / 3.0 home / 3.8 through walls | low — a per-building guess |
| `Client.RadioOn.Channel` | Which band the client is on, so the right curve is used | high — Source K |
| `Client.RadioOn.WidthMHz` | Channel width, which sets the sensitivity floor | high — Source K |

### The arithmetic

- **Free-space loss at 1 m** is `20*log10(f_MHz) - 27.55`: **47.6 dB at 5745
  MHz** against **40.3 dB at 2462 MHz**. That 7.3 dB is why 5 GHz has shorter
  range at equal power, and why the two bands need separate curves rather than
  one curve with an offset.
- **Log-distance path loss**: `RSSI(d) = Ptx - FSPL(1m) - 10*n*log10(d)`. At
  n = 3 every **doubling** of distance costs about 9 dB, which is why any
  distance control has to be logarithmic — 1→2 m costs what 8→16 m costs.
- **Ptx is assumed at 20 dBm**, not read. The box cannot read it either: Source Q
  records the readout as subject to a known driver misreport. It is a constant
  in a model already labelled typed.
- **The sensitivity floor moves with width**, about 3 dB per doubling: −82 dBm
  at 20 MHz, −76 at 80. A wide channel spreads the same power over more
  spectrum, so it dies first — on top of the extra path loss at 5 GHz.

### Where each number comes from

Every other source here rates its confidence per field because it is reading
something. This one is *computing*, so the equivalent question is which numbers
carry the authority of a standard relationship and which are ours.

The rungs and the propagation model are now **taken from their sources** —
IEEE Std 802.11-2020 Table 21-25 for receiver sensitivity, ITU-R P.1238 for the
path loss coefficient — rather than recalled, which is what an earlier revision
of this entry admitted to. What remains ours is the layer joining them: how much
impairment a given headroom above the floor implies. That part is asserted, and
the calibration walk in #221 is what would replace it.

| Quantity | Origin | Standing |
|---|---|---|
| `20*log10(f_MHz) - 27.55` | **Friis transmission equation**, decibel form for MHz and metres | exact |
| `RSSI(d) = Ptx - FSPL(1m) - 10*n*log10(d)` | **log-distance path loss**, the form ITU-R P.1238 uses | exact given `n` |
| `n` = 2.8 residential, 3.1 office | **ITU-R P.1238**, which tabulates the distance power loss coefficient N = 28 and N = 31 (N is 10n) | **cited.** The recommendation itself says site-calibrated values are needed |
| `n` = 3.8 obstructed | **ours.** ITU models walls and floors as a separate additive term `Lf(n)`, not by raising N | asserted, and named as a stand-in |
| MCS sensitivity ladder, −82 … −57 dBm at 20 MHz | **IEEE Std 802.11-2020, Table 21-25** | **cited**, exact |
| +3 dB per doubling of channel width | the standard's own scaling, and thermal noise `10*log10(2)` | **cited**, exact |
| MCS data rates | computed from the standard's OFDM parameters: subcarriers × bits × coding ÷ 4 µs | **derived**, and checked against the published 6.5 / 65 / 390 Mbit/s anchors |
| `Ptx` = 20 dBm | the common regulatory ceiling | **assumed**; the box cannot read its own (Source Q) |
| **Implementation gain, 6 dB** | **ours** | asserted, and the number doing the most work — Table 21-25 states the *worst* a conforming receiver may be, and real silicon beats it |
| **MAC efficiency, 0.65** | **ours** | asserted; preambles, spacing, block acks and contention as a flat fraction |
| **The comfort window and impairment curves** | **ours** | asserted. The ORDERING is principled — corruption before loss — the numbers are not |
| **Device figures** laptop 0/3, phone 2/4, watch 5/6 dB | **ours** | asserted, and unmeasurable here — see below |

The device figures deserve their own note, because they look more solid than
they are — they are named after real device classes, which lends them an
authority nothing earned. `station dump` gives the AP's view of a client, and nothing gives the
client's view of the AP, so the difference between the two directions **cannot
be measured from this box at all**. They are plausible for the device classes
named and nothing more.

**Nothing here is ported.** ns-3 and wmediumd implement the same standard
models and are both GPL-2.0, while `docs/LICENSING.md` commits this repo to
containing only its own MIT code. A physical relationship is not anyone's to
license; an implementation of one is, so the relationships were written out and
the implementations left alone.

### Semantics that bite

- **It is a cliff, not a slope.** Frame error rate against SNR is a sigmoid:
  nearly nothing happens across most of the range, then everything happens
  inside about 10 dB. That is what a real walk feels like, and a model that
  degrades linearly with distance is wrong in a way that is hard to name and
  obvious to anyone who has done one. `TestDegradationIsACliffNotASlope` pins
  it; do not smooth it to make the control feel better.
- **Corruption leads, loss follows late and correlated.** A weak signal does not
  drop IP packets — it damages frames that fail their checksum, having already
  spent the airtime to send them. So `CorruptPct` rises first, and `LossPct`
  stays at zero until retries are exhausted, arriving with `LossBurst > 1` so
  netem runs its Gilbert-Elliott model rather than a per-packet coin flip.
  `TestCorruptArrivesBeforeLoss` pins the ordering.
- **Out of range is total loss, NOT a zero rate.** Zero means *unlimited*
  everywhere else in this codebase, so a rate of zero at the floor would hand
  the client a perfect link at the exact moment it should have none.
- **The derived shapes are never stored.** `Policy.Rssi` holds the input;
  `desired()` computes the shapes each tick and they vanish when the model is
  cleared. This is deliberate — storing a model's output beside its input is how
  the two come to disagree, the failure `putLadder`'s provenance reset exists to
  prevent. `TestTheModelDrivesTheKernelWithoutTouchingTheStore` is the guard.
- **dBm is stored, metres are shown.** Metres depend on `n`, which is a guess, so
  a policy in metres would mean a different impairment in a different building.
  dBm replays identically anywhere.
- **The wire is metric; the interface is not necessarily.** `RssiView.DistanceM`
  is always metres — one unit on the wire, as everywhere else here. The reader's
  own unit is chosen in the browser from its locale, so a US reader is shown feet
  without anything downstream knowing. Nothing is stored or transmitted in feet,
  and no comparison anywhere depends on the displayed unit.
- **A hand edit clears the model**, on the same rule that pauses a running
  pattern: otherwise the model would overwrite the typed value on the next tick
  and the controls would appear not to work.

### What it does NOT move, and this is the important part

**RSSI, PHY rate, airtime and `tx failed` all keep reporting the real, healthy
radio.** A client at a modelled 40 m still reads −34 dBm at 961 Mbit/s PHY while
being handed 6 Mbit/s. The card says so in words rather than leaving it to be
discovered, because on screen it otherwise looks like a defect.

This is survivable because ABR players adapt on observed throughput and buffer
level, not on signal strength — the gap bites only for something that reads RSSI
directly, which includes the client's own band-steering decision. That is why a
band transition has to be **driven** rather than hoped for.

**Verified on hardware 2026-09-05.** A model of −74 dBm applied to a client on
`wlan0` (channel 11, 20 MHz) produced an enforced `cap_mbps` of **12.5** while
the stored policy still read `down.rate_mbps: 0` and `rssi: {dbm: -74, n: 3}`.
A subsequent hand edit cleared `rssi` and returned the enforced cap to 0.

**Conclusion for the product:** the model is a stand-in for a walk, honest about
being one. It should be replaced band by band with a measured curve once #106
records `signal` and airtime over time, and the provenance label is what says
which of the two you are looking at.
