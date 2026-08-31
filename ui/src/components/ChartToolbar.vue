<script setup lang="ts">
/**
 * Controls for every chart on the page.
 *
 * One row above the list rather than a copy inside each card: these settings
 * scope all the charts, and the dataviz convention puts shared controls in a
 * single row above the charts they govern. Repeating them per card would also
 * put chrome on a folded card, where there is no chart to control.
 *
 * The consequence is deliberate and worth naming: every device is drawn on the
 * same range and the same axis rule, which is what makes two devices
 * comparable at a glance. Per-device ranges would make each card readable on
 * its own and the page as a whole meaningless.
 */
import { RANGES, SORT_MODES, SUSTAINED_SEC, type SortMode, type YMode }
  from '@/types';

const props = defineProps<{
  rangeSec: number;
  yMode: YMode;
  yManual: number;
  /** Width of one plotted point, in ms. Above the live tick it is an average. */
  bucketMs: number;
  showLive: boolean;
  showSustained: boolean;
  /** How the device list below is ordered. */
  sortMode: SortMode;
}>();

const emit = defineEmits<{
  (e: 'range', sec: number): void;
  (e: 'y-mode', m: YMode): void;
  (e: 'y-manual', v: number): void;
  (e: 'show-live', v: boolean): void;
  (e: 'show-sustained', v: boolean): void;
  (e: 'sort-mode', m: SortMode): void;
}>();

/**
 * The legend keys the two SERIES, not the two directions.
 *
 * Colour already carries direction everywhere on this page -- blue down, orange
 * up -- so a swatch here would have to be one or the other and would teach the
 * wrong thing. What needs a key is the pair of lines drawn in the same hue, and
 * what tells those apart is weight. So the swatches are strokes at the weights
 * the plot actually uses, in neutral ink.
 *
 * It lives in the shared toolbar rather than on each card for the same reason
 * the other controls do: one legend read once, instead of four swatches
 * repeated down the page, and every chart showing the same series so devices
 * stay comparable.
 */
const SERIES = [
  {
    key: 'live' as const,
    label: 'live',
    title: 'Throughput during each sample. Bursty by nature: a player fetches a segment, then idles.',
  },
  {
    key: 'sustained' as const,
    label: `${SUSTAINED_SEC}s mean`,
    title: `Bytes delivered over the trailing ${SUSTAINED_SEC} seconds, divided by that time. What the device is actually sustaining across segment fetches.`,
  },
];

function toggle(key: 'live' | 'sustained') {
  if (key === 'live') emit('show-live', !props.showLive);
  else emit('show-sustained', !props.showSustained);
}
const on = (key: 'live' | 'sustained') =>
  key === 'live' ? props.showLive : props.showSustained;

const MODES: { v: YMode; label: string; title: string }[] = [
  { v: 'auto', label: 'auto', title: 'Scale to the traffic, including the cap' },
  { v: 'cap', label: 'to cap', title: 'Lock the axis to the configured cap, so headroom stays constant' },
  { v: 'manual', label: 'fixed', title: 'A fixed ceiling, so devices can be compared on one scale' },
];

function onManual(e: Event) {
  const v = Number((e.target as HTMLInputElement).value);
  if (Number.isFinite(v) && v > 0) emit('y-manual', v);
}
</script>

