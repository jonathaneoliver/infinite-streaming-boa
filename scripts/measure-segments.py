#!/usr/bin/env python3
"""Per-segment bitrate for a stream whose manifest cannot be read.

`measure-ladder.py` reads a DASH segment index directly and is the better tool
whenever that is possible. It is not possible for a DRM service: Netflix's
manifest and media are encrypted end to end, and no range request will produce
an index. This gets the same numbers a different way.

    ./scripts/measure-segments.py --host 192.168.0.29
    ./scripts/measure-segments.py --host 192.168.0.29 --secs 120 --gap 0.35
    ./scripts/measure-segments.py --file capture.txt        # analyse, don't capture

TLS hides the URL, the headers and the body. It does not hide the SHAPE. A
player asks for one segment (a small upstream request) and the server answers
with a burst at line rate; both then go quiet until the next segment is due. So
burst size and burst spacing are in clear, and burst bytes over segment duration
is the instantaneous bitrate the manifest would have given us.

MEASURED 2026-09-08 against Netflix on a Google TV, 1080p24 AV1:

    peer                down bytes    role
    198.45.61.252        3,706,887    Open Connect appliance
    45.57.58.179         3,230,021    a SECOND OCA, striped in parallel

    median fetch size    649,337 bytes
    median fetch period  5.04 s
    sustained            ~1.5 Mbps combined
    rate WITHIN a burst  40-150 Mbps

The last two lines are the point. A 650 KB segment is pulled at up to 150 Mbps
and the link then idles for five seconds. That is why a rendition's delivered
rate must be a MEAN over whole segments and never a median over samples -- the
1 Hz series is bimodal, and the median lands on a rate the traffic never
carried. See "Derived -- rendition ladders from a cap sweep" in
docs/DATA-CONTRACT.md.

# Things this gets right, each because getting it wrong gave a wrong answer

IT CAPTURES ON THE BOX, NOT ON A CLIENT. A laptop cannot see another device's
traffic on a switched network, and the appliance is the only point every
client's bytes cross.

EITHER CAPTURE POINT IS VALID, and this was checked rather than assumed:
simultaneous captures on br-lan and eth0 came back byte-for-byte identical
(5324 packets, 3,706,887 bytes each). A bridge capture does NOT double-count
forwarded frames, which is the obvious worry and would have inflated every
figure here by two.

A SHORT WINDOW MEASURES THE BUFFER, NOT THE BITRATE. A 60s sample during a
buffer refill read 4.3 Mbps for a stream whose steady state is 1.5. The default
window is deliberately long, and --secs below about 60 will report the player's
fetch behaviour rather than the content's rate.

THE BUSIEST PEER IS NOT THE WHOLE STREAM. Netflix served this title from two
OCAs at once, roughly evenly. Reporting only the top peer would have halved the
answer, so every peer above a floor is summed.

# When the box can answer this WITHOUT a capture, and when it cannot

The daemon already derives per-client throughput from tc counters at its 1 Hz
tick. MEASURED, by re-sampling this capture at different intervals and comparing
against the truth from packet timing:

    interval   fetches resolved (of 53)   segment-size error
    1.0s  (the tick)        13                  115%
    0.5s                    31                   58%
    0.2s                    46                    0%
    0.1s                    52                    0%

So the SUSTAINED RATE needs no capture at all -- the 1 Hz series reproduces it
exactly (4.38 Mbps against 4.38 from the packets). Segment SIZE and COUNT need
roughly 5 Hz: at 1 Hz two peers fetching every ~2s merge into 13 contiguous runs
where there were 53 fetches, and the recovered size is more than double the
truth.

Capture is unavoidable for two things. Burst DURATION is 0.062s here, sixteen
times shorter than one tick, so the 40-150 Mbps in-burst rate cannot be sampled
by any practical counter poll. And tc classes are per CLIENT, not per flow, so
counters cannot separate one CDN peer from another, or the video stream from
everything else the device is doing -- which is exactly how the two Netflix OCAs
were found.

# What this cannot see

Which RENDITION a burst belongs to. It gives bytes and timing, not a rung
label. Pair it with the decoder's own view over adb --
`dumpsys media.metrics` reports resolution, framerate and dropped frames -- and
the two together identify a rung: 1920x1080 at 24fps carrying 1.5 Mbps.

URLs, headers, content. Those are encrypted and stay that way. The only
cleartext identifier is the TLS SNI, which is what lets ntopng name an OCA.
"""
import argparse
import collections
import os
import re
import statistics
import subprocess
import sys
import tempfile

