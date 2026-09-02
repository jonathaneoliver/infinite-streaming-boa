<script setup lang="ts">
/**
 * The timeline editor.
 *
 * # Every field is its own timeline
 *
 * Each field -- rate, delay, jitter, loss, reorder, corrupt (and the link
 * lanes: drop/nudge/deadzone) -- has its OWN transitions, edited on its own
 * lane. Dragging rate's transition never moves delay's and is never blocked by
 * it: the lanes are independent automation tracks, the way the link lanes have
 * always been.
 *
 * The daemon, though, still plays a single list of keyframes, each carrying
 * every field at one instant. So the editor DECOMPOSES the stored keyframes
 * into per-field step timelines (`lib/pattern`), edits those, and RECOMPOSES
 * them -- the union of every field's transition times, each keyframe re-built
 * with all fields -- before handing the pattern back. The daemon is unchanged;
 * the recompose is lossless for the steps this editor authors.
 *
 * Vertical drag on a lane sets that field's value at that moment; horizontal
 * drag on a marker moves that field's transition in time. The card's sliders
 * set a whole moment (every field at the selected time), which is why selection
 * is a TIME rather than a keyframe index -- a recompose renumbers keyframes,
 * but a time is stable.
 *
 * # Why every lane is a step
 *
 * Keyframes are absolute values at absolute timestamps. The daemon can
 * interpolate and the stored format carries the mode, but the editor authors
 * steps only, because nobody has yet measured whether changing a netem rate
 * mid-flight disturbs the queue. Until that is known on hardware, a smooth
 * ramp would be a picture of a link that this box cannot prove it delivers.
 *
 * The panel is a pure view over `pattern`: every edit emits the complete next
 * timeline and the card owns it.
 */
import { computed, ref, watch } from 'vue';
import type { Keyframe, Ladder, LinkEvent, Pattern, PatternView, Shape } from '@/types';
import { EXTRA_IMPAIRMENTS, PATTERN_TEMPLATES, RATE_MAX, posToRate, rateToPos } from '@/types';
import {
  addStep,
  decomposeAll,
  durOf,
  moveStep,
  recompose,
  removeStep,
  setStepValue,
  snap as snapSec,
  type FieldKey,
  type Step,
  type Timelines,
} from '@/lib/pattern';
import { useExtras } from '@/composables/useExtras';
import PatternLibrary from './PatternLibrary.vue';

const props = defineProps<{
  pattern: Pattern | null;
  /** Identity, so the library can list and select against this device. */
  mac: string;
  rev: number;
  /** The device's ladders, so the library can say which one a built-in
   *  describes and let the operator pick when there is more than one. */
  ladders: Ladder[];
  run?: PatternView;
  /** Which MOMENT (seconds) the card's sliders are editing, if any. A time
   *  rather than a keyframe index because a recompose renumbers keyframes. */
  selectedSec: number | null;
  /** False when the device cannot be conditioned, with why in `blocked`. */
  canPlay: boolean;
  blocked: string;
}>();
const emit = defineEmits<{
  update: [Pattern];
  create: [];
  remove: [];
  play: [];
  stop: [];
  select: [number | null];
  /** The library loaded a different pattern -- the card must drop any pending
   *  optimistic edit-draft, or a stuck draft would mask the new selection. */
  changed: [];
}>();

const running = computed(() => props.run?.state === 'running');
const paused = computed(() => props.run?.state === 'paused');

/*
 * The editor is folded away until asked for.
 *
 * Closing it also drops the keyframe selection. The sliders above are the
 * keyframe editor, so leaving one selected behind a folded panel would leave
 * them silently pointed at a moment in a timeline the operator can no longer
 * see -- they would go to change this device's settings and edit a keyframe
 * instead, with the only explanation hidden.
 */
const open = ref(false);
function setOpen(v: boolean) {
  open.value = v;
  if (!v) emit('select', null);
}

/**
 * The whole header toggles the fold, not just the button on the end.
 *
 * The same two exclusions as the card title, and for the same reasons -- see
 * onHeadClick in ClientCard, which this deliberately mirrors rather than
 * inventing a second behaviour for a second header:
 *
 * Clicks on something interactive. play, stop and the fold button all live in
 * this header; swallowing their clicks would break them, and toggling the fold
 * as well as playing would be worse.
 *
 * Clicks that conclude a text selection. Dragging across the summary to copy a
 * duration ends in a click on the header, and folding the panel away underneath
 * that would be infuriating.
 *
 * The button stays. It is the keyboard route and it names the action, which a
 * click target alone does not.
 */
function onHeadClick(e: MouseEvent) {
  const el = e.target as HTMLElement | null;
  if (el?.closest('input, button, a, select, textarea')) return;
  if ((window.getSelection()?.toString() ?? '').length > 0) return;
  setOpen(!open.value);
}

/** What the folded header says, so a pattern is never invisible. */
const summary = computed(() => {
  if (!props.pattern) return 'no timeline — a fixed cap only tests steady state';
  const n = props.pattern.keys.length;
  return `${props.pattern.name} · ${dur.value.toFixed(0)}s · ${n} keyframe${n === 1 ? '' : 's'}${
    props.pattern.loop ? ' · loops' : ''
  }`;
});
const keys = computed<Keyframe[]>(() => props.pattern?.keys ?? []);
const dur = computed(() => (keys.value.length ? keys.value[keys.value.length - 1].at_sec : 0));

/*
 * The ruler runs past the end of the pattern.
 *
 * The last keyframe IS the end -- the loop point, and where a one-shot run
 * stops -- so without headroom it sits at 100% of the width and there is
 * nowhere to drag it TO. The timeline could only ever be shortened by dragging,
 * and lengthening needed a number field.
 *
 * Thirty seconds of empty ruler fixes that: drag the last keyframe into it and
 * the pattern grows, click into it and a keyframe lands past the old end. The
 * headroom is shaded so it never reads as part of the pattern -- nothing
 * happens out there until something is put there.
 */
/** What the runtime will play. Mirrors maxPatternSec in pattern.go. */
const maxPatternSec = 3600;

/*
 * How much empty ruler sits past the end, as a fraction of the pattern plus a
 * floor.
 *
 * It was a flat 30 seconds, which is most of a short pattern and nothing at all
 * on a long one: on the 11-minute ladder patterns the generator produces, 30s
 * is four percent of the width, so "drag the end into the headroom to lengthen
 * it" meant dragging into a strip a few pixels wide and releasing to get
 * another few pixels. The space exists to be dragged into, so it scales with
 * what is being dragged.
 */
const HEADROOM_MIN_SEC = 30;
const HEADROOM_FRAC = 0.25;
const viewSpan = computed(
  () => dur.value + Math.max(HEADROOM_MIN_SEC, dur.value * HEADROOM_FRAC),
);

/*
 * What the ruler is actually drawn against, which is not viewSpan mid-drag.
 *
 * The headroom regrows the moment the pattern gets longer, which is what makes
 * it always there to drag into again. Letting it regrow DURING the drag would
 * be the wrong kind of helpful: the axis would stretch under the marker as the
 * marker moved along it, so the keyframe would fall behind the cursor and the
 * lanes would creep leftwards on every pointer event. Worse, "seconds per
 * pixel" would stop being constant, so the same hand movement would mean less
 * time the further right you got.
 *
 * So the scale is pinned for the length of a gesture and the headroom is
 * consumed as you push into it. It comes back in one step on release. Past the
 * pinned span the marker sits at the right edge while the end label keeps
 * counting up, which is honest: it is still moving, there is just no more ruler
 * to show it on until the drag finishes.
 */
const renderSpan = computed(() =>
  fieldDrag.value ? Math.max(fieldDrag.value.span, dur.value) : viewSpan.value,
);

