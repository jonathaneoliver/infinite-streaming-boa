<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { sankey, sankeyLinkHorizontal, sankeyJustify } from 'd3-sankey';
import type { ClientPair, PortPair } from '@/types';
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
/**
 * WHAT THIS DRAWS IS NOT SPECIFICALLY PORTS.
 *
 * PortPair and ClientPair carry the same four fields, and the figure needs
 * nothing else: a pair of opaque keys, a rate and a frame count. Typed
 * structurally so one component draws the adapter matrix and the device matrix
 * without a mode flag deciding which fields to read -- the two differ in what
 * a key MEANS, which is the caller's business, not the drawing's.
 */
type Flow = PortPair | ClientPair;

const props = defineProps<{
  pairs: readonly Flow[];
  /**
   * The key that counts as the far side, for splitting the two directions.
   *
   * For adapters that is the uplink port; for devices it is the sentinel
   * standing for everything past the box. A flow touching it is inbound when
   * it comes FROM it and outbound when it goes TO it; a flow touching neither
   * end is local and appears in both figures.
   */
  uplink?: string;
  /**
   * Display names per key, falling back to the key itself.
   *
   * Resolution lives with the caller because that is where the roster is: an
   * adapter is already named by its key, and a device's key is a MAC that
   * means nothing on a chart. The RAW key is still used for colour, so a
   * device is the same colour here as in every other chart, and it is shown in
   * the tooltip beside the name so the address is never simply hidden.
   */
  labels?: Record<string, string>;
  /**
   * Preferred top-to-bottom order of parties, most significant first.
   *
   * Supplied by the caller because the RIGHT order is not a property of the
   * drawing: the adapter figure should read in the same order as the rack and
   * the legend above it, which is by role -- uplink, radios, then wired -- and
   * the device figure has no such order, so it falls back to alphabetical.
   * Anything not named here sorts after everything that is.
   */
  order?: string[];
  /**
   * A SECOND LINE under a party's name, where one adds something.
   *
   * For devices it is the adapter they are on, which is the join the device
   * figure could not otherwise show: the same information as a four-column
   * device-adapter-adapter-device figure, without the two middle columns.
   *
   * Those columns were the alternative and were rejected twice over. They
   * would be DERIVED from the roster rather than measured, so they could
   * disagree with the adapter figure that counts those very edges directly --
   * two figures on one page differing about one number. And twenty-odd nodes
   * do not fit this height at a readable padding.
   *
   * Appended as a line rather than to the name because the two together run to
   * about 38 characters, past the widest gutter this figure will give up.
   */
  sublabels?: Record<string, string>;
  /**
   * What a party MEANS, for its tooltip, where the key does not say it.
   *
   * A device's key is its MAC and showing that is useful. A sentinel's key is
   * a slug, and `beyond the box (beyond-the-box)` tells a reader nothing they
   * could not see -- while the question it actually raises, whether this is
   * just the WAN port, deserves an answer in the place they hover.
   */
  notes?: Record<string, string>;
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

interface Sample { at: number; pairs: readonly Flow[] }
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
/*
 * Room for the end labels, which sit outside the node columns.
 *
 * DERIVED FROM THE LABELS, not a constant, because the caller decides how long
 * they are. A fixed 120 was measured against adapter names and then clipped
 * `Jonathans-Mac-mini 1.5` the moment devices were drawn -- reported from a
 * screenshot showing `1.` against the figure's edge. Device names are whatever
 * a person called their laptop, so no constant is safe.
 *
 * Estimated from the character count rather than measured with a canvas: this
 * only has to be big enough, the labels are one short line of 10px text, and a
 * measuring context per render to save a few pixels is not a trade worth
 * making. Clamped at both ends -- a floor so short names still leave the
 * ribbons somewhere to start, a ceiling so one long name cannot squeeze the
 * figure to nothing.
 */
const CHAR_PX = 5.8;
const GUTTER_MIN = 104;
const GUTTER_MAX = 190;

/**
 * How thin a ribbon may be drawn.
 *
 * A ribbon below this is OVERSTATED by being visible at all, and that is the
 * lesser evil: the alternative is a flow that exists reading as no flow. The
 * figure says so, and the rate is on the label either way.
 *
 * 4, not 1.5. A hairline is technically visible and practically not -- against
 * a 200px slab it reads as an artefact of the rendering rather than as a
 * measured flow, which is the same failure as drawing nothing. The cost is
 * more overstatement of the smallest flows, and that cost is already declared
 * on screen next to the total.
 */
const MIN_RIBBON = 4;
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
 * The two directions sit side by side on one row -- see the note on their
 * order in the stylesheet -- so the observer measures the ROW and each figure
 * takes half of it less the gap. Derived rather than observed per figure: one
 * observer cannot measure two elements into one value, and two would make the
 * first paint depend on which fired first. The floor keeps the ribbons
 * drawable when the window is narrow.
 */
const CW = computed(() => Math.max(300, Math.floor((W.value - GAP) / 2)));

interface Node { name: string; key: string; label: string }
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
  const wan = props.uplink ?? '';
  const want = smoothed.value.filter((p) =>
    p.mbps > FLOOR.value
    && (dir === 'down' ? p.from === wan || p.to !== wan : p.to === wan || p.from !== wan));

  /*
   * THE ORDER IS FIXED AND RATE-INDEPENDENT, which is a deliberate trade.
   *
   * d3-sankey's default is to reorder nodes within a column to reduce ribbon
   * crossings, and it redoes that on every tick. The result was a figure whose
   * rows reshuffled as the numbers moved: reported from use, and the same
   * objection as the sorted device list elsewhere here -- a reader tracking one
   * adapter has to find it again every second, which is most of what this
   * figure is for.
   *
   * So parties are laid out in a stable order and the crossing-reduction pass
   * is switched off, see iterations(0) below. The cost is real -- more crossed
   * ribbons in a busy figure -- and it buys something worth more: a row that
   * stays where it was, in the same order in both directions, so up and down
   * can be read against each other line by line.
   *
   * The uplink leads because it is the one party that is not a peer, and
   * broadcast trails because it is not a destination anybody chose. Everything
   * between is alphabetical BY LABEL rather than by key, so the eye's order
   * matches the text's.
   */
  const rank = (k: string) => {
    const i = props.order?.indexOf(k) ?? -1;
    return i < 0 ? Number.MAX_SAFE_INTEGER : i;
  };
  const order = (a: string, b: string) => {
    if (a === b) return 0;
    if (a === wan) return -1;
    if (b === wan) return 1;
    if (a === 'broadcast') return 1;
    if (b === 'broadcast') return -1;
    // The caller's order wins where it has an opinion.
    const ra = rank(a);
    const rb = rank(b);
    if (ra !== rb) return ra - rb;
    const la = props.labels?.[a] ?? a;
    const lb = props.labels?.[b] ?? b;
    return la.localeCompare(lb, undefined, { sensitivity: 'base' });
  };

  // Which parties appear on which side, decided before any node exists, so the
  // node array can be built in the order the figure should read.
  const onSide: Record<'in' | 'out', Set<string>> = { in: new Set(), out: new Set() };
  for (const p of want) {
    onSide.in.add(p.from);
    onSide.out.add(p.to);
  }

  const nodes: Node[] = [];
  const index = new Map<string, number>();
  // Sources first, then sinks, each in the fixed order. d3 groups nodes into
  // columns and, with the relaxation off, keeps each column in array order.
  for (const side of ['in', 'out'] as const) {
    for (const party of [...onSide[side]].sort(order)) {
      index.set(`${party} ${side}`, nodes.length);
      nodes.push({
        name: `${party} ${side}`,
        key: party,
        label: props.labels?.[party] ?? party,
      });
    }
  }
  const idx = (party: string, side: 'in' | 'out') => index.get(`${party} ${side}`) ?? 0;

  const links: Link[] = want.map((p) => ({
    source: idx(p.from, 'in'),
    target: idx(p.to, 'out'),
    value: p.mbps,
    packets: p.packets,
  }));
  return { nodes, links };
}

