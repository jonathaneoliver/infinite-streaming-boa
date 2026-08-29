<script setup lang="ts">
import { computed, ref } from 'vue';
import type { Client, Shape, Series } from '@/types';
import { PRESETS, ntopngUrl } from '@/types';
import ShapeSliders from './ShapeSliders.vue';
import SubClasses from './SubClasses.vue';
import TrafficChart from './TrafficChart.vue';

const props = defineProps<{
  client: Client;
  series?: Series;
  ntopngPort?: number;
  collapsed?: boolean;
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
}>();

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

const conditioned = computed(() => {
  const p = props.client.policy;
  const dirty = (s: Shape) =>
    s.rate_mbps > 0 || s.delay_ms > 0 || s.jitter_ms > 0 || s.loss_pct > 0;
  return p.enabled && (dirty(p.down) || dirty(p.up));
});

/**
 * A folded row opens on a click anywhere in it, not only on the chevron.
 *
 * Clicks that land on something interactive are left alone: the name is an
 * editable input, and a device that is absent carries a "forget" button. Both
 * live in the folded row, and swallowing their clicks to expand the card would
 * make them unusable.
 *
 * Only the FOLDED state responds. Making an expanded header collapse on a bare
 * click would fire while selecting a MAC address to copy, which is a normal
 * thing to do there. The chevron still toggles both ways, and remains the
 * keyboard-accessible control.
 */
function onHeadClick(e: MouseEvent) {
  if (!props.collapsed) return;
  const el = e.target as HTMLElement | null;
  if (el?.closest('input, button, a, select, textarea')) return;
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
    <div class="card-head" :class="{ folded: collapsed }" @click="onHeadClick">
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
      <span v-if="!collapsed" class="meta num">{{ client.mac }}</span>
      <span v-if="client.ip" class="meta num">{{ client.ip }}</span>
      <!-- Privacy extensions give a device several v6 addresses at once, so
           the count matters more than any one value; all of them are shaped. -->
      <span
        v-if="!collapsed && client.ipv6?.length" class="meta num"
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
      <span v-if="!collapsed && client.station?.signal_dbm" class="meta num" :class="signalClass">
        {{ client.station.signal_dbm }} dBm
      </span>
      <span
        v-else-if="!collapsed && client.station" class="meta num"
        title="This radio reports no per-station signal level in AP mode; transmit failures stand in as the link-quality indicator"
      >tx-fail {{ client.station.tx_failed.toLocaleString() }}</span>
      <span
        v-if="!collapsed && client.station"
        class="meta num"
        title="Negotiated radio modulation rate, NOT achieved throughput"
      >
        PHY {{ client.station.tx_phy_mbps.toFixed(0) }}
      </span>

      <span class="spacer"></span>

      <!-- Folded summary: enough to answer "is this device doing anything, and
           is it being conditioned" without expanding. Uses the same chart
           component in compact mode, so direction colour and scaling cannot
           drift from the full charts below. -->
      <span v-if="collapsed" class="fold-summary">
        <span class="fold-spark">
          <TrafficChart
            :data="series?.down ?? []" color="var(--down)" label="Downlink"
            :cap="client.policy.down.rate_mbps" :height="24" compact
          />
        </span>
        <span class="fold-val num" style="color: var(--down)">
          &darr;{{ client.down_counters.throughput_mbps.toFixed(2) }}
        </span>
        <span class="fold-spark">
          <TrafficChart
            :data="series?.up ?? []" color="var(--up)" label="Uplink"
            :cap="client.policy.up.rate_mbps" :height="24" compact
          />
        </span>
        <span class="fold-val num" style="color: var(--up)">
          &uarr;{{ client.up_counters.throughput_mbps.toFixed(2) }}
        </span>
        <span class="meta">Mbps</span>
      </span>

      <!-- Deep links into ntopng for THIS device. Shown only when ntopng is
           answering and the client has an address, since both are required
           for the filtered view to resolve to anything. -->
      <template v-if="!collapsed && ntopngPort && client.ip">
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
      <button v-if="conditioned && !collapsed" @click="emit('reset')">reset</button>
      <!-- Only offered for a device that is not here: forgetting one that is
           present just makes it reappear unconfigured a second later. -->
      <button v-if="!client.present" class="ghost" @click="emit('forget')">
        forget
      </button>
      <button
        class="fold-toggle ghost" @click="emit('toggle')"
        :title="collapsed ? 'Expand this device' : 'Fold this device away'"
        :aria-expanded="!collapsed"
      >{{ collapsed ? '▸' : '▾' }}</button>
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
          <span class="readout num">
            {{ client.down_counters.throughput_mbps.toFixed(2) }}
            <small>Mbps</small>
          </span>
        </h3>
        <TrafficChart
          :data="series?.down ?? []" color="var(--down)" label="Downlink"
          :cap="client.policy.down.rate_mbps" :height="196"
        />
        <ShapeSliders
          :shape="client.policy.down" dir="down" :disabled="!client.shapeable"
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
          :data="series?.up ?? []" color="var(--up)" label="Uplink"
          :cap="client.policy.up.rate_mbps" :height="196"
        />
        <ShapeSliders
          :shape="client.policy.up" dir="up" :disabled="!client.shapeable"
          @update="(s) => emit('shape', 'up', s)"
        />
      </div>
    </div>

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
.fold-summary { display: flex; align-items: center; gap: 6px; flex: none; }
/* A folded row must not wrap. A ragged two-line list is harder to scan than a
   dense one-line one, which is the entire reason for folding. */
.card-head.folded { flex-wrap: nowrap; overflow: hidden; cursor: pointer; }
.card-head.folded:hover { background: var(--panel); }
/* The name stays a text field, so it must not inherit the row's pointer. */
.card-head.folded .name { cursor: text; }
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
