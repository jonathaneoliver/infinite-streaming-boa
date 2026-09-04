<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import type { Client, Shape, Series, ChartPrefs, Pattern } from '@/types';
import { CLEAN, DEVELOPER, PRESETS, ntopngUrl, patternFromPolicy } from '@/types';
import { setShapeAt } from '@/lib/pattern';
import ShapeSliders from './ShapeSliders.vue';
import SubClasses from './SubClasses.vue';
import LadderPanel from './LadderPanel.vue';
import PatternPanel from './PatternPanel.vue';
import TrafficChart from './TrafficChart.vue';
import { useViewportWidth } from '@/composables/useViewport';

const props = defineProps<{
  client: Client;
  series?: Series;
  ntopngPort?: number;
  /** Whether per-client link events (deauth/disassoc) can be driven right now
   *  -- true only when hostapd serves the AP. Gates the drop/nudge buttons. */
  linkControl?: boolean;
  collapsed?: boolean;
  /** Chart settings, shared by every card so devices stay comparable. */
  chart: ChartPrefs;
  /** Right-hand edge of every plot, held still while a chart is hovered. */
  now: number;
}>();
const emit = defineEmits<{
  shape: [dir: 'down' | 'up', shape: Shape];
  preset: [down: Shape, up: Shape];
  label: [string];
  reset: [];
  forget: [];
  linkDrop: [];
  linkNudge: [];
  linkDeadzone: [sec: number];
  linkSteer: [];
  toggle: [];
  addSub: [];
  removeSub: [string];
  patchSub: [string, Record<string, unknown>];
  subShape: [string, 'down' | 'up', Shape];
  hovering: [boolean];
  sweep: [service: string];
  stopSweep: [];
  removeLadder: [service: string];
  patternUpdate: [pattern: Pattern];
  patternRemove: [];
  patternPlay: [];
  patternStop: [];
}>();

// The chart props every plot on this card shares. Spread rather than repeated
// four times, so the sparkline on a folded row can never drift out of step with
// the full chart it summarises.
const chartProps = computed(() => ({
  // The client's own ceiling, for the "to PHY" axis. Per client rather than per
  // card setting, because two devices on the same radio can negotiate very
  // different rates -- an 802.11n phone and an ax laptop on one AP differ by
  // several times, and scaling both to the same number would flatter one and
  // crush the other.
  phy: props.client.station?.tx_phy_mbps ?? 0,
  windowMs: props.chart.rangeSec * 1000,
  now: props.now,
  yMode: props.chart.yMode,
  yManual: props.chart.yManual,
  showLive: props.chart.showLive,
  showSustained: props.chart.showSustained,
  sustainedSec: props.chart.sustainedSec,
}));

/**
 * Height of the two expanded plots.
 *
 * Doubled rather than made freely resizable: the point is more vertical
 * resolution when two rungs sit a few pixels apart, and one toggle keeps every
 * card the same height, which is what lets two devices be compared down the
 * page. The sparklines on a folded row are deliberately unaffected -- they
 * summarise, and a tall summary is a chart.
 */
/**
 * The card head's columns -- ONE grid, used folded and open alike.
 *
 * The head used to be two separate blocks: a positional grid when folded and a
 * flex row when open. Everything therefore shifted as the card was toggled, and
 * worse, the two had to be kept in step by hand and had already drifted -- the
 * MAC was on one and not the other. One element with one template removes both
 * problems by construction.
 *
 * The shape is: an identity group whose tracks are IDENTICAL in both states, a
 * flexible slack track, then a state-specific middle, then two shared trailing
 * tracks. Because the slack is a fraction it absorbs the whole difference
 * between the two middles, so the identity group is pinned at the left and the
 * conditioned badge and actions are pinned at the right, in both states.
 *
 * Two rules that are easy to break here:
 *
 *  - A cell may never be v-if'd out, only left EMPTY. The tracks are
 *    positional, so a missing cell slides every later one and the row stops
 *    lining up with its neighbours.
 *  - No track may be max-content. Every row is its own grid, so a
 *    content-sized track is measured per row and the list stops lining up.
 */
const vw = useViewportWidth();

/**
 * The identity columns, and which of them survive a narrow window.
 *
 * Every one of these started as minmax(0, ...) so the group would squeeze
 * before the charts did. That degrades well for one or two columns and badly
 * for nine: at 1200px the row truncated to "802...", "192.168...", "fc:9c:a..."
 * -- every field present, none of them readable, which is a worse answer than
 * showing fewer fields properly.
 *
 * So past a breakpoint columns are DROPPED rather than crushed, least useful
 * first. Dropping is safe here only because the cells are dropped WITH them:
 * the tracks are positional, so a cell without a track slides every later one.
 * cols and the template are driven from the same list for that reason.
 *
 * Breakpoints are in script rather than CSS because the template is built in
 * script -- it depends on which directions are shown and whether the card is
 * open, which no media query can see.
 */
// Folded and open rows have the SAME room for identity, because the open row
// reserves the folded tail's width rather than sizing its middle to fit -- see
// foldedTailPx. One budget for both states is what makes that true.
//
// This used to subtract 250px when folded, on the premise that a folded row
// "has less room for identity than an open one, at the same window width".
// That was correct then and is not now: it is the same 250-odd pixels either
// way. Leaving the subtraction in place kept the two states on different sides
// of these breakpoints, so a fold changed which columns existed and shifted the
// address by ~43px even once the tails matched.
const identityBudget = computed(() => vw.value);

