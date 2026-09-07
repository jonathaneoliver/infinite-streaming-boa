# Product Requirements Document (PRD)

**Product:** infinite-streaming-boa ("boa") — per-client network link
conditioner on a transparent bridge

This describes the behaviour the product **has**, not aspirational scope.
Candidate work lives in GitHub issues. When a change alters user-facing
behaviour, it aligns with this document or updates it in the same PR.

## 1) Purpose & Vision

boa sits invisibly in an existing network and conditions each client's internet
connection independently — rate, latency, jitter and loss, per device and per
direction — adjustable live from a web interface.

It exists so that a player, app or device can be tested against a *specific*
network, on the device's own hardware and its own network stack, without
installing anything on it or changing how it connects.

The property everything else follows from is that **neither end has to
cooperate**. Because conditioning happens to forwarded frames at layer 2, the
device needs no proxy setting, no trusted certificate and no software, and the
far end needs nothing at all — there is no endpoint to point it at. Any client
talking to any server on any provider is conditioned alike, over any protocol,
including destinations that cannot be configured, instrumented or even
identified. The matching cost, following from the same position in the path:
traffic is told apart by destination network, port and protocol, never by
application or by name.

Intended for:

- Adaptive-bitrate player testing: watching a real client adapt to a real cap
- Reproducing field conditions (weak mobile, congested cell, satellite) on demand
- Testing devices that cannot be instrumented — TVs, consoles, set-top boxes
- Comparing several devices sharing one network
- Exercising the client's relationship with the access point itself — roaming,
  disconnection, an AP going away — and **validating the telemetry that reports
  those events**, by causing them on a schedule and diffing what was reported
  against what the box actually did

## 2) Goals

- **Invisible in the path.** Devices under test keep their own addresses, on the
  existing subnet, with discovery protocols intact. The box is not a hop.
- **Per client, not per interface.** Each device is conditioned independently;
  one device's policy never affects another's.
- **Accurate rate control, downlink first.** The primary use is throttling
  streaming video on its way to a player, so the delivered inter-packet spacing
  must be what was configured.
- **Honest measurement.** The interface reports what the kernel is doing, and
  states plainly where the numbers cannot be trusted.
- **Self-contained appliance.** Boots ready; no first-boot internet required.
- **Every radio present serves the AP.** With a USB adapter fitted alongside
  the onboard chip the box runs **dual-band, like a router**: the adapter on
  5 GHz, where its 80 MHz and 802.11ax are worth having, and the onboard chip on
  2.4 GHz, where its 20 MHz ceiling costs nothing it could have delivered.
  One SSID across both, on one bridged segment, so a client sees a single
  network and keeps its address when it moves between them. Either radio alone
  serves on its own. **Clients on every radio are conditioned** — the daemon
  follows a list of interfaces, not one. The AP runs in **Bridged AP mode**:
  bridged onto the upstream LAN with no separate subnet, DHCP, or NAT, so a
  Wi-Fi client is conditioned exactly like a wired one.
- **One credential, and it is the SSH key.** When a public key is configured the
  image disables SSH password authentication, keeping the account password for
  the console and for recovery, and grants the login account passwordless sudo.
  A shell on the box is already the whole box, so the boundary worth defending
  is getting that shell — not a second prompt for the same short secret, which
  only breaks the non-interactive deploy path.

## 3) Non-Goals

- A router, firewall or gateway. boa issues no addresses and performs no NAT.
- A production traffic shaper or QoS system. It exists to degrade links
  deliberately, not to manage them.
- Decrypting or inspecting application payloads. Conditioning is transport-level.
- Application-level fault injection or content manipulation. HTTP status codes,
  stalled or truncated responses, corrupted segments and rewritten manifests all
  sit above the transport. That work belongs on the origin path — the
  infinite-streamer harness in this family — which composes with boa rather than
  being duplicated by it.
- Certification-grade impairment. Profiles approximate real links; they are a
  place to start, not a standard.
- **Attenuation.** boa does not weaken the radio, and on this hardware it could
  not: setting transmit power is accepted by the driver and has no effect,
  measured across the adapter's entire legal range, and the PHY rate set cannot
  be clamped either — both radios refuse `iw ... set bitrates` outright, because
  rate selection happens in firmware. A weak signal is produced by distance or
  obstruction, not from the interface. What the box does instead is condition the
  link above the radio, impose real MAC-layer cost through the radio profiles —
  which change what the radio *is*, never how loudly it talks — and **model**
  what a weaker signal would do, which is the next bullet.

- **A device can be told to behave as though it were further away.** One
  control per device stands for a distance: it derives a rate, a delay, a jitter,
  a corruption rate and — only near the edge — correlated loss, from a modelled
  signal level, and drives the device with them. The curve is a **cliff rather
  than a slope**, because frame errors against signal are a sigmoid: a real walk
  is fine, fine, fine, then falls apart within a few steps, and a control that
  degrades evenly with distance is wrong in a way that is obvious to anyone who
  has done one. Corruption leads and loss follows late, because a weak signal
  damages frames that fail their checksum rather than dropping packets, and
  retries hide the loss until they are exhausted. The rungs it steps down are
  **the standard's own** — the receiver sensitivities 802.11 requires, and the
  data rates its OFDM parameters give — rather than a curve shaped to look
  right; what remains invented is how much impairment a given amount of headroom
  above the bottom rung implies.
- **The device is half of the link, so its kind is a control.** A watch does not
  hear what a laptop hears from the same access point at the same distance.
  Picking a device kind moves both directions, because antenna gain is
  reciprocal — the small antenna that transmits poorly also receives poorly —
  and moves the uplink further still, because a phone answers more quietly than
  a mains-powered access point asks. So the card shows **two** signal levels
  rather than one, names which way each points, and the uplink is always the
  weaker: that asymmetry is why a marginal link usually fails upward first.
- **A device on a cable can be told to behave like a device on a radio.** The
  model is Wi-Fi physics, and a wired client has no band for it to read — so one
  is chosen instead, either fixed or left on **auto**. On auto the model picks,
  at each distance, whichever band would actually deliver more throughput, which
  reproduces the thing a real walk ends with: 5 GHz while it is fast, 2.4 GHz
  once it is not. A client that *is* on a radio never gets this choice; there the
  band is a fact to be read, and letting it be overridden would let the interface
  disagree with the hardware.
- **The model is labelled as a model, and says what it cannot do.** It moves what
  a player senses; it cannot move what the radio reports. A device at a modelled
  40 m still shows its real signal strength and PHY rate, so the two disagree on
  screen — and the card says so, rather than leaving it to be discovered. The
  stored value is the signal level in dBm and never the impairments derived from
  it: one of them is the operator's intent and the other is a consequence, and
  keeping only the first means they can never drift apart. **Distance is a label,
  not the stored quantity** — it depends on a per-building path-loss guess, so a
  policy in metres would mean a different impairment in a different building —
  and the unit that label is drawn in follows the reader's own locale, so a US
  reader is shown feet without anything downstream knowing it.
- **The walk itself is a pattern, not just a position.** `walkabout` sweeps the
  model on a clock: out from beside the access point to the edge of range and
  back, with the band change driven at the crossing. It is the walk-away-and-back
  test that this box could not otherwise perform, and it differs from every other
  built-in pattern in that its keyframes are **computed rather than chosen** —
  they are the same arithmetic the distance control does, evaluated at a series
  of levels instead of one.