# tcpdump -q output: "<epoch> IP <src>.<sport> > <dst>.<dport>: tcp <len>"
LINE = re.compile(
    r"^(?P<t>\d+\.\d+) IP (?P<src>[\d.]+)\.(?P<sp>\d+) > (?P<dst>[\d.]+)\.(?P<dp>\d+): tcp (?P<n>\d+)"
)

# A peer carrying less than this is control plane or telemetry, not media.
MEDIA_FLOOR_BYTES = 250_000


class Fatal(Exception):
    pass


def capture(pi, iface, host, port, secs):
    """Run tcpdump on the appliance and return its text output.

    Headers only: -s 96 keeps enough for the IP and TCP headers and discards
    the payload, which is encrypted and useless to us anyway. Capturing full
    packets would multiply the transfer for nothing.
    """
    filt = f"host {host} and tcp port {port}"
    cmd = (f"sudo timeout {secs + 2} /usr/bin/tcpdump -i {iface} "
           f"-nn -tt -q -s 96 '{filt}' 2>/dev/null")
    print(f"capturing {secs}s on {iface} for {host} ...", file=sys.stderr)
    out = subprocess.run(["ssh", pi, cmd], capture_output=True, text=True)
    # tcpdump exits non-zero when timeout kills it, which is the normal path.
    if not out.stdout.strip():
        raise Fatal(f"no packets captured. Is tcpdump installed on the box, and "
                    f"is {host} actually streaming? (stderr: {out.stderr.strip()[:200]})")
    return out.stdout


def parse(text, client):
    """-> {peer: {'down': [(t, bytes)], 'up': bytes, 'dbytes': int}}"""
    peers = collections.defaultdict(lambda: {"down": [], "up": 0, "dbytes": 0})
    for raw in text.splitlines():
        m = LINE.match(raw)
        if not m:
            continue
        n = int(m.group("n"))
        if n == 0:
            continue                       # pure ACK, no payload
        if m.group("dst") == client:
            p = peers[m.group("src")]
            p["down"].append((float(m.group("t")), n))
            p["dbytes"] += n
        elif m.group("src") == client:
            peers[m.group("dst")]["up"] += n
    return peers


