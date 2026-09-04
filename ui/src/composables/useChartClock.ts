import { ref } from 'vue';

/*
 * The right-hand edge of every plot on the page, advanced once a second.
 *
 * ONE clock, module-level, for the same reason the axis rules and the chart
 * preferences are shared: the adapter charts in a fold and the client charts
 * below them are read one after the other, and two independent tickers would
 * put "now" at two different x positions -- a second's worth of drift between
 * panes that are supposed to line up.
 *
 * Time has to come from here rather than from the data. The adapter charts
 * briefly derived their edge from the newest sample, which made them reactive
 * but froze an adapter that had gone quiet: its window stopped advancing, old
 * traffic never aged out, and the label "now" pointed at whenever the last
 * sample happened to arrive. A chart that stops telling the time when nothing
 * is happening is worst exactly when "nothing is happening" is the finding.
 *
 * The reason a plain `Date.now()` inside a computed does NOT work, which is the
 * trap this exists to close: a computed re-runs when a reactive dependency
 * changes, and the wall clock is not one. It would be read once and frozen.
 */
export const chartNow = ref(Date.now());

/*
 * Held still while a pointer is inside any chart.
 *
 * Without it the plot slides left under the cursor while it is being read, and
 * the crosshair silently comes to rest on a different sample than the one aimed
 * at -- the reading is wrong by however long the pointer stayed there.
 *
 * A COUNT rather than a flag, because the clock is now shared. Moving between
 * two charts can raise the second one's enter before the first one's leave, and
 * a boolean would be left stuck on by the trailing leave.
 */
const readers = ref(0);

export function holdClock(hold: boolean) {
  readers.value = hold ? readers.value + 1 : Math.max(0, readers.value - 1);
}

window.setInterval(() => {
  if (readers.value === 0) chartNow.value = Date.now();
}, 1000);
