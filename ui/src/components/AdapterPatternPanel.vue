<script setup lang="ts">
/**
 * The adapter timeline: what happens to the box's own RADIOS over time.
 *
 * # Why its own panel and not the device editor
 *
 * PatternPanel edits a device's conditioning, and every one of its value lanes
 * has a vertical axis carrying a number. A radio lane has no number: a radio is
 * up or it is not, and an action either fired or it did not. Sharing the
 * component would have meant a mode flag through 1600 lines of something whose
 * whole job is the other thing.
 *
 * Everything else here is deliberately the same, because an operator should not
 * have to learn a second timeline: the step path, the transparent hit areas
 * that tint on hover, the hollow diamonds on their edges, the headroom past the
 * end, the shaded beyond, the plain-text chips, and a playhead the daemon owns
 * while a run exists.
 *
 * # The grammar
 *
 * One lane per radio per kind, revealed per KIND so one chip opens the row on
 * every adapter. `iface` is the radio the event acts ON:
 *
 *   off      that radio goes down for the width of the block. The one SILENT
 *            impairment, so it has a floor: below MIN_RADIO_OFF_SEC the client
 *            never notices, because it is not told and has to miss beacons.
 *   deauth   its clients are thrown off, announced, and come back HERE.
 *   evict    its clients are asked to leave, and go to the other radio.
 *   gather   everyone on the OTHER radios is asked to come here.
 *
 * gather and evict are the same 802.11v request and differ only in which slot
 * this lane's radio fills. Named for the effect rather than the mechanism,
 * exactly as drop and deadzone are both deauth.
 *
 * # Two things drawn rather than inferred
 *
 * Where every radio is down at once is banded, because that is the only state
 * which is a real outage -- one radio down is a forced roam -- and it should be
 * something typed rather than something that emerged from two blocks
 * overlapping.
 *
 * The strip beneath the lanes reads where the pattern INTENDS clients to be.
 * Herding is lossy, so a pattern that gathers and loops starts its second lap
 * somewhere its first did not, which is invisible from the lanes alone.
 */
import { computed, onMounted, ref, watch } from 'vue';
import type { Pattern, PatternView, RadioEvent } from '@/types';
import { MIN_RADIO_OFF_SEC } from '@/types';

const props = defineProps<{
  /** The radios this box watches, in preference order. */
  radios: string[];
  /** The live box run, when one is playing. */
  run: PatternView | null;
}>();

type Kind = RadioEvent['kind'];
const KINDS: { kind: Kind; hint: string }[] = [
  { kind: 'off', hint: 'radio down, silent' },
  { kind: 'gather', hint: 'everyone comes here' },
  { kind: 'evict', hint: "this radio's clients leave" },
  { kind: 'deauth', hint: 'thrown off, come back here' },
];

const events = ref<RadioEvent[]>([]);
const loop = ref(true);
const revealed = ref(new Set<Kind>());
/** How the lanes are stacked.
 *
 *  by effect  -- the `off` rows sit adjacent, which is what makes a NO AP
 *                overlap something you see rather than something you work out,
 *                and one chip opens a contiguous set.
 *  by adapter -- all of one radio's rows together, which reads as that radio's
 *                story: gather, then deauth, then evict, then off.
 *
 *  Neither is right for every question, so it is a control rather than a
 *  decision baked into the render. */
const groupBy = ref<'effect' | 'adapter'>('effect');
const msg = ref('');
const err = ref('');
const busy = ref(false);
const stackEl = ref<HTMLElement | null>(null);

/* ---------------------------------------------------------------- lanes -- */

const kindUses = (kind: Kind) => events.value.some((e) => e.kind === kind);
/** An effect in use is never collapsible. The same rule PatternPanel applies to
 *  its own lanes, and here it is a safety property rather than tidiness: an
 *  adapter pattern must not be able to hide that it takes a radio off the air. */
const kindShown = (kind: Kind) => kindUses(kind) || revealed.value.has(kind);

const lanes = computed(() =>
  groupBy.value === 'effect'
    ? KINDS.filter((k) => kindShown(k.kind)).flatMap((k) =>
        props.radios.map((iface) => ({ iface, ...k })),
      )
    : props.radios.flatMap((iface) =>
        KINDS.filter((k) => kindShown(k.kind)).map((k) => ({ iface, ...k })),
      ),
);
const addChips = computed(() => KINDS.filter((k) => !kindShown(k.kind)));
const dropChips = computed(() => KINDS.filter((k) => kindShown(k.kind) && !kindUses(k.kind)));

const laneEvents = (iface: string, kind: Kind) =>
  events.value.map((e, i) => ({ e, i })).filter(({ e }) => e.iface === iface && e.kind === kind);

/** A zero-duration pulse still needs a visible, grabbable width. Same value
 *  PatternPanel uses, so a spike here is the width of a spike there. */
