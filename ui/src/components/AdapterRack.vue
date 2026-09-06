<script setup lang="ts">
import { computed } from 'vue';
import type { IfaceInfo, Series } from '@/types';
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
  if (r.power_known && !r.powered) return 'off';
  if (!r.ap) return 'no AP';
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
 * Where an eviction sends them: the emptiest access point that is actually up.
 *
 * CHOSEN AND NAMED, rather than left to the daemon. With two radios "away from
 * here" meant "to the other one" and needed no decision. With three it does,
 * and the daemon's own comment says why leaving it there is wrong: OtherRadio
 * "would pick the first serving radio in preference order -- deterministic, but
 * not something the operator chose, and invisible once it has happened".
 *
 * Emptiest rather than first, so repeated evictions spread clients instead of
 * piling them onto whichever radio happens to sort first. The tooltip says
 * which, so the choice is visible before it is made rather than inferred from
 * the log afterwards.
 */
function evictTo(r: IfaceInfo): IfaceInfo | undefined {
  return otherRadios(r)
    .filter((o) => o.ap?.enabled)
    .sort((a, b) => (a.ap?.stations ?? 0) - (b.ap?.stations ?? 0))[0];
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
          <!-- TELL, in whichever direction the radio is going.

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
               halves of one control disagree about what "tell" means, which is
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
            @click="bridge.setAPEnabled(r.name, !apLive(r), apLive(r) ? 'drop' : 'announce')"
          >{{ apLive(r) ? 'tell &amp; disable' : 'tell &amp; enable' }}</button>
          <button
            class="ghost" :disabled="busy || !apLive(r) || !r.ap?.stations"
            :title="apLive(r)
              ? `Deauthenticate all ${r.ap?.stations ?? 0} client(s). They are told, so they reconnect quickly.`
              : `${r.name} has no access point up, so there is nothing to deauthenticate from.`"
            @click="bridge.linkAll(r.name, 'drop')"
          >drop</button>
          <button
            class="ghost" :disabled="busy || !apLive(r) || !r.ap?.stations"
            :title="apLive(r)
              ? 'Disassociate every client — the softer transition.'
              : `${r.name} has no access point up, so there is nobody to disassociate.`"
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
                ? 'No other access point is up to move them to.'
                : `Ask all ${r.ap?.stations ?? 0} client(s) on ${r.name} to move to `
                  + `${evictTo(r)!.name}, the emptiest one that is up (802.11v). `
                  + `They may refuse.`"
            @click="evictTo(r) && bridge.gather(evictTo(r)!.name, r.name)"
          >evict</button>
          <button
            class="ghost"
            :disabled="busy || !apLive(r) || !gatherable(r)"
            :title="!apLive(r)
              ? `${r.name} has no access point up, so there is nowhere here to gather them to.`
              : !otherRadios(r).length
                ? 'No other radio on this box to gather from.'
                : `Ask all ${gatherable(r)} client(s) on `
                  + `${otherRadios(r).map((o) => o.name).join(' and ')} to move here `
                  + `to ${r.name} (802.11v). They may refuse.`"
            @click="bridge.gatherAll(
              r.name,
              otherRadios(r).filter((o) => o.ap?.stations).map((o) => o.name),
            )"
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

          <!-- The timed outage goes behind developer=1 with the power switch
               it belongs to: same mechanism, same cost, same tendency to wedge
               the USB adapter. -->
          <template v-if="DEVELOPER">
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
          </template>

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
