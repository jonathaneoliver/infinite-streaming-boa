#!/usr/bin/env python3
"""Measure what the conditioner actually delivers, from the client side.

Run this from a machine associated to the box's own Wi-Fi (or plugged into a
downstream wired port), pointed at an HLS master playlist. It caps that machine
at a series of rates and reports what fraction of each cap actually arrived,
how repeatable that is, and whether delivery stalled.

    ./scripts/measure-throttle.py \\
        --pi pifi@infinite-streaming-pifi.local \\
        --mac 12:bb:19:0e:ac:7c \\
        --bind 192.168.0.25 \\
        --master https://example/live/master.m3u8

Expect roughly 0.94-0.95 of the configured cap. That is not error: a cap limits
WIRE bytes and this counts TCP payload, and 66 bytes of header per 1448-byte
payload is +4.6%, so payload over cap lands near 1/1.046 = 0.956. The remaining
gap is the request round trip at each segment boundary, which is why the ratio
rises with rate as more bytes move between requests.

Measured 2026-08-30 on a Pi 5 over 5 GHz: 0.943, 0.947, 0.949, 0.951, 0.952 at
0.25, 0.5, 1, 2 and 4 Mbps, repeating to within 0.003.

# Five things this gets right, each because getting it wrong produced a
# confident and completely wrong answer first

TRANSFER SIZE IS MATCHED TO THE RATE. For each cap it fetches the variant a
player would actually choose. Pulling one big file instead -- a 19 MB 2160p
segment at 0.25 Mbps -- puts ten minutes of data against a queue holding 48
seconds of it, fills the queue, and produces multi-second stalls. Those were
reported as bufferbloat in the kernel before the transfer size was questioned.
With realistic sizes the worst gap across the whole range is 0.66 s.

ONE PLAYER SESSION FOR THE WHOLE RUN. A dispatcher may allocate an edge per
player_id. Minting a fresh one per request exhausted the pool after a couple of
dozen requests and the server began answering 503.

NON-200 IS AN ERROR, LOUDLY. Returning an empty body on a non-200 turned "the
server refused" into "the ladder has no variants", and the run then printed "no
valid runs" as though it had measured something.

read1(), NOT read(). read(n) blocks until it has all n bytes, so timestamps
record when the buffer filled rather than when data arrived -- about 130 ms of
pure quantisation at 0.25 Mbps, which swamps any timing measurement.

EVERY REDIRECT HOP IS BOUND. On a machine with both wired and wireless paths to
the same subnet, an unbound redirect puts the actual transfer on the wrong
interface, where nothing is shaped -- and it looks like a perfectly successful
measurement.

# What this cannot see

Sub-second delivery pacing. TLS delivers whole records, so arrival is quantised
at one record however the shaper paced the packets underneath: measured gaps
track record_size/rate to within a few percent at every rate. A packet capture
at the box's egress interface is the only way to see the real spacing.
"""
import argparse
import http.client
import json
import re
import ssl
import statistics
import subprocess
import sys
import time
import uuid
from urllib.parse import urlsplit

CTX = ssl.create_default_context()
CTX.check_hostname, CTX.verify_mode = False, ssl.CERT_NONE

# One session for the whole process, exactly as a real player uses one for its
# whole lifetime. See the header.
PLAYER_ID = uuid.uuid4()

STALL_S = 1.0


class Fetcher:
    def __init__(self, master, bind):
        u = urlsplit(master)
        self.host, self.port = u.hostname, u.port or 443
        self.master_path = u.path
        self.bind = bind

    def _src(self, bound):
        return (self.bind, 0) if (bound and self.bind) else None

    def get(self, host, port, path, bound=True):
        for _ in range(5):
            c = http.client.HTTPSConnection(host, port, timeout=25,
                                            context=CTX, source_address=self._src(bound))
            c.request("GET", path)
            r = c.getresponse()
            if r.status in (301, 302, 303, 307, 308):
                loc = urlsplit(r.getheader("Location"))
                c.close()
                host, port = loc.hostname, loc.port or 443
                path = loc.path + ("?" + loc.query if loc.query else "")
                continue
            body = r.read()
            c.close()
            if r.status != 200:
                raise RuntimeError(f"GET {path} -> HTTP {r.status}")
            return body, host, port
        raise RuntimeError(f"too many redirects fetching {path}")

    def ladder(self):
        """[(Mbps, variant playlist URI)], ascending, from the master playlist."""
        body, _, _ = self.get(self.host, self.port,
                              f"{self.master_path}?player_id={PLAYER_ID}", bound=False)
        lines = body.decode("utf-8", "replace").splitlines()
        out = []
        for i, line in enumerate(lines):
            if line.startswith("#EXT-X-STREAM-INF") and i + 1 < len(lines):
                m = re.search(r"BANDWIDTH=(\d+)", line)
                if m:
                    out.append((int(m.group(1)) / 1e6, lines[i + 1].strip()))
        if not out:
            raise RuntimeError("master playlist listed no variants")
        return sorted(out)

    def segments(self, variant):
        base = self.master_path.rsplit("/", 1)[0]
        path = variant if variant.startswith("/") else f"{base}/{variant}"
        body, host, port = self.get(self.host, self.port,
                                    f"{path}?player_id={PLAYER_ID}", bound=False)
        paths = [l.strip() for l in body.decode("utf-8", "replace").splitlines()
                 if l.strip() and not l.startswith("#")]
        return paths, host, port