<template>
  <div class="toolbar">
    <span class="lbl">order</span>
    <!-- First in the bar because it acts on the PAGE, where everything after it
         configures the charts drawn on it. -->
    <div class="seg" role="group" aria-label="Device order">
      <button
        v-for="m in SORT_MODES" :key="m.v"
        class="seg-btn" :class="{ on: sortMode === m.v }"
        :title="m.title"
        @click="emit('sort-mode', m.v)"
      >{{ m.label }}</button>
    </div>

    <span class="lbl">range</span>
    <div class="seg" role="group" aria-label="Chart time range">
      <button
        v-for="r in RANGES" :key="r.v"
        class="seg-btn" :class="{ on: rangeSec === r.v }"
        :aria-pressed="rangeSec === r.v"
        @click="emit('range', r.v)"
      >{{ r.label }}</button>
    </div>

    <span class="lbl">y-axis</span>
    <div class="seg" role="group" aria-label="Y-axis scaling">
      <button
        v-for="m in MODES" :key="m.v"
        class="seg-btn" :class="{ on: yMode === m.v }"
        :aria-pressed="yMode === m.v" :title="m.title"
        @click="emit('y-mode', m.v)"
      >{{ m.label }}</button>
    </div>

    <label v-if="yMode === 'manual'" class="manual">
      <input
        class="num" type="number" min="0.1" step="0.5" :value="yManual"
        aria-label="Y-axis maximum in Mbps"
        @change="onManual"
      />
      <span>Mbps</span>
    </label>

    <span class="lbl">series</span>
    <div class="legend" role="group" aria-label="Series shown">
      <button
        v-for="s in SERIES" :key="s.key"
        class="key" :class="{ off: !on(s.key) }"
        :aria-pressed="on(s.key)" :title="s.title"
        @click="toggle(s.key)"
      >
        <svg class="swatch" width="18" height="8" aria-hidden="true">
          <line
            x1="1" x2="17" y1="4" y2="4"
            :stroke-width="s.key === 'sustained' ? 2.25 : 1.25"
            :opacity="s.key === 'sustained' ? 1 : 0.55"
          />
        </svg>
        {{ s.label }}
      </button>
    </div>

    <span class="spacer"></span>

    <!-- Only shown once a point stops being one second of traffic. Saying
         nothing here would let an hour of six-second means read as raw data. -->
    <span v-if="bucketMs > 1000" class="res num" :title="
      'Long ranges are averaged into buckets on the server, so short spikes are smoothed'
    ">{{ Math.round(bucketMs / 1000) }}s avg</span>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 8px 12px;
  margin-bottom: 10px;
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--r);
}
.lbl {
  font-size: 11px; letter-spacing: 0.08em; text-transform: uppercase;
  color: var(--ink-faint);
}
.lbl + .seg { margin-right: 6px; }

/* A segmented control, not four loose buttons: these are exclusive choices and
   the shared border says so. */
.seg { display: flex; border: 1px solid var(--line); border-radius: 6px; overflow: hidden; }
.seg-btn {
  padding: 3px 10px;
  font: inherit; font-size: 12px;
  color: var(--ink-dim);
  background: var(--panel-2);
  border: 0;
  border-left: 1px solid var(--line);
  cursor: pointer;
}
.seg-btn:first-child { border-left: 0; }
.seg-btn:hover { color: var(--ink); background: var(--line); }
.seg-btn.on { color: var(--ink); background: var(--line); font-weight: 600; }
.seg-btn:focus-visible { outline: 2px solid var(--down); outline-offset: -2px; }

.manual { display: flex; align-items: center; gap: 5px; font-size: 12px; color: var(--ink-dim); }
.manual input {
  width: 74px; padding: 3px 6px;
  color: var(--ink); background: var(--panel-2);
  border: 1px solid var(--line); border-radius: 6px;
  font-size: 12px;
}
/* Not a segmented control: these are independent switches, not one exclusive
   choice, and sharing a border would say the opposite. */
.legend { display: flex; align-items: center; gap: 10px; }
.key {
  display: flex; align-items: center; gap: 5px;
  padding: 2px 4px;
  font: inherit; font-size: 12px;
  color: var(--ink-dim);
  background: none; border: 0; border-radius: 4px;
  cursor: pointer;
}
.key:hover { color: var(--ink); }
.key:focus-visible { outline: 2px solid var(--down); outline-offset: 0; }
.key .swatch line { stroke: var(--ink); }
/* A series that is off reads as struck through, not merely dim: dimness alone
   is how this whole toolbar renders inactive things, and "hidden" has to be
   distinguishable from "not hovered". */
.key.off { color: var(--ink-faint); text-decoration: line-through; }
.key.off .swatch line { stroke: var(--ink-faint); opacity: 0.5; }

.spacer { flex: 1; }
.res { font-size: 11px; color: var(--ink-faint); }
</style>
