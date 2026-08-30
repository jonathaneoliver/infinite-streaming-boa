# Product Requirements Document (PRD)

**Product:** infinite-streaming-pifi ("pifi") — per-client network link
conditioner on a transparent bridge

This describes the behaviour the product **has**, not aspirational scope.
Candidate work lives in GitHub issues. When a change alters user-facing
behaviour, it aligns with this document or updates it in the same PR.

## 1) Purpose & Vision

pifi sits invisibly in an existing network and conditions each client's internet
connection independently — rate, latency, jitter and loss, per device and per
direction — adjustable live from a web interface.

It exists so that a player, app or device can be tested against a *specific*
network, on the device's own hardware and its own network stack, without
installing anything on it or changing how it connects.

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

## 3) Non-Goals

- A router, firewall or gateway. pifi issues no addresses and performs no NAT.
- A production traffic shaper or QoS system. It exists to degrade links
  deliberately, not to manage them.
- Decrypting or inspecting application payloads. Conditioning is transport-level.
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

**Daemon** (`infinite-streaming-pifid`) — a single Go binary with the Vue
interface embedded. Serves :80. Requires root: it configures queueing
disciplines and opens a packet socket.

**ntopng** — traffic analysis on :3000, watching `br-lan`. Deep links from each
device card. Optional: the image builds without it.

## 6) Behaviour

### 6.1 Discovery

- A device is a **client** only if its traffic arrives on a downstream port.
  Anything on the WAN port is upstream and is excluded.
- Addresses are learned **passively**, from ARP and from sampled forwarded
  traffic. Nothing is probed, scanned or injected.
- Presence comes from the radio and the bridge, never from a DHCP lease: a lease
  outlives the client that held it.
- A client may hold several IPv6 addresses at once (privacy extensions); all are
  tracked and all are conditioned.
- Names are learned from mDNS announcements the device makes anyway, and are
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
- Each device has a downlink and an uplink policy: rate, delay, jitter, loss.
- `rate_mbps = 0` means unlimited. `delay_ms` is **per direction**; the round
  trip is the sum, and the interface shows that sum.
- Sub-classes condition part of a device's traffic, matched by destination port,
  network and protocol, evaluated before the device default.
- Writes carry the revision the operator was looking at; a concurrent edit to
  the same device is refused rather than silently overwritten.

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

### 6.5 Measuring

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
- **Encrypted payloads stay encrypted.** Manifest-level inspection needs a proxy
  that is the origin path.
- **A measured ladder is the effective one, not the manifest's.** A rendition the
  player never selects — wrong codec, wrong viewport, skipped by its own logic —
  is never delivered and so never appears. Two devices can produce different
  ladders from identical content.
- **A low cap can deliver its bytes late.** `netemLimit` floors the netem queue
  at 1000 packets, which is 0.24 s of buffering at 50 Mbps but **48 s at
  0.25 Mbps**. Throughput is unaffected — the rate is delivered exactly — but a
  client-side measurement at 0.25 Mbps saw a 13-second stall, consistent with a
  queue deep enough to drive TCP's retransmission timers. The bytes arrive; they
  do not necessarily arrive usefully. See the open issue.
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
  that speed. **Verified 0.25 to 50 Mbps**: the kernel's own byte counters match
  the configured rate at 0.25, 0.5, 1, 2 and 4 Mbps, and a client counting TCP
  payload sees about −4.5%, which is exactly the Ethernet, IP and TCP headers
  the cap counts and the payload does not. There is no accuracy penalty at the
  bottom of the range.
- A configured one-way delay appears as that delay in round-trip time —
  measured 200.6 ms for a 200 ms setting.
- A device under test cannot tell the box is present: no extra hop, no address
  change, discovery unaffected.
- The interface never claims a policy is applied when it is not.