const showIPv6Count = computed(() => identityBudget.value >= 1600);
const showAssoc = computed(() => identityBudget.value >= 1500);
const showPHY = computed(() => identityBudget.value >= 1400);
const showMAC = computed(() => identityBudget.value >= 1300);
const showSignal = computed(() => identityBudget.value >= 1200);

// The ntopng links are in the SHARED trailing group, so this is keyed off the
// raw window width rather than the folded-adjusted budget: dropping them in one
// state and not the other would pull the conditioned badge and the actions out
// of alignment, which the shared group exists to prevent.
const showLinks = computed(() => vw.value >= 1300);

const IDENTITY_COLS = computed(() => [
  '30px',                 // fold toggle -- first, so it never moves
  '14px',                 // presence dot
  '170px',                // name -- fixed, so what follows holds its x
  // 84px, not 58: the badge carries the radio name ("wlan-usb"), not the word
  // "wifi", because which radio a client is on is the fact that matters once
  // the box serves two bands.
  'minmax(0, 84px)',      // medium / radio badge
  // The radio summary is never dropped: which radio a client is on, and what
  // that radio is doing, is the reason this row exists on a two-band box.
  //
  // BEFORE the address, because that is the order the template renders them in.
  // These two were transposed: the template emits radio summary then address,
  // while this list named address first, so the address landed in the radio
  // summary's minmax(0, 168px) track. With no lower bound it collapsed under
  // the folded row's extra ~326px of sparklines and figures, and its left edge
  // moved as the card toggled -- 99px at a 1200px window, 146px at 1100px,
  // measured. The radio summary meanwhile sat in a 268px track it did not need,
  // empty on every wired client.
  'minmax(0, 168px)',     // "ch 40 · 80 MHz · 802.11ax"
  // 268px fits a full IPv4 with room to spare and most of an IPv6; longer
  // addresses ellipsise and carry the full value in a tooltip.
  'minmax(96px, 268px)',  // address
  ...(showIPv6Count.value ? ['minmax(0, 62px)'] : []),
  ...(showMAC.value ? ['minmax(0, 132px)'] : []),
  ...(showSignal.value ? ['minmax(0, 96px)'] : []),
  ...(showPHY.value ? ['minmax(0, 76px)'] : []),
  // 104px, not 92: "assoc 42m 35s" is the longest common form and 92 clipped
  // it to "assoc 42m 3...". A column narrower than the value it always holds is
  // not a graceful degradation, it is a bug that only shows on real data.
  ...(showAssoc.value ? ['minmax(0, 104px)'] : []),
]);

// Every identity track is minmax(0, ...) so the group squeezes -- and, past a
// point, disappears -- before the sparklines and throughput figures give up any
// width. Everything in it is recoverable by expanding the card; a clipped rate
// figure is not, and it is what the row exists to show.
// Must match `gap` on .card-head below: the open row reserves the folded row's
// tail width, and that total includes the gaps between its tracks.
const HEAD_GAP_PX = 8;

/** The folded row's tail: a sparkline and a figure per direction, then the unit. */
const foldedTail = computed(() => [
  ...(props.chart.showDown ? [76, 68] : []),
  ...(props.chart.showUp ? [76, 68] : []),
  38,
]);

/**
 * What that tail occupies in total, its internal gaps included.
 *
 * The OPEN row reserves exactly this for its single middle cell, rather than
 * sizing it to its contents. Both states then demand the same width, so the
 * identity tracks squeeze by the same amount and nothing shifts as a card is
 * toggled -- which is what the grid in .card-head exists to guarantee.
 *
 * Sizing the open middle to its contents instead left the folded row asking for
 * ~326px more than the open one. Below about 1400px that difference came out of
 * the identity tracks, which are minmax(0, ...) and squeeze first by design, so
 * folding a card dragged the address leftwards: 95px at a 1200px window and
 * 146px at 1100px, measured, and 0 at 1400px and above where there was slack to
 * absorb it. The slack track alone could not fix this -- it only absorbs a
 * difference that exists, and the difference was the whole problem.
 */
const foldedTailPx = computed(() =>
  foldedTail.value.reduce((total, px) => total + px, 0)
  + (foldedTail.value.length - 1) * HEAD_GAP_PX);

const headCols = computed(() => [
  ...IDENTITY_COLS.value,
  'minmax(0, 1fr)', // slack: absorbs what is left once both middles agree
  ...(props.collapsed
    // Folded: sparkline and figure per direction, then the unit.
    ? foldedTail.value.map(px => `${px}px`)
    // Open: one cell holding the reset action, laid out as a flex row inside so
    // its contents cannot push a track around -- held to the folded tail's
    // width so the identity group's budget does not change as the card opens.
    : [`${foldedTailPx.value}px`]),
  // Shared from here on, so none of it moves as the card opens.
  ...(showLinks.value ? ['minmax(0, 124px)'] : []), // ntopng deep links
  // 106px: the badge is uppercase 11px with letter-spacing and padding, and at
  // 92px it read "CONDITION..." -- a truncated warning is a worse warning.
  '106px',            // conditioned badge
  '56px',             // actions
].join(' '));

/** Two directions side by side, or one taking the full width. */
const dirCols = computed(() =>
  props.chart.showDown && props.chart.showUp ? '1fr 1fr' : '1fr');

const EXPANDED_H = 196;
const expandedH = computed(() => (props.chart.tallCharts ? EXPANDED_H * 2 : EXPANDED_H));

const open = ref(false);

