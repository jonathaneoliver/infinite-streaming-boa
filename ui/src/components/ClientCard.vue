<script setup lang="ts">
import { computed, ref } from 'vue';
import type { Client, Shape, Series, ChartPrefs } from '@/types';
import { PRESETS, SUSTAINED_SEC, ntopngUrl } from '@/types';
import ShapeSliders from './ShapeSliders.vue';
import SubClasses from './SubClasses.vue';
import LadderPanel from './LadderPanel.vue';
import TrafficChart from './TrafficChart.vue';

const props = defineProps<{
  client: Client;
  series?: Series;
  ntopngPort?: number;
  collapsed?: boolean;
  /** Chart settings, shared by every card so devices stay comparable. */
  chart: ChartPrefs;
  /** Right-hand edge of every plot, held still while a chart is hovered. */
  now: number;
}>();
const emit = defineEmits<{
  shape: [dir: 'down' | 'up', shape: Shape];
  preset: [down: Shape, up: Shape];
  label: [string];
  reset: [];
  forget: [];
  toggle: [];
  addSub: [];
  removeSub: [string];
  patchSub: [string, Record<string, unknown>];
  subShape: [string, 'down' | 'up', Shape];
  hovering: [boolean];
  sweep: [service: string];
  stopSweep: [];
  removeLadder: [service: string];
}>();

// The chart props every plot on this card shares. Spread rather than repeated
// four times, so the sparkline on a folded row can never drift out of step with
// the full chart it summarises.
const chartProps = computed(() => ({
  windowMs: props.chart.rangeSec * 1000,
  now: props.now,
  yMode: props.chart.yMode,
  yManual: props.chart.yManual,
  showLive: props.chart.showLive,
  showSustained: props.chart.showSustained,
  sustainedSec: SUSTAINED_SEC,
}));

const open = ref(false);

// Signal quality bands are the conventional Wi-Fi ones: above -60 dBm is a
// strong link, below -75 is where retries and rate-drops begin to dominate and
// the radio's own behaviour will start to confound whatever is being tested.
const signalClass = computed(() => {
  const s = props.client.station?.signal_dbm ?? 0;
  if (!s) return '';
  if (s > -60) return 'ok';
  if (s > -75) return 'warn';
  return 'bad';
});

/**
 * The downlink cap actually in force, which is NOT always the stored policy.
 *
 * A ladder sweep drives the cap itself and deliberately never writes to stored
 * policy, so that an abandoned or crashed run restores the operator's settings
 * by simply being forgotten. The cost is that the policy the UI usually draws
 * from says "unlimited" while the kernel is throttling hard.
 *
 * Drawing the policy in that state makes the interface claim a cap is not
 * applied when it is -- the inverse of the failure the PRD names, and just as
 * dishonest. The charts follow what is enforced.
 */
const sweeping = computed(() => props.client.sweep?.state === 'running');
const downCap = computed(() =>
  sweeping.value ? (props.client.sweep?.cap_mbps ?? 0) : props.client.policy.down.rate_mbps,
);

/**
 * What the downlink controls display.
 *
 * During a sweep this is the ENFORCED shape, not the stored one, so the rate
 * control tracks the cap down the ladder as each level is applied. The sweep's
 * override is clean apart from the cap -- it suspends delay, jitter and loss for
 * the duration -- so those read zero, because that is what the kernel has.
 *
 * The controls are read-only while this is happening: they are reporting, not
 * accepting input, and the stored policy underneath is untouched.
 */
const downShape = computed<Shape>(() =>
  sweeping.value
    ? { rate_mbps: downCap.value, delay_ms: 0, jitter_ms: 0, loss_pct: 0 }
    : props.client.policy.down,
);

const conditioned = computed(() => {
  const p = props.client.policy;
  const dirty = (s: Shape) =>
    s.rate_mbps > 0 || s.delay_ms > 0 || s.jitter_ms > 0 || s.loss_pct > 0;
  return p.enabled && (dirty(p.down) || dirty(p.up));
});

/**
 * The card title toggles the fold, in both directions.
 *
 * Two things are deliberately excluded:
 *
 * Clicks on something interactive. The name is an editable input and an absent
 * device carries a "forget" button, both of which sit in the title; swallowing
 * their clicks would make them unusable.
 *
 * Clicks that conclude a text selection. Dragging across a MAC address to copy
 * it ends in a click on the header, and folding the card away underneath that
 * would be infuriating. Checking the selection is emptier than disabling the
 * interaction, which was the first attempt.
 */
