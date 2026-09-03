<script setup lang="ts">
/**
 * Throughput over time for one direction.
 *
 * Hand-rolled SVG rather than a charting library: the Pi serves this page over a
 * link the operator may have just throttled to 1 Mbps, and every dependency is
 * also a licence to audit in an open-source project.
 *
 * Rendered in real pixel coordinates from a measured container width rather
 * than a stretched viewBox. `preserveAspectRatio="none"` would distort the axis
 * text and the stroke weights along with the plot.
 *
 * One series per chart, so there is no legend: the heading names what is
 * plotted, and a one-swatch legend box would just restate it.
 *
 * Points are positioned by TIMESTAMP, not by index. Index positioning assumed
 * a contiguous 1 Hz series, which stopped being true once ranges became
 * selectable: a long range arrives averaged into wider buckets, and a gap --
 * daemon restart, sleeping laptop, device that left -- must read as a gap
 * rather than being squeezed out into a continuous line.
 *
 * The y-axis is LINEAR in every mode, deliberately. Log would compress the
 * genuine dynamic range here, but zero is a real and frequent value on this
 * box -- a player that has filled its buffer stops requesting entirely -- and a
 * log axis has no position for zero, so the single most important state would
 * be drawn at an arbitrary floor. Linear also keeps the area under the curve
 * proportional to bytes moved, and keeps a shortfall against the cap the same
 * visual distance whatever the cap is, which is what makes two devices
 * comparable. Dynamic range is handled by the axis MODES instead.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import type { YMode } from '@/types';

const props = withDefaults(
  defineProps<{
    /** Sample timestamps, ascending, in unix ms. Parallel to `data`. */
    t: number[];
    data: number[];
    color: string;
    label: string;
    /** Cap in Mbps right now, drawn when no history is supplied. 0 = unlimited. */
    cap?: number;
    /**
     * The cap at each sample, parallel to `t`. 0 means unlimited.
     *
     * When present the threshold is drawn as a STEP through history rather than
     * a rule at today's value. A pattern moves the cap while the chart is being
     * watched, and a flat line at the current one invites the reader to explain
     * a three-minute-old reaction against a cap that was not in force then --
     * which is the exact mistake this box exists to make impossible.
     */
    caps?: number[];
    /** Width of the visible window in ms. */
    windowMs?: number;
    /** Right-hand edge of the plot. Held still while the pointer is inside. */
    now?: number;
    yMode?: YMode;
    yManual?: number;
    height?: number;
    /** Render the heading. Off when the surrounding card already names it. */
    titled?: boolean;
    /**
     * Strip the chrome: no axes, no ticks, no cap label, no hover. For the
     * summary on a folded card, where the job is to show SHAPE in a couple of
     * centimetres, not to be read off.
     *
     * A mode on this component rather than a second sparkline component, so
     * direction colour and scaling cannot drift between the two places a
     * device's traffic is drawn -- direction is the most confusable property
     * in a bidirectional conditioner.
     */
    compact?: boolean;
    /** Draw the per-sample trace. */
    showLive?: boolean;
    /** Draw the rolling mean over sustainedSec. */
    showSustained?: boolean;
    /** Window for the rolling mean, in seconds. */
    sustainedSec?: number;
  }>(),
  {
    cap: 0, caps: () => [], windowMs: 300_000, now: 0, yMode: 'auto', yManual: 10,
    height: 132, titled: false, compact: false,
    showLive: true, showSustained: false, sustainedSec: 30,
  },
);

// Room for y labels on the left, the endpoint value on the right, and the time
// axis underneath. The container includes the axis band, so the card never
// grows a nested scrollbar to reveal it.
// r is sized for the cap label ("cap 100.0" at 10px monospace is ~54px plus its
// 6px offset). Too small and the last character is clipped, which is worse than
// no label at all.
const PAD = computed(() =>
  props.compact
    ? { l: 0, r: 0, t: 2, b: 2 }
    : { l: 40, r: 68, t: 10, b: 20 },
);

const wrap = ref<HTMLElement | null>(null);
const w = ref(200);
let ro: ResizeObserver | null = null;

