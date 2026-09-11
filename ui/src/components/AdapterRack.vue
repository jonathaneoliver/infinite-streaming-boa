<script setup lang="ts">
import { computed, ref } from 'vue';
import type { BSSLoadState, IfaceInfo, Series } from '@/types';
import { DEVELOPER } from '@/types';
import { rackAdapters, isOpen, toggleAdapter } from '@/composables/useAdapters';
import AdapterStack from '@/components/AdapterStack.vue';
import type { useBridge } from '@/composables/useBridge';
import AdapterToken from './AdapterToken.vue';

/**
 * The adapters, as a rack.
 *
 * One fold per adapter in a FIXED order, because the question this answers is
 * "what is each radio doing" and a list that reorders itself is one you have to
 * re-read every time. The collapsed row carries what you look at constantly --
 * where it is and who is on it -- and the actions you reach for constantly.
 * Everything else is one click away, in place, so opening a fold never moves
 * the fold you were reading.
 *
 * It used to carry a channel-busy airtime figure too, from `iw survey dump`.
 * That is gone: on mt7921u the counter reads about five times low, and it sat
 * one line above a per-client airtime chart disagreeing with it by that factor.
 * A number on screen gets believed, so a wrong one is worse than none. Airtime
 * now lives in the fold, per client, from counters that were checked against
 * iperf3 -- see DATA-CONTRACT Source T.
 *
 * This rack is now the ONE home for these controls. They used to be split
 * between the bridge diagram's nodes and a panel below it, and before that they
 * existed in both at once; the principle that survived is that a control lives
 * where its subject is named, exactly once. The diagram is a picture of the
 * topology and no longer a control surface.
 */
const props = defineProps<{
  bridge: ReturnType<typeof useBridge>;
  /** Which devices are on each adapter, from the snapshot rather than from
   *  hostapd: the rack and the device list below it must agree, and the list is
   *  what the operator is actually looking at. */
  onAdapter: Record<string, { mac: string; label: string }[]>;
  /** Per-device history, keyed by MAC -- the same object the client cards read,
   *  so a band in the fold and the line on the card cannot disagree. */
  series?: Record<string, Series>;
  /** MAC to display name, covering devices that have since left. */
  labels?: Record<string, string>;
}>();

const PROFILES = [
  { name: 'clean', label: 'clean', desc: 'everything back to how the image configured it.' },
  { name: 'legacy', label: '11n', desc: 'no ac, no ax — the ceiling an older device sees.' },
  { name: 'narrow', label: '20MHz', desc: 'a quarter of the spectrum, so airtime contention is real.' },
  { name: 'dozy', label: 'power-save', desc: 'DTIM 10 at 300 ms beacon, U-APSD off.' },
];

const OUTAGES = [5, 10, 30, 60];
const outage = defineModel<Record<string, number>>('outage', { default: () => ({}) });

const busy = computed(() => props.bridge.busy.value);

/** How many names fit before the row starts wrapping. Past this they are
 *  summarised, with the full list in the tooltip. */
const NAMES_SHOWN = 3;

function on(r: IfaceInfo) {
  return props.onAdapter[r.name] ?? [];
}

