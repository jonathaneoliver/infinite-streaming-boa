<script setup lang="ts">
import { computed } from 'vue';
import { adapterColour } from '@/composables/useAdapters';

/**
 * Which adapter carried this client, on the charts' own x-axis.
 *
 * The band exists because a throughput trace is not interpretable without it on
 * a two-radio box. A trace that halved at 14:32 means one thing if the client
 * stayed put and quite another if it moved from 5GHz 80MHz to 2.4GHz 20MHz a
 * second earlier — and the client's CURRENT adapter cannot answer which,
 * because by the time you look it is simply on the other radio with nothing
 * saying it moved. Same argument as the cap series, applied to the link.
 *
 * Three things are drawn, and they are three different facts:
 *
 *	a coloured segment   attached to that adapter, in its own identity colour
 *	an orange divider    the CHANNEL changed under it, same adapter
 *	a dark gap           not attached to anything
 *
 * The gap is the one worth being careful about. Joining the segments across it
 * would claim the client stayed on a radio it had left, which is exactly the
 * confident wrongness the rest of this interface is built to avoid.
 */
const props = withDefaults(
  defineProps<{
    /** Sample timestamps, ascending, unix ms. Parallel to iface/chan. */
    t: number[];
    iface: string[];
    chan: number[];
    /** The chart's window and right-hand edge, so the axes match exactly. */
    windowMs?: number;
    now?: number;
    height?: number;
  }>(),
  { windowMs: 300_000, now: 0, height: 10 },
);

/**
 * The chart's plot inset, mirrored.
 *
 * TrafficChart lays its plot out at PAD.l/PAD.r inside its own box. Matching
 * those here is what makes "the same x-axis" true rather than approximately
 * true — and a band that is a few pixels out is worse than no band, because it
 * puts the roam next to the wrong part of the trace. Kept as literals with this
 * note rather than exported from the chart, because the chart's padding is a
 * layout detail of the chart; what is shared is the CONTRACT that both use the
 * same inset.
 */
const PAD_L = 40;
const PAD_R = 68;

const edge = computed(() => props.now || Date.now());
const start = computed(() => edge.value - props.windowMs);

type Seg = {
  iface: string;
  from: number;
  to: number;
  /** Channel changes inside this segment, as timestamps. */
  breaks: number[];
};

/**
 * Consecutive runs of the same adapter.
 *
 * A channel change does NOT start a new segment: the client did not go
 * anywhere, and drawing two blocks would say it did. It is recorded as a break
 * within the run instead, which is why `breaks` exists.
 */
const segments = computed<Seg[]>(() => {
  const out: Seg[] = [];
  let cur: Seg | null = null;
  let curChan = 0;
  for (let i = 0; i < props.t.length; i++) {
    const at = props.t[i];
    if (at < start.value) continue;
    const name = props.iface[i] ?? '';
    if (!name) {
      cur = null; // unattached: the gap is drawn by absence
      continue;
    }
    if (!cur || cur.iface !== name) {
      cur = { iface: name, from: at, to: at, breaks: [] };
      curChan = props.chan[i] ?? 0;
      out.push(cur);
      continue;
    }
    const ch = props.chan[i] ?? 0;
    if (ch && curChan && ch !== curChan) {
      cur.breaks.push(at);
    }
    if (ch) curChan = ch;
    cur.to = at;
  }
  return out;
});

/** Position as a percentage of the plot, so it tracks the chart on resize
 *  without either of them measuring anything. */
function pct(at: number): number {
  return ((at - start.value) / props.windowMs) * 100;
}

function segStyle(s: Seg) {
  const l = Math.max(0, pct(s.from));
  // The last sample covers up to the next one, so a run that is still going
  // reaches the right edge rather than stopping one tick short of it.
  const r = Math.min(100, pct(s.to));
  return {
    left: `${l}%`,
    width: `${Math.max(0.4, r - l)}%`,
    background: adapterColour(s.iface),
  };
}

const anything = computed(() => segments.value.length > 0);
</script>

<template>
  <div v-if="anything" class="strip" :style="{ paddingLeft: `${PAD_L}px`, paddingRight: `${PAD_R}px` }">
    <span class="k">on</span>
    <div class="track" :style="{ height: `${height}px` }">
      <div
        v-for="(s, n) in segments" :key="`${s.iface}-${n}`"
        class="seg" :style="segStyle(s)"
        :title="`${s.iface}${s.breaks.length ? ` · ${s.breaks.length} channel change(s)` : ''}`"
      >
        <!-- Inside the segment, because the change happened to THIS adapter
             rather than between two of them. -->
        <i
          v-for="b in s.breaks" :key="b" class="brk"
          :style="{ left: `${((b - s.from) / Math.max(1, s.to - s.from)) * 100}%` }"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.strip {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 2px;
}
/* Sits in the chart's left gutter, where the y-axis labels are, so the band
   itself starts exactly where the plot does. */
.k {
  position: absolute;
  margin-left: -34px;
  font-family: var(--mono);
  font-size: 9px;
  letter-spacing: 0.04em;
  color: var(--ink-faint);
}
.track {
  position: relative;
  flex: 1;
  min-width: 0;
  /* The gap colour. Darker than the panel so an unattached stretch reads as
     absence rather than as a segment in some third adapter's colour. */
  background: var(--bg);
  border-radius: 2px;
  overflow: hidden;
}
.seg {
  position: absolute;
  top: 0;
  bottom: 0;
  opacity: 0.85;
}
/* Orange, and the only orange in here: it marks a change rather than an
   identity, which is what every other orange in this interface means. */
.brk {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 2px;
  margin-left: -1px;
  background: var(--warn);
}
</style>
