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
const props = withDefaults(defineProps<{
  /** The adapter whose traffic this is, matched against each sample's iface. */
  iface: string;
  /**
   * What is stacked: throughput in Mbit/s, or airtime as a percentage.
   *
   * One component for both because everything except the y-axis is identical --
   * membership from the record, the shared grid, the ticking edge, the band
   * geometry, the ordering, the legend. A second component would have been a
   * copy of all of it, and this file already carries two scars from owning a
   * copy of something: a hard-coded height that drifted from the cards, and a
   * width calculation that rendered 1105px against their 1093px.
   *
   * The layouts differ and that is the only branch: throughput is a PAIR,
   * because download and upload are separate questions, while airtime is one
   * chart -- the radio's time is a single resource and transmit and receive
   * both spend it.
   *
   * Airtime keeps the pair's GRID regardless, sitting in the download column
   * with a filler beside it, so both plots carry the same window over the same
   * pixels and a vertical line through the fold means one instant.
   */
  mode?: 'throughput' | 'airtime';
  /**
   * Whether this radio's driver attributes airtime to individual stations.
   *
   * Consulted ONLY in airtime mode, and it decides between drawing an empty
   * chart and saying the question cannot be answered here. A radio that cannot
   * report it reads as a radio full of perfectly idle clients otherwise, which
   * is the same absent-versus-zero trap the airtime map and the survey note
   * already guard against elsewhere.
   */
  airtimeKnown?: boolean;
  airtimeCapable?: boolean;
  /** Per-device history, keyed by MAC -- the same object the client cards read.
   *  This, and NOT the current device list, decides what the chart contains. */
  series: Record<string, Series>;
  /** MAC to display name, for whatever the history turns out to hold. Missing
   *  entries fall back to the MAC: a device can leave the snapshot entirely
   *  while its last minute of traffic is still on screen, and an unnamed band
   *  is better than a disappearing one. */
  labels: Record<string, string>;
}>(), { mode: 'throughput', airtimeKnown: false, airtimeCapable: false });

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
  /** Value at each grid time, in the mode's own unit -- Mbit/s for throughput,
   *  percent of wall clock for airtime. Parallel to `grid`. */
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