- **The walk ends where the model says the link ends**, not at a distance
  someone picked. It steps outward until one more step would take both
  directions past the point of holding, and stops there — so the far end is the
  worst the link can be while still alive. A hand-picked endpoint goes stale
  silently every time a constant behind the model moves, and each such move
  slides the cliff along while the endpoint stays put.
- **The way back is slower than the way out**, because recovery is not
  degradation reversed. Rate control drops on a few failed frames and climbs
  back only after sustained success, and a player has a drained buffer to refill
  before it risks a higher rendition. A symmetric walk reports a recovery that
  never happened.

## 4) Users & Use Cases

**Primary users:** video engineers, player developers, QA — and anyone
building a mobile app or a Wi-Fi connected device, or responsible for the QoE
telemetry that reports on one. For a device the case is stronger: its Wi-Fi
behaviour lives in firmware, whose fix has to travel over the very link that is
failing — so a rollout is slow, staged, and reaches least of the population
that needs it most. It also usually accepts no proxy, certificate or test
harness, which is the constraint this appliance is designed around.

**One engineer, one box.** boa is a bench instrument for a single operator, not
shared lab infrastructure. No port authenticates, there is no user model and no
tenancy, and all policy, pattern and radio state is global to the box — a second
person changing something changes it for whatever run is in progress. The
short-lived claim `scripts/deploy.sh` takes on the hardware prevents colliding
deploys and is a courtesy between colleagues, not access control.
Scaling to several testers means several boxes, which is why the target is a
cheap board and a reproducible image rather than a rack appliance.

### Conditioning the link

- Throttle one device to 3 Mbps and watch a player step down, and time it.
- Add 200 ms of latency to one device while others stay clean.
- Hold a device at a fixed rate for a long soak.
- Condition part of a device's traffic — a CDN, a port — leaving the rest clean.
- Read what a device is actually doing while it is being conditioned.

### Exercising the client's relationship with the network

The link is not a dial. A real client is continuously choosing which access
point to be on, whether to roam, and what to do when the one it is using stops
answering — and none of those decisions reproduce by lowering a rate limit.

- Move one device to a named radio, or force it there while denying the rest.
- Take an access point away, with or without announcing it first.
- Make a device re-associate, once or repeatedly.
- Walk a device away from the router and back, changing band as it goes.
- Ask what a device does when Wi-Fi is associated but useless — the
  captive-portal and cellular-failover paths.

### Validating QoE telemetry against ground truth

The case the radio control was built for, and the one that needs the box rather
than a script on the device.

QoE systems increasingly collect Wi-Fi data alongside playback events — signal
level, roams, disconnects — so a re-buffer can be attributed to the network
rather than to the CDN or the player, and so a badly installed device can be
recognised from behaviour such as constant roaming or repeated drops.

**That attribution is only as good as the telemetry, and the telemetry is
almost never tested**, because a real network will not produce a known sequence
of Wi-Fi events on demand.

- Script a run: steer at 30s, take the AP down for 8s at 90s, deauth at 150s,
  hold the device at a modelled 25 m from 210s.
- Capture the box's own event log for that run
  (`GET /api/events/stream`, NDJSON, millisecond timestamps).
- Diff the client's QoE report against it, in **both** directions: events the
  telemetry missed, and events it invented, mistimed or double-counted. The
  second kind matters more — it makes a healthy install look faulty.

The record has to come from the thing that *caused* the events, not from the
device experiencing them, because the device's own account is what is under
test.

## 5) System Overview

**Topology.** A transparent layer-2 bridge (`br-lan`) spans the WAN port, the
wireless AP (`wlan0`) and a USB ethernet port (`lan0`). Clients get addresses
from the **existing upstream router**. The bridge holds a management address by
DHCP plus a fixed rescue address, so the box is reachable without being a hop.

**Daemon** (`boad`) — a single Go binary with the Vue
interface embedded. Serves :80. Requires root: it configures queueing
disciplines and opens a packet socket.

**ntopng** — traffic analysis on :3000, watching `br-lan`. Deep links from each
device card. Optional: the image builds without it.

**glances** — the box's own health on :61208: CPU, memory, SoC temperature,
disk and per-process load, linked from the header. It answers a different
question from ntopng's, and the one that matters when a throughput figure looks
wrong for a reason that is not the policy — a thermally throttled Pi, a full
card, the daemon itself eating a core. It says nothing about clients. Optional
on the same terms: the image builds without it and the UI hides the link.

None of :80, :3000 or :61208 authenticates. The box is a bench appliance for a
network you already control, and anyone who can reach it can re-shape any
device on it — and, through glances, read its whole process list; put it on a
network where that is acceptable.

## 6) Behaviour

### 6.1 Discovery

- A device is a **client** only if its traffic arrives on a downstream port.
  Anything on the WAN port is upstream and is excluded.
- Addresses are learned **passively**, from ARP and from sampled forwarded
  traffic. Nothing is probed, scanned or injected.
- Presence comes from the radio and the bridge, never from a DHCP lease: a lease
  outlives the client that held it.
- For a **wireless** client the station table is authoritative. A device absent
  from it for more than a minute stops counting as present, whatever else still
  remembers it. It stays LISTED, keeping its policy and label, so nothing is
  lost while a device is away — only "present" changes. The minute of grace
  rides out a roam or a power-save blip rather than flapping the list.
- A client may hold several IPv6 addresses at once (privacy extensions); all are
  tracked and all are conditioned.
- Names are learned from **mDNS announcements and DHCP requests** the device
  makes anyway. The two see different devices: mDNS names whatever advertises a
  service, DHCP names whatever asks for an address, and the second is the set
  that would otherwise stay a bare MAC forever. DHCP sees only a lease being
  negotiated, never a renewal, so a device that settled before the box started
  stays anonymous until it rejoins. Both are
  keyed by **MAC**: a name is bound to the device that announced it, not to the
  address it announced on. A device is named even when it announces on an
  address this box has never otherwise seen — the common case on IPv6.
- Only announcements arriving on a **downstream port** are learned. The bridge
  hears the whole segment, but a device upstream is not a client of this box and
  its name is not recorded or stored.
- A name is taken only from a device announcing an address it is sending from.
  A name announced on another host's behalf is not attributed to the sender: a
  bare MAC is an honest label and a confidently wrong name is not.
- Randomised UUID hostnames are discarded — they are worse labels than a MAC.
- An operator-set label always wins over a learned name.

### 6.2 Policy

- Policy is keyed by **MAC**, so it survives DHCP renewal, reboots and a client
  roaming between the wireless and wired ports.
- Each device has a downlink and an uplink policy: rate, delay, jitter, loss,
  and — less often needed, so kept out of the way until used — reorder and
  corrupt. The interface shows the second group only when it is empty of values;
  anything in force keeps its control on screen.
- **Reorder requires a delay** to reorder against, and is refused without one.
  Packet **duplication is deliberately absent**: the kernel will not run a
  duplicating queue alongside any other, so one device using it would stop every
  other device on that port being conditioned — which this box cannot allow.
- `rate_mbps = 0` means unlimited. `delay_ms` is **per direction**; the round
  trip is the sum, and the interface shows that sum.
