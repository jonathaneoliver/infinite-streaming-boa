<script setup lang="ts">
/**
 * Rate / delay / jitter / loss for one direction.
 *
 * Local state during a drag, committed on RELEASE. The slider reads from
 * `local` while the operator is moving it and from the model otherwise, so a
 * telemetry frame arriving mid-drag cannot snatch the handle away. `local`
 * clears when the model catches up.
 *
 * Nothing is sent while the handle is moving: see `commit` below for why a
 * drag that applied every value it passed over was changing the experiment it
 * was being used to set up.
 */
import { computed, inject, ref, watch } from 'vue';
import type { ComputedRef } from 'vue';
import type { Shape } from '@/types';
import {
  BURST_MAX, EXTRA_IMPAIRMENTS,
  lossToPos, posToLoss, posToRate, rateToPos,
} from '@/types';
import { useFields } from '@/composables/useFields';

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
  /**
   * Fields to keep on screen regardless of whether they are currently doing
   * anything.
   *
   * For a control that DRIVES these values rather than setting them once. The
   * usual rule -- show a field while it is in force -- makes controls appear
   * and vanish under the operator's hand as a distance model crosses each
   * field's threshold, which reads as the interface glitching rather than as
   * the model working. When something is driving the shape, everything it can
   * reach stays visible for as long as it is driving.
   */
  always?: readonly string[];
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

/**
 * Take the new value locally, without telling anyone.
 *
 * The handle and the readout follow the pointer from here, so the control feels
 * live; nothing leaves the browser.
 */
function stage(k: keyof Shape, value: number) {
  const next: Partial<Shape> = { [k]: value };
  // Lowering delay past the current jitter would send jitter > delay, which the
  // daemon refuses -- so delay could not be reduced without first reducing
  // jitter, and the slider simply stopped moving with the reason buried in a
  // conflict message. Jitter follows delay down, because jitter is a width
  // around delay and a width cannot outlive the thing it is a width of.
  if (k === 'delay_ms' && v('jitter_ms') > value) next.jitter_ms = value;
  local.value = { ...local.value, ...next };
}

/**
 * A slider APPLIES on release, not while it moves.
 *
 * `input` fires once per pixel of a drag; `change` fires when the drag ends --
 * and, for a keyboard user, once per arrow key, which is already one
 * deliberate value per press. So `input` stages and `change` commits.
 *
 * Dragging rate from 100 to 3 Mbps used to enforce every value on the way past:
 * each one a real tc reconfiguration on a live client and a new policy
 * revision, so a player spent the drag reacting to a dozen caps nobody chose,
 * and the run was polluted before the intended one arrived. The 200ms debounce
 * upstream never prevented that -- it coalesced PAUSES in a drag, and a slow
 * drag is mostly pauses.
 *
 * The staged value still shows while dragging, so this costs no feedback: what
 * changes is only when the box is told.
 */