/*
 * The playhead has two owners, and which one is speaking must never be
 * ambiguous. While a run exists it is the daemon's: the panel draws where
 * playback actually is, because a playhead showing the operator's last scrub
 * during playback would put the cap change at the wrong second, and lining a
 * cap change up against a player's reaction is the entire point. Otherwise it
 * is a scrub position, local to this browser.
 */
const scrub = ref(0);
const head = computed(() => (props.run ? props.run.pos_sec : scrub.value));

/*
 * Scrubbing does not enforce anything.
 *
 * Dragging the playhead while authoring writes no policy and touches no
 * kernel: building a five-keyframe pattern would otherwise issue several
 * hundred tc changes at a device nobody is testing yet.
 */
function seek(sec: number) {
  scrub.value = sec;
  // Landing on a moment that carries a transition points the sliders at it: the
  // time control chooses which instant they are editing. A time with nothing
  // keyed on any lane hands the sliders back to the stored policy.
  emit('select', hasKeyAt(sec) ? sec : null);
}

/** Whether any lane has a transition exactly at this second. */
function hasKeyAt(sec: number): boolean {
  return keys.value.some((k) => Math.abs(k.at_sec - sec) < 1e-6);
}

/** Clicking a selected moment again hands the sliders back to the policy. */
function toggleKeyAt(sec: number) {
  if (suppressClick) {
    suppressClick = false;
    return;
  }
  scrub.value = sec;
  emit('select', props.selectedSec === sec ? null : sec);
}

/*
 * Right-click the stack just moves the playhead there.
 *
 * A transition is added on the lane it belongs to (right-click a lane) or by
 * moving a slider at the selected moment, so the stack-level right-click no
 * longer plants a keyframe -- there is no single "keyframe" to plant when each
 * field is its own timeline.
 */
function ctxTimeline(e: MouseEvent) {
  if (props.run || dur.value <= 0) return;
  seek(timeAt(e.clientX, viewSpan.value));
}

// A run takes the sliders over, so nothing can be selected for editing while
// one is playing: the controls are then reporting what is enforced, not
// accepting input, exactly as they do during a sweep.
watch(
  () => props.run?.state,
  (s) => {
    if (s === 'running') emit('select', null);
  },
);

/** The selected moment, when it still lands on a transition. */
const selSec = computed(() =>
  props.selectedSec != null && hasKeyAt(props.selectedSec) ? props.selectedSec : null,
);

function mutate(fn: (p: Pattern) => void) {
  if (!props.pattern) return;
  const next = JSON.parse(JSON.stringify(props.pattern)) as Pattern;
  fn(next);
  next.keys.sort((a, b) => a.at_sec - b.at_sec);
  emit('update', next);
}

/*
 * The editor's own per-field working state.
 *
 * The daemon plays a recomposed keyframe list, which prunes any keyframe where
 * no field changes -- so a marker placed but not yet shaped (its value still
 * equal to its neighbour) would vanish on the round trip. Holding the step
 * timelines here keeps it: the marker lives in `working`, and the pruned
 * keyframes are only what we emit to the daemon.
 */
const working = ref<Timelines>(decomposeAll(props.pattern?.keys ?? []));

const SHAPE_JSON_FIELDS = [
  'rate_mbps', 'delay_ms', 'jitter_ms', 'loss_pct', 'loss_burst', 'reorder_pct', 'corrupt_pct',
] as const;

function sameKeys(a: Keyframe[], b: Keyframe[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i].at_sec !== b[i].at_sec) return false;
    for (const d of ['down', 'up'] as const)
      for (const f of SHAPE_JSON_FIELDS)
        if ((a[i][d][f] ?? 0) !== (b[i][d][f] ?? 0)) return false;
  }
  return true;
}

// Re-derive working only on a GENUINE external change (a load, a template): the
// echo of our own edit round-trips to exactly what working recomposes to, and
// re-deriving from that would drop the unshaped markers the prune removed.
watch(
  () => props.pattern?.keys,
  () => {
    const incoming = props.pattern?.keys ?? [];
    if (!sameKeys(recompose(working.value, durOf(props.pattern)), incoming)) {
      working.value = decomposeAll(incoming);
    }
  },
  { deep: true },
);

/** The step timeline for a lane in the direction on screen. */
function laneSteps(lane: FieldKey): Step[] {
  return working.value[dir.value][lane];
}

/** The latest transition on any lane -- the natural end before any trailing hold. */
function latestTransition(): number {
  let latest = 0;
  for (const d of ['down', 'up'] as const)
    for (const f of SHAPE_JSON_FIELDS)
      for (const s of working.value[d][f]) latest = Math.max(latest, s.at_sec);
  return latest;
}

/** Recompose the working timelines and emit them as the pattern's keyframes.
 *  The end holds at the current length unless a transition ran past it. */
function commitWorking() {
  if (!props.pattern) return;
  const end = Math.max(latestTransition(), durOf(props.pattern));
  emit('update', { ...props.pattern, keys: recompose(working.value, end) });
}

const stackEl = ref<HTMLElement | null>(null);

/**
 * Where on the timeline a screen x lands, quantised to the keyframe grid.
 *
 * `openEnd` lets a position run past the right edge. Dragging the LAST keyframe
 * is the case: the ruler's headroom gives it somewhere to go, but stopping dead
 * at the end of that would cap one gesture at +30s for no reason the operator
 * can see. Pulling further right keeps adding time at the same seconds per
 * pixel as the rest of the ruler, so the gesture stays predictable.
 */
function timeAt(clientX: number, spanSec: number, openEnd = false): number {
  const box = stackEl.value?.getBoundingClientRect();
  if (!box || spanSec <= 0) return 0;
  let f = (clientX - box.left) / box.width;
  f = Math.max(f, 0);
  if (!openEnd) f = Math.min(f, 1);
  return Math.round(f * spanSec * 2) / 2;
}

/**
 * Clicking the timeline moves the playhead.
 *
 * The range control underneath does the same thing, but nobody looks for a
 * scrollbar-shaped control to position a playhead they can see.
 */
function clickTimeline(e: MouseEvent) {
  if (props.run || dur.value <= 0) return;
  seek(timeAt(e.clientX, viewSpan.value));
}

/*
 * Dragging a field's transition along its own lane.
 *
 * Per field: moving rate's transition never moves delay's and is never blocked
 * by it -- each lane is its own timeline. The span is pinned for the gesture
 * (see renderSpan) so the marker does not chase the cursor as the pattern
 * grows. moveStep does the clamping: a field's transition cannot cross its own
 * neighbours, its first step (the value at 0) is pinned, and dragging its last
 * transition into the headroom grows the pattern -- default ripples this
 * field's later transitions, alt slides one within its own gap.
 */
const fieldDrag = ref<{
  lane: FieldKey;
  i: number;
  span: number;
  x0: number;
  moved: boolean;
  /** alt held when the press landed: slide within the gap, do not ripple. */
  penned: boolean;
} | null>(null);

/*
 * How far the pointer must travel before a press counts as a drag.
 *
 * Without it every click is a one-pixel drag: the press lands, the mouse
 * twitches, and the transition moves to whatever half second that pixel rounds
 * to. Worse, the drag then swallows the click that was meant to select it.
 */
const DRAG_SLOP_PX = 3;

function startFieldDrag(lane: FieldKey, i: number, e: PointerEvent) {
  // Left button only, and never the pinned first marker: a right-click here is
  // a delete, and letting it capture the pointer would swallow that.
  if (props.run || i === 0 || e.button !== 0) return;
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* no active pointer; the drag still tracks via bubbled events */
  }
  fieldDrag.value = { lane, i, span: viewSpan.value, x0: e.clientX, moved: false, penned: false };
}

