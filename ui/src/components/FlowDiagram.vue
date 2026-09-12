<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { sankey, sankeyLinkHorizontal, sankeyJustify } from 'd3-sankey';
import type { PortPair } from '@/types';
import { clientColour } from '@/composables/useClientColours';

/**
 * WHICH PORT FORWARDED TO WHICH, as a Sankey: ribbon thickness is the rate.
 *
 * The one view that answers "is this client talking to the internet or to the
 * device beside it". The stacked charts cannot: interface counters give each
 * port's row and column sums and never the matrix. The daemon counts the
 * matrix with an nftables rule per ordered pair, measured to cost nothing --
 * see portpairs.go for the mechanism and for the numbers.
 *
 * WHY A LIBRARY, when every other chart here is hand-rolled SVG and Vue was
 * the interface's only runtime dependency. Because the hard part of a Sankey is
 * LAYOUT rather than drawing: assigning nodes to columns, ordering them within
 * a column so ribbons cross as little as possible, and subdividing each node's
 * edge by which counterpart each ribbon runs to. Four sources against four
 * destinations is sixteen candidate ribbons, and hand-rolling that is how you
 * get a figure whose crossings are wrong and whose bugs look like data.
 *
 * The hand-rolled version this replaced sidestepped all of it by drawing no
 * pairing at all -- three fixed columns, each side stacked independently,
 * straight bands between them. Honest while the pairing was unmeasurable, and
 * not enough the moment the matrix existed.
 *
 * ONE DIAGRAM PER DIRECTION. A Sankey must be acyclic, and both directions in
 * one figure puts every node on both sides with the ribbons folding back over
 * themselves. It also matches every paired chart here, for the reason
 * AdapterStack gives: uplink is routinely a twentieth of downlink, so one
 * shared scale flattens it into the axis.
 */
const props = defineProps<{
  pairs: PortPair[];
  /** The WAN port's name, so it can be labelled as the uplink. */
  wanIface?: string;
  /** Rates below this are not drawn. Background chatter touches every pair at
   *  once and would fill the figure with ribbons nobody asked about. */
  floor?: number;
}>();

const FLOOR = computed(() => props.floor ?? 0.05);

/*
 * THE RIBBONS ARE A ROLLING AVERAGE, NOT ONE TICK.
 *
 * Drawn from a single tick the figure spent most of its time empty, which is
 * what it looked like and not what was happening: a pair's rate is one second's
 * difference of a counter, and real traffic is bursty enough that any given
 * second falls under the draw floor while the flow plainly exists. A figure
 * that blinks is unreadable, and worse, it reads as "no routing" when the
 * answer is "some routing, unevenly".
 *
 * Ten seconds, which is long enough to bridge the gaps between bursts and short
 * enough that switching a client on or off still shows up while you are looking
 * at it. The window is STATED in each heading, because an averaged number and
 * an instantaneous one are different quantities and must not look alike.
 *
 * Held here rather than in the daemon on purpose. Nothing else consumes the
 * pair rates, and the right window is a property of this figure, not of the
 * measurement -- a consumer wanting the raw tick should not have to undo a
 * smoothing decision made for a drawing.
 */
const WINDOW_MS = 10_000;

interface Sample { at: number; pairs: PortPair[] }
const samples = ref<Sample[]>([]);

// Timestamped and expired by age rather than counted, so a stalled stream does
// not average ten-second-old bursts for ever. It empties instead, and the
// figure says so.
watch(() => props.pairs, (p) => {
  if (!p) return;
  const now = Date.now();
  samples.value = [...samples.value, { at: now, pairs: p }]
    .filter((sm) => now - sm.at <= WINDOW_MS);
}, { immediate: true });

/**
 * The mean rate per ordered pair across the window.
 *
 * DIVIDED BY THE WHOLE SAMPLE COUNT, not by the number of samples that carried
 * the pair. The daemon reports every rule every tick, so the two are the same
 * today -- but if a pair were ever absent, dividing by its own appearances
 * would turn one busy second into a sustained rate, which is the opposite of
 * what this is for.
 */
