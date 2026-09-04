<script setup lang="ts">
import { computed } from 'vue';
import type { IfaceInfo, Series } from '@/types';
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
 * The other radio clients can be moved between, as the whole interface rather
 * than just its name -- gathering needs to know how many stations are on it,
 * not only that it exists.
 *
 * Undefined on a one-radio box, where BOTH controls are absent rather than
 * present and dead: a transition request has to name another access point, so
 * with nowhere to send anyone there is nothing to offer.
 */
function otherRadio(r: IfaceInfo): IfaceInfo | undefined {
  return rackAdapters.value.find((o) => o.name !== r.name && o.ap?.enabled);
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

        <span class="stat" title="Airtime measured busy on the operating channel">
          {{ airtime(r) }}<span class="unit">air</span>
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
          <span v-if="!r.serving && r.wireless" class="badge warn-badge">not serving</span>
          <span v-else-if="r.power_known && !r.powered" class="badge warn-badge">off</span>

        <!-- The actions reached for constantly. Every one of these acts on
             EVERY client on this adapter -- which is why the devices are named
             at the left of the row rather than counted on a button. "drop 2"
             read as part of the label, as though there were some other drop. -->
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
          >drop</button>
          <button
            class="ghost" :disabled="busy || !r.ap.stations"
            title="Disassociate every client — the softer transition."
            @click="bridge.linkAll(r.name, 'nudge')"
          >nudge</button>
          <!-- EVICT and GATHER: the same 802.11v request in both directions.
               One button called "steer" only ever emptied a radio, which is
               half the question. "Move everyone to the other band" and "bring
               everyone here" are asked about equally often on a two-band box,
               and the second one had no button at all -- you had to go to the
               OTHER adapter and press steer there, which is the same action
               named after the wrong radio.
               Each is disabled when its source radio has nobody on it, so a
               dead button always means "there is no one to move", never "this
               is not supported". -->
          <button
            v-if="otherRadio(r)" class="ghost" :disabled="busy || !r.ap.stations"
            :title="`Ask all ${r.ap.stations} client(s) on ${r.name} to move to `
              + `${otherRadio(r)!.name} (802.11v). They may refuse.`"
            @click="bridge.evict(r.name)"
          >evict</button>
          <button
            v-if="otherRadio(r)" class="ghost"
            :disabled="busy || !otherRadio(r)!.ap?.stations"
            :title="`Ask all ${otherRadio(r)!.ap?.stations ?? 0} client(s) on `
              + `${otherRadio(r)!.name} to move here to ${r.name} (802.11v). `
              + `They may refuse.`"
            @click="bridge.gather(r.name, otherRadio(r)!.name)"
          >gather</button>
          <button
            class="ghost" :disabled="busy"
            title="Survey the band. Costs a few beacon gaps, or an outage on a radio that will not scan while serving."
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

        <!-- WHAT IT IS CARRYING, with the facts rather than with the
             controls: this is status, and MOVE IT still leads the controls
             below it. It answers the question that sits between the row above
             and the client cards below -- how the adapter's capacity is being
             divided right now. A stream that halved because the radio halved
             looks identical, on its own card, to one that halved by itself.
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

        <p v-if="degraded(r)" class="notice bad inline">
          This adapter negotiated USB 2 speed ({{ r.radio?.link_mbps }} Mb/s)<template
            v-if="r.radio?.usb_version"> while declaring USB {{ r.radio.usb_version }}</template>.
          It still reports its full channel width and PHY rate while delivering roughly
          a sixth of the throughput, so nothing else here will look wrong — reseat it
          in a SuperSpeed port.
        </p>

        <template v-if="r.ap">
          <!-- MOVE IT leads the CONTROLS, straight after the status above it.
               Opening an adapter is nearly always to change where it is, and a
               band plan is the one control here that has to be READ rather than
               just pressed -- it is a picture of the spectrum and of where this
               radio sits in it. What comes before it is reference; what comes
               after is buttons you already know you want. -->
          <h4 class="first">Move it</h4>
          <slot name="plan" :radio="r" />
          <div class="action-row">
            <button class="accent" :disabled="busy"
              :title="`Survey ${r.name}'s band and move it to the quietest channel found. Takes the radio down and back up.`"
              @click="bridge.scanBand(r.name, true)"
            >scan and move to the quietest</button>
          </div>

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

        </template>
      </div>
    </article>
  </section>
</template>

<style scoped>
.rack { margin-bottom: 12px; }
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
    minmax(0, 200px)     /* adapter token: name and channel */
    minmax(0, 168px)     /* width and mode, or the wired link speed */
    64px                 /* airtime */
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
.stat {
  font-family: var(--mono);
  font-size: 12px;
  font-weight: 600;
  color: var(--ink);
  font-variant-numeric: tabular-nums;
}
.unit { color: var(--ink-faint); font-size: 9px; margin-left: 2px; }
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
