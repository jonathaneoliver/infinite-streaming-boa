/*
 * The axis rules, shared by every chart that draws one.
 *
 * These were TrafficChart's alone until the adapter folds grew a chart of their
 * own, and a second copy of "what is a round number" is a second answer waiting
 * to disagree: two panes on the same screen picking different tick precision or
 * a different ceiling ladder is exactly the kind of small inconsistency that
 * makes a reader distrust both. Pure functions, no Vue, so they are testable and
 * cannot pull component state along with them.
 *
 * Only the RULES live here. Padding, height, and what a chart chooses to draw
 * stay with the component, because those differ on purpose.
 */

/**
 * The ceiling ladder.
 *
 * Rounding a peak up to the next power of ten wastes most of the pane; these
 * intermediate steps keep the trace filling a useful fraction of its height.
 * Measured fit against representative peaks when this was introduced: 77% -> 86%,
 * 63% -> 79%, 61% -> 76%, 68% -> 85%.
 */
const LADDER = [1, 1.2, 1.5, 1.8, 2, 2.5, 3, 4, 5, 6, 7, 8, 10];

export function niceMax(v: number): number {
  if (v <= 0) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / mag;
  return (LADDER.find((s) => n <= s) ?? 10) * mag;
}

/**
 * How many decimals a value needs to be written exactly, or -1 if it cannot be
 * written within the budget.
 *
 * Two decimals RELATIVE TO THE STEP'S OWN MAGNITUDE, not two absolute ones. A
 * hard cap of two is itself a scale: it makes a 0.015 step unwritable while a
 * 1.5 one is fine, so a small axis lost gridlines for no reason but its units.
 */
export function decimalsFor(v: number): number {
  if (!(v > 0)) return -1;
  const max = Math.min(6, Math.max(2, 2 - Math.floor(Math.log10(v))));
  for (let d = 0; d <= max; d++) {
    if (Math.abs(Number(v.toFixed(d)) - v) < 1e-9) return d;
  }
  return -1;
}

/** Target gap between gridlines. 34px lands on fifths at the default height and
 *  tenths when tall -- six lines, then eleven, the short grid with its
 *  midpoints filled in. */
export const TICK_SPACING_PX = 34;

/**
 * How many intervals to divide an axis into.
 *
 * Candidates are rejected when the resulting step cannot be printed exactly by
 * the same formatter the axis uses, which is what keeps thirds of 10 and
 * quarters of 1.5 off the axis: 3.33 and 0.38 make a grid harder to read than
 * having fewer lines. Same reasoning as the LADDER, applied to the interval
 * rather than to the maximum.
 */
export function tickDivisions(max: number, plotH: number): number {
  if (!(max > 0)) return 2;
  const ideal = plotH / TICK_SPACING_PX;
  const usable = [2, 3, 4, 5, 6, 8, 10].filter((n) => decimalsFor(max / n) >= 0);
  if (!usable.length) return 2;
  return usable.reduce((a, b) => (Math.abs(b - ideal) < Math.abs(a - ideal) ? b : a));
}

/** The tick values and their fractional heights, 0 at the bottom. */
export function axisTicks(max: number, plotH: number): { v: number; frac: number }[] {
  const n = tickDivisions(max, plotH);
  return Array.from({ length: n + 1 }, (_, i) => {
    const v = (max * i) / n;
    return { v, frac: max > 0 ? v / max : 0 };
  });
}

/** One precision for a whole axis, taken from the step it is drawn in -- never
 *  per label, which is how an axis ends up printing the same string five times. */
export function axisDecimals(max: number, plotH: number): number {
  return Math.max(0, decimalsFor(max / tickDivisions(max, plotH)));
}

/** "5m", "30s", "1h" -- the window's own name, used both in the header line and
 *  as the left-hand end of the time axis. */
export function spanLabel(windowMs: number): string {
  const secs = Math.round(windowMs / 1000);
  return secs >= 3600 ? `${secs / 3600}h` : secs >= 60 ? `${secs / 60}m` : `${secs}s`;
}
