---
name: verify-on-hardware
description: Check a claim about conditioning against the running Pi's kernel instead of reasoning about it — read back the tc qdiscs, classes, filters and counters, confirm the shaper is attached to something real, and measure through the box. Invoke before asserting that shaping, delay, loss, a filter or a capability works, when a number in the UI looks wrong, or when a change touches tc, netem, HTB, filters, the bridge or the radio. Owns the "verify against the kernel, don't reason about it" discipline from CLAUDE.md.
---

# verify-on-hardware

CLAUDE.md's first working rule: nearly every bug found while building this was
a wrong assumption that looked correct. `tc` class ids are hexadecimal. A
bridge rewrites the arrival interface before local delivery. A
protocol-specific packet socket never sees forwarded frames.
`ProtectSystem=strict` makes `/run` read-only. Each was cheap to test and
expensive to assume.

The second rule: **a silent failure is worse than a loud one.** Shaping applied
to nothing, counters reading zero and history never persisting were all
invisible precisely because the failure path was quiet. "No error" is not
evidence.

Container tests pass that hardware fails, because the bridge, the radio and the
real traffic mix are all absent. State what was measured, and where.

## When to use

- Before claiming shaping, delay, jitter, loss or a filter works
- A number in the UI looks wrong, or suspiciously round, or zero
- Any change touching `tc`, netem, HTB, filters, the bridge, or the radio
- Before writing a measurement into `README.md`, `PRD.md` or
  `docs/DATA-CONTRACT.md`

## Where conditioning actually lives

Read this before looking for it in the wrong place:

| Direction | Attached to | Filter matches |
|---|---|---|
| Downlink (internet → client) | egress of the **client's own port** | destination IP |
| Uplink (client → internet) | egress of the **WAN port** | source IP |

So downlink for a wireless client is on `wlan0`, for a wired client on `lan0`,
and uplink for both is on `eth0`. Looking at the bridge itself finds nothing.

**netem enforces the rate, not HTB.** HTB is kept as a classifier and per-client
byte counter. A rate that reads right in HTB but wrong on the wire means the
netem parameters, not the class ceiling.

## The read-back

Reading the kernel's state needs no root — and `tc` must be called by its full
path, because `/usr/sbin` is not on a non-root PATH on Debian and a bare `tc`
fails with `command not found`, which reads like a missing package rather than
a PATH problem:

```sh
BOX=boa@infinite-streaming-boa.local
ssh $BOX 'for i in wlan0 lan0 eth0; do
  echo "=== $i ==="; /usr/sbin/tc qdisc show dev $i; /usr/sbin/tc -s class show dev $i; done'
ssh $BOX 'for i in wlan0 lan0 eth0; do
  echo "=== $i filters ==="; /usr/sbin/tc filter show dev $i; done'
```

Keeping these unprivileged matters: it means a read-back still works on a box
whose sudo rule is missing, which is the state a freshly flashed box is in.

Healthy output on a throttled client looks like this — an HTB class with a
netem child carrying the rate, and `overlimits` well above zero:

```
qdisc htb 1: root refcnt 2 r2q 10 default 0x1
qdisc netem 110: parent 1:10 limit 1000 rate 4580Kbit
class htb 1:10 root leaf 110: prio 0 rate 10Gbit ceil 10Gbit
 Sent 2369330801 bytes 1593669 pkt (dropped 4847, overlimits 271775 requeues 0)
```

What to check, in this order:

1. **A filter exists for the device's addresses.** A policy with no matching
   filter shapes nothing and reports no error. A device under privacy
   extensions holds several routable IPv6 addresses at once and needs a filter
   for each — one filter is a partial shape, which looks like a working shape.
2. **The class counters are moving.** `Sent … bytes` climbing on the client's
   class proves traffic is reaching the shaper. Zero means the filter is not
   matching, whatever the UI says.
3. **`overlimits` is climbing.** That is not an error — it counts how often the
   class hit its ceiling, and a healthy throttled client shows it rising
   constantly. A cap with zero overlimits under load is not capping.
4. **Class ids are hexadecimal.** `1:10` is class 16. Comparing a decimal id
   from code against `tc` output is a false mismatch.

## Measuring through the box

The kernel's own view and the client's view differ for real reasons, and the
gap is documented: a downlink cap lands within 6 % of target across 0.25–50
Mbps, because the cap counts Ethernet, IP and TCP framing that a payload
byte-count does not. A 4–6 % shortfall is expected, not error.

`iperf3` on `:5201` measures the link **unshaped** — it terminates on the box,
so it gives the ceiling a cap must sit under, not the effect of the cap. To
measure the cap, drive real traffic through to a client and read both the class
counters and what the client sees.

Uplink is untested at any rate. Do not report an uplink figure as verified.

## Capability flags

```sh
ssh $BOX 'curl -s --max-time 5 localhost/api/health' | python3 -m json.tool
```

The unit can be `active` while the daemon is failing to configure the kernel —
that distinction is what the capability flags are for. `shaping`, `uplink` and
`radio` false mean the box is running and doing nothing.

## Reporting

Say what was measured, on what, and when. Distinguish:

- what the **kernel** reports (class counters, qdisc parameters)
- what a **client** sees (throughput, RTT)
- what was **not** tested

If a claim is going into `README.md`, `PRD.md` or `docs/DATA-CONTRACT.md`, post
the data contract first — sources, exact fields, what they MEAN, edge cases,
and confidence per claim. The units alone (bytes vs bits, seconds vs
milliseconds, fractions vs percent) would otherwise produce three
plausible-looking wrong answers.