/** Scroll to a device's card, the mirror of the token's jump up to a fold. */
function showClient(mac: string) {
  document.getElementById(`client-${mac}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
}

/**
 * Whether this radio can act on its clients at all.
 *
 * Everything on the row except the two switches goes through hostapd on THIS
 * radio: a deauth, a disassociation, a steer in either direction and a band
 * scan all need a BSS to issue them from. With the radio powered off, or its
 * access point down, none of them can do anything -- so they say so by being
 * dead rather than by failing when pressed.
 *
 * The two switches are deliberately exempt. `switch on` and `enable AP` are the
 * way BACK, and a row that greys out its own recovery controls when a radio is
 * down has locked the operator out of the one state they need to leave.
 *
 * The distinction that matters, and the one an earlier version got wrong: this
 * is THIS radio's own state. Disabling a control because of the OTHER radio is
 * what made buttons vanish and then flicker while an operator worked, and it is
 * not something a button on this row should ever be saying.
 */
/**
 * What is wrong with this radio, in two words, or nothing.
 *
 * Four states rather than the two it began with, because the row grew controls
 * that create states the badge could not describe: a radio can now have its
 * access point taken down while staying powered, and that is invisible in a row
 * whose other indicators all still read normally.
 *
 * Ordered by which fact makes the others moot. A radio with its transmitter off
 * has no access point either, and saying so twice would be noise; a radio the
 * daemon does not watch is not being conditioned at all, which outranks
 * anything about its access point.
 *
 * "no AP" is the one that is not an operator's doing: powered, watched, and
 * hostapd is not answering for it. That is the shape of the wedge in #182, and
 * it is worth a badge precisely because every other indicator on the row looks
 * healthy while it is true.
 */
function warnText(r: IfaceInfo): string {
  if (!r.serving) return 'not serving';
  // The STATE, in the same words the pattern lane uses for it: a lane authoring
  // `radio-off` and a badge reading `off` are the same condition, and a reader
  // should not have to work that out. The BUTTONS keep their verbs -- a control
  // does something, a badge and a lane name a condition, and "switch off" is
  // the action that produces "radio off". See #229.
  if (r.power_known && !r.powered) return 'radio off';
  if (!r.ap) return 'no AP';
  // Before "AP disabled", because it IS one as far as `enabled` goes and the
  // two need opposite actions: a disabled AP is switched back on, whereas this
  // one has an interface that went away under hostapd and needs hostapd
  // restarted. Reported as the same thing, an operator presses enable, watches
  // it claim success, and still cannot join -- which is how twenty minutes went
  // missing on 2026-09-06.
  if (r.ap.link_down) return 'link down';
  // "AP disabled", matching the control that produces it and the pattern lane
  // that schedules it -- one word family for one thing, which is the whole
  // point of #229. It read "AP down" briefly, which was a second name for the
  // same state and sat confusingly next to "link down" above, a genuinely
  // different fault needing a different fix.
  if (!r.ap.enabled) return 'AP disabled';
  return '';
}

function apLive(r: IfaceInfo): boolean {
  return r.powered !== false && r.ap?.enabled === true;
}

/**
 * The other radio clients can be moved between, as the whole interface rather
 * than just its name -- gathering needs to know how many stations are on it,
 * not only that it exists.
 *
 * EXISTENCE, not health.
 *
 *
 * This decides only whether a peer EXISTS, which depends on how many radios the
 * box has and so never changes while anyone is looking. It previously required
 * `o.ap?.enabled`, so the moment the other radio's access point went down these
 * two buttons vanished from THIS radio's row and every control after them slid
 * left under the pointer. On a one-radio box they are present and dead rather
 * than absent, for the same reason.
 */
/**
 * EVERY other radio, which is what gather actually acts on.
 *
 * There was a singular otherRadio() returning the FIRST other wireless
 * interface. That was the whole answer on a two-radio box and became wrong the
 * moment a third appeared: a client sitting on the radio that happened to sort
 * third was invisible to every row, so gather was greyed out everywhere with
 * nothing saying why. It is gone rather than left beside this one, because a
 * helper that quietly picks one of three is a trap to reach for.
 */
function otherRadios(r: IfaceInfo): IfaceInfo[] {
  return rackAdapters.value.filter((o) => o.name !== r.name && o.wireless);
}

/** How many clients gather would be asking to move, across all other radios. */
function gatherable(r: IfaceInfo): number {
  return otherRadios(r).reduce((n, o) => n + (o.ap?.stations ?? 0), 0);
}

/**
 * Where an eviction sends them: the FIRST access point that is actually up, in
 * rack order.
 *
 * Named in the tooltip rather than left implicit, so the choice is visible
 * before it is made rather than inferred from the log afterwards -- but the
 * rule itself is the daemon's, not a second opinion. OtherRadio in
 * radioprofile.go takes the first serving radio in WlanPorts order, and this
 * takes the first in rack order, which lists the same radios the same way.
 *
 * Emptiest-first was tried here and reverted. It spreads clients, which reads
 * as the better behaviour until you notice it makes the destination depend on
 * transient state: the same button resolves differently on two runs of one
 * test, and the client card -- which asks the daemon -- named a different
 * radio than this did for the same move. One box has to give one answer, and
 * on a measurement box that answer has to be the predictable one.
 */
function evictTo(r: IfaceInfo): IfaceInfo | undefined {
  return otherRadios(r).find((o) => o.ap?.enabled);
}

/**
 * What the row says beyond the token.
 *
 * NOT the channel: the token already carries it, and printing "ch 149" twice in
 * one row reads as two different facts about the same radio. Width and mode are
 * what the token leaves out, and they are what says how the link will behave.
 */
function summary(r: IfaceInfo): string {
  if (!r.ap) return r.speed_mbps ? `${r.speed_mbps} Mb/s` : (r.up ? 'up' : 'down');
  return [r.ap.width_mhz ? `${r.ap.width_mhz} MHz` : '', r.ap.mode ?? '']
    .filter(Boolean)
    .join(' · ');
}

/**
 * How contested this radio's channel is, from whichever scan measured it.
 *
 * Three separate facts, and they stay separate on purpose:
 *
 *   ours     our own access point as ANOTHER radio heard it. Absent for the
 *            radio that took the scan -- a radio cannot hear itself.
 *   loudest  the strongest NEIGHBOUR on the channel. This is the one that
 *            explains contention: one AP at -18dBm costs far more airtime than
 *            three at -75.
 *   util     measured airtime on the channel, from neighbours' BSS Load.
 *
 * An em dash means "nobody has scanned this channel yet", never zero. Absence
 * and idleness are different facts and this box has been wrong about that
 * before: the survey-derived readout these replace reported a saturated radio
 * at 2.5% for exactly that kind of reason.
 */
function airOf(r: IfaceInfo) {
  return props.bridge.air.value[r.name];
}

/**
 * The three figures, each as a value and its unit, or null when unmeasured.
 *
 * Null rather than a formatted '—' so the template can drop the UNIT along with
 * the number. "— dBm" claims a signal reading exists and is merely being
 * displayed oddly; a bare em dash says there is no measurement, which is the
 * fact. Same reason the numbers carry units at all: -27 on its own is not
 * self-describing, and a reader should not have to know that this column is
 * signal strength to read it.
 */
/**
 * The best PHY rate any client on this radio is currently negotiating, Mbit/s.
 *
 * The HIGHEST rather than a mean, because the question a radio's headline
 * answers is "what is this link capable of right now" -- and a mean across one
 * saturated laptop and one dozing phone describes neither. Which client it
 * belongs to is on that client's own card.
 *
 * The NEGOTIATED rate, not the theoretical ceiling phyCeilingLabel computes
 * from mode and width. Those differ by a lot and only one of them is a
 * measurement: the same radio advertises 802.11ax at 80MHz whether a client is
 * running MCS 11 beside it or MCS 2 through a wall.
 *
 * Taken from the recorded series rather than the roster, so it is the same
 * number the client's chart is drawing at that instant and the two cannot
 * disagree.
 */
function phyOf(r: IfaceInfo): number | null {
  let best = 0;
  for (const c of on(r)) {
    const s = props.series?.[c.mac];
    const v = s?.phyDown?.length ? (s.phyDown[s.phyDown.length - 1] ?? 0) : 0;
    if (v > best) best = v;
  }
  return best > 0 ? best : null;
}

function oursOf(r: IfaceInfo): number | null {
  const a = airOf(r);
  return a?.ours_known && a.ours_dbm !== undefined ? a.ours_dbm : null;
}

function loudestOf(r: IfaceInfo): number | null {
  const a = airOf(r);
  // Zero means nothing was heard on the channel, which a scan reports as a
  // genuine absence of neighbours rather than a 0 dBm signal.
  return a?.loudest_dbm ? a.loudest_dbm : null;
}

/**
 * OUR OWN airtime on this radio: the five-second average, summed across its
 * clients, straight from the station counters.
 *
 * The headline, because it is the one figure here that actually answers "how
 * busy is this radio" and the one that was verified against iperf3. Absent
 * where the driver cannot attribute airtime, which is not 0%.
 */
function ownAirOf(r: IfaceInfo): number | null {
  return r.own_air_known && r.own_air_pct !== undefined ? r.own_air_pct : null;
}

/**
 * What the NEIGHBOURS say about the channel, as a range.
 *
 * A range and not a number, because they disagree: measured 2026-09-07, five
 * access points on channel 2 reported 19% to 33% of the same medium from
 * different rooms. Quoting the maximum alone made a 14-point spread look
 * precise. Collapses to a single figure when only one access point reports,
 * which is itself worth seeing -- one faint stranger's account of its own
 * corner of the world is not the same evidence as five that agree.
 *
 * Null when nobody advertised it. That is not an idle channel, and on a QUIET
 * channel it is the normal case: quiet means there is nobody nearby to ask.
 */
function othersOf(r: IfaceInfo): string | null {
  const a = airOf(r);
  if (!a?.util_known) return null;
  const hi = a.util_pct;
  const lo = a.util_min_pct ?? hi;
  return Math.round(lo) === Math.round(hi)
    ? `${hi.toFixed(0)}%`
    : `${lo.toFixed(0)}–${hi.toFixed(0)}%`;
}

/** Where the figures came from and when, since a scan describes a moment. */
function airTitle(r: IfaceInfo): string {
  const a = airOf(r);
  if (!a) {
    return `Nothing has scanned channel ${r.ap?.channel ?? '?'} yet, so there is no measurement — which is not the same as a quiet channel.`;
  }
  const age = Math.max(0, Math.round((Date.now() - a.at) / 1000));
  const who = a.from === r.name ? 'this radio itself' : a.from;
  return (
    `Channel ${a.channel}, measured by ${who} ${age}s ago.\n` +
    `ours — this radio's own access point, in dBm, as ${a.from} heard it` +
    (a.ours_known ? '.' : ' — absent, because a radio cannot hear itself.') +
    `\nloudest — the strongest neighbouring access point on this channel, in dBm. ` +
    'Signal matters more than a headcount: one AP at -20 dBm costs more airtime than three at -75.' +
    `\nair — what THIS radio spent on its own clients, averaged over 5s, from our own counters. The figure the stacked chart draws.` +
    `\nothers — ` +
    (a.util_known
      ? `${a.util_reporters ?? 0} neighbouring access point(s) advertise ${a.util_min_pct !== undefined && Math.round(a.util_min_pct) !== Math.round(a.util_pct) ? `${a.util_min_pct.toFixed(0)}–${a.util_pct.toFixed(0)}%` : `${a.util_pct.toFixed(0)}%`} busy on this channel, each averaged over about 5 seconds. They sit in different rooms and hear different amounts, so a wide range is real disagreement rather than noise.`
      : 'no neighbour on this channel advertised it. Not an idle channel — on a quiet channel there is often nobody near enough to ask.')
  );
}

