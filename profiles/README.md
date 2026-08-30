# Profiles

Saved box configurations: every device's conditioning, sub-classes, ABR
ladders and pattern, in one document.

These live here because **the box is not the system of record**. Configuration
is held in the daemon's state on the Pi, and reflashing replaces the whole
image. A measured ladder costs roughly an hour of a real device streaming real
content, so losing one to a reflash is expensive in a way that losing a rate
limit is not.

## Saving and restoring

```sh
./scripts/config.sh export > profiles/box.json
./scripts/config.sh import profiles/box.json           # merge (default)
./scripts/config.sh import profiles/box.json replace   # box matches the file
./scripts/config.sh export pifi@192.168.1.9 > other.json
```

- **merge** upserts the devices in the file and leaves every other device
  alone. It cannot destroy configuration the file does not mention, which is
  why it is the default.
- **replace** additionally *deletes* devices absent from the file, so the box
  ends up matching it exactly. It asks before doing so.

Underneath, `GET /api/config` and `POST /api/config?mode=merge|replace`.

An import is validated **in full before anything is written**, and applied in a
single store write. A partially applied configuration would leave the box
conditioning traffic by a state that is neither the old one nor the new one,
and nothing in the UI would say so.

## What is in the document

| Field | Notes |
|---|---|
| `version` | Schema version. An import of an unrecognised version is refused, not half-understood. |
| `exported_at` | Informational. Import never reads it. |
| `devices[]` | One entry per configured device: `mac`, `label`, `enabled`, `down`, `up`, `sub`, `ladders`, `pattern`. |

`rev` is **not** exported. It is the live optimistic-concurrency counter for
one box's edit history; carrying it into a document invites a restore to write
a revision belonging to a different timeline, and it would churn the diff of a
committed file on every unrelated edit.

Telemetry is absent for the same reason the store never persists it: it
rebuilds itself on restart and means nothing on another box.

## Ladders carry their provenance, and import preserves it

- `measured` — produced by a sweep. The rungs are observed plateaus.
- `typed` — entered or corrected by hand.

Every *other* write path demotes a ladder to `typed`, so that the authority of
a measurement is never attached to a number nobody measured. **Import is the
exception**: a restore is not a hand edit, and demoting would mean a box could
never be returned to the state it was backed up from. The document is
operator-owned and is trusted the way any configuration file is — so an edited
file can claim `measured`, and that is on whoever edited it.

Each rung records `mbps` — the rendition's cost in **wire** Mbps, so TCP/IP and
TLS overhead is included — plus `up_at_mbps` and `down_at_mbps`, the caps
observed to produce and to end that rendition.

**`up_at_mbps` is a sufficient cap, not a minimum one.** A sweep's cap steps
are 20-45% wide, so the true switch threshold lies somewhere between the
previous level's cap and the recorded one — roughly 1.1x to 1.5x the rung. Do
not read it as the threshold.

## The MAC caveat

A device is keyed by MAC, and iOS and Android rotate MACs per network by
default. If a device reappears with a new one, its configuration — and its
ladder — is stranded, and importing an old profile will not find it. See
[#45](../../../issues/45). Until that is resolved, a device whose ladder you
care about should have Private Wi-Fi Address turned off for this network.

## Live content bounds what a ladder can tell you

`box.json`'s `infinite-stream` ladder was measured against a live HLS stream
(`EXT-X-SERVER-CONTROL:HOLD-BACK=21.000`). A player cannot fetch past the live
edge, so anything derived from *buffering* behaviour on this content is bounded
by roughly 21 seconds regardless of the player's own policy. The rung rates are
unaffected; buffer-depth conclusions are not.
