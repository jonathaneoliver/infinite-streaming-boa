<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import type { Client, Shape, Series, ChartPrefs, Pattern } from '@/types';
import { CLEAN, DEVELOPER, PRESETS, ntopngUrl, patternFromPolicy } from '@/types';
import ShapeSliders from './ShapeSliders.vue';
import SubClasses from './SubClasses.vue';
import LadderPanel from './LadderPanel.vue';
import PatternPanel from './PatternPanel.vue';
import TrafficChart from './TrafficChart.vue';

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
 * The folded row's columns, which have to follow the directions being shown.
 *
 * A fixed template with the cells v-if'd out would not do: the columns are
 * positional, so dropping the two uplink cells slides the unit, the conditioned
 * badge and the actions two columns to the left and every row stops lining up
 * -- which is the exact failure the fixed template was introduced to fix.
 * Built here instead, from one source, so the cells and the columns cannot
 * disagree.
 */
const foldedCols = computed(() => [
  '30px', '14px', 'minmax(96px, 1.3fr)', '58px', 'minmax(104px, 1fr)',
  ...(props.chart.showDown ? ['76px', '68px'] : []),
  ...(props.chart.showUp ? ['76px', '68px'] : []),
  '38px', '92px', '56px',
].join(' '));

/** Two directions side by side, or one taking the full width. */
const dirCols = computed(() =>
  props.chart.showDown && props.chart.showUp ? '1fr 1fr' : '1fr');

const EXPANDED_H = 196;
const expandedH = computed(() => (props.chart.tallCharts ? EXPANDED_H * 2 : EXPANDED_H));