- A slider **applies when it is released**, not while it moves. The readout
  follows the handle throughout, so the control still reads as live, but the
  device is conditioned once, with the value that was chosen. Applying every
  value a drag passes over would impose a dozen caps nobody selected on a live
  client — each a real reconfiguration the device under test reacts to — which
  changes the run that the drag was setting up.
- **Loss runs to 100%, which is a blackhole** — the "drove into a tunnel" test.
  Its control is log-scaled for the same reason the rate control is: everything
  ordinarily interesting sits below 5%, and a linear track to 100 would bury it.
  A device can be blackholed from the interface it is serving and still be
  recovered, because the box's own management traffic is exempt from
  conditioning.
- Loss may be **uniform or bursty**. `loss_burst` is the mean length of a loss
  burst in packets; 1 is uniform — each packet independently, which is netem's
  default and essentially never happens on a real link. Above 1 the kernel runs
  a Gilbert-Elliott model and `loss_pct` becomes the **mean** loss over time
  rather than a per-packet probability. TCP and QUIC behave differently under
  bursty and uniform loss at the same nominal rate, so which one is in force is
  part of what a test result means.
- Burst length is in **packets**, because the model steps per packet and so
  remains defined at an unlimited rate. The interface derives the wall-clock
  equivalent from the configured rate, and says so rather than implying it.
- A burst length above 50 packets is refused. Beyond that the link is not lossy,
  it is out — and an outage belongs on a pattern, where it is visible and timed.
- Whether the kernel accepts the model is **probed at startup**, not assumed.
  Where it does not, the control is disabled and says why; loss is never
  silently downgraded to uniform, because a test run against the wrong loss
  model gives a confident wrong answer.
- **Loss does not repeat between runs, because boa does not ask it to.** Rate,
  delay and a pattern's schedule are deterministic; loss is a random process, and
  two runs of the same configuration lose different packets. Today it is
  reproducible only statistically, over enough packets.
  That is a choice, not a limitation of the kernel. netem takes a `seed`
  (iproute2 6.15, kernel 6.18 — `tc ... netem [ seed SEED ]`, and it reports the
  seed it used on every qdisc it installs). Setting one would make a lossy run
  repeat packet for packet, which for a box whose purpose is reproducing
  conditions on demand is worth having. It is not wired up yet.
- Sub-classes condition part of a device's traffic, matched by destination port,
  network and protocol, evaluated before the device default.
- Writes carry the revision the operator was looking at; a concurrent edit to
  the same device is refused rather than silently overwritten.

#### Patterns

A device may also hold a **pattern**: a timeline that drives its conditioning
instead of holding it still. A fixed cap only tests steady state, and what a
player does *through* a transition is the question this box exists to answer.

- A **keyframe** is the whole policy at one instant — both directions, all four
  parameters — at an absolute time. Values are absolute, not relative to
  anything the pattern did before.
- The device's own rate, delay, jitter and loss controls are the keyframe
  editor. Selecting a keyframe on the timeline points them at that moment;
  with none selected they edit the stored policy as usual.
- Keyframe times land on **half seconds**. Throughput is sampled once a second,
  so a transition finer than that can be configured but never observed.
- Between keyframes a value is **held**, and changes as a step at the next one.
  The stored format also carries an interpolated mode, which the editor does
  not yet offer: nothing has measured whether changing a netem rate mid-flight
  disturbs the queue, and a smooth ramp would otherwise be a picture of a link
  the box cannot prove it delivers.
- A pattern **loops** by restarting at its first keyframe. There is no wrap
  setting: a seamless loop is one whose last keyframe holds the same values as
  its first, which is visible on the timeline.
- Playing a pattern **overrides** stored policy and writes nothing, exactly as a
  sweep does. Stopping it, abandoning it or losing the daemon restores the
  operator's settings by simply forgetting the run. A one-shot pattern releases
  the device when it ends rather than holding its last keyframe.
- Playback runs in the **daemon**, not the browser: closing the page does not
  end a soak, and reloading it does not lose the playhead.
- Moving a control by hand during playback **pauses** the run and says so. The
  alternatives — overwriting the operator's value on the next tick, or leaving a
  pattern playing that no longer describes what is enforced — are both worse.
- A sweep and a pattern both drive the cap, so starting either is **refused**
  while the other is running on that device.
- The interface shows what is **enforced** during playback, not the stored
  policy: the cap line on the chart follows the timeline, and the controls
  report rather than accept input.

#### Wi-Fi link events

Everything above conditions a client's **packets** — the association stays up
throughout. boa can also condition the **link itself**, which is a different
thing a device reacts to: an iPhone's path monitor fires on a link drop, not on
5% loss, and a player resets its throughput estimate when the connection goes
down rather than when packets are merely late. netem cannot express this — it
damages packets, never link state.

- Four per-client events, keyed by **MAC**: **deauth**, the harder disconnect;
  **disassoc**, the softer one, usually a quicker recovery; **deadzone**, a held
  outage for a chosen duration — long enough to drain a player's buffer and
  force a rebuffer, which a single deauth is not. A deadzone denies the MAC for
  its length so the client cannot re-associate until it lifts, rather than a
  repeated deauth it could slip between. And **steer**, the only one that does
  not take the link away at all.
- **Every action is named for what it is, and the split is not a matter of
  taste.** An action that sends exactly one 802.11 frame takes that frame's
  name — `deauth`, `disassoc` — because the reader of this box already knows
  what those are, and inventing a word for them makes the interface harder to
  read for exactly the person it is for. An action this box composed keeps a
  plain word of its own — `deadzone`, `gather`, `evict` — because there is no
  frame it corresponds to and a standard-sounding name would imply one.
  The line falls exactly where the **deny ACL** does: everything that only sends
  a frame takes the standard's name, everything that also changes what the radio
  will accept keeps an invented one. These were once called *drop* and *nudge*
  in the interface while the same frames were called *deauth* and *disassoc* in
  the API and on the adapter lanes — one action under three names, so nothing
  said that a radio lane's deauth and a client's drop were the same thing.
- **A deadzone names how far it reaches**, because this box can serve two radios
  from one SSID. `current` denies on the radio the client is on and nothing
  else: measured, the client re-associates on the other radio in under a second,
  so it is a forced **roam** — useful precisely because, unlike a steer, the
  client cannot refuse it. `all` denies on every radio and is the **outage**
  above. A deadzone that cannot cover every radio is refused rather than
  half-applied, because one that reads as total and delivers a roam is worse
  than one that did not run.
- **Steer asks one client to move to the box's other radio** (802.11v BSS
  transition), naming it as the destination. It is a **request**: the link stays
  up, the client decides, and a device that ignores transition requests is a
  finding rather than a failure — which is the whole reason to have the button
  on a single device rather than only on a whole radio. The box resolves both
  radios itself, from the one the client is actually associated to, so a client
  on either band is steered the right way round. Offered **only when there is
  somewhere to send it**: on a box serving one radio a transition request has no
  destination to name, so the control is absent rather than present and failing.
- **The client's own answer is reported, in words.** A transition request is
  answered with a status code, and the log renders it — accepted, or the reason
  it was refused — rather than the number, which is a value nobody looks up.
  Whether a device honours a steer is the behaviour the control exists to
  measure, and a request whose result cannot be read measures nothing.