function commit(k: keyof Shape, value: number) {
  stage(k, value);
  emit('update', { ...props.shape, ...local.value });
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

/*
 * Show a field only while it is doing something, or when the operator reveals it.
 *
 * rate is always shown -- nearly every test sets a cap. The rest collapse to a
 * "+ field" chip when zero; a revealed-but-still-empty one gets a "- field" to
 * put it away again. A field in force (non-zero) has no chip: an impairment can
 * never be hidden while it is conditioning the traffic -- the same guarantee the
 * old always-open second tier gave. Shared with the pattern lanes via useFields,
 * so the two show and hide together. jitter rides with delay and burst with
 * loss (each is a width of the field above it, meaningless on its own).
 */
const { revealed, reveal, hide } = useFields();

const COLLAPSIBLE = [
  { key: 'delay_ms', label: 'delay' },
  { key: 'loss_pct', label: 'loss' },
  { key: 'reorder_pct', label: 'reorder' },
  { key: 'corrupt_pct', label: 'corrupt' },
] as const;

function active(key: keyof Shape): boolean {
  return v(key) > 0;
}
function shown(key: string): boolean {
  return active(key as keyof Shape)
    || revealed.value.has(key)
    || !!props.always?.includes(key);
}
const shownExtras = computed(() => EXTRA_IMPAIRMENTS.filter((e) => shown(e.key)));
const addChips = computed(() => COLLAPSIBLE.filter((f) => !shown(f.key)));
const dropChips = computed(() =>
  COLLAPSIBLE.filter(
    (f) => shown(f.key) && !active(f.key) && !props.always?.includes(f.key),
  ),
);

// netem rejects `reorder` outright when there is no delay to reorder against,
// and a rejected command installs no qdisc at all -- so this would not merely
// fail to reorder, it would drop the device's rate and loss with it. Disabled
// rather than left to fail, with the reason on the control.
const reorderBlocked = computed(() => v('delay_ms') <= 0);

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

// Log-scaled like rate, so the bottom few percent stay usable and 100% -- a
// blackhole -- is reachable by dragging rather than only through the API.
const lossPos = computed(() => lossToPos(loss.value));
const lossText = computed(() =>
  loss.value >= 100
    ? 'blackhole'
    : `${loss.value < 1 ? loss.value.toFixed(2) : loss.value.toFixed(1)} %`,
);

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
        @input="stage('rate_mbps', posToRate(+($event.target as HTMLInputElement).value))"
        @change="commit('rate_mbps', posToRate(+($event.target as HTMLInputElement).value))"
      />
      <span class="val num">{{ rateText }}</span>
    </div>

    <div v-if="shown('delay_ms')" class="row">
      <label>delay</label>
      <input
        type="range" min="0" max="1000" step="1"
        :value="v('delay_ms')" :disabled="disabled"
        @input="stage('delay_ms', +($event.target as HTMLInputElement).value)"
        @change="commit('delay_ms', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">{{ v('delay_ms') }} ms</span>
    </div>

    <!-- jitter rides with delay: it is a width of it, so it shows whenever delay
         does, and is disabled (not hidden) while there is no delay to vary. -->
    <div v-if="shown('delay_ms')" class="row" :class="{ off: jitterOff }">
      <label>jitter</label>
      <input
        type="range" min="0" :max="Math.max(jitterMax, 1)" step="1"
        :value="jitterVal" :disabled="disabled || jitterOff"
        :title="jitterOff
          ? 'jitter varies the delay, so it needs a delay to vary'
          : 'each packet is delayed by a value drawn from this range'"
        @input="stage('jitter_ms', +($event.target as HTMLInputElement).value)"
        @change="commit('jitter_ms', +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">{{ jitterText }}</span>
    </div>

    <div v-if="shown('loss_pct')" class="row">
      <label>loss</label>
      <input
        type="range" min="0" max="100" step="1"
        :value="lossPos" :disabled="disabled"
        @input="stage('loss_pct', posToLoss(+($event.target as HTMLInputElement).value))"
        @change="commit('loss_pct', posToLoss(+($event.target as HTMLInputElement).value))"
      />
      <span class="val num" :class="{ black: loss >= 100 }">{{ lossText }}</span>
    </div>

    <!-- Burstiness rides with the loss it modifies: it does not add an
         impairment, it changes what the loss slider means. Disabled rather than
         hidden when it can do nothing. -->
    <template v-if="shown('loss_pct')">
      <div class="row" :class="{ off: burstOff }">
        <label>burst</label>
        <input
          type="range" min="1" :max="BURST_MAX" step="1"
          :value="burst" :disabled="disabled || burstOff" :title="burstWhy"
          @input="stage('loss_burst', +($event.target as HTMLInputElement).value)"
          @change="commit('loss_burst', +($event.target as HTMLInputElement).value)"
        />
        <span class="val num">{{ burstText }}</span>
      </div>
      <p v-if="burstNote" class="meta burst-note">{{ burstNote }}</p>
      <p v-else-if="!burstCap.ok" class="meta burst-note warn">{{ burstCap.note }}</p>
    </template>

    <div
      v-for="e in shownExtras" :key="e.key"
      class="row"
      :class="{ blocked: e.needsDelay && reorderBlocked }"
    >
      <label :title="e.title">{{ e.label }}</label>
      <input
        type="range" min="0" :max="e.max" :step="e.step"
        :value="v(e.key)"
        :disabled="disabled || (e.needsDelay && reorderBlocked)"
        :title="e.needsDelay && reorderBlocked
          ? 'netem reorders by letting packets skip the delay queue. With no delay there is no queue, and it refuses the whole rule.'
          : e.title"
        @input="stage(e.key, +($event.target as HTMLInputElement).value)"
        @change="commit(e.key, +($event.target as HTMLInputElement).value)"
      />
      <span class="val num">{{ v(e.key) }} {{ e.unit }}</span>
    </div>

    <!-- One chip per collapsed field to add it, and per revealed-but-empty
         field to put it away. A field in force has no chip: it stays on screen
         for as long as it is conditioning the traffic. -->
    <div v-if="(addChips.length || dropChips.length) && !disabled" class="chips">
      <button v-for="f in addChips" :key="f.key" class="more" @click="reveal(f.key)">+ {{ f.label }}</button>
      <button v-for="f in dropChips" :key="f.key" class="more" @click="hide(f.key)">− {{ f.label }}</button>
    </div>
  </div>
</template>

<style scoped>
.rows.disabled { opacity: 0.45; pointer-events: none; }
/* Dimmed, but the label and the reason stay readable: the row is explaining
   itself, so blanking it would defeat the point of keeping it. */
.row.off { opacity: 0.55; }

/* Total loss is not "a high number", it is a different state: nothing reaches
   the device at all. It reads as a word rather than 100.0 %, and carries the
   same warning colour the interface uses for conditioning that is severe
   enough to be worth noticing on the way past. */
.val.black { color: var(--warn); }

/* Quiet: this is an affordance, not a control. It should read as the edge of
   the panel rather than competing with the sliders above it. */
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  margin-top: 2px;
  grid-column: 1 / -1;
}
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

.burst-note { margin: 2px 0 0; grid-column: 1 / -1; }
.burst-note.warn { color: var(--warn); }
</style>
