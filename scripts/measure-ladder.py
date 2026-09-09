#!/usr/bin/env python3
"""Read a real encoded ladder from the source, as ground truth for a cap sweep.

The box derives a rendition ladder by capping a client and watching what it
settles on. That answer needs something to be checked against, and this is it:
the bitrates the encoder ACTUALLY produced, taken from the stream's own segment
index rather than inferred from anything the player did.

    ./scripts/measure-ladder.py JtVljGMKOHU
    ./scripts/measure-ladder.py 'https://www.youtube.com/watch?v=...' --formats 399,398

Needs `yt-dlp` on PATH for metadata. It downloads NO media: a DASH stream's
`sidx` box lists every subsegment's byte size and duration, and it sits in the
first couple of MB, so two range requests describe a 390 MB file completely.

Measured 2026-09-08 on YouTube JtVljGMKOHU (2677 s, AV1 1080p30). See
"one real YouTube programme" in docs/DATA-CONTRACT.md:

    rung    mean     p90            max     peak/mean
    480p     387     633 (1.63x)     901     2.33x
    720p     697    1139 (1.63x)    1793     2.57x
    1080p   1224    2085 (1.70x)    3199     2.61x

# The two findings this exists to produce

EVERY ADJACENT PAIR OF RUNGS OVERLAPS. 720p's peak segment is 1.46x 1080p's
MEAN. No cap value separates two renditions by instantaneous rate; what
separates them is the sustained rate over the player's estimation window. A
ladder read as a list of non-overlapping bands is being read wrong.

MORE THAN HALF OF ALL SWEEP WINDOWS WOULD READ `unstable` ON A PERFECT LINK.
--drift applies the sweep's own arithmetic (300 samples, two halves, unstable
above 20% of the window mean) to the content's real per-second bitrate with no
cap and no impairment at all. It came out at 52-53% of windows on every rung.
Content varies on scene timescales measured in minutes, so a longer window does
not average it out -- it changes which minutes get compared. `unstable` is true
when it fires and says nothing about the network.

# Things this gets right, each because getting it wrong gave a wrong answer

THE PARSE IS CHECKED AGAINST AN INDEPENDENT DERIVATION, and disagreement is
fatal rather than a warning. The mean recomputed from the segment index must
match the bitrate yt-dlp derives from contentLength / approxDurationMs. Four
significant figures of agreement is what separates "measured" from "plausible";
a sidx parsed at a wrong offset yields numbers that look entirely reasonable.

`sidx` IS FOUND BY WALKING BOXES, not assumed at a fixed offset. It sits after
ftyp and moov and those vary in size per format.

A 403 IS REPORTED, NOT SKIPPED. YouTube gates some itags -- on the measured
video, 394/395/396 returned 403 for the index range while 399 succeeded from the
SAME freshly-extracted metadata, so it is format-specific gating rather than URL
expiry. A ladder silently missing its bottom three rungs still looks like a
ladder, which is exactly the failure this whole file exists to avoid.

DURATION COMES FROM THE INDEX, not from the rounded `duration` field. Segment
durations are in the sidx's own timescale; summing them gives 2676.9 s where the
metadata says 2677, and that 0.09 s is the difference between agreeing with
yt-dlp to four figures and to three.

# What this cannot see

Whether a player would ever choose a given rung. This reads what the encoder
made, not what an ABR algorithm does with it -- and the whole point of the
overlap finding is that those are not the same question.
"""
import argparse
import json
import re
import struct
import subprocess
import sys
import urllib.error
import urllib.request

# The sweep's own definitions, mirrored so --drift asks the same question the
# box asks. See "Derived -- rendition ladders from a cap sweep" in
# docs/DATA-CONTRACT.md; if either changes there, change it here.
SWEEP_WINDOW_S = 300
SWEEP_UNSTABLE = 0.20

# How much of the head to pull looking for the index. The measured file needed
# well under 1 MB; 2 covers formats whose moov is larger.
INDEX_BYTES = 2 * 1024 * 1024


class Fatal(Exception):
    pass


def metadata(video):
    """Format list from yt-dlp, including the signed media URLs."""
    try:
        out = subprocess.run(
            ["yt-dlp", "--no-update", "-J", video],
            capture_output=True, text=True, timeout=120,
        )
    except FileNotFoundError:
        raise Fatal("yt-dlp is not on PATH; it is needed for metadata only")
    if out.returncode != 0:
        tail = (out.stderr or "").strip().splitlines()
        raise Fatal("yt-dlp failed: " + (tail[-1] if tail else "no output"))
    return json.loads(out.stdout)


