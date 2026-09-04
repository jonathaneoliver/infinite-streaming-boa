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
import { RANGES, SORT_MODES, SUSTAINED_CHOICES, type SortMode, type YMode }
  from '@/types';

const props = defineProps<{
  rangeSec: number;
  yMode: YMode;
  yManual: number;
  /** Width of one plotted point, in ms. Above the live tick it is an average. */
  bucketMs: number;
  showLive: boolean;
  showSustained: boolean;
  showPhy: boolean;
  /** How the device list below is ordered. */
  sortMode: SortMode;
  /** Expanded charts drawn at double height. */
  tallCharts: boolean;
  /** Trailing window for the sustained mean, in seconds. */
  sustainedSec: number;
  /** Which directions are drawn at all. */
  showDown: boolean;
  showUp: boolean;
}>();

const emit = defineEmits<{
  (e: 'range', sec: number): void;
  (e: 'y-mode', m: YMode): void;
  (e: 'y-manual', v: number): void;
  (e: 'show-live', v: boolean): void;
  (e: 'show-sustained', v: boolean): void;
  (e: 'show-phy', v: boolean): void;
  (e: 'sort-mode', m: SortMode): void;
  (e: 'tall-charts', v: boolean): void;
  (e: 'sustained-sec', v: number): void;
  (e: 'show-down', v: boolean): void;
  (e: 'show-up', v: boolean): void;
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
    label: 'mean',
    title: 'Bytes delivered over the trailing window, divided by that time. What the device is actually sustaining across segment fetches.',
  },
  // The link's ceiling, drawn as a rule. A MEASUREMENT rather than a
  // prediction: it is the rate the client negotiated, not that rate scaled by
  // some efficiency factor -- measured on this box, throughput lands near 85%
  // of it on a quiet channel and about a third on a busy one, and a single
  // derived line would be wrong on one of them. So the line is the ceiling and
  // the gap beneath it is left to be read.
  {
    key: 'phy' as const,
    label: 'PHY',
    title: "The client's negotiated PHY rate — what the link could carry, not what it is being allowed. Traffic sits below it by whatever airtime costs.",
  },
];

type SeriesKey = 'live' | 'sustained' | 'phy';

function toggle(key: SeriesKey) {
  if (key === 'live') emit('show-live', !props.showLive);
  else if (key === 'phy') emit('show-phy', !props.showPhy);
  else emit('show-sustained', !props.showSustained);
}
const on = (key: SeriesKey) =>
  key === 'live' ? props.showLive : key === 'phy' ? props.showPhy : props.showSustained;
const seriesLabel = (key: SeriesKey) =>
  key === 'sustained' ? `${props.sustainedSec}s mean` : key === 'phy' ? 'PHY' : 'live';

const MODES: { v: YMode; label: string; title: string }[] = [
  { v: 'auto', label: 'auto', title: 'Scale to the traffic, including the cap' },
  { v: 'cap', label: 'to cap', title: 'Lock the axis to the configured cap, so headroom stays constant' },
  // The ceiling the LINK could carry, as opposed to the cap the box is
  // imposing. Scaling to it answers a different question from either of the
  // others: not "is the cap working" but "how much of what this radio could
  // deliver is actually arriving". Measured here, throughput lands around 85%
  // of PHY on a quiet channel and about a third of it on a busy one, so the
  // gap this mode shows is mostly airtime.
  { v: 'phy', label: 'to PHY', title: "Scale to the client's negotiated PHY rate — the link's ceiling, not the cap. Falls back to auto for a client with no radio." },
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

    <span class="lbl">height</span>
    <button
      class="seg-btn lone" :class="{ on: tallCharts }"
      :aria-pressed="tallCharts"
      title="Double the height of the expanded charts. More vertical resolution to separate close rungs; fewer devices on screen at once."
      @click="emit('tall-charts', !tallCharts)"
    >tall</button>

    <!-- Which directions to draw. Page-level like everything else in this bar:
         one card showing a direction while the next hides it would make the
         page unreadable, and the question is how much screen to spend, which
         is not a per-device one. -->
    <span class="lbl">show</span>
    <div class="seg" role="group" aria-label="Directions shown">
      <button
        class="seg-btn" :class="{ on: showDown }" :aria-pressed="showDown"
        title="Draw the downlink: traffic to the device, and what most testing here is about."
        @click="emit('show-down', !showDown)"
      >down</button>
      <button
        class="seg-btn" :class="{ on: showUp }" :aria-pressed="showUp"
        title="Draw the uplink: traffic from the device. Hiding it gives every card its width back."
        @click="emit('show-up', !showUp)"
      >up</button>
    </div>

    <span class="lbl">mean over</span>
    <div class="seg" role="group" aria-label="Window for the sustained mean">
      <button
        v-for="c in SUSTAINED_CHOICES" :key="c.v"
        class="seg-btn" :class="{ on: sustainedSec === c.v }"
        :aria-pressed="sustainedSec === c.v" :title="c.title"
        @click="emit('sustained-sec', c.v)"
      >{{ c.label }}</button>
    </div>

    <span class="lbl">series</span>
    <div class="legend" role="group" aria-label="Series shown">
      <button
        v-for="s in SERIES" :key="s.key"
        class="key" :class="{ off: !on(s.key) }"
        :aria-pressed="on(s.key)" :title="s.title"
        @click="toggle(s.key)"
      >
        <svg class="swatch" width="18" height="8" aria-hidden="true">
          <!-- The swatch is the stroke the plot actually uses, so the key can
               be matched to the line without reading the label. PHY is dotted
               and faint: it is a boundary rather than a series, and the cap --
               the other boundary here -- already owns dashes. -->
          <line
            x1="1" x2="17" y1="4" y2="4"
            :stroke-width="s.key === 'sustained' ? 2.25 : 1.25"
            :opacity="s.key === 'sustained' ? 1 : 0.55"
            :stroke-dasharray="s.key === 'phy' ? '1 3' : undefined"
          />
        </svg>
        {{ seriesLabel(s.key) }}
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

/* .seg / .seg-btn are global (style.css): three components use them now.
   A toggle, not one of an exclusive set, so it carries its own border rather
   than sharing the segmented control's. */
.lone { border: 1px solid var(--line); border-radius: 6px; }
.lbl + .lone { margin-right: 6px; }

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
