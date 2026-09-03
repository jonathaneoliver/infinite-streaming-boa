// Mirrors daemon/internal/boa/model.go. Units are the human-facing ones --
// megabits, milliseconds, percent -- and are converted to the kernel's
// bits/seconds/fractions in exactly one place on the server.

export interface Shape {
  rate_mbps: number;
  delay_ms: number;
  jitter_ms: number;
  /** Mean loss. With `loss_burst` above 1 it is the long-run fraction lost,
   *  not the chance of losing any one packet. */
  loss_pct: number;
  /**
   * Mean loss burst length in packets. 1 — and absent, for policy stored
   * before this existed — is uniform loss, each packet independently, which is
   * netem's default and essentially never happens on a real link.
   *
   * Packets rather than milliseconds because netem's model steps per packet,
   * so packets is defined even at an unlimited rate. The controls derive the
   * wall-clock equivalent for display.
   */
  loss_burst?: number;
  reorder_pct: number;
  corrupt_pct: number;
}

/**
 * The impairments kept out of the way until they are used.
 *
 * netem's `duplicate` is absent on purpose. It cannot coexist with any other
 * netem qdisc on the same interface, in either order, so one device using it
 * would make every other device on that port unconditionable -- see the note in
 * the daemon's Shape.
 *
 * Not a preference and not a menu: which controls are on screen is derived from
 * whether they are doing anything. A device with none of these set shows four
 * sliders; set one and it stays visible for as long as it is in force. So the
 * page is quiet by default and nothing conditioning the traffic can ever be
 * hidden — the failure a "show these controls" checkbox list would invite.
 *
 * They are second-tier because the traffic here rarely needs them, not because
 * they are lesser: rate and delay are what this box is for, loss and jitter are
 * the common companions, and these three are the long tail.
 */
export const EXTRA_IMPAIRMENTS = [
  {
    key: 'reorder_pct' as const,
    label: 'reorder',
    unit: '%',
    max: 50,
    step: 0.5,
    /** netem cannot reorder without a delay queue to skip. */
    needsDelay: true,
    title: 'Packets released ahead of the delay queue, arriving out of order. TCP treats this very differently from loss.',
  },
  {
    key: 'corrupt_pct' as const,
    label: 'corrupt',
    unit: '%',
    max: 20,
    step: 0.1,
    needsDelay: false,
    title: 'Packets given a bit error. The receiver discards them on checksum, so the link is consumed but nothing arrives — unlike loss, where nothing is sent.',
  },
];

/** Whether any second-tier impairment is in force in this shape. */
export function hasExtras(s: Shape): boolean {
  return EXTRA_IMPAIRMENTS.some((e) => (s[e.key] ?? 0) > 0);
}

/** The longest burst the API accepts. Past this you are describing an outage,
 *  which belongs on a pattern where it is visible and timed. */
export const BURST_MAX = 50;

export interface Match {
  dst_port?: number;
  dst_cidr?: string;
  protocol?: string;
}

export interface SubClass {
  id: string;
  name: string;
  match: Match;
  down: Shape;
  up: Shape;
  enabled: boolean;
}

export interface Rung {
  /** What the rendition costs on the wire. */
  mbps: number;
  /**
   * The caps at which the client climbed INTO this rendition and fell OUT of
   * it. Both matter, because the cost alone cannot drive a pattern: a player
   * wants headroom, so capping at a rung's own bitrate does not hold it there.
   * They differ from each other because ABR players use hysteresis on purpose.
   * Absent when not observed in this run's direction.
   */
  up_at_mbps?: number;
  down_at_mbps?: number;
  /** The window behind this rung drifted: its two halves disagreed. */
  unstable?: boolean;
}

/**
 * One service's rendition ladder as seen by one device.
 *
 * Keyed by service, never by device alone: the same box streaming Netflix and
 * YouTube produces two ladders with nothing in common, so storing one per
 * device would have each sweep silently overwrite the last.
 */
export interface Ladder {
  service: string;
  rungs: Rung[];
  /** 'typed' | 'fetched' | 'measured' | 'inferred' -- rendered differently,
   *  because they are different strengths of claim. */
  provenance: string;
  measured_at?: number;
  note?: string;
}

/**
 * How a segment arrives at its closing keyframe.
 *
 * Both are real links: a handover or a lift door is a step, walking out of
 * range is a ramp. A timeline that could only express one of them would be
 * describing half of what happens on a radio.
 */
export type Ease = 'hold' | 'ramp';

/** The whole conditioning policy at one instant on a pattern's timeline. */
export interface Keyframe {
  /** Offset from the start, on half-second boundaries. */
  at_sec: number;
  down: Shape;
  up: Shape;
  /** How the run gets HERE from the keyframe before it. Meaningless on the first. */
  ease?: Ease;
}