/** "ch 40 · 80 MHz · 802.11ax", the same phrasing the bridge diagram uses for
 *  the radio node, so the two read as descriptions of the same thing. Empty
 *  for a wired client and for a wireless one not currently on a radio. */
const radioSummary = computed(() => {
  const r = props.client.radio_on;
  if (!r) return '';
  return [
    r.channel ? `ch ${r.channel}` : '',
    r.width_mhz ? `${r.width_mhz} MHz` : '',
    r.mode || '',
  ].filter(Boolean).join(' · ');
});

// Signal quality bands are the conventional Wi-Fi ones: above -60 dBm is a
// strong link, below -75 is where retries and rate-drops begin to dominate and
// the radio's own behaviour will start to confound whatever is being tested.
/** The two bands are told apart by colour as well as by label, the same way
 *  downlink and uplink are: which band a client is on is the most confusable
 *  thing on this row once both radios serve. */
const bandClass = computed(() => {
  const b = props.client.radio_on?.band;
  return b === '5GHz' ? 'band5' : b === '2.4GHz' ? 'band24' : '';
});

// Signal quality bands are the conventional Wi-Fi ones: above -60 dBm is a
// strong link, below -75 is where retries and rate-drops begin to dominate and
// the radio's own behaviour will start to confound whatever is being tested.
const signalClass = computed(() => {
  const s = props.client.station?.signal_dbm ?? 0;
  if (!s) return '';
  if (s > -60) return 'ok';
  if (s > -75) return 'warn';
  return 'bad';
});

// How long the station has been continuously associated. It resets to ~0 when
// the link drops and re-associates, so after a drop/nudge it visibly falls to a
// few seconds -- which is the ground-truth confirmation the event landed.
const connectedLabel = computed(() => {
  const s = props.client.station?.connected_sec ?? 0;
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
});

// Transient acknowledgement that a link event was sent. The lasting proof is
// the connected-time above resetting; this just confirms the click dispatched.
const linkFlash = ref<'drop' | 'nudge' | 'deadzone' | 'steer' | null>(null);
let linkFlashTimer: ReturnType<typeof setTimeout> | undefined;
function flashLink(kind: 'drop' | 'nudge' | 'deadzone' | 'steer') {
  linkFlash.value = kind;
  clearTimeout(linkFlashTimer);
  linkFlashTimer = setTimeout(() => (linkFlash.value = null), 1400);
}
function fireLink(kind: 'drop' | 'nudge') {
  if (kind === 'drop') emit('linkDrop');
  else emit('linkNudge');
  flashLink(kind);
}
// deadzone is a sustained outage, so it carries a duration (default 10s) --
// long enough to drain a player's buffer, unlike a single drop.
const deadzoneSec = ref(10);
function fireDeadzone() {
  emit('linkDeadzone', deadzoneSec.value);
  flashLink('deadzone');
}

/*
 * Ask THIS client to move to the other radio (802.11v).
 *
 * The gentlest of the four: drop and nudge take the link away and let the
 * client choose where to land, where this names a destination and leaves the
 * link up. A client that honours it moves without ever disconnecting.
 *
 * "sent" is all this can honestly report. The request either reaches the client
 * or it does not; whether the client ACTS on it shows up as the radio on this
 * card changing, which is the thing actually worth watching -- and a device
 * that ignores transition requests is a finding, not a failure.
 */
function fireSteer() {
  emit('linkSteer');
  flashLink('steer');
}

// Past a few seconds of blackout a phone stops waiting and leaves the AP for
// another network -- iOS gives up around 3s. That is a different outcome from a
// rebuffer: the device is then off boa's Wi-Fi entirely, so it stops appearing
// as traffic and cannot be conditioned until it rejoins THIS AP on its own.
const ROAM_AWAY_SEC = 3;
const deadzoneRisky = computed(() => deadzoneSec.value >= ROAM_AWAY_SEC);

/**
 * The downlink cap actually in force, which is NOT always the stored policy.
 *
 * A ladder sweep drives the cap itself and deliberately never writes to stored
 * policy, so that an abandoned or crashed run restores the operator's settings
 * by simply being forgotten. The cost is that the policy the UI usually draws
 * from says "unlimited" while the kernel is throttling hard.
 *
 * Drawing the policy in that state makes the interface claim a cap is not
 * applied when it is -- the inverse of the failure the PRD names, and just as
 * dishonest. The charts follow what is enforced.
 */
const sweeping = computed(() => props.client.sweep?.state === 'running');
const patRun = computed(() => props.client.pattern_run);
const playing = computed(() => patRun.value?.state === 'running');
// A link button highlights only while the pattern is PLAYING and that kind is
// firing at the playhead -- a deadzone for the whole of its block, a drop/nudge
// for a short window around its pulse (wide enough to catch at the snapshot
// cadence). Not while it is merely present in a stopped pattern, and not from a
// press (that is the separate transient flash). It returns to plain as the
// playhead leaves the event.
const activeLinkKinds = computed(() => {
  const s = new Set<string>();
  const run = patRun.value;
  if (run?.state !== 'running') return s;
  for (const l of props.client.policy.pattern?.links ?? []) {
    const window = Math.max(l.dur_sec ?? 0, 1);
    if (run.pos_sec >= l.at_sec && run.pos_sec <= l.at_sec + window) s.add(l.kind);
  }
  return s;
});
const downCap = computed(() => {
  if (sweeping.value) return props.client.sweep?.cap_mbps ?? 0;
  // A playing pattern drives the cap along its timeline without writing it, on
  // the same terms as a sweep, so the chart's cap line has to follow the run
  // rather than the policy. This is what makes a step-down visible AGAINST the
  // moment the cap moved -- the one thing the timeline exists to show.
  if (playing.value) return patRun.value?.down.rate_mbps ?? 0;
  return props.client.policy.down.rate_mbps;
});

