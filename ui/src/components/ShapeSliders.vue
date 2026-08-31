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
import { EXTRA_IMPAIRMENTS, hasExtras, posToRate, rateToPos } from '@/types';
import { useExtras } from '@/composables/useExtras';

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
  const next: Partial<Shape> = { [k]: value };
  // Lowering delay past the current jitter would send jitter > delay, which the
  // daemon refuses -- so delay could not be reduced without first reducing
  // jitter, and the slider simply stopped moving with the reason buried in a
  // conflict message. Jitter follows delay down, because jitter is a width
  // around delay and a width cannot outlive the thing it is a width of.
  if (k === 'delay_ms' && v('jitter_ms') > value) next.jitter_ms = value;
  local.value = { ...local.value, ...next };
  emit('update', { ...props.shape, ...local.value, ...next });
}

// Rate uses an exponential slider; the scale itself lives in types.ts, shared
// with the pattern timeline so a ramp authored as a straight line on this
// control draws as a straight line there.
const ratePos = computed(() => rateToPos(v('rate_mbps')));
const rateText = computed(() =>
  v('rate_mbps') === 0 ? 'unlimited' : `${v('rate_mbps')} Mbps`,
);

/*
 * Jitter is a WIDTH around delay, not a value of its own.
 *
 * netem draws each packet's latency uniformly from [delay - jitter,
 * delay + jitter], so jitter above delay asks for negative latency; the kernel
 * clamps that silently and the resulting distribution is not what was
 * configured. The daemon refuses it, and the control is capped to match.
 *
 * The cap used to be max(1, delay), which made the zero case worse rather than
 * better: with no delay the slider still offered a 0-1 ms range, every value in
 * it except 0 was refused by the daemon, and because the local override only
 * clears when the server agrees, the control wedged displaying "+/- 1 ms"
 * forever -- a number that was neither stored nor enforced.
 */
const jitterMax = computed(() => v('delay_ms'));
const jitterOff = computed(() => v('delay_ms') === 0);
const jitterVal = computed(() => Math.min(v('jitter_ms'), jitterMax.value));

/**
 * Just the width, in every state.
 *
 * Two wordier versions came and went. The resulting span -- "+/- 30 ms
 * (70-130)" -- is arithmetic the reader can do and mostly does not want, and
 * "needs a delay" spent a whole readout explaining a control that is already
 * visibly disabled. A greyed "+/- 0 ms" says the same thing in the same shape
 * as every other row, so the eye can still scan the column. The reason stays on
 * hover, where it costs nothing.
 */
const jitterText = computed(() => `± ${jitterVal.value} ms`);

/**
 * The second tier opens itself.
 *
 * Held open by an explicit click, or by anything inside being in force. The
 * second half is the important half: a section that closes over a live
 * impairment would be conditioning the traffic with nothing on screen saying
 * so, which is the whole objection to hiding controls behind a preference.
 *
 * So the collapsed header does not say "3 more" -- it names what is set. Empty
 * is the only state in which this is out of sight.
 */
const { show: opened, toggle: toggleExtras } = useExtras();
const extrasActive = computed(() => hasExtras({ ...props.shape, ...local.value } as Shape));
const extrasOpen = computed(() => opened.value || extrasActive.value);

const extrasSummary = computed(() =>
  EXTRA_IMPAIRMENTS.filter((e) => v(e.key) > 0)
    .map((e) => `${e.label} ${v(e.key)}${e.unit}`)
    .join(' · '),
);

// netem rejects `reorder` outright when there is no delay to reorder against,
// and a rejected command installs no qdisc at all -- so this would not merely
// fail to reorder, it would drop the device's rate and loss with it. Disabled
// rather than left to fail, with the reason on the control.
const reorderBlocked = computed(() => v('delay_ms') <= 0);
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

    <!-- Disabled rather than hidden when there is no delay. Hiding it would
         make the row appear and vanish as delay crosses zero, moving the loss
         slider under the cursor, and would remove the explanation exactly when
         it is needed: the control is temporarily inapplicable, not clutter. -->
    <div class="row" :class="{ off: jitterOff }">
      <label>jitter</label>
      <input
        type="range" min="0" :max="Math.max(jitterMax, 1)" step="1"
        :value="jitterVal" :disabled="disabled || jitterOff"
        :title="jitterOff
          ? 'jitter varies the delay, so it needs a delay to vary'
          : 'each packet is delayed by a value drawn from this range'"
        @input="set('jitter_ms', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">{{ jitterText }}</span>
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

    <!-- The long tail. Out of the way until it is doing something, and then it
         stays put: `extrasOpen` is true whenever anything inside is non-zero,
         so no impairment can be in force while its control is off screen. -->
    <button
      v-if="!extrasOpen" class="more" :disabled="disabled"
      @click="toggleExtras()"
    >+ {{ EXTRA_IMPAIRMENTS.map((e) => e.label).join(', ') }}</button>

    <template v-else>
      <div
        v-for="e in EXTRA_IMPAIRMENTS" :key="e.key"
        class="row"
        :class="{ blocked: e.needsDelay && reorderBlocked }"
      >
        <label :title="e.title">{{ e.label }}</label>
        <input
          type="range" min="0" :max="e.max" :step="e.step"
          :value="v(e.key)"
          :disabled="disabled || (e.needsDelay && reorderBlocked)"
          @input="set(e.key, +($event.target as HTMLInputElement).value)"
        />
        <span
          v-if="e.needsDelay && reorderBlocked" class="val need"
          title="netem reorders by letting packets skip the delay queue. With no delay there is no queue, and it refuses the whole rule."
        >needs delay</span>
        <span v-else class="val num">{{ v(e.key) }} {{ e.unit }}</span>
      </div>
      <button
        v-if="!extrasActive" class="more" :disabled="disabled"
        @click="toggleExtras()"
      >− fewer</button>
    </template>
  </div>
</template>

<style scoped>
.rows.disabled { opacity: 0.45; pointer-events: none; }
/* Dimmed, but the label and the reason stay readable: the row is explaining
   itself, so blanking it would defeat the point of keeping it. */
.row.off { opacity: 0.55; }

/* Quiet: this is an affordance, not a control. It should read as the edge of
   the panel rather than competing with the sliders above it. */
.more {
  align-self: start;
  padding: 1px 0;
  font: inherit; font-size: 11px;
  color: var(--ink-faint);
  background: none; border: 0;
  cursor: pointer;
}
.more:hover { color: var(--ink-dim); }

/* A blocked row stays legible rather than greying to nothing: the reason it is
   unavailable is the useful part, and it is written where the value goes. */
.row.blocked label { color: var(--ink-faint); }
.val.need { font-size: 10px; color: var(--warn); white-space: nowrap; }
</style>
