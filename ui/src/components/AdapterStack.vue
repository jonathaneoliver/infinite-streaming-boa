<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import type { Series } from '@/types';
import { clientColour } from '@/composables/useClientColours';
import { axisDecimals, axisTicks, niceMax, spanLabel } from '@/composables/chartAxis';
import { chartHeight, chartPrefs } from '@/composables/useChartPrefs';
import { chartNow } from '@/composables/useChartClock';

/**
 * What one adapter is carrying, stacked by device.
 *
 * The rack row says how many devices are on a radio and the client cards below
 * say what each one is doing. Neither answers the question in between -- how
 * the radio's capacity is being divided right now -- and that is the question
 * behind "why is this device slow": a stream that halved because the radio
 * halved looks identical, on its own card, to one that halved by itself.
 *
 * Stacked rather than overlaid, so the TOP EDGE is the adapter's total and each
 * band's thickness is that device's share. Both readings matter and a set of
 * overlaid lines gives only the second.
 *
 * TWO charts SIDE BY SIDE, not one mirrored pair. Colour here means device, and
 * colour everywhere else in this interface means direction; one chart carrying
 * both would need the same channel to say two things. So direction moves into
 * the heading and colour is left free to mean identity.
 *
 * Geometry, padding, the tick ladder and the time axis are all TrafficChart's,
 * with the shared rules living in chartAxis so the two cannot drift: a fold and
 * a card are read one after the other and have to be the same kind of object.
 * Width is MEASURED rather than scaled from a viewBox, for the same reason that
 * chart measures it -- a viewBox stretched to the column distorts the labels
 * along with the data.
 */
const props = defineProps<{
  /** The adapter whose traffic this is, matched against each sample's iface. */
  iface: string;
  /** Per-device history, keyed by MAC -- the same object the client cards read.
   *  This, and NOT the current device list, decides what the chart contains. */
  series: Record<string, Series>;
  /** MAC to display name, for whatever the history turns out to hold. Missing
   *  entries fall back to the MAC: a device can leave the snapshot entirely
   *  while its last minute of traffic is still on screen, and an unnamed band
   *  is better than a disappearing one. */
  labels: Record<string, string>;
}>();

/**
 * Who was on this adapter, from the RECORD rather than from the roster.
 *
 * Membership used to come from the list of currently-attached devices, which
 * quietly made the chart a live gauge rather than a history: the moment a
 * device left, the traffic it had just been doing vanished from the plot, and
 * an adapter that emptied went blank instead of showing what had happened on
 * it. That is backwards for the question being asked -- "what was this radio
 * carrying" is asked most often just after something stopped.
 *
 * Each sample already records the adapter that carried it (DATA-CONTRACT
 * Source R), so the history is self-describing and no roster is needed. A
 * device that has left keeps its band for as long as its samples are inside
 * the window, and then ages out of it naturally.
 */
const members = computed<string[]>(() => {
  const out: string[] = [];
  for (const mac of Object.keys(props.series)) {
    const s = props.series[mac];
    if (s?.iface?.some((f) => f === props.iface)) out.push(mac);
  }
  return out;
});

/*
 * Padding and HEIGHT both come from the client charts, the height through the
 * shared prefs rather than as a constant here.
 *
 * Hard-coding it was a real bug and not a cosmetic one: the fold drew at the
 * default 196 while the cards below were set to tall, so the very same ceiling
 * of 1000 Mbit/s came out as three gridlines up here and eleven down there.
 * Same rule, same data, two axes that did not look like each other -- which is
 * exactly the impression a shared axis module was meant to prevent.
 */
const PAD = { l: 40, r: 68, t: 10, b: 20 };
const H = computed(() => chartHeight(chartPrefs.value));
const PLOT_H = computed(() => H.value - PAD.t - PAD.b);

/*
 * Width comes from measuring the COLUMN, not from halving the row.
 *
 * Computing `(row - gap) / 2` here meant this component owned a copy of the
 * layout, and it drifted from the real one: the client cards lay their pair out
 * with `.dirs` at `1fr 1fr` and a 1px gap, while this assumed 12px, so the two
 * families rendered 1105px against 1093px on the same screen. Measuring
 * whatever CSS actually handed the column removes the copy -- the grid decides,
 * and the chart follows, which is how TrafficChart has always sized itself.
 *
 * The first column is measured and both use it; they are `1fr 1fr` and so
 * always equal, and observing one element is cheaper than observing two.
 */
const col = ref<HTMLElement | null>(null);
const chartW = ref(320);