/**
 * A timeline of keyframes.
 *
 * There is no wrap mode: a loop restarts at the first keyframe, so a pattern
 * that loops smoothly is one whose last keyframe holds the same values as its
 * first — visible on the timeline, unlike a flag whose effect only shows up
 * during playback.
 */
/** A link-lane event: a per-client Wi-Fi impairment on the pattern timeline.
 *  `kind` is "drop" (deauth), "nudge" (disassoc) or "deadzone" (a held outage).
 *  `dur_sec` is the block width: 0 = a single pulse (fired on the rising edge),
 *  >0 = the disturbance holds for that long — a flap for drop/nudge, a clean
 *  block for deadzone. See #135. */
export interface LinkEvent {
  at_sec: number;
  kind: 'drop' | 'nudge' | 'deadzone';
  dur_sec?: number;
}

export interface Pattern {
  name: string;
  keys: Keyframe[];
  loop: boolean;
  links?: LinkEvent[];
}

/**
 * One row of the pattern library.
 *
 * Built-ins are GENERATED by the daemon from the device's ladder on every
 * request, so a row's rates and duration describe the ladder as it is now.
 * That is why the list is fetched rather than held in the client: a
 * hard-coded "valley" would be a guess at rates the box has measured.
 */
export interface PatternEntry {
  name: string;
  builtin: boolean;
  dur_sec: number;
  keys: number;
  loop: boolean;
  selected?: boolean;
  /** Which ladder a built-in was generated from, and how strong a claim it is.
   *  'default' means the stand-in ladder, so the shape is plausible rather
   *  than measured -- a difference the operator must be able to see. */
  ladder_service?: string;
  ladder?: string;
  /** Why this row cannot be built right now, e.g. a stretch that runs past
   *  what the engine will play. Shown rather than hidden: a row that silently
   *  vanishes reads as a missing feature. */
  unavailable?: string;
}

/**
 * Live progress of a pattern run.
 *
 * Distinct from `Policy.pattern`, which is the timeline as authored: this is a
 * playhead moving along it, and only one of the two is editable.
 */
export interface PatternView {
  state: 'running' | 'paused' | 'done';
  name: string;
  pos_sec: number;
  dur_sec: number;
  loop: boolean;
  laps: number;
  /** The keyframe currently in force — the one last passed, not the next. */
  index: number;
  /** What is ENFORCED now, which during a ramp equals no keyframe. */
  down: Shape;
  up: Shape;
  reason?: string;
  started_at: number;
}

export interface Policy {
  mac: string;
  rev: number;
  label: string;
  enabled: boolean;
  down: Shape;
  up: Shape;
  sub: SubClass[] | null;
  ladders?: Ladder[] | null;
  pattern?: Pattern | null;
}

export interface SweepLevel {
  level: number;
  cap_mbps: number;
  rate_mbps: number;
  drift: number;
  saturated: boolean;
  new_rung: boolean;
  samples: number;
}

/** Live progress of a ladder sweep. Absent unless one has run this session. */
export interface SweepView {
  state: 'running' | 'done' | 'failed';
  phase: string;
  service: string;
  level: number;
  cap_mbps: number;
  ceiling_mbps: number;
  found?: Rung[];
  levels?: SweepLevel[];
  reason?: string;
  started_at: number;
}

export interface Station {
  mac: string;
  signal_dbm: number;
  tx_bytes: number;
  rx_bytes: number;
  tx_phy_mbps: number;
  rx_phy_mbps: number;
  tx_failed: number;
  connected_sec: number;
  inactive_ms: number;
}

export interface Counters {
  bytes: number;
  packets: number;
  drops: number;
  overlimits: number;
  backlog: number;
  qlen: number;
  throughput_mbps: number;
  cap_mbps: number;
}

/** The access point a wireless client is associated to. Absent for a wired
 *  client, and for a wireless one not currently on any radio. */
export interface RadioOn {
  iface: string;
  channel?: number;
  width_mhz?: number;
  mode?: string;
  /** "2.4GHz" or "5GHz" — the single most useful fact about which radio a
   *  client is on, now that the box serves both at once. */
  band?: string;
}

export interface Client {
  mac: string;
  ip?: string;
  /** Routable IPv6 addresses; a device usually holds several at once. */
  ipv6?: string[];
  hostname?: string;
  label: string;
  medium: string;
  port?: string;
  radio_on?: RadioOn;
  present: boolean;
  shapeable: boolean;
  station?: Station;
  policy: Policy;
  last_seen: number;
  down_counters: Counters;
  up_counters: Counters;
  sub_counters?: Record<string, Counters>;
  /** Last time this device moved more than a trickle, unix ms. 0 = never. */
  last_active_ms?: number;
  sweep?: SweepView;
  pattern_run?: PatternView;
}