onMounted(() => {
  if (!wrap.value) return;
  ro = new ResizeObserver((entries) => {
    const cw = entries[0]?.contentRect.width ?? 0;
    if (cw > 0) w.value = cw;
  });
  ro.observe(wrap.value);
});
onBeforeUnmount(() => ro?.disconnect());

const plotW = computed(() => Math.max(40, w.value - PAD.value.l - PAD.value.r));
const plotH = computed(() => props.height - PAD.value.t - PAD.value.b);

const edge = computed(() => props.now || Date.now());
const start = computed(() => edge.value - props.windowMs);

/**
 * The samples inside the window, plus the one immediately before it.
 *
 * That extra point is what lets the line enter from the left edge instead of
 * starting in mid-air a few pixels in; it is clipped by the plot bounds.
 */
function viewOf(values: (number | null)[]) {
  const out: { t: number; v: number | null }[] = [];
  let firstIn = -1;
  for (let i = 0; i < props.t.length; i++) {
    if (props.t[i] >= start.value) {
      firstIn = i;
      break;
    }
  }
  if (firstIn < 0) return out;
  const from = Math.max(0, firstIn - 1);
  for (let i = from; i < props.t.length; i++) {
    out.push({ t: props.t[i], v: values[i] ?? null });
  }
  return out;
}

const view = computed(() =>
  viewOf(props.data).map((p) => ({ t: p.t, v: p.v ?? 0 })),
);

/**
 * Round a maximum up to a clean number, close enough above the data that the
 * plot uses its height.
 *
 * Every rung here was added to close a gap that was wasting vertical space. The
 * first pass added 1.5, 3 and 7 to a 1/2/5/10 ladder, because a 12 Mbps cap
 * scaled the axis to 20 and used barely half of it. The rungs below close what
 * that pass left: a 15.4 Mbps peak still had nothing between 15 and 20 and took
 * 20, leaving the top quarter of the chart empty.
 *
 * A finer ladder used to cost round labels, since the grid had to divide
 * whatever maximum it was given into halves that the axis could print. It no
 * longer does: tick precision follows the step's own magnitude (see
 * decimalsFor), so fifths and tenths of every rung here are exact -- 1.8 gives
 * 0.36 and 0.18, 2.5 gives 0.5 and 0.25. The grid stays at six lines and
 * eleven; only the wasted space goes.
 *
 * Fit against the old ladder, measured across representative peaks: 77% -> 86%,
 * 63% -> 79%, 61% -> 76%, 68% -> 85%.
 */
const LADDER = [1, 1.2, 1.5, 1.8, 2, 2.5, 3, 4, 5, 6, 7, 8, 10];
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / mag;
  return (LADDER.find((s) => n <= s) ?? 10) * mag;
}

const peak = computed(() =>
  view.value.length ? Math.max(...view.value.map((p) => p.v)) : 0,
);

const yMax = computed(() => {
  if (props.yMode === 'manual' && props.yManual > 0) {
    // Not rounded up: a fixed ceiling is a number the operator typed in order
    // to compare two devices, and quietly moving it to 15 would break that.
    return props.yManual;
  }
  // "To cap" falls back to auto on an unconditioned device rather than drawing
  // an empty axis: a cap of zero means unlimited, which is not a ceiling.
  if (props.yMode === 'cap' && props.cap > 0) return niceMax(props.cap * 1.15);
  // The auto scale always includes the cap, so the headroom between "what it is
  // doing" and "what it is allowed to do" stays visible even when the device is
  // idle -- that gap is the whole question this chart answers.
  return niceMax(Math.max(peak.value * 1.15, props.cap * 1.15, 0.1));
});

// A fixed ceiling can sit below the traffic. The line clamps to the top of the
// pane, which on its own looks like a plateau in the data rather than a limit
// of the view, so it is stated.
const clipped = computed(() => peak.value > yMax.value * 1.001);

const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2));

