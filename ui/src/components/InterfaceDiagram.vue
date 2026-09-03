<script setup lang="ts">
import { computed } from 'vue';
import type { BridgeInfo, IfaceInfo } from '@/types';

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

const props = defineProps<{ info: BridgeInfo }>();
const emit = defineEmits<{
  (e: 'action',
   kind: 'power-off' | 'power-on' | 'scan' | 'drop' | 'nudge' | 'steer',
   iface: string): void;
}>();

const byRole = (r: string) => props.info.ifaces.filter((i) => i.role === r);
const wan = computed(() => byRole('wan')[0]);
const bridge = computed(() => props.info.ifaces.find((i) => i.role === 'bridge'));
// Radios and the wired downstream port, in one row beneath the bridge.
const downstream = computed(() =>
  props.info.ifaces.filter((i) => i.role === 'ap' || i.role === 'radio' || i.role === 'lan'),
);

const W = 940;
const NODE_W = 190;
const NODE_H = 74;
const GAP = 26;

// Downstream nodes are centred as a row under the bridge, so the picture stays
// balanced whether there is one radio or three.
const rowWidth = computed(() => {
  const n = downstream.value.length;
  return n * NODE_W + Math.max(0, n - 1) * GAP;
});
const rowX = computed(() => (W - rowWidth.value) / 2);
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

const CX = W / 2;
const WAN_Y = 8;
const BR_Y = 132;
// The downstream row hangs off the BOTTOM of the bridge box, which is not a
// constant: it grows with the number of addresses on the bridge.
const DOWN_Y = computed(() => BR_Y + brH.value + 50);
// Room under the downstream row for two rows of per-radio action buttons.
const H = computed(() => DOWN_Y.value + NODE_H + 100);

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
        <text :x="nodeX(n) + NODE_W / 2" :y="DOWN_Y + 56" class="mono">{{ i.mac }}</text>

        <!-- Actions on the component they act on. A radio in this picture is
             the thing being cut or scanned, so the control belongs on it
             rather than in a list further down the page that has to name it
             again. foreignObject because these are real buttons: focusable,
             keyboard-reachable, and styled like every other button here. -->
        <foreignObject
          v-if="i.wireless && i.ap"
          :x="nodeX(n) - 20" :y="DOWN_Y + NODE_H + 6" :width="NODE_W + 40" height="62"
        >
          <!-- Two rows, grouped as the panel below is: what changes WHO IS
               CONNECTED on top, the rest under it. The picture is where you are
               already pointing at a radio, so the actions on that radio belong
               on it. -->
          <div class="node-actions">
            <button
              class="ghost"
              :class="{ accent: i.power_known && !i.powered }"
              :title="i.powered
                ? `Switch ${i.name} off and leave it off. Clients are told NOTHING and must time out.`
                : `${i.name} is switched off. Switch it back on.`"
              @click="emit('action', i.powered ? 'power-off' : 'power-on', i.name)"
            >{{ i.powered ? 'switch off' : 'switch on' }}</button>
            <button
              class="ghost" :disabled="!i.ap.stations"
              :title="`Deauthenticate all ${i.ap.stations} client(s) on ${i.name}. They are told, so they reconnect quickly.`"
              @click="emit('action', 'drop', i.name)"
            >drop {{ i.ap.stations }}</button>
          </div>
          <div class="node-actions">
            <button
              class="ghost" :disabled="!i.ap.stations"
              :title="`Disassociate all ${i.ap.stations} client(s) — the softer transition.`"
              @click="emit('action', 'nudge', i.name)"
            >nudge</button>
            <button
              v-if="otherRadio(i)"
              class="ghost" :disabled="!i.ap.stations"
              :title="`Ask all ${i.ap.stations} client(s) to move to ${otherRadio(i)} (802.11v). They may refuse.`"
              @click="emit('action', 'steer', i.name)"
            >steer</button>
            <button
              class="ghost"
              :title="`Scan ${i.name}'s band. A few beacon gaps, or an outage if this radio will not scan while serving.`"
              @click="emit('action', 'scan', i.name)"
            >scan</button>
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