const PULSE_VIS_SEC = 0.5;
const visSec = (e: RadioEvent) => (e.kind === 'off' ? e.dur_sec ?? 0 : PULSE_VIS_SEC);
const endOf = (e: RadioEvent) => e.at_sec + (e.kind === 'off' ? e.dur_sec ?? 0 : 0);

/* ----------------------------------------------------------------- time -- */

/**
 * The end of the pattern: the loop point, and where a one-shot run stops.
 *
 * Derived from the events, as the client editor derives it from the last
 * keyframe -- so dragging an event rightwards LENGTHENS the pattern and there
 * is no number field to keep in step with the picture.
 *
 * `tail` is the one addition. A client pattern can express a quiet run-out by
 * ending on a keyframe that changes nothing; a radio lane has no no-op event to
 * place, and "the radio comes back, then two minutes to watch clients return"
 * is a normal thing to want. So the end marker drags past the last event and
 * holds there.
 */
const tail = ref(0);
const contentEnd = computed(() => events.value.reduce((m, e) => Math.max(m, endOf(e)), 0));
const MIN_SPAN_SEC = 60;
const dur = computed(() => Math.max(MIN_SPAN_SEC, contentEnd.value, tail.value));

/**
 * The ruler runs past the end of the pattern.
 *
 * Without headroom the end sits at 100% of the width and there is nowhere to
 * drag it TO: the timeline could only ever be shortened. Same constants as
 * PatternPanel, so both rulers feel the same under the hand.
 */
const HEADROOM_MIN_SEC = 30;
const HEADROOM_FRAC = 0.25;
const viewSpan = computed(() => dur.value + Math.max(HEADROOM_MIN_SEC, dur.value * HEADROOM_FRAC));

const drag = ref<{
  i: number;
  mode: 'move' | 'resize' | 'resizeL' | 'end';
  x0: number;
  at0: number;
  dur0: number;
  span: number;
} | null>(null);

/**
 * What the ruler is drawn against, which is not viewSpan mid-drag.
 *
 * Letting the headroom regrow DURING a drag would stretch the axis under the
 * marker as it moved, so seconds-per-pixel would stop being constant and the
 * same hand movement would mean less time the further right you got. The scale
 * is pinned for the gesture and the headroom comes back in one step on release.
 */
const renderSpan = computed(() =>
  drag.value ? Math.max(drag.value.span, dur.value) : viewSpan.value,
);

const pct = (s: number) => `${(s / renderSpan.value) * 100}%`;
const widthPct = (s: number) => `${(s / renderSpan.value) * 100}%`;

/* The playhead has two owners and which is speaking must never be ambiguous.
   While a run exists it is the daemon's, because a playhead showing the last
   scrub during playback would put an outage at the wrong second. Otherwise it
   is a scrub position, local to this browser, and scrubbing enforces nothing. */
const scrub = ref(0);
const head = computed(() => (props.run ? props.run.pos_sec : scrub.value));
const playing = computed(() => props.run?.state === 'running');

/**
 * Folded by default, the same as a client's timeline.
 *
 * Eight radio lanes, a ruler, a preset row and a chip row is a lot of chrome
 * for something the box is usually not doing -- and PatternPanel had already
 * reached that conclusion for four lanes on a device. The summary and the
 * transport stay in the head, so a box with a pattern is never silent about
 * having one.
 */
const open = ref(false);

/**
 * The whole header toggles the fold, not just the button on the end.
 *
 * The same two exclusions PatternPanel documents, mirrored rather than
 * reinvented: play, stop and save live in this header and swallowing their
 * clicks would break them; and dragging across the summary to copy a duration
 * ends in a click on the header, which must not fold the panel away underneath
 * the selection.
 *
 * The button stays. It is the keyboard route and it names the action, which a
 * click target alone does not.
 */
function onHeadClick(e: MouseEvent) {
  const el = e.target as HTMLElement | null;
  if (el?.closest('input, button, a, select, textarea')) return;
  if ((window.getSelection()?.toString() ?? '').length > 0) return;
  open.value = !open.value;
}

/** What the folded header says, so a timeline is never invisible. */
const summary = computed(() => {
  if (!events.value.length) {
    return 'no timeline — the radios hold still';
  }
  const n = events.value.length;
  return `${n} radio event${n === 1 ? '' : 's'} · ${dur.value}s${loop.value ? ' · loops' : ''}`;
});

/**
 * What a folded panel must still say while a run is in force.
 *
 * More important here than on a device card. A device being conditioned is
 * invisible but harmless; a radio that is OFF THE AIR is a missing access
 * point, and a folded panel that hid it would send the operator looking for a
 * hardware fault. So the radios that are down are named, not counted.
 */
const foldedStatus = computed(() => {
  if (!props.run) return '';
  const at = `${props.run.pos_sec.toFixed(0)}s of ${props.run.dur_sec.toFixed(0)}s`;
  const down = props.radios.filter((iface) =>
    events.value.some(
      (e) =>
        e.kind === 'off' &&
        e.iface === iface &&
        props.run!.pos_sec >= e.at_sec &&
        props.run!.pos_sec < endOf(e),
    ),
  );
  const state = props.run.state === 'running' ? 'playing' : props.run.state;
  return down.length
    ? `${state}, ${at} — ${down.join(' and ')} off the air`
    : `${state}, ${at}`;
});