/*
 * BSS LOAD: what a radio CLAIMS about its congestion, as against what it
 * measures.
 *
 * Its own section, "what the beacon says", and NOT under "conditioning the
 * link" where it started. Conditioning changes the LINK -- the rate, the
 * retries, the packets a client actually meets. These two change only what the
 * beacon SAYS and leave the link exactly as it was, which makes them the one
 * pair here aimed at a client's decision rather than at its traffic.
 */

/**
 * Staged handle positions, keyed by interface.
 *
 * A slider bound straight at the daemon's value fights the poll: the inventory
 * reloads every few seconds and would snatch the handle back to the last
 * committed number mid-drag. A drag lives here instead, and the entry is
 * dropped once the daemon has answered -- at which point what is on screen is
 * the daemon's number, already clamped to the floor.
 */
const bssDraft = ref<Record<string, { stations: number; util: number }>>({});

function bssOf(r: IfaceInfo): BSSLoadState | undefined {
  return props.bridge.bssLoad.value[r.name];
}

function bssOn(r: IfaceInfo): boolean {
  return bssOf(r)?.on === true;
}

/** Advertising the box's own estimate rather than hostapd's zero. */
function bssFix(r: IfaceInfo): boolean {
  return bssOf(r)?.fix === true;
}

/** The estimate `fix` would advertise, or null where there is nothing to
 *  correct with — no measured airtime and no neighbour on the channel saying
 *  anything. */
function bssFixUtil(r: IfaceInfo): number | null {
  const s = bssOf(r);
  return s?.fix_known ? s.fix_util_pct : null;
}

/**
 * The floors: what the radio is REALLY doing, and the lowest either handle can
 * go.
 *
 * Rounded UP, so a slider's minimum is a whole number and its steps land on
 * whole numbers after it. The daemon clamps to the exact figure, which is never
 * above this, so the rounding can only ever claim slightly more than the truth
 * -- the safe direction, and the only direction this control may move in at
 * all.
 */
function bssFloorUtil(r: IfaceInfo): number {
  return Math.ceil(bssOf(r)?.floor_util_pct ?? 0);
}
function bssFloorStations(r: IfaceInfo): number {
  return bssOf(r)?.floor_stations ?? 0;
}
/** False where the driver reports no per-station airtime, so a 0% floor is an
 *  absence of measurement rather than an idle radio. */
function bssFloorKnown(r: IfaceInfo): boolean {
  return bssOf(r)?.floor_known === true;
}

/**
 * Where each handle sits: the staged drag, else what the daemon holds, and
 * never below the floor.
 *
 * The floor is re-applied on every read rather than trusted from the last
 * commit, because it MOVES -- it is our own airtime, second by second. A claim
 * that was honest when it was made stops being honest the moment the radio gets
 * busier, so the handle climbs with it.
 */
function bssUtil(r: IfaceInfo): number {
  return Math.max(bssDraft.value[r.name]?.util ?? bssOf(r)?.util_pct ?? 0, bssFloorUtil(r));
}
function bssStations(r: IfaceInfo): number {
  return Math.max(
    bssDraft.value[r.name]?.stations ?? bssOf(r)?.stations ?? 0,
    bssFloorStations(r),
  );
}

type BSSPatch = Partial<{ stations: number; util: number }>;

/** Stage a drag without sending it: one request per commit, not per pixel. */
function stageBSS(r: IfaceInfo, patch: BSSPatch) {
  bssDraft.value = {
    ...bssDraft.value,
    [r.name]: { stations: bssStations(r), util: bssUtil(r), ...patch },
  };
}

/**
 * Commit, then let go.
 *
 * The draft is dropped rather than kept in step with the response, because the
 * daemon's answer is the one that counts: it has clamped both numbers up to a
 * floor this may well have been dragged below, and a handle left where it was
 * released would be showing a claim the box is not making.
 */
async function commitBSS(r: IfaceInfo, on: boolean, patch: BSSPatch = {}, fix?: boolean) {
  const next = { stations: bssStations(r), util: bssUtil(r), ...patch };
  stageBSS(r, next);
  await props.bridge.setBSSLoad(r.name, on, fix ?? bssFix(r), next.stations, next.util);
  const rest = { ...bssDraft.value };
  delete rest[r.name];
  bssDraft.value = rest;
}

/** Why the handle will not go lower, said where somebody tries to drag it. */
function bssFloorTitle(r: IfaceInfo, what: 'util' | 'stations'): string {
  const why =
    what === 'util'
      ? bssFloorKnown(r)
        ? `${bssFloorUtil(r)}% is what this radio's own clients are using right now, ` +
          'measured from the station counters \u2014 so the channel is at least that busy.'
        : 'This radio reports no per-station airtime, so there is no measured floor. ' +
          'The 0% minimum is an absence of measurement, not an idle radio.'
      : `${bssFloorStations(r)} station(s) are actually associated.`;
  return (
    `${why}\n\nThe handle only goes UP from there, and that is the safety rule ` +
    'rather than a nicety: overstating load pushes devices away, which is what a ' +
    'genuinely busy access point does anyway, while understating it would PULL them ' +
    'in \u2014 onto neighbours\u2019 equipment nobody here owns or can observe.' +
    '\n\nThe floor is live, so the claim follows it up and comes back down: start ' +
    'traffic and the beacon carries the real figure even if it is above what was ' +
    'set here, and when the traffic stops the setting is still the setting.' +
    '\n\nMoving a handle ticks the override on. Unticking it is the only way ' +
    'back: the beacon then returns to the zeros hostapd advertises by default.'
  );
}

function degraded(i: IfaceInfo): boolean {
  return i.radio?.bus === 'usb' && !!i.radio.link_mbps && i.radio.link_mbps < 5000;
}
</script>