- **A transition request either takes no for an answer or does not, and which
  one is a property of the control that sent it.** The same 802.11v frame
  carries `disassoc_imminent` or does not:
  - a **steer** or a **gather** names a destination, so it must leave a refusing
    client where it is. Forcing one sends it wherever it likes while the
    interface claims it went where it was told.
  - an **evict** names no destination, so disassociating a client that will not
    leave is exactly what it says it does.
  The deadline is counted from the **request**, not from a refusal, and repeating
  the request restarts it rather than stacking — so pressing the button twice
  makes the wait longer, not shorter. It is expressed in beacon intervals on the
  wire, which is not seconds: a value passed as "30 seconds" was measured firing
  after 3.
- **Moving and answering are separate facts, and are reported separately.** A
  client can honour a transition and send no answer, or answer and not move;
  both were measured here on the same device within minutes. Nothing infers one
  from the other, because reading "did not move" as "refused" — or silence as
  "does not support transitions" — is a conclusion the evidence does not carry.
- **Silence is stated rather than left as a gap.** A client that has not
  answered within a few seconds is reported as not having answered, along with
  whether it moved anyway. A reader who sees a request and then nothing cannot
  otherwise tell a refusal from a request that went nowhere.
- They are driven from the device card as **one-shot** buttons, or scheduled on
  a **pattern lane** beside rate and loss — a deauth at t=120s is exactly
  reproducible, which no packet impairment is, and is the specific event this
  exists for.
- **Two of them move a client rather than breaking its link, and neither asks.**
  `pin` holds a device on a named band by denying it on every radio serving
  another; `evict` denies only the radio it is leaving, so where it goes next is
  its own choice. They are the pair the radio lane already has, one scope down,
  and they run the same mechanism over a single client.
- **An evict is not a deadzone, though both deny the radio a client is on.** A
  deadzone holds its ban for the full duration whatever the client does — that
  is what makes it an outage, and why its block has a width worth reading. An
  evict lifts the moment the client lands somewhere else, so its duration is a
  deadline rather than a dose: five seconds of deadzone costs five seconds of
  service, five seconds of evict usually costs a fraction of one.
- **Asking was tried and measured failing.** A transition request is a
  suggestion, and on this box an iPhone ignored a same-band one outright and
  then refused a cross-band one, offering its own candidate list. It was right
  to: the distance model does not move real signal strength, so its 5 GHz link
  was excellent and it had no reason to go anywhere. A modelled walk therefore
  cannot reach 2.4 GHz by asking, which is the same conclusion the radio
  controls reached — 802.11 has no request that places a station on a BSS.
- **A pin names a band, not a radio, and that makes it idempotent.** Already on
  that band means nothing happens. The distinction is not academic on a box
  serving two radios in one band: resolving "5 GHz" to a radio picks one of
  them, and comparing that to where the client is would move it sideways
  between two equally good radios on every lap of a looping walk.
- A **pin** is **generated rather than drawn** — it needs a destination band and
  the timeline has no way to ask for one — but it is shown wherever a pattern
  uses it, and can be deleted there. An **evict** names nothing, so it is drawn
  by hand like the other lanes.
- They require the **AP running through hostapd**, which is how both radios are
  now driven — the onboard one as well as a USB adapter — so the controls work
  whichever radio is serving. (They were USB-only while the onboard radio ran
  through NetworkManager, which exposes no control interface.)
- **This is the first time boa acts observably *on* a client.** The rest of the
  box is invisible to the device under test; a deauth is not. §6.1's "nothing is
  probed, scanned or injected" is scoped to discovery and still holds — link
  events are a deliberate, named exception, put on the record here so the
  contrast is not a surprise.
- **A long deadzone can evict rather than pause.** Past a few seconds of blackout
  a phone gives up on the AP and switches to another network (iOS around 3s). It
  is then off boa's Wi-Fi entirely: not shown offline, but gone — no traffic to
  see and nothing to condition until it rejoins this AP on its own. The interface
  warns once a deadzone is set long enough to risk it.

### 6.3 Enforcement

- Both directions are shaped on a **true egress queue**: downlink on the
  client's own port, uplink on the WAN port.
- **netem enforces the rate, not HTB.** A token bucket accumulates credit while
  idle and releases a burst at line rate when traffic resumes — exactly when a
  player starts a segment and measures throughput. HTB is a classifier and byte
  counter only.
- Both IPv4 and IPv6 are conditioned by one policy.
- The box's **management traffic** — the interface, SSH, ntopng, glances — is
  exempt, so
  it cannot throttle itself on a device it is conditioning. The exemption is
  scoped to those ports, not to the box as a whole: everything else the box
  sends is conditioned like any other traffic, which is what lets the box
  measure the downlink it is enforcing.
- The netem queue is sized from rate x delay. netem's 1000-packet default would
  silently discard traffic on high-delay profiles while reporting zero loss.
- Every discovered client gets a counting class even when unconditioned, so
  throughput is visible without setting a policy first.
- Stopping the daemon removes all conditioning.

### 6.4 Rendition ladders

- A **ladder** is the set of bitrates a player actually delivers. It is keyed by
  **(device, service)**, never by device alone: two streaming services share no
  rungs, so one ladder per device would have each measurement overwrite the last.
- The service is **named by the operator**, not detected. SNI is being removed by
  ECH, QUIC buries the handshake, and DoH removes the DNS — each would decay into
  silently mislabelling a ladder rather than into failing.
- A ladder is **measured by sweeping**: hold the device unconditioned to find the
  ceiling, then place each cap just under the last rung the player demonstrated,
  and record where throughput settles. Anchoring on the rung rather than stepping
  the cap uniformly forces a downshift every level, so the sweep visits each rung
  once instead of re-measuring rungs the player has not been pushed off.
- **A level waits for the client, not for a clock.** While a player is still on a
  rendition it can no longer afford it fetches continuously and stays pinned to
  the cap; the moment it drops, idle gaps appear. The sweep waits for that, then
  for the rate to steady, before measuring. A fixed wait cannot work: a real
  device took 40 seconds to let go and 15 more to settle, and measuring through
  the transition reports a confident rung that does not exist.
- **A client that never drops has reached its lowest rendition.** That level ends
  early rather than measuring the cap back.
- **The sweep only ever descends.** Re-visiting a rung means raising the cap, and
  a climb cannot be detected: a player that has not begun climbing looks exactly
  like one that has finished.
- The sweep drives the device's downlink cap for its duration and suspends the
  operator's delay, jitter and loss. Nothing is written while it runs, so an
  abandoned or crashed sweep restores stored policy by forgetting.
- **One sweep at a time.** Wi-Fi airtime is shared, so two at once measure each
  other.
- A sweep that is stopped, or whose device leaves, yields **no ladder**. It cannot
  know whether the ladder continues below where it stopped.
- Every ladder carries its **provenance** — measured or typed — and the interface
  renders them differently. Editing a rung by hand makes the ladder typed.
- Rungs measured from a window too noisy to be flat are marked **approximate**
  rather than dropped.

### 6.5 Interface

- The interface is **one scrolling view**, read top to bottom in the order the
  traffic travels: the fabric that reaches the internet, then the adapters that
  carry the air, then the devices on them. It was two tabs, and the split was
  wrong for the question actually being asked — "why is this device slow" is
  answered by facts on both sides of it, and a tab made comparing them an act of
  memory. Nothing here acts on more than one subject, so nothing needed
  separating; the controls that act on a whole radio live on that radio, and the
  controls that act on one device live on that device.