/**
 * How many gridlines to draw, from the height of the plot rather than from any
 * setting.
 *
 * Three lines across 166px is a readable grid; the same three across 362px is
 * an empty pane with a line top, middle and bottom, which throws away the
 * resolution a taller chart was chosen for. Spacing is what should stay
 * constant, so the count is derived from it.
 *
 * The divisor is chosen from those that keep every label round, tested rather
 * than assumed: a value is admissible only if it survives a round-trip through
 * the same formatter the axis prints with. That rejects thirds of 10 and
 * quarters of 1.5, which would otherwise put 3.33 and 0.38 on the axis and make
 * the grid harder to read than having fewer lines. It is the same reasoning as
 * the LADDER above, applied to the interval instead of the maximum.
 *
 * 34px lands on fifths at the default height and tenths when tall -- six lines,
 * then eleven, the short grid with its midpoints filled in.
 *
 * Those two divisors are why this spacing and not a looser one. Fifths and
 * tenths are round for every value niceMax can return and for any sane ceiling
 * typed by hand, so in practice EVERY chart on the page gets the same grid.
 * That matters more than the individual choice: this toolbar's whole premise is
 * that one range and one axis rule make two devices comparable at a glance, and
 * a grid that thins out on one card because its maximum happened to divide
 * differently works against exactly that. A looser 42px target picked quarters
 * for a 20 Mbps axis and tenths for a 0.5 Mbps one, so two charts a few pixels
 * apart carried nine lines and eleven.
 *
 * Still a target rather than a guarantee, because roundness wins: a 0.15 Mbps
 * axis in tenths is 0.015, which prints as 0.01, so it keeps fifths and stays
 * at six lines.
 */
const TICK_SPACING_PX = 34;
/**
 * Decimals needed to write v exactly, or -1 if more than `max` would be.
 *
 * The axis label formatter cannot be the test for this. It picks precision from
 * each value's own magnitude -- two decimals under ten, one under a hundred --
 * which is right for a single reading and wrong for a column of them, because
 * whether a tick is representable then depends on where it happens to fall. A
 * fixed ceiling of 16.6 in fifths is 3.32, 6.64, 9.96, 13.28: the first three
 * are exact and the fourth crosses ten and prints as 13.3, so the whole
 * division was rejected and the axis fell back to halves -- three gridlines on
 * a chart asking for eleven. Precision belongs to the STEP, and every tick on
 * one axis shares it.
 */
function decimalsFor(v: number): number {
  if (!(v > 0)) return -1;
  // Two decimals RELATIVE TO THE STEP'S OWN MAGNITUDE, not two absolute ones.
  // A hard cap of two is itself a scale: it makes a 0.015 step unwritable while
  // a 1.5 one is fine, so a small axis lost gridlines for no reason but its
  // units. Every maximum niceMax returns divides into tenths that need exactly
  // one decimal more than the maximum itself, which is why this is the whole
  // fix -- the range was never the problem.
  const max = Math.min(6, Math.max(2, 2 - Math.floor(Math.log10(v))));
  for (let d = 0; d <= max; d++) {
    if (Math.abs(Number(v.toFixed(d)) - v) < 1e-9) return d;
  }
  return -1;
}

const tickDivisions = computed(() => {
  const m = yMax.value;
  if (!(m > 0)) return 2;
  const ideal = plotH.value / TICK_SPACING_PX;
  const usable = [2, 3, 4, 5, 6, 8, 10].filter((n) => decimalsFor(m / n) >= 0);
  if (!usable.length) return 2;
  return usable.reduce((a, b) => (Math.abs(b - ideal) < Math.abs(a - ideal) ? b : a));
});

/** One precision for the whole axis, from the step it is drawn in. */
const tickDecimals = computed(() =>
  Math.max(0, decimalsFor(yMax.value / tickDivisions.value)),
);

const ticks = computed(() => {
  const m = yMax.value;
  const n = tickDivisions.value;
  return Array.from({ length: n + 1 }, (_, i) => {
    const v = (m * i) / n;
    return { v, y: PAD.value.t + plotH.value - (v / m) * plotH.value };
  });
});

const xAt = (t: number) =>
  PAD.value.l + ((t - start.value) / props.windowMs) * plotW.value;
const yAt = (v: number) =>
  PAD.value.t + plotH.value - Math.max(0, Math.min(1, v / yMax.value)) * plotH.value;

/**
 * Break the line where the data stops.
 *
 * The threshold is derived from the samples themselves rather than assumed,
 * because one point covers a second on a short range and several on a long one.
 * Joining across a real outage would draw a straight line through time the box
 * has no observations for, which is the chart telling a lie about the past.
 */
