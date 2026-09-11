<script setup lang="ts">
import { computed } from 'vue';
import type { IfaceInfo, ScanSummary } from '@/types';
import { describeChannel, mergeScans, rateFor, type Quality } from '@/composables/channelQuality';

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
  /**
   * The box's OTHER radios, so the plan can show where they already are.
   *
   * Without this the grid happily offers a channel one of our own radios is
   * sitting on, and moving there makes the box interfere with itself for no
   * gain whatever -- two of our access points splitting one channel while the
   * rest of the band sits empty. Measured here: wlan-usb2 occupies 149-161 at
   * 80MHz, which is four of the nine cells wlan-usb is offered.
   */
  others?: IfaceInfo[];
}>();
const emit = defineEmits<{ (e: 'move', channel: number, width: number): void }>();

/**
 * EVERY scan, not this radio's own.
 *
 * Was `props.scans?.[radio.name]`, which tied a plan's colours to a scan by
 * that radio -- and on the mt7921u adapters a scan means taking the access
 * point down and dropping every client. So colouring a 5GHz plan cost an
 * outage, every time.
 *
 * The onboard radio scans both bands while it keeps serving and hears channel
 * 40 and channel 149 perfectly well, so merging removes that cost entirely.
 * Freshest reading per channel wins; see mergeScans.
 */
const merged = computed(() => mergeScans(props.scans));

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
 * The channels this radio can be moved to: every band the radio itself has.
 *
 * The daemon's allowlist, mirrored -- 2.4GHz 1/6/11 and 5GHz 36/40/44/48 plus
 * 149/153/157/161/165, with DFS excluded because the Pi cannot serve an access
 * point on one.
 *
 * THIS USED TO BE FILTERED to the band the radio was already on, and the reason
 * was honest: a move is a down-and-up on the same phy, and the daemon could not
 * change hw_mode, so offering 5GHz on a 2.4GHz radio offered something that
 * would then be refused. MoveChannel now sets hw_mode, ieee80211ac and the
 * centre index across a band change, measured working in both directions on
 * 2026-09-09, so the filter would hide a move that works.
 *
 * Filtered by the radio's CAPABILITY instead, which is the honest limit and a
 * different one: a single-band radio must not be offered the band it does not
 * have. A dual-band radio gets both rulers.
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
  // `bands` is what the phy can actually beacon on, so a dual-band radio gets
  // both rulers and a single-band one is never offered a move it would be
  // refused. Absent on a box running an older daemon, and there the old
  // behaviour is the right fallback: show the band the radio is on, because
  // that daemon cannot cross bands anyway.
  const bands = radio.ap?.bands;
  if (bands?.length) {
    const out: number[] = [];
    if (bands.includes("2.4GHz")) out.push(...CHANNELS_24);
    if (bands.includes("5GHz")) out.push(...CHANNELS_5);
    if (out.length) return out;
  }
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

/** True for a group of 2.4GHz channels. Read from the first channel, since a
 *  group is never mixed: the groups are built per band below. */
const groupIs24 = (g: number[]): boolean => g[0] <= 14;

/** The blocks a radio's bands are drawn in, in order. 2.4GHz is a single block:
 *  1/6/11 are one contiguous set of choices with no break in them. 5GHz is its
 *  two 80MHz blocks and then 165 alone.
 *
 *  A dual-band radio gets 2.4GHz first and then both 5GHz blocks, four groups
 *  with a break between each -- so the ruler reads left to right in frequency
 *  order, and the break between 11 and 36 is the same visual device as the one
 *  between 48 and 149. It stands for a much larger gap, but the plan has never
 *  claimed its breaks are to scale.
 *
 *  DERIVED FROM THE CHANNEL LIST, not from the radio's band field, so the two
 *  cannot disagree about what is on offer. */
function planGroups(radio: IfaceInfo): number[][] {
  const chans = channelsFor(radio);
  if (!chans.length) return [];
  const groups: number[][] = [];
  const has24 = chans.some((c) => c <= 14);
  const has5 = chans.some((c) => c > 14);
  if (has24) groups.push(CHANNELS_24);
  if (has5) groups.push(...BLOCKS_5, SOLO_5);
  return groups;
}

