# Steering: what each command actually does to a client

> **Target:** boa on the Cudy TR3000, OpenWrt 25.12, boa v0.7.0 with #517.
> **Radios:** `phy1-ap0`, the Cudy's onboard mt798x on 5 GHz ch 149;
> `phy0-ap0`, the onboard mt798x on 2.4 GHz ch 1; `phy3-ap0`, a USB mt7921u on
> 5 GHz ch 36. All three on one SSID, `cudy1263`.
> **Clients:** iPhone, Mac mini, MacBook Pro, all in one room near the box.
> **When:** 2026-10-01 and 2026-10-02.
>
> None of this is a property of boa alone. It is what three particular Apple
> clients did with these radios, in this room. Another client, chipset or
> floor plan can answer differently, so measure your own before relying on it.

## The short version

- **Clients go where they prefer, and a request only works when they already
  half agree.** No client accepted a request to move to another 5 GHz radio:
  0 of 27. About half of the requests to 2.4 GHz were accepted: 14 of 27.
- **The iPhone never stays on 2.4 GHz.** In all 6 of its accepted moves it was
  back on 5 GHz within about 9 s. The Macs mostly stay.
- **A disconnect is not a redirect.** `deauth`, `disassoc`, and the disconnect
  `force` adds after a refusal all left clients reconnecting to the radio they
  prefer, usually the one they were thrown off.