<template>
  <section class="rack">
    <h2 class="section-title">
      adapters<span v-if="rackAdapters.length" class="count">{{ rackAdapters.length }}</span>
    </h2>

    <article
      v-for="r in rackAdapters" :key="r.name"
      :id="`adapter-${r.name}`"
      class="fold" :class="{ open: isOpen(r.name), off: r.power_known && !r.powered }"
    >
      <!-- The collapsed row is a header, not a button: it holds controls of its
           own, and nesting those inside a button is both invalid and a way to
           fire an action while trying to open a fold. The caret is the
           control that opens it. -->
      <header class="row">
        <button
          class="caret" :aria-expanded="isOpen(r.name)"
          :title="isOpen(r.name) ? 'Collapse' : 'Show everything this radio can do'"
          @click="toggleAdapter(r.name)"
        >{{ isOpen(r.name) ? '▾' : '▸' }}</button>

        <AdapterToken :name="r.name" head />

        <span class="sum">{{ summary(r) }}</span>

        <!-- Contention, in the fixed column the old survey-derived `air` badge
             used to hold. Three figures rather than one because they answer
             different questions and no single number could: how well the box
             hears its own radio, how loud the competition is, and how much of
             the channel is actually in use.

             Every one carries a LABEL and a UNIT. "-27" and "9%" are not
             self-describing, and a reader should not have to already know that
             this column is signal strength and channel airtime to read it. The
             unit is dropped along with the number when there is no measurement,
             so an unscanned channel is a bare em dash rather than "— dBm",
             which would claim a reading exists. -->
        <!-- The cell is ALWAYS present, and empty for a wired port.
             `v-if` on the whole span looked right and broke the row: this is a
             fixed-column grid, so removing an element slides every later cell
             one track left and lan0's device list rendered in the contention
             column. Only the CONTENTS are conditional -- a wired port has no
             channel, so no neighbours and no airtime, and three em dashes there
             would invite the reader to wonder what had failed to be measured
             about a cable. -->
        <span class="air" :title="r.wireless ? airTitle(r) : ''">
          <template v-if="r.wireless">
          <!-- PHY leads, because it is the only one of these four that is about
               the LINK rather than about the channel, and it is the number an
               operator checks first. Em dash when no client is associated:
               there is no negotiated rate without a station to negotiate with,
               and the mode-and-width ceiling beside it is not a substitute. -->
          <span class="k">PHY</span
          ><span class="v num">{{ phyOf(r)?.toFixed(0) ?? '—'
            }}<span v-if="phyOf(r) !== null" class="unit">Mb/s</span></span>
          <span class="k">ours</span
          ><span class="v num">{{ oursOf(r)?.toFixed(0) ?? '—'
            }}<span v-if="oursOf(r) !== null" class="unit">dBm</span></span>
          <span class="k">loudest</span
          ><span class="v num">{{ loudestOf(r)?.toFixed(0) ?? '—'
            }}<span v-if="loudestOf(r) !== null" class="unit">dBm</span></span>
          <!-- OURS first, because it is the trustworthy one: our own counters,
               five-second average, the same total the chart draws. The
               neighbours' figure beside it answers a different question and
               says so in its label. -->
          <span class="k">air</span
          ><span class="v num">{{ ownAirOf(r)?.toFixed(0) ?? '—'
            }}<span v-if="ownAirOf(r) !== null" class="unit">%</span></span>
          <span class="k">others</span
          ><span class="v num">{{ othersOf(r) ?? '—'
            }}<span v-if="othersOf(r) && (airOf(r)?.util_reporters ?? 0) > 0"
              class="unit">×{{ airOf(r)?.util_reporters }}</span></span>
          </template>
        </span>


        <!-- WHO is on this radio, after everything describing the radio itself.
             The facts to the left are one subject -- where this adapter is and
             how busy its air is -- and interleaving the device names among them
             made two subjects read as one list. These are also the blast radius
             of every button to the right, which is why they are named rather
             than counted: "2 clients" answers a question nobody asked, and
             "Jonathans-iPhone, Watch" answers the one that is. -->
        <span v-if="on(r).length" class="who" :title="on(r).map((c) => c.label).join(', ')">
          <!-- The count LEADS the names, because the names abbreviate.
               Three fit; past that the rest become a "+2" that is easy to read
               past, and the track can ellipsise a name at any width. The total
               is the number the buttons to the right act on, so it must not be
               something the reader has to reconstruct from a truncated list. -->
          <span class="who-n">{{ on(r).length }}</span>
          <button
            v-for="c in on(r).slice(0, NAMES_SHOWN)" :key="c.mac"
            class="who-name" :title="`Show ${c.label} below`"
            @click="showClient(c.mac)"
          >{{ c.label }}</button>
          <span v-if="on(r).length > NAMES_SHOWN" class="who-more">
            +{{ on(r).length - NAMES_SHOWN }}
          </span>
        </span>
        <span v-else class="who-none">no clients</span>

        <div class="tail">
          <!-- The badge holds its place whether or not it has anything to say.
               It sits before the buttons in a flex row, so appearing pushed
               every control along by its own width -- mid-press, on the one
               radio whose state was changing. -->
          <span
            v-if="r.wireless" class="badge warn-badge"
            :class="{ blank: !warnText(r) }"
            :title="warnText(r) === 'AP disabled'
              ? `${r.name} is powered, but its access point is down — clients cannot join it.`
              : warnText(r) === 'no AP'
                ? `${r.name} is powered and watched, but hostapd is not answering for it.`
                : undefined"
          >{{ warnText(r) || '\u00a0' }}</span>

        <!-- The actions reached for constantly. Every one of these acts on
             EVERY client on this adapter -- which is why the devices are named
             at the left of the row rather than counted on a button. "drop 2"
             read as part of the label, as though there were some other drop. -->
        <!-- DRAWN WHENEVER THIS IS A RADIO, never gated on hostapd being
             readable. `r.ap` is absent whenever the control socket cannot be
             read -- a wedged adapter, a rebuild, a band scan -- which is
             precisely when an operator is watching this row and reaching for
             these controls. Removing them then took the whole set off the page
             and slid everything below it upwards, so a click already committed
             to landed somewhere else.
             A disabled button says "not now"; an absent one says "this box
             cannot do that", and only one of those is true here. -->
        <template v-if="r.wireless">
          <!-- CUTTING POWER IS BEHIND developer=1.
               It is the most destructive control here and the least
               recoverable: rfkill wedges the USB adapter often enough that
               #182 exists about it, and the recovery takes about two minutes
               during which the radio serves nobody. `disable AP` produces the
               same visible outcome -- the network goes away -- for a client
               that is told, comes back in a second, and has never wedged
               anything. That is the one to reach for by default.
               Not removed, because a silent outage is a real experiment and
               the only way to make one; just not the button nearest to hand. -->
          <button
            v-if="DEVELOPER"
            class="ghost" :class="{ accent: r.power_known && !r.powered }"
            :disabled="busy || !r.power_known"
            :title="r.powered
              ? `Switch ${r.name} off. Clients are told NOTHING and must time out.`
              : `Switch ${r.name} back on.`"
            @click="bridge.setPower(r.name, !r.powered)"
          >{{ r.powered ? 'switch off' : 'switch on' }}</button>
          <!-- THE OTHER HALF OF THE PAIR, and it sits next to the power switch
               on purpose: the two look identical to a client -- the network
               goes away -- and differ in the one respect being tested, whether
               it was TOLD. Power is rfkill and says nothing; this closes the
               BSS with the transmitter still on, so the departure is announced.
               Side by side is what makes them read as a choice of mechanism
               rather than two unrelated ways to break the same thing.

               VERBS, not states. These first read "AP up" / "AP down", which
               name the action the same way "switch on" / "switch off" do -- but
               those two are unmistakably imperative and these two are not. "AP
               up" on a radio whose access point is DOWN parses as a status
               label announcing the opposite of the truth, which is worse than
               ambiguous on a control an operator reaches for precisely when
               they are unsure what state a radio is in. -->
          <button
            class="ghost" :class="{ accent: r.powered && r.ap && !r.ap.enabled }"
            :disabled="busy || !r.powered || !r.ap"
            :title="r.ap?.enabled
              ? `Take ${r.name}'s access point down, leaving the radio powered. \
Clients ARE told it has gone, unlike a power cut.`
              : `Bring ${r.name}'s access point back up.`"
            @click="bridge.setAPEnabled(r.name, !r.ap?.enabled)"
          >{{ r.ap?.enabled === false ? 'enable AP' : 'disable AP' }}</button>
          <!-- The same teardown with an EXPLICIT goodbye first.
               Its own button rather than a mode, because it is a property of
               one press: an operator comparing how a device reacts to being
               told against how it reacts to working it out varies this between
               one press and the next.

               NOT v-if. It was, on the reasoning that with nobody to tell the
               announcement is the only thing that would differ -- which is an
               argument for disabling it and never for removing it. The button
               then vanished exactly when a radio's access point went down,
               which is the third time a control on this row has been made to
               come and go with the state of the radio it belongs to. Nothing
               on this row is conditionally rendered any more; state changes
               what a button DOES, never whether it is there. -->
          <!-- DEAUTH +, in whichever direction the radio is going.

               Named for the frame it sends rather than for the intention
               behind it. It was "tell", which was friendly and hid which frame
               went out -- the same fault "drop" had for a deauthentication and
               "nudge" for a disassociation, in an interface whose whole job is
               to be precise about exactly that. See #229.

               The audience differs, which is why this is not one action with a
               sign flipped. Going down it is the stations currently
               associated, addressed individually before the BSS closes. Coming
               up it is clients that still BELIEVE they are associated and are
               not -- exactly the population a silent outage creates, since
               cutting power tells nobody -- and all the access point can do
               for them is broadcast, which this box otherwise suppresses
               (#224) because it lands on the clients a measurement is
               watching.

               DEAUTHENTICATION in both directions, deliberately. The frames
               differ from disassociation -- deauth withdraws authentication as
               well as association, so a client must redo the whole handshake --
               and an earlier version sent a disassociation on the way down
               while the way up re-enabled a broadcast DEAUTH. That made the two
               halves of one control disagree about which frame goes out, which is
               no use to somebody comparing how a device reacts to being told.
               Under WPA2 both force a full reconnect anyway, so the choice
               costs nothing and buys one answer instead of two.

               So: individually on the way down, by broadcast on the way up,
               the same frame type in both, and never a no-op in either. -->
          <button
            class="ghost" :class="{ accent: r.powered && r.ap && !r.ap.enabled }"
            :disabled="busy || !r.powered || !r.ap
              || (apLive(r) && !r.ap?.stations)"
            :title="!apLive(r)
              ? `Bring ${r.name}'s access point back up AND announce it, so a client `
                + `still holding a stale association is told to start again rather `
                + `than left to notice.`
              : `Deauthenticate all ${r.ap?.stations ?? 0} client(s), then take `
                + `${r.name}'s access point down. An explicit goodbye, rather than `
                + `whatever hostapd does on its own.`"
            @click="bridge.setAPEnabled(r.name, !apLive(r), true)"
          >{{ apLive(r) ? 'deauth + disable AP' : 'deauth + enable AP' }}</button>
          <button
            class="ghost" :disabled="busy || !apLive(r) || !r.ap?.stations"
            :title="apLive(r)
              ? `Deauthenticate all ${r.ap?.stations ?? 0} client(s). They are told, so they reconnect quickly.`
              : `${r.name} has no access point up, so there is nothing to deauthenticate from.`"
            @click="bridge.linkAll(r.name, 'deauth')"
          >deauth</button>
          <button
            class="ghost" :disabled="busy || !apLive(r) || !r.ap?.stations"
            :title="apLive(r)
              ? 'Disassociate every client — the softer transition.'
              : `${r.name} has no access point up, so there is nobody to disassociate.`"
            @click="bridge.linkAll(r.name, 'disassoc')"
          >disassoc</button>
          <!-- EVICT and GATHER: the same 802.11v request in both directions.
               One button called "steer" only ever emptied a radio, which is
               half the question. "Move everyone to the other band" and "bring
               everyone here" are asked about equally often on a two-band box,
               and the second one had no button at all -- you had to go to the
               OTHER adapter and press steer there, which is the same action
               named after the wrong radio.
               Each is disabled when its source radio has nobody on it, so a
               dead button always means "there is no one to move", never "this
               is not supported".

               Gated on THIS radio's access point, and never on the other
               one's. The difference is the whole lesson from getting it wrong
               twice: keyed to the PEER's state these vanished, and then
               flickered, while an operator was working -- and a greyed button
               then meant "some other radio is down", which is not something a
               button on this row should ever say.
               Its own AP is different. With no BSS here there is genuinely
               nothing to steer, and dead is the honest state. -->
          <button
            class="ghost"
            :disabled="busy || !apLive(r) || !r.ap?.stations || !evictTo(r)"
            :title="!apLive(r)
              ? `${r.name} has no access point up, so it has nobody to move.`
              : !evictTo(r)
                ? 'No other access point is up for them to go to.'
                : `Push all ${r.ap?.stations ?? 0} client(s) off ${r.name} and deny `
                  + `them here, so they cannot come straight back. Each ban lifts as `
                  + `soon as that client lands somewhere. Where each one goes is its `
                  + `own choice — this empties a radio, it does not place anyone.`"
            @click="bridge.evict(r.name)"
          >evict</button>
          <button
            class="ghost"
            :disabled="busy || !apLive(r) || !gatherable(r)"
            :title="!apLive(r)
              ? `${r.name} has no access point up, so there is nowhere here to gather them to.`
              : !otherRadios(r).length
                ? 'No other radio on this box to gather from.'
                : `Move all ${gatherable(r)} client(s) on `
                  + `${otherRadios(r).map((o) => o.name).join(' and ')} here by denying `
                  + `them on the others. Each ban lifts as soon as that client arrives. `
                  + `They cannot refuse — this removes the alternatives rather than `
                  + `asking. Use steer on a client to test whether it honours a request.`"
            @click="bridge.gather(r.name)"
          >gather</button>
          <button
            class="ghost" :disabled="busy || !apLive(r)"
            :title="apLive(r)
              ? 'Survey the band. Costs a few beacon gaps, or an outage on a radio that will not scan while serving.'
              : `${r.name} has no access point up; a scan takes the BSS down and puts it back, so there is nothing to take down.`"
            @click="bridge.scanBand(r.name, false)"
          >scan</button>
          <!-- The profiles are NOT here. They restart the access point and drop
               every client on it, which is a different weight of action from
               the rest of this row, and they belong beside the thresholds in
               the fold where the heading says what they do. -->
        </template>
        </div>
      </header>

      <!-- IN PLACE, so opening one fold never moves the one above it. -->
      <div v-if="isOpen(r.name)" class="body">
        <div class="facts">
          <div><span class="k">adapter</span>
            <span class="v">{{ [r.radio?.vendor, r.radio?.product].filter(Boolean).join(' ') || '—' }}</span></div>
          <div><span class="k">driver</span><span class="v num">{{ r.radio?.driver || '—' }}</span></div>
          <!-- THE PHYSICAL PORT, which is the identifier the kernel uses and
               the operator can reach. A USB fault is reported against this path
               and nothing else, so it is what connects a warning in the
               activity log to a socket to unplug. "2-1" is straight into the
               Pi; "4-1.3" is the third port of a hub on bus 4 -- and moving an
               adapter between the two changes this while the interface name
               deliberately does not. Only for USB: the onboard radio is on
               mmc and has no port to name. -->
          <!-- Still conditional, and safely so: bus is a property of the
               hardware and cannot change while this fold is open, so it cannot
               reflow the grid the way the AP fields could. -->
          <div v-if="r.radio?.bus === 'usb'"><span class="k">USB port</span>
            <span class="v num">{{ r.radio.socket || '—' }}</span></div>
          <div><span class="k">MAC</span><span class="v num">{{ r.mac }}</span></div>
          <div><span class="k">bridge port</span><span class="v num">{{ r.master || 'not bridged' }}</span></div>
          <!-- ALWAYS RENDERED, em-dash when there is no access point, rather
               than hidden behind v-if="r.ap".

               These four used to disappear together whenever the AP went down,
               which is every profile apply, every channel move and every
               restart. In a grid that is a whole row arriving and leaving, so
               everything below the strip jumped while an operator watched it.

               A dash is also the better answer on its own terms: a field that
               vanishes cannot be told apart from a field that is broken, and
               this strip already says "not bridged" rather than hiding the
               bridge port for the same reason. -->
          <div><span class="k">SSID</span><span class="v">{{ r.ap?.ssid || '—' }}</span></div>
          <div><span class="k">BSSID</span><span class="v num">{{ r.ap?.bssid || '—' }}</span></div>
          <div><span class="k">country</span><span class="v num">{{ r.ap?.country || '—' }}</span></div>
          <div><span class="k">beacon / DTIM</span>
            <span class="v num">{{ r.ap ? `${r.ap.beacon_int_ms} ms / ${r.ap.dtim_period}` : '—' }}</span></div>
        </div>

        <!-- WHAT IT IS CARRYING, with the facts rather than with the
             controls: this is status, and CHANNEL AND WIDTH still leads the
             controls below it. It answers the question that sits between the
             row above and the client cards below -- how the adapter's capacity
             is being divided right now. A stream that halved because the radio
             halved looks identical, on its own card, to one that halved by
             itself.
             Shown on EVERY adapter, unconditionally. It was gated first on
             being a radio, which left the wired port with no chart at all, and
             then on having devices attached, which was subtler and worse: the
             chart went blank the moment an adapter emptied, deleting the very
             traffic you had just been watching. What a radio carried is asked
             about most often right after it stopped carrying it. With nothing
             in the window the component says so in words. -->
        <AdapterStack
          v-if="series"
          :iface="r.name" :series="series" :labels="labels ?? {}"
        />

        <!-- Airtime BELOW the throughput pair, on the same x-axis and in the
             same device colours.

             The pairing is the point, and neither half answers alone. Throughput
             says what crossed the link; airtime says what it cost the radio, and
             the two come apart badly — measured 2026-09-07, one client held 46%
             of a radio to move 34 Mbit/s while another held 77% to move 505. On
             the chart above, the expensive one merely looks quiet. Read together
             they give efficiency, which is what actually says whether a device
             is a problem for everyone else on the radio.

             Only for a radio. A wired port has no airtime to divide. -->
        <AdapterStack
          v-if="series && r.wireless"
          mode="airtime"
          :iface="r.name" :series="series" :labels="labels ?? {}"
          :airtime-known="r.airtime_cap_known"
          :airtime-capable="r.airtime_per_client"
        />

        <p v-if="degraded(r)" class="notice bad inline">
          This adapter negotiated USB 2 speed ({{ r.radio?.link_mbps }} Mb/s)<template
            v-if="r.radio?.usb_version"> while declaring USB {{ r.radio.usb_version }}</template>.
          It still reports its full channel width and PHY rate while delivering roughly
          a sixth of the throughput, so nothing else here will look wrong — reseat it
          in a SuperSpeed port.
        </p>

        <template v-if="r.ap">
          <!-- CHANNEL AND WIDTH leads the CONTROLS, straight after the status
               above it. Opening an adapter is nearly always to change where it
               is, and a band plan is the one control here that has to be READ
               rather than just pressed -- it is a picture of the spectrum and
               of where this radio sits in it. What comes before it is
               reference; what comes after is buttons you already know you want.

               NAMED FOR THE CHOICE rather than for the verb, and that is the
               whole reason it is not "Move it". A cell in the 40 or 80 row is a
               channel AND a width together -- 36-48 is not a channel -- which
               is exactly why this is a band plan and not two dropdowns. A
               heading naming only the channel would put back the split the
               control exists to close. It also breaks the imperative voice of
               the headings under it on purpose: those are buttons that do
               something, this is a picture to be read first. -->
          <!-- EACH GROUP IN ITS OWN BOX, because the fold now holds four of
               them and a heading alone stopped being enough to say where one
               ends. The controls inside a group act on the same thing; a reader
               scanning for the beacon switches should not have to work out
               which "off" button belongs to which heading. -->
          <section class="group">
            <h4 class="first">Channel and width</h4>
            <slot name="plan" :radio="r" :others="otherRadios(r)" />
            <div class="action-row">
              <button class="accent" :disabled="busy"
                :title="`Survey ${r.name}'s band and move it to the quietest channel found. Takes the radio down and back up.`"
                @click="bridge.scanBand(r.name, true)"
              >scan and move to the quietest</button>
            </div>
          </section>

          <!-- The timed outage goes behind developer=1 with the power switch
               it belongs to: same mechanism, same cost, same tendency to wedge
               the USB adapter. -->
          <template v-if="DEVELOPER">
          <section class="group">
          <h4>Take it away</h4>
          <p class="warn-line">
            <strong>Silent.</strong> Clients are told nothing and must time out —
            a tripped breaker, not a disconnection.
          </p>
          <div class="action-row">
            <label class="k">cut it for</label>
            <div class="seg" role="group" aria-label="outage length">
              <button
                v-for="s in OUTAGES" :key="s"
                class="seg-btn" :class="{ on: outage[r.name] === s }"
                @click="outage = { ...outage, [r.name]: s }"
              >{{ s }}s</button>
            </div>
            <button
              :disabled="busy || !r.powered"
              @click="bridge.powerOutage(r.name, outage[r.name] ?? 10)"
            >cut and restore automatically</button>
            <span v-if="r.power_known && !r.powered" class="meta warn-line">
              <strong>OFF</strong> — switch it back on above
            </span>
          </div>
          <p class="meta">
            A client with a randomised MAC may return as a <strong>new device</strong>,
            leaving its policy behind on the old address (#45).
          </p>
          </section>
          </template>

          <section class="group">
          <h4>Conditioning the link</h4>
          <p class="meta group-note">
            A profile restarts the access point, dropping all
            {{ r.ap.stations }} client(s). The thresholds below do not — they are
            live on the next frame and nobody is dropped.
          </p>
          <div class="action-row">
            <label class="k">profile</label>
            <button
              v-for="p in PROFILES" :key="p.name"
              :class="{ accent: p.name === 'clean' }"
              :disabled="busy"
              :title="`${p.desc} Restarts the AP, dropping all ${r.ap.stations} client(s).`"
              @click="bridge.applyProfile(r.name, p.name)"
            >{{ p.label }}</button>
          </div>
          <div class="action-row">
            <label class="k">RTS/CTS</label>
            <button :disabled="busy"
              title="RTS/CTS before every frame — roughly halves throughput and adds two control frames of latency per data frame."
              @click="bridge.setThreshold(r.name, 'rts', 0)">every frame</button>
            <button :disabled="busy" @click="bridge.setThreshold(r.name, 'rts', 'off')">off</button>
            <label class="k">fragment</label>
            <button :disabled="busy"
              title="Fragment every frame at 256 bytes. With any error rate the retry cost explodes superlinearly."
              @click="bridge.setThreshold(r.name, 'frag', 256)">at 256</button>
            <button :disabled="busy" @click="bridge.setThreshold(r.name, 'frag', 'off')">off</button>
          </div>
          </section>

          <!-- ITS OWN SECTION, and it was under the conditioning heading
               first, which was wrong in a way worth recording. Conditioning
               damages the LINK. Neither control here touches the link at all,
               and one of them makes the radio's account of itself more accurate
               rather than less -- filing a correction under a heading about
               making things worse is a heading contradicting its contents.

               What these two share is a SUBJECT, not an effect: both change
               what the beacon says about this radio, one towards the truth and
               one away from it. So they are named for the subject, the way
               "channel and width" is, and the note carries the direction.

               The truth sits in the row beside the claim, always. A control
               that can lie is only safe while what it is lying about is on
               screen next to it. -->
          <section class="group">
          <h4>What the beacon says</h4>
          <p class="meta group-note">
            Neither of these touches the link — they change what this radio
            <em>tells</em> clients about itself, which some weigh when choosing
            between access points. It is the only lever here that offers a
            device a <em>reason</em> to move rather than ordering it to. Nobody
            is dropped, and nothing lifts on its own.
          </p>
          <div class="action-row beacon">
            <label class="chk"
              title="hostapd fills in a BSS Load element in every beacon from the driver&#39;s survey counter. On this hardware that counter is broken: measured over one 22.3s window at 494 Mbit/s it reported the channel 11.9% busy while the radio&#39;s own transmit and receive counters — from the same command — said 71.6%, and boa&#39;s per-station figures said 78.9%. So hostapd advertises a permanent 0%.&#10;&#10;Ticking this replaces it with the larger of what this radio measures at its own antenna and what the busiest neighbour on the channel reports. Still a lower bound: neither half can see a source that does not beacon, because this box has no spectral scan.&#10;&#10;Unticking is NOT silence. The element cannot be taken out of the beacon on this build — verified three ways — so unticked means advertising a 0% nobody chose, which is wrong in the direction that pulls clients towards us.">
              <input
                type="checkbox" :checked="bssFix(r)" :disabled="busy"
                @change="commitBSS(r, bssOn(r), {}, ($event.target as HTMLInputElement).checked)"
              />
              fix the BSS Load value
              <!-- The comparison is dropped where there is nothing to compare:
                   "would advertise 0% · hostapd says 0%" prints one number
                   twice and calls it a correction. -->
              <span class="fixv">{{
                bssFixUtil(r) === null
                  ? '(nothing measured, and no neighbour to ask)'
                  : bssFixUtil(r)! < 0.5
                    ? '(nothing to correct — this channel reads idle)'
                    : `(would advertise ${bssFixUtil(r)!.toFixed(0)}% · hostapd says 0%)`
              }}</span>
            </label>
          </div>
          <div class="action-row beacon">
            <label class="chk"
              title="hostapd fills in a BSS Load element in every beacon by itself, from the driver&#39;s survey counter. Ticking this replaces its two numbers with the ones below, from the next beacon onwards. No restart, and nobody is dropped.&#10;&#10;What is being overridden is worthless on this hardware: that survey counter reads near zero on the mt7921u while the radio is 80% busy, so hostapd&#39;s own figure is a permanent 0 stations and 0%. Unticking restores that default, which is not silence and not a measurement.&#10;&#10;Moving either handle ticks this on its own — the sliders are the claim, so setting one is asking for it. Untick to stop, which is the only way back: a claim stays on the air until it is switched off, across daemon restarts and deploys.">
              <input
                type="checkbox" :checked="bssOn(r)" :disabled="busy"
                @change="commitBSS(r, ($event.target as HTMLInputElement).checked)"
              />
              override it with the values below
            </label>
            <span v-if="bssOn(r) && bssFix(r)" class="meta">
              a deliberate claim wins over the correction
            </span>
          </div>
          <div class="load-rows" :class="{ off: !bssOn(r) }">
          <div class="load-row">
            <label>utilisation</label>
            <input
              type="range" :min="bssFloorUtil(r)" max="100" step="1"
              :value="bssUtil(r)" :disabled="busy"
              :title="bssFloorTitle(r, 'util')"
              @input="stageBSS(r, { util: +($event.target as HTMLInputElement).value })"
              @change="commitBSS(r, true, { util: +($event.target as HTMLInputElement).value })"
            />
            <span class="vals" :title="bssFloorTitle(r, 'util')">
              <span class="val num">{{ bssUtil(r).toFixed(0) }}%</span>
              <span class="floor">really {{
                bssFloorKnown(r) ? bssFloorUtil(r) + '%' : '\u2014'
              }}</span>
            </span>
          </div>
          <div class="load-row">
            <label>stations</label>
            <input
              type="range" :min="bssFloorStations(r)" max="100" step="1"
              :value="bssStations(r)" :disabled="busy"
              :title="bssFloorTitle(r, 'stations')"
              @input="stageBSS(r, { stations: +($event.target as HTMLInputElement).value })"
              @change="commitBSS(r, true, { stations: +($event.target as HTMLInputElement).value })"
            />
            <span class="vals" :title="bssFloorTitle(r, 'stations')">
              <span class="val num">{{ bssStations(r) }}</span>
              <span class="floor">really {{ bssFloorStations(r) }}</span>
            </span>
          </div>
          </div>
          </section>

        </template>
      </div>
    </article>

    <!-- Rack-level, and deliberately NOT another fold.
         A timeline that spans the radios has the SET as its subject, so it
         belongs in the section that names the set rather than beside it -- the
         rule this rack already states, that a control lives where its subject
         is named, exactly once.
         Not an <article class="fold"> though: the heading counts adapters, and
         a fourth fold under "adapters 3" would contradict it; and the fold row
         is a positional grid of six fixed tracks that a pattern header has no
         cells for. So it is its own element, at the folds' indent, separated by
         a rule. -->
    <div v-if="$slots.pattern" class="fold rack-wide">
      <slot name="pattern"></slot>
    </div>
  </section>
</template>

<style scoped>
.rack { margin-bottom: 12px; }
/* Wears the fold's clothes, because it IS one to the eye: same box, same
   indent, same caret. What it is not is an <article> in the v-for -- the
   heading counts adapters, and the fold's six-track grid has no cells for a
   pattern header. Only the marker sets it apart. */
.rack-wide { position: relative; }
.rack-wide::before {
  content: '';
  position: absolute; left: 14px; right: 14px; top: -6px;
  border-top: 1px dashed var(--line-soft);
}
/* Matches the client list's heading exactly: these two are the page's only
   sections now that the tabs are gone, and they have to look like a pair. At
   the old 10px faint weight this read as a caption on the first fold rather
   than as a heading over all of them. */
.section-title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 0 0 8px;
  font-size: 13px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--ink);
}
.section-title .count {
  font-family: var(--mono);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0;
  color: var(--ink-faint);
  font-variant-numeric: tabular-nums;
}
/* A fold is a PEER of a client card, so it wears the same clothes: the shared
   .card border, radius and ground. At the lighter --line-soft and a 6px gap the
   rack read as a strip of annotations above the real content, which is
   backwards -- these are the things every device below is attached to. */