function onFieldDrag(e: PointerEvent) {
  const d = fieldDrag.value;
  if (!d) return;
  const steps = laneSteps(d.lane);
  const cur = steps[d.i];
  // The step can go away underneath a drag (a template replacing the pattern);
  // stop rather than write to undefined.
  if (!cur) return;
  if (!d.moved && Math.abs(e.clientX - d.x0) < DRAG_SLOP_PX) return;
  if (!d.moved) emit('select', cur.at_sec);
  d.moved = true;
  // Only a field's last transition may run past the ruler.
  const at = timeAt(e.clientX, d.span, d.i === steps.length - 1);
  if (at !== cur.at_sec) {
    // Always clamp between this field's own neighbours: a marker never crosses
    // the one to its left or right.
    moveStep(steps, d.i, at, true, maxPatternSec);
    commitWorking();
    scrub.value = at;
  }
}

function endFieldDrag(e: PointerEvent) {
  if (!fieldDrag.value) return;
  try {
    (e.target as HTMLElement).releasePointerCapture(e.pointerId);
  } catch {
    /* nothing was captured */
  }
  if (fieldDrag.value.moved) suppressClick = true;
  fieldDrag.value = null;
}

let suppressClick = false;

/**
 * Add a transition to a lane at the clicked time, holding the value there so
 * adding one changes nothing until it is shaped -- the way the link lanes add
 * on click. It survives in `working` even though the daemon keyframes prune it.
 */
function addField(lane: FieldKey, e: MouseEvent) {
  if (props.run) return;
  const at = timeAt(e.clientX, renderSpan.value);
  if (at <= 0) return; // 0 is the field's starting value, always present
  addStep(working.value[dir.value][lane], at);
  commitWorking();
  emit('select', at);
}

/** Remove a field's transition. Its step at 0 is the starting value and stays. */
function removeField(lane: FieldKey, i: number) {
  if (props.run || i === 0) return;
  removeStep(working.value[dir.value][lane], i);
  commitWorking();
  emit('select', null);
}

/*
 * Dragging a lane up and down sets the value held at that moment.
 *
 * Which keyframe it edits is not a choice the operator has to make: a lane is
 * flat between keyframes because the value IS held, so the keyframe governing a
 * point is the last one at or before it. Pressing on a segment therefore has
 * exactly one meaning.
 *
 * The scale is pinned for the gesture, for the same reason the time span is
 * during a horizontal drag. Three of the lanes scale to the tallest value the
 * pattern reaches, so dragging the tallest value would move the ceiling it is
 * being measured against -- the line would chase the cursor and never arrive.
 */
const vdrag = ref<{
  lane: LaneKey;
  /** The governing step's time -- an existing transition, the one whose value
   *  this segment holds. Editing it changes the segment, not the moment pressed. */
  at: number;
  ceil: number;
  y0: number;
  /** Where the value sat when the press landed, 0 at the floor and 1 at the top. */
  n0: number;
  moved: boolean;
} | null>(null);

/*
 * The ceiling a flat-zero lane is dragged against.
 *
 * A lane whose values are all zero has no scale of its own, so there would be
 * nothing to drag against and the gesture would do nothing at all -- which is
 * exactly the case of adding delay to a pattern that has none. These are
 * starting scales, not limits: once a lane holds any value it scales to the
 * pattern like the others, and the sliders still reach the full range.
 */
const LANE_START_CEIL: Record<LaneKey, number> = {
  rate_mbps: 0, // exponential, and never flat: unlimited draws at the top
  delay_ms: 200,
  jitter_ms: 200,
  loss_pct: 5,
  // Well under each slider's maximum, for the same reason the others are: this
  // is the scale a lane is dragged against before it holds anything, so a
  // ceiling at the top of the range would put every useful value in the bottom
  // tenth of the lane. Reorder is worth having in whole percents; corrupt bites
  // at a fraction of one.
  reorder_pct: 25,
  corrupt_pct: 5,
};

/** The time of the step whose value this lane holds at x -- the transition at
 *  or before it, so a vertical drag edits the segment rather than adding one. */
function stepGoverning(lane: LaneKey, clientX: number): number {
  const t = timeAt(clientX, renderSpan.value);
  const steps = laneSteps(lane);
  let at = steps.length ? steps[0].at_sec : 0;
  for (const s of steps) if (s.at_sec <= t) at = s.at_sec;
  return at;
}

/**
 * Where a value sits in its lane at the moment a drag begins.
 *
 * Mirrors norm(), but against the ceiling PINNED for the gesture rather than
 * the live one, so the starting point cannot move as the value does.
 */
function normOf(v: number, lane: LaneKey, ceil: number): number {
  if (lane === 'rate_mbps') return v <= 0 ? 1 : rateToPos(v) / 100;
  return ceil > 0 ? Math.min(Math.max(v / ceil, 0), 1) : 0;
}

/**
 * How far a vertical movement shifts a value, as a fraction of the lane.
 *
 * RELATIVE to where the press landed, not the cursor's absolute height. Reading
 * the absolute position meant the value jumped to wherever the pointer happened
 * to be the instant the drag registered -- and since the line is what you aim
 * at, pressing on a rate near the top of its lane snapped it to the 200 Mbps
 * ceiling before the cursor had moved a millimetre. A drag should nudge the
 * value it grabbed, not replace it.
 *
 * The divisor is the lane's usable height in pixels: yOf spends VB-14 of the
 * viewBox's VB units on the value, so the same fraction of the rendered box.
 */
function normDelta(dyPx: number, el: HTMLElement): number {
  const box = el.getBoundingClientRect();
  const usable = (box.height * (VB - 14)) / VB;
  if (usable <= 0) return 0;
  return dyPx / usable;
}

function startVDrag(lane: LaneKey, e: PointerEvent) {
  if (props.run || e.button !== 0) return;
  const at = stepGoverning(lane, e.clientX);
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* no active pointer; the drag still tracks via bubbled events */
  }
  // Twice the current max (or the start ceiling) so there is always headroom to
  // drag UP -- a lane whose largest value is 1 ms must still reach further than
  // 1 ms in one gesture. The dragged lane renders against this pinned ceiling
  // (see norm) so the marker tracks the cursor.
  const ceil = Math.max(laneMax(lane) * 2, LANE_START_CEIL[lane]);
  const cur = laneSteps(lane).find((s) => Math.abs(s.at_sec - at) < 1e-9);
  vdrag.value = {
    lane, at, ceil,
    y0: e.clientY,
    n0: normOf(cur?.value ?? 0, lane, ceil),
    moved: false,
  };
}

function onVDrag(e: PointerEvent) {
  const d = vdrag.value;
  if (!d) return;
  // The same slop as the horizontal drag, and for the same reason: without it
  // every click on a lane is a one-pixel drag that nudges a value on the way to
  // moving the playhead, and neither action is what was asked for.
  if (!d.moved && Math.abs(e.clientY - d.y0) < DRAG_SLOP_PX) return;
  if (!d.moved) emit('select', d.at);
  d.moved = true;
  // Up is positive: the screen's y grows downward and a value grows upward.
  const n = d.n0 + normDelta(d.y0 - e.clientY, e.currentTarget as HTMLElement);
  setLaneValue(d.lane, d.at, Math.min(Math.max(n, 0), 1), d.ceil);
}

function endVDrag(e: PointerEvent) {
  if (!vdrag.value) return;
  try {
    (e.target as HTMLElement).releasePointerCapture(e.pointerId);
  } catch {
    /* nothing was captured */
  }
  // A drag ends in a click on the lane, which would otherwise move the playhead
  // to wherever the gesture happened to finish.
  if (vdrag.value.moved) suppressClick = true;
  vdrag.value = null;
}

