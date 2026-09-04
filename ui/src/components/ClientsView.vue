<script setup lang="ts">
import { computed, inject, ref, watch } from 'vue';
import type {
  Capabilities, ChartPrefs, Client, Pattern, Series, Shape, Snapshot, SortMode, YMode,
} from '@/types';
import { SUSTAINED_SEC, sortClients } from '@/types';
import { chartPrefs } from '@/composables/useChartPrefs';
import { chartNow, holdClock } from '@/composables/useChartClock';
import type { useDevice } from '@/composables/useDevice';
import ClientCard from '@/components/ClientCard.vue';
import ChartToolbar from '@/components/ChartToolbar.vue';

/*
 * The device list: what every client is getting, and every control that
 * conditions one.
 *
 * Lifted out of App.vue unchanged when the Bridge tab arrived. Everything here
 * is per-device by construction -- the box-wide radio controls live in
 * BridgeView precisely because they are not, and mixing the two is what issue
 * #122 calls "the collision".
 */

const props = defineProps<{
  snap: Snapshot | null;
  series: Record<string, Series>;
  bucketMs: number;
  caps?: Capabilities;
}>();

// The visible range needs history the page may not have fetched, and only
// useSnapshot can decide whether a refetch is required. It lives in App,
// which owns the one EventSource, so the choice is announced upwards.
const emit = defineEmits<{ (e: 'range', sec: number): void }>();

/*
 * The write path, injected rather than passed as a prop.
 *
 * useDevice holds in-flight request state and a debounce timer per device, so
 * there must be exactly one of it -- a second instance would coalesce nothing
 * and race the first. App owns it and provides it, the same shape the page
 * already uses for lossBurst.
 */
const dev = inject('dev') as ReturnType<typeof useDevice>;

/*
 * The order the list is drawn in.
 *
 * Stored beside the chart preferences because it is the same kind of thing: a
 * view setting that should survive a reload rather than resetting every time
 * the page is opened to check on something.
 */
const SORT_KEY = 'boa.sort';
function loadSort(): SortMode {
  try {
    const v = localStorage.getItem(SORT_KEY);
    return v === 'name' || v === 'traffic' ? v : 'busy';
  } catch {
    return 'busy';
  }
}
const sortMode = ref<SortMode>(loadSort());

/*
 * Whether devices that are not currently connected are listed.
 *
 * Off by default. The learner deliberately keeps a binding for twenty minutes
 * after a device goes quiet -- see the comment on the ARP merge in state.go --
 * so a phone that left the building stays in the list long enough to look like
 * a client that is still there. On a box whose whole purpose is measuring what
 * is on the air, that reads as contention that does not exist.
 *
 * They are hidden rather than dropped: a device keeps its policy while it is
 * away, and the button says how many are hidden so nothing disappears silently.
 */
const OFFLINE_KEY = 'boa.offline';
function loadShowOffline(): boolean {
  try {
    return localStorage.getItem(OFFLINE_KEY) === 'true';
  } catch {
    return false;
  }
}
const showOffline = ref<boolean>(loadShowOffline());
watch(showOffline, (v) => {
  try {
    localStorage.setItem(OFFLINE_KEY, String(v));
  } catch {
    /* as above */
  }
});
watch(sortMode, (v) => {
  try {
    localStorage.setItem(SORT_KEY, v);
  } catch {
    /* private windows and blocked storage must not break the page */
  }
});

const allClients = computed(() =>
  sortClients(props.snap?.clients ?? [], sortMode.value, props.snap?.time ?? Date.now()),
);
const clients = computed(() =>
  showOffline.value ? allClients.value : allClients.value.filter((c) => c.present),
);
const offlineCount = computed(() => allClients.value.filter((c) => !c.present).length);

// Every write carries the revision the operator was looking at, so a
// simultaneous edit from another tab is refused rather than silently lost.
const rev = (c: Client) => c.policy.rev;

/**
 * Fold state.
 *
 * The default is "folded when there is more than one device": folding the only
 * card on the page helps nobody, and a long scroll of full cards stops the page
 * answering "what is my network doing" at a glance.
 *
 * An explicit choice always wins over that default and persists, so a card
 * deliberately opened stays open across a reload. Stored per MAC; unknown
 * devices simply fall back to the default.
 */
const STORE_KEY = 'boa.folded';

function loadPrefs(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(STORE_KEY) ?? '{}');
  } catch {
    return {}; // a corrupt or unavailable store is not worth failing over
  }
}
const prefs = ref<Record<string, boolean>>(loadPrefs());
watch(
  prefs,
  (v) => {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify(v));
    } catch {
      /* private windows and blocked storage must not break the page */
    }
  },
  { deep: true },
);

const isFolded = (mac: string) => prefs.value[mac] ?? clients.value.length > 1;
function toggleFold(mac: string) {
  prefs.value = { ...prefs.value, [mac]: !isFolded(mac) };
}
function setAllFolded(folded: boolean) {
  const next = { ...prefs.value };
  for (const c of clients.value) next[c.mac] = folded;
  prefs.value = next;
}
const anyExpanded = computed(() => clients.value.some((c) => !isFolded(c.mac)));

/**
 * Chart settings.
 *
 * Page-wide rather than per card, because the point of them is comparison: two
 * devices drawn on different ranges or different axes look alike when they are
 * an order of magnitude apart.
 *
 * Persisted for the same reason the fold state is -- a chosen range is a
 * working setup, and having it reset on every reload makes the page feel like
 * it forgets what you were doing.
 */
const chart = chartPrefs;

// A longer range needs history the page has not fetched yet; a shorter one is
// already in memory. useSnapshot decides which, so this can just say what is
// wanted.
watch(() => chart.value.rangeSec, (v) => emit('range', v), { immediate: true });

