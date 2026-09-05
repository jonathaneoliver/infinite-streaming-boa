<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue';
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
  stickToBottom();
});

// Opening the panel should show the live end, not the top of the history.
watch(open, (v) => {
  if (v) {
    following.value = true;
    stickToBottom();
  }
});

onMounted(stickToBottom);

/**
 * The scrolling area, so the newest line can be kept in view.
 */
const rows = ref<HTMLElement | null>(null);

/**
 * Whether to follow the bottom.
 *
 * TRUE until the operator scrolls away, and true again the moment they come
 * back. A log that always jumps to the end fights anyone reading back through
 * it -- which on this box is exactly when it matters, because reading back is
 * how a run is reconstructed afterwards. A log that never follows makes them
 * scroll for every new line.
 *
 * "Near enough" rather than exactly at the end: a couple of pixels of rounding,
 * a partially visible row, or a scroll that lands one pixel short would
 * otherwise turn following off silently and for good.
 */
const NEAR = 24;
const following = ref(true);

function onScroll() {
  const el = rows.value;
  if (!el) return;
  following.value = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR;
}

function stickToBottom() {
  if (!following.value) return;
  const el = rows.value;
  if (!el) return;
  // After the DOM has the new row, or this scrolls to where the list used to
  // end and stops one line short every time.
  nextTick(() => {
    el.scrollTop = el.scrollHeight;
  });
}

const shown = computed(() =>
  // The LAST few when closed, not the first: the list is chronological now, so
  // the newest lines are at the end and those are the ones a collapsed panel
  // exists to show.
  open.value ? log.events.value : log.events.value.slice(-PREVIEW),
);

/**
 * How many blank rows to hold the closed log open with.
 *
 * The comment below promised "a fixed handful of rows, so the page below it
 * sits at the same height whatever has just happened", and slice() only ever
 * delivered a CAP: with fewer than PREVIEW events the panel was shorter and
 * grew as they arrived, shifting everything under it -- most visibly after a
 * restart clears the ring and it grows from nothing four times over.
 *
 * Padding to PREVIEW makes the promise true. Blank rows rather than a
 * min-height, so the reserved space is exactly a row tall however the type
 * metrics land.
 */
const padRows = computed(() => (open.value ? 0 : Math.max(0, PREVIEW - shown.value.length)));
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

    <div ref="rows" class="rows" :class="{ scroll: open }" @scroll="onScroll">
      <!-- The failure is shown IN the log rather than beside it: an activity
           panel that has silently stopped polling looks exactly like a quiet
           box, and that is the one lie it must not tell. -->
      <p v-if="log.err.value" class="row bad">{{ log.err.value }}</p>
      <p v-else-if="!log.events.value.length" class="row quiet">
        Nothing has happened since the daemon started. Joins, roams between
        radios, channel changes and anything pressed here land in this list.
      </p>
      <!-- ABOVE the rows, not below. The newest line sits at the bottom edge,
           so a short log has to grow downwards from the top like a terminal
           does -- pads underneath would leave the live end floating in the
           middle of the panel. Holds the closed log at its full height either
           way, so nothing below it moves as events arrive. See padRows. -->
      <p v-for="i in padRows" :key="`pad${i}`" class="row pad" aria-hidden="true">
        <span class="t">&nbsp;</span>
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
/* Only the open log scrolls, and it does so at a FIXED height.
 *
 * max-height was the same half-measure padRows was written to fix at the other
 * end: it caps the panel but does not reserve it, so an open log grew from one
 * row up to the cap as events arrived and pushed the whole page down on every
 * line. On a box where watching the log IS the task -- events arrive exactly
 * when an operator is reaching for the controls under it -- that is the worst
 * possible moment to move them.
 *
 * A fixed height means empty space under a quiet log, which is the right trade:
 * the space belongs to the log either way, and reserving it is the point.
 *
 * scrollbar-gutter keeps the width steady too, so the rows do not reflow the
 * first time the content passes the fold. */
.rows.scroll {
  height: 220px;
  overflow-y: auto;
  scrollbar-gutter: stable;
}
.row.pad { visibility: hidden; }
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
