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
- Traffic **sourced by the box itself** is exempt, so the interface cannot
  throttle itself on a device it is conditioning.
- The netem queue is sized from rate x delay. netem's 1000-packet default would
  silently discard traffic on high-delay profiles while reporting zero loss.
- Every discovered client gets a counting class even when unconditioned, so
  throughput is visible without setting a policy first.
- Stopping the daemon removes all conditioning.

### 6.4 Interface

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
- Device cards fold when there is more than one device, keeping a sparkline and
  current figure per direction on the fold title. Folding is presentation only;
  a folded card stays live.
- Downlink is blue and uplink orange, consistently, everywhere. Direction is the
  most confusable property in a bidirectional conditioner.
- The interface states its own limits: that Wi-Fi airtime is shared and
  conditioning is additive on top of it; that `overlimits` is not an error; that
  PHY rate is not throughput.

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

## 8) Success Criteria

- A configured cap is delivered within the framing overhead of a real link of
  that speed — measured −4.5% at caps from 1.5 to 50 Mbps.
- A configured one-way delay appears as that delay in round-trip time —
  measured 200.6 ms for a 200 ms setting.
- A device under test cannot tell the box is present: no extra hop, no address
  change, discovery unaffected.
- The interface never claims a policy is applied when it is not.