/* The right-hand edge of every plot, and the hold that keeps it still while a
   chart is being read. Both now live in useChartClock, shared with the adapter
   charts in the folds above: two tickers would drift and put "now" at two
   different x positions on panes meant to line up. */
const now = chartNow;
</script>

<template>
  <div class="clients-view">
    <!-- Titled, and at the same weight as the rack above it. The two are the
         page's only sections now that the tabs are gone, and a reader scrolling
         past the adapters needs to see where one ends and the other begins --
         which a bare list of cards does not say. -->
    <h2 class="section-title">
      clients<span v-if="clients.length" class="count">{{ clients.length }}</span>
    </h2>

    <!-- The list's own controls: how many are hidden, and whether the cards are
         open. They sit with the list rather than in the page header, which is
         shared with everything else on the page. -->
    <div v-if="offlineCount || showOffline || clients.length > 1" class="list-controls">
      <button
        v-if="offlineCount || showOffline" class="pill link"
        :title="showOffline
          ? 'hide devices that are not currently connected'
          : `${offlineCount} device(s) seen recently but not connected now`"
        @click="showOffline = !showOffline"
      >{{ showOffline ? 'hide offline' : `${offlineCount} offline` }}</button>
      <!-- One control for the whole list rather than only per-card, so a page
           of devices can be opened or put away in a single action. -->
      <button
        v-if="clients.length > 1" class="pill link"
        @click="setAllFolded(anyExpanded)"
      >{{ anyExpanded ? 'fold all' : 'expand all' }}</button>
    </div>

    <ChartToolbar
      v-if="clients.length"
      :range-sec="chart.rangeSec" :y-mode="chart.yMode" :y-manual="chart.yManual"
      :bucket-ms="bucketMs" :sort-mode="sortMode"
      @sort-mode="(v: SortMode) => (sortMode = v)"
      :show-live="chart.showLive" :show-sustained="chart.showSustained"
      :show-phy="chart.showPhy"
      :tall-charts="chart.tallCharts" :sustained-sec="chart.sustainedSec"
      :show-down="chart.showDown" :show-up="chart.showUp"
      @show-down="(v: boolean) => (chart = { ...chart, showDown: v })"
      @show-up="(v: boolean) => (chart = { ...chart, showUp: v })"
      @sustained-sec="(v: number) => (chart = { ...chart, sustainedSec: v })"
      @tall-charts="(v: boolean) => (chart = { ...chart, tallCharts: v })"
      @range="(v: number) => (chart = { ...chart, rangeSec: v })"
      @y-mode="(v: YMode) => (chart = { ...chart, yMode: v })"
      @y-manual="(v: number) => (chart = { ...chart, yManual: v })"
      @show-live="(v: boolean) => (chart = { ...chart, showLive: v })"
      @show-sustained="(v: boolean) => (chart = { ...chart, showSustained: v })"
      @show-phy="(v: boolean) => (chart = { ...chart, showPhy: v })"
    />

    <!-- Addressable, so the adapter rack above can jump down to a named device
         the same way a device row jumps up to its adapter. Keyed by MAC like
         everything else that has to survive a rename or a roam. -->
    <ClientCard
      v-for="c in clients" :key="c.mac" :id="`client-${c.mac}`"
      :client="c" :series="series[c.mac]"
      :chart="chart" :now="now"
      @hovering="holdClock"
      :ntopng-port="caps?.ntopng ? caps.ntopng_port : 0"
      :link-control="caps?.link_control ?? false"
      :collapsed="isFolded(c.mac)"
      @toggle="toggleFold(c.mac)"
      @shape="(dir, s) => dev.patchShape(c.mac, rev(c), dir, s)"
      @preset="(down: Shape, up: Shape) => dev.patchPolicy(c.mac, rev(c), { down, up })"
      @label="(l: string) => dev.patchPolicy(c.mac, rev(c), { label: l })"
      @reset="dev.reset(c.mac)"
      @forget="dev.forget(c.mac)"
      @link-drop="dev.linkDeauth(c.mac)"
      @link-nudge="dev.linkDisassoc(c.mac)"
      @link-deadzone="(sec: number) => dev.linkDeadzone(c.mac, sec)"
      @link-steer="dev.linkSteer(c.mac)"
      @add-sub="dev.addSub(c.mac, rev(c), 'new rule', {})"
      @remove-sub="(id: string) => dev.deleteSub(c.mac, id)"
      @patch-sub="(id: string, p: Record<string, unknown>) => dev.patchSub(c.mac, id, rev(c), p)"
      @sub-shape="(id: string, dir: 'down' | 'up', s: Shape) => dev.patchSub(c.mac, id, rev(c), { [dir]: s })"
      @sweep="(svc: string) => dev.startSweep(c.mac, svc)"
      @stop-sweep="dev.stopSweep(c.mac)"
      @remove-ladder="(svc: string) => dev.removeLadder(c.mac, svc)"
      @pattern-update="(p: Pattern) => dev.putPattern(c.mac, rev(c), p)"
      @pattern-remove="dev.deletePattern(c.mac)"
      @pattern-play="dev.playPattern(c.mac)"
      @pattern-stop="dev.stopPattern(c.mac)"
    />

    <div v-if="!clients.length" class="empty">
      <p>No devices seen yet.</p>
      <p class="meta">
        Join the Wi-Fi network or plug a device into the USB ethernet port.
        Devices appear here as soon as they send their first packet.
      </p>
    </div>
  </div>
</template>

<style scoped>
/* Section titles, shared in spirit with the rack's. Loud enough to divide the
   page now that nothing else does: at the old 10px faint weight they read as
   captions on the thing below them rather than as the two headings this page
   has. The count rides along because "how many devices" is the first question
   asked of this list. */
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

.list-controls {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}
</style>
