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
  half agree.** Requests to the weakest radio, `phy2-ap0`/`phy3-ap0` on 5 GHz
  ch 36, were refused every time: 0 of 27 in the run and about 15 by hand.
  Requests between the two strong radios were mostly accepted, **in both
  directions**: 5 to 2.4 GHz and 2.4 to 5 GHz. So signal, not band, looks like
  what decides it. Section 5 has the evidence.
- **The iPhone never stays on 2.4 GHz.** In all 6 of its accepted moves it was
  back on 5 GHz within about 9 s. The Macs mostly stay.
- **A disconnect is not a redirect.** `deauth`, `disassoc`, and the disconnect
  `force` adds after a refusal all left clients reconnecting to the radio they
  prefer, usually the one they were thrown off.
- **Only a deny moves a client reliably**: `ban`, `gather`, `evict`. Even then
  a slow client can outlast the 10 s hold, and one left the network twice.
  The hold ends when every client has landed, so it moves clients but does not
  keep them: one that did not want the destination goes back.

| Command | What it sends | Reliable move? |
| --- | --- | --- |
| `steer`, `warn`, `term` | an 802.11v BSS Transition request, which the client may decline | **No.** Often toward a stronger radio, never toward a weaker one |
| `force` | the same request, then a disassociation if the client stays | **No.** The client reconnects where it likes, usually the same radio, unless the only other radio up is the target |
| `deauth`, `disassoc` | an immediate disconnect | **No.** Back on the same radio within 0.2–21 s |
| `ban`, `gather`, `evict` | a deny list on the radios the client may not use | **Yes**, mostly within 8 s; see the caveats |

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

## 1. A request to the weaker 5 GHz radio is never accepted

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
client that has not landed when the hold lifts is free to go back. The API
accepts a longer ceiling with `?pin=<seconds>`; the rack's buttons always use
10 s.

**Since #527 (2026-10-02), gather and evict ask before they bar.** They bar
every radio the client must not use except the one it is on, send a `force`
request toward the destination while it is still associated, wait up to 5 s
for it to leave or refuse, and only then bar its current radio. A client that
accepts moves without being disconnected; one that refuses is barred and
lands where it must. Nothing is ever sent to the destination. Measured on the
Cudy after the change: clients that accepted stayed on the destination after
the bars lifted, and clients pushed by the bar went back to their preferred
radio within 4–12 s. The measurements above this paragraph predate #527.

## 5. Hand tests after a reboot: the target radio decides

> **Scope:** tested by hand from the rack's buttons, 2026-10-02 19:26–19:38,
> after the Cudy rebooted. The USB radio came back as **`phy2-ap0`** (it was
> `phy3-ap0`: phy numbers follow probe order), with 802.11k/v on all three.
> The "radios up" column is reconstructed from the activity log, which
> records every access point taken down or brought up.

The log does not record which button was pressed: `steer`, `warn`, `term` and
`force` all log the same "asked to move" line. A `force` is shown only where it
can be inferred, from a refusing client being disconnected exactly 5 s later.

| Time | Radios up | Request | Result |
| --- | --- | --- | --- |
| 19:26:56–19:27:12 | phy0, phy1, phy2 | phy1 → **phy2** ×3 | all refused, or no answer from the iPhone; nobody moved |
| 19:29:06 | phy0, phy1 | phy1 → **phy0** | both Macs **accepted**; iPhone no answer |
| 19:31:21–41 | phy0, phy2 | iPhone: phy2 → phy0 ×3 | refused (7), refused (1), then **accepted**; back on phy2 by itself 12 s later |
| 19:31:59–32:24 | phy0, phy2 | phy0 → **phy2** ×4, then `force` | Macs refused every time; after the disconnect, with only phy2 left, the MacBook landed on phy2 |
| 19:32:33–46 | phy0, phy2 | phy2 → phy0 | all three **accepted** (the MacBook on its second try) |
| 19:32:54 | phy0, phy2 | phy0 → **phy2**, `force` | Macs refused; after the disconnect all three ended on phy2 |
| 19:33:46 | phy0, phy1 | iPhone: phy0 → **phy1** | **accepted** |
| 19:33:56 | phy0, phy1 | phy1 → **phy0** | all three **accepted** |
| 19:34:10 | phy0, phy1 | phy0 → **phy1** | all three **accepted** |
| 19:34:27–50 | phy1, phy2 | phy1 → **phy2**, `force` | Macs refused; after the disconnect the iPhone and Mac mini landed on phy2, the MacBook back on phy1 |
| 19:37:05–26 | phy1, phy2 | MacBook: phy1 → **phy2**, `force` | refused twice; disconnected; back on phy1 |
| 19:37:40 | phy0, phy1 | phy1 → **phy0**, `force` | Macs **accepted**; iPhone refused (7), disconnected at +5 s, rejoined phy1 7.5 s later |

What this adds to sections 1 and 2:

- **Every request to `phy2-ap0` was refused**, from either radio, by both
  Macs, and the iPhone never accepted one. It is the weakest of the three by
  5–10 dB.
- **Requests between `phy0-ap0` and `phy1-ap0`, the two strong radios, were
  mostly accepted, in both directions.** 2.4 → 5 GHz at 19:34:10 was accepted
  by all three. So the 5 → 5 GHz refusals in section 1 are better explained by
  the target being weaker than by its band. That is a likely explanation, not
  a proven one; making `phy2-ap0` the strongest radio would test it.
- **Where `force` puts a client depends on which radios are up.** With only
  the source and one other radio serving, a disconnected client often lands on
  the other, because it is the only choice. With a strong alternative up it
  goes back to the strong one.

## What is not known

- **Whether the AP's chipset matters.** The mt7921u's own 802.11v trials
  from 2026-10-01 are void, because it was not advertising BSS Transition
  then (#516, fixed by #517). They were not repeated, and section 1 used it
  only as a target.
- **Whether signal alone explains the refusals.** Turning `phy1-ap0` down
  until `phy2-ap0` is the stronger 5 GHz radio, then repeating the requests,
  would show it. The onboard radios honour transmit power live, so it is a
  ten-minute test.
- **What drives the streaks in section 2.** A channel scan alongside each
  trial would show whether a busy channel 1 explains the refusals.
- **How general any of this is.** Three Apple devices, one room, two days,
  and samples of 3–9 per cell. Android, Windows and older clients answer
  802.11v differently, some not at all.

## Related

- #498 / #527: the deauth that followed a gather or evict could kick a client
  off the radio it had just been sent to. Fixed: the pin now asks first and
  never sends anything to the destination.
- #516 / #517: a hotplugged radio did not advertise 802.11k/v.
- The 10 s gather/evict hold is too short for some clients, and the rack has
  no way to set a longer one. The API's `?pin=` can. A hold that keeps clients
  in place after they land is not planned.
- #525 / #526: the activity log worded `steer`, `warn`, `term` and `force`
  identically, so the hand session in section 5 could not be read back by
  button. Fixed: each "asked to move" line now names the button.
