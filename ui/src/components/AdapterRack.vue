<script setup lang="ts">
import { computed } from 'vue';
import type { IfaceInfo } from '@/types';
import { rackAdapters, isOpen, toggleAdapter } from '@/composables/useAdapters';
import type { useBridge } from '@/composables/useBridge';
import AdapterToken from './AdapterToken.vue';

/**
 * The adapters, as a rack.
 *
 * One fold per adapter in a FIXED order, because the question this answers is
 * "what is each radio doing" and a list that reorders itself is one you have to
 * re-read every time. The collapsed row carries what you look at constantly --
 * where it is, how many clients, how busy the air is -- and the actions you
 * reach for constantly. Everything else is one click away, in place, so opening
 * a fold never moves the fold you were reading.
 *
 * This rack is now the ONE home for these controls. They used to be split
 * between the bridge diagram's nodes and a panel below it, and before that they
 * existed in both at once; the principle that survived is that a control lives
 * where its subject is named, exactly once. The diagram is a picture of the
 * topology and no longer a control surface.
 */
const props = defineProps<{
  bridge: ReturnType<typeof useBridge>;
  /** Clients per adapter, from the snapshot rather than from hostapd: the
   *  station count and the device list must agree, and the list is what the
   *  operator is actually looking at. */
  counts: Record<string, number>;
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

/** The other radio a client could be steered to. Empty on a one-radio box,
 *  where the control is absent rather than present and dead. */
function otherRadio(r: IfaceInfo): string {
  return rackAdapters.value.find((o) => o.name !== r.name && o.ap?.enabled)?.name ?? '';
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
 * Airtime, where the driver measures it.
 *
 * An em dash where it does not, and that is not a rendering fallback: measured
 * 2026-09-04, `iw dev wlan0 survey dump` on brcmfmac returns nothing at all,
 * so there is no figure to show and inventing a zero would report an idle
 * channel on a radio that has never been asked. See DATA-CONTRACT Source L.
 */
function airtime(r: IfaceInfo): string {
  const pct = props.bridge.airtimePct.value[r.name];
  return pct === undefined ? '—' : `${pct.toFixed(0)}%`;
}

function degraded(i: IfaceInfo): boolean {
  return i.radio?.bus === 'usb' && !!i.radio.link_mbps && i.radio.link_mbps < 5000;
}
</script>

<template>
  <section class="rack">
    <h2 class="rack-title">adapters</h2>

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

        <AdapterToken :name="r.name" />

        <span class="sum">{{ summary(r) }}</span>

        <span class="stat" :title="`${counts[r.name] ?? 0} client(s) on this adapter`">
          {{ counts[r.name] ?? 0 }}<span class="unit">cl</span>
        </span>
        <span class="stat" title="Airtime measured busy on the operating channel">
          {{ airtime(r) }}<span class="unit">air</span>
        </span>

        <span v-if="!r.serving && r.wireless" class="badge warn-badge">not serving</span>
        <span v-else-if="r.power_known && !r.powered" class="badge warn-badge">off</span>

        <span class="spacer" />

        <!-- The actions reached for constantly. Everything here acts on EVERY
             client on this adapter, which is why the count sits beside them. -->
        <template v-if="r.ap">
          <button
            class="ghost" :class="{ accent: r.power_known && !r.powered }"
            :disabled="busy || !r.power_known"
            :title="r.powered
              ? `Switch ${r.name} off. Clients are told NOTHING and must time out.`
              : `Switch ${r.name} back on.`"
            @click="bridge.setPower(r.name, !r.powered)"
          >{{ r.powered ? 'switch off' : 'switch on' }}</button>
          <button
            class="ghost" :disabled="busy || !r.ap.stations"
            :title="`Deauthenticate all ${r.ap.stations} client(s). They are told, so they reconnect quickly.`"
            @click="bridge.linkAll(r.name, 'drop')"
          >drop {{ r.ap.stations }}</button>
          <button
            class="ghost" :disabled="busy || !r.ap.stations"
            title="Disassociate every client — the softer transition."
            @click="bridge.linkAll(r.name, 'nudge')"
          >nudge</button>
          <button
            v-if="otherRadio(r)" class="ghost" :disabled="busy || !r.ap.stations"
            :title="`Ask every client to move to ${otherRadio(r)} (802.11v). They may refuse.`"
            @click="bridge.steerAll(r.name)"
          >steer</button>
          <button
            class="ghost" :disabled="busy"
            title="Survey the band. Costs a few beacon gaps, or an outage on a radio that will not scan while serving."
            @click="bridge.scanBand(r.name, false)"
          >scan</button>
          <div class="seg" role="group" aria-label="radio profile">
            <button
              v-for="p in PROFILES" :key="p.name"
              class="seg-btn" :disabled="busy" :title="`${p.desc} Restarts the AP, dropping all ${r.ap.stations} client(s).`"
              @click="bridge.applyProfile(r.name, p.name)"
            >{{ p.label }}</button>
          </div>
        </template>
      </header>

      <!-- IN PLACE, so opening one fold never moves the one above it. -->
      <div v-if="isOpen(r.name)" class="body">
        <div class="facts">
          <div><span class="k">adapter</span>
            <span class="v">{{ [r.radio?.vendor, r.radio?.product].filter(Boolean).join(' ') || '—' }}</span></div>
          <div><span class="k">driver</span><span class="v num">{{ r.radio?.driver || '—' }}</span></div>
          <div><span class="k">MAC</span><span class="v num">{{ r.mac }}</span></div>
          <div><span class="k">bridge port</span><span class="v num">{{ r.master || 'not bridged' }}</span></div>
          <template v-if="r.ap">
            <div><span class="k">SSID</span><span class="v">{{ r.ap.ssid || '—' }}</span></div>
            <div><span class="k">BSSID</span><span class="v num">{{ r.ap.bssid || '—' }}</span></div>
            <div><span class="k">country</span><span class="v num">{{ r.ap.country || '—' }}</span></div>
            <div><span class="k">beacon / DTIM</span>
              <span class="v num">{{ r.ap.beacon_int_ms }} ms / {{ r.ap.dtim_period }}</span></div>
          </template>
        </div>

        <p v-if="degraded(r)" class="notice bad inline">
          This adapter negotiated USB 2 speed ({{ r.radio?.link_mbps }} Mb/s)<template
            v-if="r.radio?.usb_version"> while declaring USB {{ r.radio.usb_version }}</template>.
          It still reports its full channel width and PHY rate while delivering roughly
          a sixth of the throughput, so nothing else here will look wrong — reseat it
          in a SuperSpeed port.
        </p>

        <template v-if="r.ap">
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

          <h4>Make the link worse</h4>
          <p class="meta group-note">Clients stay associated; the link they are on gets worse.</p>
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
            <span class="meta">live on the next frame; nobody dropped</span>
          </div>

          <h4>Move it</h4>
          <slot name="plan" :radio="r" />
          <div class="action-row">
            <button class="accent" :disabled="busy"
              :title="`Survey ${r.name}'s band and move it to the quietest channel found. Takes the radio down and back up.`"
              @click="bridge.scanBand(r.name, true)"
            >scan and move to the quietest</button>
          </div>
        </template>
      </div>
    </article>
  </section>
</template>

<style scoped>
.rack { margin-bottom: 12px; }
.rack-title {
  margin: 0 0 6px;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--ink-faint);
}
.fold {
  border: 1px solid var(--line-soft);
  border-radius: var(--r);
  background: var(--panel);
  margin-bottom: 6px;
}
.fold.open { border-color: var(--line); }
/* A switched-off adapter is the loud thing in the rack: an access point that is
   deliberately silent must not look like one that is merely quiet. */
.fold.off { border-color: color-mix(in srgb, var(--warn) 45%, var(--line)); }

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  flex-wrap: wrap;
}
.caret {
  background: none;
  border: 0;
  color: var(--ink-faint);
  cursor: pointer;
  font-size: 11px;
  padding: 0;
  width: 10px;
}
.caret:hover { color: var(--ink); }
.sum {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--ink-dim);
}
.stat {
  font-family: var(--mono);
  font-size: 11px;
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}
.unit { color: var(--ink-faint); font-size: 9px; margin-left: 2px; }
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
.action-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin: 4px 0;
}
.facts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 2px 14px;
  font-size: 12px;
}
.facts .k { color: var(--ink-faint); margin-right: 6px; }
.facts .v { color: var(--ink-dim); }
.meta { font-size: 11px; color: var(--ink-faint); }
.warn-line { color: var(--warn); font-size: 11px; margin: 2px 0; }
.group-note { margin: 0 0 4px; }
.notice.inline { margin: 8px 0 0; }
.badge.warn-badge {
  color: var(--warn);
  border-color: color-mix(in srgb, var(--warn) 45%, var(--line));
}
</style>
