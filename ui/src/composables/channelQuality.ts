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

export function rateChannel(c: ScanChannel | undefined): Quality {
  if (!c || c.aps === 0) return 'clear';
  const s = c.strongest_dbm;
  if (s !== undefined && s >= LOUD_DBM) return 'crowded';
  if (c.aps >= 3) return 'crowded';
  if (s !== undefined && s < FAINT_DBM && c.aps === 1) return 'clear';
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
  if (!c || c.aps === 0) return 'nothing heard here';
  const loud =
    c.strongest_dbm !== undefined ? `, strongest ${c.strongest_dbm} dBm` : '';
  return `${c.aps} access point${c.aps === 1 ? '' : 's'}${loud}`;
}