def fetch_range(url, headers, start, end):
    req = urllib.request.Request(url)
    for k, v in headers.items():
        req.add_header(k, v)
    req.add_header("Range", f"bytes={start}-{end}")
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.read()


def iter_boxes(buf):
    """Walk top-level ISO-BMFF boxes, yielding (type, size, body_offset)."""
    off = 0
    while off + 8 <= len(buf):
        size = struct.unpack(">I", buf[off:off + 4])[0]
        btype = buf[off + 4:off + 8].decode("latin1", "replace")
        body = off + 8
        if size == 1:
            if off + 16 > len(buf):
                return
            size = struct.unpack(">Q", buf[off + 8:off + 16])[0]
            body = off + 16
        if size <= 0:
            return
        yield btype, size, body
        off += size


def parse_sidx(buf, body):
    """ISO/IEC 14496-12 sidx -> [(size_bytes, duration_seconds), ...]."""
    p = body
    version = buf[p]
    p += 4                                     # version(1) + flags(3)
    p += 4                                     # reference_ID
    timescale = struct.unpack(">I", buf[p:p + 4])[0]
    p += 4
    p += 16 if version else 8                  # earliest_presentation_time, first_offset
    p += 2                                     # reserved
    count = struct.unpack(">H", buf[p:p + 2])[0]
    p += 2
    if timescale <= 0:
        raise Fatal("sidx declares a zero timescale; the parse is off")
    segs = []
    for _ in range(count):
        w0, dur, _sap = struct.unpack(">III", buf[p:p + 12])
        segs.append((w0 & 0x7FFFFFFF, dur / timescale))   # top bit is reference_type
        p += 12
    return segs


def codec_family(fmt):
    """Which LADDER a format belongs to: av01, avc1, vp9.

    A player picks one codec's ladder and moves within it. Comparing a rung of
    one against a rung of another is comparing renditions no client will ever
    choose between, so every cross-rung claim below is made inside a family.
    """
    v = (fmt.get("vcodec") or "").split(".")[0]
    return {"vp09": "vp9"}.get(v, v) or "?"


def segments_for(fmt):
    """Per-segment (bytes, seconds) for one format."""
    # WebM/Matroska is EBML, not ISO-BMFF: there is no sidx and walking it as
    # boxes yields plausible-looking rubbish rather than an error. Said plainly
    # instead, because "no sidx (boxes: B)" reads like a fetch problem.
    container = (fmt.get("container") or "") + " " + (fmt.get("ext") or "")
    if "webm" in container:
        raise Fatal("WebM/Matroska container: segment index is EBML cues, "
                    "which this does not parse (mp4/DASH only)")

    url, headers = fmt["url"], fmt.get("http_headers", {})
    try:
        head = fetch_range(url, headers, 0, INDEX_BYTES - 1)
    except urllib.error.HTTPError as e:
        # Loud, and specific: a gated format and an expired URL need different
        # responses, and both present as an HTTP error.
        raise Fatal(f"HTTP {e.code} fetching the index "
                    f"({'format is gated' if e.code == 403 else 'URL may have expired'})")
    for btype, size, body in iter_boxes(head):
        if btype == "sidx":
            if body + 12 > len(head):
                raise Fatal("sidx begins beyond the fetched head")
            return parse_sidx(head, body)
    seen = ", ".join(b[0] for b in iter_boxes(head)) or "none"
    raise Fatal(f"no sidx in the first {INDEX_BYTES // 1024} KB (boxes: {seen})")


def summarise(segs):
    rates = sorted(sz * 8 / s / 1000.0 for sz, s in segs if s > 0)
    total_b = sum(sz for sz, _ in segs)
    total_s = sum(s for _, s in segs)
    mean = total_b * 8 / total_s / 1000.0
    q = lambda p: rates[min(len(rates) - 1, int(len(rates) * p))]
    return {
        "n": len(rates), "span_s": total_s, "mean": mean,
        "min": rates[0], "p50": q(0.50), "p90": q(0.90), "max": rates[-1],
    }


def per_second(segs):
    """The per-second series a steady player would deliver."""
    out = []
    for sz, dur in segs:
        kbps = sz * 8 / dur / 1000.0
        out.extend([kbps] * max(1, int(round(dur))))
    return out