- **Only a deny moves a client reliably**: `ban`, `gather`, `evict`. Even then
  a slow client can outlast the 10 s hold, and one left the network twice (#523).

| Command | What it sends | Reliable move? |
| --- | --- | --- |
| `steer`, `warn`, `term` | an 802.11v BSS Transition request, which the client may decline | **No.** Toward 2.4 GHz sometimes, toward 5 GHz never |
| `force` | the same request, then a disassociation if the client stays | **No.** The client reconnects where it likes, nearly always the same radio |
| `deauth`, `disassoc` | an immediate disconnect | **No.** Back on the same radio within 0.2–21 s |
| `ban`, `gather`, `evict` | a deny list on the radios the client may not use | **Yes**, mostly within 8 s; see the caveats below |

## How it was measured

Clients were placed on the source radio first: a deny on the others until
they landed there, then the deny lifted and a 10 s settle. One command was
sent, and each client's radio was polled about ten times a second for 25 s,
from `iw station get` on the box. Each client's 802.11v answer was read from
hostapd's `BSS-TM-RESP` log line: `status_code` 0 is accept, 1 is reject,
7 is "no suitable candidates", and no line at all means it never answered.

Signal, as measured by clients in the same room: the MacBook saw ch 149 at
−29 dBm and ch 36 at −34 dBm (2026-10-01). A scan from a fixed Linux host saw
ch 1 at −29, ch 149 at −37 and ch 36 at −47 dBm (2026-10-02). `phy3-ap0` is
the weakest of the three by about 5–10 dB. `iw` reports the mt7921u's transmit
power as 3 dBm. That figure is wrong, so it is not used here.

## 1. A request to another 5 GHz radio is never accepted

> **Scope:** source `phy1-ap0` (5 GHz ch 149), target `phy3-ap0` (5 GHz ch 36),
> `phy0-ap0` switched off. Radio-wide requests, all three clients at once,
> 2026-10-02 15:13–15:21. Three repeats of each of `warn`, `term` and `force`.

| Client | Accepted | Answer | Reached the target |
| --- | --- | --- | --- |
| iPhone | **0/9** | none: never sent a BTM response | 0 |
| Mac mini | **0/9** | refused, status 1, every time | 1, after a `force` disconnect |
| MacBook Pro | **0/9** | refused, status 1, every time | 0 |

The iPhone often dropped off 6–8 s after a request and rejoined the same
radio within a second, but it never answered. Of the 9 disconnects `force`
caused, 8 ended back on `phy1-ap0` and 1 (the Mac mini) on `phy3-ap0`.

`phy3-ap0` advertised BSS Transition and 802.11k throughout. That was checked
over the air, from a scan of its beacons. So this is not #516. It does mean
"5 GHz to 5 GHz" here is also "toward a 5–10 dB weaker radio", and a stronger
5 GHz target is untested.

## 2. A request to 2.4 GHz is often accepted, and the iPhone comes back

> **Scope:** source `phy1-ap0` (5 GHz ch 149), target `phy0-ap0` (2.4 GHz ch 1),
> `phy3-ap0` switched off. Radio-wide, all three clients, 2026-10-02
> 15:21–15:30. Three repeats of each of `warn`, `term` and `force`.

| Client | Accepted | Reached 2.4 GHz | Still there at 25 s |
| --- | --- | --- | --- |
| iPhone | **6/9** | 6, within 0.4 s | **0**: back on 5 GHz at 8.8–9.1 s every time |
| Mac mini | **5/9** | 5, within 0.4 s | 3 |
| MacBook Pro | **3/9** | 3, within 0.3 s | 3 |

Refusals were status 7, "no suitable candidates". **Acceptance came in
streaks, not at a steady rate.** Three requests in a row went to all three
clients and drew 9 refusals between 11 acceptances before them and 3 after.
Nothing about the request changed, so the clients re-judged the target, likely
from their own recent scan of it. The box measured nearby 2.4 GHz channels
43–85 % busy during the run, but not its own channel 1, so a busy channel is
plausible and not shown.

The same, tested by hand from the rack's buttons on 2026-10-02 14:00–14:07
(`phy1-ap0` → `phy0-ap0`, all three radios on): the iPhone accepted 4 of 7,
the Mac mini 3 of 7 and the MacBook 1 of 7, and the iPhone went back to 5 GHz
within about 5 s every time. The README's 2026-09-23 table, where `warn` and
`term` moved a MacBook "to the radio named", was this same 5 → 2.4 GHz case,
because the box had only those two radios then.

## 3. Disconnects bring clients back to where they were

> **Scope:** source `phy1-ap0`, the 108-trial matrix, 2026-10-01, one trial
> per client per command, `phy0-ap0` off.

| Command | iPhone | Mac mini | MacBook Pro |
| --- | --- | --- | --- |
| `deauth` | back on the same radio in 0.3 s | back in 8 s | back in 21 s |
| `disassoc` | back in 0.2 s | back in 0.5 s | back in 0.2 s |
| `force` (`insist`), 3 each | back in 7–8 s | back in 5–6 s | back in 5 s |

From the weaker `phy3-ap0` the same commands sent every client to the
stronger `phy1-ap0`: deauth in 8–16 s, disassoc in 1–4 s. That is also a
client going where it prefers, not a command being obeyed.

## 4. Denying the old radio is what moves clients

> **Scope:** the 108-trial matrix, 2026-10-01, plus the demo recordings and
> hand tests of 2026-10-01 and 02.

| Command | From `phy1-ap0` (5 GHz, stronger) | From `phy3-ap0` (5 GHz, weaker) |
| --- | --- | --- |
| `ban` | iPhone 7.2 s and MacBook 7.1 s to `phy3-ap0`; **Mac mini still off the network at 25 s** | all to `phy1-ap0` in 3.6–24 s |
| `gather` | iPhone 7.6 s and MacBook 6.0 s | all to `phy1-ap0` in 1.6–8.6 s |
| `evict` | iPhone 5.5 s and Mac mini 4.1 s; **MacBook back on `phy1-ap0` at 16 s**, after the hold lifted | all to `phy1-ap0` in 1.3–9.8 s |

In the demo recordings (2026-10-01, `phy1-ap0` ↔ `phy0-ap0`), `gather` moved
all three to 2.4 GHz within 8 s and `evict` moved them back within 6 s. The
iPhone's video played straight through both moves.

**The hold is 10 s, and some clients take longer.** The Mac mini twice
failed to land within it when gathered by hand on 2026-10-02: once it rejoined
51 s later, and once it left `cudy1263` for another network entirely. A
client that has not landed when the hold lifts is free to go back. That is
#523, and the API already accepts a longer `?pin=`.

## What is not known

- **Whether the AP's chipset matters.** The mt7921u's own 802.11v trials
  from 2026-10-01 are void, because it was not advertising BSS Transition
  then (#516, fixed by #517). They were not repeated, and section 1 used it
  only as a target.
- **Whether a client accepts a move to a *stronger* 5 GHz radio.** The only
  other 5 GHz radio here was the weaker one.
- **What drives the streaks in section 2.** A channel scan alongside each
  trial would show whether a busy channel 1 explains the refusals.
- **How general any of this is.** Three Apple devices, one room, two days,
  and samples of 3–9 per cell. Android, Windows and older clients answer
  802.11v differently, some not at all.

## Related

- #498: the deauth that follows a gather or evict can catch a client as it
  lands.
- #516 / #517: a hotplugged radio did not advertise 802.11k/v.
- #523: the 10 s gather/evict hold is too short for some clients, and the rack
  has no way to set a longer one.
- The activity log words `warn` and `term` identically, so the log does not
  say which was sent.