// A value lane carries two gestures -- vertical to set a value, horizontal on a
// marker to move a transition -- and a captured marker drag bubbles its moves to
// the lane. One dispatcher runs both handlers; each is a no-op unless its own
// drag is live, so they never fight.
function onLaneMove(e: PointerEvent) {
  onVDrag(e);
  onFieldDrag(e);
}
function onLaneUp(e: PointerEvent) {
  endVDrag(e);
  endFieldDrag(e);
}

/**
 * Writes one field's value at one time from a lane position.
 *
 * The clamps are the daemon's own, applied here so a drag cannot compose a
 * policy the box will refuse -- a slider that silently produces rejected writes
 * is worse than one that cannot reach the value. The cross-field rules
 * (jitter <= delay, reorder needs delay) are enforced by the recompose's
 * sanitize, since delay and jitter are now independent timelines.
 */
function setLaneValue(lane: LaneKey, at: number, n: number, ceil: number) {
  if (!props.pattern) return;
  let value: number;
  if (lane === 'rate_mbps') {
    // Never 0 from a drag. Zero means unlimited, drawn at the TOP of the lane
    // while the slider puts it at the bottom, so letting the floor produce it
    // would send the line leaping to the ceiling. Clearing a cap is the
    // slider's job.
    value = posToRate(Math.max(1, Math.round(n * 100)));
  } else if (lane === 'loss_pct') {
    value = Math.round(Math.min(n * ceil, 20) * 10) / 10;
  } else {
    value = Math.round(Math.min(n * ceil, 10000));
  }
  setStepValue(working.value[dir.value][lane], at, value);
  commitWorking();
}

/*
 * Link lanes: drop / nudge / deadzone as on/off blocks.
 *
 * Unlike the value lanes there is no vertical axis -- a block is simply present
 * or not (0/1). The width is the DURATION: 0 (a thin pulse block) fires once on
 * the rising edge; a wider block holds the disturbance for that long -- a flap
 * for drop/nudge, a clean outage for deadzone. So every lane edits the same
 * way: click to add, drag to move, drag the right edge to lengthen. See #135.
 */
const LINK_LANES: { kind: LinkEvent['kind']; label: string }[] = [
  { kind: 'drop', label: 'drop' },
  { kind: 'nudge', label: 'nudge' },
  { kind: 'deadzone', label: 'deadzone' },
];
const PULSE_VIS_SEC = 0.5; // a zero-duration pulse still needs a grabbable width
const DEFAULT_DEADZONE_SEC = 10;

const links = computed<LinkEvent[]>(() => props.pattern?.links ?? []);

function laneEvents(kind: string): { ev: LinkEvent; i: number }[] {
  return links.value.map((ev, i) => ({ ev, i })).filter((x) => x.ev.kind === kind);
}
function evVisSec(ev: LinkEvent): number {
  const d = ev.dur_sec ?? 0;
  return d > 0 ? d : PULSE_VIS_SEC;
}
function evLeftPct(ev: LinkEvent): number {
  return renderSpan.value > 0 ? (ev.at_sec / renderSpan.value) * 100 : 0;
}
function evWidthPct(ev: LinkEvent): number {
  return renderSpan.value > 0 ? (evVisSec(ev) / renderSpan.value) * 100 : 0;
}
// A 0/1 square wave for a link lane, drawn exactly like the value lanes'
// step path so the whole stack reads as one instrument: low, rise at a block's
// start, hold high for its width, fall at its end.
function linkLanePath(kind: string): string {
  if (renderSpan.value <= 0 || dur.value <= 0) return '';
  const hi = 8;
  const lo = VB - 6;
  const end = xOf(dur.value); // stop at the loop point, like the value lanes
  const evs = laneEvents(kind)
    .map((x) => x.ev)
    .slice()
    .sort((a, b) => a.at_sec - b.at_sec);
  const d = [`M 0,${lo}`];
  for (const ev of evs) {
    const a = xOf(ev.at_sec);
    if (a >= end) continue; // an event past the end never plays
    const b = Math.min(xOf(ev.at_sec + evVisSec(ev)), end); // clamp to the end
    d.push(`L ${a.toFixed(1)},${lo}`, `L ${a.toFixed(1)},${hi}`, `L ${b.toFixed(1)},${hi}`, `L ${b.toFixed(1)},${lo}`);
  }
  d.push(`L ${end.toFixed(1)},${lo}`);
  return d.join(' ');
}

function addLink(kind: LinkEvent['kind'], e: MouseEvent) {
  if (props.run) return;
  const at = timeAt(e.clientX, renderSpan.value);
  const width = kind === 'deadzone' ? DEFAULT_DEADZONE_SEC : PULSE_VIS_SEC;
  // Refuse to stack a block on an existing one of the same kind: overlapping
  // blocks pile up into something you can neither read nor click to delete.
  const clash = (props.pattern?.links ?? []).some(
    (l) => l.kind === kind && at < l.at_sec + evVisSec(l) && at + width > l.at_sec,
  );
  if (clash) return;
  mutate((p) => {
    const ev: LinkEvent = { at_sec: at, kind };
    if (kind === 'deadzone') ev.dur_sec = DEFAULT_DEADZONE_SEC;
    p.links = [...(p.links ?? []), ev];
  });
}
function removeLink(i: number) {
  mutate((p) => {
    p.links = (p.links ?? []).filter((_, j) => j !== i);
  });
}

// Horizontal drag: move a block, or resize its right edge. Kept in insertion
// order (mutate sorts keys, never links) so the index stays stable mid-drag.
type LinkDrag = { i: number; mode: 'move' | 'resizeL' | 'resizeR'; grabSec: number };
const linkDrag = ref<LinkDrag | null>(null);

function startLinkMove(i: number, e: PointerEvent) {
  if (props.run || e.button !== 0) return;
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* older Safari */
  }
  linkDrag.value = { i, mode: 'move', grabSec: timeAt(e.clientX, renderSpan.value) - links.value[i].at_sec };
}
function startLinkResize(i: number, side: 'l' | 'r', e: PointerEvent) {
  if (props.run || e.button !== 0) return;
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* older Safari */
  }
  linkDrag.value = { i, mode: side === 'l' ? 'resizeL' : 'resizeR', grabSec: 0 };
}
function onLinkDrag(e: PointerEvent) {
  const d = linkDrag.value;
  if (!d) return;
  const t = timeAt(e.clientX, renderSpan.value, true);
  mutate((p) => {
    const all = p.links ?? [];
    const ev = all[d.i];
    if (!ev) return;
    // deadzone must stay >= 1s; drop/nudge may shrink to a pulse (0).
    const min = ev.kind === 'deadzone' ? 1 : 0;
    const snap = (x: number) => Math.round(x * 2) / 2; // 0.5s grid
    // Neighbours of the same kind bound the block: an edge or a move can never
    // cross the block before or after it on its own lane.
    const same = all
      .map((x, idx) => ({ x, idx }))
      .filter((o) => o.x.kind === ev.kind)
      .sort((a, b) => a.x.at_sec - b.x.at_sec);
    const pos = same.findIndex((o) => o.idx === d.i);
    const prev = pos > 0 ? same[pos - 1].x : null;
    const next = pos >= 0 && pos < same.length - 1 ? same[pos + 1].x : null;
    const lo = prev ? prev.at_sec + evVisSec(prev) : 0;
    const nextStart = next ? next.at_sec : Infinity;
    if (d.mode === 'move') {
      const width = evVisSec(ev);
      const hi = next ? nextStart - width : Infinity;
      ev.at_sec = Math.max(lo, Math.min(snap(t - d.grabSec), Math.max(lo, hi)));
    } else if (d.mode === 'resizeL') {
      // The rising edge moves, holding the falling edge; it stops at the prev block.
      const endT = ev.at_sec + (ev.dur_sec ?? 0);
      const s = Math.max(lo, Math.min(snap(t), endT - min));
      ev.at_sec = s;
      ev.dur_sec = Math.max(min, snap(endT - s));
    } else {
      // The falling edge moves, holding the start; it stops at the next block.
      const wanted = Math.max(min, snap(t - ev.at_sec));
      const room = nextStart - ev.at_sec;
      ev.dur_sec = Math.min(wanted, room);
    }
  });
}
function endLinkDrag() {
  linkDrag.value = null;
}

