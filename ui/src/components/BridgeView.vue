<script setup lang="ts">
import { computed, ref, watchEffect, type Ref } from 'vue';
import { DEVELOPER } from '@/types';
import type { Client, IfaceInfo, PatternView, PortFlow, Series } from '@/types';
import { useBridge } from '@/composables/useBridge';
import InterfaceDiagram from '@/components/InterfaceDiagram.vue';
import FabricStrip from '@/components/FabricStrip.vue';
import AdapterRack from '@/components/AdapterRack.vue';
import AdapterStack from '@/components/AdapterStack.vue';
import AdapterPatternPanel from '@/components/AdapterPatternPanel.vue';
import ChannelPlan from '@/components/ChannelPlan.vue';
import { setAdapterIfaces } from '@/composables/useAdapters';

/*
 * The box: its fabric, and a rack of the adapters that carry clients.
 *
 * Sits above the device list rather than behind a tab. Issue #122's "collision"
 * -- box-wide controls sitting among per-device ones -- is answered by LAYOUT
 * here: everything in the rack states how many clients it affects, and nothing
 * box-wide appears among a device's own controls. Hiding half the page was a
 * blunter way of saying the same thing, and it cost every question that spans
 * both halves.
 */

const props = defineProps<{
  active: boolean;
  clients?: Client[];
  /** Passed straight through to the rack's per-adapter charts. The chart
   *  SETTINGS are not passed: those live in the shared prefs store, so the fold
   *  and the client cards cannot be looking at different ranges. */
  series?: Record<string, Series>;
  /** The radios this box watches, for the adapter timeline. From caps rather
   *  than from the bridge's interface list: caps names the radios the DAEMON
   *  watches, which is exactly the set an adapter pattern may address. */
  radios?: string[];
  /** The box's own pattern run, when one is playing. */
  adapterRun?: PatternView | null;
  /** Per-PORT throughput, keyed by interface. Distinct from `series`, which is
   *  keyed by MAC: one is what crossed a wire and the other is what a device
   *  was given, and the first is not the sum of the second. */
  portSeries?: Record<string, Series>;
  /** This tick's raw per-port flows, for the exact figures that are not drawn
   *  over time — the uplink's unattributed egress above all. */
  ports?: PortFlow[];
}>();
const activeRef = computed(() => props.active) as Ref<boolean>;

/*
 * The box-wide total, and which identity its bands carry.
 *
 * BY PORT is the complete answer and the default. A `tc` class only counts
 * what its filter matched, so the per-device grouping is missing everything
 * with no client attribution -- measured on the container host, the WAN's
 * unmatched class held 112 MB against 12 MB for the busiest client. By device
 * answers "who is using this box", which is the question more often asked and
 * the one whose total does not add up.
 *
 * Not persisted. It is a way of looking at the chart in front of you rather
 * than a setting about the box, and the chart RANGE -- which is a setting -- is
 * already in the shared prefs store where both groupings read it.
 */
const GROUPINGS = [
  {
    key: 'adapter' as const,
    label: 'by adapter',
    title: 'Every frame that crossed each adapter, from the kernel\'s own '
      + 'interface counters. Includes the box\'s own traffic, devices it is not '
      + 'tracking, broadcast and multicast — so this is the complete total. The '
      + 'uplink is reported beside it rather than stacked into it.',
  },
  {
    key: 'client' as const,
    label: 'by device',
    title: 'What the box attributed to each device, box-wide rather than per '
      + 'adapter. Incomplete by construction: traffic with no client behind it '
      + 'is absent, so this total is lower than the adapter total.',
  },
];
const grouping = ref<'adapter' | 'client'>('adapter');

