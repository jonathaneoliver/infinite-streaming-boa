<script setup lang="ts">
import { computed } from 'vue';
import type { IfaceInfo, ScanSummary } from '@/types';
import { describeChannel, rateFor, type Quality } from '@/composables/channelQuality';

/**
 * One radio's band plan: the channels it may be moved to, drawn as a ruler.
 *
 * Extracted from the topology drawing, where it used to hang under each node.
 * A plan is a CONTROL -- pressing a cell takes the access point down and brings
 * it back somewhere else -- and the drawing is a picture of how the box is
 * wired. Keeping a control on the picture meant the same action existed both
 * there and in the rack below, which is the duplication the rack was built to
 * end.
 *
 * Widths stacked and each cell sized in proportion to its bandwidth, so the
 * claim the drawing makes is literally true: an 80MHz cell IS the four 20MHz
 * cells above it. Picking a cell picks a channel AND a width, which is the
 * choice that actually exists.
 */
const props = defineProps<{
  radio: IfaceInfo;
  scans?: Record<string, ScanSummary>;
  busy?: boolean;
}>();
const emit = defineEmits<{ (e: 'move', channel: number, width: number): void }>();

/*
 * A COMPUTED, not `const radio = props.radio`.
 *
 * That plain capture is what this line used to be, and it read as harmless
 * shorthand: `props` is reactive, so the value is right at setup. But it is
 * read ONCE and frozen, and every snapshot replaces `props.radio` with a
 * freshly parsed object -- so after a move the plan went on comparing against
 * the channel the radio was on when the fold was opened, and the highlight
 * never followed. The fold body is `v-if`, so closing and reopening it
 * rebuilt the component and appeared to fix it, which is exactly the shape of
 * bug that survives a manual test.
 *
 * The header token got this right by accident rather than by design: it reads
 * the channel through `useAdapters`, a separate reactive path. Two sources for
 * one fact, disagreeing on screen.
 */
const radio = computed(() => props.radio);


/**
 * The channels this radio can be moved to: its own band's, and only those.
 *
 * The daemon's allowlist, mirrored -- 2.4GHz 1/6/11 and 5GHz 36/40/44/48 plus
 * 149/153/157/161/165, with DFS excluded because the Pi cannot serve an access
 * point on one. Filtered to the band the radio is already on: a move is a
 * down-and-up on the same phy, not a band change, and offering 5GHz channels
 * on a 2.4GHz radio would be offering something the daemon then refuses.
 *
 * The 5GHz set is kept as the BLOCKS it physically is, not as one flat list.
 * 36-48 and 149-161 are each a single 80MHz block, and the entire DFS range
 * sits between them -- so the two cannot be paired across, and drawing them as
 * one ruler would claim a 40MHz cell spanning 48 and 149 exists.
 */
const CHANNELS_24 = [1, 6, 11];
const BLOCKS_5 = [
  [36, 40, 44, 48],
  [149, 153, 157, 161],
];
/** 165 is offered, but alone: `iw phy` marks the channel above it "no IR", so
 *  it has no 40MHz partner and therefore no wider cell of any kind. */
const SOLO_5 = [165];
const CHANNELS_5 = [...BLOCKS_5.flat(), ...SOLO_5];
function channelsFor(radio: IfaceInfo): number[] {
  const ch = radio.ap?.channel ?? 0;
  if (!ch) return [];
  return ch > 14 ? CHANNELS_5 : CHANNELS_24;
}

/**
 * The channel plan: the standard way this is drawn.
 *
 * Router admin pages give you two independent dropdowns, Channel and Channel
 * Width, and say nothing about the fact that they interact -- which is why "36
 * at 80MHz" and "40 at 80MHz" being the same spectrum comes as a surprise. The
 * chart every Wi-Fi analyser and every published band plan uses instead is a
 * frequency ruler with the widths stacked: 20MHz cells on top, 40MHz cells
 * spanning pairs of them, 80MHz spanning four.
 *
 * Cells are sized in proportion to their bandwidth, so the nesting IS the
 * picture -- an 80MHz cell is visibly the four 20MHz cells above it, and no
 * bracket or legend is needed to say so. Picking a cell picks a channel and a
 * width together, which is the choice that actually exists; the two dropdowns
 * were always one decision wearing a disguise.
 */
interface PlanCell {
  /** Empty means this is not a choice: either a filler holding a column open,
   *  or the break between the two 5GHz blocks. */
  channels: number[];
  label: string;
  /** How many 20MHz slots wide, so a cell sits under exactly the cells it is
   *  made of. Taken from the cell rather than from channels.length, because a
   *  filler has no channels and must still hold its width. */
  span: number;
  /** The gap between blocks: fixed width, not proportional, since the spectrum
   *  it stands for is not to scale with anything else here. */
  gap?: boolean;
}
interface PlanRow {
  width: number;
  cells: PlanCell[];
}