const open = ref(false);

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
const linkFlash = ref<'drop' | 'nudge' | 'deadzone' | null>(null);
let linkFlashTimer: ReturnType<typeof setTimeout> | undefined;
function flashLink(kind: 'drop' | 'nudge' | 'deadzone') {
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
 * Which keyframe the sliders are editing, or null for the stored policy.
 *
 * Never a keyframe while a run is playing: the controls are then reporting what
 * is enforced, and accepting an edit into a keyframe while displaying the
 * enforced value would write one thing where the operator could see another.
 */
const patSelected = ref<number | null>(null);
const editKey = computed(() =>
  !playing.value && patSelected.value != null
    ? (pattern.value?.keys[patSelected.value] ?? null)
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

/** Route a slider to the selected keyframe, or to the stored policy. */
function onShape(dir: 'down' | 'up', s: Shape) {
  if (editKey.value && pattern.value && patSelected.value != null) {
    const next = JSON.parse(JSON.stringify(pattern.value)) as Pattern;
    next.keys[patSelected.value][dir] = s;
    applyPattern(next);
    return;
  }
  emit('shape', dir, s);
}

// Presets follow the sliders: with a keyframe selected, "3G" means "this
// moment is 3G", which is the shortest route from a named link to a timeline.
function onPreset(down: Shape, up: Shape) {
  if (editKey.value && pattern.value && patSelected.value != null) {
    const next = JSON.parse(JSON.stringify(pattern.value)) as Pattern;
    next.keys[patSelected.value].down = down;
    next.keys[patSelected.value].up = up;
    applyPattern(next);
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
      v-if="collapsed" class="card-head folded"
      :style="{ gridTemplateColumns: foldedCols }"
      @click="onHeadClick"
    >
      <button
        class="fold-toggle ghost" @click="emit('toggle')"
        title="Expand this device" :aria-expanded="false"
      >&#9656;</button>

      <span
        class="dot" :class="client.present ? 'live' : 'off'"
        :title="client.present ? 'Connected now' : 'Not currently connected'"
      ></span>

      <input
        class="name" :value="client.label"
        @change="emit('label', ($event.target as HTMLInputElement).value)"
      />

      <span class="cell">
        <span v-if="client.medium" class="badge" :class="client.medium">
          {{ client.medium }}
        </span>
      </span>

      <span class="cell meta num addr">
        {{ client.ip || (client.ipv6?.length ? client.ipv6[0] : 'no address yet') }}
      </span>

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

      <span class="cell">
        <span v-if="conditioned" class="badge" style="color: var(--warn)">
          conditioned
        </span>
      </span>

      <span class="cell">
        <button v-if="!client.present" class="ghost" @click="emit('forget')">
          forget
        </button>
      </span>

    </div>

    <div v-else class="card-head" @click="onHeadClick">
      <button
        class="fold-toggle ghost" @click="emit('toggle')"
        title="Fold this device away" :aria-expanded="true"
      >&#9662;</button>

      <span
        class="dot"
        :class="client.present ? 'live' : 'off'"
        :title="client.present ? 'Connected now' : 'Not currently connected'"
      ></span>

      <input
        class="name" :value="client.label"
        @change="emit('label', ($event.target as HTMLInputElement).value)"
      />

      <span v-if="client.medium" class="badge" :class="client.medium">
        {{ client.medium }}
      </span>
      <span class="meta num">{{ client.mac }}</span>
      <span v-if="client.ip" class="meta num">{{ client.ip }}</span>
      <!-- Privacy extensions give a device several v6 addresses at once, so
           the count matters more than any one value; all of them are shaped. -->
      <span
        v-if="client.ipv6?.length" class="meta num"
        :title="'IPv6, all conditioned:\n' + client.ipv6.join('\n')"
      >+{{ client.ipv6.length }} IPv6</span>
      <!-- An explicit condition, not a v-else. Inserting the IPv6 badge above
           re-paired the v-else with the IPv6 test, so a client with an IPv4
           address but no v6 rendered "192.168.0.214  no address yet". -->
      <span
        v-if="!client.ip && !client.ipv6?.length" class="meta"
        title="Associated, but has not taken an address yet"
      >no address yet</span>

      <!-- Only shown when the driver actually reports it. The Pi's Broadcom
           chip gives no per-station RSSI in AP mode, and printing "0 dBm" is
           a confident-looking lie. -->
      <span v-if="client.station?.signal_dbm" class="meta num" :class="signalClass">
        {{ client.station.signal_dbm }} dBm
      </span>
      <span
        v-else-if="client.station" class="meta num"
        title="This radio reports no per-station signal level in AP mode; transmit failures stand in as the link-quality indicator"
      >tx-fail {{ client.station.tx_failed.toLocaleString() }}</span>
      <span
        v-if="client.station"
        class="meta num"
        title="Negotiated radio modulation rate, NOT achieved throughput"
      >
        PHY {{ client.station.tx_phy_mbps.toFixed(0) }}
      </span>
      <span
        v-if="client.station"
        class="meta num"
        title="How long this device has been continuously associated. It resets when the link drops and re-associates, so it falls to a few seconds right after a drop or nudge."
      >assoc {{ connectedLabel }}</span>

      <span class="spacer"></span>

      <!-- Deep links into ntopng for THIS device. Shown only when ntopng is
           answering and the client has an address, since both are required
           for the filtered view to resolve to anything. -->
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

      <span v-if="conditioned" class="badge" style="color: var(--warn)">
        conditioned
      </span>
      <button v-if="conditioned" @click="emit('reset')">reset</button>
      <!-- Only offered for a device that is not here: forgetting one that is
           present just makes it reappear unconfigured a second later. -->
      <button v-if="!client.present" class="ghost" @click="emit('forget')">
        forget
      </button>
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
            keyframe {{ (patSelected ?? 0) + 1 }} &middot; {{ editKey.at_sec }}s
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
            keyframe {{ (patSelected ?? 0) + 1 }} &middot; {{ editKey.at_sec }}s
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
    <div v-if="linkControl && client.present" class="link-events">
      <span class="link-label">Link</span>
      <button
        class="ghost" :class="{ flash: linkFlash === 'drop' }" @click="fireLink('drop')"
        title="Deauthenticate: take this client's Wi-Fi link down; it reconnects on its own"
      >{{ linkFlash === 'drop' ? 'sent' : 'drop' }}</button>
      <button
        class="ghost" :class="{ flash: linkFlash === 'nudge' }" @click="fireLink('nudge')"
        title="Disassociate: the softer 802.11 disconnect, usually a quicker recovery than drop"
      >{{ linkFlash === 'nudge' ? 'sent' : 'nudge' }}</button>
      <button
        class="ghost" :class="{ flash: linkFlash === 'deadzone' }" @click="fireDeadzone"
        title="Hold the link down for the duration -- long enough to drain a player's buffer and force a rebuffer, unlike a single drop"
      >{{ linkFlash === 'deadzone' ? 'sent' : `deadzone ${deadzoneSec}s` }}</button>
      <input
        type="number" min="1" max="300" step="1" v-model.number="deadzoneSec"
        class="dz-dur" title="deadzone length in seconds" aria-label="deadzone seconds"
      />
      <span class="link-hint meta">watch <b>assoc</b> above reset when it lands</span>
    </div>

    <!-- The timeline sits directly under the controls that author it. The
         sliders above ARE the keyframe editor, and putting the playhead
         anywhere else would separate the control from the thing it edits. -->
    <PatternPanel
      :pattern="pattern"
      :mac="client.mac"
      :rev="client.policy?.rev ?? 0"
      :ladders="client.policy?.ladders ?? []"
      :run="client.pattern_run"
      :selected="patSelected"
      :can-play="patBlocked === ''"
      :blocked="patBlocked"
      @update="applyPattern"
      @create="createPattern"
      @remove="removePattern"
      @play="emit('patternPlay')"
      @stop="emit('patternStop')"
      @select="(i: number | null) => (patSelected = i)"
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

/* Fixed columns so every folded row lines up. Flex sized each row to its own
   content, which made a list of devices impossible to scan down. */
.card-head.folded {
  display: grid;
  grid-template-columns:
    30px                  /* fold toggle -- first, so it is in the same place
                             on every row and does not shift with content */
    14px                  /* presence */
    minmax(96px, 1.3fr)   /* name */
    58px                  /* medium */
    minmax(104px, 1fr)    /* address */
    76px 68px             /* downlink: shape, then figure -- 68px fits a
                             three-digit rate such as 103.66 without touching
                             the next column */
    76px 68px             /* uplink */
    38px                  /* unit */
    92px                  /* conditioned */
    56px;                 /* actions */
  align-items: center;
  gap: 8px;
  overflow: hidden;
}
.card-head.folded:hover { background: var(--panel); }
.card-head.folded .cell { min-width: 0; overflow: hidden; }
.card-head.folded .addr,
.card-head.folded .name { text-overflow: ellipsis; white-space: nowrap; }
.card-head.folded .val { text-align: right; font-size: 12px; font-weight: 600; }
.card-head.folded .unit { font-size: 11px; }
.card-head.folded .spark { display: block; }
.card-head.folded .name { flex: 0 1 auto; min-width: 90px; }
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