const gutter = computed(() => {
  let longest = 0;
  for (const p of smoothed.value) {
    for (const k of [p.from, p.to]) {
      // The name, plus room for a rate of up to five characters beside it.
      const n = (props.labels?.[k] ?? k).length + 6;
      if (n > longest) longest = n;
    }
  }
  return Math.min(GUTTER_MAX, Math.max(GUTTER_MIN, Math.ceil(longest * CHAR_PX) + 8));
});

/*
 * ONE SCALE ACROSS BOTH FIGURES, which is the whole of what was wrong before.
 *
 * d3-sankey fits whatever it is given to the extent it is given, so a figure
 * holding one ribbon draws that ribbon at full height WHATEVER ITS RATE. In use
 * that put a 0.3 Mbit/s upload on screen as a slab of identical weight to a
 * 32.9 Mbit/s download -- reported from a screenshot, and the figure was doing
 * exactly what it was told.
 *
 * The old note admitted it in prose: read across the two by the totals, never
 * by eye. That was not good enough. A caveat does not stop a reader believing
 * a picture, and a picture whose main visual channel means nothing is worse
 * than one that is harder to read.
 *
 * So the layout still runs per figure at full height, and the RESULT is scaled
 * by that direction's share of the larger total. Thickness then means the same
 * thing in both, and the asymmetry between up and down -- routinely twentyfold
 * on this box -- becomes the first thing visible rather than a footnote.
 *
 * SCALED ABOUT EACH NODE'S CENTRE, links and node rectangles together, so the
 * ribbons still tile the node edge exactly instead of drifting off it: each
 * link's attachment point moves toward the centre by the same factor as its
 * width shrinks. Nothing about the layout -- columns, ordering, crossings -- is
 * touched, because that is the part a library should be doing.
 */