const smoothed = computed<PortPair[]>(() => {
  const window = samples.value;
  if (!window.length) return [];
  const acc = new Map<string, PortPair>();
  for (const sm of window) {
    for (const p of sm.pairs) {
      const k = `${p.from}>${p.to}`;
      const a = acc.get(k);
      if (a) {
        a.mbps += p.mbps;
        a.packets += p.packets;
      } else {
        acc.set(k, { from: p.from, to: p.to, mbps: p.mbps, packets: p.packets });
      }
    }
  }
  return [...acc.values()].map((p) => ({
    ...p,
    mbps: p.mbps / window.length,
    packets: p.packets / window.length,
  }));
});

/*
 * THE viewBox IS MEASURED IN PIXELS, NEVER FIXED.
 *
 * It was `viewBox="0 0 620 132"` with `width: 100%`, which makes the browser
 * scale the entire coordinate system to the container: at 2000px that is 3.2x,
 * so 10px labels rendered at 32px, the figure swallowed the viewport and the
 * end labels collided with each other. Reported from a screenshot.
 * AdapterStack had already learned this and says so -- one SVG unit has to be
 * one CSS pixel, or type does not render at the size it is set in.
 */
const H = 190;
const PAD = { t: 10, b: 10 };
/**
 * Room for the end labels, which sit outside the node columns.
 *
 * MEASURED, not chosen. The widest label this box produces is
 * `wlan-usb-46c7 52.1` at 101px, drawn 6px clear of the node, so 104 put three
 * pixels of it outside the SVG viewport where it was simply cut off. 120 leaves
 * headroom for a four-digit rate on a name of that length.
 */
const GUTTER = 120;
/** The gap between the two columns, matching the grid's own. */
const GAP = 12;

const box = ref<HTMLElement | null>(null);
const W = ref(680);
const ro = new ResizeObserver((e) => {
  const cw = e[0]?.contentRect.width ?? 0;
  if (cw > 0) W.value = Math.max(420, Math.floor(cw));
});
// Re-observed on element CHANGE rather than once on mount: this figure sits
// behind a v-if and has no element to measure when it first renders, so
// observing in onMounted attaches to nothing and the width stays at its
// initial guess for ever. The same trap AdapterStack documents.
watch(box, (el, prev) => {
  if (prev) ro.unobserve(prev);
  if (el) ro.observe(el);
}, { immediate: true, flush: 'post' });
onBeforeUnmount(() => ro.disconnect());

/*
 * A COLUMN's width, which is what every coordinate below is in.
 *
 * The two directions sit side by side on one row -- download, meaning inbound
 * to the clients, on the left -- so the observer measures the ROW and each
 * figure takes half of it less the gap. Derived rather than observed per
 * figure: one observer cannot measure two elements into one value, and two
 * would make the first paint depend on which fired first. The floor keeps the
 * ribbons drawable when the window is narrow.
 */
const CW = computed(() => Math.max(300, Math.floor((W.value - GAP) / 2)));

interface Node { name: string; label: string }
interface Link { source: number; target: number; value: number; packets: number }

/**
 * One direction's graph.
 *
 * A pair is placed by which side its ports sit on rather than by name, so a
 * LOCAL pair -- adapter to adapter, neither of them the uplink -- appears in
 * both diagrams. That is correct rather than double counting: the same frames
 * are inbound to one client and outbound from another.
 *
 * NODES ARE DUPLICATED ACROSS THE COLUMNS ON PURPOSE. A radio is both a source
 * and a destination, and a Sankey cannot have one node be both without a
 * cycle. So a port on the left means "traffic that arrived by this port" and on
 * the right "traffic that left by it" -- two different quantities that share a
 * name, keyed apart so the layout treats them as separate.
 */