/*
 * Re-observed WHENEVER the element changes, not once on mount.
 *
 * Observing in `onMounted` looked right and was wrong in one specific case that
 * this component hits routinely: the measured column lives inside the
 * `v-if="!empty"` branch, so an adapter with nothing in its window has no such
 * element when it mounts, the observer is never attached, and when traffic
 * arrives and the pair finally renders nothing measures it. The plot then keeps
 * its initial width forever -- a stunted x-axis inside a full-width column.
 *
 * An adapter that has gone quiet and comes back is not an edge case here, it is
 * the normal life of a radio, and it is exactly the state the drain-to-empty
 * behaviour creates.
 */
const ro = new ResizeObserver((e) => {
  const cw = e[0]?.contentRect.width ?? 0;
  if (cw > 0) chartW.value = Math.max(200, Math.floor(cw));
});
watch(col, (el, prev) => {
  if (prev) ro.unobserve(prev);
  if (el) ro.observe(el);
}, { immediate: true, flush: 'post' });
onBeforeUnmount(() => ro.disconnect());

const plotW = computed(() => Math.max(40, chartW.value - PAD.l - PAD.r));

const windowMs = computed(() => chartPrefs.value.rangeSec * 1000);
const span = computed(() => spanLabel(windowMs.value));

interface Band {
  mac: string;
  label: string;
  colour: string;
  /** Value at each grid time, in Mbit/s. Parallel to `grid`. */
  vals: number[];
  peak: number;
}

/**
 * The shared time grid.
 *
 * Stacking needs every device sampled at the same instants, and here they
 * genuinely are: `useSnapshot.record()` hoists one `Date.now()` outside its
 * per-client loop, so every device in one snapshot carries an IDENTICAL
 * timestamp, and the server's buckets are stamped at `slot * bucketMS`, an
 * absolute grid every device shares. So a union of the timestamps is exact --
 * no nearest-neighbour matching, no interpolation, no tolerance to tune.
 *
 * A device missing from a bucket contributes ZERO rather than being carried
 * forward. Absent means it reported nothing in that second, which for a
 * throughput stack is genuinely no traffic, not an unknown to be guessed at.
 */
const allTimes = computed<number[]>(() => {
  const seen = new Set<number>();
  for (const mac of members.value) {
    const s = props.series[mac];
    if (!s) continue;
    for (let i = 0; i < s.t.length; i++) {
      // Only samples this adapter actually carried. A device that roamed in
      // halfway through contributes to the second half of the stack and to
      // nothing before it -- the same truth the strip under its own card draws
      // as a change of band.
      if (s.iface[i] === props.iface) seen.add(s.t[i]);
    }
  }
  return [...seen].sort((a, b) => a - b);
});

/**
 * The window's right-hand edge: the SHARED CLOCK, not the newest sample.
 *
 * Two wrong answers preceded this one and both are worth keeping named. First
 * `Date.now()` inside a computed, which never re-runs because the wall clock is
 * not a reactive dependency -- the window froze at page load while samples kept
 * arriving, and every new point mapped past the right edge (measured at x=1035
 * in a 700-wide viewBox). Then the newest SAMPLE, which is reactive and fixed
 * that, but stopped time whenever an adapter went quiet: the window ceased
 * advancing, old traffic never aged out, and the axis label "now" pointed at
 * whenever the last sample had happened to arrive.
 *
 * A ticking ref is the only one of the three that is both reactive and honest,
 * and sharing it with the client charts keeps the two panes' "now" at the same
 * x. It also means an idle adapter drains to the left and empties, which is the
 * correct picture: a radio carrying nothing should look like a radio carrying
 * nothing, not like a radio frozen at the last thing it did.
 */
const edge = computed(() => {
  // Whichever is LATER, and the max is not defensive tidying -- it is the only
  // thing keeping both properties true at once. The clock ticks once a second
  // while samples are stamped with Date.now(), so the newest sample is
  // routinely up to a second AHEAD of it; taking the clock alone drew that
  // sample past the right-hand edge and into the margin, measured at 7.3px
  // beyond a plot 718px wide. Taking the newest sample alone is the bug this
  // replaced, where an adapter receiving nothing froze.
  //
  // So: an active adapter follows its data, a silent one follows the clock and
  // drains to the left, and neither case can overshoot the pane.
  const t = allTimes.value;
  return Math.max(chartNow.value, t.length ? t[t.length - 1] : 0);
});
const start = computed(() => edge.value - windowMs.value);
const grid = computed(() => allTimes.value.filter((t) => t >= start.value));

