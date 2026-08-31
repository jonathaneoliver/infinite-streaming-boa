<script setup lang="ts">
/**
 * The timeline editor.
 *
 * A keyframe is the whole policy at one instant -- both directions, all four
 * parameters -- so the controls that author it are the ones already on the
 * card: this panel selects WHICH moment the rate, delay, jitter and loss
 * sliders are editing, and the sliders do the rest. A second set of numeric
 * fields would be a second source of truth about the same values, free to
 * disagree with the first.
 *
 * # Why the markers are shared and the lanes are not
 *
 * Because a keyframe carries every parameter, it belongs to the TIMELINE, not
 * to a lane. One ruler of markers therefore runs along the top, and the lanes
 * below it draw the value each keyframe holds. Per-lane markers would imply
 * four independent automation tracks that could be keyed at different times,
 * which is a different and much worse data model: three lanes can disagree
 * about what second 30 means, and the operator reading a surprising result then
 * has to reconstruct the effective policy in their head.
 *
 * Dragging a lane vertically is not a second set of markers and does not change
 * that. It edits ONE parameter of the keyframe already governing that moment --
 * the same value the card's slider edits, reached where it is being read
 * instead of after a separate selection. Nothing new is keyed by it.
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
import type { Keyframe, Ladder, Pattern, PatternView, Shape } from '@/types';
import { EXTRA_IMPAIRMENTS, PATTERN_TEMPLATES, RATE_MAX, posToRate, rateToPos } from '@/types';
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
  /** Which keyframe the card's sliders are editing, if any. */
  selected: number | null;
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
  drag.value ? Math.max(drag.value.span, dur.value) : viewSpan.value,
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
  // Landing on a keyframe points the sliders at it: the time control chooses
  // which moment they are editing, which is the whole interaction.
  emit('select', keyAt(sec));
}

/** The keyframe exactly at this position, if the playhead is sitting on one. */
function keyAt(sec: number): number | null {
  for (let i = 0; i < keys.value.length; i++) {
    if (Math.abs(keys.value[i].at_sec - sec) < 1e-6) return i;
  }
  return null;
}

/** Clicking the selected marker again hands the sliders back to the policy. */
function toggleKey(i: number) {
  if (suppressClick) {
    suppressClick = false;
    return;
  }
  scrub.value = keys.value[i]?.at_sec ?? 0;
  emit('select', props.selected === i ? null : i);
}

/*
 * Right-click adds a keyframe where you clicked, and removes the one you
 * clicked on.
 *
 * An accelerator, not the mechanism: a right-click affordance is invisible, and
 * on a trackpad it is a modifier chord, so the explicit "+ keyframe here" and
 * "delete" buttons stay. This just removes the trip to them once you know.
 */
function ctxTimeline(e: MouseEvent) {
  if (props.run || dur.value <= 0) return;
  const at = timeAt(e.clientX, viewSpan.value);
  seek(at);
  if (keyAt(at) == null) snapshot();
}