- **Adapters come before devices, in a fixed order that does not move.** The
  rack is `wlan-usb`, `wlan0`, `lan0` — the same order every load, regardless of
  which is serving, which has clients, or what just changed. An interface whose
  position encodes its current state cannot be pointed at across time, and a
  device roaming between radios must never reorder anything.
- **One token stands for an adapter everywhere it is named**: a colour swatch, the
  interface name, and its channel. The colour is fixed per interface and is
  drawn from a palette deliberately disjoint from the direction and status
  colours, so an adapter is never mistaken for a downlink or a fault. Where the
  token is not the subject it also offers a jump to wherever that subject is.
- Under each device's charts, an **ON ADAPTER strip** runs on the same x-axis,
  saying which radio carried each moment and marking where its channel changed.
  A roam is otherwise invisible in a throughput plot: the line simply gets worse,
  and the reason is off-screen in another component.
- The fabric — the WAN port and the bridge — is **one line**, with the topology
  diagram behind a disclosure. It is the part that is either working or not, and
  a picture of it is reference rather than something read every visit.
- The page header reports the same things throughout: transport, the WAN port,
  and which radio is serving with its negotiated bus speed. A USB adapter that
  quietly enumerated at High-Speed is invisible from every other angle, so it
  must not become invisible.

- State arrives as **complete snapshots** over server-sent events, with polling
  as an equivalent fallback. A dropped frame cannot cause drift.
- Charts hold up to one hour at 1 Hz, seeded from the server on load so a
  refresh does not start blank. The visible range is selectable (1m / 5m / 15m /
  1h) and applies to every device at once, because the reason to change it is
  comparison. Long ranges are averaged into buckets on the way out and the
  interface says so rather than implying raw resolution.
- The y-axis is chosen the same way for every chart: follow the data, lock to
  the configured cap, or a fixed ceiling the operator sets. Locking to the cap
  keeps the headroom between delivered and allowed a constant distance; a fixed
  ceiling makes two devices comparable. The axis is linear in all three: zero is
  a real and frequent reading here, and a log axis has no position for it.
- Where a fixed ceiling sits below the traffic the plot is marked as clipped,
  so a line resting on the top of the pane is never mistaken for a plateau.
- Both settings persist, and the plot's right-hand edge stops advancing while a
  chart is being read, so the point under the pointer stays the point measured.
- Each direction is drawn as two series: the **live** trace, and a **sustained**
  line — the bytes delivered over the trailing 30 seconds divided by that time.
  An adaptive player fetches a segment then idles, so the live trace is a square
  wave between roughly the cap and zero and neither extreme answers "what is
  this device getting". A legend names both and switches either off; the choice
  persists. Both are the same colour, because colour means direction here —
  they are told apart by weight.
- The sustained line is withheld rather than guessed: nothing is drawn across a
  gap in the record, nothing until the window is at least half full, and nothing
  once the server's own averaging approaches the window, where it would be a
  mean of means.
- Device cards fold when there is more than one device, keeping a sparkline and
  current figure per direction on the fold title. Folding is presentation only;
  a folded card stays live. The fold sparkline shows the live trace only —
  its job is shape in a couple of centimetres.
- Downlink is blue and uplink orange, consistently, everywhere. Direction is the
  most confusable property in a bidirectional conditioner.
- The interface states its own limits: that Wi-Fi airtime is shared and
  conditioning is additive on top of it; that `overlimits` is not an error; that
  PHY rate is not throughput.

- An **activity log** sits under the page header, showing the newest few
  lines until it is opened. One line says that something changed without saying
  what; a roam alone is two lines. It records what CHANGED rather than what is: a client joining,
  leaving, or moving between radios; a radio's channel, width or mode changing,
  whether this box did it or not; and every box-wide action taken here. State
  answers "what is true now" and is silent about "what just happened", which on
  a two-radio box is the more interesting question -- a device that moved to the
  other band is simply on the other band, with nothing saying it moved.
- **A refused steer says where the client asked to go instead.** A client that
  declines a transition request may answer "candidate list provided" and name
  the BSS it would rather have, with a preference from 1 to 255. The box reads
  that list out of the response frame itself and puts it on the same line as the
  refusal, naming one of its own radios where the candidate is one, so a refusal
  reads as a preference rather than a dead end. Measured on this hardware, a
  client refusing a move to one 5GHz radio asked for the 2.4GHz one at the
  maximum preference: it was not declining to move, it was choosing a band.
- **A client that leaves of its own accord says why.** Its deauthentication or
  disassociation carries a reason code, and that is the only thing separating a
  device that roamed away from one that timed out from one that gave up on this
  network — all three simply stop appearing. What the box itself sends is not
  reported this way and cannot be: an access point sees the frames it receives,
  not the ones it transmits.
- **Everything else a client says is available behind a switch in the log's own
  bar.** Capability elements on every association, and frame types nothing acts
  on, are context rather than events: they repeat on every join and would bury
  what the log is for, so they are off by default and the log is unchanged when
  they are. The switch is in the activity bar because that is where the question
  is asked, and it takes effect immediately — the moment anyone wants this is
  while watching a device misbehave, and a setting needing a restart is one that
  gets turned on for the run that has already finished.
- **That switch changes what the box RECORDS, not what one reader sees.** It is
  shared, and another browser sees it move. This is deliberate: the log keeps a
  fixed number of lines, so recording everything and filtering per reader would
  let capability chatter push the refusals and disconnect reasons out of the
  history before anyone came looking for them. Turning it on and off is itself
  recorded, because otherwise lines ceasing to appear is indistinguishable from
  a box that went quiet.
- **A radio that stops serving is reported, and so is its recovery.** Whether a
  radio is powered is not the same question as whether it has a working access
  point on it, and the second one is what decides if anybody can connect: a
  disabled BSS still reports the channel it will use when it returns, so a radio
  serving nobody otherwise looks identical to one serving happily. Both edges
  are logged — stopped serving, and serving again — whatever the cause. A
  warning is therefore never left standing over a radio that has since
  recovered, which is the state that misleads during a test. A radio
  deliberately switched **off** is not reported as a fault: not serving is the
  correct state for a radio that is off, and the power control has already said
  what it did.
- **An access point that does not survive a power cut is rebuilt, unasked.** A
  radio can come back from an outage with its driver reset underneath hostapd,
  which leaves hostapd asserting a BSS that is not on the air: every status
  source calls the radio healthy and no client can join it, indefinitely. The
  box detects that by the one thing that separates it from a healthy recovery --
  hostapd refusing to enable an interface that had just failed to look enabled
  -- and tears the access point down and builds it again, which is the only
  measured remedy. It says that it is doing so, and says whether it worked.
  Clients on that radio are dropped by the rebuild; they have just been dropped
  by the outage anyway, and the alternative is a radio nobody can join.
- **"Not answering" is never reported as "not serving".** A radio's control
  interface can go silent for minutes while its driver re-initialises, and a
  question that could not be asked has no answer. The box says that it cannot
  yet confirm the access point rather than asserting it is serving nobody,
  because a confident wrong answer about a radio is what sends an operator
  hunting a hardware fault that is not there.