/*
 * THE WAN IS NOT A BAND, AND THIS IS NOT TIDINESS.
 *
 * A stack ADDS its bands, and the uplink is not additive with the ports beneath
 * it: a packet from the internet to a Wi-Fi client is counted once crossing
 * wan0 and again crossing the radio. Stacking both gave a download total of
 * 874 Mbit/s for 412 of actual traffic -- a number that is not wrong about any
 * port and is meaningless as a sum.
 *
 * The DOWNSTREAM ports are additive, because a packet reaches exactly one
 * client and therefore crosses exactly one of them. So the stack is downstream
 * demand, which is a real quantity, and the uplink is what that demand has to
 * fit through -- reported beside the chart and as the axis ceiling rather than
 * as another band.
 *
 * The exception, already noted where the counters are read: the bridge
 * replicates every multicast frame to every port, so downstream can legitimately
 * exceed the uplink rather than only because traffic terminated on the box.
 */
/*
 * THE BANDS ARE THE ADAPTERS THE RACK BELOW LISTS, and the label says so.
 *
 * Once the uplink came out of the stack, what remained was exactly
 * RACK_ROLES -- ap, scanner, radio, lan -- under a heading that reads
 * "adapters". Calling the same four things ports up here and adapters down
 * there would be two words for one set, on one screen.
 *
 * The wire type stays PortFlow, and that is not an inconsistency: it carries
 * the WAN, which is a bridge port and not an adapter -- on the container it is
 * a veth with no vendor, product or socket behind it. Port is the bridge role,
 * adapter is the thing you plug in, and this chart bands the latter.
 */
const wanIface = computed(
  () => bridge.info.value?.ifaces.find((i) => i.role === 'wan')?.name ?? '',
);
const downstreamPorts = computed(() => {
  const out: Record<string, Series> = {};
  for (const [iface, s] of Object.entries(props.portSeries ?? {})) {
    if (iface !== wanIface.value) out[iface] = s;
  }
  return out;
});
/** The uplink's latest reading, as the figure the stack is measured against. */
const wanNow = computed(() => {
  const s = props.portSeries?.[wanIface.value];
  if (!s || !s.down.length) return null;
  return { down: s.down[s.down.length - 1], up: s.up[s.up.length - 1] };
});

/**
 * The uplink's unattributed egress, straight from the wire.
 *
 * From the SNAPSHOT rather than from a series, because it is not drawn over
 * time: one exact number beside the chart, where a second band would imply it
 * is additive with the adapters below and it is not.
 */
const wanUnattributed = computed(
  () => props.ports?.find((p) => p.role === 'wan')?.unattributed_up_mbps ?? 0,
);

/** The last value of a series, or 0. */
const last = (a: number[] | undefined) => (a && a.length ? a[a.length - 1] : 0);

/**
 * TRAFFIC THAT DID NOT CROSS THE UPLINK, in aggregate.
 *
 * The adapters deliver more than the uplink brought in exactly when something
 * local produced it: one client talking to another, or the box talking to a
 * client. This box does that routinely and by design -- the bridge forwards
 * between downstream ports, no hostapd config sets `ap_isolate`, and the
 * interface, ntopng, glances and iperf3 all terminate on the bridge.
 *
 * DERIVED, AND APPROXIMATE, which is why it is marked with a tilde and is not
 * a band. Two known error terms, both of which inflate it:
 *
 *   - The bridge replicates every multicast frame to every port, so the
 *     downstream sum counts one arriving frame several times.
 *   - It subtracts a Wi-Fi port's byte counter from an Ethernet one, and the
 *     two do not count framing alike.
 *
 * So a small reading is noise and a large one is real. It is the aggregate
 * only: which two adapters is not knowable from interface counters, which give
 * row and column sums and never the matrix. Pairwise flows -- the Sankey -- need
 * per-flow accounting the box does not have; conntrack in the container has
 * byte accounting OFF, no procfs interface and zero tracked connections.
 *
 * Clamped at zero rather than going negative. Negative would mean the uplink
 * carried more than the adapters delivered, which is inbound traffic that
 * terminated on the box -- a real quantity, but one this subtraction cannot
 * separate from its own error terms.
 */