/**
 * The band header: which stretch of the ruler is 2.4GHz and which is 5GHz.
 *
 * Needed the moment a radio can be offered both. Before that the band was
 * implicit -- a plan showed one band and the row you were looking at was the
 * band you were on -- and a ruler that runs 1, 6, 11, 36, 40 ... with nothing
 * marking the join asks the reader to know that 11 and 36 are 2.6GHz apart.
 *
 * SPANS RUNS OF GROUPS, not single groups. The 5GHz side is three groups --
 * 36-48, 149-161, and 165 alone -- and labelling each of them "5GHz" would say
 * the same thing three times and imply three bands. One label covers the run,
 * including the break tracks inside it, so it lines up with exactly the
 * channels it names.
 */
function planBandRow(groups: number[][]): PlanCell[] {
  const cells: PlanCell[] = [];
  let n = 0;
  while (n < groups.length) {
    const is24 = groupIs24(groups[n]);
    // How many groups this band runs for, and how many tracks that covers: the
    // channels themselves plus the break track sitting between each pair.
    let span = 0;
    const from = n;
    while (n < groups.length && groupIs24(groups[n]) === is24) {
      if (n > from) span += 1;
      span += groups[n].length;
      n += 1;
    }
    if (from) cells.push(BREAK);
    cells.push({ channels: [], label: is24 ? '2.4GHz' : '5GHz', span });
  }
  return cells;
}

/** The rows and the track set together, so the template cannot pair a row with
 *  a grid definition built from different groups. */
function plan(radio: IfaceInfo): {
  cols: string; rows: PlanRow[]; bands: PlanCell[];
} | null {
  const groups = planGroups(radio);
  if (!groups.length) return null;
  const cols = planCols(groups);
  const bands = planBandRow(groups);
  // 2.4GHz is offered at 20MHz only: 1/6/11 are the only non-overlapping
  // choices, 40MHz eats two of the three, and 80MHz does not exist there. So a
  // 2.4GHz-only radio gets a single row, and on a dual-band radio the 2.4GHz
  // GROUP contributes a filler to the wider rows rather than cells.
  //
  // A filler and not nothing, for the reason filler() gives: dropping the cell
  // slides every cell after it left, out from under the 20MHz cells it is meant
  // to sit beneath. Before this, a dual-band radio pushed all its channels into
  // one group, the plan read that group as 2.4GHz, and the 40 and 80MHz rows
  // vanished entirely -- the 5GHz widths gone from a radio that has them.
  const wide = (g: number[], build: (g: number[]) => PlanCell[]): PlanCell[] =>
    groupIs24(g) ? filler(g) : build(g);

  const twenty = planRow(groups, 20, (g) =>
    g.map((c) => ({ channels: [c], label: String(c), span: 1 })));
  if (groups.every(groupIs24)) return { cols, rows: [twenty], bands };

  return {
    cols,
    bands,
    rows: [
      twenty,
      planRow(groups, 40, (g) => wide(g, (g5) => {
        if (g5.length < 2) return filler(g5);
        const pairs: PlanCell[] = [];
        for (let n = 0; n < g5.length; n += 2) {
          const p = g5.slice(n, n + 2);
          pairs.push({ channels: p, label: `${p[0]}–${p[p.length - 1]}`, span: p.length });
        }
        return pairs;
      })),
      planRow(groups, 80, (g) => wide(g, (g5) =>
        g5.length < 4
          ? filler(g5)
          : [{ channels: g5, label: `${g5[0]}–${g5[g5.length - 1]}`, span: g5.length }])),
    ],
  };
}

/**
 * Which channels each OTHER radio of ours occupies, keyed by channel.
 *
 * The whole block it fills, not the one it beacons on: a radio on 149 at 80MHz
 * is using 149, 153, 157 and 161, and a plan that only marked 149 would offer
 * 157 as though it were free. Same widening the scan applies to a neighbour's
 * spectrum -- ours is no different from anyone else's, except that we can see
 * it exactly rather than inferring it from a beacon.
 */