.fold {
  border: 1px solid var(--line);
  border-radius: var(--r);
  background: var(--panel);
  margin-bottom: 10px;
}
.fold.open { border-color: var(--line); }
/* A switched-off adapter is the loud thing in the rack: an access point that is
   deliberately silent must not look like one that is merely quiet. */
.fold.off { border-color: color-mix(in srgb, var(--warn) 45%, var(--line)); }

/* A GRID, not a flex row, and the tracks are the point.
   Every field describing the radio sits in a fixed track, so the channel, the
   width and the airtime line up vertically down the whole rack and can be read
   as columns rather than found again on each line. The device names take the
   remaining space and are therefore left-aligned at the SAME x on every row,
   however long the name above them was.
   Two rules follow from the tracks being positional, the same ones ClientCard's
   head lives by: a cell may never be v-if'd away, only left empty, or every
   later cell slides; and no track may be max-content, or each row measures
   itself and the columns stop lining up.
   Padding and gap match .card-head so a collapsed fold and a collapsed client
   card sit on the same rhythm. */
.row {
  display: grid;
  grid-template-columns:
    28px                 /* caret */
    /* Both of these were cut when the contention column arrived, because they
       were the two with slack: the token holds "wlan-usb ch 149" and the next
       cell "80 MHz · 802.11ax", neither of which grows. The device names are
       what a narrow window would otherwise crush -- they sit in the 1fr track,
       so every fixed track above is taken out of THEM -- and a name matters
       more than whitespace beside a channel number. */
    minmax(0, 168px)     /* adapter token: name and channel */
    minmax(0, 144px)     /* width and mode, or the wired link speed */
    minmax(0, 408px)     /* PHY, signals, our airtime, the neighbours' */
    minmax(0, 1fr)       /* the devices on it, and the slack */
    auto;                /* badges and actions, pinned right */
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
}
/* The trailing cell is its own row, so the buttons pack right without needing
   a spacer track that would move with them. */