def set_cap(pi, mac, mbps):
    body = json.dumps({"down": {"rate_mbps": mbps, "delay_ms": 0,
                                "jitter_ms": 0, "loss_pct": 0}})
    subprocess.run(["ssh", pi,
                    f"curl -s -X PATCH localhost/api/devices/{mac}/policy "
                    f"-H 'Content-Type: application/json' -d '{body}' >/dev/null"],
                   capture_output=True, timeout=30)


def contention(pi, mac, seconds):
    """What every OTHER client pulled, so the confound is measured not assumed."""
    raw = subprocess.run(["ssh", pi, "curl -s localhost/api/history"],
                         capture_output=True, text=True, timeout=30).stdout
    try:
        clients = json.loads(raw).get("clients", {})
    except json.JSONDecodeError:
        return 0.0
    return sum(statistics.mean([s["down"] for s in series][-seconds:])
               for m, series in clients.items()
               if m != mac and series)


def one_run(f, rungs, cap, window):
    """Fetch the variant this cap admits, back to back, for `window` seconds."""
    fits = [v for v in rungs if v[0] <= cap * 0.95] if cap else rungs
    _, variant = fits[-1] if fits else rungs[0]
    paths, host, port = f.segments(variant)
    if not paths:
        return None

    conn = http.client.HTTPSConnection(host, port, timeout=25, context=CTX,
                                       source_address=f._src(True))
    ev, t0 = [], time.monotonic()
    end, i, nseg = t0 + window, 0, 0
    try:
        while time.monotonic() < end:
            conn.request("GET", f"{paths[i % len(paths)]}?player_id={PLAYER_ID}")
            i += 1
            r = conn.getresponse()
            if r.status != 200:
                r.read()
                break
            while True:
                chunk = r.read1(65536)
                if not chunk:
                    break
                ev.append((time.monotonic() - t0, len(chunk)))
                if time.monotonic() >= end:
                    r.read()
                    break
            nseg += 1
    except Exception as exc:
        print(f"      transfer ended early: {exc}", file=sys.stderr)
    finally:
        conn.close()

    if len(ev) < 10:
        return None
    # Span runs first arrival to last, so the handshake ahead of the first byte
    # is outside the measured window.
    span = ev[-1][0] - ev[0][0]
    if span <= 0:
        return None
    total = sum(n for _, n in ev)
    gaps = [ev[k][0] - ev[k - 1][0] for k in range(1, len(ev))]
    return {"mbps": total * 8 / span / 1e6, "segments": nseg, "mb": total / 1e6,
            "max_gap_s": max(gaps) if gaps else 0.0, "span_s": span,
            "variant": variant}


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--pi", required=True, help="ssh target for the box")
    ap.add_argument("--mac", required=True, help="this machine's MAC as the box sees it")
    ap.add_argument("--bind", required=True, help="this machine's IP on the shaped path")
    ap.add_argument("--master", required=True, help="HLS master playlist URL")
    ap.add_argument("--rates", default="0.25,0.5,1,2,4", help="caps in Mbps")
    ap.add_argument("--repeats", type=int, default=4)
    ap.add_argument("--window", type=int, default=20, help="sample seconds per run")
    ap.add_argument("--settle", type=int, default=5)
    ap.add_argument("--json", help="write results here")
    a = ap.parse_args()

    f = Fetcher(a.master, a.bind)
    rungs = f.ladder()
    print(f"ladder: {', '.join(f'{b:.2f}' for b, _ in rungs)} Mbps")
    print(f"{a.repeats} runs of {a.window}s per rate\n")
    print(f"{'cap':>6} {'mean':>7} {'sd':>6} {'spread':>7} {'worst gap':>10} "
          f"{'others':>7}  runs")
    print("-" * 78)

    out = []
    for cap in [float(x) for x in a.rates.split(",")]:
        set_cap(a.pi, a.mac, cap)
        ratios, rows, others = [], [], []
        for _ in range(a.repeats):
            time.sleep(a.settle)
            r = one_run(f, rungs, cap, a.window)
            if not r:
                continue
            rows.append(r)
            ratios.append(r["mbps"] / cap if cap else float("nan"))
            others.append(contention(a.pi, a.mac, a.window))
        if not ratios:
            print(f"{cap:6.2f}   no valid runs")
            continue
        worst = max(r["max_gap_s"] for r in rows)
        stalls = sum(1 for r in rows if r["max_gap_s"] > STALL_S)
        print(f"{cap:6.2f} {statistics.mean(ratios):7.3f} "
              f"{statistics.pstdev(ratios):6.3f} "
              f"{max(ratios) - min(ratios):7.3f} {worst:9.2f}s {max(others):7.2f}  "
              + " ".join(f"{x:.3f}" for x in ratios)
              + (f"   {stalls} STALLED" if stalls else ""))
        out.append({"cap": cap, "ratios": ratios, "mean": statistics.mean(ratios),
                    "sd": statistics.pstdev(ratios), "worst_gap_s": worst,
                    "stalls": stalls, "other_traffic_mbps": max(others),
                    "runs": rows})

    set_cap(a.pi, a.mac, 0)
    print("\ncap cleared")
    if a.json:
        with open(a.json, "w") as fh:
            json.dump(out, fh, indent=2)


if __name__ == "__main__":
    main()
