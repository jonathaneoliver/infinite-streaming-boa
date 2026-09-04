<script setup lang="ts">
import { computed, provide, ref, watch } from 'vue';
import { useSnapshot } from '@/composables/useSnapshot';
import { useDevice } from '@/composables/useDevice';
import { ntopngUrl, glancesUrl } from '@/types';
import { exportConfig, importConfig } from '@/composables/useConfig';
import ClientsView from '@/components/ClientsView.vue';
import BridgeView from '@/components/BridgeView.vue';
import EventLog from '@/components/EventLog.vue';

const { snap, connected, transport, series, bucketMs, rangeSec, setRange } = useSnapshot();
const dev = useDevice();

/*
 * One useDevice for the page.
 *
 * It holds a debounce timer per device and the in-flight write state, so a
 * second instance would coalesce nothing and race the first. Provided rather
 * than passed down, the same shape already used for lossBurst.
 */
provide('dev', dev);

/*
 * There is no view switch any more.
 *
 * This used to be two tabs -- devices, and the box -- on the reasoning that
 * those are two different questions, with issue #122 cited for keeping
 * per-device controls away from ones that hit a whole radio. That separation is
 * still real and is now expressed by LAYOUT rather than by hiding half the page:
 * the adapters are a rack at the top whose every control says how many clients
 * it affects, and the devices are below it. Nothing box-wide has drifted in
 * among the per-device controls.
 *
 * What the tabs actually cost was the common case. Almost every question here
 * spans both halves -- which radio is this phone on, why did its throughput
 * fall when the channel moved -- and answering one meant remembering one tab
 * while looking at the other.
 */

const caps = computed(() => snap.value?.caps);
// The running build's version, stamped into the daemon at build time and shown
// in the footer so an operator can tell which build a box is on. "dev" for a
// development build; see main.version and scripts/version.sh.
const version = computed(() => snap.value?.version);

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
const presentCount = computed(
  () => (snap.value?.clients ?? []).filter((c) => c.present).length,
);

// The AP radio, named in the header. USB adapters report the speed they
// NEGOTIATED, which is the number worth showing: a USB 3 adapter on a bad port
// or a cable without SuperSpeed pins enumerates as USB 2 and then looks correct
// everywhere else while delivering a fraction of the throughput.
const radioLabel = computed(() => {
  const a = caps.value?.adapter;
  if (!a?.iface) return '';
  if (a.bus !== 'usb') return `radio: ${a.iface} (onboard)`;
  // link_mbps is what it negotiated, not what it can do. 5000 and above is
  // SuperSpeed; 480 is High-Speed and worth flagging.
  const gen = !a.link_mbps ? '' : a.link_mbps >= 5000 ? ' · USB 3' : ' · USB 2';
  return `radio: ${a.iface}${gen}`;
});

// True only for the case that is wrong but looks right: a USB adapter running
// at High-Speed. An onboard radio is not degraded, it is just not on the bus.
const radioDegraded = computed(() => {
  const a = caps.value?.adapter;
  return !!a && a.bus === 'usb' && !!a.link_mbps && a.link_mbps < 5000;
});

const radioTitle = computed(() => {
  const a = caps.value?.adapter;
  if (!a?.iface) return '';
  if (a.bus !== 'usb') return `onboard radio${a.driver ? ` (${a.driver})` : ''}`;
  const bits = [
    [a.vendor, a.product].filter(Boolean).join(' ') || 'USB adapter',
    a.driver && `driver ${a.driver}`,
    a.usb_version && `declares USB ${a.usb_version}`,
    a.link_mbps && `negotiated ${a.link_mbps} Mb/s`,
  ].filter(Boolean);
  if (radioDegraded.value) {
    bits.push('running at USB 2 speed — reseat it in a SuperSpeed port');
  }
  return bits.join(' · ');
});

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

/**
 * Saving and restoring the whole setup.
 *
 * The box holds the work -- conditioning, ladders, saved and merged patterns --
 * and the browser holds the view. A file that carried only one of those would
 * restore half a setup, so it carries both. See useConfig.
 */
const cfgMsg = ref('');
const cfgErr = ref('');

async function onSaveConfig() {
  cfgErr.value = '';
  try {
    cfgMsg.value = `saved ${await exportConfig()}`;
  } catch (e) {
    cfgErr.value = `save failed: ${(e as Error).message}`;
  }
}

async function onLoadConfig(e: Event) {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  // Cleared straight away, so picking the same file twice still fires a change.
  input.value = '';
  if (!file) return;
  cfgErr.value = '';
  cfgMsg.value = '';
  try {
    const n = await importConfig(file);
    // Only what the document actually carried. A version 2 export holds no
    // devices at all, and "loaded 0 device(s)" reads as a failure when the
    // import in fact restored a ladder that cost an hour of real streaming.
    const parts: string[] = [];
    if (n.ladder) parts.push('ladder');
    if (n.patterns) parts.push(`${n.patterns} pattern(s)`);
    if (n.devices) parts.push(`${n.devices} device(s)`);
    cfgMsg.value = `loaded ${parts.join(', ')} — reloading`;
    // A reload rather than reactive re-application: the restored view
    // preferences are read once at startup by design, and re-plumbing every one
    // of them to be settable at runtime would be a lot of machinery for an
    // action taken once in a while.
    setTimeout(() => location.reload(), 700);
  } catch (err) {
    cfgErr.value = `load failed: ${(err as Error).message}`;
  }
}
</script>

