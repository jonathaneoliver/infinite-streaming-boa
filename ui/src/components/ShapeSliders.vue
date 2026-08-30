<script setup lang="ts">
/**
 * Rate / delay / jitter / loss for one direction.
 *
 * Local state during a drag, debounced commit upstream. The slider reads from
 * `local` while the operator is moving it and from the model otherwise, so a
 * telemetry frame arriving mid-drag cannot snatch the handle away. `local`
 * clears when the model catches up.
 */
import { computed, ref, watch } from 'vue';
import type { Shape } from '@/types';
import { posToRate, rateToPos } from '@/types';

const props = defineProps<{
  shape: Shape;
  dir: 'down' | 'up';
  disabled?: boolean;
}>();
const emit = defineEmits<{ update: [Shape] }>();

const local = ref<Partial<Shape>>({});

// Once the server's value matches what we sent, stop overriding it locally.
watch(
  () => props.shape,
  (s) => {
    for (const k of Object.keys(local.value) as (keyof Shape)[]) {
      if (Math.abs((local.value[k] as number) - s[k]) < 1e-6) delete local.value[k];
    }
  },
  { deep: true },
);

const v = (k: keyof Shape) => local.value[k] ?? props.shape[k];

function set(k: keyof Shape, value: number) {
  local.value = { ...local.value, [k]: value };
  emit('update', { ...props.shape, ...local.value, [k]: value });
}

// Rate uses an exponential slider; the scale itself lives in types.ts, shared
// with the pattern timeline so a ramp authored as a straight line on this
// control draws as a straight line there.
const ratePos = computed(() => rateToPos(v('rate_mbps')));
const rateText = computed(() =>
  v('rate_mbps') === 0 ? 'unlimited' : `${v('rate_mbps')} Mbps`,
);

// netem subtracts jitter from delay per packet, so jitter above delay asks for
// negative latency. The kernel clamps it silently and the resulting
// distribution is not what was configured, so the control is capped instead.
const jitterMax = computed(() => Math.max(1, v('delay_ms')));
</script>

<template>
  <div :class="['rows', { disabled }]">
    <div class="row">
      <label>rate</label>
      <input
        type="range" min="0" max="100" step="1"
        :value="ratePos" :disabled="disabled"
        @input="set('rate_mbps', posToRate(+($event.target as HTMLInputElement).value))"
      />
      <span class="val num">{{ rateText }}</span>
    </div>

    <div class="row">
      <label>delay</label>
      <input
        type="range" min="0" max="1000" step="1"
        :value="v('delay_ms')" :disabled="disabled"
        @input="set('delay_ms', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">{{ v('delay_ms') }} ms</span>
    </div>

    <div class="row">
      <label>jitter</label>
      <input
        type="range" min="0" :max="jitterMax" step="1"
        :value="Math.min(v('jitter_ms'), jitterMax)" :disabled="disabled"
        @input="set('jitter_ms', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">± {{ Math.min(v('jitter_ms'), jitterMax) }} ms</span>
    </div>

    <div class="row">
      <label>loss</label>
      <input
        type="range" min="0" max="20" step="0.1"
        :value="v('loss_pct')" :disabled="disabled"
        @input="set('loss_pct', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">{{ v('loss_pct').toFixed(1) }} %</span>
    </div>
  </div>
</template>

<style scoped>
.rows.disabled { opacity: 0.45; pointer-events: none; }
</style>