function bandsFor(dir: 'down' | 'up' | 'air'): Band[] {
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
      const v = (dir === 'down' ? s.down[i] : dir === 'up' ? s.up[i] : s.air[i]) ?? 0;
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

/** Airtime reads in whole and tenths of a percent. Two decimals on a figure
 *  whose useful range is 0-100 is noise, and the third digit changes every
 *  tick. */
function fmtPct(v: number): string {
  return v >= 10 ? v.toFixed(0) : v.toFixed(1);
}

/**
 * The airtime axis is FIXED at 0-100%, and that is the point of it.
 *
 * Everywhere else here the axis adapts, because throughput has no meaningful
 * ceiling to draw against. Airtime does: the radio's time is the whole
 * resource, so "how full is this radio" is the only question the chart is for,
 * and niceMax would answer it identically for a radio at 8% and one at 80%.
 * Measured 2026-09-07, both readings occur on this box within the same minute.
 *
 * The stack's top edge is airtime spent on THIS BOX'S OWN CLIENTS. The gap to
 * 100% is not free capacity -- it is beacons, management frames, multicast and
 * every neighbour on the channel, none of which is measurable from here. So the
 * remainder is deliberately left as plain background with no band, no shading
 * and no label: anything drawn there would be a measurement this box never took.
 */
const AIRTIME_MAX = 100;

const charts = computed(() => {
  if (props.mode === 'airtime') {
    const bands = bandsFor('air');
    const tot = totals(bands);
    const dp = axisDecimals(AIRTIME_MAX, PLOT_H.value);
    return [{
      dir: 'air' as const,
      title: 'airtime',
      bands,
      max: AIRTIME_MAX,
      peak: tot.length ? Math.max(...tot) : 0,
      avg: tot.length ? tot.reduce((a, b) => a + b, 0) / tot.length : 0,
      unit: '%',
      ticks: axisTicks(AIRTIME_MAX, PLOT_H.value).map((t) => ({
        key: t.v,
        y: PAD.t + PLOT_H.value - t.frac * PLOT_H.value,
        label: t.v.toFixed(dp),
      })),
      now: tot.length ? tot[tot.length - 1] : 0,
    }];
  }
  return (['down', 'up'] as const).map((dir) => {
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
      unit: '',
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
  });
});

/** Nothing recorded on this adapter yet. Said in words rather than drawn as an
 *  empty pane, which reads as a radio carrying nothing rather than as a chart
 *  with nothing in it yet. */
const empty = computed(() => grid.value.length < 2);

/**
 * The radio's driver cannot attribute airtime to a station, so there is nothing
 * to draw and never will be on this hardware.
 *
 * A DIFFERENT state from empty, and the distinction is the whole reason the
 * flag travels: an empty chart says "nobody used this radio", which on the Pi's
 * onboard brcmfmac would be a flat lie about a radio that may be saturated.
 * Only claimed once a station has actually been on the radio to ask with --
 * until then the question is open, not answered in the negative.
 */
const cannotMeasure = computed(
  () => props.mode === 'airtime' && props.airtimeKnown && !props.airtimeCapable,
);

/** One legend for the pair: the same devices, the same colours, in the same
 *  order. Two copies would be two places to disagree. */
const legend = computed(() => charts.value[0].bands);
</script>

<template>
  <div class="stack">
    <div class="pair" :class="{ quiet: empty || cannotMeasure, single: mode === 'airtime' }">
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
            peak {{ mode === 'airtime' ? fmtPct(c.peak) : fmt(c.peak) }}{{ c.unit }} ·
            avg {{ mode === 'airtime' ? fmtPct(c.avg) : fmt(c.avg) }}{{ c.unit }} · {{ span }}
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
            {{ mode === 'airtime' ? fmtPct(c.now) + '%' : fmt(c.now) }}
          </text>
          <!-- The time axis, in the band below the plot, exactly where a
               client chart puts it. -->
          <g class="xaxis num">
            <text :x="PAD.l" :y="H - 6">−{{ span }}</text>
            <text :x="PAD.l + plotW" :y="H - 6" text-anchor="end">now</text>
          </g>
        </svg>
      </div>

      <!-- Holds the second column open so the airtime plot keeps the download
           plot's width and therefore its time axis. `1fr 1fr` with a single
           child still stretches that child across the track it is in; only an
           actual second item makes the first one half. -->
      <div v-if="mode === 'airtime'" class="one filler" aria-hidden="true" />

      <!-- OVER the plots, not INSTEAD of them.

           "in the last 5m", not "yet". Both states reach here and they are
           different facts: a page just opened has no record, and an adapter
           that has gone quiet has had its record age out of the window. Saying
           "yet" claimed the first in both cases, which on an adapter you had
           been watching a minute earlier reads as the chart having lost the
           data rather than the traffic having stopped.

           Same words as before; what changed is that saying them no longer
           resizes the fold. -->
      <!-- The driver's silence, not the radio's. Said before the empty message
           and instead of it: "no traffic" would be a claim about the air, and
           this is a claim about what can be asked of the hardware. -->
      <p v-if="cannotMeasure" class="none">
        This radio's driver does not report per-client airtime, so there is
        nothing to stack — not an idle radio, no measurement. The onboard
        brcmfmac chip omits the counters entirely; the USB adapters carry them.
      </p>
      <p v-else-if="empty" class="none">
        {{ mode === 'airtime' ? 'No airtime recorded on' : 'No traffic on' }}
        {{ iface }} in the last {{ span }} — either nothing has
        been on it, or whatever was has gone quiet long enough to scroll off.
      </p>
    </div>

    <!-- The legend is not optional here. Colour is doing identity work, and a
         band nobody can name is a colour with no meaning attached.

         Its box is held open even while there is nothing to name, for the same
         reason the plots are: a device appearing is exactly when the legend
         gains its first row, and that is exactly when the operator is reading
         the chart above it. -->
    <div class="legend">
      <span v-for="b in legend" :key="b.mac" class="key">
        <span class="chip" :style="{ background: b.colour }" />
        {{ b.label }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.stack { margin: 8px 0 2px; }
/* Centred OVER the pair rather than in the flow, so an adapter falling quiet
   and an adapter picking up traffic both leave every pixel below this
   component exactly where it was. The chart is the thing whose height must not
   depend on whether it has content -- a fold that grows by ~160px when a
   device starts talking pushes the cards under it down the page, and an
   operator halfway through pressing something on one of them hits the wrong
   control. Reserving the space is the whole point; the message just moves. */
.none {
  position: absolute;
  inset: 0;
  z-index: 1;
  display: grid;
  place-items: center;
  margin: 0;
  padding: 0 16px;
  text-align: center;
  font-size: 12px;
  color: var(--ink-faint);
  pointer-events: none;
}
/* The client cards' own `.dirs` rule, deliberately identical: `1fr 1fr` with a
   1px gap on the line colour, so the hairline between download and upload is
   the same hairline in a fold as on a card, and the two collapse to one column
   at the same width rather than at two nearby ones. */
.pair {
  position: relative;
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
/* Airtime is one chart, not a pair: the radio's time is a single resource and
   transmit and receive both spend it, so splitting by direction would divide a
   quantity that is not divisible that way.

   But it keeps the PAIR'S GRID and sits in the download column, rather than
   spanning the fold. Stretching it to full width was the first attempt and it
   defeated the entire point of the chart's position: a plot twice as wide
   carries the same five minutes over twice the pixels, so a spike in airtime
   sat at a different x from the throughput that caused it, and the two could
   not be read against each other by eye. Same column, same 1fr, same 1px gap,
   so the time axes are identical and a vertical line through both means one
   instant.

   The second column is held open by a filler rather than collapsed, because
   `1fr 1fr` with one child would still stretch it. */
.pair.single .filler { background: var(--panel); }
/* Below the collapse the pair is one column, so download is full width and the
   filler has nothing left to reserve -- it would stack under the plot as an
   empty panel the height of a chart. The airtime plot is full width there too,
   which is still the download plot's width, so the axes stay aligned. */
@media (max-width: 860px) { .pair.single .filler { display: none; } }
/* Drawn, but plainly not carrying anything. An empty pane at full strength
   reads as a radio carrying nothing -- the exact misreading the message above
   exists to prevent -- so the frame recedes and the words lead. */
.quiet .plot { opacity: 0.3; }
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
  align-items: center;
  gap: 4px 12px;
  margin-top: 6px;
  /* One row's worth, held whether or not there is a row. */
  min-height: 15px;
  font-size: 11px;
  color: var(--ink-dim);
}
.key { display: inline-flex; align-items: center; gap: 5px; }
.chip { width: 9px; height: 9px; border-radius: 2px; display: inline-block; }
</style>
