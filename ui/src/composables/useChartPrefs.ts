import { ref, watch } from 'vue';
import { SUSTAINED_SEC } from '@/types';
import type { ChartPrefs } from '@/types';

/*
 * The chart settings, shared by everything that draws a chart.
 *
 * Module-level for the same reason useAdapters is: there is now more than one
 * place on the page drawing a time series, and a second copy of "which range,
 * how tall" is a second answer waiting to disagree. It already did -- the
 * adapter charts were built at TrafficChart's default height while the client
 * charts below them were set to tall, so the same ceiling of 1000 Mbit/s came
 * out as three gridlines in the fold and eleven on the card. Same rule, same
 * data, two different-looking axes.
 *
 * The PRD says a range "applies to every device at once, because the reason to
 * change it is comparison". That reason does not stop at the device list.
 */

const CHART_KEY = 'boa.chart';

export const CHART_DEFAULTS: ChartPrefs = {
  rangeSec: 300, yMode: 'auto', yManual: 10, showPhy: false,
  // Both on by default: the live trace is the record, and the mean is the
  // answer to the question most often being asked of it.
  showLive: true, showSustained: true, sustainedSec: SUSTAINED_SEC,
  // Off by default: the taller plot costs how many devices are visible at once,
  // which is the more common need.
  tallCharts: false,
  // Both on: hiding a direction is a deliberate act, and a card that silently
  // omitted one would be a card lying about what it is conditioning.
  showDown: true, showUp: true,
};

/** The base plot height, and its tall variant. Shared so the adapter charts and
 *  the client charts respond to the same switch by the same amount. */
export const EXPANDED_H = 196;
export function chartHeight(prefs: ChartPrefs): number {
  return prefs.tallCharts ? EXPANDED_H * 2 : EXPANDED_H;
}

function load(): ChartPrefs {
  try {
    // Merged over the defaults, so a stored object written by an older build
    // (or a hand-edited one) cannot leave a field undefined.
    return { ...CHART_DEFAULTS, ...JSON.parse(localStorage.getItem(CHART_KEY) ?? '{}') };
  } catch {
    return { ...CHART_DEFAULTS };
  }
}

/**
 * Persisted for the same reason the fold state is -- a chosen range is a
 * working setup, and having it reset on every reload makes the page feel like
 * it forgets what you were doing.
 */
export const chartPrefs = ref<ChartPrefs>(load());

watch(
  chartPrefs,
  (v) => {
    try {
      localStorage.setItem(CHART_KEY, JSON.stringify(v));
    } catch {
      /* private windows and blocked storage must not break the page */
    }
  },
  { deep: true },
);