.tail {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-wrap: wrap;
}
/* The same control a client card folds with, so the two lists open the same
   way. */
.caret {
  background: none;
  border: 0;
  color: var(--ink-faint);
  cursor: pointer;
  font-size: 13px;
  line-height: 1;
  padding: 3px 8px;
}
.caret:hover { color: var(--ink); }
.sum {
  font-family: var(--mono);
  font-size: 12px;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink-dim);
}
/* The contention triple. Keys are faint and small so the numbers lead: the
   labels are read once and the figures are read every time. Tabular numerals so
   the column does not jitter as values change. */
.air { display: inline-flex; align-items: baseline; gap: 3px; white-space: nowrap; }
.air .k { color: var(--ink-faint); font-size: 9px; }
.air .v {
  font-family: var(--mono);
  font-size: 12px;
  font-weight: 600;
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}
/* The unit rides tight against its number and recedes, so the row reads as
   three values rather than six tokens. Same treatment the old badge gave its
   own unit, kept so this column looks like the rest of the interface. */
.air .unit { color: var(--ink-faint); font-size: 9px; margin-left: 1px; }
/* A gap before each label, so "ours -27dBm" groups more tightly than
   "-27dBm loudest" and the pairs do not run together. */
.air .k:not(:first-child) { margin-left: 6px; }
/* The devices on this radio. Buttons, because each one goes to that device's
   card -- the mirror of the token's arrow going the other way. */