/*
 * Scrubbing by dragging the time lane.
 *
 * The playhead used to be positioned by a range input sitting in a flex row
 * with a label before it and a readout and a button after it. Its track
 * therefore covered a different pixel span than the lanes above, so the same
 * cursor movement meant a different number of seconds in the two places and the
 * playhead visibly lagged or ran ahead of the hand. The control and the picture
 * of what it controlled disagreed.
 *
 * The time lane is inside the stack, so it shares the lanes' x-axis by
 * construction rather than by arithmetic anybody has to keep in step.
 */
const scrubbing = ref(false);

function startScrub(e: PointerEvent) {
  if (props.run) return;
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* no active pointer; the drag still tracks via bubbled events */
  }
  scrubbing.value = true;
  seek(timeAt(e.clientX, renderSpan.value));
}

function onScrub(e: PointerEvent) {
  if (!scrubbing.value) return;
  seek(timeAt(e.clientX, renderSpan.value));
}

function endScrub(e: PointerEvent) {
  if (!scrubbing.value) return;
  try {
    (e.target as HTMLElement).releasePointerCapture(e.pointerId);
  } catch {
    /* nothing was captured */
  }
  scrubbing.value = false;
  // Deliberately NOT setting suppressClick. That flag is consumed by
  // toggleKey, and a scrub does not end on a marker -- so arming it here would
  // leave it set and swallow the next keyframe click instead. The trailing
  // click lands on the stack and seeks to where the drag already finished,
  // which changes nothing.
}

/**
 * Keyboard scrubbing, which the range input used to provide for free.
 *
 * Half-second steps because that is the grid keyframes land on; a finer arrow
 * key would move the playhead to positions a keyframe can never occupy.
 */
function keyScrub(e: KeyboardEvent) {
  if (props.run) return;
  const step = e.shiftKey ? 5 : 0.5;
  const at = head.value;
  switch (e.key) {
    case 'ArrowLeft':
      seek(Math.max(0, at - step));
      break;
    case 'ArrowRight':
      seek(Math.min(renderSpan.value, at + step));
      break;
    case 'Home':
      seek(0);
      break;
    case 'End':
      seek(dur.value);
      break;
    default:
      return;
  }
  e.preventDefault();
}

function setLoop(on: boolean) {
  mutate((p) => {
    p.loop = on;
  });
}

function rename(name: string) {
  mutate((p) => {
    p.name = name.trim() || 'pattern';
  });
}

/**
 * Change how long the pattern runs, without adding a transition.
 *
 * The length IS the end of the timeline: the loop point, and where a one-shot
 * run stops. Every field simply holds its last value for longer. Recompose is
 * asked for a longer end; shortening is refused past the latest transition on
 * any lane, which would otherwise silently drop transitions off the end.
 */
function setLength(sec: number) {
  if (!props.pattern || keys.value.length < 2) return;
  const end = Math.max(snapSec(sec), latestTransition());
  emit('update', { ...props.pattern, keys: recompose(working.value, end) });
}

/**
 * Make every field end where it began -- a seamless loop.
 *
 * A loop restarts at t=0, so matching the last value of each field to its first
 * is what a seamless loop IS. Adds a transition at the end of each lane holding
 * a value that differs from its start; nothing else moves.
 */
function closeLoop() {
  if (!props.pattern) return;
  const end = dur.value;
  for (const d of ['down', 'up'] as const)
    for (const f of SHAPE_JSON_FIELDS) {
      const steps = working.value[d][f];
      if (steps.length) setStepValue(steps, end, steps[0].value);
    }
  commitWorking();
}

function useTemplate(name: string) {
  const t = PATTERN_TEMPLATES.find((x) => x.name === name);
  if (t) emit('update', t.make());
}

/*
 * The lanes.
 *
 * All four parameters are drawn, not just the cap. A pattern that raises
 * latency without touching rate is a normal thing to want, and a timeline that
 * only plotted rate would show it as a flat line -- the interface claiming
 * nothing happens while the kernel adds 600 ms.
 *
 * One direction at a time. Eight lanes at once is not a chart anyone reads, and
 * the downlink is what nearly every test here is about.
 */
type LaneKey =
  | 'rate_mbps' | 'delay_ms' | 'jitter_ms' | 'loss_pct'
  | 'reorder_pct' | 'corrupt_pct';

const CORE_LANES: { key: LaneKey; label: string; unit: string }[] = [
  { key: 'rate_mbps', label: 'rate', unit: 'Mbps' },
  { key: 'delay_ms', label: 'delay', unit: 'ms' },
  { key: 'jitter_ms', label: 'jitter', unit: 'ms' },
  { key: 'loss_pct', label: 'loss', unit: '%' },
];

/**
 * The second-tier impairments get a lane only when the pattern uses one.
 *
 * Same rule as the sliders, for the same reason as the note above about eight
 * lanes: a timeline that always drew seven would be unreadable for the patterns
 * that vary one thing, which is most of them. A lane that is flat at zero for
 * the whole run says nothing and costs a quarter of the panel.
 *
 * Derived from the keyframes rather than from a setting, so a pattern that uses
 * reorder always shows reorder, in every session and for whoever opens it.
 */
const EXTRA_LANES: { key: LaneKey; label: string; unit: string }[] = [
  { key: 'reorder_pct', label: 'reorder', unit: '%' },
  { key: 'corrupt_pct', label: 'corrupt', unit: '%' },
];

const dir = ref<'down' | 'up'>('down');
const VB = 100;

function valueAt(k: Keyframe, lane: LaneKey): number {
  return k[dir.value][lane];
}

function laneMax(lane: LaneKey): number {
  return keys.value.reduce((m, k) => Math.max(m, valueAt(k, lane)), 0);
}

// A second-tier lane earns its place by being used in EITHER direction: the
// panel shows one direction at a time, and a lane appearing when you switch
// direction would read as the interface losing track of the pattern.
const { show: showExtras, toggle: toggleExtras } = useExtras();

// The same switch the sliders use, so a reorder slider and a reorder lane are
// never one without the other. ORed with "the pattern actually uses it": a lane
// carrying data is drawn whatever the switch says, because a keyframe changing
// something the timeline does not draw is the timeline lying about the run.
const LANES = computed(() => [
  ...CORE_LANES,
  ...EXTRA_LANES.filter(
    (l) =>
      showExtras.value ||
      keys.value.some((k) => k.down[l.key] > 0 || k.up[l.key] > 0),
  ),
]);

/** True when the switch is the only reason the extra lanes are up. */
const extrasUnused = computed(() =>
  EXTRA_LANES.every((l) => !keys.value.some((k) => k.down[l.key] > 0 || k.up[l.key] > 0)),
);

/*
 * The wifi link lanes get their OWN expander, separate from reorder/corrupt --
 * they are a different kind of impairment (association events, not packet
 * shaping). Same rule as the extras: local to this panel (there is no wifi
 * slider elsewhere to keep in step), persisted, and ORed with "the pattern
 * actually uses it" so a collapsed group can never hide an event in force.
 */
const WIFI_KEY = 'boa.wifi';
const showWifi = ref(
  (() => {
    try {
      return localStorage.getItem(WIFI_KEY) === '1';
    } catch {
      return false;
    }
  })(),
);
watch(showWifi, (v) => {
  try {
    localStorage.setItem(WIFI_KEY, v ? '1' : '0');
  } catch {
    /* not worth breaking a control over */
  }
});