export interface RadioInfo {
  iface: string;
  driver?: string;
  bus: 'usb' | 'onboard';
  product?: string;
  vendor?: string;
  link_mbps?: number;
  usb_version?: string;
}

/**
 * The box's own interfaces, from GET /api/bridge. Mirrors BridgeInfo in
 * daemon/internal/boa/bridgeinfo.go; see Sources J and K in the data contract.
 *
 * A separate fetch from the snapshot rather than part of it: this changes when
 * somebody plugs a cable in, and putting it on the 1 Hz stream would re-send an
 * unchanging inventory to every open browser once a second.
 */
export type IfaceRole = 'wan' | 'bridge' | 'ap' | 'radio' | 'lan' | 'other';

/** What a hostapd-served radio is doing right now. */
export interface APStatus {
  ssid?: string;
  bssid?: string;
  /** Global regulatory domain from `iw reg get` — hostapd reports none. */
  country?: string;
  channel?: number;
  freq_mhz?: number;
  /** DERIVED: hostapd has no width field. See apWidth in radioctl.go. */
  width_mhz?: number;
  mode?: string;
  /** True only when hostapd says state=ENABLED, i.e. actually beaconing. */
  enabled: boolean;
  stations: number;
  beacon_int_ms?: number;
  dtim_period?: number;
}

export interface IfaceInfo {
  name: string;
  role: IfaceRole;
  mac?: string;
  ipv4?: string[];
  ipv6?: string[];
  up: boolean;
  /** `carrier_known` is false when the interface is down: sysfs returns EINVAL
   *  rather than a value, and "no carrier" is a different fact from "could not
   *  ask". Rendering the two the same reports a healthy interface as a dead
   *  cable. */
  carrier: boolean;
  carrier_known: boolean;
  /** Wired ports only. A bridge reports a speed too, describing nothing. */
  speed_mbps?: number;
  master?: string;
  wireless: boolean;
  radio?: RadioInfo;
  ap?: APStatus;
  /** A radio the daemon watches. Clients on any other are NOT conditioned and
   *  never appear in the Clients tab. */
  serving: boolean;
  /** rfkill state. false means the transmitter is off and the AP is silent
   *  WITHOUT having told any client — they must time out and discover it.
   *  `power_known` is false when the switch could not be read; unknown must
   *  not render as "off", which would show a healthy radio as dead. */
  powered: boolean;
  power_known: boolean;
}

export interface ScanAP {
  bssid: string;
  ssid?: string;
  freq_mhz: number;
  channel: number;
  signal_dbm: number;
  /** Served by this box — excluded from the competition count. */
  ours?: boolean;
}

export interface ScanChannel {
  channel: number;
  freq_mhz: number;
  /** Neighbouring APs, excluding our own. */
  aps: number;
  strongest_dbm?: number;
  recommended?: boolean;
}

export interface ScanResult {
  iface: string;
  band: string;
  channels: ScanChannel[];
  aps: ScanAP[];
  best_channel?: number;
  was_channel?: number;
  now_channel?: number;
  /** The radio came back up on best_channel rather than where it started. */
  applied?: boolean;
  /** How long the radio was actually out of service, so the cost of the
   *  answer is reported next to it. */
  outage_sec: number;
  note: string;
}

export interface BridgeInfo {
  bridge: string;
  ifaces: IfaceInfo[];
  notes?: Notice[];
}

export interface SurveyChannel {
  /** The OPERATING frequency, from `iw dev info` — not from the survey block,
   *  whose own label is wrong on mt7921u. */
  freq_mhz: number;
  /** What the driver labelled this block, kept so the disagreement is visible. */
  reported_freq_mhz?: number;
  active_ms: number;
  busy_ms: number;
  receive_ms: number;
  transmit_ms: number;
  /** From the delta between two reads; absent on the first call. */
  busy_pct?: number;
  delta_active_ms?: number;
}

export interface SurveyResult {
  iface: string;
  operating_freq_mhz?: number;
  channels: SurveyChannel[];
  /** The driver attributed airtime to a frequency the radio is not on. */
  mislabelled?: boolean;
  note: string;
}