const stepMs = computed(() => {
  const v = view.value;
  if (v.length < 3) return 1000;
  const diffs: number[] = [];
  for (let i = 1; i < v.length; i++) diffs.push(v[i].t - v[i - 1].t);
  diffs.sort((a, b) => a - b);
  return Math.max(500, diffs[Math.floor(diffs.length / 2)]);
});

const segments = computed(() => segmentsOf(view.value));

/**
 * Split a series wherever the record stops: a real gap in time, or a point the
 * series itself has no value for.
 */
function segmentsOf(points: { t: number; v: number | null }[]) {
  const out: { t: number; v: number }[][] = [];
  let cur: { t: number; v: number }[] = [];
  const gap = stepMs.value * 2.5;
  for (const p of points) {
    const broken = p.v === null || (cur.length && p.t - cur[cur.length - 1].t > gap);
    if (broken && cur.length) {
      out.push(cur);
      cur = [];
    }
    if (p.v !== null) cur.push({ t: p.t, v: p.v });
  }
  if (cur.length) out.push(cur);
  return out.filter((s) => s.length > 1);
}

function pathsOf(segs: { t: number; v: number }[][]) {
  return segs.map((seg) => {
    const line = seg.map((p) => `${xAt(p.t).toFixed(1)},${yAt(p.v).toFixed(1)}`).join(' ');
    const base = PAD.value.t + plotH.value;
    return {
      line,
      area: `${xAt(seg[0].t).toFixed(1)},${base} ${line} ${xAt(seg[seg.length - 1].t).toFixed(1)},${base}`,
    };
  });
}

const paths = computed(() => pathsOf(segments.value));

/**
 * The sustained series: what the link delivered over the trailing window,
 * rather than during the last sample.
 *
 * # Bytes over time, not a mean of rates
 *
 * Each sample is a rate the daemon derived over its own interval, so the bytes
 * that sample represents are `rate x interval`. Summing those and dividing by
 * the summed intervals is therefore the byte delta over the time delta -- the
 * question actually being asked -- and it stays correct when the intervals are
 * not all the same width.
 *
 * They frequently are not. The server decimates long ranges into wider buckets,
 * so a window near a range boundary can straddle two widths, and any pause
 * leaves an interval far longer than the rest. An unweighted mean of the rates
 * would silently over-weight the short intervals. With uniform 1 Hz samples the
 * two agree exactly, which is why the difference is easy to miss.
 *
 * Deriving from the rate series rather than from raw counters also inherits the
 * daemon's counter-epoch handling: a class recreated by a policy write resets
 * its bytes, and `rate` already reports 0 rather than a negative spike.
 *
 * # What it refuses to draw
 *
 * Nothing across an outage: an interval longer than the gap threshold restarts
 * the accumulator, because averaging over time the box has no observations for
 * would invent throughput. And nothing until the window is at least half full,
 * so the line begins where it means something instead of opening on a mean of
 * two samples that reads wildly high or low.
 */
const sustained = computed<(number | null)[]>(() => {
  const t = props.t;
  const d = props.data;
  const win = props.sustainedSec * 1000;
  const gap = stepMs.value * 2.5;
  const out: (number | null)[] = new Array(t.length).fill(null);

  let lo = 1; // first interval still inside the window
  let bytes = 0; // rate x interval, summed
  let dur = 0; // interval widths, summed

  for (let i = 1; i < t.length; i++) {
    const dt = t[i] - t[i - 1];
    if (dt > gap) {
      lo = i + 1;
      bytes = 0;
      dur = 0;
      continue;
    }
    bytes += (d[i] ?? 0) * dt;
    dur += dt;
    while (lo < i && dur > win) {
      const dlo = t[lo] - t[lo - 1];
      bytes -= (d[lo] ?? 0) * dlo;
      dur -= dlo;
      lo++;
    }
    if (dur >= win / 2) out[i] = bytes / dur;
  }
  return out;
});

/**
 * Whether a rolling mean still says anything.
 *
 * Once the server's buckets approach the window, each plotted point is already
 * a mean over a comparable span and this line converges on the live one --
 * drawing a mean of means while implying it is showing something new.
 */
const sustainedWorthDrawing = computed(
  () => props.sustainedSec * 1000 >= stepMs.value * 2,
);