const shownLinkLanes = computed(() =>
  LINK_LANES.filter((ll) => showWifi.value || links.value.some((l) => l.kind === ll.kind)),
);

/** True when the switch is the only thing that could raise the link lanes. */
const wifiUnused = computed(() => links.value.length === 0);

/**
 * Where a value sits in its lane, 0 at the floor and 1 at the ceiling.
 *
 * Rate uses the slider's own exponential scale so that the handle position and
 * the drawn height agree; on a linear axis every mobile-relevant rate would
 * flatten against the floor. Unlimited is pinned to the TOP: it means no cap,
 * and drawing it at zero -- where the log scale would put it -- would render
 * "as fast as the link allows" as "nothing gets through".
 *
 * The other three are linear against the tallest value the pattern itself
 * reaches, so a lane that never moves stays flat instead of inventing a scale.
 */
function norm(v: number, lane: LaneKey): number {
  if (lane === 'rate_mbps') return v <= 0 ? 1 : rateToPos(v) / 100;
  // While this lane is being dragged, scale against the pinned headroom ceiling
  // so the marker tracks the cursor instead of the live max chasing it.
  const max = vdrag.value?.lane === lane ? vdrag.value.ceil : laneMax(lane);
  return max > 0 ? Math.min(v / max, 1) : 0;
}

function yOf(v: number, lane: LaneKey): number {
  return VB - 6 - norm(v, lane) * (VB - 14);
}

function xOf(sec: number): number {
  return renderSpan.value > 0 ? (sec / renderSpan.value) * 1000 : 0;
}

/**
 * A step path: hold, then jump.
 *
 * Absolute values at absolute timestamps, so the line between two keyframes is
 * flat by definition and the vertical edge lands exactly where the change does.
 * That edge is the thing the operator lines a player's reaction up against, so
 * it is drawn where it happens rather than smoothed across the gap.
 */
function lanePath(lane: LaneKey): string {
  const ks = keys.value;
  if (ks.length < 2 || dur.value <= 0) return '';
  let y = yOf(valueAt(ks[0], lane), lane);
  const d = [`M 0,${y.toFixed(1)}`];
  for (let i = 1; i < ks.length; i++) {
    const x = xOf(ks[i].at_sec).toFixed(1);
    d.push(`L ${x},${y.toFixed(1)}`);
    y = yOf(valueAt(ks[i], lane), lane);
    d.push(`L ${x},${y.toFixed(1)}`);
  }
  return d.join(' ');
}

/**
 * The ceiling label: what the TOP of the lane means, not the tallest value in
 * it.
 *
 * They are the same thing for the linear lanes, which scale to the pattern, and
 * not for rate, which is pinned to the slider's fixed scale so that a keyframe
 * drawn here and the same keyframe on the slider agree. Labelling rate's top
 * with the pattern's own maximum would say the line is at the ceiling when it
 * is at 73% of it.
 */
function laneTop(lane: LaneKey, unit: string): string {
  if (lane === 'rate_mbps') {
    // Unlimited is pinned to the very top, so when the pattern uses it, that is
    // genuinely what the top of this lane means.
    const anyUnlimited = keys.value.some((k) => valueAt(k, 'rate_mbps') <= 0);
    return anyUnlimited ? 'unlimited' : `${RATE_MAX} ${unit}`;
  }
  const max = laneMax(lane);
  return max > 0 ? `${max} ${unit}` : 'unused';
}

/** The value this lane holds at the playhead, so the ruler reads as numbers. */
function laneNow(lane: LaneKey): string {
  const ks = keys.value;
  if (!ks.length) return '';
  let v = valueAt(ks[0], lane);
  for (const k of ks) {
    if (k.at_sec <= head.value) v = valueAt(k, lane);
  }
  if (lane === 'rate_mbps' && v <= 0) return 'unlimited';
  return `${v}`;
}

function pct(sec: number): string {
  return `${renderSpan.value > 0 ? (sec / renderSpan.value) * 100 : 0}%`;
}

function fmtRate(s: Shape): string {
  return s.rate_mbps === 0 ? 'unlimited' : `${s.rate_mbps} Mbps`;
}

const status = computed(() => {
  const r = props.run;
  if (!r) return '';
  const at = `${r.pos_sec.toFixed(1)}s of ${r.dur_sec.toFixed(0)}s`;
  if (r.state === 'running') {
    return `playing · ${at}${r.loop && r.laps ? ` · lap ${r.laps + 1}` : ''} · ${fmtRate(r.down)}`;
  }
  return `${r.state} at ${at} — ${r.reason ?? ''}`;
});
</script>

