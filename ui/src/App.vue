<script setup lang="ts">
import { computed, onUnmounted, provide, ref, watch } from 'vue';
import { useSnapshot } from '@/composables/useSnapshot';
import { useDevice } from '@/composables/useDevice';
import type { Client, Shape, ChartPrefs, SortMode, YMode, Pattern } from '@/types';
import { ntopngUrl, sortClients } from '@/types';
import ClientCard from '@/components/ClientCard.vue';
import ChartToolbar from '@/components/ChartToolbar.vue';

const { snap, connected, transport, series, bucketMs, setRange } = useSnapshot();
const dev = useDevice();

/*
 * The order the list is drawn in.
 *
 * Stored beside the chart preferences because it is the same kind of thing: a
 * view setting that should survive a reload rather than resetting every time
 * the page is opened to check on something.
 */
const SORT_KEY = 'pifi.sort';
function loadSort(): SortMode {
  try {
    const v = localStorage.getItem(SORT_KEY);
    return v === 'name' || v === 'traffic' ? v : 'busy';
  } catch {
    return 'busy';
  }
}
const sortMode = ref<SortMode>(loadSort());
watch(sortMode, (v) => {
  try {
    localStorage.setItem(SORT_KEY, v);
  } catch {
    /* private windows and blocked storage must not break the page */
  }
});

const clients = computed(() =>
  sortClients(snap.value?.clients ?? [], sortMode.value, snap.value?.time ?? Date.now()),
);
const caps = computed(() => snap.value?.caps);

/*
 * Whether the box can do bursty loss, provided once for every ShapeSliders on
 * the page. A property of the kernel rather than of any device, so threading it
 * through ClientCard and SubClasses -- neither of which has any use for it --
 * would be plumbing for its own sake.
 *
 * Defaults to available while the first snapshot is in flight, so the control
 * does not flicker through a disabled state on every page load.
 */
provide(
  'lossBurst',
  computed(() => ({
    ok: caps.value?.loss_burst ?? true,
    note: caps.value?.loss_burst_note ?? '',
  })),
);
const presentCount = computed(() => clients.value.filter((c) => c.present).length);

/**
 * The command that points a device at the box's iperf3 server.
 *
 * The host comes from the browser, not the server: the bridge holds several
 * addresses -- a DHCP one and a fixed rescue one on a private /24 clients
 * cannot reach -- and the box has no way to know which of them the reader can
 * get to. Whatever address this page was served over demonstrably works.
 */
const iperfCmd = computed(() =>
  caps.value?.iperf ? `iperf3 -c ${location.hostname}` : '',
);

const notices = computed(() => snap.value?.notices ?? []);
const errorNotices = computed(() => notices.value.filter((n) => n.level === 'error'));
const infoNotices = computed(() => notices.value.filter((n) => n.level !== 'error'));

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
const STORE_KEY = 'pifi.folded';

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
const CHART_KEY = 'pifi.chart';
const CHART_DEFAULTS: ChartPrefs = {
  rangeSec: 300, yMode: 'auto', yManual: 10,
  // Both on by default: the live trace is the record, and the mean is the
  // answer to the question most often being asked of it.
  showLive: true, showSustained: true,
  // Off by default: the taller plot costs how many devices are visible at once,
  // which is the more common need.
  tallCharts: false,
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
watch(() => chart.value.rangeSec, setRange, { immediate: true });

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
  <div class="wrap">
    <header class="top">
      <h1>infinite-streaming-pifi</h1>
      <span class="sub">per-client network link conditioner</span>
      <span class="spacer"></span>

      <span class="pill" :title="`transport: ${transport}`">
        <span class="dot" :class="connected ? 'live' : 'off'"></span>
        {{ connected ? (transport === 'sse' ? 'live' : 'polling') : 'disconnected' }}
      </span>
      <span v-if="caps" class="pill">
        bridge via {{ caps.uplink_if }}
      </span>
      <span class="pill">
        {{ presentCount }} connected
      </span>
      <!-- One control for the whole list rather than only per-card, so a page
           of devices can be opened or put away in a single action. -->
      <button
        v-if="clients.length > 1" class="pill link"
        @click="setAllFolded(anyExpanded)"
      >{{ anyExpanded ? 'fold all' : 'expand all' }}</button>
      <a
        v-if="caps?.ntopng"
        class="pill link"
        :href="ntopngUrl(caps.ntopng_port, '/lua/if_stats.lua')"
        target="_blank" rel="noopener"
        title="Traffic analysis for the whole bridge, in ntopng"
      >ntopng ↗</a>
    </header>

    <!-- Actionable messages stay at the top: an error the reader has to
         scroll past every device to find is worse than clutter. -->
    <div v-if="dev.conflict.value" class="notice bad">
      {{ dev.conflict.value }}
    </div>
    <div v-for="n in errorNotices" :key="n.text" class="notice bad">
      {{ n.text }}
    </div>

    <ChartToolbar
      v-if="clients.length"
      :range-sec="chart.rangeSec" :y-mode="chart.yMode" :y-manual="chart.yManual"
      :bucket-ms="bucketMs" :sort-mode="sortMode"
      @sort-mode="(v: SortMode) => (sortMode = v)"
      :show-live="chart.showLive" :show-sustained="chart.showSustained"
      :tall-charts="chart.tallCharts"
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
      :collapsed="isFolded(c.mac)"
      @toggle="toggleFold(c.mac)"
      @shape="(dir, s) => dev.patchShape(c.mac, rev(c), dir, s)"
      @preset="(down: Shape, up: Shape) => dev.patchPolicy(c.mac, rev(c), { down, up })"
      @label="(l: string) => dev.patchPolicy(c.mac, rev(c), { label: l })"
      @reset="dev.reset(c.mac)"
      @forget="dev.forget(c.mac)"
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

    <!-- Standing truths about how the box behaves. They never change and never
         need acting on, so they read as footnotes rather than pushing the
         devices -- the actual content -- below the fold. -->
    <footer v-if="infoNotices.length || iperfCmd" class="notes">
      <div v-for="n in infoNotices" :key="n.text" class="notice info">
        {{ n.text }}
      </div>
      <!-- The two directions measure different things, and which is which is
           the easiest thing here to get backwards. Said explicitly rather than
           left to be discovered from a number that looks wrong. -->
      <div v-if="iperfCmd" class="notice info">
        Measure a device with <code>{{ iperfCmd }} -R</code>: the reverse
        direction is that device's <strong>downlink</strong>, and it is
        conditioned by the policy set here, so this is the cap being enforced.
        Without <code>-R</code> you are measuring upload to this box, which ends
        here and never reaches the {{ caps?.uplink_if }} queue where uplink
        shaping lives — that reports what the link can do, not what the policy
        allows. Verifying <strong>uplink</strong> needs load from a host beyond
        {{ caps?.uplink_if }}.
      </div>
    </footer>
  </div>
</template>