const takenBy = computed<Map<number, string>>(() => {
  const out = new Map<number, string>();
  for (const o of props.others ?? []) {
    const ch = o.ap?.channel ?? 0;
    const w = o.ap?.width_mhz ?? 20;
    if (!ch || !o.ap?.enabled) continue;
    // A radio with its access point down occupies nothing, and blocking a
    // channel on its behalf would be blocking it for a radio that is not there.
    const block = BLOCKS_5.find((b) => b.includes(ch));
    let held = [ch];
    if (block && w >= 80) held = block;
    else if (block && w >= 40) {
      const i = block.indexOf(ch);
      held = block.slice(i - (i % 2), i - (i % 2) + 2);
    }
    for (const c of held) out.set(c, o.name);
  }
  return out;
});

/** The radio of ours sitting on this cell, or '' -- any overlap counts, since
 *  a cell you cannot have all of is a cell you cannot have. */
function takenName(cell: PlanCell): string {
  for (const c of cell.channels) {
    const who = takenBy.value.get(c);
    if (who) return who;
  }
  return '';
}

/** The cell the radio is running right now: same width, and holding its channel. */
function isCurrent(radio: IfaceInfo, row: PlanRow, cell: PlanCell): boolean {
  return (radio.ap?.width_mhz ?? 0) === row.width && cell.channels.includes(radio.ap?.channel ?? 0);
}

/**
 * The cell holding the PRIMARY channel -- the one the radio actually beacons on
 * -- at any width.
 *
 * `here` marks the block a radio occupies, and at 80MHz that block is four
 * channels wide. Which of the four is primary is invisible in it, and that is
 * the fact an operator needs: the primary decides where beacons go, where CCA
 * happens, which sub-channel a 20MHz-only client associates on -- and, the
 * moment the radio narrows, it IS the radio's location.
 *
 * It cannot be inferred from what was asked for, either. hostapd's 20/40
 * coexistence scan swaps primary and secondary to dodge neighbours, so a radio
 * asked for 36 at 80MHz comes back running 40. This marks the value read back
 * from the running radio, so the swap is visible rather than only in the log.
 */
function isPrimary(radio: IfaceInfo, cell: PlanCell): boolean {
  const ch = radio.ap?.channel ?? 0;
  return !!ch && !!radio.ap?.enabled && cell.channels.includes(ch);
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
    const q = rateFor(merged.value, c);
    if (q === 'unknown') return 'q-unknown';
    if (order.indexOf(q) > order.indexOf(worst)) worst = q;
  }
  return `q-${worst}`;
}

function cellNote(radio: IfaceInfo, row: PlanRow, cell: PlanCell): string {
  const per = cell.channels
    .map((c) => `ch ${c}: ${describeChannel(merged.value, c)}`)
    .join('; ');
  if (isCurrent(radio, row, cell)) {
    return `${radio.name} is here now, beaconing on channel ${radio.ap?.channel}. ${per}`;
  }
  if (isPrimary(radio, cell)) {
    return (
      `Channel ${radio.ap?.channel} is ${radio.name}'s primary — the one it ` +
      `beacons on — so this is where it lands if narrowed to ${row.width} MHz ` +
      `without moving. ${per}`
    );
  }
  // Our OWN radio, which is a refusal rather than a warning: putting two of the
  // box's access points on one channel halves both for nothing, while the rest
  // of the band sits empty. Named, so the answer to "why can I not press this"
  // is on the cell rather than left to be worked out.
  const who = takenName(cell);
  if (who) {
    return (
      `${who} is already here, so ${radio.name} cannot move onto it — two of ` +
      `this box's own radios sharing a channel split it between themselves ` +
      `and gain nothing. Move ${who} first if you want this block. ${per}`
    );
  }
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
    <!-- Which stretch is which band. On the same track set as the rows below,
         so a label covers exactly the channels it names rather than being
         positioned to look as though it does. -->
    <div
      class="plan-row plan-bands"
      :style="{ gridTemplateColumns: plan(radio)!.cols }"
    >
      <span class="lbl" aria-hidden="true" />
      <template v-for="(cell, n) in plan(radio)!.bands" :key="`band-${n}`">
        <span v-if="cell.gap" class="plan-gap" aria-hidden="true" />
        <span
          v-else class="band-lbl" :style="{ gridColumn: `span ${cell.span}` }"
        >{{ cell.label }}</span>
      </template>
    </div>
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
          class="cell"
          :class="[cellClass(radio, cell), {
            here: isCurrent(radio, row, cell),
            primary: isPrimary(radio, cell) && !isCurrent(radio, row, cell),
            taken: !isCurrent(radio, row, cell) && !!takenName(cell),
          }]"
          :style="{ gridColumn: `span ${cell.span}` }"
          :disabled="busy || isCurrent(radio, row, cell) || !!takenName(cell)"
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
/* The band header. Quieter than the width labels down the left, because it
   names the spectrum rather than offering a choice: nothing here is clickable,
   and it must not read as a row of cells that has stopped working. */