.who {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
}
.who-name {
  background: none;
  border: 0;
  padding: 0;
  font-family: var(--sans);
  font-size: 12px;
  color: var(--ink-dim);
  cursor: pointer;
  max-width: 15ch;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.who-name:hover { color: var(--ink); text-decoration: underline; }
.who-more, .who-none { font-size: 12px; color: var(--ink-faint); }
/* The count, in the same weight the airtime figure uses: it is a number the
   buttons act on, not a caption on the names beside it. Fixed-width digits and
   flex: none so it never shrinks when the names do. */
.who-n {
  flex: none;
  font-family: var(--mono);
  font-size: 12px;
  font-weight: 600;
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}
.spacer { flex: 1; min-width: 0; }

.body {
  border-top: 1px solid var(--line-soft);
  padding: 8px 10px 10px;
}
.body h4 {
  margin: 12px 0 4px;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--ink-faint);
  font-weight: 600;
}
/* The first heading follows the facts, which carry their own spacing below. */
.body h4.first { margin-top: 6px; }
/* One box per group of controls.
   SOFTER than the fold's own border, and a hair lighter than its background --
   these are divisions WITHIN a card, and a box as strong as the card's would
   read as four cards. */
.group {
  border: 1px solid var(--line-soft);
  border-radius: 8px;
  background: color-mix(in srgb, var(--ink) 2%, transparent);
  padding: 2px 12px 10px;
  margin: 10px 0 0;
}
/* The heading is the box's label, so its top margin belongs to the box. */
.group > h4:first-child, .group > h4.first { margin-top: 8px; }
/* The band plan is a picture that runs to the box's edge better than it sits
   inset from it. */