const sides = computed(() => {
  const graphs = (['down', 'up'] as const).map((dir) => {
    const g = graphFor(dir);
    return { dir, g, total: g.links.reduce((a, l) => a + l.value, 0) };
  });
  const maxTotal = Math.max(...graphs.map((x) => x.total));

  return graphs.map(({ dir, g, total }) => {
    const title = dir === 'down' ? 'download' : 'upload';
    if (!g.links.length) {
      return { dir, title, nodes: [], links: [], total: 0, hairline: false };
    }
    // The share of the LARGER direction. 1 for whichever is bigger, so that
    // figure is drawn exactly as the layout produced it.
    const scale = maxTotal > 0 ? total / maxTotal : 1;

    // sankeyJustify, not the default: it pushes nodes with no outgoing link to
    // the far column, so destinations line up with each other instead of
    // floating beside whichever source happened to feed them.
    const layout = sankey<Node, Link>()
      .nodeWidth(9)
      // 14, not 10. Labels are centred on their node and set at 11px, so 10px
      // between node edges let two thin neighbours' labels touch -- measured
      // as an overlap between `Jonathans-iPhone 0.2` and the name below it. A
      // Sankey spaces nodes by VALUE, so two small flows sit almost on top of
      // each other however few parties there are, and the padding is the only
      // floor under that.
      .nodePadding(14)
      .nodeAlign(sankeyJustify)
      // NO RELAXATION PASSES, so each column keeps the order graphFor gave it.
      // This is the switch that stops the rows reshuffling every tick; the
      // reasoning, and what it costs, is at the comparator in graphFor.
      .iterations(0)
      .extent([[gutter.value, PAD.t],
        [Math.max(gutter.value + 60, CW.value - gutter.value), H - PAD.b]]);
    // The layout MUTATES what it is handed, so it gets a copy. Passing the
    // reactive arrays would have it write x/y coordinates back into the props
    // and retrigger the computed that produced them.
    const laid = layout({
      nodes: g.nodes.map((n) => ({ ...n })),
      links: g.links.map((l) => ({ ...l })),
    });
    const path = sankeyLinkHorizontal<Node, Link>();
    const label = (n: unknown) => (n as Node).label;
    // COLOUR FROM THE RAW KEY, never from the label. A device's colour comes
    // from its MAC everywhere else in the interface, so colouring by display
    // name would give it one colour here and another on its own card.
    const key = (n: unknown) => (n as Node).key;
    // The address beside the name, so resolving a key to something readable
    // does not hide what it actually was. Only when the two differ -- an
    // adapter is already named by its key, and repeating it would be noise.
    /*
     * TWO NAMES, and conflating them was a regression worth recording.
     *
     * `endName` names a party inside a RIBBON's tooltip, where two of them
     * appear either side of an arrow. `nodeTitle` names it on its own node,
     * where there is room to say what it means.
     *
     * One function did both, and adding the sentinel note to it put a sentence
     * on each end of every ribbon: `beyond the box — the upstream router, and
     * any device here the box has not identified. Nearly all of this crossed
     * the WAN port, but not by definition → Jonathans-Mac-mini (d0:11:...)`.
     * Caught by reading the deployed tooltips rather than by looking at the
     * figure, where nothing appeared wrong at all.
     */
    const endName = (n: unknown) =>
      (label(n) === key(n) ? label(n) : `${label(n)} (${key(n)})`);
    const nodeTitle = (n: unknown) => {
      const note = props.notes?.[key(n)];
      // The note REPLACES the key rather than joining it: it exists precisely
      // where the key is a slug worth explaining instead of showing.
      const base = note ? `${label(n)} — ${note}` : endName(n);
      const sub = props.sublabels?.[key(n)];
      return sub ? `${base} on ${sub}` : base;
    };
    const centre = (n: unknown) => {
      const nd = n as { y0?: number; y1?: number };
      return ((nd.y0 ?? 0) + (nd.y1 ?? 0)) / 2;
    };

    /*
     * WHERE EACH RIBBON LEAVES ITS NODE, which iterations(0) left to chance.
     *
     * Pinning the node order fixed the rows, and quietly broke something else:
     * d3 sorts the links WITHIN a node -- deciding which leaves from the top
     * of it and which from the bottom -- only as part of the relaxation passes
     * that iterations(0) switches off. So they kept the order the pairs
     * happened to arrive in, and two ribbons from one node crossed for no
     * reason: reported from a screenshot where 11.9 Mbit/s left the bottom of
     * the uplink to reach the top sink while 0.8 left the top to reach the
     * bottom one.
     *
     * So sort them here, by the height of the node at the other end, and
     * recompute each attachment point from that order -- which is what d3's
     * own computeLinkBreadths does, and all it does. A crossing that survives
     * this one is topology rather than layout: A to B and B to A in the same
     * figure must cross, whatever the order, and that crossing is a reading.
     */
    type Side = { y0?: number; y1?: number };
    const mid = (n: Side) => ((n.y0 ?? 0) + (n.y1 ?? 0)) / 2;
    for (const n of laid.nodes ?? []) {
      n.sourceLinks?.sort((a, b) => mid(a.target as Side) - mid(b.target as Side));
      n.targetLinks?.sort((a, b) => mid(a.source as Side) - mid(b.source as Side));
    }
    for (const n of laid.nodes ?? []) {
      let y = n.y0 ?? 0;
      for (const l of n.sourceLinks ?? []) {
        l.y0 = y + (l.width ?? 0) / 2;
        y += l.width ?? 0;
      }
      y = n.y0 ?? 0;
      for (const l of n.targetLinks ?? []) {
        l.y1 = y + (l.width ?? 0) / 2;
        y += l.width ?? 0;
      }
    }

    // Applied BEFORE the path generator reads y0/y1, which is why the links are
    // mutated rather than mapped: sankeyLinkHorizontal takes the link, not a
    // pair of numbers.
    let hairline = false;
    for (const l of laid.links ?? []) {
      const drawn = (l.width ?? 1) * scale;
      if (drawn < MIN_RIBBON) hairline = true;
      const cs = centre(l.source);
      const ct = centre(l.target);
      l.y0 = cs + ((l.y0 ?? cs) - cs) * scale;
      l.y1 = ct + ((l.y1 ?? ct) - ct) * scale;
      l.width = Math.max(MIN_RIBBON, drawn);
    }

    /*
     * MIRRORED HORIZONTALLY, WHICH IS WHAT PUTS THE UPLINK IN THE MIDDLE.
     *
     * A Sankey draws a source to the left of its target, and the uplink is the
     * SOURCE of download and the TARGET of upload -- so unmirrored it lands at
     * the far left of one figure and the far right of the other, with the
     * clients in between. That is the arrangement that made the two halves
     * read as mirrors of each other.
     *
     * Flipping both puts it at the inner edge of each: the right edge of the
     * download figure and the left edge of the upload figure, meeting in the
     * centre of the page, with the clients out at the two ends. The figures
     * keep their order, download first.
     *
     * x0 AND x1 SWAP ROLES rather than both being negated in place, and that
     * is load-bearing. The path generator always reads `source.x1` and
     * `target.x0`, so after the swap those resolve to the source's LEFT edge
     * and the target's RIGHT edge -- which is what a right-to-left ribbon
     * needs. Negating them without the swap draws each ribbon 9px into both
     * node rectangles instead of butting against them.
     */
    for (const n of laid.nodes ?? []) {
      const x0 = n.x0 ?? 0;
      const x1 = n.x1 ?? 0;
      n.x0 = CW.value - x0;
      n.x1 = CW.value - x1;
    }

    /*
     * A SECOND LINE ONLY WHERE THAT NODE HAS ROOM, decided PER NODE.
     *
     * This was a count -- eight parties or fewer and every label got two lines
     * -- and a count is the wrong question. Reported from a screenshot at six
     * parties: two small nodes 8px apart each drew two lines, and the second
     * line of the upper one landed on the first line of the lower, rendering
     * as `Jonathans-Mac-mini46c3`. What matters is not how many nodes there
     * are but how far apart THESE two are, and a Sankey spaces nodes by their
     * value, so two thin flows sit almost on top of each other however few of
     * them there are.
     *
     * 16px of centre-to-centre room, because the labels are centred on the
     * node: the second line's baseline falls 9px below the centre and the next
     * label's first line rises 6px above its own. The last node in a column
     * always has room. Where there is none the adapter is still in the
     * tooltip, so nothing is lost, only deferred to a hover.
     */
    const MIN_LABEL_GAP = 16;
    const byColumn = new Map<number, { y: number }[]>();
    for (const n of laid.nodes ?? []) {
      const col = byColumn.get(n.x0 ?? 0) ?? [];
      col.push({ y: ((n.y0 ?? 0) + (n.y1 ?? 0)) / 2 });
      byColumn.set(n.x0 ?? 0, col);
    }
    for (const col of byColumn.values()) col.sort((a, b) => a.y - b.y);
    const hasRoom = (n: { x0?: number; y0?: number; y1?: number }) => {
      const col = byColumn.get(n.x0 ?? 0) ?? [];
      const c = ((n.y0 ?? 0) + (n.y1 ?? 0)) / 2;
      const below = col.find((o) => o.y > c);
      return !below || below.y - c >= MIN_LABEL_GAP;
    };

    return {
      dir,
      title,
      // True when some ribbon hit the floor, so the figure can say that its
      // thinnest marks are bigger than the truth rather than let them read as
      // exact.
      hairline,
      nodes: (laid.nodes ?? []).map((n) => {
        // Scaled about the centre too, so a node's edge is exactly as tall as
        // the ribbons meeting it.
        const c = centre(n);
        /*
         * THE SAME FLOOR AS A RIBBON, and it was missed the first time.
         *
         * Raising MIN_RIBBON widened the ribbons and left the node rectangles
         * at their old 1px minimum, so a small flow drew as a 4px line ending
         * in nothing -- reported from a screenshot of `uplink 0.1` and
         * `lan-usb-6518 1.0`. A node's edge is where its ribbons meet it, so it
         * cannot be thinner than the thinnest of them.
         *
         * Grown about the CENTRE rather than downward from the top, so the
         * label stays where it was: it is centred on the node, and floored
         * one-sidedly the text would drift off a small node as the floor bit.
         */
        const half = Math.max(
          MIN_RIBBON / 2,
          (((n.y1 ?? 0) - (n.y0 ?? 0)) / 2) * scale,
        );
        // The mirror above leaves x0 to the RIGHT of x1, so the rectangle and
        // the label take the visual edges rather than the stored order.
        const vl = Math.min(n.x0 ?? 0, n.x1 ?? 0);
        const vr = Math.max(n.x0 ?? 0, n.x1 ?? 0);
        return {
          label: label(n),
          sub: hasRoom(n) ? (props.sublabels?.[key(n)] ?? '') : '',
          title: nodeTitle(n),
          x0: vl, x1: vr,
          y0: c - half, y1: c + half,
          value: n.value ?? 0,
          colour: clientColour(key(n)),
          // Which half of the figure, so a label sits clear of the ribbons on
          // its own side -- outward for a client, into the centre for the
          // uplink.
          left: vl < CW.value / 2,
        };
      }),
      links: (laid.links ?? []).map((l, i) => ({
        key: i,
        // Carried on the link so the readout knows which figure it belongs to
        // without the template reaching back to the side it came from.
        dir,
        d: path(l) ?? '',
        width: l.width ?? MIN_RIBBON,
        value: l.value,
        packets: (l as Link).packets,
        from: endName(l.source),
        to: endName(l.target),
        // THE NON-UPLINK END, not the source, so a conversation keeps ONE
        // colour across the two figures. Coloured by source, the same
        // conversation with the internet came out as the uplink's hue going
        // down and the client's coming back -- measured on the box, #b98ec4
        // for `uplink → wlan-usb-46c7` against #d4a15a for the reverse. It
        // also puts colour back to meaning identity, which is what
        // AdapterStack says it means and what the node colours here already
        // do. A local pair has no uplink end, so it takes its source and is
        // the same ribbon in both figures anyway.
        colour: clientColour(key(l.source) === (props.uplink ?? '')
          ? key(l.target) : key(l.source)),
      })),
      total,
    };
  });
});