function bandsFor(dir: 'down' | 'up'): Band[] {
  const at = new Map(grid.value.map((t, i) => [t, i]));
  const out: Band[] = [];
  for (const mac of members.value) {
    const s = props.series[mac];
    if (!s) continue;
    const vals = new Array(grid.value.length).fill(0);
    let peak = 0;
    let any = false;
    for (let i = 0; i < s.t.length; i++) {
      if (s.iface[i] !== props.iface) continue;
      const g = at.get(s.t[i]);
      if (g === undefined) continue;
      const v = (dir === 'down' ? s.down[i] : s.up[i]) ?? 0;
      vals[g] = v;
      if (v > peak) peak = v;
      any = true;
    }
    if (!any) continue;
    out.push({
      mac,
      label: props.labels[mac] ?? mac,
      colour: clientColour(mac),
      vals,
      peak,
    });
  }
  // Biggest at the BOTTOM. A stack's lowest band is the only one with a flat
  // baseline, so it is the only one whose shape can be read directly; giving
  // that place to the device moving the most traffic makes the chart answer
  // "what is using this radio" at a glance. Ordered by peak over the window
  // rather than by current rate, so it does not reshuffle every second.
  return out.sort((a, b) => b.peak - a.peak);
}

/** The stacked total at each grid point, which is also the chart's top edge. */
function totals(bands: Band[]): number[] {
  return grid.value.map((_, i) => bands.reduce((a, b) => a + b.vals[i], 0));
}

const xAt = (t: number) =>
  PAD.l + ((t - start.value) / windowMs.value) * plotW.value;
const yAt = (v: number, max: number) =>
  PAD.t + PLOT_H.value - Math.max(0, Math.min(1, v / max)) * PLOT_H.value;

/**
 * One band's filled area: along its own top edge, back along the top edge of
 * everything below it.
 *
 * Built from the cumulative sums rather than from each band's height, so
 * adjacent bands share exact boundaries and no seam or overlap appears.
 */
function area(bands: Band[], n: number, max: number): string {
  const g = grid.value;
  if (g.length < 2) return '';
  const below: number[] = [];
  const above: number[] = [];
  for (let i = 0; i < g.length; i++) {
    let acc = 0;
    for (let k = 0; k < n; k++) acc += bands[k].vals[i];
    below.push(acc);
    above.push(acc + bands[n].vals[i]);
  }
  const top = g.map((t, i) => `${xAt(t).toFixed(1)},${yAt(above[i], max).toFixed(1)}`);
  const bot = g
    .map((t, i) => `${xAt(t).toFixed(1)},${yAt(below[i], max).toFixed(1)}`)
    .reverse();
  return `M${top.join('L')}L${bot.join('L')}Z`;
}

function fmt(v: number): string {
  return v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2);
}

const charts = computed(() =>
  (['down', 'up'] as const).map((dir) => {
    const bands = bandsFor(dir);
    const tot = totals(bands);
    const peak = tot.length ? Math.max(...tot) : 0;
    // Floored at 1 Mbit/s before the ladder rounds it. Without a floor an idle
    // radio scales its axis to whatever trickle of ARP and mDNS chatter is on
    // it, and a few kbit/s of background noise draws as a full-height mountain
    // range -- a chart that invents traffic out of nothing.
    //
    // Per direction, never shared: uplink here is routinely a twentieth of
    // downlink, and one ceiling for both would flatten upload into the axis.
    // The two axes are what stop side-by-side from reading as same-scale.
    const max = niceMax(Math.max(peak * 1.15, 1));
    const dp = axisDecimals(max, PLOT_H.value);
    return {
      dir,
      title: dir === 'down' ? 'download' : 'upload',
      bands,
      max,
      peak,
      avg: tot.length ? tot.reduce((a, b) => a + b, 0) / tot.length : 0,
      ticks: axisTicks(max, PLOT_H.value).map((t) => ({
        key: t.v,
        y: PAD.t + PLOT_H.value - t.frac * PLOT_H.value,
        label: t.v.toFixed(dp),
      })),
      // The LAST recorded total, not a separate live reading: the figure and
      // the right-hand edge of the plot are then the same number, and cannot
      // drift apart while the chart is being watched.
      now: tot.length ? tot[tot.length - 1] : 0,
    };
  }),
);

/** Nothing recorded on this adapter yet. Said in words rather than drawn as an
 *  empty pane, which reads as a radio carrying nothing rather than as a chart
 *  with nothing in it yet. */
const empty = computed(() => grid.value.length < 2);

/** One legend for the pair: the same devices, the same colours, in the same
 *  order. Two copies would be two places to disagree. */
const legend = computed(() => charts.value[0].bands);
</script>

