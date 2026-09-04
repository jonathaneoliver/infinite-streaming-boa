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

## 4) Users & Use Cases

**Primary users:** video engineers, player developers, QA.

- Throttle one device to 3 Mbps and watch a player step down, and time it.
- Add 200 ms of latency to one device while others stay clean.
- Hold a device at a fixed rate for a long soak.
- Condition part of a device's traffic — a CDN, a port — leaving the rest clean.
- Read what a device is actually doing while it is being conditioned.

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

Neither :80 nor :3000 authenticates. The box is a bench appliance for a network
you already control, and anyone who can reach it can re-shape any device on it;
put it on a network where that is acceptable.

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

- Three per-client events, keyed by **MAC**: **drop** (deauthenticate), the
  harder disconnect; **nudge** (disassociate), the softer one, usually a quicker
  recovery; and **deadzone**, a held outage for a chosen duration — long enough
  to drain a player's buffer and force a rebuffer, which a single drop is not. A
  deadzone denies the MAC for its length so the client cannot re-associate until
  it lifts, rather than a repeated deauth it could slip between.
- They are driven from the device card as **one-shot** buttons, or scheduled on
  a **pattern lane** beside rate and loss — a deauth at t=120s is exactly
  reproducible, which no packet impairment is, and is the specific event this
  exists for.
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
- The box's **management traffic** — the interface, SSH, ntopng — is exempt, so
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

- The interface has **two tabs**. **Clients** is the device list and everything
  that conditions one device; **Bridge** is the box itself. They are separate
  because the controls differ in kind, not merely in subject: everything on a
  device card acts on one device, and everything on the Bridge tab acts on a
  whole radio at once. Clients is the default — the devices are what the box is
  for, and the bridge is what you go and look at.
- The page header is shared by both and reports the same things throughout:
  transport, the WAN port, and which radio is serving with its negotiated bus
  speed. A USB adapter that quietly enumerated at High-Speed is invisible from
  every other angle, so it must not become invisible from whichever tab is open.

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

- An **activity log** sits under the tabs on both views, showing the newest few
  lines until it is opened. One line says that something changed without saying
  what; a roam alone is two lines. It records what CHANGED rather than what is: a client joining,
  leaving, or moving between radios; a radio's channel, width or mode changing,
  whether this box did it or not; and every box-wide action taken here. State
  answers "what is true now" and is silent about "what just happened", which on
  a two-radio box is the more interesting question -- a device that moved to the
  other band is simply on the other band, with nothing saying it moved.
- The log is **in memory and lossy by design**: a few hundred events, cleared by
  a restart or a deploy. An association event per client per roam, persisted, is
  exactly the steady write that wears an SD card out, and every event still
  worth having rebuilds itself within seconds.

### 6.6 The bridge

- The Bridge tab shows **every interface the box has**, discovered from the
  kernel rather than read from configuration: the WAN port, the bridge, each
  radio and the wired downstream port, with MAC, addresses, link state and — for
  a radio — its adapter, driver, negotiated bus speed, and the SSID, channel,
  width, mode and country its access point is actually running. A diagram draws
  the same thing as a topology.
- **A radio the daemon is not watching is named as such, prominently.** The
  daemon follows one interface, so a second adapter's clients associate, take
  addresses and pass traffic while being conditioned by nothing and appearing
  nowhere in the Clients tab. Discovering the hardware rather than the
  configuration is what lets the interface say this instead of leaving it to be
  inferred from a device list that is quietly short.
- The tab offers **box-wide radio controls**: a broadcast deauthentication and
  an airtime readout. Each states on screen that it affects **every client on
  that radio**, and how many that currently is. It also warns that a client
  using a private Wi-Fi address may reassociate under a different MAC and so
  return as a new device with no policy.
  This is a deliberate exception to the per-device independence of §6.2, taken
  because the box is a single-operator instrument and because these impairments
  are unreachable any other way — not a general licence for AP-wide controls to
  appear beside per-device ones.
- Where a radio exposes no hostapd control socket, its controls are **shown
  disabled with the reason**, never offered and silently ignored. Where a radio
  refuses an action its driver claims to support — an 802.11h channel switch on
  the `mt7921u` is the measured case — the refusal is reported with the
  driver's own words rather than reported as success.
- The airtime readout is labelled as the **operating channel only**. It is not a
  survey of the band: a beaconing radio never visits other channels, so their
  counters are zero, and one driver measured here mislabels the frequency
  outright. Choosing a quieter channel needs a radio that is not serving, and
  that is left unbuilt rather than approximated.
- A radio is moved by **picking a cell from its band plan**, not from two
  independent dropdowns — a cell is a channel and a width together, which is
  the choice that actually exists. Only channels the box will accept are drawn:
  2.4GHz 1/6/11, and the non-DFS 5GHz channels 36/40/44/48 and
  149/153/157/161/165. DFS is excluded because neither radio can serve an
  access point on one.
- The two 5GHz blocks are drawn **with a break between them**, because they are
  not adjacent: the whole DFS range sits in the gap. Offering both is what lets
  two 5GHz radios sit somewhere they are not inside each other's spectrum.
  Widths a channel cannot do are **not offered** rather than silently narrowed —
  165 has no channel above it that may be radiated on, so it appears at 20MHz
  only. Where an automatic move must narrow a radio to fit the channel it chose,
  it says so.

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
