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
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';

const props = withDefaults(
  defineProps<{
    data: number[];
    color: string;
    label: string;
    /** Configured cap in Mbps, drawn as a threshold line. 0 = unlimited. */
    cap?: number;
    /** Seconds between samples, for the time axis. */
    interval?: number;
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
  { cap: 0, interval: 1, height: 132, titled: false, compact: false },
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

/**
 * Round a maximum up to a clean number so the ticks read as round values.
 *
 * The ladder includes 1.5 and 3, not just 1/2/5/10: with the coarse ladder a
 * 12 Mbps cap scaled the axis to 20 and the plot used barely half its height.
 * These extra rungs still divide evenly in half for the mid gridline.
 */
const LADDER = [1, 1.5, 2, 3, 5, 10];
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / mag;
  return (LADDER.find((s) => n <= s) ?? 10) * mag;
}

// The scale follows the data, but always includes the cap so the headroom
// between "what it is doing" and "what it is allowed to do" stays visible --
// that gap is the whole question this chart answers.
const yMax = computed(() => {
  const peak = Math.max(0, ...props.data);
  return niceMax(Math.max(peak * 1.15, props.cap * 1.15, 0.1));
});

const ticks = computed(() => {
  const m = yMax.value;
  return [0, m / 2, m].map((v) => ({ v, y: PAD.value.t + plotH.value - (v / m) * plotH.value }));
});

const xAt = (i: number) => {
  const n = props.data.length;
  if (n < 2) return PAD.value.l;
  return PAD.value.l + (i / (n - 1)) * plotW.value;
};
const yAt = (v: number) =>
  PAD.value.t + plotH.value - Math.min(1, v / yMax.value) * plotH.value;

const linePts = computed(() =>
  props.data.map((v, i) => `${xAt(i).toFixed(1)},${yAt(v).toFixed(1)}`).join(' '),
);
const areaPts = computed(() => {
  if (props.data.length < 2) return '';
  const base = PAD.value.t + plotH.value;
  return `${PAD.value.l},${base} ${linePts.value} ${xAt(props.data.length - 1).toFixed(1)},${base}`;
});

const capY = computed(() => (props.cap > 0 ? yAt(props.cap) : null));

const last = computed(() => props.data[props.data.length - 1] ?? 0);
const peak = computed(() => (props.data.length ? Math.max(...props.data) : 0));
const avg = computed(() =>
  props.data.length ? props.data.reduce((a, b) => a + b, 0) / props.data.length : 0,
);

const spanLabel = computed(() => {
  const secs = props.data.length * props.interval;
  return secs >= 60 ? `${Math.round(secs / 60)}m` : `${secs}s`;
});

/* --- hover ------------------------------------------------------------- */
// The crosshair finds the X: the reader aims at a moment in time, never at a
// 2px line. The overlay spans the whole plot, so the pointer only has to be at
// the right horizontal position.
const hoverIdx = ref<number | null>(null);

function onMove(e: PointerEvent) {
  const n = props.data.length;
  if (n < 2 || !wrap.value) return;
  const x = e.clientX - wrap.value.getBoundingClientRect().left;
  const frac = (x - PAD.value.l) / plotW.value;
  hoverIdx.value = Math.max(0, Math.min(n - 1, Math.round(frac * (n - 1))));
}

const hover = computed(() => {
  const i = hoverIdx.value;
  if (i === null || i >= props.data.length) return null;
  const agoSec = (props.data.length - 1 - i) * props.interval;
  return {
    x: xAt(i),
    y: yAt(props.data[i]),
    value: props.data[i],
    ago: agoSec === 0 ? 'now' : agoSec < 60 ? `${agoSec}s ago` : `${Math.round(agoSec / 60)}m ago`,
  };
});

const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2));
const gid = `g${Math.random().toString(36).slice(2, 8)}`;
</script>

<template>
  <div class="chart" ref="wrap">
    <div v-if="!compact" class="chart-head">
      <span v-if="titled" class="chart-title">{{ label }}</span>
      <span class="stats num">
        peak {{ fmt(peak) }} · avg {{ fmt(avg) }} · {{ spanLabel }}
      </span>
    </div>

    <svg
      :width="w" :height="height" class="plot"
      @pointermove="compact ? undefined : onMove($event)"
      @pointerleave="hoverIdx = null"
      role="img" :aria-label="`${label}: ${fmt(last)} megabits per second`"
    >
      <defs>
        <linearGradient :id="gid" x1="0" y1="0" x2="0" y2="1">
          <!-- A wash, never a saturated block. -->
          <stop offset="0%" :stop-color="color" stop-opacity="0.16" />
          <stop offset="100%" :stop-color="color" stop-opacity="0.02" />
        </linearGradient>
      </defs>

      <!-- Gridlines: solid hairlines one step off the surface, recessive.
           Dashing here would read as "threshold" when it is only a grid. -->
      <g v-if="!compact" class="grid">
        <line
          v-for="t in ticks" :key="'g' + t.v"
          :x1="PAD.l" :x2="PAD.l + plotW" :y1="t.y" :y2="t.y"
        />
      </g>
      <g v-if="!compact" class="tick-text num">
        <text
          v-for="t in ticks" :key="'t' + t.v"
          :x="PAD.l - 8" :y="t.y + 3" text-anchor="end"
        >{{ fmt(t.v) }}</text>
      </g>

      <polygon v-if="areaPts" :points="areaPts" :fill="`url(#${gid})`" />
      <polyline
        v-if="data.length > 1" :points="linePts" fill="none"
        :stroke="color" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"
      />

      <!-- The configured cap. Dashed deliberately: this IS a threshold, which is
           exactly the meaning a dashed rule carries. -->
      <g v-if="capY !== null && !compact" class="cap">
        <line :x1="PAD.l" :x2="PAD.l + plotW" :y1="capY" :y2="capY" :stroke="color" />
        <text :x="PAD.l + plotW + 6" :y="capY + 3" class="cap-text num">
          cap {{ fmt(cap) }}
        </text>
      </g>

      <!-- Endpoint marker, with a 2px surface ring so it stays legible where it
           crosses the cap line. No text label here: the pane heading directly
           above is already the direct label for this value, and a second copy
           collided with the cap label in the right margin. -->
      <g v-if="data.length > 1">
        <circle
          :cx="xAt(data.length - 1)" :cy="yAt(last)" r="4"
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