function timeAt(clientX: number): number {
  const box = stackEl.value?.getBoundingClientRect();
  if (!box || renderSpan.value <= 0) return 0;
  const f = Math.max(0, Math.min((clientX - box.left) / box.width, 1));
  // Whole seconds, not the half-seconds a value lane snaps to: a radio cannot
  // be cycled faster than that, and the daemon's floor is in whole seconds.
  return Math.round(f * renderSpan.value);
}

/** Dragging the time lane moves the playhead, and enforces nothing: building a
 *  pattern would otherwise cycle radios at a box nobody is testing yet. */
const scrubbing = ref(false);
function startScrub(ev: PointerEvent) {
  if (props.run) return;
  scrubbing.value = true;
  (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
  scrub.value = Math.min(timeAt(ev.clientX), dur.value);
}
function onScrub(ev: PointerEvent) {
  if (!scrubbing.value) return;
  scrub.value = Math.min(timeAt(ev.clientX), dur.value);
}
function endScrub() {
  scrubbing.value = false;
}
function keyScrub(ev: KeyboardEvent) {
  if (props.run) return;
  const step = ev.shiftKey ? 10 : 1;
  if (ev.key === 'ArrowLeft') {
    scrub.value = Math.max(0, scrub.value - step);
    ev.preventDefault();
  } else if (ev.key === 'ArrowRight') {
    scrub.value = Math.min(dur.value, scrub.value + step);
    ev.preventDefault();
  }
}

/* -------------------------------------------------------------- editing -- */

function add(iface: string, kind: Kind, ev: MouseEvent) {
  if (playing.value) return;
  const e: RadioEvent = { at_sec: timeAt(ev.clientX), iface, kind };
  // An outage opens at the floor, so the shortest thing you can draw is already
  // the shortest thing that works.
  if (kind === 'off') e.dur_sec = MIN_RADIO_OFF_SEC;
  events.value = [...events.value, e].sort((a, b) => a.at_sec - b.at_sec);
}

function remove(i: number) {
  if (playing.value) return;
  events.value = events.value.filter((_, n) => n !== i);
}

function startDrag(i: number, mode: 'move' | 'resize' | 'resizeL' | 'end', ev: PointerEvent) {
  if (playing.value) return;
  const e = events.value[i];
  drag.value = {
    i,
    mode,
    x0: ev.clientX,
    at0: mode === 'end' ? dur.value : e?.at_sec ?? 0,
    dur0: e?.dur_sec ?? 0,
    span: viewSpan.value,
  };
  (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
}

function onDrag(ev: PointerEvent) {
  const d = drag.value;
  if (!d || !stackEl.value) return;
  const perSec = stackEl.value.getBoundingClientRect().width / d.span;
  const delta = Math.round((ev.clientX - d.x0) / perSec);

  if (d.mode === 'end') {
    // Dragging the end marker sets a run-out past the last event. It cannot go
    // left of the content: a pattern is at least as long as the things in it.
    tail.value = Math.max(contentEnd.value, d.at0 + delta);
    return;
  }

  const next = [...events.value];
  const e = { ...next[d.i] };
  if (d.mode === 'move') {
    e.at_sec = Math.max(0, d.at0 + delta);
  } else if (d.mode === 'resizeL') {
    // The falling edge stays put: dragging the rising one changes when the
    // outage starts, not how long the radio is down after it.
    const end = d.at0 + d.dur0;
    const at = Math.max(0, Math.min(d.at0 + delta, end - MIN_RADIO_OFF_SEC));
    e.at_sec = at;
    e.dur_sec = end - at;
  } else {
    e.dur_sec = Math.max(MIN_RADIO_OFF_SEC, d.dur0 + delta);
  }
  next[d.i] = e;
  events.value = next;
}

function endDrag() {
  if (!drag.value) return;
  drag.value = null;
  events.value = [...events.value].sort((a, b) => a.at_sec - b.at_sec);
}

/* -------------------------------------------------------------- drawing -- */

const VB = 100;
/** The lane as a step function, the shape PatternPanel draws for its link
 *  lanes. The line is what makes a lane readable: with shapes alone, "the radio
 *  is up" would be the absence of one, which is a convention you have to know
 *  rather than something drawn. It also makes the loop seam visible -- a clean
 *  loop leaves the line at the height it started. */
function lanePath(iface: string, kind: Kind): string {
  if (renderSpan.value <= 0) return '';
  const hi = 8;
  const lo = VB - 6;
  const x = (t: number) => Math.max(0, Math.min((t / renderSpan.value) * 1000, 1000));
  const end = x(dur.value);
  const evs = laneEvents(iface, kind)
    .map(({ e }) => e)
    .slice()
    .sort((a, b) => a.at_sec - b.at_sec);
  const d = [`M 0,${lo}`];
  for (const e of evs) {
    const a = x(e.at_sec);
    if (a >= end) continue; // an event past the end never plays
    const b = Math.min(x(e.at_sec + visSec(e)), end);
    d.push(
      `L ${a.toFixed(1)},${lo}`,
      `L ${a.toFixed(1)},${hi}`,
      `L ${b.toFixed(1)},${hi}`,
      `L ${b.toFixed(1)},${lo}`,
    );
  }
  d.push(`L ${end.toFixed(1)},${lo}`);
  return d.join(' ');
}

/** Where every radio is off at once. Computed from the lanes rather than
 *  authored, so it cannot disagree with them. */
const blackouts = computed(() => {
  if (props.radios.length < 2) return [] as [number, number][];
  const spans = props.radios.map((iface) =>
    events.value
      .filter((e) => e.iface === iface && e.kind === 'off')
      .map((e) => [e.at_sec, endOf(e)] as [number, number]),
  );
  return spans.reduce((acc, list) =>
    acc.flatMap(([a0, a1]) =>
      list
        .map(([b0, b1]) => [Math.max(a0, b0), Math.min(a1, b1)] as [number, number])
        .filter(([lo, hi]) => hi > lo),
    ),
  );
});


const ticks = computed(() => {
  const span = renderSpan.value;
  const step = span <= 120 ? 15 : span <= 300 ? 30 : 60;
  const out: number[] = [];
  for (let t = 0; t <= span; t += step) out.push(t);
  return out;
});

/* -------------------------------------------------------------- presets -- */

/**
 * Pre-populated patterns, the adapter equivalent of the built-ins above a
 * client's lanes.
 *
 * Built here rather than in the daemon, unlike the client library, and for a
 * reason: a client built-in is generated per device because it is built from
 * that device's measured ladder. An adapter pattern has no ladder -- it is
 * radio names and seconds -- so the only per-box input is which radios exist,
 * which this panel already has. A round trip would buy nothing.
 *
 * All of them respect the outage floor, so a preset can never author the
 * silent-but-unnoticed outage the daemon would reject.
 */
const PRESETS = computed(() => {
  const [a, b] = props.radios;
  const two = props.radios.length >= 2;
  const out: { name: string; note: string; build: () => RadioEvent[] }[] = [];

  if (two) {
    out.push({
      name: 'bounce',
      note:
        `herd everyone onto ${b}, then back to ${a}, twice — a forced roam the ` +
        'client cannot decline, with nothing ever off the air',
      build: () => [
        { at_sec: 20, iface: b, kind: 'gather' },
        { at_sec: 65, iface: a, kind: 'gather' },
        { at_sec: 110, iface: b, kind: 'gather' },
        { at_sec: 155, iface: a, kind: 'gather' },
      ],
    });
    out.push({
      name: 'graceful shutdown',
      note:
        `empty ${a} first, then take it down — the outage costs nobody anything, ` +
        'because the eviction already moved them',
      build: () => [
        { at_sec: 30, iface: a, kind: 'evict' },
        { at_sec: 60, iface: a, kind: 'off', dur_sec: 60 },
      ],
    });
  }

  out.push({
    name: 'outage',
    note: two
      ? 'every radio down together for 45s — the only state that is a real outage, ' +
        'since one radio down is a roam'
      : 'the radio down for 45s',
    build: () =>
      props.radios.map((iface) => ({ at_sec: 60, iface, kind: 'off' as const, dur_sec: 45 })),
  });
  out.push({
    name: 'flap',
    note:
      `${a} down twice for 40s — silent, so the client discovers it by missing ` +
      'beacons rather than being told',
    build: () => [
      { at_sec: 20, iface: a, kind: 'off', dur_sec: 40 },
      { at_sec: 110, iface: a, kind: 'off', dur_sec: 40 },
    ],
  });
  out.push({
    name: 'deauth storm',
    note:
      'every client on every radio thrown off, four times — announced, so they ' +
      'come back within a second or two',
    build: () =>
      [20, 60, 100, 140].flatMap((at_sec) =>
        props.radios.map((iface) => ({ iface, kind: 'deauth' as const, at_sec })),
      ),
  });
  return out;
});

/** Applying a preset REPLACES the timeline. Merging would produce something
 *  that is neither the preset nor what was there, and which would then have to
 *  be unpicked; replacing is one gesture to undo. */
function applyPreset(p: { build: () => RadioEvent[]; name: string }) {
  events.value = p.build().sort((x, y) => x.at_sec - y.at_sec);
  tail.value = 0;
  err.value = '';
  msg.value = `${p.name} loaded — save to keep it`;
}

function clearAll() {
  events.value = [];
  tail.value = 0;
}

/* -------------------------------------------------------- validity, io -- */

const tooShort = computed(() =>
  events.value.some((e) => e.kind === 'off' && (e.dur_sec ?? 0) < MIN_RADIO_OFF_SEC),
);
const needsTwoRadios = computed(() =>
  events.value.some((e) => e.kind === 'evict' || e.kind === 'gather'),
);
const oneRadioOnly = computed(() => props.radios.length < 2);

/**
 * A herding pattern that loops without ever clearing the population.
 *
 * This is the one thing the population strip existed to show, said once as a
 * note instead of drawn continuously as a row. Gather and evict are LOSSY: a
 * gather merges two populations into one, so after a lap the original split is
 * gone and the second lap does not begin where the first did.
 *
 * Taking every radio down clears it -- every client re-associates by its own
 * preference when they return -- so a pattern with a NO AP band loops honestly
 * and this stays quiet.
 *
 * A note rather than a rendered belief on purpose. Where clients ACTUALLY end
 * up is a measurement, and the event log has it; a strip claiming it every
 * second is a prediction competing with real data, and a client that declines
 * a steer would make it quietly wrong.
 */
const herdsWithoutReset = computed(() => {
  if (!loop.value) return false;
  const herds = events.value.some((e) => e.kind === 'gather' || e.kind === 'evict');
  return herds && blackouts.value.length === 0;
});
/** An event drawn on a radio that is off at that instant has nobody to act on
 *  and no hostapd to act through. Same-lane, so the offending block sits on the
 *  row the mark is on. */
const incoherent = computed(() =>
  events.value.filter(
    (e) =>
      e.kind !== 'off' &&
      events.value.some(
        (o) => o.kind === 'off' && o.iface === e.iface && e.at_sec >= o.at_sec && e.at_sec < endOf(o),
      ),
  ),
);

async function call(path: string, init?: RequestInit) {
  err.value = '';
  msg.value = '';
  busy.value = true;
  try {
    const r = await fetch(path, init);
    const body = await r.json().catch(() => ({}));
    if (!r.ok) {
      err.value = body.error ?? `${r.status}`;
      return null;
    }
    return body;
  } catch (e) {
    err.value = String(e);
    return null;
  } finally {
    busy.value = false;
  }
}

async function load() {
  const body = await call('/api/bridge/pattern');
  const p: Pattern | null = body?.pattern ?? null;
  if (!p) return;
  events.value = [...(p.radios ?? [])].sort((a, b) => a.at_sec - b.at_sec);
  loop.value = p.loop;
  tail.value = p.keys?.[p.keys.length - 1]?.at_sec ?? 0;
}

async function save() {
  // Two clean keyframes bracket the timeline, so the daemon's validator -- which
  // requires a pattern to be a pattern -- sees one. The same shape linkPattern
  // uses so a link pattern can layer over one that owns the rate.
  const pattern: Pattern = {
    name: '__adapter__',
    keys: [
      { at_sec: 0, down: {}, up: {}, ease: 'hold' },
      { at_sec: dur.value, down: {}, up: {}, ease: 'hold' },
    ] as Pattern['keys'],
    loop: loop.value,
    radios: events.value,
  };
  const body = await call('/api/bridge/pattern', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(pattern),
  });
  if (body) msg.value = `saved — ${body.radios} radio event(s) over ${body.dur_sec}s`;
}

async function play() {
  if (await call('/api/bridge/pattern/play', { method: 'POST' })) msg.value = 'playing';
}
async function stop() {
  const body = await call('/api/bridge/pattern/play', { method: 'DELETE' });
  if (body) msg.value = body.note ? `stopped — ${body.note}` : 'stopped';
}

onMounted(load);
watch(() => props.radios.join(','), load);
</script>

<template>
  <section class="adapter">
    <!-- Folded by default. The summary and the transport stay, so a box with
         a pattern is never silent about having one. -->
    <h3 class="head" :class="{ open }" @click="onHeadClick">
      <!-- The rack's own caret, so everything on this page folds one way. -->
      <button
        class="caret" :aria-expanded="open"
        :title="open ? 'Collapse' : 'Show the timeline'"
        @click="open = !open"
      >{{ open ? '▾' : '▸' }}</button>
      Pattern
      <span class="meta">{{ summary }}</span>
      <span class="spacer"></span>
      <button v-if="open" class="ghost" :disabled="busy || playing" @click="save()">save</button>
      <button v-if="playing" class="ghost" :disabled="busy" @click="stop()">stop</button>
      <button
        v-else class="primary" :disabled="busy || !events.length" @click="play()"
      >play</button>
      <button class="ghost" @click="open = !open">
        {{ open ? 'close' : events.length ? 'edit' : 'add' }}
      </button>
    </h3>

    <!-- A run reports itself whether or not the editor is open: a radio that is
         off the air must never be something the fold is hiding. -->
    <p v-if="foldedStatus && !open" class="meta folded-status">{{ foldedStatus }}</p>

    <p v-if="open && !radios.length" class="note">This box is serving no radios.</p>

    <div v-else-if="open" class="body">
      <!-- Pre-populated patterns, above the lanes as on a client card. These
           WRITE the timeline; the chips below only reveal an empty lane. -->
      <div class="presets">
        <span class="lead">group</span>
        <span class="seg" role="group" aria-label="lane grouping">
          <button
            class="seg-btn" :class="{ on: groupBy === 'effect' }"
            title="all the off rows together, so an overlap is visible"
            @click="groupBy = 'effect'"
          >by effect</button>
          <button
            class="seg-btn" :class="{ on: groupBy === 'adapter' }"
            title="one radio's rows together, as that radio's story"
            @click="groupBy = 'adapter'"
          >by adapter</button>
        </span>
        <span class="gap"></span>
        <span class="lead">patterns</span>
        <button
          v-for="p in PRESETS" :key="p.name" class="preset"
          :disabled="playing" :title="p.note" @click="applyPreset(p)"
        >{{ p.name }}</button>
        <button
          class="preset" :disabled="playing || !events.length"
          title="empty every lane" @click="clearAll()"
        >clear</button>
      </div>

      <div class="stackwrap">
        <div
          ref="stackEl" class="stack"
          @pointermove="onDrag" @pointerup="endDrag" @pointercancel="endDrag"
        >
          <!-- Every radio down at once: the only state that is a real outage. -->
          <div
            v-for="(b, i) in blackouts" :key="`b${i}`" class="blackout"
            :style="{
              left: pct(b[0]),
              width: widthPct(b[1] - b[0]),
              height: `${lanes.length * 40}px`,
            }"
          ><span>NO AP</span></div>

          <div
            v-for="l in lanes" :key="`${l.iface}/${l.kind}`"
            class="lane" :class="`k-${l.kind}`"
            :title="playing ? '' : `double-click or right-click to add a ${l.kind} on ${l.iface}`"
            @dblclick.prevent.stop="add(l.iface, l.kind, $event)"
            @contextmenu.prevent.stop="add(l.iface, l.kind, $event)"
          >
            <svg :viewBox="`0 0 1000 ${VB}`" preserveAspectRatio="none">
              <path :d="lanePath(l.iface, l.kind)" class="line" vector-effect="non-scaling-stroke" />
            </svg>

            <!-- A hit area, not a drawn object. The step path is the render;
                 this is what you grab, and its edges carry the same hollow
                 diamond the ruler uses. -->
            <div
              v-for="{ e, i } in laneEvents(l.iface, l.kind)" :key="i"
              class="hit"
              :style="{ left: pct(e.at_sec), width: widthPct(visSec(e)) }"
              :title="e.kind === 'off'
                ? `${l.iface} off the air ${e.at_sec}s–${e.at_sec + (e.dur_sec ?? 0)}s`
                : `${l.kind} on ${l.iface} at ${e.at_sec}s`"
              @pointerdown.stop="startDrag(i, 'move', $event)"
              @dblclick.prevent.stop="remove(i)"
              @contextmenu.prevent.stop="remove(i)"
            >
              <span
                v-if="e.kind === 'off'" class="kf l" title="drag the rising edge"
                @pointerdown.stop="startDrag(i, 'resizeL', $event)"
              ></span>
              <span
                v-if="e.kind === 'off'" class="kf r" title="drag the falling edge"
                @pointerdown.stop="startDrag(i, 'resize', $event)"
              ></span>
              <span v-else class="kf l"></span>
              <!-- The same escape hatch a link block has: both gestures delete,
                   and a visible control means neither has to be discovered. -->
              <button v-if="!playing" class="hitx" title="remove" @click.stop="remove(i)">×</button>
            </div>

            <span class="lane-name" :title="l.hint">
              {{ l.iface }} <em>{{ l.kind }}</em>
            </span>
          </div>

          <!-- Time is a lane, not a control beside the picture: inside the
               stack it shares the x-axis with every radio row, so dragging it
               moves the playhead exactly as far as the cursor travels. Named
               and read out like any other lane. -->
          <div
            class="lane timelane" :class="{ grab: !run }"
            :tabindex="run ? -1 : 0" role="slider" aria-label="time"
            :aria-valuemin="0" :aria-valuemax="Math.round(dur)"
            :aria-valuenow="Math.round(head * 10) / 10"
            @pointerdown.stop="startScrub"
            @pointermove="onScrub"
            @pointerup="endScrub"
            @pointercancel="endScrub"
            @keydown="keyScrub"
          >
            <span v-for="t in ticks" :key="t" class="tick" :style="{ left: pct(t) }"></span>
            <span class="lane-name">time <b class="num">{{ head.toFixed(1) }}s</b></span>
          </div>

          <!-- Everything past the last event is ruler, not pattern. Shaded so
               the two are never confused: a loop restarts at the end marker, and
               the space after it is somewhere to drag into, not silence that
               gets played. Drag the marker to leave a run-out. -->
          <div class="beyond" :style="{ left: pct(dur) }"></div>
          <div class="endline" :style="{ left: pct(dur) }"></div>
          <span
            class="endgrip" :style="{ left: pct(dur) }"
            :title="playing ? '' : 'drag to leave a quiet run-out after the last event'"
            @pointerdown.stop="startDrag(-1, 'end', $event)"
          ></span>

          <div class="playhead" :style="{ left: pct(head) }"></div>
        </div>
      </div>

      <!-- One chip per EFFECT, not per radio: adding deauth opens an EMPTY
           deauth lane on every adapter at once. -->
      <div v-if="addChips.length || dropChips.length" class="head-row chips">
        <button
          v-for="c in addChips" :key="`+${c.kind}`" class="more" :disabled="playing"
          :title="`open an empty ${c.kind} lane on each of ${radios.join(' and ')} — ${c.hint}`"
          @click="revealed = new Set(revealed).add(c.kind)"
        >+ {{ c.kind }}</button>
        <button
          v-for="c in dropChips" :key="`-${c.kind}`" class="more" :disabled="playing"
          title="put these empty lanes away again"
          @click="revealed = new Set([...revealed].filter((k) => k !== c.kind))"
        >− {{ c.kind }}</button>
      </div>

      <p v-if="oneRadioOnly && needsTwoRadios" class="note bad">
        This pattern moves clients between radios, and this box is serving only
        one. There is nowhere to steer to, so it will be refused.
      </p>
      <p v-if="herdsWithoutReset" class="note warnish">
        This pattern moves clients between radios and loops, but never takes
        every radio down — so the second lap starts somewhere the first did not.
        Add an outage on every radio to clear it.
      </p>
      <p v-if="tooShort" class="note bad">
        An outage shorter than {{ MIN_RADIO_OFF_SEC }}s is silent for less time
        than a client takes to notice one. Use <b>deauth</b> for a short,
        announced disturbance.
      </p>
      <p v-for="(e, i) in incoherent" :key="`i${i}`" class="note bad">
        The <b>{{ e.kind }}</b> at {{ e.at_sec }}s is inside an outage on
        {{ e.iface }} — there is nobody on it to act on.
      </p>
      <p v-if="err" class="note bad">{{ err }}</p>
      <p v-else-if="msg" class="note">{{ msg }}</p>
    </div>
  </section>