const showSustainedLine = computed(
  () => props.showSustained && !props.compact && sustainedWorthDrawing.value,
);

const sustainedPaths = computed(() =>
  showSustainedLine.value ? pathsOf(segmentsOf(viewOf(sustained.value))) : [],
);

const capY = computed(() => (props.cap > 0 ? yAt(props.cap) : null));

/*
 * The cap over time, as a step.
 *
 * Stepped, never sloped, because a cap is set to a value and holds it: an
 * interpolated cap line would draw a ramp the kernel never applied, and the
 * vertical edge is the moment being lined up against a player's reaction.
 *
 * Unlimited breaks the line rather than drawing at zero. A cap of 0 means "no
 * ceiling", and a rule along the floor would read as "throttled to nothing" --
 * the exact opposite. segmentsOf already splits on nulls, so the gap falls out.
 */
const capSteps = computed(() => {
  if (!props.caps.length) return [];
  const pts = viewOf(props.caps).map((p) => ({
    t: p.t,
    v: p.v && p.v > 0 ? p.v : null,
  }));
  const out: { t: number; v: number | null }[] = [];
  for (let i = 0; i < pts.length; i++) {
    // The held value up to this instant, then the change at it.
    if (i > 0 && pts[i].v !== pts[i - 1].v) {
      out.push({ t: pts[i].t, v: pts[i - 1].v });
    }
    out.push(pts[i]);
  }
  return out;
});

const capPaths = computed(() => pathsOf(segmentsOf(capSteps.value)));

/** Whether the cap moved inside the window: a flat one needs no step drawing. */
const capVaries = computed(() => {
  const vs = capSteps.value;
  return vs.some((p) => p.v !== vs[0].v);
});

/** The cap at the right-hand edge, which is what the label names. */
const capNow = computed(() => {
  const vs = capSteps.value;
  for (let i = vs.length - 1; i >= 0; i--) {
    if (vs[i].v !== null) return vs[i].v as number;
  }
  return 0;
});

const lastPoint = computed(() => view.value[view.value.length - 1] ?? null);
const last = computed(() => lastPoint.value?.v ?? 0);
const avg = computed(() =>
  view.value.length ? view.value.reduce((a, p) => a + p.v, 0) / view.value.length : 0,
);

const spanLabel = computed(() => {
  const secs = Math.round(props.windowMs / 1000);
  return secs >= 3600 ? `${secs / 3600}h` : secs >= 60 ? `${secs / 60}m` : `${secs}s`;
});

/* --- hover ------------------------------------------------------------- */
// The crosshair finds the X: the reader aims at a moment in time, never at a
// 2px line. The overlay spans the whole plot, so the pointer only has to be at
// the right horizontal position.
const hoverIdx = ref<number | null>(null);

function onMove(e: PointerEvent) {
  const v = view.value;
  if (v.length < 2 || !wrap.value) return;
  const x = e.clientX - wrap.value.getBoundingClientRect().left;
  const want = start.value + ((x - PAD.value.l) / plotW.value) * props.windowMs;
  let best = 0;
  let bestD = Infinity;
  for (let i = 0; i < v.length; i++) {
    const d = Math.abs(v[i].t - want);
    if (d < bestD) {
      bestD = d;
      best = i;
    }
  }
  hoverIdx.value = best;
}

function ago(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s <= 0) return 'now';
  if (s < 60) return `${s}s ago`;
  return `${Math.round(s / 60)}m ago`;
}

const hover = computed(() => {
  const i = hoverIdx.value;
  const v = view.value;
  if (i === null || i >= v.length) return null;
  // The sustained value for the same instant, so the tooltip answers both
  // "what was it doing" and "what was it sustaining" without a second gesture.
  const s = showSustainedLine.value ? (viewOf(sustained.value)[i]?.v ?? null) : null;
  // The cap AT that instant, not the one in force now.
  //
  // Reading a chart is asking why the line did what it did, and the answer is
  // usually the cap -- but under a pattern the cap has moved since, so the
  // current value is the one number guaranteed not to explain a past moment.
  // Comes from the same recorded series the stepped line is drawn from, so the
  // tooltip and the dashes cannot disagree.
  const capAt = props.caps.length ? (viewOf(props.caps)[i]?.v ?? null) : null;
  return {
    x: xAt(v[i].t),
    y: yAt(v[i].v),
    value: v[i].v,
    sustained: s,
    // 0 is unlimited, which is an absence of a cap rather than a cap of zero:
    // shown as such, or the reader is told the link was throttled to nothing at
    // the exact moment it was throttled to nothing at all.
    cap: capAt !== null && capAt > 0 ? capAt : null,
    uncapped: capAt !== null && capAt <= 0,
    ago: ago(edge.value - v[i].t),
  };
});