/*
 * WHICH RIBBON IS UNDER THE POINTER, held rather than left to the browser.
 *
 * The <title> elements are still there and are still the accessible name, but
 * they cannot be the readout. A native tooltip waits about a second of
 * stillness before it appears, and every attribute change on the hovered
 * element restarts that wait -- while this figure rewrites every path once a
 * second, for ever. Reported as "hover doesn't seem to do anything", and the
 * elements had been hit-testing perfectly the whole time.
 */
const hover = ref<{ dir: string; key: number } | null>(null);
const hovered = computed(() => {
  const h = hover.value;
  if (!h) return null;
  const side = sides.value.find((sd) => sd.dir === h.dir);
  return side?.links.find((l) => l.key === h.key) ?? null;
});

/**
 * How wide a ribbon's INVISIBLE hit area is.
 *
 * The shared scale draws a small flow as a 1.5px hairline, which is honest and
 * unhittable -- a target that thin needs pixel-perfect aim. So each ribbon
 * carries a transparent twin at least this wide, on the same path, so the
 * target is exactly where the ribbon appears to be and merely thicker.
 */
const HIT_W = 14;

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
        <!-- The hovered ribbon REPLACES the total, in the same slot, so
             pointing at a ribbon answers where the eye already is rather than
             in a tooltip somewhere near the pointer. -->
        <span v-if="hovered && hovered.dir === s.dir" class="stats num read">
          {{ hovered.from }} → {{ hovered.to }} ·
          {{ fmt(hovered.value) }} Mbit/s · {{ pkt(hovered.packets) }} frames/s
        </span>
        <span v-else class="stats num">
          {{ fmt(s.total) }} Mbit/s · {{ WINDOW_MS / 1000 }}s avg
        </span>
        <!-- Only where it is true, and it is true of the thinnest marks only. -->
        <span v-if="s.hairline" class="stats warn-note"
              title="Some ribbons here are below the thinnest line that can be
