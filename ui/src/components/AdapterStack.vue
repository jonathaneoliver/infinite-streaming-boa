<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import type { Series } from '@/types';
import { clientColour } from '@/composables/useClientColours';
import { axisDecimals, axisTicks, niceMax, spanLabel } from '@/composables/chartAxis';

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
  /** Every device that might be on it, for names and colours. */
  clients: { mac: string; label: string }[];
  /** Per-device history, keyed by MAC -- the same object the client cards read. */
  series: Record<string, Series>;
  /** The window on screen, shared with the client charts so the two compare. */
  rangeSec: number;
}>();

/* TrafficChart's own non-compact values, so a fold and a card line up. */
const H = 132;
const PAD = { l: 40, r: 68, t: 10, b: 20 };
const PLOT_H = H - PAD.t - PAD.b;

const wrap = ref<HTMLElement | null>(null);
const w = ref(320);
let ro: ResizeObserver | null = null;
onMounted(() => {
  if (!wrap.value) return;
  ro = new ResizeObserver((e) => {
    const cw = e[0]?.contentRect.width ?? 0;
    if (cw > 0) w.value = cw;
  });
  ro.observe(wrap.value);
});
onBeforeUnmount(() => ro?.disconnect());

/** Half the row each, less the gap between them. Below the point where that
 *  leaves a plot too narrow to read, the grid wraps to one column and each
 *  chart takes the full width instead. */
const oneCol = computed(() => w.value < 640);
const chartW = computed(() =>
  Math.max(240, oneCol.value ? w.value : Math.floor((w.value - 12) / 2)),
);
const plotW = computed(() => Math.max(40, chartW.value - PAD.l - PAD.r));

const windowMs = computed(() => props.rangeSec * 1000);
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
  for (const c of props.clients) {
    const s = props.series[c.mac];
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
 * The window, taken from the DATA rather than from the clock.
 *
 * This was `Date.now()` inside a computed, which is a trap worth naming: a
 * computed re-runs only when a reactive dependency changes, and the wall clock
 * is not one. The window froze at page load while samples kept arriving, so
 * every new point mapped past the right-hand edge -- measured at x = 1035 in a
 * 700-wide viewBox before this was fixed, which drew the plot off its own pane.
 *
 * Anchoring to the newest sample makes the window reactive with the series that
 * fills it, and is the more honest edge anyway: the right of the plot is the
 * last thing recorded, not the moment the page happened to be rendered.
 */
const edge = computed(() => {
  const t = allTimes.value;
  return t.length ? t[t.length - 1] : Date.now();
});
const start = computed(() => edge.value - windowMs.value);
const grid = computed(() => allTimes.value.filter((t) => t >= start.value));

function bandsFor(dir: 'down' | 'up'): Band[] {
  const at = new Map(grid.value.map((t, i) => [t, i]));
  const out: Band[] = [];
  for (const c of props.clients) {
    const s = props.series[c.mac];
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
    out.push({ mac: c.mac, label: c.label, colour: clientColour(c.mac), vals, peak });
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
  PAD.t + PLOT_H - Math.max(0, Math.min(1, v / max)) * PLOT_H;

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
    const dp = axisDecimals(max, PLOT_H);
    return {
      dir,
      title: dir === 'down' ? 'download' : 'upload',
      bands,
      max,
      peak,
      avg: tot.length ? tot.reduce((a, b) => a + b, 0) / tot.length : 0,
      ticks: axisTicks(max, PLOT_H).map((t) => ({
        key: t.v,
        y: PAD.t + PLOT_H - t.frac * PLOT_H,
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
  <div class="stack" ref="wrap">
    <p v-if="empty" class="none">
      Nothing recorded on {{ iface }} yet — a device has to be associated and
      moving traffic for a second or two before there is a shape to draw.
    </p>

    <template v-else>
      <div class="pair" :class="{ single: oneCol }">
        <div v-for="c in charts" :key="c.dir" class="one">
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
/* Side by side. The single class comes from the measured width rather than a
   media query, because what matters is how wide this FOLD is, not the page. */
.pair { display: flex; gap: 12px; }
.pair.single { flex-direction: column; }
.one { min-width: 0; }
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