- The log is **in memory and lossy by design**: a few hundred events, cleared by
  a restart or a deploy. An association event per client per roam, persisted, is
  exactly the steady write that wears an SD card out, and every event still
  worth having rebuilds itself within seconds.

### 6.6 The bridge

- The rack shows **every interface the box has**, discovered from the kernel
  rather than read from configuration: the WAN port, the bridge, each radio and
  the wired downstream port, with MAC, addresses, link state and — for a radio —
  its adapter, driver, negotiated bus speed, and the SSID, channel, width, mode
  and country its access point is actually running. A diagram draws the same
  thing as a topology.
- **An adapter collapses to one line, and that line is a table row.** Every field
  describing the radio sits in a fixed column — the token and its channel, width
  and mode, then the devices on it — so the rack is read down as
  columns rather than found again on each row. The devices are **named, not
  counted**: they are the blast radius of every control to the right of them, and
  a name says that where a tally does not. The list is capped and carries a
  leading count so an abbreviated one still says how many it stands for, and each
  name jumps to that device's card below.
- The fold opens on the **facts first, then the controls, and the band plan
  leads the controls** — opening an adapter is nearly always to change where it
  is, and the band plan is the one control here that is read rather than
  pressed.
- **A radio the daemon is not watching is named as such, prominently.** The
  daemon follows one interface, so a second adapter's clients associate, take
  addresses and pass traffic while being conditioned by nothing and appearing
  nowhere in the device list. Discovering the hardware rather than the
  configuration is what lets the interface say this instead of leaving it to be
  inferred from a device list that is quietly short.
- The rack offers **box-wide radio controls**, chiefly a broadcast
  deauthentication. Each states on screen that it affects **every client on
  that radio**, and how many that currently is. It also warns that a client
  using a private Wi-Fi address may reassociate under a different MAC and so
  return as a new device with no policy.
  This is a deliberate exception to the per-device independence of §6.2, taken
  because the box is a single-operator instrument and because these impairments
  are unreachable any other way — not a general licence for AP-wide controls to
  appear beside per-device ones.
- **A radio can be emptied or filled, and the two make opposite promises.**
  `gather` fills this radio; `evict` empties it. They were once the same 802.11v
  request read in two directions, and that was wrong: a transition request is a
  *suggestion*, so a client that refused — or that was disassociated anyway and
  rescanned — chose for itself, and "gather to wlan-usb" was measured putting a
  device on wlan0 instead. A control that names a destination cannot keep its
  word by asking, because **802.11 has no request that places a station on a
  BSS**.
  So neither asks. Each removes the alternatives, through the runtime deny ACL:
  - **gather** denies the client on every serving radio **except** the
    destination, then moves it off the one it is on. Its own rescan finds
    exactly one access point here it may join. The promise is "it comes here",
    and it is kept.
  - **evict** denies the client only on the radio being emptied. It keeps every
    choice except coming back. The promise is "it leaves" — where it goes is
    explicitly not this box's decision, and the control says so rather than
    implying otherwise.
  Each is disabled when its source radio has nobody on it, so a dead button
  always means "there is nobody to move" and never "this is not supported".
  An evict off the **only** serving radio is refused: that would put its clients
  off the box altogether, which is a whole-network outage and not what the
  button offers.
- **The bans are timed, lift on arrival, and lift together.** A deny list is a
  real outage for that client on those radios, so it is held only for as long as
  the decision it exists to constrain. It is released the moment the clients
  land, and after five seconds regardless — measured, holding one longer is how
  a device is pushed off the network entirely, because its own association
  backoff takes over.
  **Together**, not per client: an operation is not finished until every client
  it covers has moved, and releasing the first arrival early would free it to
  wander back into radios the others are still being held out of.
- **A new movement command supersedes the last one, over the clients it
  covers.** A box-wide gather or evict clears every ban in force before placing
  its own; a per-client one clears only the claims on that client. The release
  itself is not optional either way — a client is held by one operation at a
  time, so re-claiming it without releasing first would strand the old
  operation's bans with nothing left to lift them. But widening that release to
  the whole box at per-client scope would let two devices running walkabouts
  cancel each other at every band change, leaving the first free to roam off the
  band its own pattern is still conditioning for — which is the per-device
  independence of §6.2 broken silently. Two overlapping operations would
  otherwise deny a client *everywhere* — the first holding it at A by denying B
  and C, the second holding it at B by denying A and C — leaving it unable to
  associate at all, with each deny list looking individually reasonable. Pressing
  gather again means "now do this instead", never "do both".
- **Removing the choice is not measuring the choice, and the interface says
  which is which.** A pinned gather cannot tell you whether a device honours a
  transition request: it was never asked. The per-client **steer** on the
  Clients tab remains the control for that question, and the two are described
  in their own words rather than one borrowing the other's.
- **An access point can be taken down and brought back, and both ends can be
  announced.** `disable` closes the BSS; `enable` reopens it. On its own, either
  is silent — a closed BSS tells nobody, and clients discover it by timing out,
  which is a slower and less legible outcome than a device being told.
  So each has a **deauth +** form, and the audience differs at each end:
  - going **down**, the audience is the stations currently associated, addressed
    individually before the BSS closes;
  - coming **up**, it is clients that still *believe* they are associated and
    are not — exactly the population a silent outage creates — and the only
    thing an access point can do for them is broadcast.
  Both send a **deauthentication**, and the control is named for that rather
  than for the intention behind it — it was "tell", which was friendly and hid
  which frame went out, the same fault *drop* had for a deauthentication. An
  earlier version sent a disassociation on the way down and a broadcast
  deauthentication on the way up, which made the two halves of one control
  disagree about which frame they meant. Under WPA2 both force a full reconnect,
  so matching them costs nothing and buys one answer instead of two.
- **The box does not announce a radio starting or stopping unless asked.**
  hostapd broadcasts a deauthentication at both ends by default; that frame
  lands on exactly the clients a measurement is watching, so it is switched off
  globally and re-enabled only for the deliberate `deauth +` above. A silent outage
  is a real field condition — a router losing power tells nobody — and it must
  be reproducible on demand rather than drowned by the box being polite.
- **Each box-wide control appears exactly once, on the adapter it acts on.**
  Everything that acts on a radio — switching it off, dropping, nudging or
  steering its clients, scanning, moving it, taking it away for a fixed outage,
  and the link-conditioning profiles and thresholds — is on that radio's own row
  or in its fold, and nowhere else. The topology diagram is a picture only: it
  once carried these controls too, and two copies of one action is a second
  place for the two to disagree. The copy further from the thing it names is the
  one to lose.
- **A radio switched off stays off until the operator switches it back on.**
  Radio power is a test instrument here — the measurement IS "switch it off and
  watch what the client does" — so an outage that ends itself early does not
  merely surprise, it invalidates the run. Neither a radio hotplug nor the
  daemon restart every deploy performs undoes the decision. A **reboot** does:
  being off is bench state for the run in hand, not configuration, and a box
  power-cycled back into service comes up serving on every radio it has.
- A **fixed-length outage** is protected the same way and for the same reason: a
  hotplug landing in the middle of one does not cut it short. What still ends it
  is the box losing the process that owns it — a cut that outlives the daemon
  that made it is not an impairment but a broken appliance, so a radio found off
  at startup with no standing decision behind it is switched back on.