const localFlow = computed(() => {
  const wan = props.portSeries?.[wanIface.value];
  if (!wan) return null;
  let down = 0;
  let up = 0;
  for (const s of Object.values(downstreamPorts.value)) {
    down += last(s.down);
    up += last(s.up);
  }
  return {
    down: Math.max(0, down - last(wan.down)),
    up: Math.max(0, up - last(wan.up)),
  };
});
/** Below this, the derivation's own error terms dominate. */
const LOCAL_FLOOR_MBPS = 1;
const localWorthShowing = computed(
  () => !!localFlow.value
    && (localFlow.value.down > LOCAL_FLOOR_MBPS || localFlow.value.up > LOCAL_FLOOR_MBPS),
);

const totalSeries = computed(() =>
  grouping.value === 'adapter' ? downstreamPorts.value : props.series ?? {});
// Ports are named by their own key, which IS the interface name, so the
// fallback in the chart is already right and an empty map is honest.
const totalLabels = computed(() => (grouping.value === 'adapter' ? {} : labels.value));
const totalHas = computed(() => Object.keys(totalSeries.value).length > 0);

/*
 * The uplink's link rate, for the readout beside the chart.
 *
 * NOT for the axis. It was the axis floor first, and that failed on the box it
 * was built on: the container's uplink is a veth reporting 10 Gbit/s, so 421
 * Mbit/s of real traffic drew as a 4%-high sliver. The number is what carries
 * the headroom -- "407 down of 10000" says the same thing in a form that
 * cannot make the trace unreadable.
 *
 * From the bridge inventory rather than from the samples, because a port with
 * no traffic still has a speed. Zero when it has no `speed` file, as every
 * radio does: the figure that would answer "how fast is this radio" moves per
 * frame and lives on the client instead.
 */
const wanSpeed = computed(
  () => bridge.info.value?.ifaces.find((i) => i.role === 'wan')?.speed_mbps ?? 0,
);
const bridge = useBridge(activeRef);

const wired = computed(
  () => bridge.info.value?.ifaces.filter((i) => !i.wireless) ?? [],
);

/**
 * Feed the shared adapter store from every bridge poll.
 *
 * The store is what makes an adapter look the same in the rack, on a client
 * row and in a log line, and it is module-level precisely so those three cannot
 * disagree. This is the one place it is written.
 */
watchEffect(() => setAdapterIfaces(bridge.info.value?.ifaces ?? []));

/**
 * WHO is on each adapter, taken from the snapshot the operator is looking at
 * rather than from hostapd's station count.
 *
 * Names rather than a tally: "2 clients" answers a question nobody asked, and
 * the one actually being asked at the top of this page is which devices are on
 * which radio. The count is then obvious from the names, so nothing is lost by
 * dropping it.
 *
 * From the snapshot because the rack and the device list below it must agree,
 * and the list is the thing being read.
 */
/**
 * Names for the adapter charts, covering devices that are NOT currently
 * attached. `onAdapter` above is the live roster and is right for the header
 * row; a chart showing the last five minutes needs to name whatever was on the
 * adapter during them, including something that has since left.
 */
const labels = computed(() => {
  const out: Record<string, string> = {};
  for (const c of props.clients ?? []) out[c.mac] = c.label || c.mac;
  return out;
});

const onAdapter = computed(() => {
  const out: Record<string, { mac: string; label: string }[]> = {};
  for (const c of props.clients ?? []) {
    if (!c.present || !c.port) continue;
    (out[c.port] ??= []).push({ mac: c.mac, label: c.label || c.mac });
  }
  return out;
});

/** Chosen outage length per radio; 10s is long enough to drain a player's
 *  buffer without being long enough for a phone to give up and leave for
 *  another network. */
const outage = ref<Record<string, number>>({});

function addrs(i: IfaceInfo) {
  return [...(i.ipv4 ?? []), ...(i.ipv6 ?? [])];
}

/** Carrier is three-state: sysfs returns EINVAL on a down interface, so
 *  "no carrier" and "could not ask" are different facts. */