export interface Capabilities {
  shaping: boolean;
  uplink: boolean;
  radio: boolean;
  leases: boolean;
  wlan_iface: string;
  uplink_if: string;
  /** The radio actually serving the AP. `bus` is "usb" or "onboard"; for a USB
   *  adapter `link_mbps` is the speed it NEGOTIATED (5000 = SuperSpeed,
   *  480 = High-Speed), which is not always the speed it is capable of. */
  adapter: RadioInfo;
  /** True only when ntopng is ANSWERING, not merely installed. */
  ntopng: boolean;
  ntopng_port: number;
  /** True only when the iperf3 server is LISTENING, not merely installed. */
  iperf: boolean;
  iperf_port: number;
  /** True when per-client link events (deauth/disassoc) can be driven -- i.e.
   *  hostapd is serving the AP and exposing its control socket. False on the
   *  onboard/NetworkManager radio, so the link actions are hidden rather than
   *  offered as dead buttons. */
  link_control: boolean;
  /** True when this kernel's netem accepts a Gilbert-Elliott loss model,
   *  asked at startup rather than assumed. False disables the burst control
   *  with `loss_burst_note` as the reason: a control that says "bursty" while
   *  the kernel delivers uniform loss is worse than no control. */
  loss_burst: boolean;
  loss_burst_note?: string;
  reason?: string;
}

/**
 * Build a deep link into ntopng.
 *
 * The host comes from the current page rather than being hardcoded, so links
 * work however you reached it -- infinite-streaming-boa.local, its DHCP address, or the rescue
 * address. Only the port is fixed.
 *
 * ifid=0 is br-lan: ntopng watches the bridge, the one interface that sees
 * every client on both media in both directions.
 */
export function ntopngUrl(
  port: number,
  path = '',
  params: Record<string, string> = {},
): string {
  const host = window.location.hostname || 'localhost';
  if (!path) return `http://${host}:${port}/`;
  const qs = new URLSearchParams({ ifid: '0', ...params }).toString();
  return `http://${host}:${port}${path}?${qs}`;
}

export interface Notice {
  /** "error" sits at the top of the page; "info" is a footnote at the bottom. */
  level: 'error' | 'info';
  text: string;
}

export interface Snapshot {
  version: string;
  revision: number;
  control_revision: number;
  time: number;
  caps: Capabilities;
  clients: Client[];
  notices?: Notice[];
}

export const CLEAN: Shape = {
  rate_mbps: 0,
  delay_ms: 0,
  jitter_ms: 0,
  loss_pct: 0,
  reorder_pct: 0,
  corrupt_pct: 0,
};

// Named starting points. These are approximations of real-world links, not
// standards -- the numbers come from typical measured behaviour and are meant
// as a place to start rather than a certification profile.
//
// Burst lengths follow the same rule and are the least certain numbers here:
// nobody has fitted them to a measured link. The shape of the claim is what
// matters -- a router dropping from a full queue loses single packets, while a
// radio fade loses a run of them -- so congestion-like presets stay near
// uniform and the mobile ones do not.
export interface Preset {
  name: string;
  note: string;
  down: Shape;
  up: Shape;
}

export const PRESETS: Preset[] = [
  { name: 'Clean', note: 'no conditioning', down: { ...CLEAN }, up: { ...CLEAN } },
  {
    name: 'Fibre',
    note: '100 Mbps, 8 ms',
    down: { ...CLEAN, rate_mbps: 100, delay_ms: 4, jitter_ms: 1, loss_pct: 0 },
    up: { ...CLEAN, rate_mbps: 40, delay_ms: 4, jitter_ms: 1, loss_pct: 0 },
  },
  {
    name: 'Cable',
    note: '30 Mbps, 30 ms',
    down: { ...CLEAN, rate_mbps: 30, delay_ms: 15, jitter_ms: 4, loss_pct: 0.05, loss_burst: 2 },
    up: { ...CLEAN, rate_mbps: 6, delay_ms: 15, jitter_ms: 4, loss_pct: 0.05, loss_burst: 2 },
  },
  {
    name: '4G good',
    note: '20 Mbps, 50 ms',
    down: { ...CLEAN, rate_mbps: 20, delay_ms: 25, jitter_ms: 8, loss_pct: 0.1, loss_burst: 4 },
    up: { ...CLEAN, rate_mbps: 8, delay_ms: 25, jitter_ms: 8, loss_pct: 0.1, loss_burst: 4 },
  },
  {
    name: '4G weak',
    note: '3 Mbps, 120 ms, 1%',
    down: { ...CLEAN, rate_mbps: 3, delay_ms: 60, jitter_ms: 25, loss_pct: 1, loss_burst: 10 },
    up: { ...CLEAN, rate_mbps: 1, delay_ms: 60, jitter_ms: 25, loss_pct: 1, loss_burst: 10 },
  },
  {
    name: '3G',
    note: '1.5 Mbps, 200 ms, 1.5%',
    down: { ...CLEAN, rate_mbps: 1.5, delay_ms: 100, jitter_ms: 40, loss_pct: 1.5, loss_burst: 10 },
    up: { ...CLEAN, rate_mbps: 0.5, delay_ms: 100, jitter_ms: 40, loss_pct: 1.5, loss_burst: 10 },
  },
  {
    name: 'Satellite',
    note: '25 Mbps, 600 ms',
    down: { ...CLEAN, rate_mbps: 25, delay_ms: 300, jitter_ms: 20, loss_pct: 0.2, loss_burst: 5 },
    up: { ...CLEAN, rate_mbps: 3, delay_ms: 300, jitter_ms: 20, loss_pct: 0.2, loss_burst: 5 },
  },
  {
    name: 'Lossy',
    note: '10 Mbps, 5% loss in bursts',
    down: { ...CLEAN, rate_mbps: 10, delay_ms: 20, jitter_ms: 10, loss_pct: 5, loss_burst: 20 },
    up: { ...CLEAN, rate_mbps: 5, delay_ms: 20, jitter_ms: 10, loss_pct: 5, loss_burst: 20 },
  },
];