- **Where the box does put a radio back on anyway, it says so.** A USB adapter
  that re-enumerates returns as a fresh device with its transmitter enabled, and
  no record the box keeps can stop the kernel handing it back that way. The
  activity log names the radio and says that anything measured across that point
  was not measured through an outage — the alternative is a result that is
  quietly wrong and cannot be questioned afterwards.
- Where a radio exposes no hostapd control socket, its controls are **shown
  disabled with the reason**, never offered and silently ignored. Where a radio
  refuses an action its driver claims to support — an 802.11h channel switch on
  the `mt7921u` is the measured case — the refusal is reported with the
  driver's own words rather than reported as success.
- **An open adapter shows what it is carrying, stacked by device.** Download and
  upload as two charts side by side, on the same window and the same axis rules
  as the client charts below, with one coloured band per device and the top edge
  as the adapter's total. This answers the question that sits between the rack
  and the device list: a stream that halved because the radio halved looks
  identical, on the device's own card, to one that halved by itself.
  Colour here means **device**, which is the one place in this interface it does
  not mean direction. That is only safe because the two directions are separate
  charts named in their headings rather than one chart drawn in two colours, and
  because every band is named in a legend — colour is a key into that legend and
  never the only thing carrying the meaning.
  The axis is floored at 1 Mbit/s rather than scaled to whatever is there: an
  idle radio otherwise scales to a trickle of ARP and mDNS chatter and draws
  background noise as a full-height mountain range.
- **Below those, the same stack in airtime: what each device COST the radio.**
  One chart rather than a pair, because the radio's time is a single resource
  and transmit and receive both spend it, on the same window, the same device
  colours and the same legend as the throughput above — so a spike in airtime
  and the throughput that did or did not accompany it line up vertically.
  This is the half throughput cannot show. Measured 2026-09-07, one client held
  46% of a radio to move 34 Mbit/s while another held 77% to move 505 — six
  times the airtime per bit, because small unaggregated frames pay preamble,
  IFS and ACK that a full frame aggregate amortises. On the throughput chart the
  expensive device merely looks quiet. Read together the two give efficiency,
  which is what says whether a device is slow or is costing everyone else the
  radio.
  The axis is **fixed at 0–100%**, deliberately unlike every other chart here.
  Airtime has a real ceiling — the radio's whole time — and "how full is this
  radio" is the only question the chart is for, which a scaled axis would answer
  identically at 8% and at 80%.
  It is an **occupancy, never a share**: the bands do not sum to 100 and the gap
  to the top is **not free capacity**. It is beacons, management frames,
  multicast and every neighbour on the channel, none of which this box can
  measure, so nothing is drawn there.
  **A radio whose driver cannot attribute airtime says so in words** rather than
  drawing an empty chart. The onboard `brcmfmac` chip reports no per-station
  airtime at all, and rendering its clients at 0% would show a possibly
  saturated radio as idle — the same absent-versus-zero rule the airtime and
  survey readouts already follow. A radio nobody has yet been associated to is
  not accused of the same thing; the question is left open until a client is
  there to answer it.
- **What an adapter carried outlives what is attached to it.** The chart's
  contents come from the recorded history, which names the adapter that carried
  each sample, and never from the list of currently-attached devices. A device
  that leaves keeps its band until its samples age out of the window, and an
  adapter emptied by a steer still shows what it was doing a moment ago —
  which is the moment the question is most often asked. The row above it still
  reports who is on it *now*: "what it carried" and "who is on it" are
  different questions and are answered separately.
- **Time advances whether or not data is arriving.** The right-hand edge of
  every plot follows the clock, so a radio that goes quiet drains to the left
  and empties rather than freezing on the last thing it did — a chart that
  stopped telling the time would be most misleading exactly when "nothing is
  happening" is the finding. The edge is held still while a chart is being
  read, so the point under the pointer stays the point measured.
- **The channel-busy airtime readout has been withdrawn, because it was wrong.**
  It came from `iw dev <if> survey dump`, and on the mt7921u radios that counter
  reads roughly **5x low**: measured 2026-09-07 it reported 4.97% busy over a
  window in which two stations accounted for 23.24% between them, and 8.2% while
  a single station held 39.7%. It is gone rather than caveated, because a figure
  on screen gets believed and this one sat beside a per-client airtime chart
  saying something five times larger. Airtime is now reported **per client**,
  from the station counters, which were verified against iperf3 — see
  DATA-CONTRACT Source T, and Source L for what the survey counter does say.
- A radio is moved by **picking a cell from its band plan**, not from two
  independent dropdowns — a cell is a channel and a width together, which is
  the choice that actually exists. Only channels the box will accept are drawn:
  2.4GHz 1/6/11, and the non-DFS 5GHz channels 36/40/44/48 and
  149/153/157/161/165. DFS is excluded because neither radio can serve an
  access point on one.
  **Where the radio is now is filled in the accent colour**, the same treatment
  a chosen profile gets, so "you are here" reads the same way everywhere in the
  interface rather than being inferred from a slightly different shade.
  **A block another of the box's own radios occupies is hatched and refused**,
  naming that radio and saying to move it first. Two of our access points on one
  channel split it between themselves and gain nothing while the rest of the band
  sits empty, and a cell that is merely unclickable reads as a bug rather than as
  an answer.
- **Each radio's row carries what the air around it is doing**: the best PHY rate
  its clients have negotiated, our own access point and the loudest neighbour in
  dBm, our own airtime over the last five seconds, and what neighbouring access
  points report for the channel.
  The last two answer different questions and only one of them is ours. **Our
  airtime** comes from the station counters, is summed across the radio's
  clients, and is the same total the stacked chart draws. **Others** is what
  neighbours advertise about the whole channel, shown as a **range with a
  reporter count** because they disagree — five access points on one channel
  reported 19% to 33% of the same medium from different rooms — and because a
  single distant reporter is not the same evidence as four that agree. It is
  weakest exactly where a channel is quiet, since quiet means nobody is near
  enough to ask, and an absent figure is drawn as absent rather than as zero.
  Both are averaged over five seconds, which is what a neighbour's own figure
  already is, so the two describe the same span.
- **The figures are refreshed by scanning, and a background scan may never cost
  an outage.** One radio here scans both bands while it keeps serving; the others
  refuse to scan while beaconing and can only do it with their access point
  taken down. So the box learns which of its radios is the cheap one by asking,
  records the answer, and thereafter polls only that one — a refusal costs
  nothing and is a complete answer. One such scan describes every radio's
  channel, so no radio is ever taken off the air to find out how busy its own
  channel is.
- **A chosen channel is remembered, and a radio is put back on it.** The move
  itself is applied to the running access point and lasts only as long as that
  process, so a restart, a reboot, a USB re-enumeration or a driver reload
  would otherwise return the radio to whatever the image was built with,
  silently. The choice is kept as box state rather than written into the access
  point's own configuration, which has no single file it belongs in: those
  configurations are per ROLE, two of them describe the same onboard radio in
  different bands, and which one is live depends on whether the USB adapter is
  present.
- Putting it back **costs an outage and says so before it takes one**. The move
  drops every client on that radio without telling them, so the log names the
  channel it drifted to and the one it is going back to, before it moves. It is
  attempted only on a radio that is actually serving — a radio deliberately
  switched off stays off, because being off is the operator's decision and not
  something a restore may quietly undo. After a few failed attempts it stops
  and says it has stopped: a channel that will not stick is a cause the box
  cannot fix, and retrying it would be an outage every second in pursuit of it.