function linkText(i: IfaceInfo): string {
  if (!i.up) return 'down';
  if (!i.carrier_known) return 'up';
  return i.carrier ? 'up, carrier' : 'up, NO carrier';
}


/*
 * Not-yet-built controls.
 *
 * Shown only behind ?developer=1, the same gate LadderPanel uses. A button that
 * does nothing is exactly the silent no-op this codebase keeps getting bitten
 * by; a button behind a flag someone deliberately typed is a preview of
 * unfinished work, and reloading without it gives back the plain interface.
 */
const SOON = [
  {
    label: 'Monitor-mode capture on the idle radio',
    what: 'Per-frame retries, actual MCS and per-frame RSSI, without disturbing the AP.',
    why: 'Blocked three ways: tcpdump is not on the image, brcmfmac has no monitor mode at all, and the one radio that does (mt7921u) is now busy serving 5GHz. Needs a free radio and an image change.',
    issue: '#122 Group D',
  },
];
const pending = ref('');



</script>

<template>
  <div class="bridge-view">
    <!-- RESERVED, not inserted.
         Every action here reports its outcome, and a row that appears on press
         pushed the whole page down under the cursor -- so the next click landed
         on whatever slid into place. In this rack that is a real hazard: the
         button two along from `evict` is `switch off`, which takes the radio
         down and drops every client on it. Measured on the bench: an intended
         gather became a switch off, and the radio went dark.
         The same reasoning as `scrollbar-gutter: stable` in style.css, which is
         here because a scrollbar appearing shifted every right-aligned column
         by 15px. Keep the space whether or not there is anything in it. -->
    <div class="msg-slot">
      <div v-if="bridge.error.value" class="notice bad">{{ bridge.error.value }}</div>
      <div v-else-if="bridge.actionMsg.value" class="notice">{{ bridge.actionMsg.value }}</div>
      <div v-else class="notice placeholder" aria-hidden="true">&nbsp;</div>
    </div>

    <!-- Standing facts about what is and is not being conditioned. An
         unwatched radio is an error-level notice: its clients pass traffic
         while appearing nowhere, which is the worst kind of quiet. -->
    <div v-for="n in bridge.info.value?.notes ?? []" :key="n.text"
         class="notice" :class="{ bad: n.level === 'error' }">
      {{ n.text }}
    </div>

    <template v-if="bridge.info.value">
      <FabricStrip :info="bridge.info.value">
        <template #topology>
          <InterfaceDiagram
            :info="bridge.info.value"
            :scans="bridge.scanSummaries.value"
          />

          <table class="counters">
            <thead>
              <tr><th>interface</th><th>role</th><th>MAC</th><th>link</th><th>speed</th><th>addresses</th></tr>
            </thead>
            <tbody>
              <tr v-for="i in wired" :key="i.name">
                <td class="num">{{ i.name }}</td>
                <td>{{ i.role }}</td>
                <td class="num">{{ i.mac }}</td>
                <td>{{ linkText(i) }}</td>
                <td class="num">{{ i.speed_mbps ? `${i.speed_mbps} Mb/s` : '—' }}</td>
                <td class="num addrs">
                  <div v-for="a in addrs(i)" :key="a">{{ a }}</div>
                  <span v-if="!addrs(i).length">—</span>
                </td>
              </tr>
            </tbody>
          </table>
          <p class="meta">
            Two interfaces sharing a MAC is normal: a bridge with no address set
            takes the lowest among its ports, and newer images pin it to the WAN
            port's instead. Speed is a wired-port fact — a bridge reports one, and
            it describes nothing.
          </p>
        </template>
      </FabricStrip>

      <!-- THE WHOLE BOX, ABOVE THE PER-ADAPTER DETAIL.
           Deliberately before the rack rather than inside a fold. It answers a
           question none of the folds can: whether the uplink is the limit. The
           folds beneath are the detail under it, and the reading order matches
           -- the constraint first, then what is using it.

           It is banded by PORT, not by device, and that is not a re-grouping
           of the same numbers. A `tc` class only sees what its filter matched,
           so summing the per-client series to get a port total understates it:
           measured on the container host, the WAN's unmatched default class
           held 112 MB against 12 MB for the busiest client. Nine tenths of
           what crossed that port belonged to no client and appears in no fold.

           The WAN band carrying LESS than the sum of the ports beneath it is
           the normal case and is itself a reading: the difference is traffic
           that never left the box. It can also run the other way, because the
           bridge replicates every multicast frame to every port, so the
           downstream sum is not a conserved quantity. -->
      <div v-if="totalHas" class="total">
        <div class="total-head">
          <h2 class="section-title">traffic</h2>
          <!-- A CHOICE OF GROUPING, not two charts. The bands are the same
               machinery over a different input, so the axis, the clock and the
               window cannot disagree between them. -->
          <!-- ONE WRAPPER THAT ALWAYS RENDERS, holding figures that do not.
               Every figure here is adapter-grouping only, and the right-pushing
               auto margin used to sit on the first of them — so under `by
               device` there was nothing carrying it and the grouping buttons
               slid left to meet the title. Reported from a screenshot: a
               control must not move because of what is displayed beside it.
               The margin now lives on this wrapper, which is present in both
               groupings and merely empty in one. -->
          <div class="total-figs">
          <!-- The uplink, beside the stack rather than in it. See downstreamPorts:
               it is not additive with the ports below, so it cannot be a band,
               and a bottleneck is read by comparing these two numbers against
               the axis the chart is drawn to. -->
          <span v-if="grouping === 'adapter' && wanNow" class="total-wan num"
                :title="`The uplink itself, ${wanIface}. Not stacked with the ports below: `
                  + `a packet crossing the uplink crosses a downstream port too, so adding `
                  + `them would count it twice. The stack is downstream demand; this is what `
                  + `it has to fit through.`">
            uplink {{ wanNow.down.toFixed(0) }} down · {{ wanNow.up.toFixed(0) }} up
            <template v-if="wanSpeed">of {{ wanSpeed }}</template>
          </span>
          <!-- The two things the uplink figure alone does not tell you.

               The first is EXACT: the shaper's default class on the WAN, so
               it is the same counter, point and tick as the per-client
               classes. Egress only -- there are no ingress classes there, and
               an inbound figure would have to be invented.

               The second is DERIVED and says so with a tilde. See localFlow
               for its two error terms, both of which inflate it. -->
          <span v-if="grouping === 'adapter' && wanUnattributed > 0" class="total-aside num"
                :title="`Traffic leaving the uplink that no client filter claimed: this box's `
                  + `own, plus any device it is not tracking. Exact — the shaper's default `
                  + `class, read on the same tick as the per-client ones. Outbound only: `
                  + `downlink is shaped on each client's own port, so the uplink has no `
                  + `inbound classes to split.`">
            box itself {{ wanUnattributed.toFixed(1) }} up
          </span>
          <span v-if="grouping === 'adapter' && localWorthShowing && localFlow" class="total-aside num"
                :title="`Traffic the adapters carried that did not cross the uplink — one client `
                  + `talking to another, or to this box. APPROXIMATE: the bridge replicates every `
                  + `multicast frame to every port, and this subtracts a Wi-Fi byte counter from `
                  + `an Ethernet one, both of which inflate it. Aggregate only — which two `
                  + `adapters is not knowable from interface counters.`">
            local ~{{ localFlow.down.toFixed(0) }} down · ~{{ localFlow.up.toFixed(0) }} up
          </span>
          </div>
          <!-- LAST, and with no auto margin of its own, so the container's own
               right edge fixes it. Nothing to its left can move it. -->
          <span class="seg" role="group" aria-label="group the total by">
            <button
              v-for="g in GROUPINGS" :key="g.key"
              class="ghost" :class="{ on: grouping === g.key }"
              :title="g.title"
              @click="grouping = g.key"
            >{{ g.label }}</button>
          </span>
        </div>
        <AdapterStack
          mode="total"
          :iface="grouping === 'adapter' ? 'these adapters' : 'this box'"
          :series="totalSeries" :labels="totalLabels"
        />
        <!-- SAID, not implied, and only on the grouping it is true of.
             Per-client is the incomplete view by construction: a tc class only
             counts what its filter matched, so everything with no client
             attribution is missing from it -- measured on the container host,
             nine tenths of what crossed the WAN. Per-port is the complete one.
             A reader comparing the two totals and finding them different
             deserves to be told which is which rather than left to guess. -->
        <p v-if="grouping === 'client'" class="total-note">
          Per device counts only traffic the box attributed to a client. It is
          not the adapter total: anything the box itself sent, and any device it
          is not tracking, is missing from here. Group by adapter for everything
          that crossed the wire.
        </p>
      </div>

      <AdapterRack
        :bridge="bridge" :on-adapter="onAdapter"
        :series="series" :labels="labels"
        v-model:outage="outage"
      >
        <!-- The radios on a clock, inside the section that names them. The
             rack's own rule is that a control lives where its subject is named
             exactly once, and the subject of a cross-radio timeline is the set
             of radios rather than any one of them.
             Worth knowing: this is the first many-subject control in this UI to
             sit INSIDE its per-subject list. The others are a strip above the
             list (ClientsView), a peer section (FabricStrip), or hoisted above
             both (EventLog). If it stops earning that place -- most likely by
             becoming part of a scenario surface, where its subject is the RUN
             rather than the radios -- this is the seam to pull. -->
        <template #pattern>
          <AdapterPatternPanel :radios="props.radios ?? []" :run="props.adapterRun ?? null" />
        </template>

        <template #plan="{ radio, others }">
          <ChannelPlan
            :radio="radio" :scans="bridge.scanSummaries.value" :busy="bridge.busy.value"
            :others="others"
            @move="(ch: number, w: number) => bridge.moveChannel(radio.name, ch, w)"
          />
        </template>
      </AdapterRack>


      <!-- Unfinished work, behind ?developer=1. Never in the default view. -->
      <section v-if="DEVELOPER" class="card soon">
        <div class="card-head">
          <span class="name">Not yet built</span>
          <span class="badge warn-badge">developer</span>
        </div>
        <p class="meta">
          These are the remaining router behaviours from issue #122. The buttons
          are here so the shape of the panel is reviewable; none of them is wired
          to anything yet.
        </p>
        <div v-for="s in SOON" :key="s.label" class="soon-row">
          <button @click="pending = s.label">{{ s.label }}</button>
          <div class="soon-text">
            <div>{{ s.what }}</div>
            <div class="meta">{{ s.why }} <em>{{ s.issue }}</em></div>
            <div v-if="pending === s.label" class="notice inline">
              Coming soon — not implemented yet. Tracked in {{ s.issue }}.
            </div>
          </div>
        </div>
      </section>
    </template>

    <div v-else-if="!bridge.error.value" class="empty">
      <p>Reading the bridge…</p>
    </div>
  </div>
