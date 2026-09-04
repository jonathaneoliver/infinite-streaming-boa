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
// Nothing to emit: this draws the topology, it does not act on it.

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
// Sized for the BOXES now, which is what this draws.
//
// It used to be sized for the controls: each node carried buttons and a channel
// plan overhanging its rectangle by 65px either side, so the gap had to clear
// two of those or one radio's plan sat under the next radio's. Those moved to
// the rack, and a gap kept at 150px would leave three boxes marooned at the
// edges of a picture whose whole job is to show that they are connected.
const GAP = 40;

// Downstream nodes are centred as a row under the bridge, so the picture stays
// balanced whether there is one radio or three.
const rowWidth = computed(() => {
  const n = downstream.value.length;
  return n * NODE_W + Math.max(0, n - 1) * GAP;
});
// The canvas grows to hold the row rather than the row being squeezed into a
// fixed canvas: a third radio must push the picture wider, not overlap it.
//
// It only has to hold the rectangles now. It used to have to hold the channel
// plans as well, which bled past the outermost boxes and were clipped by a
// canvas sized to the boxes alone; with the plans gone the boxes are the widest
// thing in here again.
const W = computed(() => Math.max(640, rowWidth.value + 40));
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

        <!-- No controls here any more.

             Each node used to carry switch/drop/nudge/steer/scan and a channel
             plan. They now live once, in the adapter rack, on the fold that
             names the same radio. A picture of how the box is wired and a
             surface for acting on it are two different jobs, and doing both
             meant every control existed twice -- which is the thing the rack
             exists to stop. This draws the topology; the rack drives it. -->
      </g>
    </svg>

    <figcaption>
      Clients get their addresses from your existing router; the box is not a hop.
      <template v-if="downstream.some(unwatched)">
        A dashed link marks a radio the daemon is not watching — its clients are
        not conditioned and do not appear in the Clients tab.
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
.node.unwatched .name { fill: var(--warn); }

figcaption {
  color: var(--ink-faint);
  font-size: 12px;
  margin-top: 6px;
}

@media (max-width: 760px) {
  /* The row of downstream nodes stops being legible well before the page does,
     so below this the diagram scrolls rather than shrinking to unreadable. */
  .diagram { overflow-x: auto; }
  svg { min-width: 700px; }
}
</style>
