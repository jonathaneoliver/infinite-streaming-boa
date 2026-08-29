# Data Contract — pifi monitoring and enforcement

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

Retained for reference only. As a transparent bridge pifi runs no DHCP server,
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

**VERIFIED.** Real output from the stack pifi builds:

```json
{"class":"htb","handle":"1:10","leaf":"0x110","rate":1500000,"ceil":1500000,
 "stats":{"bytes":0,"packets":0,"drops":0,"overlimits":0,
          "backlog":0,"qlen":0,"lended":0,"borrowed":0}}
```

| Field | Meaning |
|---|---|
| `handle` | Class id, e.g. `1:10`. pifi's own map binds this to a device + sub-class |
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

**Semantics that bite**

- Three different units from the three fields a user sets in one row of the UI.
  Milliseconds and percent exist only in the API and the UI; the kernel boundary
  is seconds and fractions. Convert in exactly one place.
- **`limit` is a silent-loss trap.** netem's default queue is 1000 packets. The
  bandwidth-delay product of a 50 Mbps link at 500 ms is roughly 2100 full-size
  packets — so a "50 Mbps, 500 ms, 0 % loss" profile would drop ~50 % of traffic
  while reporting zero configured loss, and the tester would blame the
  application. pifi must size `limit` from the configured rate and delay
  (`rate_bps x delay_s / (8 x 1500)`, with headroom) rather than accept the
  default.

---

## Derived model — the join

```
identity      = MAC                       (stable across DHCP renewal)
ip            = resolved attribute        (F, else C; may be absent)
port          = from B (wifi) or C2 (wired); REQUIRED to shape downlink
present       = appears in B, C2 or F     (NOT "has a lease")
label         = A.hostname, else user-set nickname, else MAC
policy        = keyed by MAC, persisted   (survives IP change and disconnect)
counters      = D + E, keyed by classid   (epoch-bounded per policy write)
```

Join order is **B LEFT JOIN (A, C)**: presence comes from the radio, addresses
are decoration. A client that is associated but has not completed DHCP is a real
and common state — it must render in the UI as present-but-unaddressed, and it
cannot be shaped yet, because every `tc` filter needs an IP to match on.

## Enforcement semantics

Both directions are shaped on a **true egress queue**, on the last interface the
packet crosses before leaving pifi:

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
nothing injected onto a network pifi is meant to be invisible on.

**Semantics that bite**

- **The arrival interface is the BRIDGE, not the physical port.** The bridge
  rewrites `skb->dev` before delivering locally, so a frame that physically
  arrived on wlan0 is reported as arriving on br-lan. An early version of pifi
  discarded bridge-delivered frames in order to read the port from them, which
  silently discarded *every* frame and left every client undiscovered and
  unconditioned while the interface showed policies applied.
- ARP therefore answers **only** "what address does this MAC hold". The port
  comes from the forwarding database and the wireless station table, which are
  the right tools for that question and were verified to work on real forwarded
  traffic.
- Hosts upstream of pifi ARP too. Anything the forwarding database places on the
  WAN port is excluded, or the client list fills with the rest of the network.
- Entries older than 5 minutes are dropped: a stale binding would aim a shaping
  filter at an address that may since have moved to another device.