</template>

<style scoped>
.radio-card { margin-bottom: 16px; }
.card { padding-bottom: 14px; margin-bottom: 16px; }

.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 2px 18px;
  padding: 12px 14px;
}
.facts > div { display: flex; gap: 10px; font-size: 12px; }
.k { color: var(--ink-dim); min-width: 96px; }
.v { color: var(--ink); }
.addrs div { white-space: nowrap; }

.actions { padding: 2px 14px 8px; border-top: 1px solid var(--line-soft); }
/* The three group headings carry the structure, so they are the loud ones. */
/* Denser than the rest of the page on purpose: this is a control panel, not
   prose. Every explanation not needed AT THE MOMENT OF PRESSING moved into a
   button title. */
.actions h4 {
  margin: 14px 0 2px;
  padding-top: 9px;
  border-top: 1px solid var(--line-soft);
  font-size: 11px; font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.09em;
  color: var(--ink-dim);
}
/* The blast radius, stated where the buttons are rather than in a tooltip. */
.warn-line { color: var(--warn); font-size: 12px; margin: 0 0 6px; }
/* What the group IS, in plain words, under its heading. Quieter than a warning:
   it is orientation, not something to act on. */
.group-note { margin: 0 0 6px; max-width: 84ch; }
.scope-note { margin-bottom: 4px; }
/* Sub-headings inside a group. Deliberately much quieter than the h4 above
   them, so the three groups read as the structure and these as its contents. */
