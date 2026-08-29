# Backlog

**Candidate work lives in GitHub issues, not here.** This file used to carry the
reasoning for each item and drifted out of date within a day — a standup read it
and reported two completed items as blockers, because the work had been done and
the file had not been updated. One source of truth is worth more than a tidy
document.

Each issue carries its own reasoning: why it exists, the mechanism it would use,
and the constraint it runs into. Sizes are Fibonacci points; priority and value
labels say what is worth doing, and several items are deliberately P3 or
value:low because they are not.

    gh issue list --label priority:P2        # what is worth doing next
    gh issue list --label decision           # blocked on a choice, not on work
    gh issue list --label correctness        # produces wrong or missing behaviour

What remains below is not work — it is the set of constraints that are accepted
and documented so they are not rediscovered.

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