def drift(series, window=SWEEP_WINDOW_S, step=10):
    """The sweep's own metric: |mean(h1) - mean(h2)| / mean(window)."""
    half = window // 2
    ds = []
    for i in range(0, max(0, len(series) - window), step):
        w = series[i:i + window]
        m = sum(w) / len(w)
        if m <= 0:
            continue
        ds.append(abs(sum(w[:half]) / half - sum(w[half:]) / half) / m)
    return sorted(ds)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("video", help="video ID or URL")
    ap.add_argument("--formats", help="comma-separated itags (default: every video-only format)")
    ap.add_argument("--drift", action="store_true",
                    help="also apply the sweep's unstable metric to the content")
    ap.add_argument("--tolerance", type=float, default=0.01,
                    help="max %% disagreement with yt-dlp's bitrate before failing (default 1%%)")
    args = ap.parse_args()

    meta = metadata(args.video)
    want = args.formats.split(",") if args.formats else None
    formats = [
        f for f in meta["formats"]
        if (want is not None and f["format_id"] in want)
        or (want is None and f.get("vcodec") not in (None, "none") and f.get("tbr"))
    ]
    if not formats:
        raise Fatal("no matching formats")

    print(f"{meta.get('title', '?')}  ({meta.get('duration')} s)")
    print()
    print(f"{'itag':<6} {'resolution':<12} {'mean':>8} {'p90':>8} {'max':>8} {'peak/mean':>10}  n")
    print("-" * 66)

    failures, rows = [], []
    for f in sorted(formats, key=lambda x: x.get("tbr") or 0):
        itag = f["format_id"]
        try:
            segs = segments_for(f)
        except Fatal as e:
            failures.append((itag, str(e)))
            continue
        s = summarise(segs)

        # The check that makes this a measurement. yt-dlp derives its bitrate
        # from contentLength / approxDurationMs; we derive ours by summing the
        # index. They are independent, so agreement is evidence and a mismatch
        # means the parse landed somewhere wrong.
        ref = f.get("tbr")
        if ref:
            off = abs(s["mean"] - ref) / ref
            if off > args.tolerance:
                failures.append((itag, f"index mean {s['mean']:.1f} disagrees with "
                                       f"yt-dlp {ref:.1f} by {off*100:.2f}%"))
                continue
        rows.append((itag, f, s, segs))
        print(f"{itag:<6} {f.get('resolution', '?'):<12} "
              f"{s['mean']:8.1f} {s['p90']:8.1f} {s['max']:8.1f} "
              f"{s['max']/s['mean']:9.2f}x  {s['n']}")

    # WITHIN A CODEC, never across. A player moves up and down one codec's
    # ladder; "avc1 720p peak vs av01 1080p mean" compares two renditions no
    # client chooses between, and printing it as an overlap would invent a
    # finding.
    families = {}
    for row in rows:
        families.setdefault(codec_family(row[1]), []).append(row)

    for fam, frows in families.items():
        if len(frows) < 2:
            continue
        print()
        print(f"overlap between adjacent {fam} rungs (a peak above the next "
              f"rung's mean means no cap separates them by rate):")
        for (ia, _fa, sa, _), (ib, _fb, sb, _) in zip(frows, frows[1:]):
            over = sa["max"] / sb["mean"]
            print(f"  {ia} peak {sa['max']:7.1f}  vs  {ib} mean {sb['mean']:7.1f}   "
                  f"{over:.2f}x {'OVERLAPS' if over > 1 else 'clear'}")

    if args.drift:
        print()
        print(f"sweep `unstable` on this content, no cap and no impairment "
              f"({SWEEP_WINDOW_S}s window, halves, >{int(SWEEP_UNSTABLE*100)}%):")
        for itag, _f, _s, segs in rows:
            ds = drift(per_second(segs))
            if not ds:
                continue
            tripped = sum(1 for d in ds if d > SWEEP_UNSTABLE) / len(ds)
            print(f"  {itag:<6} median {ds[len(ds)//2]*100:5.1f}%   "
                  f"p90 {ds[int(len(ds)*0.9)]*100:5.1f}%   max {ds[-1]*100:5.1f}%   "
                  f"over threshold {tripped*100:5.1f}% of windows")

    # Never silent. A ladder missing its bottom rungs still looks like a ladder.
    if failures:
        print()
        print("NOT MEASURED:", file=sys.stderr)
        for itag, why in failures:
            print(f"  {itag}: {why}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Fatal as e:
        print(f"measure-ladder: {e}", file=sys.stderr)
        sys.exit(2)
    except KeyboardInterrupt:
        sys.exit(130)