const BREAK: PlanCell = { channels: [], label: '', span: 1, gap: true };

/**
 * The column track set, shared by every row of one radio's plan.
 *
 * A GRID rather than a flex row per width, because the whole claim the picture
 * makes is that an 80MHz cell is exactly the four 20MHz cells above it. Under
 * flex it was not: the rows hold different numbers of children, so the 2px gaps
 * between them came to 22px on the 20MHz row and 10px on the 80MHz row, and
 * every row divided a different amount of space. The drift was small enough to
 * look like rounding and is the reason a cell could be a few px narrower than
 * the one beside it.
 *
 * One track per 20MHz channel, a fixed one for each break, and one shared
 * definition for all three rows -- so a cell spanning two tracks also spans the
 * gap between them and lands exactly on the pair it is made of.
 */
function planCols(groups: number[][]): string {
  const blocks = groups
    .map((g) => `repeat(${g.length}, minmax(0, 1fr))`)
    .join(' 7px '); // 7px: the break between blocks, one track wide
  return `14px ${blocks}`; // 14px: the width label down the left
}

/** A row of cells at one width, laid out group by group with a break between
 *  groups so every row breaks in the same place and the columns stay aligned. */
function planRow(groups: number[][], width: number,
                 cellsFor: (g: number[]) => PlanCell[]): PlanRow {
  const cells: PlanCell[] = [];
  groups.forEach((g, n) => {
    if (n) cells.push(BREAK);
    cells.push(...cellsFor(g));
  });
  return { width, cells };
}

/** A group too small for this width contributes a filler, not nothing: drop the
 *  cell entirely and every cell after it slides left out from under the 20MHz
 *  cells it is supposed to sit beneath. */
const filler = (g: number[]): PlanCell[] => [{ channels: [], label: '', span: g.length }];

/** The blocks a radio's band is drawn in, in order. 2.4GHz is a single block:
 *  1/6/11 are one contiguous set of choices with no break in them. */
function planGroups(radio: IfaceInfo): number[][] {
  const chans = channelsFor(radio);
  if (!chans.length) return [];
  return chans[0] <= 14 ? [chans] : [...BLOCKS_5, SOLO_5];
}

/** The rows and the track set together, so the template cannot pair a row with
 *  a grid definition built from different groups. */
function plan(radio: IfaceInfo): { cols: string; rows: PlanRow[] } | null {
  const groups = planGroups(radio);
  if (!groups.length) return null;
  const cols = planCols(groups);
  // 2.4GHz is offered at 20MHz only: 1/6/11 are the only non-overlapping
  // choices, 40MHz eats two of the three, and 80MHz does not exist there.
  if (groups[0][0] <= 14) {
    return {
      cols,
      rows: [planRow(groups, 20, (g) =>
        g.map((c) => ({ channels: [c], label: String(c), span: 1 })))],
    };
  }
  return {
    cols,
    rows: [
      planRow(groups, 20, (g) =>
        g.map((c) => ({ channels: [c], label: String(c), span: 1 }))),
      planRow(groups, 40, (g) => {
        if (g.length < 2) return filler(g);
        const pairs: PlanCell[] = [];
        for (let n = 0; n < g.length; n += 2) {
          const p = g.slice(n, n + 2);
          pairs.push({ channels: p, label: `${p[0]}–${p[p.length - 1]}`, span: p.length });
        }
        return pairs;
      }),
      planRow(groups, 80, (g) =>
        g.length < 4
          ? filler(g)
          : [{ channels: g, label: `${g[0]}–${g[g.length - 1]}`, span: g.length }]),
    ],
  };
}

/** The cell the radio is running right now: same width, and holding its channel. */
function isCurrent(radio: IfaceInfo, row: PlanRow, cell: PlanCell): boolean {
  return (radio.ap?.width_mhz ?? 0) === row.width && cell.channels.includes(radio.ap?.channel ?? 0);
}

/**
 * A block is only as clear as its busiest slice, so a cell takes the WORST
 * rating of the channels it covers. An 80MHz block containing one channel with
 * six neighbours on it is not a quiet block, however empty the other three are.
 */
function cellClass(radio: IfaceInfo, cell: PlanCell): string {
  const order: Quality[] = ['unknown', 'clear', 'busy', 'crowded'];
  let worst: Quality = 'clear';
  for (const c of cell.channels) {
    const q = rateFor(props.scans?.[radio.name], c);
    if (q === 'unknown') return 'q-unknown';
    if (order.indexOf(q) > order.indexOf(worst)) worst = q;
  }
  return `q-${worst}`;
}