.actions h5 {
  margin: 9px 0 3px;
  font-size: 11px; font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.06em;
  color: var(--ink-faint);
}
.action-row {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap; margin-bottom: 5px;
}
.actions .warn-line, .actions .meta { line-height: 1.35; }
.action-row .k { min-width: auto; }

.survey { border-top: 1px solid var(--line-soft); margin-top: 10px; }
.survey .facts { padding-left: 0; padding-right: 0; }

.badge.warn-badge {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 45%, var(--line));
}

.notice.inline { margin: 8px 0 0; }
/* Holds the row's height open when there is nothing to say. Not visibility:
   hidden on the slot -- a real notice has to be readable -- so an empty one is
   drawn transparent instead, keeping the exact metrics of the real thing. */
.placeholder { visibility: hidden; }
.disabled-note { padding: 0 14px 12px; }

.soon { opacity: 0.9; }
.soon-row {
  display: flex; gap: 12px; align-items: flex-start;
  padding: 8px 14px; border-top: 1px solid var(--line-soft);
}
.soon-row > button { flex: 0 0 auto; min-width: 250px; text-align: left; }
.soon-text { font-size: 12px; color: var(--ink-dim); }
.soon-text em { font-style: normal; color: var(--ink-faint); }

.counters { width: 100%; }