function ctxKey(i: number) {
  if (props.run) return;
  removeKey(i);
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

const selKey = computed(() =>
  props.selected != null ? (keys.value[props.selected] ?? null) : null,
);

function mutate(fn: (p: Pattern) => void) {
  if (!props.pattern) return;
  const next = JSON.parse(JSON.stringify(props.pattern)) as Pattern;
  fn(next);
  next.keys.sort((a, b) => a.at_sec - b.at_sec);
  emit('update', next);
}

/**
 * Add a keyframe at the playhead, inheriting the keyframe before it.
 *
 * Adding a point to a timeline must not change the timeline. Every value is
 * held until the next keyframe, so the pattern already HAS a value at this
 * instant -- the previous keyframe's -- and copying it means the link behaves
 * identically until the operator deliberately moves a slider. Seeding from the
 * sliders instead would silently reshape the pattern at the moment of adding a
 * point to it, which is the behaviour that makes people distrust an editor.
 *
 * The new keyframe is then selected, so the sliders are already pointed at it:
 * add, then shape. The very first keyframes come from the device's current
 * settings instead, because a brand new pattern has nothing to inherit.
 */
function snapshot() {
  const sec = head.value;
  if (!props.pattern || keyAt(sec) != null) return;
  // Where this lands once the list is re-sorted; computed here rather than
  // waiting for the round trip, so the new keyframe is selected immediately and
  // the sliders are pointed at it while the operator still means to edit it.
  const idx = keys.value.filter((k) => k.at_sec < sec).length;
  const prev = keys.value[Math.max(0, idx - 1)];
  mutate((p) => {
    p.keys.push({
      at_sec: sec,
      down: { ...prev.down },
      up: { ...prev.up },
      ease: 'hold',
    });
  });
  emit('select', idx);
}

/*
 * Why the add button is disabled rather than hidden.
 *
 * It used to appear only when the playhead sat between keyframes, which meant
 * the one control that adds a keyframe was invisible exactly when someone had
 * just landed on one and was looking for it -- and at 0s, where the first
 * keyframe always is, it was offered and then did nothing. An action that is
 * present and explains why it cannot run beats one that vanishes, and both beat
 * one that silently no-ops.
 */
const canSnapshot = computed(() => !props.run && keyAt(head.value) == null);
const snapshotWhy = computed(() => {
  if (props.run) return 'Stop playback before editing the timeline';
  if (keyAt(head.value) != null) {
    return 'There is already a keyframe here — move the playhead, or edit this one with the sliders above';
  }
  return `Add a keyframe at ${head.value.toFixed(1)}s, inheriting the one before it`;
});

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
 * Dragging a keyframe along the ruler.
 *
 * The span is captured when the drag starts and held for its duration. Dragging
 * the LAST keyframe changes the pattern's length, so recomputing x from a
 * duration that the drag itself is changing makes the marker chase the cursor
 * and never settle.
 *
 * moveKey does the clamping: a keyframe cannot cross its neighbours, and the
 * first one cannot move at all -- a timeline with no value at t=0 has no
 * defined starting condition.
 */
const drag = ref<{
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
 * twitches, and the keyframe moves to whatever half second that pixel rounds
 * to. Worse, the drag then swallows the click that was meant to select it, so
 * clicking a keyframe would nudge it in time and fail to select it -- both at
 * once, and neither visibly.
 */
const DRAG_SLOP_PX = 3;

function startDrag(i: number, e: PointerEvent) {
  if (props.run || i === 0) return;
  // Capture keeps the pointer's events on the marker once the cursor leaves it,
  // which is most of any real drag. It is best-effort: a browser that refuses
  // must not take the drag down with it, because the fallback -- events on
  // whatever is under the cursor -- still works for the common case.
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* no active pointer; the drag still tracks via bubbled events */
  }
  // Selection is NOT claimed here. A press that turns out to be a plain click
  // must leave it to the click handler, or selecting on the way down and
  // toggling on the way up cancel each other out and the keyframe can never be
  // selected at all.
  // Read once, at the press. Sampling it per move would let the gesture change
  // meaning halfway through, so half a drag would ripple and half would not.
  drag.value = {
    i, span: viewSpan.value, x0: e.clientX, moved: false, penned: e.altKey,
  };
}

function onDrag(e: PointerEvent) {
  const d = drag.value;
  const cur = keys.value[d?.i ?? -1];
  // The keyframe can go away underneath a drag -- another tab deleting it, a
  // pattern replaced by a template. Without this the move writes to undefined
  // and the drag dies with an exception rather than simply stopping.
  if (!d || !cur) return;
  if (!d.moved && Math.abs(e.clientX - d.x0) < DRAG_SLOP_PX) return;
  // Past the threshold this is a drag: it points the sliders at what is being
  // moved, and its click will be suppressed.
  if (!d.moved) emit('select', d.i);
  d.moved = true;
  // Only the last keyframe may run past the ruler; the rest are penned in by
  // their neighbours anyway.
  const at = timeAt(e.clientX, d.span, d.i === keys.value.length - 1);
  if (at !== cur.at_sec) moveKey(d.i, at, d.penned, d.span);
}

function endDrag(e: PointerEvent) {
  if (!drag.value) return;
  // Releasing comes first but must not be able to prevent the cleanup below: if
  // capture was never granted, releasing throws, and an exception here would
  // leave the drag live so the keyframe followed the cursor with no button
  // held.
  try {
    (e.target as HTMLElement).releasePointerCapture(e.pointerId);
  } catch {
    /* nothing was captured */
  }
  // A drag ends in a click on the marker. Without this the click would toggle
  // the selection straight back off, so moving a keyframe would deselect it and
  // the sliders would jump back to the stored policy mid-edit.
  if (drag.value.moved) suppressClick = true;
  drag.value = null;
}

let suppressClick = false;

function removeKey(i: number) {
  // The first keyframe is the pattern's starting condition and a timeline
  // without one has no defined value at t=0; two is the minimum the daemon
  // will accept at all.
  if (keys.value.length <= 2 || i === 0) return;
  mutate((p) => {
    p.keys.splice(i, 1);
  });
  emit('select', null);
}

/**
 * Move a keyframe, taking everything after it along -- or not, with alt held.
 *
 * # Why ripple is the default
 *
 * The commonest edit to a ladder pattern is "give this rung longer", and that
 * is a statement about ONE segment: the gap before this keyframe grew, and
 * nothing else about the pattern changed. Ripple says exactly that in one
 * gesture.
 *
 * Penned dragging cannot say it at all. On a 24-keyframe pattern, stretching
 * one dwell means dragging that keyframe and then all 22 after it, each clamped
 * by the neighbour not yet moved, and finally the last one again to extend the
 * end. Twenty-three gestures for one intent is not an editing model.
 *
 * The cost is that the total duration and the loop point move. That is real,
 * and it is visible: the end marker travels, the shaded headroom follows it,
 * and the header's duration updates as you drag. Nothing changes quietly.
 *
 * # Why penned is still reachable
 *
 * Sliding a keyframe within its gap -- making one segment shorter and the next
 * longer, with the pattern's length untouched -- is a real and different edit.
 * alt-drag does it, which is the modifier timeline editors already use for
 * "leave the neighbours alone".
 */
function moveKey(i: number, sec: number, penned = false, limitSec = maxPatternSec) {
  // Half-second granularity, because throughput is sampled once a second and a
  // transition finer than that can be configured but never observed.
  if (i === 0) return;
  const ks = keys.value;
  const at = Math.max(0, Math.round(sec * 2) / 2);

  if (penned || i === ks.length - 1) {
    // The last keyframe has nothing after it to push, so a ripple and a penned
    // move are the same gesture there -- and it is how the pattern is
    // lengthened by dragging into the headroom.
    const lo = ks[i - 1].at_sec + 0.5;
    const hi = i + 1 < ks.length ? ks[i + 1].at_sec - 0.5 : maxPatternSec;
    const clamped = Math.min(Math.max(at, lo), hi);
    mutate((p) => {
      p.keys[i].at_sec = clamped;
    });
    scrub.value = clamped;
    return;
  }

  // Ripple. The keyframe may not cross the one before it -- that would reorder
  // the timeline under the hand -- and the delta is capped so the LAST keyframe
  // cannot be pushed past what the runtime will play. Clamping the whole shift
  // rather than each keyframe keeps the gaps after this one exactly as they
  // were: clamping individually would silently compress the tail against the
  // ceiling instead of stopping.
  //
  // The tail also stops at the RULER, not just at the runtime's ceiling. The
  // axis is pinned for the length of a gesture -- it has to be, or the scale
  // would stretch under the marker and the keyframe would chase the cursor --
  // so a ripple that runs past the pinned span has nowhere left to draw its
  // tail and stacks every trailing marker on the right edge. Stopping is
  // legible where piling up is not; release and the ruler regrows around the
  // longer pattern, so the next drag carries on.
  const lo = ks[i - 1].at_sec + 0.5;
  const room = Math.min(limitSec, maxPatternSec) - ks[ks.length - 1].at_sec;
  const target = Math.min(Math.max(at, lo), ks[i].at_sec + room);
  const delta = target - ks[i].at_sec;
  if (delta === 0) return;
  mutate((p) => {
    for (let n = i; n < p.keys.length; n++) {
      p.keys[n].at_sec = Math.round((p.keys[n].at_sec + delta) * 2) / 2;
    }
  });
  scrub.value = target;
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
  i: number;
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

/** Which keyframe holds the value at this x. */
function keyGoverning(clientX: number): number {
  const t = timeAt(clientX, renderSpan.value);
  let i = 0;
  for (let n = 0; n < keys.value.length; n++) {
    if (keys.value[n].at_sec <= t) i = n;
  }
  return i;
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
  if (props.run) return;
  const i = keyGoverning(e.clientX);
  try {
    (e.target as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    /* no active pointer; the drag still tracks via bubbled events */
  }
  const ceil = laneMax(lane) || LANE_START_CEIL[lane];
  vdrag.value = {
    lane, i, ceil,
    y0: e.clientY,
    n0: normOf(valueAt(keys.value[i], lane), lane, ceil),
    moved: false,
  };
}

function onVDrag(e: PointerEvent) {
  const d = vdrag.value;
  if (!d || !keys.value[d.i]) return;
  // The same slop as the horizontal drag, and for the same reason: without it
  // every click on a lane is a one-pixel drag that nudges a value on the way to
  // moving the playhead, and neither action is what was asked for.
  if (!d.moved && Math.abs(e.clientY - d.y0) < DRAG_SLOP_PX) return;
  if (!d.moved) emit('select', d.i);
  d.moved = true;
  // Up is positive: the screen's y grows downward and a value grows upward.
  const n = d.n0 + normDelta(d.y0 - e.clientY, e.currentTarget as HTMLElement);
  setLaneValue(d.i, d.lane, Math.min(Math.max(n, 0), 1), d.ceil);
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

/**
 * Writes one parameter of one keyframe from a lane position.
 *
 * The clamps are the daemon's own, applied here so a drag cannot compose a
 * policy the box will refuse -- a slider that silently produces rejected writes
 * is worse than one that cannot reach the value.
 */
function setLaneValue(i: number, lane: LaneKey, n: number, ceil: number) {
  mutate((p) => {
    const shape = p.keys[i][dir.value];
    if (lane === 'rate_mbps') {
      // Never 0 from a drag. Zero means unlimited, which is drawn at the TOP of
      // the lane while the slider puts it at the bottom, so letting the floor
      // produce it would send the line leaping to the ceiling as the cursor
      // reached the bottom. Clearing a cap is the slider's job.
      shape.rate_mbps = posToRate(Math.max(1, Math.round(n * 100)));
      return;
    }
    if (lane === 'loss_pct') {
      shape.loss_pct = Math.round(Math.min(n * ceil, 20) * 10) / 10;
      return;
    }
    const v = Math.round(Math.min(n * ceil, 10000));
    if (lane === 'delay_ms') {
      shape.delay_ms = v;
      // Jitter is a width around delay; the daemon refuses a width wider than
      // the thing it is a width of, so it follows delay down.
      if (shape.jitter_ms > v) shape.jitter_ms = v;
    } else {
      shape.jitter_ms = Math.min(v, shape.delay_ms);
    }
  });
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
 * Change how long the pattern runs, without adding anything to it.
 *
 * The length IS the last keyframe: it is the loop point, and the moment a
 * one-shot run ends. So lengthening the timeline moves that keyframe later and
 * the values before it simply hold for longer -- no new keyframe, and nothing
 * else on the timeline shifts. Shortening is refused past the keyframe before
 * it, which would otherwise silently delete keyframes off the end.
 */
function setLength(sec: number) {
  if (keys.value.length < 2) return;
  moveKey(keys.value.length - 1, sec);
}

/**
 * Make the last keyframe match the first.
 *
 * A loop restarts at keyframe 0, so this is what a seamless loop IS -- there is
 * no wrap setting to get wrong, and the result is visible on the timeline
 * rather than hidden in a flag.
 */
function closeLoop() {
  mutate((p) => {
    const last = p.keys.length - 1;
    p.keys[last].down = { ...p.keys[0].down };
    p.keys[last].up = { ...p.keys[0].up };
  });
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
  const max = laneMax(lane);
  return max > 0 ? v / max : 0;
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
      @changed="emit('select', null)"
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
        <!-- One ruler for the whole stack: a keyframe sets every parameter at
             once, so it is a moment in the timeline rather than a point on a
             curve. -->
        <div class="ruler">
          <button
            v-for="(k, i) in pattern.keys" :key="i"
            class="kf" :class="{ on: selected === i, pinned: i === 0 }"
            :style="{ left: pct(k.at_sec) }"
            :disabled="!!running"
            :title="i === 0
              ? `${k.at_sec}s · ${fmtRate(k.down)} · the starting condition, fixed at 0s`
              : `${k.at_sec}s · ${fmtRate(k.down)} · drag to move, right-click to delete`"
            @click.stop="toggleKey(i)"
            @contextmenu.prevent.stop="ctxKey(i)"
            @pointerdown.stop="startDrag(i, $event)"
            @pointermove="onDrag"
            @pointerup="endDrag"
            @pointercancel="endDrag"
          ></button>
        </div>

        <div
          v-for="l in LANES" :key="l.key" class="lane"
          :class="{ grab: !run, dragging: vdrag?.lane === l.key }"
          :title="run ? '' : `drag up and down to set ${l.label} at that moment`"
          @pointerdown.stop="startVDrag(l.key, $event)"
          @pointermove="onVDrag"
          @pointerup="endVDrag"
          @pointercancel="endVDrag"
        >
          <svg :viewBox="`0 0 1000 ${VB}`" preserveAspectRatio="none">
            <path :d="lanePath(l.key)" class="line" vector-effect="non-scaling-stroke" />
          </svg>
          <span class="lane-name">
            {{ l.label }} <b class="num">{{ laneNow(l.key) }}</b>
          </span>
          <span class="lane-top">{{ laneTop(l.key, l.unit) }}</span>
        </div>

        <!-- Everything past the last keyframe is ruler, not pattern. Shaded so
             the two are never confused: a loop restarts at the end marker, and
             the empty space after it is somewhere to drag into, not silence
             that gets played. -->
        <div class="beyond" :style="{ left: pct(dur) }"></div>
        <div class="endline" :style="{ left: pct(dur) }"></div>

        <!-- The selected keyframe is a column, not a dot: selecting it selects
             all four values at that instant, and the band says so. -->
        <div
          v-if="selKey" class="selband"
          :style="{ left: pct(selKey.at_sec) }"
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

      <!-- The same switch the sliders carry, put where the lanes are so it can
           be reached from whichever view raised the question. Hidden while the
           pattern uses these values: the lanes are then not optional, and a
           control that cannot do anything is worse than no control. -->
      <button
        v-if="extrasUnused" class="more"
        @click="toggleExtras()"
      >{{ showExtras ? '−' : '+' }}
        {{ EXTRA_IMPAIRMENTS.map((e) => e.label).join(', ') }}</button>

      <!-- Adding a keyframe is one always-present button rather than something
           that appears only once the playhead happens to be between keyframes.
           It is not on the time lane: a button inside the stack would inset the
           axis it sits on, which is the whole problem being fixed. -->
      <div class="head-row">
        <button :disabled="!canSnapshot" :title="snapshotWhy" @click="snapshot">
          + keyframe here
        </button>
      </div>

      <!-- What the sliders above are pointed at. Stated rather than implied:
           the same controls edit three different things depending on this line,
           and guessing which is not something an operator should have to do. -->
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
      <div v-else-if="selKey" class="sel">
        <b>keyframe {{ (selected ?? 0) + 1 }} of {{ pattern.keys.length }}</b>
        <span class="meta">
          both directions, all four sliders — set them above
        </span>
        <label class="at">
          at
          <input
            type="number" min="0" step="0.5" :value="selKey.at_sec"
            :disabled="selected === 0"
            @change="moveKey(selected!, +($event.target as HTMLInputElement).value)"
          />
          s
        </label>
        <span class="spacer"></span>
        <button
          class="ghost" :disabled="selected === 0 || pattern.keys.length <= 2"
          @click="removeKey(selected!)"
        >delete</button>
      </div>
      <div v-else class="sel">
        <span class="meta">
          Drag a keyframe to move it and everything after it; hold alt to slide
          it between its neighbours instead. A ripple stops when its tail
          reaches the end of the ruler — let go and the ruler grows, then carry
          on.
          Click the timeline to move the playhead, then add a keyframe — or
          right-click the timeline to do both at once. A new keyframe inherits
          the one before it, so adding one changes nothing until you move a
          slider. Keyframes drag to move, right-click to delete. Drag the last
          one into the shaded space, or click out there, to make the pattern
          longer.
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