/**
 * Throughput history for one client, in Mbps.
 *
 * Parallel arrays sharing an index rather than an array of objects: at 3600
 * points per client per direction this is the difference between three arrays
 * and 3600 short-lived objects rebuilt on every tick.
 *
 * `t` carries the wall-clock time of each sample. The x-axis was formerly an
 * implied 1 Hz sequence, which cannot represent either of the two things that
 * now happen routinely: a long range arriving pre-averaged into wider buckets,
 * and a gap where the daemon restarted or the device went away. Without real
 * timestamps both are silently squeezed into a continuous line and the chart
 * misreports when things happened.
 */
export interface Series {
  t: number[];
  down: number[];
  up: number[];
  /**
   * The downlink cap in force at each sample, 0 for unlimited.
   *
   * Parallel to `t`, because a pattern moves the cap while the chart is being
   * watched: the device's current cap says nothing about what was enforced when
   * the player reacted three minutes ago, and lining those two up is the whole
   * point of the box.
   */
  cap: number[];
}

/**
 * Developer surfaces, shown only with ?developer=1 in the URL.
 *
 * Rendition-ladder discovery is a measurement tool, not part of conditioning a
 * device: a sweep drives the cap for half an hour and produces numbers that
 * only mean something to someone who knows what a rendition ladder is. On a
 * page whose job is "throttle this phone", it is a large panel on every card
 * asking a question most sessions are not asking.
 *
 * A URL flag rather than a stored preference, deliberately. It is a thing you
 * turn on for a session because you came to do that work, and it leaves no
 * state to explain later when a panel someone forgot about is missing -- or
 * present. Reload without it and the page is the plain conditioner again.
 *
 * Read once at load: it cannot change without a navigation, and re-reading it
 * per render would only invite the belief that it could.
 */
export const DEVELOPER =
  new URLSearchParams(window.location.search).get('developer') === '1';

/**
 * How the device list is ordered.
 *
 * 'busy' puts whatever is happening at the top; 'name' is the plain
 * alphabetical list; 'traffic' is by current throughput.
 */
export type SortMode = 'busy' | 'name' | 'traffic';

export const SORT_MODES: { v: SortMode; label: string; title: string }[] = [
  {
    v: 'busy',
    label: 'busy first',
    title:
      'Sweeping, then playing a pattern, then conditioned, then moving traffic. ' +
      'Devices doing none of those are ordered by when they last moved any, so ' +
      'the one that was streaming a minute ago sits above the one silent since ' +
      'Tuesday.',
  },
  { v: 'name', label: 'name', title: 'Alphabetical, present devices first.' },
  {
    v: 'traffic',
    label: 'traffic',
    title:
      'Busiest downlink first. This one DOES reorder as traffic changes — ' +
      'useful for finding the active device, awkward for clicking one.',
  },
];

/**
 * How recently a device must have moved traffic to count as active.
 *
 * Longer than a segment interval on purpose. A player is bursty by nature --
 * fetch a segment, idle, fetch the next -- so a threshold shorter than the gap
 * would have it dropping out of "active" between every segment and climbing
 * back, which is exactly the churn this ordering exists to prevent. Thirty
 * seconds spans any normal segment cadence, so a working player stays put and
 * one that genuinely stops falls out once.
 */
const ACTIVE_WITHIN_MS = 30_000;

/**
 * How interesting a device is, low is more interesting.
 *
 * Deliberately derived from STATE, not from live values. The server's own
 * comment on the fallback order says why: "so the list does not reshuffle as
 * telemetry changes". Sorting on throughput moves rows under the cursor every
 * second, and a row that will not hold still cannot be clicked. Every tier here
 * changes only when an operator does something, or when a device starts or
 * stops for longer than ACTIVE_WITHIN_MS.
 */