const emit = defineEmits<{ (e: 'hovering', v: boolean): void }>();
function enter() {
  if (!props.compact) emit('hovering', true);
}
function leave() {
  hoverIdx.value = null;
  emit('hovering', false);
}

const gid = `g${Math.random().toString(36).slice(2, 8)}`;
</script>

<template>
  <div class="chart" ref="wrap">
    <div v-if="!compact" class="chart-head">
      <span v-if="titled" class="chart-title">{{ label }}</span>

      <span class="stats num">
        <span v-if="clipped" class="clip" title="Traffic exceeds the fixed y-axis maximum">
          clipped ·
        </span>
        peak {{ fmt(peak) }} · avg {{ fmt(avg) }} · {{ spanLabel }}
      </span>
    </div>

    <svg
      :width="w" :height="height" class="plot"
      @pointerenter="enter"
      @pointermove="compact ? undefined : onMove($event)"
      @pointerleave="leave"
      role="img" :aria-label="`${label}: ${fmt(last)} megabits per second`"
    >
      <defs>
        <linearGradient :id="gid" x1="0" y1="0" x2="0" y2="1">
          <!-- A wash, never a saturated block. -->
          <stop offset="0%" :stop-color="color" stop-opacity="0.16" />
          <stop offset="100%" :stop-color="color" stop-opacity="0.02" />
        </linearGradient>
        <!-- Points just outside the window are drawn so the line enters from
             the edge; this keeps them from spilling over the axis labels. -->
        <clipPath :id="`${gid}c`">
          <rect :x="PAD.l" :y="PAD.t" :width="plotW" :height="plotH" />
        </clipPath>
      </defs>

      <!-- Gridlines: solid hairlines one step off the surface, recessive.
           Dashing here would read as "threshold" when it is only a grid. -->
      <g v-if="!compact" class="grid">
        <line
          v-for="tk in ticks" :key="'g' + tk.v"
          :x1="PAD.l" :x2="PAD.l + plotW" :y1="tk.y" :y2="tk.y"
        />
      </g>
      <g v-if="!compact" class="tick-text num">
        <text
          v-for="tk in ticks" :key="'t' + tk.v"
          :x="PAD.l - 8" :y="tk.y + 3" text-anchor="end"
        >{{ tk.v.toFixed(tickDecimals) }}</text>
      </g>

      <!-- Both series are the SAME hue: they are the same quantity over
           different spans, and direction is what colour means everywhere else
           on this page. They are told apart by weight instead -- the live trace
           thins and fades when the mean is drawn over it, so the eye reads the
           steady line as the trend and the spiky one as the detail. Not dashed:
           a dashed rule already means "threshold" here, which is the cap.

           When the mean is off the live trace returns to full weight, so
           turning the feature off leaves the chart exactly as it was. -->
      <g :clip-path="`url(#${gid}c)`">
        <polygon
          v-for="(p, i) in paths" :key="'a' + i"
          :points="p.area" :fill="`url(#${gid})`"
        />
        <polyline
          v-for="(p, i) in paths" :key="'l' + i"
          v-show="showLive"
          :points="p.line" fill="none"
          :stroke="color"
          :stroke-width="showSustainedLine ? 1.25 : 2"
          :opacity="showSustainedLine ? 0.45 : 1"
          stroke-linejoin="round" stroke-linecap="round"
        />
        <polyline
          v-for="(p, i) in sustainedPaths" :key="'s' + i"
          :points="p.line" fill="none"
          :stroke="color" stroke-width="2.25"
          stroke-linejoin="round" stroke-linecap="round"
        />
      </g>

      <!-- The cap as it was, when it moved. Dashed deliberately: this IS a
           threshold, which is exactly the meaning a dashed rule carries. The
           label names the value at the right-hand edge, because that is the one
           the endpoint marker sits against. -->
      <g v-if="capVaries && !compact" class="cap">
        <polyline
          v-for="(d, i) in capPaths" :key="i" :points="d.line" fill="none"
          :stroke="color" vector-effect="non-scaling-stroke"
        />
        <text
          v-if="capNow > 0 && capNow <= yMax"
          :x="PAD.l + plotW + 6" :y="yAt(capNow) + 3" class="cap-text num"
        >
          cap {{ fmt(capNow) }}
        </text>
      </g>

      <!-- A cap that has not moved is a rule, not a step: one line and one
           label, which is cheaper to read than a flat path with the same
           meaning. -->
      <g v-else-if="capY !== null && !compact && cap <= yMax" class="cap">
        <line :x1="PAD.l" :x2="PAD.l + plotW" :y1="capY" :y2="capY" :stroke="color" />
        <text :x="PAD.l + plotW + 6" :y="capY + 3" class="cap-text num">
          cap {{ fmt(cap) }}
        </text>
      </g>

      <!-- Endpoint marker, with a 2px surface ring so it stays legible where it
           crosses the cap line. No text label here: the pane heading directly
           above is already the direct label for this value, and a second copy
           collided with the cap label in the right margin. -->
      <g v-if="lastPoint && view.length > 1">
        <circle
          :cx="xAt(lastPoint.t)" :cy="yAt(lastPoint.v)" r="4"
          :fill="color" stroke="var(--panel)" stroke-width="2"
        />
      </g>

      <g v-if="hover" class="crosshair">
        <line :x1="hover.x" :x2="hover.x" :y1="PAD.t" :y2="PAD.t + plotH" />
        <circle
          :cx="hover.x" :cy="hover.y" r="4"
          :fill="color" stroke="var(--panel)" stroke-width="2"
        />
      </g>

      <g v-if="!compact" class="axis-text">
        <text :x="PAD.l" :y="height - 6">−{{ spanLabel }}</text>
        <text :x="PAD.l + plotW" :y="height - 6" text-anchor="end">now</text>
      </g>
    </svg>

    <!-- The key sits UNDER the plot it describes.
         There is a shared one in the toolbar, but a legend is read while
         looking at a chart, and on a page of device cards that one has scrolled
         off the top by the time you are looking at any of them. Below rather
         than above because the chart is the thing being read: the key is
         referred to on the way out, not on the way in.

         Down here the swatches can also be the chart's OWN colour -- in the
         toolbar a swatch must serve both directions at once and so has to stay
         neutral, whereas here blue means downlink because downlink is what is
         plotted. And the cap gets a key at all, which it never had: three
         things are drawn on a conditioned chart and only two were ever named.

         Not on a folded row. There the chart is a two-centimetre sparkline
         whose job is shape, the mean and cap lines are not drawn at all, and
         the key collapses to a swatch and the word "live" -- restating the one
         thing that is obviously being shown, on the row where vertical space
         is the whole point. Every other decoration here is already guarded on
         !compact; this one was missed. -->
    <span v-if="!compact" class="key" role="group" aria-label="Series shown">
          <span v-if="showLive" class="key-item" title="Throughput during each sample. Bursty by nature: a player fetches a segment, then idles.">
            <svg width="16" height="8" aria-hidden="true">
              <line x1="0" y1="4" x2="16" y2="4" :stroke="color"
                    :stroke-width="showSustainedLine ? 1.25 : 2"
                    :opacity="showSustainedLine ? 0.45 : 1" />
            </svg>live
          </span>
          <span v-if="showSustainedLine" class="key-item" :title="`Bytes delivered over the trailing ${sustainedSec} seconds, divided by that time. What the device is actually sustaining across segment fetches.`">
            <svg width="16" height="8" aria-hidden="true">
              <line x1="0" y1="4" x2="16" y2="4" :stroke="color" stroke-width="2.25" />
            </svg>{{ sustainedSec }}s mean
          </span>
          <span v-if="capY !== null && cap <= yMax" class="key-item"
                title="The rate being enforced right now, which is not always the rate you saved: a ladder sweep drives the cap itself while it runs.">
            <svg width="16" height="8" aria-hidden="true">
              <line x1="0" y1="4" x2="16" y2="4" :stroke="color"
                    stroke-width="1" stroke-dasharray="4 3" opacity="0.8" />
            </svg>cap
          </span>
        </span>

    <!-- Value leads, label follows: the reader already knows the series and
         wants the number. -->
    <div
      v-if="hover && !compact" class="tip num"
      :style="{ left: Math.min(Math.max(hover.x - 46, 0), w - 96) + 'px' }"
    >
      <span class="tip-val">{{ fmt(hover.value) }} Mbps</span>
      <span v-if="hover.sustained !== null" class="tip-sus">
        {{ fmt(hover.sustained) }} sustained
      </span>
      <span v-if="hover.cap !== null" class="tip-cap">
        {{ fmt(hover.cap) }} cap
      </span>
      <span v-else-if="hover.uncapped" class="tip-cap">uncapped</span>
      <span class="tip-key"><i :style="{ background: color }"></i>{{ label }}</span>
      <span class="tip-ago">{{ hover.ago }}</span>
    </div>
  </div>