function cellNote(radio: IfaceInfo, row: PlanRow, cell: PlanCell): string {
  const per = cell.channels
    .map((c) => `ch ${c}: ${describeChannel(props.scans?.[radio.name], c)}`)
    .join('; ');
  if (isCurrent(radio, row, cell)) return `${radio.name} is here now. ${per}`;
  return (
    `Move ${radio.name} to ${cell.label} at ${row.width} MHz. ${per}. ` +
    `Takes the radio down and brings it back, so all ${radio.ap?.stations ?? 0} ` +
    `client(s) are dropped and NOT told.` +
    (row.width >= 40
      ? ' At this width hostapd picks which slice is the primary, so the channel it reports may be a sibling of the one asked for.'
      : '')
  );
}
</script>

<template>
  <!-- The channel plan, drawn the way band plans are always drawn:
       a frequency ruler with the widths stacked and each cell sized in
       proportion to its bandwidth. Picking a cell picks a channel AND a
       width, which is the choice that actually exists. -->
  <div
    v-if="plan(radio)" class="plan" :class="{ working: busy }"
  >
    <div
      v-for="row in plan(radio)!.rows" :key="row.width"
      class="plan-row" :style="{ gridTemplateColumns: plan(radio)!.cols }"
    >
      <span class="lbl">{{ row.width }}</span>
      <template v-for="(cell, n) in row.cells" :key="`${row.width}-${n}`">
        <!-- The break between the two 5GHz blocks. The DFS range it
             stands for is 500MHz wide and not offered, so it is drawn
             as a break rather than to scale. -->
        <span v-if="cell.gap" class="plan-gap" aria-hidden="true" />
        <!-- A width this block cannot do: holds the column open so the
             cells above and below still line up. -->
        <span
          v-else-if="!cell.channels.length"
          class="plan-filler" :style="{ gridColumn: `span ${cell.span}` }"
          aria-hidden="true"
        />
        <button
          v-else
          class="cell" :class="[cellClass(radio, cell), { here: isCurrent(radio, row, cell) }]"
          :style="{ gridColumn: `span ${cell.span}` }"
          :disabled="busy || isCurrent(radio, row, cell)"
          :title="cellNote(radio, row, cell)"
          @click="emit('move', cell.channels[0], row.width)"
        >{{ cell.label }}</button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.plan { font-family: var(--sans); margin-top: 2px; }
/* Grid, not flex: every row shares one track set, so a cell spanning two tracks
   spans the gap between them too and lands exactly on the pair it is made of.
   Under flex each row divided a different amount of space, because the rows
   hold different numbers of children and so a different number of 2px gaps. */
.plan-row {
  display: grid;
  align-items: stretch;
  gap: 2px;
  margin-bottom: 2px;
}
/* The break between the two 5GHz blocks: its own fixed track, the same in every
   row. They are ~500MHz apart with the whole DFS range between them, so this
   stands for a discontinuity rather than measuring one. */
.plan-gap { }
/* Holds a column open where a block has no cell at this width -- 165 has no
   40MHz partner. Invisible, but it occupies the track. */
.plan-filler { min-width: 0; }
.plan .lbl {
  font-size: 9px;
  color: var(--ink-faint);
  width: 14px;
  flex: none;
  text-align: right;
  line-height: 16px;
}
.cell {
  min-width: 0;
  height: 16px;
  padding: 0 1px;
  font-size: 10px;
  line-height: 1;
  /* One line, always. The cell is 16px tall, so a label that wraps loses its
     second line behind `overflow: hidden` -- "149–153" did exactly that, being
     a couple of px wider than "157–161" beside it because proportional figures
     make "1" narrow and it has one fewer of them. Ellipsis is the fallback if a
     label ever genuinely cannot fit; wrapping is not a fallback at this height,
     it is a label with its middle cut out. */
  white-space: nowrap;
  text-overflow: ellipsis;
  border: 1px solid var(--line);
  border-radius: 3px;
  background: var(--panel-2);
  color: var(--ink-dim);
  cursor: pointer;
  overflow: hidden;
}
.cell:hover:not(:disabled) { background: var(--line); color: var(--ink); }
.cell:disabled { cursor: default; }
/* Where the radio is now. Filled rather than outlined, so it reads as a
   position on the ruler rather than as one more thing to press. */
.cell.here {
  background: var(--line);
  color: var(--ink);
  font-weight: 600;
  border-color: var(--ink-faint);
  opacity: 1;
}
.plan.working .cell { cursor: progress; }
/* Colour is a MEASUREMENT, so it only appears once a scan has been taken:
   q-unknown is the plain cell, and a box nobody has scanned shows no opinion
   at all rather than a wall of green. */
.cell.q-clear { border-color: var(--ok); color: var(--ok); }
.cell.q-busy { border-color: var(--warn); color: var(--warn); }
.cell.q-crowded { border-color: var(--bad); color: var(--bad); }
.node.unwatched .name { fill: var(--warn); }
</style>
