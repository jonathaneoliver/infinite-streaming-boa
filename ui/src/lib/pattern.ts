// Per-field editing over a shared-keyframe pattern.
//
// The stored/played format is a list of keyframes, each carrying EVERY field
// for both directions at one `at_sec` -- all fields are pinned to the same set
// of times (see daemon Pattern.At). The editor, however, wants each field to be
// its own independent timeline: dragging rate's transition must not move or be
// blocked by delay's.
//
// So the editor DECOMPOSES the shared keyframes into one step-timeline per
// (direction, field), edits those independently, and RECOMPOSES them back into
// a keyframe array -- the union of every field's transition times, each
// keyframe re-carrying all fields -- before handing the pattern back to the
// daemon. The daemon is unchanged; it still receives an ordinary Pattern.
//
// The recompose is lossless for hold patterns: recompose(decompose(keys)) === keys.

import type { Keyframe, Pattern, Shape } from '@/types';

export type Dir = 'down' | 'up';

/** The six fields that get an editable lane. `loss_burst` rides silently with
 *  loss (it has no lane) but is still carried through decompose/recompose. */
export type FieldKey =
  | 'rate_mbps'
  | 'delay_ms'
  | 'jitter_ms'
  | 'loss_pct'
  | 'reorder_pct'
  | 'corrupt_pct';

/** One transition on a field's timeline: from `at_sec` on, the field holds `value`. */
export interface Step {
  at_sec: number;
  value: number;
}

/** Every numeric shape field a recompose must preserve. `loss_burst` is here so
 *  it survives even though it has no lane; it is coupled to loss in sanitize. */
const SHAPE_FIELDS = [
  'rate_mbps',
  'delay_ms',
  'jitter_ms',
  'loss_pct',
  'loss_burst',
  'reorder_pct',
  'corrupt_pct',
] as const;
type ShapeField = (typeof SHAPE_FIELDS)[number];

const DIRS: Dir[] = ['down', 'up'];
const GRID = 0.5;

/** Quantise to the half-second grid the daemon validates against. */
export function snap(s: number): number {
  return Math.round(s / GRID) * GRID;
}

function field(s: Shape, f: ShapeField): number {
  return (s[f] ?? 0) as number;
}

export type Timelines = Record<Dir, Record<ShapeField, Step[]>>;

/** Decompose one field's step timeline from the shared keyframes: a point at
 *  the first keyframe, then one wherever the value changes. */
export function decompose(keys: Keyframe[], dir: Dir, f: ShapeField): Step[] {
  if (!keys.length) return [{ at_sec: 0, value: 0 }];
  const steps: Step[] = [{ at_sec: keys[0].at_sec, value: field(keys[0][dir], f) }];
  for (let i = 1; i < keys.length; i++) {
    const v = field(keys[i][dir], f);
    if (v !== steps[steps.length - 1].value) steps.push({ at_sec: keys[i].at_sec, value: v });
  }
  return steps;
}

/** All 14 timelines (7 fields x 2 directions). */
export function decomposeAll(keys: Keyframe[]): Timelines {
  const t = { down: {}, up: {} } as Timelines;
  for (const d of DIRS) for (const f of SHAPE_FIELDS) t[d][f] = decompose(keys, d, f);
  return t;
}

/** The value a step timeline holds at time `t` -- the most recent step at or
 *  before it (matching the daemon's HOLD semantics in Pattern.At). */
export function valueAtStep(steps: Step[], t: number): number {
  if (!steps.length) return 0;
  let v = steps[0].value;
  for (const s of steps) {
    if (s.at_sec <= t + 1e-9) v = s.value;
    else break;
  }
  return v;
}

/** The pattern's length: the last keyframe's time. */
export function durOf(p: Pattern | null | undefined): number {
  const ks = p?.keys ?? [];
  return ks.length ? ks[ks.length - 1].at_sec : 0;
}

/** Cross-field invariants the daemon's validShape enforces. Independent field
 *  timelines can momentarily violate them (e.g. delay ramped to 0 under a live
 *  reorder), so every recomposed keyframe is repaired here rather than rejected.
 *  Each repair matches what netem can actually do. */
function sanitize(s: Shape): Shape {
  const out: Shape = { ...s };
  if (out.jitter_ms > out.delay_ms) out.jitter_ms = out.delay_ms; // jitter <= delay
  if (out.delay_ms <= 0) out.reorder_pct = 0; // no delay queue => nothing to reorder
  if ((out.loss_pct ?? 0) < 0.01 && (out.loss_burst ?? 0) > 0) out.loss_burst = 0; // burst needs loss
  return out;
}

function buildShape(fields: Record<ShapeField, Step[]>, at: number): Shape {
  const s = {} as Record<string, number>;
  for (const f of SHAPE_FIELDS) s[f] = valueAtStep(fields[f], at);
  return sanitize(s as unknown as Shape);
}