function graphFor(dir: 'down' | 'up') {
  const wan = props.wanIface ?? '';
  const want = smoothed.value.filter((p) =>
    p.mbps > FLOOR.value
    && (dir === 'down' ? p.from === wan || p.to !== wan : p.to === wan || p.from !== wan));

  const nodes: Node[] = [];
  const index = new Map<string, number>();
  const idx = (iface: string, side: 'in' | 'out') => {
    const key = `${iface} ${side}`;
    let i = index.get(key);
    if (i === undefined) {
      i = nodes.length;
      index.set(key, i);
      nodes.push({ name: key, label: iface === wan ? 'uplink' : iface });
    }
    return i;
  };

  const links: Link[] = want.map((p) => ({
    source: idx(p.from, 'in'),
    target: idx(p.to, 'out'),
    value: p.mbps,
    packets: p.packets,
  }));
  return { nodes, links };
}

const sides = computed(() =>
  (['down', 'up'] as const).map((dir) => {
    const g = graphFor(dir);
    const title = dir === 'down' ? 'download' : 'upload';
    if (!g.links.length) return { dir, title, nodes: [], links: [], total: 0 };

    // sankeyJustify, not the default: it pushes nodes with no outgoing link to
    // the far column, so destinations line up with each other instead of
    // floating beside whichever source happened to feed them.
    const layout = sankey<Node, Link>()
      .nodeWidth(9)
      .nodePadding(10)
      .nodeAlign(sankeyJustify)
      .extent([[GUTTER, PAD.t], [Math.max(GUTTER + 60, CW.value - GUTTER), H - PAD.b]]);
    // The layout MUTATES what it is handed, so it gets a copy. Passing the
    // reactive arrays would have it write x/y coordinates back into the props
    // and retrigger the computed that produced them.
    const laid = layout({
      nodes: g.nodes.map((n) => ({ ...n })),
      links: g.links.map((l) => ({ ...l })),
    });
    const path = sankeyLinkHorizontal<Node, Link>();
    const label = (n: unknown) => (n as Node).label;
    return {
      dir,
      title,
      nodes: (laid.nodes ?? []).map((n) => ({
        label: label(n),
        x0: n.x0 ?? 0, x1: n.x1 ?? 0, y0: n.y0 ?? 0, y1: n.y1 ?? 0,
        value: n.value ?? 0,
        colour: clientColour(label(n)),
        // Which column, so a label sits outside the figure on its own side.
        left: (n.x0 ?? 0) < CW.value / 2,
      })),
      links: (laid.links ?? []).map((l, i) => ({
        key: i,
        d: path(l) ?? '',
        width: Math.max(1, l.width ?? 1),
        value: l.value,
        packets: (l as Link).packets,
        from: label(l.source),
        to: label(l.target),
        colour: clientColour(label(l.source)),
      })),
      total: g.links.reduce((a, l) => a + l.value, 0),
    };
  }),
);

const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v.toFixed(1));
const pkt = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(0));
</script>