</template>

<style scoped>
.chart { position: relative; min-width: 0; }
.chart-head {
  display: flex; align-items: baseline; gap: 10px;
  margin-bottom: 2px;
}
.chart-title {
  font-size: 11px; letter-spacing: 0.08em; text-transform: uppercase;
  color: var(--ink-dim);
}
.stats { margin-left: auto; font-size: 11px; color: var(--ink-faint); }
.clip { color: var(--warn, #d9a441); }
.plot { display: block; touch-action: none; }

/* Swatches are the real strokes at the real weights, so the key cannot drift
   from the plot: change a width in the SVG above and the swatch is wrong in a
   way somebody will notice. */
.key {
  display: flex;
  align-items: center;
  gap: 10px;
  /* Sits under the plot, indented to the plot's left edge rather than the
     card's, so it lines up with what it is describing. */
  margin: 2px 0 0 34px;
}
.key-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--ink-dim);
  white-space: nowrap;
}
.key-item svg { overflow: visible; }

.grid line { stroke: var(--line-soft); stroke-width: 1; }
/* Text never wears the data colour -- identity comes from the mark beside it. */
.tick-text text { fill: var(--ink-faint); font-size: 10px; }
.axis-text text { fill: var(--ink-faint); font-size: 10px; }

/* Both shapes: the cap is a <line> when it never moved and a <polyline> when it
   stepped, and they must look identical -- a threshold that changes weight
   depending on whether a pattern happened to run is two different meanings for
   one thing. Styling only `line` left the stepped version solid and full
   weight, reading as a data series rather than as a threshold. */
