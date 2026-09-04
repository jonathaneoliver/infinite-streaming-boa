<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useEvents } from '@/composables/useEvents';

/**
 * The activity log: a strip under the tabs saying what just happened.
 *
 * Collapsed it shows the newest few lines, not one. One line answers "did
 * anything change while I was not looking" but not "what changed", and a single
 * event is often meaningless alone -- a client leaving one radio and appearing
 * on the other is two lines, and the second is what makes the first mean
 * something. A fixed preview of a few rows still keeps the devices, which are
 * the actual content, above the fold; open it for the rest.
 *
 * It does NOT survive a restart of the daemon, by design: the ring is in
 * memory, so a deploy clears it. Persisting an association event per client per
 * roam is exactly the steady write that wears an SD card out.
 */
const OPEN_KEY = 'boa.activity.open';

/** Rows shown while collapsed. Enough for a roam and the context around it. */
const PREVIEW = 4;

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

const shown = computed(() =>
  open.value ? log.events.value : log.events.value.slice(0, PREVIEW),
);
const hidden = computed(() => log.events.value.length - shown.value.length);

/**
 * The badge counts only what SCROLLED PAST unseen. The newest few lines are on
 * screen even when collapsed, so counting those as unseen would claim there is
 * something to look at while the reader is looking straight at it.
 */
const unseenHidden = computed(() => Math.max(0, log.unseen.value - PREVIEW));

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
      <!-- The bar carries no event text of its own: the rows below always show
           the newest lines, and repeating one of them here reads as a stutter.
           It says what is NOT on screen instead. -->
      <span class="line quiet">
        <template v-if="!log.events.value.length">nothing yet</template>
        <template v-else-if="hidden > 0">{{ hidden }} more</template>
        <template v-else>{{ log.events.value.length }} since the daemon started</template>
      </span>
      <span v-if="!open && unseenHidden" class="badge">{{ unseenHidden }}</span>
    </button>

    <div class="rows" :class="{ scroll: open }">
      <!-- The failure is shown IN the log rather than beside it: an activity
           panel that has silently stopped polling looks exactly like a quiet
           box, and that is the one lie it must not tell. -->
      <p v-if="log.err.value" class="row bad">{{ log.err.value }}</p>
      <p v-else-if="!log.events.value.length" class="row quiet">
        Nothing has happened since the daemon started. Joins, roams between
        radios, channel changes and anything pressed here land in this list.
      </p>
      <p v-for="e in shown" :key="e.seq" class="row">
        <span class="t">{{ clock(e.at) }}</span>
        <span class="dot" :class="e.kind" />
        <span class="text">{{ e.text }}</span>
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
  border-top: 1px solid var(--line-soft);
  padding: 4px 10px 6px;
}
/* Only the open log scrolls. Collapsed it is a fixed handful of rows, so the
   page below it sits at the same height whatever has just happened. */
.rows.scroll {
  max-height: 220px;
  overflow-y: auto;
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
/* Each row is one line high, truncated rather than wrapped, for the same
   reason: a long event text must not reflow the devices below it. */
.row .text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