.group > :deep(.plan) { margin-left: -2px; margin-right: -2px; }
.action-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin: 4px 0;
}
/* EVERY CELL IS EXACTLY ONE LINE TALL, at every width.

   The grid already sizes its columns from the container rather than from its
   content, so a value getting longer does not add a column. What it could still
   do is wrap INSIDE its cell: the 200px is a floor, `1fr` lets a track be
   narrower than what is in it, and `adapter MediaTek Inc. Wireless_Device` is
   about 215px at this size. One wrapped cell makes its whole row two lines and
   shoves everything below the strip down.

   So the value truncates instead. An ellipsis is a visible, stable failure; a
   reflow is an invisible one that moves the page under a reader's eyes.

   Deliberately NOT a fixed or minimum height on the strip: the right height
   genuinely differs with width, so any constant is wrong somewhere. */
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 2px 14px;
  font-size: 12px;
}
.facts > div {
  white-space: nowrap;
  min-width: 0; /* a grid item defaults to min-content, which refuses to shrink */
  overflow: hidden;
}
.facts .k { color: var(--ink-faint); margin-right: 6px; }
.facts .v { color: var(--ink-dim); overflow: hidden; text-overflow: ellipsis; }
/* Hex, MACs and counts, so digits keep their column as they change rather than
   sliding the rest of the value sideways on every update. */
.facts .v.num { font-variant-numeric: tabular-nums; }
.meta { font-size: 11px; color: var(--ink-faint); }
.warn-line { color: var(--warn); font-size: 11px; margin: 2px 0; }
.group-note { margin: 0 0 4px; }
/* NOT `.row`, which in this file is the collapsed adapter row's six-track grid
   and would drop a slider into the middle of it. */
/* The switch reads as a sentence rather than a pair of verbs, because there is
   only one thing to decide: whether the radio is lying. A two-button segment
   said "stop / claim this", which implied two actions where dragging a handle
   was already the second one. */
.action-row .chk {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--ink-dim);
  cursor: pointer;
}
.action-row .chk input { cursor: pointer; }
/* The estimate rides on the switch that would send it, so "what would this
   actually advertise" is answered where it is decided rather than a row away.
   Not tabular figures: it is a sentence, and .num fights one. */
.action-row .chk .fixv { color: var(--ink-faint); font-size: 11px; }
/* The two switches are one group, so they take the group's rhythm rather than
   the action row's, which is spaced for buttons. */
.body .action-row.beacon { margin: 2px 0; }

/* Indented to sit under the LABEL of the switch they belong to, rather than
   under its checkbox. Flush left they read as a third peer of the two switches,
   which is backwards -- these are that switch's values. 21px is the box plus
   its gap. */
.load-rows { margin: 2px 0 0 21px; }
/* Dimmed, not disabled. The handles still set what the beacon will carry the
   moment it is switched on, and moving one switches it on -- so they have to
   stay draggable while nothing is being advertised. */
.load-rows.off { opacity: 0.6; }
/* NOT `.row`, which in this file is the collapsed adapter row's six-track grid
   and would drop a slider into the middle of it. */
.load-row {
  display: grid;
  grid-template-columns: 72px minmax(80px, 190px) auto;
  align-items: center;
  gap: 10px;
  margin: 3px 0;
  font-size: 12px;
}
.load-row > label { color: var(--ink-faint); }
.load-row input[type='range'] { width: 100%; margin: 0; }
/* The value and the truth in ONE cell, because they are one fact read together.
   As two grid tracks the number sat right-aligned in a fixed column with a gap
   before the thing it was being compared against, and the pair read as two
   unrelated figures that happened to land near each other. */
.load-row .vals { display: inline-flex; align-items: baseline; gap: 8px; }
.load-row .val { color: var(--ink-dim); min-width: 26px; text-align: right; }
.load-row .floor { font-size: 11px; color: var(--ink-faint); }
.notice.inline { margin: 8px 0 0; }
/* An empty badge keeps its box so the controls after it never move. Invisible
   rather than absent: `visibility` reserves the space that `display:none` would
   give back, which is the whole point.
   Kept as its own rule and NOT folded into the selector below -- an earlier
   edit inserted it into the middle of `.badge.warn-badge`, which left the
   browser parsing `.badge .badge.blank` as a descendant selector that matches
   nothing, so every quiet radio drew an empty outlined box. */
.badge.blank { visibility: hidden; }
/* And a CONSTANT width, or the reservation is worthless: hiding a badge that
   held one space still gave back the difference between that and "AP
   DISABLED", so the row slid anyway -- which is the whole fault this was
   supposed to prevent. Wide enough for the longest thing it says. */
.warn-badge {
  min-width: 6.5rem;
  text-align: center;
}
.badge.warn-badge {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 45%, var(--line));
}
</style>