<template>
  <!-- ALWAYS DRAWN, EVEN IDLE, and the earlier version's reasoning was wrong.
       It rendered only when something was moving, on the argument that an empty
       Sankey reads as broken. The cost of that is worse: the figure is 400px of
       page, so every lull removed it and pushed everything below back up, and
       every packet put it back. A reader loses their place in a view whose
       whole purpose is watching a number move. So the height is held and the
       figure says it is idle, which no amount of layout shift can say.

       It also fixes the measuring trap this file already documents. Behind a
       v-if there was no element for the ResizeObserver on first render, so the
       width stayed at its initial guess until something forced a remeasure. -->
  <div ref="box" class="flows">
    <figure v-for="s in sides" :key="s.dir" class="flow">
      <div class="head">
        <span class="dir">{{ s.title }}</span>
        <span class="stats num">{{ fmt(s.total) }} Mbit/s · {{ WINDOW_MS / 1000 }}s avg</span>
      </div>
      <svg
        :viewBox="`0 0 ${CW} ${H}`" :width="CW" :height="H"
        role="img" :aria-label="`${s.title}: ${fmt(s.total)} megabits per second`"
      >
        <!-- The idle state, which is a READING and not an absence: the
             counters were read and nothing crossed. Said in the figure's own
             space so the space is not given up. -->
        <text
          v-if="!s.links.length" :x="CW / 2" :y="H / 2" class="idle"
          text-anchor="middle"
        >nothing forwarded in the last {{ WINDOW_MS / 1000 }}s</text>
        <!-- Ribbons first, so a node's edge stays crisp over them. Coloured by
             SOURCE, so a reader follows a flow from where it came. -->
        <g class="links">
          <path v-for="l in s.links" :key="l.key"
                :d="l.d" :stroke="l.colour" :stroke-width="l.width">
            <title>{{ l.from }} → {{ l.to }}: {{ fmt(l.value) }} Mbit/s, {{ pkt(l.packets) }} frames/s</title>
          </path>
        </g>
        <g v-for="(n, i) in s.nodes" :key="i">
          <rect :x="n.x0" :y="n.y0" :width="n.x1 - n.x0"
                :height="Math.max(1, n.y1 - n.y0)" :fill="n.colour" />
          <text :x="n.left ? n.x0 - 6 : n.x1 + 6" :y="(n.y0 + n.y1) / 2 + 3"
                class="lab" :class="{ end: n.left }">
            {{ n.label }} <tspan class="num">{{ fmt(n.value) }}</tspan>
          </text>
        </g>
      </svg>
    </figure>
    <!-- SAID, because the figure cannot say it. Each column is a SIDE of a
         forwarding decision rather than a device, and the two figures do not
         share a scale — which the drawing alone cannot admit to. Measured on
         the container host: 34.0 Mbit/s inbound and 0.1 outbound drew ribbons
         of identical width, because a lone ribbon fills whichever figure it is
         in. Read across the two by the totals in the headings, never by eye. -->
    <p class="flow-note">
      Counted where the bridge forwards, one counter per ordered port pair, so a
      ribbon is a measured path rather than an inference. A port can appear on
      both sides: on the left it is traffic that arrived by that port, on the
      right traffic that left by it. Hover a ribbon for its frame rate, which
      does not track its width — aggregated frames downstream are far larger
      than the acknowledgements coming back. Every rate here is a
      {{ WINDOW_MS / 1000 }}-second mean rather than one tick, because a single
      second of a bursty flow routinely reads as nothing at all. Download is
      what came in toward the
      clients and upload what went out from them, named from their point of view
      rather than the box's. Each direction is scaled to its own
      total, shown beside its heading, so widths compare within a figure and not
      between the two.
    </p>
  </div>
</template>

<style scoped>
/* ONE ROW, DOWNLOAD LEFT. The two directions are read against each other and
   stacked they were a screen apart. Download is INBOUND TO THE CLIENTS, which
   is what the left figure is titled; `sides` yields down before up, so source
   order puts it there without a reordering rule to keep in step with it. */
.flows {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin: 0 0 12px;
}
.flow { margin: 0; }
.head { display: flex; align-items: baseline; gap: 8px; margin-bottom: 2px; }
.dir {
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--ink-dim);
}
.stats { font-size: 11px; color: var(--ink-faint); margin-left: auto; }
/* NOT width:100%. The viewBox is in real pixels, so stretching it would put
   the scaling bug straight back. */
svg { display: block; max-width: 100%; }
.links path {
  fill: none;
  stroke-opacity: 0.4;
  transition: stroke-opacity 120ms;
}
.links path:hover { stroke-opacity: 0.78; }
.lab { font-size: 10px; fill: var(--ink-dim); }
.idle { font-size: 11px; fill: var(--ink-faint); }
.lab.end { text-anchor: end; }
.lab .num { fill: var(--ink-faint); }
.flow-note {
  grid-column: 1 / -1;
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--ink-faint);
  max-width: 78ch;
}
</style>