</template>

<style scoped>
/* Violet sits clear of --down blue and --up orange, which mean DIRECTION
   everywhere else in boa and must not be borrowed for a radio. */
/* No box of its own: the rack wrapper around this IS the fold, so a second
   border here would draw a card inside a card. */
.adapter { --violet: #a78bfa; }

/* The rack's collapsed row, not a panel header: same padding, same gap, same
   weight, so this line sits among the adapters rather than beside them. The
   rule under it appears only when the editor is open, because a folded row has
   nothing to divide it from. */
.head {
  display: flex; align-items: center; gap: 12px; margin: 0;
  padding: 12px 14px; font-size: 14px; font-weight: 600;
}
.head.open { border-bottom: 1px solid var(--line); }
.caret {
  background: none; border: 0; color: var(--ink-faint);
  cursor: pointer; font-size: 13px; line-height: 1; padding: 3px 8px;
  border-radius: 0;
}
.caret:hover { color: var(--ink); }
.head { cursor: pointer; }
.head .meta { font-weight: 400; color: var(--ink-dim); font-size: 12px; }
.folded-status {
  margin: 0; padding: 6px 14px 8px; font-size: 12px; color: var(--warn);
}
.spacer { flex: 1; }
button {
  font: inherit; color: var(--ink); background: var(--panel-2);
  border: 1px solid var(--line); border-radius: 999px; padding: 3px 11px; cursor: pointer;
}
button:disabled { opacity: 0.45; cursor: not-allowed; }
button.ghost { background: transparent; color: var(--ink-dim); }
button.primary { background: var(--down); border-color: var(--down); color: #fff; }

.body { padding: 10px 14px 12px; }

/* Pills rather than plain text, because these WRITE the timeline where a chip
   only reveals an empty row -- the weight says which one changes your work. */
.presets { display: flex; gap: 7px; flex-wrap: wrap; align-items: center; padding-bottom: 10px; }
.presets .lead {
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.08em;
  color: var(--ink-faint); margin-right: 2px;
}
.preset { font-size: 12px; color: var(--ink-dim); }
.preset:hover:not(:disabled) { border-color: var(--ink-faint); color: var(--ink); }

.stackwrap { overflow-x: auto; }
.stack { position: relative; min-width: 620px; }

.presets .gap { width: 10px; }
.seg { display: flex; border: 1px solid var(--line); border-radius: 6px; overflow: hidden; }
.seg-btn {
  padding: 3px 10px; font: inherit; font-size: 12px; color: var(--ink-dim);
  background: var(--panel-2); border: 0; border-left: 1px solid var(--line);
  border-radius: 0; cursor: pointer;
}
.seg-btn:first-child { border-left: 0; }
.seg-btn:hover { color: var(--ink); background: var(--line); }
.seg-btn.on { color: var(--ink); background: var(--line); font-weight: 600; }

.lane { position: relative; height: 40px; border-bottom: 1px solid var(--line-soft); }
.lane:first-of-type { border-top: 1px solid var(--line-soft); }
.lane svg { position: absolute; inset: 0; width: 100%; height: 100%; display: block; }
.line { fill: none; stroke: currentColor; stroke-width: 1.5; }

/* On the plot rather than in a gutter, so every lane starts at the same x as
   the ruler and carries the panel's background -- a step line passing beneath
   must not swallow it. */
.lane-name {
  position: absolute; left: 8px; bottom: 3px; font-size: 11px;
  color: var(--ink-faint); pointer-events: none; z-index: 5;
  background: var(--panel); padding: 0 4px; border-radius: 3px;
}
.lane-name em {
  font-style: normal; font-family: var(--mono); font-weight: 600; color: var(--ink-dim);
}

/* Transparent, tinting on hover -- the same object PatternPanel's .linkblock
   is. The step path is the render; this is only what you grab. */
.hit {
  position: absolute; top: 6px; bottom: 6px; min-width: 7px; border-radius: 3px;
  background: transparent; cursor: grab; z-index: 3;
}
.hit:hover { background: color-mix(in srgb, currentColor 18%, transparent); }

/* The hollow diamond the ruler and the value lanes already use. Inheriting the
   shape rather than inventing a marker is the point: an edge here reads as the
   same kind of thing as a keyframe there. */
.kf {
  position: absolute; top: 50%; width: 9px; height: 9px; margin: -5px 0 0 -5px;
  background: var(--panel); border: 1.5px solid currentColor;
  transform: rotate(45deg); z-index: 4;
}
.kf.l { left: 0; cursor: ew-resize; }
.kf.r { left: 100%; cursor: ew-resize; }

.k-off { color: var(--violet); }
.k-gather { color: var(--ok); }
.k-evict { color: var(--down); }
.k-deauth { color: var(--warn); }

.blackout {
  position: absolute; top: 0; z-index: 1; pointer-events: none;
  border-left: 1px solid var(--bad); border-right: 1px solid var(--bad);
  background: repeating-linear-gradient(
    -45deg, rgba(248, 113, 113, 0.14) 0 6px, rgba(248, 113, 113, 0.04) 6px 12px);
}
.blackout span {
  position: absolute; bottom: 4px; left: 50%; transform: translateX(-50%);
  font-family: var(--mono); font-size: 10px; letter-spacing: 0.1em; color: var(--bad);
  background: var(--bg); padding: 0 6px; border-radius: 3px;
}

.timelane {
  height: 22px; background: var(--panel-2); border-radius: 0 0 5px 5px;
  border-bottom: 0; cursor: default;
}
.timelane.grab { cursor: ew-resize; }
.timelane:focus-visible { outline: 2px solid var(--down); outline-offset: -2px; }
.tick { position: absolute; top: 0; width: 1px; height: 5px; background: var(--line); }
.num { font-family: var(--mono); font-variant-numeric: tabular-nums; }

/* The same escape hatch .linkx gives a link block: hidden until the shape is
   hovered, so it never competes with the line it sits on. */
.hitx {
  position: absolute; top: -7px; right: -6px; width: 14px; height: 14px;
  padding: 0; line-height: 11px; font-size: 11px; border-radius: 50%;
  border: 1px solid var(--line); background: var(--panel); color: var(--ink-dim);
  cursor: pointer; display: none;
}
.hit:hover .hitx { display: block; }

.beyond {
  position: absolute; top: 0; right: 0; bottom: 0;
  background: rgba(0, 0, 0, 0.22); pointer-events: none; z-index: 2;
}
.endline {
  position: absolute; top: 0; bottom: 0; width: 1px;
  background: var(--line); pointer-events: none; z-index: 2;
}
.endgrip {
  position: absolute; bottom: 0; width: 13px; height: 24px; margin-left: -6px;
  cursor: ew-resize; z-index: 6;
}
.endgrip::after {
  content: ''; position: absolute; left: 5px; top: 7px;
  width: 3px; height: 10px; background: var(--ink-faint); border-radius: 2px;
}
.endgrip:hover::after { background: var(--ink-dim); }

.playhead {
  position: absolute; top: 0; bottom: 0; width: 1px;
  background: var(--warn); pointer-events: none; z-index: 7;
}

/* Lifted from PatternPanel: plain text, no pill. A chip is a disclosure, not an
   action, and a border made it compete with save and play. */
.head-row { display: flex; gap: 8px; align-items: center; margin-top: 10px; }
.head-row.chips { flex-wrap: wrap; gap: 4px 10px; }
.more {
  align-self: start; margin-top: 2px; padding: 1px 0;
  font: inherit; font-size: 11px; color: var(--ink-faint);
  background: none; border: 0; cursor: pointer;
}
.more:hover:not(:disabled) { color: var(--ink-dim); }
.more:disabled { opacity: 0.5; cursor: not-allowed; }

.note { margin: 8px 0 0; font-size: 12px; color: var(--ink-dim); }
.note.bad { color: var(--bad); }
.note.warnish { color: var(--warn); }
</style>
