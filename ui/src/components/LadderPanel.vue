<script setup lang="ts">
import { computed, ref } from 'vue';
import type { Client, Ladder } from '@/types';

const props = defineProps<{ client: Client }>();
const emit = defineEmits<{
  sweep: [service: string];
  stopSweep: [];
  removeLadder: [service: string];
}>();

/**
 * The service name is required and has no default.
 *
 * A ladder belongs to a service, not to a device: Netflix, YouTube and a
 * self-hosted stream share no rungs, no segment durations and no adaptation
 * logic. Defaulting the name would mean the next sweep of a different service
 * silently replaced the last one's result, and nobody would find out until they
 * generated a pattern from the wrong ladder.
 *
 * It is typed rather than detected. SNI is readable today but ECH is rolling
 * out and QUIC buries the handshake; DNS snooping stops working the moment a
 * player uses DoH -- and stops working silently. Both would end up confidently
 * mislabelling a ladder, which is worse than asking.
 */
const service = ref('');

const sweep = computed(() => props.client.sweep);
const running = computed(() => sweep.value?.state === 'running');
const ladders = computed<Ladder[]>(() => props.client.policy.ladders ?? []);

const canStart = computed(
  () => service.value.trim().length > 0 && props.client.present && props.client.shapeable,
);

// Not a countdown: the total is not known until the ceiling has been measured,
// and a progress bar that invents a denominator is worse than a level count.
const phaseLabel = computed(() => {
  const s = sweep.value;
  if (!s) return '';
  if (s.state === 'done') return 'finished';
  if (s.state === 'failed') return 'stopped';
  if (s.level === 0) return 'measuring the ceiling, unconditioned';
  return s.phase === 'settling'
    ? `holding ${s.cap_mbps.toFixed(2)} Mbps, waiting for the player to react`
    : `measuring at ${s.cap_mbps.toFixed(2)} Mbps`;
});

function start() {
  if (!canStart.value) return;
  emit('sweep', service.value.trim());
}

function when(ms?: number): string {
  if (!ms) return '';
  return new Date(ms).toLocaleString();
}
</script>

<template>
  <div class="ladder">
    <h3>
      Rendition ladders
      <span class="meta">measured per service</span>
    </h3>

    <!-- Running sweep. Shown in place of the start control rather than beside
         it: only one sweep runs at a time, because Wi-Fi airtime is shared and
         two at once would measure each other. -->
    <div v-if="running" class="sweep-live">
      <div class="sweep-head">
        <span class="dot live"></span>
        <b>{{ sweep!.service }}</b>
        <span class="meta">level {{ sweep!.level }} — {{ phaseLabel }}</span>
        <span class="spacer"></span>
        <button class="ghost" @click="emit('stopSweep')">stop</button>
      </div>
      <p class="meta warn-note">
        This device's conditioning is being driven by the sweep and any delay,
        jitter or loss you set is suspended until it finishes. Nothing is
        written: your settings come back the moment it ends.
      </p>
      <div v-if="sweep!.found?.length" class="rungs">
        <span v-for="r in sweep!.found" :key="r.mbps" class="rung">
          {{ r.mbps.toFixed(2) }}
        </span>
        <span class="meta">Mbps so far</span>
      </div>
      <p v-else class="meta">No rungs found yet.</p>
    </div>

    <div v-else class="sweep-start">
      <input
        v-model="service" class="svc"
        placeholder="what is it streaming? e.g. netflix"
        :disabled="!client.present || !client.shapeable"
        @keyup.enter="start"
      />
      <button :disabled="!canStart" @click="start">characterise</button>
      <span v-if="!client.present || !client.shapeable" class="meta">
        The device has to be connected and addressed to be swept.
      </span>
      <span v-else class="meta">
        Start playback first, then sweep. It steps the cap down past each
        rendition in turn and records where throughput settles, waiting for the
        player to react at every step — a few minutes, and playback is degraded
        throughout.
      </span>
    </div>

    <!-- The outcome of a finished or abandoned run. Kept visible because the
         reason a sweep stopped is the most useful thing about it: "the client
         stopped sending" and "reached the floor" mean very different things
         about the ladder below the last rung found. -->
    <p v-if="sweep && !running" class="meta sweep-reason">
      Last sweep of <b>{{ sweep.service }}</b> {{ sweep.state === 'done' ? 'finished' : 'stopped' }}:
      {{ sweep.reason }}
    </p>

    <table v-if="ladders.length" class="ladders">
      <thead>
        <tr>
          <th>service</th>
          <th>rungs (Mbps)</th>
          <th>source</th>
          <th>measured</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="l in ladders" :key="l.service">
          <td><b>{{ l.service }}</b></td>
          <td class="rungs">
            <!-- The cap that produced a rendition is shown beside its cost,
                 because the cost alone will not hold a player there: it wants
                 headroom, so capping at the rung's own bitrate drops it. -->
            <span
              v-for="r in l.rungs" :key="r.mbps"
              class="rung" :class="{ shaky: r.unstable }"
              :title="[
                r.unstable ? 'Measured off a noisy window — approximate' : '',
                r.up_at_mbps ? `climbed into it at a ${r.up_at_mbps.toFixed(2)} Mbps cap` : '',
                r.down_at_mbps ? `fell out of it at ${r.down_at_mbps.toFixed(2)} Mbps` : '',
              ].filter(Boolean).join(' · ')"
            >{{ r.mbps.toFixed(2) }}<small
              v-if="r.up_at_mbps" class="at"
            >@{{ r.up_at_mbps.toFixed(2) }}</small></span>
          </td>
          <!-- Provenance is rendered, not hidden: a swept ladder and a
               hand-typed one are different claims and must not look alike. -->
          <td>
            <span class="badge" :class="l.provenance">{{ l.provenance }}</span>
          </td>
          <td class="meta num">{{ when(l.measured_at) }}</td>
          <td>
            <button class="ghost" @click="emit('removeLadder', l.service)">
              remove
            </button>
          </td>
        </tr>
      </tbody>
    </table>
    <p v-else-if="!running" class="meta">
      No ladders yet for this device.
    </p>
  </div>
</template>

<style scoped>
.ladder {
  padding: 12px 14px;
  border-top: 1px solid var(--line);
}
.ladder h3 {
  margin: 0 0 8px;
  font-size: 13px;
  display: flex;
  gap: 8px;
  align-items: baseline;
}
.sweep-start {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.svc {
  min-width: 15rem;
}
.sweep-head {
  display: flex;
  gap: 8px;
  align-items: center;
}
.warn-note {
  color: var(--warn);
  margin: 6px 0;
}
.sweep-reason {
  margin: 8px 0 0;
}
.rungs {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
}
.rung {
  font-variant-numeric: tabular-nums;
  padding: 1px 6px;
  border: 1px solid var(--line);
  border-radius: 4px;
}
/* The cap is secondary to the cost, so it reads as an annotation rather than
   competing with the number it qualifies. */
.rung .at {
  opacity: 0.65;
  margin-left: 2px;
}
/* An approximate rung must not read as an exact one. */
.rung.shaky {
  border-style: dashed;
  color: var(--warn);
}
.ladders {
  width: 100%;
  margin-top: 10px;
  border-collapse: collapse;
}
.ladders th,
.ladders td {
  text-align: left;
  padding: 4px 8px 4px 0;
  vertical-align: top;
}
.ladders th {
  font-weight: 500;
  color: var(--ink-dim);
}
.badge.measured {
  color: var(--ok);
}
.badge.typed {
  color: var(--ink-dim);
}
.spacer {
  flex: 1;
}
</style>