- Where the coexistence scan lands a radio on the sibling of the channel asked
  for, **that is remembered as satisfying the choice**, not as a drift to be
  corrected. Otherwise the one normal case on this box would be a permanent
  mismatch, and correcting it forever would be worse than the mismatch.
- **One radio reconfigures at a time.** A channel move is a read-modify-write
  on hardware taking seconds, so two at once interleave and each reads back the
  other's result. Two radios still move in parallel; one radio does not move
  twice.
- The two 5GHz blocks are drawn **with a break between them**, because they are
  not adjacent: the whole DFS range sits in the gap. Offering both is what lets
  two 5GHz radios sit somewhere they are not inside each other's spectrum.
  Widths a channel cannot do are **not offered** rather than silently narrowed —
  165 has no channel above it that may be radiated on, so it appears at 20MHz
  only. Where an automatic move must narrow a radio to fit the channel it chose,
  it says so.
- **A channel's colour is a measurement where one exists.** Most neighbours
  advertise BSS Load — the airtime they observe busy, and their client count —
  and that is what decides clear/busy/crowded. A headcount is the fallback for
  channels where nobody advertised it, and the interface says which of the two
  it is quoting: a colour resting on evidence and one resting on a guess must
  not look identical. A channel nobody measured is never treated as idle, which
  would paint the busiest one green.
- **A neighbour is counted on every channel it occupies, not just the one it
  beacons on.** An access point at 80MHz fills four 20MHz channels and competes
  across all of them, so a channel with no access point primary on it can still
  be fully occupied — the common case on 5GHz, and invisible to a headcount.
  The primary is kept as a separate figure, because that is where the beacons
  and management traffic actually are.
- **What a scan found is recorded in the activity log**, not just what it
  recommended: how many access points and clients were heard, the busiest and
  quietest channels with their measured airtime, and how many channels carried a
  measurement at all. A recommendation that cannot be checked against the
  evidence behind it is one that has to be taken on trust.

### 6.7 Measuring

- An **iperf3 server** runs on the box, so a device can be measured without a
  second host and without installing anything but a client. The interface shows
  the command, addressed to whatever host the interface itself was reached on.
- **Downlink is measured against the policy.** Traffic from the box to a client
  is conditioned like any other, so the reverse-direction test reports the cap
  as it is actually enforced.
- **Uplink is not, and cannot be, measured this way.** A client's upload to the
  box terminates at the bridge and never reaches the WAN port where uplink
  shaping lives. That direction reports what the link can do, not what the
  policy allows, and the interface says so where it offers the command.
  Verifying uplink needs load from a host beyond the WAN port.
- A test is bound to the address the client is measured on. A device attached by
  both Wi-Fi and cable is two paths, and only the one carrying the client's own
  address is conditioned by that client's policy.

## 7) Constraints & Accepted Limitations

- **Clients depend on upstream DHCP.** Being invisible means issuing no
  addresses; with no live WAN port, clients associate and get nothing.
- **Wi-Fi airtime is shared.** Conditioning is additive on top of a variable
  radio baseline, not absolute.
- **No per-station signal level.** The Pi 5's radio reports none in AP mode;
  transmit failures stand in.
- **Client-to-client traffic is not conditioned** on the uplink path, as it
  never crosses the WAN port.
- **A shared budget across media is not expressible** while downlink is shaped
  per client port. See the open decision in the issues.
- **The bridge is indistinguishable from a rogue AP to enterprise network
  security.** Many MACs on one switch port is what port security, BPDU guard,
  DHCP snooping and wireless IDS exist to catch, and boa depends on upstream
  DHCP crossing the bridge — which snooping is built to block. Wireless IPS
  containment additionally transmits deauthentication frames at the clients,
  producing the same impairment the box produces deliberately and making a run
  conducted there unmeasurable rather than merely inadvisable. The WAN port
  belongs on a network the operator controls.
- **The Wi-Fi passphrase is the only perimeter.** The AP bridges onto the
  existing LAN and the management interfaces do not authenticate (§5), so
  `AP_PASSWORD` alone gates both access to the network and the ability to
  black-hole any device on it. A weak WPA2 passphrase is crackable offline from
  one handshake; use a strong one.
- **Encrypted payloads stay encrypted.** Manifest-level inspection needs a proxy
  that is the origin path.
- **A measured ladder is the effective one, not the manifest's.** A rendition the
  player never selects — wrong codec, wrong viewport, skipped by its own logic —
  is never delivered and so never appears. Two devices can produce different
  ladders from identical content.
- **The netem queue is deep at low caps, but segmented traffic does not fill
  it.** `netemLimit` floors the queue at 1000 packets: 0.24 s of buffering at
  50 Mbps, but 48 s at 0.25 Mbps. A single bulk transfer large enough to fill it
  does stall — 13 s observed — but that requires putting minutes of data in
  flight, which no player does. Fetching the variant a player would actually
  choose at that cap (190 KB at 0.25 Mbps, not 19 MB) the worst gap is 0.66 s
  and pacing is even.
- **Rung resolution is a merge tolerance.** Two renditions closer together than
  the larger of 250 kbps and 10% of the rate cannot be told apart and are
  reported as one. Real ladders are never spaced tighter than about 25%, which
  is the margin that makes this safe.
- **A rung is a window mean, over several segments.** Fetches arrive in bursts
  with idle gaps, so no individual sample is ever a rendition rate — only the
  mean over whole segment periods is. A window spanning too few segments reads
  the burst pattern rather than the stream.
- **A rung is a wire rate, not a media bitrate.** It counts what the kernel
  counts, framing included, because that is what a cap limits — so it is the
  right unit for setting one. Against a manifest's `AVERAGE-BANDWIDTH` expect
  about +4.6% on IPv4 and +6% on IPv6, plus retransmissions.

## 8) Success Criteria

- A configured cap is delivered within the framing overhead of a real link of
  that speed. Measured −4.5% at caps from 1.5 to 50 Mbps. Separately measured at
  **0.25, 0.5, 1, 2 and 4 Mbps** downlink over Wi-Fi, from both the kernel's own
  counters and an independent client: the counters read the configured rate
  exactly, and the client sees 0.94–0.95 of it, which is the Ethernet, IP and
  TCP framing the cap counts and a client's payload does not.

  Four runs per rate with the radio otherwise quiet: 0.943 at 0.25 Mbps, 0.947
  at 0.5, 0.949 at 1, 0.951 at 2, 0.952 at 4, each repeating to within 0.006 or better and the
  best to 0.001. The figure rises monotonically with rate because the per-request
  round trip costs relatively less as more bytes move between requests, so it
  converges on the framing limit rather than drifting.

  0.25 Mbps sits at 0.943, measured over fifteen further runs at two window
  lengths — 0.943 ± 0.003 over 20 s and 0.943 ± 0.001 over 60 s, with no stall
  in any of them. An earlier single run of 0.767 did not reproduce and is taken
  as a transient rather than a property of the rate.

  **Uplink is untested at any rate.**
- A configured one-way delay appears as that delay in round-trip time —
  measured 200.6 ms for a 200 ms setting.
- A device under test cannot tell the box is present: no extra hop, no address
  change, discovery unaffected.
- The interface never claims a policy is applied when it is not.