/* The box-wide total. Sits above the rack with the same panel treatment the
   folds have, so it reads as the level above them rather than as another
   adapter. */
.total { margin: 0 0 12px; }
.total-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
}
/* The rack's own heading style, repeated rather than shared -- which is this
   codebase's existing pattern for it, see AdapterRack and ClientsView. This
   section sits directly above the rack, so the two headings must not look like
   different levels of the same page. */
.section-title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin: 0;
  font-size: 13px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.1em;
  color: var(--ink);
}
/* The same segmented shape the outage control uses, so a choice of view looks
   like the other choices on this page rather than like a new idiom. */
.total .seg { display: inline-flex; gap: 0; }
.total .seg button {
  font-size: 10px;
  padding: 1px 7px;
  border-radius: 0;
}
.total .seg button:first-child { border-radius: var(--r) 0 0 var(--r); }
.total .seg button:last-child { border-radius: 0 var(--r) var(--r) 0; }
.total .seg button + button { border-left: none; }
.total .seg button.on {
  color: var(--bg);
  background: var(--down);
  border-color: var(--down);
}
/* Dialled back to a footnote: it is standing context for the view above, not
   something needing attention. Same treatment the page footer gives its own
   notes. */
/* The figures, right-aligned as a group and pushed there by the wrapper rather
   than by whichever of them happens to be rendering. See the template. */
.total-figs {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: auto;
}
.total-wan {
  font-size: 11px;
  color: var(--ink-dim);
}
/* The decomposition beside the uplink. Fainter than the uplink figure itself,
   because each is a part of it rather than a peer. */
.total-aside {
  font-size: 11px;
  color: var(--ink-faint);
}
.total-note {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--ink-faint);
  max-width: 78ch;
}

/* Channel quality lives with the channel plan now, in the diagram: the cells
   it colours and the legend that reads them are both up there, so a copy of
   the palette down here could only ever drift away from it. */
</style>