function shapeEq(a: Shape, b: Shape): boolean {
  return SHAPE_FIELDS.every((f) => field(a, f) === field(b, f));
}

/** The safety ceiling the daemon enforces (maxKeys). */
export const MAX_KEYS = 256;

/** Recompose the shared keyframe array from the per-field timelines: the sorted
 *  union of every field's transition times (plus 0 and `dur`), each keyframe
 *  carrying every field. Keyframes where nothing changed are pruned so the array
 *  stays minimal; the endpoints are always kept so DurSec and the >=2-key rule
 *  hold. */
export function recompose(tl: Timelines, dur: number): Keyframe[] {
  const end = Math.max(snap(dur), GRID);
  const set = new Set<number>([0, end]);
  for (const d of DIRS)
    for (const f of SHAPE_FIELDS)
      for (const s of tl[d][f]) {
        const at = snap(s.at_sec);
        if (at >= 0 && at <= end + 1e-9) set.add(at);
      }
  const times = [...set].sort((a, b) => a - b);
  const built = times.map((at) => ({
    at_sec: at,
    down: buildShape(tl.down, at),
    up: buildShape(tl.up, at),
    ease: 'hold' as const,
  }));

  const out: Keyframe[] = [];
  for (let i = 0; i < built.length; i++) {
    const k = built[i];
    const ends = i === 0 || i === built.length - 1;
    const prev = out[out.length - 1];
    if (!ends && prev && shapeEq(k.down, prev.down) && shapeEq(k.up, prev.up)) continue;
    out.push(k);
  }
  return out;
}

/** Run a mutation on one field's own timeline and recompose. The pattern grows
 *  if a step is moved past the current end. Never touches another field. */
export function withField(
  p: Pattern,
  dir: Dir,
  f: FieldKey,
  fn: (steps: Step[]) => void,
): Keyframe[] {
  const tl = decomposeAll(p.keys);
  fn(tl[dir][f]);
  let end = durOf(p);
  for (const s of tl[dir][f]) end = Math.max(end, snap(s.at_sec));
  return recompose(tl, end);
}

/** Move a field's transition in time, mirroring the keyframe ripple/penned rule
 *  but scoped to this field: default drags carry this field's LATER transitions
 *  along; alt (penned) slides within its own neighbours. The step at 0 is the
 *  field's starting value and is pinned, like keyframe 0. */
export function moveStep(steps: Step[], i: number, at: number, penned: boolean, maxSec: number): void {
  if (i <= 0 || i >= steps.length) return;
  const target = Math.max(0, snap(at));
  if (penned || i === steps.length - 1) {
    const lo = steps[i - 1].at_sec + GRID;
    const hi = i + 1 < steps.length ? steps[i + 1].at_sec - GRID : maxSec;
    steps[i].at_sec = Math.min(Math.max(target, lo), hi);
  } else {
    const lo = steps[i - 1].at_sec + GRID;
    const delta = Math.min(Math.max(target, lo), maxSec) - steps[i].at_sec;
    for (let n = i; n < steps.length; n++) steps[n].at_sec = snap(steps[n].at_sec + delta);
  }
  steps.sort((a, b) => a.at_sec - b.at_sec);
}

/** Add a transition at `at` carrying the field's currently-held value there, so
 *  adding a point never changes the timeline until it is dragged. */
export function addStep(steps: Step[], at: number): void {
  const t = snap(at);
  if (steps.some((s) => Math.abs(s.at_sec - t) < 1e-9)) return;
  steps.push({ at_sec: t, value: valueAtStep(steps, t) });
  steps.sort((a, b) => a.at_sec - b.at_sec);
}

/** Remove a transition. The step at 0 (the starting value) cannot be removed. */
export function removeStep(steps: Step[], i: number): void {
  if (i <= 0 || i >= steps.length) return;
  steps.splice(i, 1);
}

/** Set the value the field holds at time `t`: edit the step governing `t`, or
 *  create one there. */
export function setStepValue(steps: Step[], t: number, value: number): void {
  const at = snap(t);
  const i = steps.findIndex((s) => Math.abs(s.at_sec - at) < 1e-9);
  if (i >= 0) {
    steps[i].value = value;
    return;
  }
  steps.push({ at_sec: at, value });
  steps.sort((a, b) => a.at_sec - b.at_sec);
}

/** Set an entire Shape at one time (the card's sliders). Each field gets a step
 *  at `t`; fields left equal to their held value are pruned on recompose, so the
 *  sliders stay effectively per-field. */
export function setShapeAt(p: Pattern, dir: Dir, t: number, shape: Shape): Keyframe[] {
  const tl = decomposeAll(p.keys);
  const at = snap(t);
  for (const f of SHAPE_FIELDS) setStepValue(tl[dir][f], at, field(shape, f));
  return recompose(tl, Math.max(durOf(p), at));
}