export function busyRank(c: Client, now: number): number {
  if (c.sweep?.state === 'running') return 0; // a half-hour measurement
  if (c.pattern_run?.state === 'running') return 1;
  if (c.pattern_run) return 2; // paused or stopped, but loaded and mid-test
  if (!isCleanPolicy(c.policy)) return 3; // conditioned right now
  if (c.present && isActive(c, now)) return 4; // doing something of its own
  if (c.present) return 5;
  return 6; // gone: nothing can be done with it
}

/** Whether this device has moved traffic recently enough to count as busy. */
export function isActive(c: Client, now: number): boolean {
  return !!c.last_active_ms && now - c.last_active_ms < ACTIVE_WITHIN_MS;
}

/** Whether a device's stored policy imposes nothing in either direction. */
export function isCleanPolicy(p: Policy): boolean {
  const clean = (s: Shape) =>
    !s.rate_mbps && !s.delay_ms && !s.jitter_ms && !s.loss_pct &&
    !s.reorder_pct && !s.corrupt_pct;
  return clean(p.down) && clean(p.up);
}

/** Order a device list for display. Never mutates the input. */
export function sortClients(list: Client[], mode: SortMode, now: number): Client[] {
  const byName = (a: Client, b: Client) =>
    a.label.localeCompare(b.label) || a.mac.localeCompare(b.mac);
  const out = [...list];
  if (mode === 'name') {
    return out.sort(
      (a, b) => Number(b.present) - Number(a.present) || byName(a, b),
    );
  }
  if (mode === 'traffic') {
    return out.sort(
      (a, b) =>
        b.down_counters.throughput_mbps - a.down_counters.throughput_mbps ||
        byName(a, b),
    );
  }
  return out.sort((a, b) => {
    const ra = busyRank(a, now);
    const rb = busyRank(b, now);
    if (ra !== rb) return ra - rb;
    // Recency orders the QUIET devices, and is deliberately not consulted for
    // active ones.
    //
    // For anything idle the timestamp is frozen, so the order cannot move --
    // and it answers the useful question about a row that is doing nothing:
    // was it doing something a minute ago, or has it been silent for days?
    // Alphabetical says nothing at all about that.
    //
    // For active devices it would be the opposite. Their timestamps all sit at
    // roughly now and update as each one fetches, so two bursty players would
    // take it in turns to be most recent and trade places every few seconds.
    // They sort by name instead, which cannot move.
    if (!isActive(a, now) && (a.last_active_ms ?? 0) !== (b.last_active_ms ?? 0)) {
      return (b.last_active_ms ?? 0) - (a.last_active_ms ?? 0);
    }
    return byName(a, b);
  });
}

/** Chart time ranges, in the `{ v, label }` shape the streaming dashboard uses. */
export const RANGES = [
  { v: 60, label: '1m' },
  { v: 300, label: '5m' },
  { v: 900, label: '15m' },
  { v: 3600, label: '1h' },
] as const;

/**
 * How the y-axis maximum is chosen.
 *
 * - `auto`   — follow the data (and the cap), rescaling as traffic changes.
 * - `cap`    — lock to the configured cap, so the headroom between what a
 *              device is doing and what it is allowed to do stays a constant
 *              distance. That gap is the question these charts exist to answer,
 *              and an axis that rescales hides it.
 * - `manual` — a fixed ceiling, so two devices an order of magnitude apart can
 *              be compared on one scale instead of both filling their panes.
 *
 * Linear throughout, deliberately: zero is a real and frequent value here (a
 * player that has filled its buffer stops requesting), and a log axis has no
 * position for it. See the note in TrafficChart.
 */
export type YMode = 'auto' | 'cap' | 'manual';

/**
 * Window for the sustained line, in seconds.
 *
 * An adaptive player does not consume bandwidth per second: it pulls a segment
 * at whatever rate it can get, then idles while the buffer drains. The live
 * trace is therefore a square wave between roughly the cap and zero, and
 * neither extreme answers "what is this device actually getting".
 *
 * 30s spans five fetch cycles at the 6s segment duration that is the common
 * case, and fifteen at a 2s cadence -- enough for the line to sit still rather
 * than breathe with each fetch.
 *
 * The cost is lag, and it is three different numbers worth keeping apart. The
 * line is drawn all the way to the live edge -- at `now` the trailing window is
 * fully available, so it never stops short of it. But the value there averages
 * the whole window, so its centre of mass is half a window old: ~15s at 30s.
 * A step change is not FULLY reflected until the old samples have aged out,
 * which takes the whole 30s. And at the start of a series the line begins once
 * the window is HALF full rather than full, so ~15s in.
 */