def bursts(packets, gap):
    """Split a peer's downstream packets into fetches on quiet gaps."""
    packets = sorted(packets)
    out, cur, start, last = [], 0, packets[0][0], packets[0][0]
    for t, n in packets:
        if t - last > gap:
            out.append((start, last, cur))
            cur, start = 0, t
        cur += n
        last = t
    out.append((start, last, cur))
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--host", help="the client whose stream to measure")
    ap.add_argument("--pi", default="boa@infinite-streaming-boa.local")
    ap.add_argument("--iface", default="br-lan",
                    help="capture interface; br-lan and eth0 are equivalent")
    ap.add_argument("--port", default="443")
    ap.add_argument("--secs", type=int, default=120,
                    help="capture length. Under ~60s measures the buffer, not the rate")
    ap.add_argument("--gap", type=float, default=0.35,
                    help="quiet seconds that separate one fetch from the next")
    ap.add_argument("--file", help="analyse an existing tcpdump -tt -q text file")
    ap.add_argument("--detail", action="store_true", help="print every fetch")
    args = ap.parse_args()

    if args.file:
        text = open(args.file).read()
        if not args.host:
            raise Fatal("--host is needed to tell downstream from upstream")
    else:
        if not args.host:
            raise Fatal("--host is required (the client to measure)")
        text = capture(args.pi, args.iface, args.host, args.port, args.secs)

    peers = parse(text, args.host)
    if not peers:
        raise Fatal("captured packets, but none to or from " + args.host)

    media = {ip: p for ip, p in peers.items() if p["dbytes"] >= MEDIA_FLOOR_BYTES}
    print(f"{'peer':<18} {'down bytes':>13} {'up':>9} {'pkts':>8}  role")
    for ip, p in sorted(peers.items(), key=lambda kv: -kv[1]["dbytes"]):
        role = "media" if ip in media else "control/telemetry"
        print(f"  {ip:<16} {p['dbytes']:>13,} {p['up']:>9,} {len(p['down']):>8,}  {role}")

    if not media:
        raise Fatal(f"no peer carried more than {MEDIA_FLOOR_BYTES:,} bytes; "
                    "nothing here looks like a media stream")

    # Every media peer, not just the busiest: Netflix stripes one title across
    # two OCAs, and taking the top peer alone halves the answer.
    all_sizes, all_periods, first, last, total = [], [], None, None, 0
    for ip, p in sorted(media.items(), key=lambda kv: -kv[1]["dbytes"]):
        bs = bursts(p["down"], args.gap)
        total += sum(b for _, _, b in bs)
        first = min(x for x in (first, bs[0][0]) if x is not None)
        last = max(x for x in (last, bs[-1][1]) if x is not None)
        all_sizes += [b for _, _, b in bs]
        all_periods += [bs[i][0] - bs[i - 1][0] for i in range(1, len(bs))]

        print(f"\n  {ip}: {len(bs)} fetches")
        if args.detail:
            print(f"  {'#':>4} {'at':>8} {'bytes':>10} {'burst':>7} {'gap':>8} {'Mbps in burst':>14}")
            prev = None
            for i, (s, e, b) in enumerate(bs, 1):
                dur = max(e - s, 1e-6)
                g = f"{s - prev:7.2f}s" if prev else "      -"
                print(f"  {i:>4} {s - bs[0][0]:7.2f}s {b:>10,} {dur:6.2f}s {g:>8} "
                      f"{b * 8 / dur / 1e6:>13.1f}")
                prev = e

    span = last - first
    print(f"\nsummary over {span:.1f}s across {len(media)} media peer(s)")
    print(f"  total delivered      {total:,} bytes")
    print(f"  SUSTAINED RATE       {total * 8 / span / 1e6:.2f} Mbps")
    if all_sizes:
        print(f"  median fetch size    {int(statistics.median(all_sizes)):,} bytes")
    if all_periods:
        print(f"  median fetch period  {statistics.median(all_periods):.2f}s  (per peer)")

    # DELIBERATELY NOT PRINTED: "segment size / fetch period" as a bitrate.
    # It reads like the per-rendition rate and is wrong by the striping factor
    # whenever a service uses more than one server -- Netflix uses two, so each
    # peer's period is twice the real segment cadence and the figure came out at
    # a third of the sustained rate sitting directly above it. The sustained
    # rate is the honest number; converting a fetch period into a segment
    # duration needs to know how the streams are divided, and nothing in a
    # capture says that.
    if len(media) > 1:
        print(f"\n  NOTE: {len(media)} peers served this stream in parallel. Each peer's")
        print("  fetch period is therefore longer than the segment duration, and the")
        print("  sustained rate above -- not any per-peer figure -- is the stream's rate.")
    if span < 60:
        print("\n  WARNING: window under 60s. A player refilling its buffer pulls far")
        print("  above the content's rate; this number is the fetch behaviour, not"
              " the bitrate.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Fatal as e:
        print(f"measure-segments: {e}", file=sys.stderr)
        sys.exit(2)
    except KeyboardInterrupt:
        sys.exit(130)