<template>
  <div class="stack">
    <!-- "in the last 5m", not "yet". Both states reach here and they are
         different facts: a page just opened has no record, and an adapter that
         has gone quiet has had its record age out of the window. Saying "yet"
         claimed the first in both cases, which on an adapter you had been
         watching a minute earlier reads as the chart having lost the data
         rather than the traffic having stopped. -->
    <p v-if="empty" class="none">
      No traffic on {{ iface }} in the last {{ span }} — either nothing has been
      on it, or whatever was has gone quiet long enough to scroll off.
    </p>

    <template v-else>
      <div class="pair">
        <div
          v-for="(c, i) in charts" :key="c.dir" class="one"
          :ref="(el) => { if (i === 0) col = el as HTMLElement }"
        >
          <div class="head">
            <!-- NAMED, not coloured. Direction is blue and orange everywhere
                 else in this interface; here colour has been given to the
                 devices, so the heading carries the direction on its own. -->
            <span class="dir">{{ c.title }}</span>
            <span class="stats num">
              peak {{ fmt(c.peak) }} · avg {{ fmt(c.avg) }} · {{ span }}
            </span>
          </div>
          <svg :width="chartW" :height="H" class="plot" role="img"
            :aria-label="`${iface} ${c.title}, stacked by device`">
            <g class="grid">
              <template v-for="t in c.ticks" :key="t.key">
                <line :x1="PAD.l" :x2="PAD.l + plotW" :y1="t.y" :y2="t.y" />
                <text :x="PAD.l - 8" :y="t.y + 3" text-anchor="end" class="num">
                  {{ t.label }}
                </text>
              </template>
            </g>
            <path
              v-for="(b, n) in c.bands" :key="b.mac"
              :d="area(c.bands, n, c.max)"
              :fill="b.colour" fill-opacity="0.85"
            />
            <!-- The current total, at the right-hand edge where the client
                 charts put their endpoint value. -->
            <text :x="PAD.l + plotW + 6" :y="yAt(c.now, c.max) + 4" class="now num">
              {{ fmt(c.now) }}
            </text>
            <!-- The time axis, in the band below the plot, exactly where a
                 client chart puts it. -->
            <g class="xaxis num">
              <text :x="PAD.l" :y="H - 6">−{{ span }}</text>
              <text :x="PAD.l + plotW" :y="H - 6" text-anchor="end">now</text>
            </g>
          </svg>
        </div>
      </div>

      <!-- The legend is not optional here. Colour is doing identity work, and a
           band nobody can name is a colour with no meaning attached. -->
      <div class="legend">
        <span v-for="b in legend" :key="b.mac" class="key">
          <span class="chip" :style="{ background: b.colour }" />
          {{ b.label }}
        </span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.stack { margin: 8px 0 2px; }
.none {
  margin: 0;
  font-size: 12px;
  color: var(--ink-faint);
  max-width: 62ch;
}
/* The client cards' own `.dirs` rule, deliberately identical: `1fr 1fr` with a
   1px gap on the line colour, so the hairline between download and upload is
   the same hairline in a fold as on a card, and the two collapse to one column
   at the same width rather than at two nearby ones. */
.pair {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 1px;
  background: var(--line-soft);
  /* Cancels the fold body's 10px side padding so the pair spans the full width
     of the fold, exactly as `.dirs` spans the full width of a card. Without it
     the two families sit on containers 20px apart and no amount of matching the
     inner rules makes the plots the same size. */
  margin-left: -10px;
  margin-right: -10px;
}
@media (max-width: 860px) { .pair { grid-template-columns: 1fr; } }
/* `.dir`'s padding, to the pixel: the chart is inset from its column by the
   same amount on a card and in a fold, which is the other half of the two
   plots coming out the same width. */
.one { min-width: 0; background: var(--panel); padding: 13px 14px; }
.head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 2px;
}
.dir {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--ink-faint);
  font-weight: 600;
}
.stats { font-size: 10px; color: var(--ink-faint); white-space: nowrap; }
.plot { display: block; max-width: 100%; }
.grid line { stroke: var(--line-soft); stroke-width: 1; }
.grid text { fill: var(--ink-faint); font-size: 10px; }
.xaxis text { fill: var(--ink-faint); font-size: 10px; }
.now { fill: var(--ink-dim); font-size: 11px; font-weight: 600; }
.legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  margin-top: 6px;
  font-size: 11px;
  color: var(--ink-dim);
}
.key { display: inline-flex; align-items: center; gap: 5px; }
.chip { width: 9px; height: 9px; border-radius: 2px; display: inline-block; }
</style>