export const SUSTAINED_SEC = 30;

/**
 * The windows on offer, and what each is for.
 *
 * 30s stays the default for the reason above. The other two exist because the
 * right window depends on the segment duration of the thing being watched, and
 * that is a property of the stream rather than of this box:
 *
 * - 10s places the line ~5s behind the traffic rather than ~15s, and settles
 *   within 10s of a step, which is what you want while stepping a pattern by
 *   hand and watching a player react. Under a 6s segment cadence it spans fewer
 *   than two fetches, so it still breathes with them.
 * - 60s sits flat across ten fetches at 6s: the window to read a rendition
 *   plateau off rather than to watch a transition, since it puts the line ~30s
 *   behind and takes a full minute to settle.
 *
 * Every one of them is computed in the browser from points already held, so
 * changing it redraws the whole visible history at the new window rather than
 * only what arrives next.
 */
export const SUSTAINED_CHOICES = [
  { v: 10, label: '10s', title: 'Follows a change in ~5s. Best while stepping a cap by hand; still breathes with each segment fetch.' },
  { v: 30, label: '30s', title: 'Five fetch cycles at a 6s segment cadence. The default: steady, and ~15s behind a change.' },
  { v: 60, label: '60s', title: 'Ten fetch cycles at 6s. Flattest, for reading a rendition plateau rather than watching a transition.' },
] as const;

/*
 * The rate scale, shared by the slider and the pattern timeline.
 *
 * A linear 0-200 Mbps control puts every interesting mobile-network rate --
 * 0.5 to 5 Mbps -- inside the first three pixels of travel, which makes the
 * most-used part of the range unusable. Position 0 is reserved for
 * "unlimited", which has no place on a log scale at all.
 *
 * The timeline plots its curve on the same scale for the same reason, and so
 * that a ramp authored as a straight line on the slider draws as a straight
 * line on the chart.
 */
export const RATE_MIN = 0.1;
export const RATE_MAX = 200;

export function posToRate(p: number): number {
  if (p <= 0) return 0;
  const r = RATE_MIN * Math.pow(RATE_MAX / RATE_MIN, (p - 1) / 99);
  return r < 1 ? Math.round(r * 100) / 100 : Math.round(r * 10) / 10;
}

export function rateToPos(r: number): number {
  if (r <= 0) return 0;
  return Math.round(1 + (99 * Math.log(r / RATE_MIN)) / Math.log(RATE_MAX / RATE_MIN));
}

/**
 * The loss scale, for the same reason as the rate scale and with one addition.
 *
 * Everything interesting about loss lives between about 0.1% and 5% — 1% is
 * where TCP begins to suffer, 5% is a bad radio. The old linear 0–20 control
 * put all of that in its first quarter and spent the other three quarters on
 * values nobody sets deliberately.
 *
 * The addition is the top of the range. **100% is a blackhole** — the "drove
 * into a tunnel" test — and it has to be reachable by dragging, not just
 * through the API. A linear control that reached it would have been useless
 * everywhere else; a log control reaches it and keeps the bottom usable.
 *
 * Unlike rate, position 0 is not a special case: no loss is a real value at the
 * natural bottom of the scale, where "unlimited" was not.
 */
export const LOSS_MIN = 0.01;
export const LOSS_MAX = 100;

export function posToLoss(p: number): number {
  if (p <= 0) return 0;
  const l = LOSS_MIN * Math.pow(LOSS_MAX / LOSS_MIN, (p - 1) / 99);
  // Two decimals below 1% because that is where the useful resolution is, one
  // above: nobody distinguishes 37.4% loss from 37.5%.
  return l < 1 ? Math.round(l * 100) / 100 : Math.round(l * 10) / 10;
}

export function lossToPos(l: number): number {
  if (l <= 0) return 0;
  return Math.round(1 + (99 * Math.log(l / LOSS_MIN)) / Math.log(LOSS_MAX / LOSS_MIN));
}

/**
 * Starting timelines.
 *
 * Absolute Mbps, deliberately: boa cannot read a manifest, so it does not know
 * where this device's renditions sit. A step at "4 Mbps" says a player got
 * 4 Mbps; a step at "just above rung 3" would say which rendition it should
 * have sustained, which is the better question — and the one #28 exists to
 * answer by generating these from a measured ladder instead.
 *
 * Every keyframe time lands on a whole second. Throughput is sampled once a
 * second, so a transition finer than that could be configured but never
 * observed.
 */
export interface PatternTemplate {
  name: string;
  note: string;
  make: () => Pattern;
}

