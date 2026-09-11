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

/**
 * A rating and the rule that produced it.
 *
 * ONE function, because the colour and the tooltip explaining the colour must
 * not be able to disagree. They were separate: `rateChannel` picked the colour
 * and `describeChannel` listed the evidence without ever naming the verdict or
 * the threshold it turned on — so a cell measured at 22% busy drew green, the
 * tooltip said "22% airtime busy, measured", and nothing on screen connected
 * the two or mentioned that 25% is where green stops. Asked why a plan looked
 * so green, the interface had no answer in it.
 */
export interface Verdict {
  quality: Quality;
  /** Why, in a clause that completes "rated <quality> because …". */
  why: string;
}

export function judgeChannel(c: ScanChannel | undefined): Verdict {
  // Nothing heard at all. Absence IS evidence here -- the scan lists every
  // channel it heard something on -- so this stays clear rather than unknown.
  if (!c || (c.covering ?? c.aps) === 0) {
    return {
      quality: 'clear',
      why: 'the scan heard no access point on it or overlapping it',
    };
  }

  // Prefer what was measured.
  if ((c.util_from ?? 0) > 0) {
    const u = c.util_pct ?? 0;
    const at = `${Math.round(u)}% of airtime measured busy`;
    if (u >= CROWDED_PCT) {
      return { quality: 'crowded', why: `${at}, at or above the ${CROWDED_PCT}% mark` };
    }
    if (u >= BUSY_PCT) {
      return { quality: 'busy', why: `${at}, at or above the ${BUSY_PCT}% mark` };
    }
    return { quality: 'clear', why: `${at}, below the ${BUSY_PCT}% mark` };
  }

  // Fallback: nobody on this channel advertised BSS Load, so this is the old
  // proxy -- how many neighbours occupy it and how loud the worst one is.
  // `covering` rather than `aps`, so an 80MHz neighbour counts against every
  // channel it fills instead of only the one it beacons on.
  const n = c.covering ?? c.aps;
  const s = c.strongest_dbm;
  const est = 'nobody here reported airtime, so this is an estimate';
  if (s !== undefined && s >= LOUD_DBM) {
    return {
      quality: 'crowded',
      why: `${est}: the loudest neighbour is ${s} dBm, at or above ${LOUD_DBM}`,
    };
  }
  if (n >= 3) {
    return { quality: 'crowded', why: `${est}: ${n} neighbours occupy it` };
  }
  if (s !== undefined && s < FAINT_DBM && n === 1) {
    return {
      quality: 'clear',
      why: `${est}: one neighbour, and it is faint at ${s} dBm`,
    };
  }
  return { quality: 'busy', why: `${est}: ${n} neighbour${n === 1 ? '' : 's'} occupy it` };
}

export function rateChannel(c: ScanChannel | undefined): Quality {
  return judgeChannel(c).quality;
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

/**
 * One channel table from every scan held, freshest reading per channel.
 *
 * Built because the radio that can afford to scan is not the radio you want to
 * know about. The onboard brcmfmac radio sweeps BOTH bands while it keeps
 * serving; the mt7921u adapters refuse to scan while beaconing and have to have
 * their access point taken down for it. Keyed per-radio, a 5GHz radio's band
 * plan could therefore only be coloured by dropping that radio's clients.
 *
 * Merged, one free scan on the onboard radio colours every plan on the box.
 *
 * FRESHEST wins rather than the radio's own reading, because a scan describes a
 * moment that moves: measured 2026-09-07, channel 40 read 9.8% busy at idle and
 * 69.8% under load minutes apart. A stale first-hand number is worth less than
 * a current second-hand one.
 *
 * Returns undefined when nothing has been scanned at all, which `rateFor` reads
 * as 'unknown' — the state that stops an unscanned box drawing a wall of green.
 */
export function mergeScans(
  scans: Record<string, ScanSummary> | undefined,
): ScanSummary | undefined {
  if (!scans) return undefined;
  const all = Object.values(scans);
  if (all.length === 0) return undefined;

  // Per channel, remember WHICH scan's reading won, so a later-iterated but
  // older scan cannot overwrite a newer one.
  const best = new Map<number, { at: number; c: ScanChannel }>();
  let newest = 0;
  for (const s of all) {
    const at = s.at ?? 0;
    if (at > newest) newest = at;
    for (const c of s.channels ?? []) {
      const prev = best.get(c.channel);
      if (!prev || at > prev.at) best.set(c.channel, { at, c });
    }
  }
  return { at: newest, channels: [...best.values()].map((b) => b.c) };
}

/**
 * What the colour is claiming, and WHY it claims it, for a tooltip.
 *
 * The verdict leads. A plan is forty cells of colour and the question it
 * provokes is always the same one -- why is that cell that colour -- so the
 * answer belongs in the first clause rather than left to be inferred from a
 * list of figures further along.
 *
 * The verdict comes from judgeChannel, the same call that picks the colour, so
 * the explanation cannot drift away from the thing it explains.
 */
export function describeChannel(
  scan: ScanSummary | undefined,
  channel: number,
): string {
  if (!scan) return 'not scanned yet';
  const c = scan.channels?.find((x) => x.channel === channel);
  const v = judgeChannel(c);

  if (!c || (c.covering ?? c.aps) === 0) {
    // The caveat matters and used to be missing. A channel with no entry is
    // rated clear on the strength of "the scan lists everything it heard" --
    // which is only as good as the scan's coverage, and a partial scan is
    // indistinguishable here from a genuinely empty channel.
    const looked = (scan.looked ?? []).includes(channel);
    return looked
      ? `${v.quality} — ${v.why}. It was listened to, so this is a real absence`
      : `${v.quality} — ${v.why}, and it is not in the list of channels the ` +
          `scan listened to, so treat this as unmeasured rather than quiet`;
  }

  const n = c.covering ?? c.aps;
  const parts: string[] = [`${c.aps} access point${c.aps === 1 ? '' : 's'}`];
  // Only worth saying when they differ: it is the whole point that a channel
  // can be occupied by neighbours that do not beacon on it.
  if (n > c.aps) parts.push(`${n} covering it at their width`);
  if (c.strongest_dbm !== undefined) parts.push(`strongest ${c.strongest_dbm} dBm`);
  if (c.stations) parts.push(`${c.stations} client(s)`);
  // The spread, where several neighbours measured the same medium and
  // disagreed. It is the one figure that says how much to trust the rest.
  if ((c.util_from ?? 0) > 1 && c.util_min_pct !== undefined) {
    parts.push(
      `${c.util_from} of them measured it, ` +
        `${Math.round(c.util_min_pct)}–${Math.round(c.util_pct ?? 0)}%`,
    );
  }
  return `${v.quality} — ${v.why} · ${parts.join(', ')}`;
}