drawn, so they are shown thicker than they are. Their rates are on the labels.">
          thinnest not to scale
        </span>
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
          <g v-for="l in s.links" :key="l.key" class="ribbon"
             :class="{ on: hover && hover.dir === s.dir && hover.key === l.key }">
            <path class="draw" :d="l.d" :stroke="l.colour" :stroke-width="l.width" />
            <!-- The hit area: transparent, never thinner than HIT_W, and drawn
                 second so it sits above its own ribbon. It changes nothing
                 about the picture and everything about aiming at one. -->
            <path
              class="hit" :d="l.d" :stroke-width="Math.max(HIT_W, l.width)"
              @mouseenter="hover = { dir: s.dir, key: l.key }"
              @mouseleave="hover = null"
            >
              <title>{{ l.from }} → {{ l.to }}: {{ fmt(l.value) }} Mbit/s, {{ pkt(l.packets) }} frames/s</title>
            </path>
          </g>
        </g>
        <g v-for="(n, i) in s.nodes" :key="i">
          <rect :x="n.x0" :y="n.y0" :width="n.x1 - n.x0"
                :height="Math.max(MIN_RIBBON, n.y1 - n.y0)" :fill="n.colour">
            <title>{{ n.title }}: {{ fmt(n.value) }} Mbit/s</title>
          </rect>
          <!-- THE FIRST LINE SITS AT THE NODE'S CENTRE WHETHER OR NOT THERE
               IS A SECOND, and that is the fix for a collision that survived
               raising the padding. Shifting a two-line label up by 2 to centre
               the pair made it encroach UPWARD as well as down, so a one-line
               neighbour 15px above -- sitting 3px below its own centre -- came
               within 10px of it and the boxes met. Anchoring line one and
               hanging the second below means a label only ever grows
               downward, which is the one direction the room check measures. -->
          <text :x="n.left ? n.x0 - 6 : n.x1 + 6"
                :y="(n.y0 + n.y1) / 2 + 3"
                class="lab" :class="{ end: n.left }">
            {{ n.label }} <tspan class="num">{{ fmt(n.value) }}</tspan>
            <!-- The adapter, under the name. Same anchor, so it stays on its
                 own side of the figure without a second x to keep in step. -->
            <tspan v-if="n.sub" class="sub" :x="n.left ? n.x0 - 6 : n.x1 + 6" dy="11"
            >{{ n.sub }}</tspan>
          </text>
        </g>
      </svg>
    </figure>
  </div>