.cap line,
.cap polyline { stroke-width: 1; stroke-dasharray: 4 3; opacity: 0.8; fill: none; }
.cap-text { fill: var(--ink-dim); font-size: 10px; }
.crosshair line { stroke: var(--ink-faint); stroke-width: 1; opacity: 0.6; }

.tip {
  position: absolute; bottom: 26px;
  display: flex; flex-direction: column; gap: 1px;
  padding: 5px 8px;
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: 5px;
  pointer-events: none;
  white-space: nowrap;
  box-shadow: 0 4px 12px rgb(0 0 0 / 0.4);
}
.tip-val { font-size: 12px; font-weight: 600; color: var(--ink); }
.tip-key { font-size: 10px; color: var(--ink-dim); display: flex; align-items: center; gap: 5px; }
/* A short stroke of the series colour keys the row -- at tooltip density a
   filled box is data-weight ink doing a label's job. */
.tip-key i { width: 10px; height: 2px; border-radius: 1px; display: inline-block; }
/* Subordinate to the instantaneous value above it, in the same order the two
   lines sit in the plot's visual hierarchy. */
/* Both are the same kind of thing: a secondary quantity about the instant the
   headline value describes, so they share one rule rather than being two that
   have to be kept in step. The cap first carried a dashed underline echoing the
   cap line -- a decoration earning its keep only if you already knew what it
   meant, on the densest text in the interface. */
.tip-sus,
.tip-cap { font-size: 11px; color: var(--ink-dim); }
.tip-ago { font-size: 10px; color: var(--ink-faint); }
</style>
