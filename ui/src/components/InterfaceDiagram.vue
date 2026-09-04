<script setup lang="ts">
import { computed } from 'vue';
import type { BridgeInfo, IfaceInfo, ScanSummary } from '@/types';
import {
  describeChannel,
  rateFor,
  type Quality,
} from '@/composables/channelQuality';
import { phyCeilingLabel } from '@/types';

/*
 * The bridge, drawn from live data.
 *
 * The README has carried this shape as ASCII since the beginning; this is the
 * same picture with the box's actual interfaces, MACs and addresses in it. The
 * point is the topology, which is the thing people get wrong about this
 * appliance: the WAN port is where uplink conditioning happens, br-lan is ONE
 * layer-2 segment, and everything below it is downstream.
 *
 * Inline SVG, no library. It is five boxes and some lines, and the artefact
 * this replaces was a code fence.
 */

const props = defineProps<{
  info: BridgeInfo;
  /**
   * An action is in flight. Every node button disables while it is.
   *
   * Not cosmetic. Switching a radio back on measures 4.5s on the mt7921u --
   * the rfkill unblock is instant, the wait is hostapd rebuilding the BSS at
   * 80MHz -- and a button that stays live through it reads as a button that
   * did nothing, which is how you get someone pressing it three more times.
   */
  busy?: boolean;
  /**
   * The last scan per radio, for colouring the channel buttons. Absent until
   * someone presses scan, and the buttons stay uncoloured until then rather
   * than guessing.
   */
  scans?: Record<string, ScanSummary>;
}>();
const emit = defineEmits<{
  // `arg` carries the channel AND width for kind 'channel', and is unused
  // otherwise. Both together because they are one choice: a channel number
  // means nothing without the width it is a channel of.
  (e: 'action',
   kind: 'power-off' | 'power-on' | 'scan' | 'drop' | 'nudge' | 'steer' | 'channel',
   iface: string, arg?: { channel: number; width: number }): void;
}>();

const byRole = (r: string) => props.info.ifaces.filter((i) => i.role === r);
const wan = computed(() => byRole('wan')[0]);
const bridge = computed(() => props.info.ifaces.find((i) => i.role === 'bridge'));
// Radios and the wired downstream port, in one row beneath the bridge.
const downstream = computed(() =>
  props.info.ifaces.filter((i) => i.role === 'ap' || i.role === 'radio' || i.role === 'lan'),
);

const NODE_W = 190;
// 84, not 74: an access point node carries a fourth line, the PHY ceiling its
// width and mode imply. Uniform across every node so the row still reads as one
// band of boxes -- a taller radio beside a shorter WAN port would look like a
// difference in kind rather than in content.
const NODE_H = 84;
// Sized for the CONTROLS, not the boxes. Each downstream node's buttons and
// channel plan overhang its rectangle by PLAN_BLEED either side, so a gap
// chosen for bare rectangles put one radio's channel plan underneath the next
// radio's.
//
// Wide enough for the WIDEST plan, which is a 5GHz radio's: nine 20MHz channels
// across two blocks, where 2.4GHz has three. At the old 64 the columns came to
// ~26px and "149" did not fit in one -- it ellipsised to "1…". This costs
// nothing at three downstream nodes: the row is still narrower than the 940
// minimum canvas, so the picture does not grow, the nodes just sit further
// apart.
const PLAN_BLEED = 65;
const GAP = 2 * PLAN_BLEED + 20; // 20px of clear air between adjacent plans

