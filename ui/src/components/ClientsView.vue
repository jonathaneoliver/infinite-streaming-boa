<script setup lang="ts">
import { computed, inject, onUnmounted, ref, watch } from 'vue';
import type {
  Capabilities, ChartPrefs, Client, Pattern, Series, Shape, Snapshot, SortMode, YMode,
} from '@/types';
import { SUSTAINED_SEC, sortClients } from '@/types';
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
const CHART_KEY = 'boa.chart';
const CHART_DEFAULTS: ChartPrefs = {
  rangeSec: 300, yMode: 'auto', yManual: 10,
  // Both on by default: the live trace is the record, and the mean is the
  // answer to the question most often being asked of it.
  showLive: true, showSustained: true, sustainedSec: SUSTAINED_SEC,
  // Off by default: the taller plot costs how many devices are visible at once,
  // which is the more common need.
  tallCharts: false,
  // Both on: hiding a direction is a deliberate act, and a card that silently
  // omitted one would be a card lying about what it is conditioning.
  showDown: true, showUp: true,
};

function loadChart(): ChartPrefs {
  try {
    // Merged over the defaults, so a stored object written by an older build
    // (or a hand-edited one) cannot leave a field undefined.
    return { ...CHART_DEFAULTS, ...JSON.parse(localStorage.getItem(CHART_KEY) ?? '{}') };
  } catch {
    return { ...CHART_DEFAULTS };
  }
}
const chart = ref<ChartPrefs>(loadChart());
watch(
  chart,
  (v) => {
    try {
      localStorage.setItem(CHART_KEY, JSON.stringify(v));
    } catch {
      /* private windows and blocked storage must not break the page */
    }
  },
  { deep: true },
);
// A longer range needs history the page has not fetched yet; a shorter one is
// already in memory. useSnapshot decides which, so this can just say what is
// wanted.
watch(() => chart.value.rangeSec, (v) => emit('range', v), { immediate: true });

/**
 * The right-hand edge of every plot, advanced once a second.
 *
 * Held still while the pointer is inside a chart. Without that, the plot slides
 * left under the cursor while it is being read, and the crosshair silently
 * comes to rest on a different sample than the one aimed at -- the reading is
 * wrong by however long the pointer stayed there.
 */
const now = ref(Date.now());
const hovering = ref(false);
const ticker = window.setInterval(() => {
  if (!hovering.value) now.value = Date.now();
}, 1000);
onUnmounted(() => window.clearInterval(ticker));
</script>

<template>
  <div class="clients-view">
    <!-- The list's own controls: how many are hidden, and whether the cards are
         open. They sit with the list rather than in the page header now that
         the header is shared with a tab that has no devices on it. -->
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
    />

    <ClientCard
      v-for="c in clients" :key="c.mac"
      :client="c" :series="series[c.mac]"
      :chart="chart" :now="now"
      @hovering="(v: boolean) => (hovering = v)"
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
.list-controls {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}
</style>
