<script setup lang="ts">
/**
 * Sub-classes: rules that condition part of a device's traffic differently.
 *
 * A sub-class is written in terms of the SERVICE being talked to -- a port, a
 * network. The server flips that around on the download path, where the same
 * service is the packet's source rather than its destination, so the operator
 * never has to think about direction here.
 */
import type { Client, Counters, Shape } from '@/types';
import ShapeSliders from './ShapeSliders.vue';

defineProps<{ client: Client; subCounters?: Record<string, Counters> }>();
const emit = defineEmits<{
  add: [];
  remove: [string];
  patch: [string, Record<string, unknown>];
  shape: [string, 'down' | 'up', Shape];
}>();
</script>

<template>
  <div class="subs">
    <div class="subs-head">
      <strong>Sub-classes</strong>
      <span class="meta">
        condition part of this device's traffic differently
      </span>
      <span class="spacer"></span>
      <button @click="emit('add')">+ add rule</button>
    </div>

    <p v-if="!client.policy.sub?.length" class="meta none">
      No rules. The device default above applies to everything it sends.
    </p>

    <div v-for="s in client.policy.sub ?? []" :key="s.id" class="sub">
      <div class="sub-head">
        <input
          type="checkbox" :checked="s.enabled"
          @change="emit('patch', s.id, { enabled: ($event.target as HTMLInputElement).checked })"
        />
        <input
          type="text" :value="s.name" class="sub-name"
          @change="emit('patch', s.id, { name: ($event.target as HTMLInputElement).value })"
        />
        <span class="meta">matches</span>
        <select
          :value="s.match.protocol ?? ''"
          @change="emit('patch', s.id, { match: { ...s.match, protocol: ($event.target as HTMLSelectElement).value } })"
        >
          <option value="">any protocol</option>
          <option value="tcp">TCP</option>
          <option value="udp">UDP</option>
        </select>
        <input
          type="number" placeholder="port" min="0" max="65535" style="width: 76px"
          :value="s.match.dst_port || ''"
          @change="emit('patch', s.id, { match: { ...s.match, dst_port: +($event.target as HTMLInputElement).value } })"
        />
        <input
          type="text" placeholder="network e.g. 23.32.0.0/16" style="width: 168px"
          :value="s.match.dst_cidr ?? ''"
          @change="emit('patch', s.id, { match: { ...s.match, dst_cidr: ($event.target as HTMLInputElement).value } })"
        />
        <span class="spacer"></span>
        <span v-if="subCounters?.[s.id]" class="num meta">
          {{ subCounters[s.id].throughput_mbps.toFixed(2) }} Mbps
        </span>
        <button class="ghost" @click="emit('remove', s.id)">remove</button>
      </div>

      <div class="sub-body">
        <div class="dir down">
          <h3>Down</h3>
          <ShapeSliders
            :shape="s.down" dir="down" :disabled="!s.enabled"
            @update="(sh) => emit('shape', s.id, 'down', sh)"
          />
        </div>
        <div class="dir up">
          <h3>Up</h3>
          <ShapeSliders
            :shape="s.up" dir="up" :disabled="!s.enabled"
            @update="(sh) => emit('shape', s.id, 'up', sh)"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.subs { padding: 12px 14px; border-top: 1px solid var(--line); }
.subs-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; flex-wrap: wrap; }
.none { margin: 4px 0 0; }
.sub { border: 1px solid var(--line); border-radius: 6px; margin-top: 8px; overflow: hidden; }
.sub-head {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 8px 10px; background: var(--panel-2); border-bottom: 1px solid var(--line);
}
.sub-name { width: 150px; font-weight: 600; }
.sub-body { display: grid; grid-template-columns: 1fr 1fr; gap: 1px; background: var(--line-soft); }
.sub-body .dir { padding: 10px 12px; }
@media (max-width: 860px) { .sub-body { grid-template-columns: 1fr; } }
</style>
