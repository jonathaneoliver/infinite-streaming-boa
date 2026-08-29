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
    /** Configured cap in Mbps, drawn as a threshold line. 0 = unlimited. */
    cap?: number;
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
  }>(),
  {
    cap: 0, windowMs: 300_000, now: 0, yMode: 'auto', yManual: 10,
    height: 132, titled: false, compact: false,
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
const view = computed(() => {
  const out: { t: number; v: number }[] = [];
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
    out.push({ t: props.t[i], v: props.data[i] ?? 0 });
  }
  return out;
});

/**
 * Round a maximum up to a clean number so the ticks read as round values.
 *
 * The ladder includes 1.5, 3 and 7, not just 1/2/5/10: with the coarse ladder a
 * 12 Mbps cap scaled the axis to 20 and the plot used barely half its height,
 * and a 50 Mbps peak scaled to 100 and used half of it. These extra rungs still
 * divide evenly in half for the mid gridline.
 */
const LADDER = [1, 1.5, 2, 3, 5, 7, 10];
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

const ticks = computed(() => {
  const m = yMax.value;
  return [0, m / 2, m].map((v) => ({ v, y: PAD.value.t + plotH.value - (v / m) * plotH.value }));
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

const segments = computed(() => {
  const out: { t: number; v: number }[][] = [];
  let cur: { t: number; v: number }[] = [];
  const gap = stepMs.value * 2.5;
  for (const p of view.value) {
    if (cur.length && p.t - cur[cur.length - 1].t > gap) {
      out.push(cur);
      cur = [];
    }
    cur.push(p);
  }
  if (cur.length) out.push(cur);
  return out.filter((s) => s.length > 1);
});

const paths = computed(() =>
  segments.value.map((seg) => {
    const line = seg.map((p) => `${xAt(p.t).toFixed(1)},${yAt(p.v).toFixed(1)}`).join(' ');
    const base = PAD.value.t + plotH.value;
    return {
      line,
      area: `${xAt(seg[0].t).toFixed(1)},${base} ${line} ${xAt(seg[seg.length - 1].t).toFixed(1)},${base}`,
    };
  }),
);

const capY = computed(() => (props.cap > 0 ? yAt(props.cap) : null));

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
  return {
    x: xAt(v[i].t),
    y: yAt(v[i].v),
    value: v[i].v,
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

const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2));
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
        >{{ fmt(tk.v) }}</text>
      </g>

      <g :clip-path="`url(#${gid}c)`">
        <polygon
          v-for="(p, i) in paths" :key="'a' + i"
          :points="p.area" :fill="`url(#${gid})`"
        />
        <polyline
          v-for="(p, i) in paths" :key="'l' + i"
          :points="p.line" fill="none"
          :stroke="color" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"
        />
      </g>

      <!-- The configured cap. Dashed deliberately: this IS a threshold, which is
           exactly the meaning a dashed rule carries. -->
      <g v-if="capY !== null && !compact && cap <= yMax" class="cap">
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

    <!-- Value leads, label follows: the reader already knows the series and
         wants the number. -->
    <div
      v-if="hover && !compact" class="tip num"
      :style="{ left: Math.min(Math.max(hover.x - 46, 0), w - 96) + 'px' }"
    >
      <span class="tip-val">{{ fmt(hover.value) }} Mbps</span>
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

.grid line { stroke: var(--line-soft); stroke-width: 1; }
/* Text never wears the data colour -- identity comes from the mark beside it. */
.tick-text text { fill: var(--ink-faint); font-size: 10px; }
.axis-text text { fill: var(--ink-faint); font-size: 10px; }

.cap line { stroke-width: 1; stroke-dasharray: 4 3; opacity: 0.8; }
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
.tip-ago { font-size: 10px; color: var(--ink-faint); }
</style>