/*
 * The pattern being edited, which is not always what the server has yet.
 *
 * Keyframe edits arrive as slider drags and are debounced like any other, so
 * between the first movement and the write landing the card must draw its own
 * pending version -- otherwise every drag snaps back for 200 ms, and an edit
 * made inside that window would be computed from a timeline missing the edit
 * before it. The draft is dropped once the server's copy matches it.
 */
const patDraft = ref<Pattern | null>(null);
const pattern = computed<Pattern | null>(
  () => patDraft.value ?? props.client.policy.pattern ?? null,
);
watch(
  () => JSON.stringify(props.client.policy.pattern ?? null),
  (stored) => {
    if (patDraft.value && JSON.stringify(patDraft.value) === stored) {
      patDraft.value = null;
    }
  },
);

/**
 * Which MOMENT (seconds) the sliders are editing, or null for the stored policy.
 * A time, not a keyframe index, because the pattern panel now edits each field
 * on its own timeline and recomposes the keyframes -- a time is stable across
 * that, an index is not.
 *
 * Never a keyframe while a run is playing: the controls are then reporting what
 * is enforced, and accepting an edit while displaying the enforced value would
 * write one thing where the operator could see another.
 */
const patSelected = ref<number | null>(null);
const editKey = computed(() =>
  !playing.value && patSelected.value != null
    ? (pattern.value?.keys.find((k) => Math.abs(k.at_sec - patSelected.value!) < 1e-6) ?? null)
    : null,
);

function applyPattern(next: Pattern) {
  patDraft.value = next;
  emit('patternUpdate', next);
}

function createPattern() {
  applyPattern(patternFromPolicy(props.client.policy.down, props.client.policy.up));
}

function removePattern() {
  patDraft.value = null;
  patSelected.value = null;
  emit('patternRemove');
}

/** Route a slider to the selected moment (setting that field at that time), or
 *  to the stored policy. */
function onShape(dir: 'down' | 'up', s: Shape) {
  if (editKey.value && pattern.value && patSelected.value != null) {
    applyPattern({ ...pattern.value, keys: setShapeAt(pattern.value, dir, patSelected.value, s) });
    return;
  }
  emit('shape', dir, s);
}

// Presets follow the sliders: with a moment selected, "3G" means "this moment
// is 3G", the shortest route from a named link to a timeline. Both directions
// are set at that time.
function onPreset(down: Shape, up: Shape) {
  if (editKey.value && pattern.value && patSelected.value != null) {
    const t = patSelected.value;
    let keys = setShapeAt(pattern.value, 'down', t, down);
    keys = setShapeAt({ ...pattern.value, keys }, 'up', t, up);
    applyPattern({ ...pattern.value, keys });
    return;
  }
  emit('preset', down, up);
}

// Both a sweep and a pattern drive the cap. The daemon refuses the second one
// rather than letting them fight; the card says so before the click.
const patBlocked = computed(() => {
  if (!props.client.present || !props.client.shapeable) {
    return 'The device has to be connected and addressed before a pattern can condition it.';
  }
  if (sweeping.value) {
    return 'A ladder sweep is driving this device. Stop it before playing a pattern.';
  }
  return '';
});

/**
 * What the controls display, which is one of three different things.
 *
 * ENFORCED, while a sweep or a pattern is driving this device. Neither writes
 * to stored policy -- so that an abandoned or crashed run restores the
 * operator's settings by being forgotten -- and drawing the policy in that
 * state would have the interface claim a cap is not applied when it is. The
 * controls are read-only here: reporting, not accepting input.
 *
 * A KEYFRAME, when one is selected on the timeline. The sliders are the
 * keyframe editor; the playhead chooses which moment they describe.
 *
 * The STORED POLICY otherwise, which is the ordinary case.
 *
 * A sweep's override is clean apart from the cap -- it suspends delay, jitter
 * and loss for the duration -- so those read zero, because that is what the
 * kernel has.
 */
const downShape = computed<Shape>(() => {
  if (sweeping.value) {
    return { ...CLEAN, rate_mbps: downCap.value };
  }
  if (playing.value) return patRun.value!.down;
  return editKey.value ? editKey.value.down : props.client.policy.down;
});

const upShape = computed<Shape>(() => {
  if (playing.value) return patRun.value!.up;
  return editKey.value ? editKey.value.up : props.client.policy.up;
});

const conditioned = computed(() => {
  const p = props.client.policy;
  const dirty = (s: Shape) =>
    s.rate_mbps > 0 || s.delay_ms > 0 || s.jitter_ms > 0 || s.loss_pct > 0;
  return p.enabled && (dirty(p.down) || dirty(p.up));
});

/**
 * The card title toggles the fold, in both directions.
 *
 * Two things are deliberately excluded:
 *
 * Clicks on something interactive. The name is an editable input and an absent
 * device carries a "forget" button, both of which sit in the title; swallowing
 * their clicks would make them unusable.
 *
 * Clicks that conclude a text selection. Dragging across a MAC address to copy
 * it ends in a click on the header, and folding the card away underneath that
 * would be infuriating. Checking the selection is emptier than disabling the
 * interaction, which was the first attempt.
 */
