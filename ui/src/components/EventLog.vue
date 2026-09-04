<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useEvents, type BoaEvent } from '@/composables/useEvents';

/**
 * The activity log: a strip under the tabs saying what just happened.
 *
 * Collapsed to ONE line by default. The question it answers -- "did anything
 * change while I was not looking" -- is answered by the newest line alone
 * nine times out of ten, and a permanently open log of twenty lines would push
 * the devices, which are the actual content, down the page. Open it when the
 * one line is not enough.
 *
 * It does NOT survive a restart of the daemon, by design: the ring is in
 * memory, so a deploy clears it. Persisting an association event per client per
 * roam is exactly the steady write that wears an SD card out.
 */
const OPEN_KEY = 'boa.activity.open';

const log = useEvents();
const open = ref(localStorage.getItem(OPEN_KEY) === '1');

watch(open, (v) => {
  localStorage.setItem(OPEN_KEY, v ? '1' : '0');
  if (v) log.markSeen();
});
onMounted(log.start);

// Only the badge cares about unseen events while it is closed; once it is open
// they are, by definition, seen.
watch(log.events, () => {
  if (open.value) log.markSeen();
});

const latest = computed<BoaEvent | undefined>(() => log.events.value[0]);

/**
 * Clock time, not "3m ago". These events are read against a bench test that is
 * being watched live, so what matters is lining an event up with something on a
 * chart, and a chart is labelled in clock time.
 */
function clock(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}
</script>

<template>
  <section class="log" :class="{ open }">
    <button
      class="bar"
      :aria-expanded="open"
      :title="open ? 'Collapse the activity log' : 'What has happened on the box'"
      @click="open = !open"
    >
      <span class="caret">{{ open ? '▾' : '▸' }}</span>
      <span class="k">activity</span>
      <!-- The newest line is the WHOLE bar when closed, and disappears from it
           when open: the list below starts with that same line, and showing it
           twice makes the panel look like it stuttered. -->
      <template v-if="!open">
        <span v-if="latest" class="dot" :class="latest.kind" />
        <span v-if="latest" class="line">
          <span class="t">{{ clock(latest.at) }}</span> {{ latest.text }}
        </span>
        <span v-else class="line quiet">nothing yet</span>
        <span v-if="log.unseen.value" class="badge">{{ log.unseen.value }}</span>
      </template>
      <span v-else class="line quiet">
        {{ log.events.value.length }} since the daemon started
      </span>
    </button>

    <div v-if="open" class="rows">
      <!-- The failure is shown IN the log rather than beside it: an activity
           panel that has silently stopped polling looks exactly like a quiet
           box, and that is the one lie it must not tell. -->
      <p v-if="log.err.value" class="row bad">{{ log.err.value }}</p>
      <p v-else-if="!log.events.value.length" class="row quiet">
        Nothing has happened since the daemon started. Joins, roams between
        radios, channel changes and anything pressed here land in this list.
      </p>
      <p v-for="e in log.events.value" :key="e.seq" class="row">
        <span class="t">{{ clock(e.at) }}</span>
        <span class="dot" :class="e.kind" />
        <span>{{ e.text }}</span>
      </p>
    </div>
  </section>
</template>

<style scoped>
.log {
  border: 1px solid var(--line-soft);
  border-radius: var(--r);
  background: var(--panel);
  margin-bottom: 10px;
}
.bar {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 6px 10px;
  background: none;
  border: 0;
  color: var(--ink-dim);
  font-family: var(--sans);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.bar:hover { color: var(--ink); }
.caret { color: var(--ink-faint); width: 8px; }
.k {
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-size: 10px;
  color: var(--ink-faint);
}
/* The newest line is truncated rather than wrapped: the bar is one line high
   whatever it holds, so the page below it never moves as events arrive. */
.line {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.quiet { color: var(--ink-faint); }
.badge {
  background: var(--panel-2);
  border: 1px solid var(--line);
  border-radius: 999px;
  padding: 0 6px;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}
.rows {
  max-height: 220px;
  overflow-y: auto;
  border-top: 1px solid var(--line-soft);
  padding: 4px 10px 6px;
}
.row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 0;
  padding: 2px 0;
  font-size: 12px;
  color: var(--ink-dim);
}
.row.bad { color: var(--bad); }
.t {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--ink-faint);
  font-variant-numeric: tabular-nums;
}
/* Kind is carried by a colour, not a word: the text already says what happened
   and a repeated "RADIO" label in every row is a column of noise. */
.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
  background: var(--ink-faint);
}
.dot.join { background: var(--ok); }
.dot.leave { background: var(--ink-faint); }
.dot.roam { background: var(--down); }
.dot.radio { background: var(--up); }
.dot.action { background: var(--ink-dim); }
.dot.warning { background: var(--bad); }
</style>
