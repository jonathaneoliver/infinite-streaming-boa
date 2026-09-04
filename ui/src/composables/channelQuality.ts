import type { ScanChannel, ScanSummary } from '@/types';

/**
 * How good a channel looks, from a scan this box actually took.
 *
 * MEASURED, never inferred. There is no rating until someone presses scan, and
 * what comes back describes that moment: a neighbour that was streaming when
 * the scan ran may be idle now, and one that was asleep will not appear at all.
 * So this grades what was heard and says when it was heard, rather than
 * pretending to a standing truth about the air.
 */
export type Quality = 'clear' | 'busy' | 'crowded' | 'unknown';

/**
 * Two numbers decide it, and the LOUD one dominates.
 *
 * Signal strength matters more than a headcount because interference is about
 * whether your radio has to defer to someone else, and one access point at
 * -20dBm -- a few metres away, which is what this box actually sees on channel
 * 40 -- costs far more airtime than three at -75dBm that are barely audible.
 * Counting APs alone would rank those the wrong way round.
 *
 * The thresholds are the same ones the client cards use for signal strength, so
 * "red" means the same thing everywhere in this interface.
 */
const LOUD_DBM = -60;
const FAINT_DBM = -75;

/*
 * Measured airtime, where a neighbour reported it.
 *
 * BSS Load is the only thing in a scan that MEASURES congestion rather than
 * letting it be inferred, and the inference is demonstrably poor: on this box
 * an access point with zero clients sat in 37% utilisation while one with ten
 * clients sat in 8.6%. A headcount ranks those the wrong way round.
 *
 * 50% is where a channel stops being merely shared and starts costing a client
 * throughput it asked for; 25% is comfortably shared. Both are judgements, but
 * they are judgements about a measured quantity rather than about a proxy.
 */
const BUSY_PCT = 25;
const CROWDED_PCT = 50;

/** Whether a rating came from a measurement or from a headcount. The two are
 *  not equally good and the interface should not present them identically. */
export type Basis = 'measured' | 'estimated' | 'none';

export function basisFor(c: ScanChannel | undefined): Basis {
  if (!c) return 'none';
  return (c.util_from ?? 0) > 0 ? 'measured' : 'estimated';
}

export function rateChannel(c: ScanChannel | undefined): Quality {
  // Nothing heard at all. Absence IS evidence here -- the scan lists every
  // channel it heard something on -- so this stays clear rather than unknown.
  if (!c || (c.covering ?? c.aps) === 0) return 'clear';

  // Prefer what was measured.
  if ((c.util_from ?? 0) > 0) {
    const u = c.util_pct ?? 0;
    if (u >= CROWDED_PCT) return 'crowded';
    if (u >= BUSY_PCT) return 'busy';
    return 'clear';
  }

  // Fallback: nobody on this channel advertised BSS Load, so this is the old
  // proxy -- how many neighbours occupy it and how loud the worst one is.
  // `covering` rather than `aps`, so an 80MHz neighbour counts against every
  // channel it fills instead of only the one it beacons on.
  const n = c.covering ?? c.aps;
  const s = c.strongest_dbm;
  if (s !== undefined && s >= LOUD_DBM) return 'crowded';
  if (n >= 3) return 'crowded';
  if (s !== undefined && s < FAINT_DBM && n === 1) return 'clear';
  return 'busy';
}

/**
 * Rate a channel against a radio's last scan.
 *
 * A channel MISSING from the scan is clear, not unknown: the scan lists every
 * channel it heard something on, so absence is evidence rather than an absence
 * of evidence. No scan at all is what 'unknown' is for -- and the two must not
 * be conflated, or a box nobody has scanned would show a wall of green.
 */
export function rateFor(scan: ScanSummary | undefined, channel: number): Quality {
  if (!scan) return 'unknown';
  return rateChannel(scan.channels?.find((c) => c.channel === channel));
}

/** What the colour is claiming, for a tooltip. */
export function describeChannel(
  scan: ScanSummary | undefined,
  channel: number,
): string {
  if (!scan) return 'not scanned yet';
  const c = scan.channels?.find((x) => x.channel === channel);
  if (!c || (c.covering ?? c.aps) === 0) return 'nothing heard here';

  const n = c.covering ?? c.aps;
  const parts: string[] = [`${c.aps} access point${c.aps === 1 ? '' : 's'}`];
  // Only worth saying when they differ: it is the whole point that a channel
  // can be occupied by neighbours that do not beacon on it.
  if (n > c.aps) parts.push(`${n} covering it at their width`);
  if (c.strongest_dbm !== undefined) parts.push(`strongest ${c.strongest_dbm} dBm`);

  if ((c.util_from ?? 0) > 0) {
    // Lead with the measurement and name it as one, so a colour resting on
    // evidence is distinguishable from a colour resting on a guess.
    const s = c.stations ? `, ${c.stations} client(s)` : '';
    return `${Math.round(c.util_pct ?? 0)}% airtime busy, measured${s} · ${parts.join(', ')}`;
  }
  return `${parts.join(', ')} · no airtime reported, so this is an estimate`;
}