function onHeadClick(e: MouseEvent) {
  const el = e.target as HTMLElement | null;
  if (el?.closest('input, button, a, select, textarea')) return;
  if ((window.getSelection()?.toString() ?? '').length > 0) return;
  emit('toggle');
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${u[i]}`;
}
</script>

<template>
  <div :class="['card', { absent: !client.present }]">
    <!-- The folded row is its own markup rather than the full header with
         pieces hidden. A grid only lines up if every row has the SAME cells,
         and the optional fields -- medium, address, conditioned -- would drop
         out and shift everything after them. Each slot is always rendered here
         and simply left empty when it does not apply. -->
    <div
      class="card-head" :class="{ folded: collapsed }"
      :style="{ gridTemplateColumns: headCols }"
      @click="onHeadClick"
    >
      <button
        class="fold-toggle ghost" @click="emit('toggle')"
        :title="collapsed ? 'Expand this device' : 'Fold this device away'"
        :aria-expanded="!collapsed"
      >{{ collapsed ? '▸' : '▾' }}</button>

      <span
        class="dot" :class="client.present ? 'live' : 'off'"
        :title="client.present ? 'Connected now' : 'Not currently connected'"
      ></span>

      <input
        class="name" :value="client.label"
        @change="emit('label', ($event.target as HTMLInputElement).value)"
      />

      <!-- WHICH radio, not merely "wifi".
           The box serves both bands at once, and a client on 2.4GHz at 20MHz
           behaves nothing like one on 5GHz at 80MHz -- so "wifi" alone stopped
           being an answer the moment the second radio came up. -->
      <span class="cell">
        <!-- The BAND, not the interface name. "5 GHz" is the fact that means
             something about how this client will behave; "wlan-usb" is an
             implementation detail of which adapter happens to serve it, and it
             is one hover away in the tooltip. -->
        <span
          v-if="client.medium" class="badge" :class="[client.medium, bandClass]"
          :title="client.radio_on
            ? `On ${client.radio_on.iface}, channel ${client.radio_on.channel}`
            : (client.port ? `On ${client.port}` : '')"
        >{{ client.radio_on?.band || client.port || client.medium }}</span>
      </span>

      <!-- What that radio is doing, in the same words the diagram uses. Its own
           column rather than a second line: the head is one row by design, and
           a wrapping cell makes this card taller than its neighbours. -->
      <span class="cell meta num" :title="client.radio_on ? 'The access point this client is associated to' : ''">
        {{ radioSummary }}
      </span>

      <span
        class="cell meta num addr"
        :title="client.ip || (client.ipv6?.length ? client.ipv6[0] : '')"
      >
        {{ client.ip || (client.ipv6?.length ? client.ipv6[0] : 'no address yet') }}
      </span>

      <!-- Privacy extensions give a device several v6 addresses at once, so
           the count matters more than any one value; all of them are shaped.
           v-if paired with its track in IDENTITY_COLS: a cell without a track
           slides every later one and the row stops lining up. -->
      <span
        v-if="showIPv6Count"
        class="cell meta num"
        :title="client.ipv6?.length ? 'IPv6, all conditioned:\n' + client.ipv6.join('\n') : ''"
      >{{ client.ipv6?.length ? `+${client.ipv6.length} IPv6` : '' }}</span>

      <!-- The MAC, on the folded row as well as the expanded card.
           It is the identity everything is keyed by -- policy, ladders and
           patterns all hang off it, not off the address -- and a device that
           rotates its private Wi-Fi address comes back as a different client
           with no conditioning. Spotting that from a folded list is the point;
           having to expand every card to compare MACs is not. -->
      <span v-if="showMAC" class="cell meta num mac" :title="client.mac">{{ client.mac }}</span>

      <!-- Signal, and the same fallback the expanded head uses. Only shown when
           the driver actually reports it: the Pi's Broadcom chip gives no
           per-station RSSI in AP mode, and printing "0 dBm" is a
           confident-looking lie. Wired clients have no station at all, so the
           cell stays empty rather than being dropped -- the columns are
           positional, and a missing cell slides every later one left. -->
      <span v-if="showSignal" class="cell meta num" :class="client.station?.signal_dbm ? signalClass : ''">
        <template v-if="client.station?.signal_dbm">
          {{ client.station.signal_dbm }} dBm
        </template>
        <template v-else-if="client.station">
          <span title="This radio reports no per-station signal level in AP mode; transmit failures stand in as the link-quality indicator"
          >tx-fail {{ client.station.tx_failed.toLocaleString() }}</span>
        </template>
      </span>

      <!-- PHY rate, labelled. It routinely reads 400+ Mbps on a link moving
           2 Mbps, so it is never allowed to appear as a bare number next to the
           throughput figures further along this same row. -->
      <span
        v-if="showPHY"
        class="cell meta num"
        :title="client.station ? 'Negotiated radio modulation rate, NOT achieved throughput' : ''"
      >{{ client.station ? `PHY ${client.station.tx_phy_mbps.toFixed(0)}` : '' }}</span>

      <!-- Association age, folded and open alike. It resets to a few seconds
           when a link event lands, which is the ground-truth confirmation that
           a drop or nudge actually happened -- worth being able to watch down a
           list of devices rather than only on the one card that is open. -->
      <span
        v-if="showAssoc"
        class="cell meta num"
        :title="client.station ? 'How long this device has been continuously associated. It resets when the link drops and re-associates, so it falls to a few seconds right after a drop or nudge.' : ''"
      >{{ client.station ? `assoc ${connectedLabel}` : '' }}</span>

      <!-- The slack. Everything before it is the identity group and sits at the
           same x in both states; everything after it is right-aligned by the
           grid. Putting the flexible track here is what lets the two states
           carry different middles without moving either end. -->
      <span class="cell"></span>

      <template v-if="collapsed">
      <template v-if="chart.showDown">
        <span class="cell spark">
          <TrafficChart
            v-bind="chartProps"
            :t="series?.t ?? []" :data="series?.down ?? []" :caps="series?.cap ?? []"
            color="var(--down)" label="Downlink"
            :cap="downCap" :height="24" compact
          />
        </span>
        <span class="cell num val" style="color: var(--down)">
          &darr;{{ client.down_counters.throughput_mbps.toFixed(2) }}
        </span>
      </template>

      <template v-if="chart.showUp">
        <span class="cell spark">
          <TrafficChart
            v-bind="chartProps"
            :t="series?.t ?? []" :data="series?.up ?? []"
            color="var(--up)" label="Uplink"
            :cap="client.policy.up.rate_mbps" :height="24" compact
          />
        </span>
        <span class="cell num val" style="color: var(--up)">
          &uarr;{{ client.up_counters.throughput_mbps.toFixed(2) }}
        </span>
      </template>

      <span class="cell meta unit">Mbps</span>
      </template>

      <!-- The open card's middle: just the reset action now that the deep links
           are shared. One cell, laid out as a flex row inside, so whatever it
           holds cannot push a grid track around. -->
      <template v-else>
        <span class="cell tail">
          <button v-if="conditioned" @click="emit('reset')">reset</button>
        </span>
      </template>

      <!-- The last three tracks are shared by both states and carry the same
           widths, and because the slack above is a fraction they end at the
           same x -- so none of them moves as the card opens. -->

      <!-- Deep links into ntopng for THIS device. Shown only when ntopng is
           answering and the client has an address, since both are required for
           the filtered view to resolve to anything. Folded too: jumping to a
           device's flows is a thing you want from the list, not something worth
           opening a card for first. -->
      <span v-if="showLinks" class="cell tail">
        <template v-if="ntopngPort && client.ip">
          <a
            class="ntop-link"
            :href="ntopngUrl(ntopngPort, '/lua/host_details.lua', { host: client.ip })"
            target="_blank" rel="noopener"
            :title="`Traffic breakdown for ${client.ip} in ntopng`"
          >traffic ↗</a>
          <a
            class="ntop-link"
            :href="ntopngUrl(ntopngPort, '/lua/flows_stats.lua', { host: client.ip })"
            target="_blank" rel="noopener"
            :title="`Live flows for ${client.ip} in ntopng, labelled by application`"
          >flows ↗</a>
        </template>
      </span>

      <span class="cell">
        <span v-if="conditioned" class="badge" style="color: var(--warn)">
          conditioned
        </span>
      </span>

      <span class="cell">
        <!-- Only offered for a device that is not here: forgetting one that is
             present just makes it reappear unconfigured a second later. -->
        <button v-if="!client.present" class="ghost" @click="emit('forget')">
          forget
        </button>
      </span>

    </div>

    <!-- Folding is presentation only: the card keeps receiving live updates,
         so the summary above stays current and expanding shows no gap. -->
    <template v-if="!collapsed">
    <div v-if="!client.shapeable && client.present" class="notice bad" style="margin: 10px 14px">
      This device has no IP address yet, so it cannot be conditioned. Traffic
      filters match on addresses, and there is nothing to match on until it
      finishes joining the network.
    </div>

    <div class="presets" style="padding: 10px 14px 0">
      <button
        v-for="p in PRESETS" :key="p.name"
        :title="p.note"
        @click="onPreset({ ...p.down }, { ...p.up })"
      >
        {{ p.name }}
      </button>
    </div>

    <div class="dirs" :style="{ marginTop: '10px', gridTemplateColumns: dirCols }">
      <div v-if="chart.showDown" class="dir down">
        <h3>
          Downlink <span class="meta">to device</span>
          <span v-if="sweeping" class="badge" style="color: var(--warn)">
            swept &middot; {{ downCap.toFixed(2) }} Mbps
          </span>
          <span v-if="editKey" class="badge" style="color: var(--down)">
            at {{ editKey.at_sec }}s
          </span>
          <span class="readout num">
            {{ client.down_counters.throughput_mbps.toFixed(2) }}
            <small>Mbps</small>
          </span>
        </h3>
        <TrafficChart
          v-bind="chartProps"
          :t="series?.t ?? []" :data="series?.down ?? []" :caps="series?.cap ?? []"
          color="var(--down)" label="Downlink"
          :cap="downCap" :height="expandedH"
          @hovering="(v: boolean) => emit('hovering', v)"
        />
        <p v-if="sweeping" class="meta swept-note">
          These controls are showing what the sweep is enforcing, not your saved
          settings — those are untouched and return when it ends.
        </p>
        <ShapeSliders
          :shape="downShape" dir="down"
          :disabled="!client.shapeable || sweeping || playing"
          @update="(s) => onShape('down', s)"
        />
      </div>

      <div v-if="chart.showUp" class="dir up">
        <h3>
          Uplink <span class="meta">from device</span>
          <span v-if="editKey" class="badge" style="color: var(--up)">
            at {{ editKey.at_sec }}s
          </span>
          <span class="readout num">
            {{ client.up_counters.throughput_mbps.toFixed(2) }}
            <small>Mbps</small>
          </span>
        </h3>
        <TrafficChart
          v-bind="chartProps"
          :t="series?.t ?? []" :data="series?.up ?? []"
          color="var(--up)" label="Uplink"
          :cap="playing ? (patRun?.up.rate_mbps ?? 0) : client.policy.up.rate_mbps"
          :height="expandedH"
          @hovering="(v: boolean) => emit('hovering', v)"
        />
        <ShapeSliders
          :shape="upShape" dir="up"
          :disabled="!client.shapeable || playing"
          @update="(s) => onShape('up', s)"
        />
      </div>
    </div>

    <!-- Group A link events: an impairment on the ASSOCIATION, not the packets,
         and not tied to a direction -- so they get their own section under both
         directions' controls rather than living inside either. Only when
         hostapd serves the AP (caps.link_control) and the client is present. -->
    <!-- Wi-Fi only: drop/nudge/deadzone act on the 802.11 association, which a
         wired client on lan0 does not have. -->
    <div v-if="linkControl && client.present && client.medium === 'wifi'" class="link-events">
      <span class="link-label">Wi-Fi</span>
      <button
        class="ghost" :class="{ flash: linkFlash === 'drop', active: activeLinkKinds.has('drop') }" @click="fireLink('drop')"
        title="Deauthenticate: take this client's Wi-Fi link down; it reconnects on its own"
      >{{ linkFlash === 'drop' ? 'sent' : 'drop' }}</button>
      <button
        class="ghost" :class="{ flash: linkFlash === 'nudge', active: activeLinkKinds.has('nudge') }" @click="fireLink('nudge')"
        title="Disassociate: the softer 802.11 disconnect, usually a quicker recovery than drop"
      >{{ linkFlash === 'nudge' ? 'sent' : 'nudge' }}</button>
      <!-- Only when the daemon says there is somewhere to send it. A transition
           request has to NAME a destination access point, so on a box serving
           one radio there is nothing to offer and the button is absent rather
           than present-and-failing. -->
      <button
        v-if="client.steer_to"
        class="ghost" :class="{ flash: linkFlash === 'steer' }" @click="fireSteer"
        :title="`Ask this client to move to ${client.steer_to} (802.11v BSS transition). `
          + `The link stays up and it may refuse — whether this device honours a `
          + `transition request is the thing being tested.`"
      >{{ linkFlash === 'steer' ? 'sent' : 'steer' }}</button>
      <button
        class="ghost" :class="{ flash: linkFlash === 'deadzone', active: activeLinkKinds.has('deadzone') }" @click="fireDeadzone"
        title="Hold the link down for the duration -- long enough to drain a player's buffer and force a rebuffer, unlike a single drop"
      >{{ linkFlash === 'deadzone' ? 'sent' : `deadzone ${deadzoneSec}s` }}</button>
      <input
        type="number" min="1" max="300" step="1" v-model.number="deadzoneSec"
        class="dz-dur" title="deadzone length in seconds" aria-label="deadzone seconds"
      />
      <span class="link-hint meta">watch <b>assoc</b> above reset when it lands</span>
    </div>
    <!-- A long blackout does not rebuffer, it evicts: the device leaves this AP
         and boa can no longer see or shape it until it comes back. -->
    <p v-if="linkControl && client.present && client.medium === 'wifi' && deadzoneRisky" class="meta dz-warn">
      A deadzone this long can make the device give up on this Wi-Fi and switch
      to another network (iOS around 3s). It then leaves boa entirely — not just
      shown offline, but gone: no traffic and nothing to condition until it
      rejoins the Pi's Wi-Fi on its own.
    </p>

    <!-- The timeline sits directly under the controls that author it. The
         sliders above ARE the keyframe editor, and putting the playhead
         anywhere else would separate the control from the thing it edits. -->
    <PatternPanel
      :pattern="pattern"
      :mac="client.mac"
      :rev="client.policy?.rev ?? 0"
      :ladders="client.policy?.ladders ?? []"
      :run="client.pattern_run"
      :selected-sec="patSelected"
      :can-play="patBlocked === ''"
      :blocked="patBlocked"
      @update="applyPattern"
      @create="createPattern"
      @remove="removePattern"
      @play="emit('patternPlay')"
      @stop="emit('patternStop')"
      @select="(i: number | null) => (patSelected = i)"
      @changed="patDraft = null; patSelected = null"
    />

    <!-- Ladders sit above the fold, not behind the counters toggle: a sweep is
         a deliberate action someone came to the page to start, and one that
         takes minutes needs its progress visible without hunting for it.

         Behind ?developer=1, because that deliberate action is a measurement
         rather than part of conditioning a device -- see DEVELOPER. Hidden, not
         disabled: there is nothing here to explain to someone who did not come
         looking for it. A sweep already running still reports itself through
         the controls, which say they are showing what the sweep is enforcing. -->
    <LadderPanel
      v-if="DEVELOPER"
      :client="client"
      @sweep="(svc: string) => emit('sweep', svc)"
      @stop-sweep="emit('stopSweep')"
      @remove-ladder="(svc: string) => emit('removeLadder', svc)"
    />

    <div class="card-foot">
      <span class="spacer"></span>
      <button class="ghost" @click="open = !open">
        {{ open ? 'hide' : 'show' }} counters &amp; rules
      </button>
    </div>

    <div v-if="open">
      <div class="scroll-x" style="padding: 12px 14px">
        <table class="counters">
          <thead>
            <tr>
              <th>direction</th>
              <th>throughput</th>
              <th>cap enforced</th>
              <th>bytes</th>
              <th>packets</th>
              <th>drops</th>
              <th title="Times the class hit its ceiling. Expected on a throttled link — not a fault.">
                overlimits
              </th>
              <th title="Bytes queued right now: the honest congestion signal.">backlog</th>
              <th>qlen</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in [
              { k: 'down', label: 'downlink', v: client.down_counters },
              { k: 'up', label: 'uplink', v: client.up_counters },
            ]" :key="c.k">
              <td :style="{ color: c.k === 'down' ? 'var(--down)' : 'var(--up)' }">
                {{ c.label }}
              </td>
              <td class="num">{{ c.v.throughput_mbps.toFixed(2) }} Mbps</td>
              <td class="num">
                {{ c.v.cap_mbps > 5000 ? 'unlimited' : c.v.cap_mbps.toFixed(1) + ' Mbps' }}
              </td>
              <td class="num">{{ fmtBytes(c.v.bytes) }}</td>
              <td class="num">{{ c.v.packets.toLocaleString() }}</td>
              <td class="num" :style="c.v.drops ? 'color:var(--bad)' : ''">
                {{ c.v.drops.toLocaleString() }}
              </td>
              <td class="num meta">{{ c.v.overlimits.toLocaleString() }}</td>
              <td class="num">{{ c.v.backlog.toLocaleString() }}</td>
              <td class="num">{{ c.v.qlen.toLocaleString() }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <SubClasses
        :client="client" :sub-counters="client.sub_counters"
        @add="emit('addSub')"
        @remove="(id) => emit('removeSub', id)"
        @patch="(id, p) => emit('patchSub', id, p)"
        @shape="(id, dir, s) => emit('subShape', id, dir, s)"
      />
    </div>
    </template>
  </div>
</template>

<style scoped>
/* A control that is reporting rather than accepting input must not look
   editable, but it must still be readable -- the whole point is watching it
   move. Dimmed, not hidden. */
.swept-note {
  color: var(--warn);
  margin: 6px 0 0;
}
.dz-warn {
  color: var(--warn);
  margin: 6px 0 0;
}
/* Editing a keyframe used to be announced by a paragraph between the chart and
   the sliders. It said the right thing and said it in the wrong place: the line
   appeared and vanished with the selection, moving every control below it --
   and it appeared exactly when a lane was being dragged, so the panel shifted
   under the hand doing the dragging.
   It is a badge in the heading now, beside the one a sweep uses. The headings
   are always there, so nothing reflows, and it sits with the sliders whose
   destination it is describing. */

.fold-summary { display: flex; align-items: center; gap: 6px; flex: none; }
/* A folded row must not wrap. A ragged two-line list is harder to scan than a
   dense one-line one, which is the entire reason for folding. */
.card-head { cursor: pointer; }
/* The name stays a text field, so it must not inherit the row's pointer. */
.card-head .name { cursor: text; }

/* A grid in BOTH states, folded and open, so the identity fields hold their x
   as a card is toggled and nothing slides sideways under the pointer.
   Overrides the global flex `.card-head`.

   The tracks themselves are NOT restated here. They depend on which directions
   are shown and on whether the card is open, so they are built in headCols and
   applied inline -- and an inline style always wins, which meant the copy that
   used to live here was dead the moment it disagreed with the script. It had
   already drifted. One source only. */
.card-head {
  display: grid;
  align-items: center;
  gap: 8px;
  overflow: hidden;
}
.card-head.folded:hover { background: var(--panel); }
/* The open card's middle: deep links and reset, in a row inside one track. */
.card-head .tail { display: flex; align-items: center; gap: 10px; }
/* nowrap on every cell, not just the ones that looked like they needed it.
   A squeezed column wraps to a second line by default, which makes the row
   taller than its neighbours and breaks the even rhythm the folded list exists
   for -- "+1 IPv6" and "-61 dBm" both did exactly that at 1180px. Truncating is
   the right failure here: everything on this row is recoverable by expanding
   the card. */
.card-head .cell {
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.card-head .name { text-overflow: ellipsis; white-space: nowrap; }
/* Dimmer than the address: the MAC is the identity, but the address is what
   someone is usually reading down the list for. */
.card-head .mac { color: var(--ink-faint); }
/* 5GHz and 2.4GHz told apart at a glance. Not the direction pair's blue and
   orange -- those mean downlink and uplink everywhere else in this interface,
   and reusing them here would make band look like direction. */
.badge.band5 { color: #7dd3fc; border-color: color-mix(in srgb, #7dd3fc 40%, var(--line)); }
.badge.band24 { color: #c4b5fd; border-color: color-mix(in srgb, #c4b5fd 40%, var(--line)); }
.card-head.folded .val { text-align: right; font-size: 12px; font-weight: 600; }
.card-head.folded .unit { font-size: 11px; }
.card-head.folded .spark { display: block; }
.fold-spark { width: 84px; display: block; }
.fold-val { font-size: 12px; font-weight: 600; }
.fold-toggle { font-size: 13px; line-height: 1; padding: 3px 8px; }
.ntop-link {
  font-size: 11px;
  color: var(--ink-dim);
  text-decoration: none;
  border: 1px solid var(--line);
  border-radius: 4px;
  padding: 2px 7px;
}
.ntop-link:hover { color: var(--ink); border-color: var(--ink-faint); }
.ok { color: var(--ok); }
.warn { color: var(--warn); }
.bad { color: var(--bad); }
</style>
