<script setup lang="ts">
import { computed, ref, watchEffect, type Ref } from 'vue';
import { DEVELOPER } from '@/types';
import type { Client, IfaceInfo } from '@/types';
import { useBridge } from '@/composables/useBridge';
import InterfaceDiagram from '@/components/InterfaceDiagram.vue';
import FabricStrip from '@/components/FabricStrip.vue';
import AdapterRack from '@/components/AdapterRack.vue';
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

const props = defineProps<{ active: boolean; clients?: Client[] }>();
const activeRef = computed(() => props.active) as Ref<boolean>;
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
    <div v-if="bridge.error.value" class="notice bad">{{ bridge.error.value }}</div>
    <div v-if="bridge.actionMsg.value" class="notice">{{ bridge.actionMsg.value }}</div>

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

      <AdapterRack :bridge="bridge" :on-adapter="onAdapter" v-model:outage="outage">
        <template #plan="{ radio }">
          <ChannelPlan
            :radio="radio" :scans="bridge.scanSummaries.value" :busy="bridge.busy.value"
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

/* Channel quality lives with the channel plan now, in the diagram: the cells
   it colours and the legend that reads them are both up there, so a copy of
   the palette down here could only ever drift away from it. */
</style>
