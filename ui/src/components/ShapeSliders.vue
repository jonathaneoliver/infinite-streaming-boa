<script setup lang="ts">
/**
 * Rate / delay / jitter / loss for one direction.
 *
 * Local state during a drag, debounced commit upstream. The slider reads from
 * `local` while the operator is moving it and from the model otherwise, so a
 * telemetry frame arriving mid-drag cannot snatch the handle away. `local`
 * clears when the model catches up.
 */
import { computed, inject, ref, watch } from 'vue';
import type { ComputedRef } from 'vue';
import type { Shape } from '@/types';
import { BURST_MAX, posToRate, rateToPos } from '@/types';

/*
 * Whether this kernel will take a Gilbert-Elliott loss model.
 *
 * Injected rather than threaded through as a prop: it is a property of the box,
 * identical for every card, and the two components between here and the
 * snapshot -- ClientCard and SubClasses -- have no use for it. Provided once in
 * App.vue.
 */
const burstCap = inject<ComputedRef<{ ok: boolean; note: string }>>(
  'lossBurst',
  computed(() => ({ ok: true, note: '' })),
);

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
      // An absent optional field reads as 0, not NaN: comparing against
      // undefined would make the difference NaN, which is never below the
      // tolerance, and the local override would stick for ever.
      const server = (s[k] as number | undefined) ?? 0;
      if (Math.abs((local.value[k] as number) - server) < 1e-6) delete local.value[k];
    }
  },
  { deep: true },
);

// Always a number. loss_burst is optional on the wire -- policy stored before
// bursts existed carries no such field -- and every reader here wants a value,
// not a maybe. Zero is the right absence for all of them: for burst it means
// uniform, which is what those stored policies were.
const v = (k: keyof Shape): number => local.value[k] ?? props.shape[k] ?? 0;

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

/*
 * Burstiness.
 *
 * 1 is uniform loss -- each packet independently -- which is what this control
 * did before it existed, and what essentially never happens on a real link. A
 * burst length above 1 hands the kernel a Gilbert-Elliott model whose mean loss
 * is still the figure on the loss slider.
 */
const burst = computed(() => Math.max(1, local.value.loss_burst ?? props.shape.loss_burst ?? 1));
const loss = computed(() => v('loss_pct'));

// A burst length with no loss configures nothing -- there are no bursts of
// nothing -- so the control says why it is inert rather than sitting there
// accepting input that does nothing.
const burstWhy = computed(() => {
  if (!burstCap.value.ok) return burstCap.value.note;
  if (loss.value <= 0) return 'No loss to make bursty — raise the loss slider first';
  return 'Mean length of a loss burst. 1 is uniform, independent per packet';
});
const burstOff = computed(() => !burstCap.value.ok || loss.value <= 0);
const burstText = computed(() => (burst.value <= 1 ? 'uniform' : `${burst.value} packets`));

/*
 * What the numbers actually mean, which neither slider can show on its own.
 *
 * Bursts start roughly every B/L packets, so a long burst at a low mean loss is
 * a RARE event -- and someone who sets 2% at 40 packets, watches for fifteen
 * seconds and sees nothing will conclude the feature is broken when it is
 * simply not due yet. The duration matters for the opposite reason: eight
 * packets is 5 ms at 12 Mbps and 100 ms at 0.5 Mbps, which are different events
 * as far as any player is concerned.
 */
const MTU_BYTES = 1500; // matches mtuBytes in shape.go

function fmtDur(sec: number): string {
  // Convert once, up front. Formatting seconds under a millisecond label is
  // exactly the factor-of-1000 mistake docs/DATA-CONTRACT.md exists to prevent,
  // and it reads as plausible: an 8 ms burst rendered "0.008 ms" looks like a
  // very fast link rather than a wrong number.
  const ms = sec * 1000;
  if (ms < 10) return `${ms.toFixed(1)} ms`;
  if (ms < 1000) return `${ms.toFixed(0)} ms`;
  return `${sec.toFixed(1)} s`;
}

const burstNote = computed(() => {
  if (burstOff.value || burst.value <= 1) return '';
  const gapPackets = Math.round(burst.value / (loss.value / 100));
  const rate = v('rate_mbps');
  if (rate <= 0) {
    // No rate, no packet clock: a duration here would be invented.
    return `≈ one burst every ${gapPackets.toLocaleString()} packets · timing depends on the rate`;
  }
  const pktsPerSec = (rate * 1e6) / 8 / MTU_BYTES;
  return `≈ ${fmtDur(burst.value / pktsPerSec)} of loss every ${fmtDur(gapPackets / pktsPerSec)} at ${rate} Mbps`;
});
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

    <!-- Burstiness. Disabled rather than hidden when it can do nothing: a
         control that vanishes leaves the operator wondering whether the box
         supports the idea at all, while one that explains itself answers the
         question on the spot. -->
    <div class="row">
      <label>burst</label>
      <input
        type="range" min="1" :max="BURST_MAX" step="1"
        :value="burst" :disabled="disabled || burstOff" :title="burstWhy"
        @input="set('loss_burst', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num" :class="{ inert: burstOff }">{{ burstText }}</span>
    </div>
    <p v-if="burstNote" class="meta burst-note">{{ burstNote }}</p>
    <p v-else-if="!burstCap.ok" class="meta burst-note warn">{{ burstCap.note }}</p>
  </div>
</template>

<style scoped>
.rows.disabled { opacity: 0.45; pointer-events: none; }
/* A control that cannot act must not read as one that is set to something. */
.val.inert { color: var(--ink-faint); }
.burst-note { margin: 2px 0 0; grid-column: 1 / -1; }
.burst-note.warn { color: var(--warn); }
</style>