</template>

<style scoped>
/* NO EXPLANATORY NOTE ANY MORE. It was folded away first and then removed
   outright: it was eight lines of prose under a figure that is read at a
   glance. What it said that the drawing cannot is still said in the chrome --
   each heading carries its own averaging window, a floored ribbon is flagged
   beside it as not to scale, and hovering a ribbon names both ends. The rest
   belonged in the source, where it is. */

/* ONE ROW, WITH THE UPLINK IN THE MIDDLE OF THE PAGE.

   Upload on the left and download on the right, which looks backwards until you
   see what it buys. The uplink is the TARGET of upload and the SOURCE of
   download, and a Sankey always draws a source on the left of its target -- so
   in this order the uplink lands at the right edge of the left figure and the
   left edge of the right figure. The two instances meet in the centre of the
   page, and the whole row then reads in one direction: what the clients sent,
   the uplink, what the clients received.

   This replaced download-on-the-left, which was asked for first and which put
   the uplink at the two far edges with the clients in the middle -- the
   opposite arrangement, and the reason the halves read as mirrors of each
   other. `sides` yields up before down, so source order alone places them and
   there is no separate ordering rule to keep in step. */
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
/* BESIDE ITS OWN TITLE, not pushed to the figure's right edge. With the two
   figures side by side, an auto margin here put each one's rate hard against
   the OTHER one's title -- reported from a screenshot as genuinely ambiguous
   about which number belonged to which. */
.stats { margin-left: 0; }
.warn-note { color: var(--warn); }
/* NOT width:100%. The viewBox is in real pixels, so stretching it would put
   the scaling bug straight back. */
svg { display: block; max-width: 100%; }
.links path { fill: none; }
.links .draw {
  stroke-opacity: 0.4;
  transition: stroke-opacity 120ms;
}
/* Driven by the held hover rather than :hover, because the element under the
   pointer is the transparent twin and not the ribbon it belongs to. */
.links .ribbon.on .draw { stroke-opacity: 0.82; }
.links .hit { stroke: transparent; }
.read { color: var(--ink-dim); }
.lab { font-size: 10px; fill: var(--ink-dim); }
.idle { font-size: 11px; fill: var(--ink-faint); }
.lab.end { text-anchor: end; }
.lab .num { fill: var(--ink-faint); }
.lab .sub { fill: var(--ink-faint); font-size: 9px; }
</style>