<template>
  <div class="pattern">
    <!-- Folded by default. A timeline is four lanes, a ruler and two rows of
         controls; on a page of devices that is a lot of chrome for something
         most cards are not doing. The summary and the play control stay, so a
         device with a pattern is never silent about having one. -->
    <h3 class="head" @click="onHeadClick">
      Pattern
      <span class="meta">{{ summary }}</span>
      <span class="spacer"></span>
      <template v-if="pattern">
        <button v-if="running" class="ghost" @click="emit('stop')">stop</button>
        <button v-else :disabled="!canPlay" :title="blocked" @click="emit('play')">
          {{ paused ? 'resume' : 'play' }}
        </button>
      </template>
      <button class="ghost" @click="setOpen(!open)">
        {{ open ? 'close' : pattern ? 'edit' : 'add' }}
      </button>
    </h3>

    <!-- Playback has to report itself whether or not the editor is open: the
         device is being conditioned by something that is not its saved
         settings, and a folded panel must not hide that. -->
    <p v-if="run && !open" class="meta folded-status">{{ status }}</p>

    <!-- The library comes first, and stays visible whether or not a timeline
         is loaded. Choosing what to play is the common action; authoring one
         is the rare one, so the picker is not buried behind the editor. -->
    <PatternLibrary
      v-if="open"
      :mac="mac"
      :rev="rev"
      :ladders="ladders"
      :current="pattern"
      :locked="running"
      @changed="emit('select', null); emit('changed')"
    />

    <!-- Hand-authoring, for a shape the ladder does not describe. Both routes
         start from something concrete: the device's current settings, or a
         named link. An empty editor with a single keyframe at zero would be a
         worse blank page. -->
    <div v-if="open && !pattern" class="start">
      <button @click="emit('create')">start from these settings</button>
      <select @change="useTemplate(($event.target as HTMLSelectElement).value)">
        <option value="">or a template…</option>
        <option v-for="t in PATTERN_TEMPLATES" :key="t.name" :value="t.name" :title="t.note">
          {{ t.name }} — {{ t.note }}
        </option>
      </select>
    </div>

    <div v-else-if="open && pattern">
      <div class="bar">
        <input
          class="name" :value="pattern.name" :disabled="!!run"
          @change="rename(($event.target as HTMLInputElement).value)"
        />
        <label class="loop">
          <input
            type="checkbox" :checked="pattern.loop" :disabled="!!run"
            @change="setLoop(($event.target as HTMLInputElement).checked)"
          />
          loop
        </label>
        <!-- Which direction the lanes draw. Not which direction is EDITED --
             a keyframe always holds both, and the sliders for both are above. -->
        <span class="dirsel">
          <button :class="{ on: dir === 'down' }" @click="dir = 'down'">downlink</button>
          <button :class="{ on: dir === 'up' }" @click="dir = 'up'">uplink</button>
        </span>
        <span class="spacer"></span>
        <!-- Play lives in the header, where it is reachable without opening
             the editor. Only what edits the timeline is down here. -->
        <button v-if="run && !running" class="ghost" @click="emit('stop')">clear</button>
        <button class="ghost" :disabled="!!run" @click="emit('remove')">remove</button>
      </div>

      <div
        ref="stackEl" class="stack" :class="[dir, { seekable: !run }]"
        @click="clickTimeline"
        @contextmenu.prevent="ctxTimeline"
      >
        <!-- Each value lane carries its OWN markers now -- its field's
             transitions, dragged independently of every other lane. Vertical
             drag on the lane sets the value; a marker drags in time; right-click
             the lane to add a point, or a marker to delete it. -->
        <div
          v-for="l in LANES" :key="l.key" class="lane"
          :class="{ grab: !run, dragging: vdrag?.lane === l.key }"
          :title="run ? '' : `double-click or right-click to add a ${l.label} point; drag up and down to set it`"
          @pointerdown.stop="startVDrag(l.key, $event)"
          @pointermove="onLaneMove"
          @pointerup="onLaneUp"
          @pointercancel="onLaneUp"
          @dblclick.prevent.stop="addField(l.key, $event)"
          @contextmenu.prevent.stop="addField(l.key, $event)"
        >
          <svg :viewBox="`0 0 1000 ${VB}`" preserveAspectRatio="none">
            <path :d="lanePath(l.key)" class="line" vector-effect="non-scaling-stroke" />
          </svg>
          <button
            v-for="(s, i) in laneSteps(l.key)" :key="i"
            class="kf" :class="{ on: selSec === s.at_sec, pinned: i === 0 }"
            :style="{ left: pct(s.at_sec) }"
            :disabled="!!running"
            :title="i === 0
              ? `${s.at_sec}s · ${l.label} starts here`
              : `${s.at_sec}s · ${l.label} · drag to move, double-click or right-click to delete`"
            @click.stop="toggleKeyAt(s.at_sec)"
            @dblclick.prevent.stop="removeField(l.key, i)"
            @contextmenu.prevent.stop="removeField(l.key, i)"
            @pointerdown.stop="startFieldDrag(l.key, i, $event)"
          ></button>
          <span class="lane-name">
            {{ l.label }} <b class="num">{{ laneNow(l.key) }}</b>
          </span>
          <span class="lane-top">{{ laneTop(l.key, l.unit) }}</span>
        </div>

        <!-- Link lanes: drop/nudge/deadzone as on/off blocks, behind their own
             "wifi" expander. No vertical axis; the block WIDTH is the duration
             (a thin pulse fires once on the rising edge, a wide block holds the
             disturbance). See #135. -->
        <div
          v-for="ll in shownLinkLanes" :key="ll.kind"
          class="lane linklane" :class="{ grab: !run }"
          :title="run ? '' : `double-click or right-click to add a ${ll.label}; drag it to move`"
          @dblclick.prevent.stop="addLink(ll.kind, $event)"
          @contextmenu.prevent.stop="addLink(ll.kind, $event)"
          @pointermove="onLinkDrag"
          @pointerup="endLinkDrag"
          @pointercancel="endLinkDrag"
        >
          <svg :viewBox="`0 0 1000 ${VB}`" preserveAspectRatio="none">
            <path :d="linkLanePath(ll.kind)" class="line" vector-effect="non-scaling-stroke" />
          </svg>
          <div
            v-for="{ ev, i } in laneEvents(ll.kind)" :key="i"
            class="linkblock" :class="ll.kind"
            :style="{ left: evLeftPct(ev) + '%', width: evWidthPct(ev) + '%' }"
            :title="run ? '' : 'drag to move, drag an edge to resize, double-click or right-click to delete'"
            @pointerdown.stop="startLinkMove(i, $event)"
            @click.stop
            @dblclick.prevent.stop="removeLink(i)"
            @contextmenu.prevent.stop="removeLink(i)"
          >
            <span
              class="kf l" title="drag the rising edge"
              @pointerdown.stop="startLinkResize(i, 'l', $event)"
            ></span>
            <span
              class="kf r" title="drag the falling edge"
              @pointerdown.stop="startLinkResize(i, 'r', $event)"
            ></span>
            <button class="linkx" @click.stop="removeLink(i)" title="remove">×</button>
          </div>
          <span class="lane-name">{{ ll.label }}</span>
        </div>

        <!-- Everything past the last keyframe is ruler, not pattern. Shaded so
             the two are never confused: a loop restarts at the end marker, and
             the empty space after it is somewhere to drag into, not silence
             that gets played. -->
        <div class="beyond" :style="{ left: pct(dur) }"></div>
        <div class="endline" :style="{ left: pct(dur) }"></div>

        <!-- The selected moment is a column across every lane: the card's
             sliders set all fields at that instant, and the band says which. -->
        <div
          v-if="selSec != null" class="selband"
          :style="{ left: pct(selSec) }"
        ></div>
        <div class="playhead" :style="{ left: pct(head) }"></div>

        <!-- Time is a lane, not a control beside the picture. Inside the stack
             it shares the x-axis with rate, delay, jitter and loss, so dragging
             it moves the playhead exactly as far as the cursor travels -- which
             a slider in its own row could only approximate. -->
        <div
          class="lane timelane" :class="{ grab: !run }"
          :tabindex="run ? -1 : 0" role="slider"
          aria-label="time"
          :aria-valuemin="0" :aria-valuemax="Math.round(renderSpan)"
          :aria-valuenow="Math.round(head * 10) / 10"
          @pointerdown.stop="startScrub"
          @pointermove="onScrub"
          @pointerup="endScrub"
          @pointercancel="endScrub"
          @keydown="keyScrub"
        >
          <!-- The end is already drawn: the shaded headroom starts there and
               the end marker sits on it, and the header states the length in
               words. A number floating on the axis as well was a third telling
               of the same fact, on the row whose job is the playhead. -->
          <span class="lane-name">time <b class="num">{{ head.toFixed(1) }}s</b></span>
        </div>
      </div>

      <!-- Two expanders, side by side: the second-tier packet impairments
           (reorder, corrupt) and the wifi link lanes (drop, nudge, deadzone).
           Each is hidden while nothing uses it, and each cannot hide a lane
           that carries data -- a control that cannot do anything is worse than
           none. -->
      <div class="head-row">
        <button
          v-if="extrasUnused" class="more"
          @click="toggleExtras()"
        >{{ showExtras ? '−' : '+' }}
          {{ EXTRA_IMPAIRMENTS.map((e) => e.label).join(', ') }}</button>
        <button
          v-if="wifiUnused" class="more"
          @click="showWifi = !showWifi"
        >{{ showWifi ? '−' : '+' }} wifi</button>
      </div>

      <!-- What the sliders above are pointed at. Stated rather than implied:
           the same controls edit different things depending on this line, and
           guessing which is not something an operator should have to do. -->
      <div v-if="run" class="sel">
        <b>{{ status }}</b>
        <span v-if="paused" class="meta">
          The sliders show your settings again; playback is where you left it.
        </span>
        <span v-else-if="running" class="meta">
          The controls above are showing what this pattern is enforcing. Move
          one and playback pauses.
        </span>
      </div>
      <div v-else-if="selSec != null" class="sel">
        <b>editing {{ selSec }}s</b>
        <span class="meta">
          both directions, every slider — set them above; each field keeps its
          own value at this moment
        </span>
        <span class="spacer"></span>
        <button class="ghost" @click="emit('select', null)">done</button>
      </div>
      <div v-else class="sel">
        <span class="meta">
          Each lane is its own timeline. Drag a marker to move that field's
          transition — nothing on another lane moves, and it stops at its own
          neighbours rather than crossing them. Double-click or right-click a
          lane to add a point, then drag it up and down to set its value; the
          same on a marker (or a drop/nudge/deadzone block) deletes it. Single-
          click the timeline to pick a moment and let the sliders above set
          every field at once. Drag a field's last transition into the shaded
          space, or set the length below, to make the pattern longer.
        </span>
      </div>

      <div v-if="!run" class="tools">
        <label class="len">
          length
          <input
            type="number" min="1" step="0.5" :value="dur"
            @change="setLength(+($event.target as HTMLInputElement).value)"
          />
          s
        </label>
        <span class="meta">
          The last keyframe is the end, so this moves it — the same thing
          dragging it does, for when you want an exact number.
        </span>
        <span class="spacer"></span>
        <button class="ghost" @click="closeLoop">match the last keyframe to the first</button>
        <span class="meta">
          A loop restarts at the first keyframe, so matching the last one to it
          is what makes it seamless.
        </span>
      </div>

      <p v-if="!canPlay && !run" class="meta blocked">{{ blocked }}</p>
    </div>
  </div>