function onHeadClick(e: MouseEvent) {
  const el = e.target as HTMLElement | null;
  if (el?.closest('input, button, a, select, textarea')) return;
  if ((window.getSelection()?.toString() ?? '').length > 0) return;
  emit('toggle');
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${u[i]}`;
}
</script>

<template>
  <div :class="['card', { absent: !client.present }]">
    <!-- The folded row is its own markup rather than the full header with
         pieces hidden. A grid only lines up if every row has the SAME cells,
         and the optional fields -- medium, address, conditioned -- would drop
         out and shift everything after them. Each slot is always rendered here
         and simply left empty when it does not apply. -->
    <div v-if="collapsed" class="card-head folded" @click="onHeadClick">
      <button
        class="fold-toggle ghost" @click="emit('toggle')"
        title="Expand this device" :aria-expanded="false"
      >&#9656;</button>

      <span
        class="dot" :class="client.present ? 'live' : 'off'"
        :title="client.present ? 'Connected now' : 'Not currently connected'"
      ></span>

      <input
        class="name" :value="client.label"
        @change="emit('label', ($event.target as HTMLInputElement).value)"
      />

      <span class="cell">
        <span v-if="client.medium" class="badge" :class="client.medium">
          {{ client.medium }}
        </span>
      </span>

      <span class="cell meta num addr">
        {{ client.ip || (client.ipv6?.length ? client.ipv6[0] : 'no address yet') }}
      </span>

      <span class="cell spark">
        <TrafficChart
          v-bind="chartProps"
          :t="series?.t ?? []" :data="series?.down ?? []"
          color="var(--down)" label="Downlink"
          :cap="downCap" :height="24" compact
        />
      </span>
      <span class="cell num val" style="color: var(--down)">
        &darr;{{ client.down_counters.throughput_mbps.toFixed(2) }}
      </span>

      <span class="cell spark">
        <TrafficChart
          v-bind="chartProps"
          :t="series?.t ?? []" :data="series?.up ?? []"
          color="var(--up)" label="Uplink"
          :cap="client.policy.up.rate_mbps" :height="24" compact
        />
      </span>
      <span class="cell num val" style="color: var(--up)">
        &uarr;{{ client.up_counters.throughput_mbps.toFixed(2) }}
      </span>

      <span class="cell meta unit">Mbps</span>

      <span class="cell">
        <span v-if="conditioned" class="badge" style="color: var(--warn)">
          conditioned
        </span>
      </span>

      <span class="cell">
        <button v-if="!client.present" class="ghost" @click="emit('forget')">
          forget
        </button>
      </span>

    </div>

    <div v-else class="card-head" @click="onHeadClick">
      <button
        class="fold-toggle ghost" @click="emit('toggle')"
        title="Fold this device away" :aria-expanded="true"
      >&#9662;</button>

      <span
        class="dot"
        :class="client.present ? 'live' : 'off'"
        :title="client.present ? 'Connected now' : 'Not currently connected'"
      ></span>

      <input
        class="name" :value="client.label"
        @change="emit('label', ($event.target as HTMLInputElement).value)"
      />

      <span v-if="client.medium" class="badge" :class="client.medium">
        {{ client.medium }}
      </span>
      <span class="meta num">{{ client.mac }}</span>
      <span v-if="client.ip" class="meta num">{{ client.ip }}</span>
      <!-- Privacy extensions give a device several v6 addresses at once, so
           the count matters more than any one value; all of them are shaped. -->
      <span
        v-if="client.ipv6?.length" class="meta num"
        :title="'IPv6, all conditioned:\n' + client.ipv6.join('\n')"
      >+{{ client.ipv6.length }} IPv6</span>
      <!-- An explicit condition, not a v-else. Inserting the IPv6 badge above
           re-paired the v-else with the IPv6 test, so a client with an IPv4
           address but no v6 rendered "192.168.0.214  no address yet". -->
      <span
        v-if="!client.ip && !client.ipv6?.length" class="meta"
        title="Associated, but has not taken an address yet"
      >no address yet</span>

      <!-- Only shown when the driver actually reports it. The Pi's Broadcom
           chip gives no per-station RSSI in AP mode, and printing "0 dBm" is
           a confident-looking lie. -->
      <span v-if="client.station?.signal_dbm" class="meta num" :class="signalClass">
        {{ client.station.signal_dbm }} dBm
      </span>
      <span
        v-else-if="client.station" class="meta num"
        title="This radio reports no per-station signal level in AP mode; transmit failures stand in as the link-quality indicator"
      >tx-fail {{ client.station.tx_failed.toLocaleString() }}</span>
      <span
        v-if="client.station"
        class="meta num"
        title="Negotiated radio modulation rate, NOT achieved throughput"
      >
        PHY {{ client.station.tx_phy_mbps.toFixed(0) }}
      </span>

      <span class="spacer"></span>

      <!-- Deep links into ntopng for THIS device. Shown only when ntopng is
           answering and the client has an address, since both are required
           for the filtered view to resolve to anything. -->
      <template v-if="ntopngPort && client.ip">
        <a
          class="ntop-link"
          :href="ntopngUrl(ntopngPort, '/lua/host_details.lua', { host: client.ip })"
          target="_blank" rel="noopener"
          :title="`Traffic breakdown for ${client.ip} in ntopng`"
        >traffic ↗</a>
        <a
          class="ntop-link"
          :href="ntopngUrl(ntopngPort, '/lua/flows_stats.lua', { host: client.ip })"
          target="_blank" rel="noopener"
          :title="`Live flows for ${client.ip} in ntopng, labelled by application`"
        >flows ↗</a>
      </template>

      <span v-if="conditioned" class="badge" style="color: var(--warn)">
        conditioned
      </span>
      <button v-if="conditioned" @click="emit('reset')">reset</button>
      <!-- Only offered for a device that is not here: forgetting one that is
           present just makes it reappear unconfigured a second later. -->
      <button v-if="!client.present" class="ghost" @click="emit('forget')">
        forget
      </button>
    </div>

    <!-- Folding is presentation only: the card keeps receiving live updates,
         so the summary above stays current and expanding shows no gap. -->
    <template v-if="!collapsed">
    <div v-if="!client.shapeable && client.present" class="notice bad" style="margin: 10px 14px">
      This device has no IP address yet, so it cannot be conditioned. Traffic
      filters match on addresses, and there is nothing to match on until it
      finishes joining the network.
    </div>

    <div class="presets" style="padding: 10px 14px 0">
      <button
        v-for="p in PRESETS" :key="p.name"
        :title="p.note"
        @click="emit('preset', { ...p.down }, { ...p.up })"
      >
        {{ p.name }}
      </button>
    </div>

    <div class="dirs" style="margin-top: 10px">
      <div class="dir down">
        <h3>
          Downlink <span class="meta">to device</span>
          <span v-if="sweeping" class="badge" style="color: var(--warn)">
            swept &middot; {{ downCap.toFixed(2) }} Mbps
          </span>
          <span class="readout num">
            {{ client.down_counters.throughput_mbps.toFixed(2) }}
            <small>Mbps</small>
          </span>
        </h3>
        <TrafficChart
          v-bind="chartProps"
          :t="series?.t ?? []" :data="series?.down ?? []"
          color="var(--down)" label="Downlink"
          :cap="downCap" :height="196"
          @hovering="(v: boolean) => emit('hovering', v)"
        />
        <p v-if="sweeping" class="meta swept-note">
          These controls are showing what the sweep is enforcing, not your saved
          settings — those are untouched and return when it ends.
        </p>
        <ShapeSliders
          :shape="downShape" dir="down"
          :disabled="!client.shapeable || sweeping"
          @update="(s) => emit('shape', 'down', s)"
        />
      </div>

      <div class="dir up">
        <h3>
          Uplink <span class="meta">from device</span>
          <span class="readout num">
            {{ client.up_counters.throughput_mbps.toFixed(2) }}
            <small>Mbps</small>
          </span>
        </h3>
        <TrafficChart
          v-bind="chartProps"
          :t="series?.t ?? []" :data="series?.up ?? []"
          color="var(--up)" label="Uplink"
          :cap="client.policy.up.rate_mbps" :height="196"
          @hovering="(v: boolean) => emit('hovering', v)"
        />
        <ShapeSliders
          :shape="client.policy.up" dir="up" :disabled="!client.shapeable"
          @update="(s) => emit('shape', 'up', s)"
        />
      </div>
    </div>

    <!-- Ladders sit above the fold, not behind the counters toggle: a sweep is
         a deliberate action someone came to the page to start, and one that
         takes minutes needs its progress visible without hunting for it. -->
    <LadderPanel
      :client="client"
      @sweep="(svc: string) => emit('sweep', svc)"
      @stop-sweep="emit('stopSweep')"
      @remove-ladder="(svc: string) => emit('removeLadder', svc)"
    />

    <div class="card-foot">
      <!-- Round-trip is computed here rather than left to the reader: delay is
           configured per direction, and forgetting that it applies twice is the
           single most misread control in a bidirectional conditioner. -->
      <span class="rtt">
        added round-trip
        <b class="num">{{ client.rtt_added_ms.toFixed(0) }} ms</b>
        <span class="meta"> ({{ client.policy.down.delay_ms }} down + {{ client.policy.up.delay_ms }} up)</span>
      </span>
      <span class="spacer"></span>
      <button class="ghost" @click="open = !open">
        {{ open ? 'hide' : 'show' }} counters &amp; rules
      </button>
    </div>

    <div v-if="open">
      <div class="scroll-x" style="padding: 12px 14px">
        <table class="counters">
          <thead>
            <tr>
              <th>direction</th>
              <th>throughput</th>
              <th>cap enforced</th>
              <th>bytes</th>
              <th>packets</th>
              <th>drops</th>
              <th title="Times the class hit its ceiling. Expected on a throttled link — not a fault.">
                overlimits
              </th>
              <th title="Bytes queued right now: the honest congestion signal.">backlog</th>
              <th>qlen</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in [
              { k: 'down', label: 'downlink', v: client.down_counters },
              { k: 'up', label: 'uplink', v: client.up_counters },
            ]" :key="c.k">
              <td :style="{ color: c.k === 'down' ? 'var(--down)' : 'var(--up)' }">
                {{ c.label }}
              </td>
              <td class="num">{{ c.v.throughput_mbps.toFixed(2) }} Mbps</td>
              <td class="num">
                {{ c.v.cap_mbps > 5000 ? 'unlimited' : c.v.cap_mbps.toFixed(1) + ' Mbps' }}
              </td>
              <td class="num">{{ fmtBytes(c.v.bytes) }}</td>
              <td class="num">{{ c.v.packets.toLocaleString() }}</td>
              <td class="num" :style="c.v.drops ? 'color:var(--bad)' : ''">
                {{ c.v.drops.toLocaleString() }}
              </td>
              <td class="num meta">{{ c.v.overlimits.toLocaleString() }}</td>
              <td class="num">{{ c.v.backlog.toLocaleString() }}</td>
              <td class="num">{{ c.v.qlen.toLocaleString() }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <SubClasses
        :client="client" :sub-counters="client.sub_counters"
        @add="emit('addSub')"
        @remove="(id) => emit('removeSub', id)"
        @patch="(id, p) => emit('patchSub', id, p)"
        @shape="(id, dir, s) => emit('subShape', id, dir, s)"
      />
    </div>
    </template>
  </div>
</template>

<style scoped>
/* A control that is reporting rather than accepting input must not look
   editable, but it must still be readable -- the whole point is watching it
   move. Dimmed, not hidden. */
.swept-note {
  color: var(--warn);
  margin: 6px 0 0;
}

.fold-summary { display: flex; align-items: center; gap: 6px; flex: none; }
/* A folded row must not wrap. A ragged two-line list is harder to scan than a
   dense one-line one, which is the entire reason for folding. */
.card-head { cursor: pointer; }
/* The name stays a text field, so it must not inherit the row's pointer. */
.card-head .name { cursor: text; }

/* Fixed columns so every folded row lines up. Flex sized each row to its own
   content, which made a list of devices impossible to scan down. */
.card-head.folded {
  display: grid;
  grid-template-columns:
    30px                  /* fold toggle -- first, so it is in the same place
                             on every row and does not shift with content */
    14px                  /* presence */
    minmax(96px, 1.3fr)   /* name */
    58px                  /* medium */
    minmax(104px, 1fr)    /* address */
    76px 68px             /* downlink: shape, then figure -- 68px fits a
                             three-digit rate such as 103.66 without touching
                             the next column */
    76px 68px             /* uplink */
    38px                  /* unit */
    92px                  /* conditioned */
    56px;                 /* actions */
  align-items: center;
  gap: 8px;
  overflow: hidden;
}
.card-head.folded:hover { background: var(--panel); }
.card-head.folded .cell { min-width: 0; overflow: hidden; }
.card-head.folded .addr,
.card-head.folded .name { text-overflow: ellipsis; white-space: nowrap; }
.card-head.folded .val { text-align: right; font-size: 12px; font-weight: 600; }
.card-head.folded .unit { font-size: 11px; }
.card-head.folded .spark { display: block; }
.card-head.folded .name { flex: 0 1 auto; min-width: 90px; }
.fold-spark { width: 84px; display: block; }
.fold-val { font-size: 12px; font-weight: 600; }
.fold-toggle { font-size: 13px; line-height: 1; padding: 3px 8px; }
.ntop-link {
  font-size: 11px;
  color: var(--ink-dim);
  text-decoration: none;
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 2px 7px;
}
.ntop-link:hover { color: var(--ink); border-color: var(--ink-faint); }
.ok { color: var(--ok); }
.warn { color: var(--warn); }
.bad { color: var(--bad); }
</style>