// Downstream nodes are centred as a row under the bridge, so the picture stays
// balanced whether there is one radio or three.
const rowWidth = computed(() => {
  const n = downstream.value.length;
  return n * NODE_W + Math.max(0, n - 1) * GAP;
});
// The canvas grows to hold the row rather than the row being squeezed into a
// fixed canvas: a third radio must push the picture wider, not overlap it.
//
// It must hold the PLANS, not the rectangles: the outermost nodes' plans bleed
// PLAN_BLEED past their boxes on the outward side, and a canvas sized to the
// boxes alone clips them. Measured after widening the bleed for the 5GHz plan,
// the left edge of "36" and the whole 20/40/80 label column fell off the
// canvas, because the row was centred inside a width that never accounted for
// the overhang.
const W = computed(() => Math.max(940, rowWidth.value + 2 * PLAN_BLEED + 20));
const rowX = computed(() => (W.value - rowWidth.value) / 2);
const nodeX = (i: number) => rowX.value + i * (NODE_W + GAP);

/**
 * EVERY address on the bridge, not just the first.
 *
 * The bridge is the only interface on the box that carries an IP at all -- the
 * ports are layer 2, because the box is not a hop -- and it normally carries
 * TWO: the address your router leased it, and BOA_RESCUE_IP, the static
 * fallback that keeps the box reachable when no DHCP answers.
 *
 * Showing one of them was showing the wrong one. The kernel lists the rescue
 * address first and reports both as permanent, so there is nothing in `ip addr`
 * that says which is the lease -- and a box displaying only 192.168.99.1 looks
 * exactly like a box that never got one. Both, always: it is at most two lines
 * and it is the address people actually type.
 */
function bridgeAddrs(i: IfaceInfo): string[] {
  const out = [...(i.ipv4 ?? []), ...routableV6(i)];
  return out.length ? out : [i.mac ?? ''];
}

/**
 * The IPv6 addresses worth putting on screen: global and unique-local, never
 * link-local.
 *
 * fe80:: is on every interface, is not an identifier for the box, and cannot be
 * used without a %interface scope naming the CLIENT's interface -- so printing
 * it here would be offering an address that does not work when pasted. The ULA
 * is the one mDNS hands out and the one that answers ssh.
 */
function routableV6(i: IfaceInfo): string[] {
  return (i.ipv6 ?? []).filter((a) => !a.toLowerCase().startsWith('fe80:'));
}

/** The bridge box grows a line per extra address rather than hiding one. */
const ADDR_LINE = 17;
// Wider than the others, because it is the only node carrying an IPv6 address
// and 40 characters of one do not fit in 190px. It is centred with room either
// side, so widening it costs the layout nothing.
const BR_W = 340;
const brH = computed(() =>
  bridge.value ? NODE_H + ADDR_LINE * (bridgeAddrs(bridge.value).length - 1) : NODE_H,
);

const CX = computed(() => W.value / 2);
const WAN_Y = 8;
const BR_Y = 132;
// The downstream row hangs off the BOTTOM of the bridge box, which is not a
// constant: it grows with the number of addresses on the bridge.
const DOWN_Y = computed(() => BR_Y + brH.value + 50);
// Room under the downstream row for two rows of per-radio action buttons.
const H = computed(() => DOWN_Y.value + NODE_H + 166);

/**
 * The band a channel is in. Derived from the channel number rather than read,
 * because a channel number IS a frequency: 1-14 are 2.4GHz and everything above
 * is 5GHz, with no overlap to be ambiguous about.
 *
 * Shown FIRST on the node, ahead of the channel, because it is the fact that
 * predicts how a client will behave -- range, throughput, and whether the
 * microwave matters. "ch 36" says the same thing to someone who has memorised
 * the table, and nothing to anyone else.
 */
function bandOf(channel: number | undefined): string {
  if (!channel) return '';
  return channel > 14 ? '5GHz' : '2.4GHz';
}

function subtitle(i: IfaceInfo): string {
  if (i.role === 'ap' && i.ap) {
    const bits = [bandOf(i.ap.channel), `ch ${i.ap.channel}`].filter(Boolean);
    if (i.ap.width_mhz) bits.push(`${i.ap.width_mhz} MHz`);
    if (i.ap.mode) bits.push(i.ap.mode);
    return bits.join(' · ');
  }
  if (i.role === 'radio') return i.up ? 'idle' : 'down';
  if (i.speed_mbps) return `${i.speed_mbps} Mb/s`;
  if (!i.up) return 'down';
  return '';
}