const clean = (): Shape => ({ ...CLEAN });
const at = (
  at_sec: number,
  down: Partial<Shape>,
  ease: Ease = 'hold',
): Keyframe => ({
  at_sec,
  down: { ...CLEAN, ...down },
  up: { ...CLEAN, ...down, rate_mbps: 0 },
  ease,
});

/**
 * Starting points for AUTHORING a timeline by hand.
 *
 * Deliberately not the same thing as the pattern library. These are impairment
 * scenarios -- a tunnel, a handover -- whose rates are illustrative and are
 * meant to be dragged around. The library's valley and pyramid are generated by
 * the daemon from the device's measured ladder and describe real renditions.
 *
 * A hard-coded "valley" used to live here at 12 and 1.5 Mbps. It was removed
 * when the ladder-derived one arrived: two different patterns under one name,
 * one of them invented, is worse than either.
 */
export const PATTERN_TEMPLATES: PatternTemplate[] = [
  {
    name: 'tunnel',
    note: 'clean, then the link all but disappears for 15s, then clean',
    make: () => ({
      name: 'tunnel',
      loop: true,
      keys: [
        at(0, { ...CLEAN, rate_mbps: 25 }),
        at(30, { ...CLEAN, rate_mbps: 25 }),
        at(31, { ...CLEAN, rate_mbps: 0.2, delay_ms: 400, jitter_ms: 200, loss_pct: 15 }),
        at(46, { ...CLEAN, rate_mbps: 0.2, delay_ms: 400, jitter_ms: 200, loss_pct: 15 }),
        at(47, { ...CLEAN, rate_mbps: 25 }),
        at(70, { ...CLEAN, rate_mbps: 25 }),
      ],
    }),
  },
  {
    name: 'congested cell',
    note: 'a sawtooth: capacity bleeds away, then the cell empties',
    make: () => ({
      name: 'congested cell',
      loop: true,
      keys: [
        at(0, { ...CLEAN, rate_mbps: 20, delay_ms: 30, jitter_ms: 10 }),
        at(45, { ...CLEAN, rate_mbps: 2, delay_ms: 120, jitter_ms: 60, loss_pct: 1 }),
        at(46, { ...CLEAN, rate_mbps: 20, delay_ms: 30, jitter_ms: 10 }),
      ],
    }),
  },
  {
    name: 'handover',
    note: 'a clean step between two cells, with a latency spike across it',
    make: () => ({
      name: 'handover',
      loop: true,
      keys: [
        at(0, { ...CLEAN, rate_mbps: 15, delay_ms: 30, jitter_ms: 5 }),
        at(25, { ...CLEAN, rate_mbps: 15, delay_ms: 30, jitter_ms: 5 }),
        at(26, { ...CLEAN, rate_mbps: 0.5, delay_ms: 600, jitter_ms: 300, loss_pct: 5 }),
        at(29, { ...CLEAN, rate_mbps: 6, delay_ms: 80, jitter_ms: 20 }),
        at(55, { ...CLEAN, rate_mbps: 6, delay_ms: 80, jitter_ms: 20 }),
      ],
    }),
  },
];

/** A first pattern for a device with none: its current policy, held, twice. */
export function patternFromPolicy(down: Shape, up: Shape): Pattern {
  return {
    name: 'pattern',
    loop: true,
    keys: [
      { at_sec: 0, down: { ...down }, up: { ...up } },
      { at_sec: 30, down: { ...down }, up: { ...up }, ease: 'hold' },
    ],
  };
}

export interface ChartPrefs {
  rangeSec: number;
  yMode: YMode;
  /** Ceiling in Mbps for `manual`; ignored in the other modes. */
  yManual: number;
  /** Draw the per-sample trace. */
  showLive: boolean;
  /** Draw the rolling mean over sustainedSec. */
  showSustained: boolean;
  /** Trailing window for that mean, in seconds. One of SUSTAINED_CHOICES. */
  sustainedSec: number;
  /**
   * Draw the expanded charts at double height.
   *
   * Height is resolution on the y axis, and the default trades it for how many
   * devices fit on one screen. A slow rung and the rung below it can sit a few
   * pixels apart at 196px; doubling separates them without touching the range
   * or the axis rule, so the plot still means the same thing.
   */
  tallCharts: boolean;
  /**
   * Which directions to draw at all.
   *
   * A view setting, not a policy one: hiding a direction changes nothing about
   * what is conditioned or measured, it stops drawing it. Uplink is the case
   * this exists for -- most work here is downlink, the README says uplink is
   * untested at any rate, and a column nobody reads is half the width of every
   * card.
   */
  showDown: boolean;
  showUp: boolean;
}
