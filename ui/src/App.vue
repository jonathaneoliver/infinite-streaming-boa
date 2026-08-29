<script setup lang="ts">
import { computed } from 'vue';
import { useSnapshot } from '@/composables/useSnapshot';
import { useDevice } from '@/composables/useDevice';
import type { Client, Shape } from '@/types';
import { ntopngUrl } from '@/types';
import ClientCard from '@/components/ClientCard.vue';

const { snap, connected, transport, series } = useSnapshot();
const dev = useDevice();

const clients = computed(() => snap.value?.clients ?? []);
const caps = computed(() => snap.value?.caps);
const presentCount = computed(() => clients.value.filter((c) => c.present).length);

const notices = computed(() => snap.value?.notices ?? []);
const errorNotices = computed(() => notices.value.filter((n) => n.level === 'error'));
const infoNotices = computed(() => notices.value.filter((n) => n.level !== 'error'));

// Every write carries the revision the operator was looking at, so a
// simultaneous edit from another tab is refused rather than silently lost.
const rev = (c: Client) => c.policy.rev;
</script>

<template>
  <div class="wrap">
    <header class="top">
      <h1>pifi</h1>
      <span class="sub">per-client network link conditioner</span>
      <span class="spacer"></span>

      <span class="pill" :title="`transport: ${transport}`">
        <span class="dot" :class="connected ? 'live' : 'off'"></span>
        {{ connected ? (transport === 'sse' ? 'live' : 'polling') : 'disconnected' }}
      </span>
      <span v-if="caps" class="pill">
        bridge via {{ caps.uplink_if }}
      </span>
      <span class="pill">
        {{ presentCount }} connected
      </span>
      <a
        v-if="caps?.ntopng"
        class="pill link"
        :href="ntopngUrl(caps.ntopng_port, '/lua/if_stats.lua')"
        target="_blank" rel="noopener"
        title="Traffic analysis for the whole bridge, in ntopng"
      >ntopng ↗</a>
    </header>

    <!-- Actionable messages stay at the top: an error the reader has to
         scroll past every device to find is worse than clutter. -->
    <div v-if="dev.conflict.value" class="notice bad">
      {{ dev.conflict.value }}
    </div>
    <div v-for="n in errorNotices" :key="n.text" class="notice bad">
      {{ n.text }}
    </div>

    <ClientCard
      v-for="c in clients" :key="c.mac"
      :client="c" :series="series[c.mac]"
      :ntopng-port="caps?.ntopng ? caps.ntopng_port : 0"
      @shape="(dir, s) => dev.patchShape(c.mac, rev(c), dir, s)"
      @preset="(down: Shape, up: Shape) => dev.patchPolicy(c.mac, rev(c), { down, up })"
      @label="(l: string) => dev.patchPolicy(c.mac, rev(c), { label: l })"
      @reset="dev.reset(c.mac)"
      @add-sub="dev.addSub(c.mac, rev(c), 'new rule', {})"
      @remove-sub="(id: string) => dev.deleteSub(c.mac, id)"
      @patch-sub="(id: string, p: Record<string, unknown>) => dev.patchSub(c.mac, id, rev(c), p)"
      @sub-shape="(id: string, dir: 'down' | 'up', s: Shape) => dev.patchSub(c.mac, id, rev(c), { [dir]: s })"
    />

    <div v-if="!clients.length" class="empty">
      <p>No devices seen yet.</p>
      <p class="meta">
        Join the Wi-Fi network or plug a device into the USB ethernet port.
        Devices appear here as soon as they send their first packet.
      </p>
    </div>

    <!-- Standing truths about how the box behaves. They never change and never
         need acting on, so they read as footnotes rather than pushing the
         devices -- the actual content -- below the fold. -->
    <footer v-if="infoNotices.length" class="notes">
      <div v-for="n in infoNotices" :key="n.text" class="notice info">
        {{ n.text }}
      </div>
    </footer>
  </div>
</template>
