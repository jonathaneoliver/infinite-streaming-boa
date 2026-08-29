<script setup lang="ts">
import { computed, ref } from 'vue';
import type { Client, Shape, Series } from '@/types';
import { PRESETS, ntopngUrl } from '@/types';
import ShapeSliders from './ShapeSliders.vue';
import SubClasses from './SubClasses.vue';
import TrafficChart from './TrafficChart.vue';

const props = defineProps<{ client: Client; series?: Series; ntopngPort?: number }>();
const emit = defineEmits<{
  shape: [dir: 'down' | 'up', shape: Shape];
  preset: [down: Shape, up: Shape];
  label: [string];
  reset: [];
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
    <div class="card-head">
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
      <span v-else class="meta" title="Associated, but has not taken an address yet">
        no address yet
      </span>

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
    </div>

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
          :cap="client.policy.down.rate_mbps"
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
          :cap="client.policy.up.rate_mbps"
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
  </div>
</template>

<style scoped>
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