.plan-bands { margin-bottom: 3px; }
.band-lbl {
  min-width: 0;
  font-size: 9px;
  letter-spacing: 0.04em;
  color: var(--ink-faint);
  text-align: center;
  line-height: 11px;
  /* A hairline under the label, so the eye reads it as a bracket over the
     channels beneath rather than as a caption floating above the whole plan. */
  border-bottom: 1px solid var(--rule, rgba(255, 255, 255, 0.12));
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
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
.plan.working .cell { cursor: progress; }
/* Colour is a MEASUREMENT, so it only appears once a scan has been taken:
   q-unknown is the plain cell, and a box nobody has scanned shows no opinion
   at all rather than a wall of green. */
.cell.q-clear { border-color: var(--ok); color: var(--ok); }
.cell.q-busy { border-color: var(--warn); color: var(--warn); }
.cell.q-crowded { border-color: var(--bad); color: var(--bad); }
/* Where the radio is NOW, and it has to win the row at a glance.
   
   The same treatment `button.accent` gives a chosen profile -- filled in the
   accent colour with the background as the text -- because that is already what
   "this is the selected one" looks like everywhere else here, and a grid of
   forty cells is the last place to invent a second vocabulary for it.
   
   It was a grey fill (`--line`) with a faint border, which lost twice over: the
   fill was barely a shade off the unselected `--panel-2` beside it, and the
   border it set was then overridden by the quality rules above, so "here" came
   down to slightly bolder text in a 10px font.
   
   AFTER the quality rules, deliberately. Those set `color` as well as
   `border-color`, so declared earlier this would have had its text colour
   replaced by the channel's rating and rendered green-on-blue. Only `color` and
   `background` are set here, which leaves `border-color` to the rating -- so a
   crowded channel you are sitting on still shows a red edge around the blue.
   Both facts, one cell. */
/* The primary channel, on rows the radio is NOT currently at.

   A left edge rather than a fill or a border. Fill is taken by `here` and a
   border is taken by the quality rating, and both of those are facts this must
   not displace -- a crowded channel you would narrow onto still needs its red
   edge. A thick inside edge is the one piece of the cell nothing else claims.

   Deliberately quieter than `here`: this is not where the radio is, it is where
   narrowing would put it. */
.cell.primary { box-shadow: inset 3px 0 0 var(--down); }

.cell.here {
  background: var(--down);
  color: var(--bg);
  font-weight: 700;
  opacity: 1;
}
/* Occupied by ANOTHER of our radios. Struck through and dimmed rather than
   merely disabled, because a cell that is simply unclickable reads as a bug --
   the operator presses it, nothing happens, and nothing says why. The tooltip
   names which radio is there and what to do about it. */
.cell.taken {
  background: repeating-linear-gradient(
    -45deg, var(--panel-2) 0 3px, var(--line-soft) 3px 6px);
  color: var(--ink-faint);
  border-color: var(--line);
  cursor: not-allowed;
}
.node.unwatched .name { fill: var(--warn); }
</style>