/** A radio the daemon does not watch gets marked, because its clients are
 *  conditioned by nothing and appear nowhere in the Clients tab. */
const unwatched = (i: IfaceInfo) => i.wireless && !i.serving;

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
function channelsFor(i: IfaceInfo): number[] {
  const ch = i.ap?.channel ?? 0;
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
function planGroups(i: IfaceInfo): number[][] {
  const chans = channelsFor(i);
  if (!chans.length) return [];
  return chans[0] <= 14 ? [chans] : [...BLOCKS_5, SOLO_5];
}

/** The rows and the track set together, so the template cannot pair a row with
 *  a grid definition built from different groups. */
function plan(i: IfaceInfo): { cols: string; rows: PlanRow[] } | null {
  const groups = planGroups(i);
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
function isCurrent(i: IfaceInfo, row: PlanRow, cell: PlanCell): boolean {
  return (i.ap?.width_mhz ?? 0) === row.width && cell.channels.includes(i.ap?.channel ?? 0);
}

/**
 * A block is only as clear as its busiest slice, so a cell takes the WORST
 * rating of the channels it covers. An 80MHz block containing one channel with
 * six neighbours on it is not a quiet block, however empty the other three are.
 */
function cellClass(i: IfaceInfo, cell: PlanCell): string {
  const order: Quality[] = ['unknown', 'clear', 'busy', 'crowded'];
  let worst: Quality = 'clear';
  for (const c of cell.channels) {
    const q = rateFor(props.scans?.[i.name], c);
    if (q === 'unknown') return 'q-unknown';
    if (order.indexOf(q) > order.indexOf(worst)) worst = q;
  }
  return `q-${worst}`;
}

function cellNote(i: IfaceInfo, row: PlanRow, cell: PlanCell): string {
  const per = cell.channels
    .map((c) => `ch ${c}: ${describeChannel(props.scans?.[i.name], c)}`)
    .join('; ');
  if (isCurrent(i, row, cell)) return `${i.name} is here now. ${per}`;
  return (
    `Move ${i.name} to ${cell.label} at ${row.width} MHz. ${per}. ` +
    `Takes the radio down and brings it back, so all ${i.ap?.stations ?? 0} ` +
    `client(s) are dropped and NOT told.` +
    (row.width >= 40
      ? ' At this width hostapd picks which slice is the primary, so the channel it reports may be a sibling of the one asked for.'
      : '')
  );
}

/** Whether any radio is running wide enough for the grouping to mean something. */
const anyWide = computed(() =>
  props.info.ifaces.some((i) => (i.ap?.width_mhz ?? 0) >= 40),
);

/** Whether any cell on screen is actually coloured, which is what decides
 *  whether a colour legend is a key or a puzzle. */
const anyScanned = computed(() =>
  Object.keys(props.scans ?? {}).length > 0,
);

/**
 * A channel's colour, from that radio's own last scan.
 *
 * Per radio, not global: a 5GHz scan says nothing about which 2.4GHz channel is
 * quiet, and colouring one band's buttons with the other band's findings would
 * be worse than leaving them grey.
 */
const chanClass = (i: IfaceInfo, c: number) =>
  `q-${rateFor(props.scans?.[i.name], c)}`;
const chanNote = (i: IfaceInfo, c: number) =>
  describeChannel(props.scans?.[i.name], c);

/** The radio a client could be steered to: another one actually serving. */
function otherRadio(i: IfaceInfo): string {
  return props.info.ifaces.find(
    (o) => o.wireless && o.name !== i.name && o.ap?.enabled,
  )?.name ?? '';
}
</script>

<template>
  <figure class="diagram">
    <svg :viewBox="`0 0 ${W} ${H}`" role="img"
         aria-label="The bridge: WAN port above, br-lan in the middle, radios and wired port below">
      <!-- Lines first, so the boxes sit on top of them. -->
      <g class="wire">
        <line v-if="wan" :x1="CX" :y1="WAN_Y + NODE_H" :x2="CX" :y2="BR_Y" />
        <template v-for="(i, n) in downstream" :key="`w-${i.name}`">
          <path
            :d="`M ${CX} ${BR_Y + brH} V ${BR_Y + brH + 26}
                 H ${nodeX(n) + NODE_W / 2} V ${DOWN_Y}`"
            :class="{ dashed: unwatched(i) }"
          />
        </template>
      </g>

      <!-- Upstream. Named separately because uplink conditioning lives on this
           port and nowhere else -- the single most misread fact about the box. -->
      <g v-if="wan" class="node wan">
        <rect :x="CX - NODE_W / 2" :y="WAN_Y" :width="NODE_W" :height="NODE_H" rx="8" />
        <text :x="CX" :y="WAN_Y + 24" class="name">{{ wan.name }}</text>
        <text :x="CX" :y="WAN_Y + 42" class="sub">to your router · uplink shaped here</text>
        <text :x="CX" :y="WAN_Y + 60" class="mono">{{ wan.mac }}</text>
      </g>

      <g v-if="bridge" class="node bridge">
        <rect :x="CX - BR_W / 2" :y="BR_Y" :width="BR_W" :height="brH" rx="8" />
        <text :x="CX" :y="BR_Y + 22" class="name">{{ bridge.name }}</text>
        <text :x="CX" :y="BR_Y + 39" class="sub">one layer-2 segment</text>
        <text
          v-for="(a, k) in bridgeAddrs(bridge)" :key="a"
          :x="CX" :y="BR_Y + 56 + k * ADDR_LINE" class="mono"
        >{{ a }}</text>
      </g>

      <g v-for="(i, n) in downstream" :key="i.name"
         class="node" :class="[i.role, { unwatched: unwatched(i), off: i.power_known && !i.powered }]">
        <rect :x="nodeX(n)" :y="DOWN_Y" :width="NODE_W" :height="NODE_H" rx="8" />
        <text :x="nodeX(n) + NODE_W / 2" :y="DOWN_Y + 22" class="name">{{ i.name }}</text>
        <text :x="nodeX(n) + NODE_W / 2" :y="DOWN_Y + 39" class="sub">{{ subtitle(i) }}</text>
        <!-- A SEPARATE line, not appended to the summary above.
             The summary describes what the radio IS -- band, channel, width,
             mode -- and changes only when someone changes it. A rate belongs
             beside it rather than inside it: the negotiated PHY moves
             constantly with the client's rate control, and a volatile number
             spliced into a stable identity string makes the whole line churn.
             What is shown here is the CEILING the configuration implies, which
             is stable for that reason, and "up to" because the streams that
             reach it are the client's to bring. -->
        <text
          v-if="i.ap && phyCeilingLabel(i.ap.mode ?? '', i.ap.width_mhz ?? 0)"
          :x="nodeX(n) + NODE_W / 2" :y="DOWN_Y + 56" class="phy"
        >{{ phyCeilingLabel(i.ap.mode ?? '', i.ap.width_mhz ?? 0) }}</text>
        <text :x="nodeX(n) + NODE_W / 2" :y="DOWN_Y + 73" class="mono">{{ i.mac }}</text>

        <!-- Actions on the component they act on. A radio in this picture is
             the thing being cut or scanned, so the control belongs on it
             rather than in a list further down the page that has to name it
             again. foreignObject because these are real buttons: focusable,
             keyboard-reachable, and styled like every other button here. -->
        <foreignObject
          v-if="i.wireless && i.ap"
          :x="nodeX(n) - PLAN_BLEED" :y="DOWN_Y + NODE_H + 6"
          :width="NODE_W + 2 * PLAN_BLEED" height="128"
        >
          <!-- Two rows, grouped as the panel below is: what changes WHO IS
               CONNECTED on top, the rest under it. The picture is where you are
               already pointing at a radio, so the actions on that radio belong
               on it. -->
          <div class="node-actions" :class="{ working: busy }">
            <button
              class="ghost"
              :class="{ accent: i.power_known && !i.powered }"
              :disabled="busy"
              :title="i.powered
                ? `Switch ${i.name} off and leave it off. Clients are told NOTHING and must time out.`
                : `${i.name} is switched off. Switch it back on.`"
              @click="emit('action', i.powered ? 'power-off' : 'power-on', i.name)"
            >{{ i.powered ? 'switch off' : 'switch on' }}</button>
            <button
              class="ghost" :disabled="busy || !i.ap.stations"
              :title="`Deauthenticate all ${i.ap.stations} client(s) on ${i.name}. They are told, so they reconnect quickly.`"
              @click="emit('action', 'drop', i.name)"
            >drop {{ i.ap.stations }}</button>
          </div>
          <div class="node-actions" :class="{ working: busy }">
            <button
              class="ghost" :disabled="busy || !i.ap.stations"
              :title="`Disassociate all ${i.ap.stations} client(s) — the softer transition.`"
              @click="emit('action', 'nudge', i.name)"
            >nudge</button>
            <button
              v-if="otherRadio(i)"
              class="ghost" :disabled="busy || !i.ap.stations"
              :title="`Ask all ${i.ap.stations} client(s) to move to ${otherRadio(i)} (802.11v). They may refuse.`"
              @click="emit('action', 'steer', i.name)"
            >steer</button>
            <button
              class="ghost" :disabled="busy"
              :title="`Scan ${i.name}'s band. A few beacon gaps, or an outage if this radio will not scan while serving.`"
              @click="emit('action', 'scan', i.name)"
            >scan</button>
          </div>
          <!-- The channel plan, drawn the way band plans are always drawn:
               a frequency ruler with the widths stacked and each cell sized in
               proportion to its bandwidth. Picking a cell picks a channel AND a
               width, which is the choice that actually exists. -->
          <div
            v-if="plan(i)" class="plan" :class="{ working: busy }"
          >
            <div
              v-for="row in plan(i)!.rows" :key="row.width"
              class="plan-row" :style="{ gridTemplateColumns: plan(i)!.cols }"
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
                  class="cell" :class="[cellClass(i, cell), { here: isCurrent(i, row, cell) }]"
                  :style="{ gridColumn: `span ${cell.span}` }"
                  :disabled="busy || isCurrent(i, row, cell)"
                  :title="cellNote(i, row, cell)"
                  @click="emit('action', 'channel', i.name,
                                { channel: cell.channels[0], width: row.width })"
                >{{ cell.label }}</button>
              </template>
            </div>
          </div>
        </foreignObject>
      </g>
    </svg>

    <figcaption>
      Clients get their addresses from your existing router; the box is not a hop.
      <template v-if="downstream.some(unwatched)">
        A dashed link marks a radio the daemon is not watching — its clients are
        not conditioned and do not appear in the Clients tab.
      </template>
      <template v-if="anyWide">
        Under each radio is its channel plan, widths stacked: a cell on the 40
        or 80&#8239;MHz row <strong>is</strong> the 20&#8239;MHz cells above it,
        so picking one chooses a channel and a width together. Colours come from
        that radio's last scan — press <em>scan</em> to take one.
      </template>
      <!-- The colours are a measurement, not advice: they describe the moment
           that radio's scan ran. No scan, no legend -- a box nobody has scanned
           must not be given a key to colours it is not showing. -->
      <template v-if="anyScanned">
        <span class="legend">
          <span class="q q-clear">clear</span>
          <span class="q q-busy">busy</span>
          <span class="q q-crowded">crowded</span>
        </span>
      </template>
      <!-- The other way to change channel, and why it is not a cell you can
           press. 802.11h CSA counts the switch down in the beacons and clients
           follow WITHOUT dropping -- strictly better, and refused by both chips
           on this box (measured on brcmfmac and mt7921u, #154). A control that
           can only ever report a refusal is worse than a sentence saying so, so
           this is the sentence. POST .../channel still exists and still works
           the day a driver gains support. -->
      <template v-if="downstream.some((i) => i.wireless && i.ap)">
        Moving takes the radio down and back up, so its clients are dropped and
        not told: 802.11h would move them without dropping anyone, but
        <strong>both radios here refuse it</strong> (#154).
      </template>
    </figcaption>
  </figure>
</template>

<style scoped>
.diagram { margin: 0 0 20px; }
svg { width: 100%; height: auto; display: block; }

.wire line, .wire path {
  stroke: var(--line);
  stroke-width: 2;
  fill: none;
}
/* Dashed, not red: an unwatched radio is a limitation of this build, not a
   fault in the wiring. */
.wire path.dashed { stroke-dasharray: 5 5; stroke: var(--warn); }

.node rect {
  fill: var(--panel);
  stroke: var(--line);
  stroke-width: 1;
}
.node text { text-anchor: middle; }
.node .name { fill: var(--ink); font-size: 14px; font-weight: 600; }
.node .sub { fill: var(--ink-dim); font-size: 11px; }
/* Quieter than the summary above it: this is a consequence of the line above,
   not another fact about the radio. Same size as the MAC, which is the other
   derived-detail line. */
.node .phy { fill: var(--ink-faint); font-size: 11px; }
.node .mono {
  fill: var(--ink-faint);
  font-size: 11px;
  font-family: var(--mono);
}

/* The WAN port is where uplink conditioning is applied, so it is the one node
   that carries a direction colour. */
.node.wan rect { stroke: color-mix(in srgb, var(--up) 45%, var(--line)); }
.node.wan .name { fill: var(--up); }
.node.ap rect { stroke: color-mix(in srgb, var(--down) 45%, var(--line)); }
.node.ap .name { fill: var(--down); }
.node.lan rect { stroke: color-mix(in srgb, var(--ok) 40%, var(--line)); }
.node.unwatched rect { stroke: var(--warn); stroke-dasharray: 4 4; }
/* Powered off: the AP is silent and no client has been told. Dimmed
   rather than coloured as a fault, because it is a deliberate state. */
.node.off rect { stroke: var(--ink-faint); stroke-dasharray: 2 4; }
.node.off .name, .node.off .sub { fill: var(--ink-faint); }
.node-actions {
  display: flex; gap: 5px; justify-content: center;
  font-family: var(--sans);
  margin-bottom: 4px;
}
.node-actions button { font-size: 11px; padding: 2px 7px; }
.node-actions button:disabled { opacity: 0.4; cursor: default; }
/* While something is in flight the whole cluster says so, not just the button
   that was pressed: a power-on takes seconds and any other press would queue
   behind it. */
.node-actions.working button { cursor: progress; }
/* The plan is a ruler, so the cells FILL the width and their proportions carry
   the meaning. Nothing is centred and nothing is padded to fit: an 80MHz cell
   is exactly as wide as the four 20MHz cells above it because it is them. */
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

figcaption {
  color: var(--ink-faint);
  font-size: 12px;
  margin-top: 6px;
}
/* The legend borrows the cells' own colours rather than restating them, so a
   change to one cannot drift from the other. */
.legend { display: inline-flex; gap: 8px; margin: 0 4px; }
.legend .q { font-family: var(--mono); font-size: 11px; }
.legend .q-clear { color: var(--ok); }
.legend .q-busy { color: var(--warn); }
.legend .q-crowded { color: var(--bad); }

@media (max-width: 760px) {
  /* The row of downstream nodes stops being legible well before the page does,
     so below this the diagram scrolls rather than shrinking to unreadable. */
  .diagram { overflow-x: auto; }
  svg { min-width: 700px; }
}
</style>