<template>
  <div class="wrap">
    <!-- The header stays on both tabs. A USB adapter that quietly negotiated
         High-Speed is invisible from every other angle, and it must not become
         invisible here too just because you were looking at the device list. -->
    <header class="top">
      <h1>infinite-streaming-boa</h1>
      <span class="sub">per-client network link conditioner</span>
      <span class="spacer"></span>

      <span class="pill" :title="`transport: ${transport}`">
        <span class="dot" :class="connected ? 'live' : 'off'"></span>
        {{ connected ? (transport === 'sse' ? 'live' : 'polling') : 'disconnected' }}
      </span>
      <span v-if="caps" class="pill">
        bridge via {{ caps.uplink_if }}
      </span>
      <!-- Which radio is serving, and whether a USB one got the bus speed it
           was sold at. A USB 3 adapter that quietly enumerated at USB 2 is
           identical from every other angle -- same channel, same 802.11ax,
           same PHY rate -- while carrying a fraction of the throughput, so it
           is worth a permanent readout rather than a note in a document. -->
      <span v-if="radioLabel" class="pill" :class="{ warn: radioDegraded }" :title="radioTitle">
        {{ radioLabel }}
      </span>
      <button
        class="pill link" title="Download this box's setup: every device's
conditioning, its ladders, saved and merged patterns, and this browser's chart
preferences."
        @click="onSaveConfig()"
      >export config</button>
      <!-- A label WRAPPING the input, not a button that clicks it from
           script. Clicking a label opens its own file picker natively, with no
           JavaScript and no question about user activation -- and a
           script-driven .click() on a hidden input is exactly the kind of thing
           a browser may decline without saying so, which is how this shipped
           looking like a button that did nothing. -->
      <label class="pill link" title="Restore a setup from a file. Merges: devices in
the file are replaced, devices not mentioned are left alone.">
        import config
        <input
          type="file" accept="application/json,.json"
          class="hidden-file" @change="onLoadConfig"
        />
      </label>
      <a
        v-if="caps?.ntopng"
        class="pill link"
        :href="ntopngUrl(caps.ntopng_port, '/lua/if_stats.lua')"
        target="_blank" rel="noopener"
        title="Traffic analysis for the whole bridge, in ntopng"
      >ntopng ↗</a>
      <a
        v-if="caps?.glances"
        class="pill link"
        :href="glancesUrl(caps.glances_port)"
        target="_blank" rel="noopener"
        title="The box's own health -- CPU, memory, temperature, disk and
per-process load -- in glances. Says nothing about the clients; this is the
appliance watching itself."
      >glances ↗</a>
    </header>

    <!-- Above everything, because it belongs to the box rather than to any one
         part of it: a client roaming between radios is a fact about the radio
         and about the device at the same time. -->
    <EventLog />

    <div v-if="cfgErr" class="notice bad">{{ cfgErr }}</div>
    <div v-if="cfgMsg" class="notice">{{ cfgMsg }}</div>

    <!-- Actionable messages stay at the top: an error the reader has to
         scroll past every device to find is worse than clutter. -->
    <div v-if="dev.conflict.value" class="notice bad">
      {{ dev.conflict.value }}
    </div>
    <div v-for="n in errorNotices" :key="n.text" class="notice bad">
      {{ n.text }}
    </div>

    <!-- ONE scrolling view: the fabric, the adapters, then the devices.

         This was two tabs, on the reasoning that the page answered two
         different questions. It does not. Nearly every question here spans
         both -- which radio is this phone on, why did its throughput fall when
         the channel moved, is that adapter the busy one -- and answering them
         meant holding one tab in your head while reading the other.

         The order is the point. The fabric is one line because it is almost
         never the answer. The adapters are folded because their detail is
         occasional and their summary is not. The devices are open, because
         they are what the page is for. -->
    <BridgeView
      :active="true" :clients="snap?.clients"
      :series="series" :range-sec="rangeSec"
    />
    <ClientsView
      :snap="snap" :series="series" :bucket-ms="bucketMs" :caps="caps"
      @range="setRange"
    />

    <!-- Standing truths about how the box behaves. They never change and never
         need acting on, so they read as footnotes rather than pushing the
         devices -- the actual content -- below the fold. -->
    <footer v-if="infoNotices.length || iperfCmd || version" class="notes">
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
      <!-- Which build this box is on. Quiet by design: an operator needs it
           when reporting a result or an issue, not while working. -->
      <div v-if="version" class="version">boa {{ version }}</div>
    </footer>
  </div>
</template>