</template>

<style scoped>
/* Matches the affordance on the sliders, because it is the same switch. */
.more {
  align-self: start;
  margin-top: 2px;
  padding: 1px 0;
  font: inherit; font-size: 11px;
  color: var(--ink-faint);
  background: none; border: 0;
  cursor: pointer;
}
.more:hover { color: var(--ink-dim); }

.pattern {
  padding: 12px 14px;
  border-top: 1px solid var(--line);
}
.pattern h3 {
  margin: 0;
  font-size: 13px;
  display: flex;
  gap: 8px;
  align-items: center;
}
.pattern h3 + * {
  margin-top: 8px;
}
.folded-status {
  margin: 8px 0 0;
}
.start,
.bar,
.sel,
.tools {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}
.bar {
  margin-bottom: 8px;
}
.name {
  min-width: 12rem;
}
.loop {
  display: flex;
  gap: 4px;
  align-items: center;
  color: var(--ink-dim);
}
.dirsel button.on {
  color: var(--down);
  border-color: var(--down);
}
/* Clickable, so it says so. The text inside stays selectable -- onHeadClick
   ignores a click that ends a selection, which is what makes that safe. */
.head { cursor: pointer; }

/* Only a cursor says a lane is draggable, so it has to. ns-resize rather than
   grab: the gesture is vertical only, and a cursor promising both directions
   would offer a horizontal drag that does nothing. */
.lane.grab { cursor: ns-resize; }
.lane.dragging { background: var(--line); }

.spacer {
  flex: 1;
}

/* The stack: a shared marker ruler over four lanes that share an x-axis.
   Markers and the playhead are HTML over SVG, because SVG shapes would be
   stretched by the non-uniform scaling that lets each lane fill any width. */
.stack {
  position: relative;
  background: var(--panel-2);
  border: 1px solid var(--line);
  border-radius: 4px;
  padding-top: 12px;
}
.ruler {
  position: relative;
  height: 0;
}
.lane {
  position: relative;
  height: 42px;
  border-top: 1px solid var(--line-soft);
}
.lane:first-of-type {
  border-top: none;
}
.lane svg {
  width: 100%;
  height: 100%;
  display: block;
}
/* Link lanes: on/off blocks, horizontal only. No vertical value, so the lane
   is a crosshair (click empty to add) rather than ns-resize. */
.linklane.grab {
  cursor: crosshair;
}
.linklane svg {
  pointer-events: none;
}
/* The square-wave path is the visual; each block is an invisible hit-area over
   it for move/resize/delete. A faint tint on hover shows what you'd grab. */
.linkblock {
  position: absolute;
  top: 6px;
  bottom: 6px;
  min-width: 7px;
  border-radius: 3px;
  background: transparent;
  cursor: grab;
}
.linkblock:hover {
  background: color-mix(in srgb, var(--down) 18%, transparent);
}
/* The rising and falling edges are keyframes drawn with the SAME .kf marker the
   ruler uses -- the hollow diamond, its fill, border, size and rotation all
   inherited from the base .kf rule. Only what genuinely differs for an edge is
   set here: it sits on its block's corner (not on the ruler at an inline left),
   and the cursor says resize rather than grab. */
.linkblock .kf { cursor: ew-resize; }
.linkblock .kf.l { left: 0; }
.linkblock .kf.r { left: 100%; }
.linkblock:hover .kf { border-color: var(--down); }
.linkx {
  position: absolute;
  top: -7px;
  right: -6px;
  width: 14px;
  height: 14px;
  padding: 0;
  line-height: 11px;
  font-size: 11px;
  border-radius: 50%;
  border: 1px solid var(--line);
  background: var(--panel);
  color: var(--ink-dim);
  cursor: pointer;
  display: none;
}
.linkblock:hover .linkx {
  display: block;
}
/* The lanes take the direction's colour, the same one its chart and slider
   already use, so which direction is on screen is never in question. */
.line {
  fill: none;
  stroke: var(--down);
  stroke-width: 1.5;
}
.stack.up .line {
  stroke: var(--up);
}
/* Labels sit ON the plot rather than in a gutter, so every lane starts at the
   same x as the ruler above it and a keyframe lines up across all four without
   arithmetic. They carry the panel's own background so a step line passing
   underneath cannot swallow them. */
.lane-name,
.lane-top {
  position: absolute;
  top: 3px;
  font-size: 10px;
  pointer-events: none;
  background: var(--panel-2);
  padding: 0 3px;
  border-radius: 2px;
}
/* The name carries the value under the playhead: a step chart on its own says
   "it went down", not "to 1.5". */
.lane-name {
  left: 3px;
  color: var(--ink-dim);
}
.lane-name b {
  color: var(--ink);
}
.lane-top {
  right: 3px;
  color: var(--ink-faint);
}
.kf {
  position: absolute;
  top: -6px;
  width: 11px;
  height: 11px;
  padding: 0;
  transform: translateX(-50%) rotate(45deg);
  background: var(--panel);
  border: 1px solid var(--ink-dim);
  border-radius: 2px;
  cursor: grab;
  z-index: 2;
  touch-action: none;
}
.kf:active {
  cursor: grabbing;
}
/* The first keyframe is the starting condition and cannot move, so it must not
   invite a drag it will refuse. */
.kf.pinned {
  cursor: pointer;
  border-style: dashed;
}
.kf.on {
  background: var(--down);
  border-color: var(--down);
}
/* Value-lane markers sit at the top of their own lane (a direct child of the
   lane), not on a shared ruler above the stack. */
.lane > .kf {
  top: -1px;
}
.stack.up .lane > .kf.on {
  background: var(--up);
  border-color: var(--up);
}
/* Headroom: ruler past the end of the pattern, to drag or click into. */
.beyond {
  position: absolute;
  top: 12px;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.22);
  pointer-events: none;
}
.endline {
  position: absolute;
  top: 6px;
  bottom: 0;
  width: 1px;
  background: var(--line);
  pointer-events: none;
}
/* Shorter than a data lane: it carries a label and a tick, not a shape. */
.timelane {
  height: 20px;
  cursor: default;
}
.timelane.grab {
  cursor: ew-resize;
}
.timelane:focus-visible {
  outline: 2px solid var(--down);
  outline-offset: -2px;
}
.selband {
  position: absolute;
  top: 12px;
  bottom: 0;
  width: 1px;
  background: var(--down);
  opacity: 0.5;
  pointer-events: none;
}
.playhead {
  position: absolute;
  top: 6px;
  bottom: 0;
  width: 1px;
  background: var(--warn);
  pointer-events: none;
  z-index: 1;
}
.stack.seekable {
  cursor: crosshair;
}
.head-row {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-top: 10px;
}
.scrub {
  flex: 1;
  margin: 0;
}
.at-now {
  min-width: 4rem;
  text-align: right;
  color: var(--ink-dim);
}
.ticks {
  display: flex;
  margin-bottom: 6px;
}
.sel {
  min-height: 26px;
}
.sel .at input,
.len input {
  width: 5rem;
}
.len {
  display: flex;
  gap: 4px;
  align-items: center;
  color: var(--ink-dim);
}
.tools {
  margin-top: 6px;
}
.blocked {
  color: var(--warn);
  margin: 6px 0 0;
}
</style>
