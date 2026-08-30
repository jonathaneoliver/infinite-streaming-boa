// Mirrors daemon/internal/pifi/model.go. Units are the human-facing ones --
// megabits, milliseconds, percent -- and are converted to the kernel's
// bits/seconds/fractions in exactly one place on the server.

export interface Shape {
  rate_mbps: number;
  delay_ms: number;
  jitter_ms: number;
  loss_pct: number;
}

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
  mbps: number;
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

export interface Policy {
  mac: string;
  rev: number;
  label: string;
  enabled: boolean;
  down: Shape;
  up: Shape;
  sub: SubClass[] | null;
  ladders?: Ladder[] | null;
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

export interface Client {
  mac: string;
  ip?: string;
  /** Routable IPv6 addresses; a device usually holds several at once. */
  ipv6?: string[];
  hostname?: string;
  label: string;
  medium: string;
  port?: string;
  present: boolean;
  shapeable: boolean;
  station?: Station;
  policy: Policy;
  last_seen: number;
  down_counters: Counters;
  up_counters: Counters;
  sub_counters?: Record<string, Counters>;
  rtt_added_ms: number;
  sweep?: SweepView;
}

export interface Capabilities {
  shaping: boolean;
  uplink: boolean;
  radio: boolean;
  leases: boolean;
  wlan_iface: string;
  uplink_if: string;
  /** True only when ntopng is ANSWERING, not merely installed. */
  ntopng: boolean;
  ntopng_port: number;
  /** True only when the iperf3 server is LISTENING, not merely installed. */
  iperf: boolean;
  iperf_port: number;
  reason?: string;
}

/**
 * Build a deep link into ntopng.
 *
 * The host comes from the current page rather than being hardcoded, so links
 * work however you reached it -- infinite-streaming-pifi.local, its DHCP address, or the rescue
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
};

// Named starting points. These are approximations of real-world links, not
// standards -- the numbers come from typical measured behaviour and are meant
// as a place to start rather than a certification profile.
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
    down: { rate_mbps: 100, delay_ms: 4, jitter_ms: 1, loss_pct: 0 },
    up: { rate_mbps: 40, delay_ms: 4, jitter_ms: 1, loss_pct: 0 },
  },
  {
    name: 'Cable',
    note: '30 Mbps, 30 ms',
    down: { rate_mbps: 30, delay_ms: 15, jitter_ms: 4, loss_pct: 0.05 },
    up: { rate_mbps: 6, delay_ms: 15, jitter_ms: 4, loss_pct: 0.05 },
  },
  {
    name: '4G good',
    note: '20 Mbps, 50 ms',
    down: { rate_mbps: 20, delay_ms: 25, jitter_ms: 8, loss_pct: 0.1 },
    up: { rate_mbps: 8, delay_ms: 25, jitter_ms: 8, loss_pct: 0.1 },
  },
  {
    name: '4G weak',
    note: '3 Mbps, 120 ms, 1%',
    down: { rate_mbps: 3, delay_ms: 60, jitter_ms: 25, loss_pct: 1 },
    up: { rate_mbps: 1, delay_ms: 60, jitter_ms: 25, loss_pct: 1 },
  },
  {
    name: '3G',
    note: '1.5 Mbps, 200 ms, 1.5%',
    down: { rate_mbps: 1.5, delay_ms: 100, jitter_ms: 40, loss_pct: 1.5 },
    up: { rate_mbps: 0.5, delay_ms: 100, jitter_ms: 40, loss_pct: 1.5 },
  },
  {
    name: 'Satellite',
    note: '25 Mbps, 600 ms',
    down: { rate_mbps: 25, delay_ms: 300, jitter_ms: 20, loss_pct: 0.2 },
    up: { rate_mbps: 3, delay_ms: 300, jitter_ms: 20, loss_pct: 0.2 },
  },
  {
    name: 'Lossy',
    note: '10 Mbps, 5% loss',
    down: { rate_mbps: 10, delay_ms: 20, jitter_ms: 10, loss_pct: 5 },
    up: { rate_mbps: 5, delay_ms: 20, jitter_ms: 10, loss_pct: 5 },
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
 * than breathe with each fetch. The cost is a trailing lag of half the window,
 * so a cap change takes ~15s to be fully reflected.
 */
export const SUSTAINED_SEC = 30;

export interface ChartPrefs {
  rangeSec: number;
  yMode: YMode;
  /** Ceiling in Mbps for `manual`; ignored in the other modes. */
  yManual: number;
  /** Draw the per-sample trace. */
  showLive: boolean;
  /** Draw the rolling mean over SUSTAINED_SEC. */
  showSustained: boolean;
}
