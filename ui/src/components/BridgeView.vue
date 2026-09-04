<script setup lang="ts">
import { computed, ref, type Ref } from 'vue';
import { DEVELOPER } from '@/types';
import type { IfaceInfo } from '@/types';
import { useBridge } from '@/composables/useBridge';
import InterfaceDiagram from '@/components/InterfaceDiagram.vue';

/*
 * The box itself: every interface it has, what each radio is doing, and the
 * controls that act on a whole radio at once.
 *
 * Separate from the Clients tab on purpose. Everything on a device card
 * conditions ONE device; everything here hits every client on a radio
 * simultaneously -- that is issue #122's "collision", and the resolution it
 * asks for is exactly this: a per-client link event over there, a box-wide
 * radio control over here, saying on screen that it affects everyone.
 */

const props = defineProps<{ active: boolean }>();
const activeRef = computed(() => props.active) as Ref<boolean>;
const bridge = useBridge(activeRef);

const radios = computed(
  () => bridge.info.value?.ifaces.filter((i) => i.wireless) ?? [],
);
const wired = computed(
  () => bridge.info.value?.ifaces.filter((i) => !i.wireless) ?? [],
);

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

const busName = (i: IfaceInfo) => {
  const r = i.radio;
  if (!r) return '';
  if (r.bus !== 'usb') return 'onboard';
  return !r.link_mbps ? 'USB' : r.link_mbps >= 5000 ? 'USB 3' : 'USB 2';
};
// The failure that is invisible from every other angle: a USB 3 adapter that
// enumerated at High-Speed looks identical -- same channel, same 802.11ax, same
// PHY rate -- while delivering about a sixth of the throughput.
const degraded = (i: IfaceInfo) =>
  i.radio?.bus === 'usb' && !!i.radio.link_mbps && i.radio.link_mbps < 5000;

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

/** Mirrors radioProfiles in radioprofile.go. "clean" first, because it is the
 *  way back from every other one. */
const PROFILES = [
  { name: 'clean', label: 'clean', desc: 'everything back to how the image configured it.' },
  { name: 'legacy', label: '802.11n only', desc: 'no ac, no ax \u2014 the ceiling an older device sees, with real MAC-layer cost rather than a rate limit.' },
  { name: 'narrow', label: '20 MHz', desc: 'a quarter of the spectrum, so airtime contention is real and shared rather than imposed per client.' },
  { name: 'dozy', label: 'power-save torture', desc: 'DTIM 10 at a 300 ms beacon, U-APSD off \u2014 a dozing phone waits up to three seconds for buffered downlink, drawing a comb of spikes no netem delay can.' },
];

/** Quick actions fired from the diagram. The picture is the natural place to
 *  press them -- the radio being cut is the box you are pointing at -- so they
 *  go through the same composable the panel below uses rather than a second
 *  path that could drift out of step. */
function onDiagramAction(
  kind: 'power-off' | 'power-on' | 'scan' | 'drop' | 'nudge' | 'steer' | 'channel',
  iface: string,
  arg?: { channel: number; width: number },
) {
  if (kind === 'drop' || kind === 'nudge') return void bridge.linkAll(iface, kind);
  if (kind === 'steer') return void bridge.steerAll(iface);
  if (kind === 'channel' && arg) {
    // Channel and width together, from the cell that was pressed. They are one
    // choice: picking a cell on the 80MHz row IS picking 80MHz, and taking the
    // width from anywhere else would answer a question nobody asked.
    return void bridge.moveChannel(iface, arg.channel, arg.width);
  }
  // A latch, matching the panel below: the diagram switch turns the radio off
  // and leaves it off. A button on a picture of a radio should behave like the
  // switch it is drawn as, not fire a timed pulse.
  if (kind === 'power-off') bridge.setPower(iface, false);
  else if (kind === 'power-on') bridge.setPower(iface, true);
  else bridge.scanBand(iface, false);
}

/* The steer target is worked out in InterfaceDiagram now, which is where the
 * only steer button lives. Two copies of the same "another radio that is
 * serving" rule was one copy too many. */
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
      <InterfaceDiagram
        :info="bridge.info.value" :busy="bridge.busy.value"
        :scans="bridge.scanSummaries.value"
        @action="onDiagramAction"
      />

      <section v-for="r in radios" :key="r.name" class="card radio-card">
        <div class="card-head">
          <span class="name">{{ r.name }}</span>
          <!-- Three states, not two. A radio that is simply switched off is not
               the same as one carrying clients nobody is conditioning, and
               labelling the quiet case with the alarming one is how a warning
               stops being read. -->
          <span v-if="r.serving" class="badge wifi">serving the AP</span>
          <span v-else-if="!r.up" class="badge">off</span>
          <span v-else class="badge warn-badge">not conditioned</span>
          <span v-if="busName(r)" class="badge" :class="{ 'warn-badge': degraded(r) }">
            {{ busName(r) }}
          </span>
          <span class="spacer"></span>
          <span class="meta num">{{ r.mac }}</span>
        </div>

        <div class="facts">
          <div><span class="k">adapter</span>
            <span class="v">{{ [r.radio?.vendor, r.radio?.product].filter(Boolean).join(' ') || '—' }}</span></div>
          <div><span class="k">driver</span><span class="v num">{{ r.radio?.driver || '—' }}</span></div>
          <div><span class="k">link</span><span class="v">{{ linkText(r) }}</span></div>
          <div><span class="k">bridge port</span><span class="v num">{{ r.master || 'not bridged' }}</span></div>
          <template v-if="r.ap">
            <div><span class="k">SSID</span><span class="v">{{ r.ap.ssid || '—' }}</span></div>
            <div><span class="k">BSSID</span><span class="v num">{{ r.ap.bssid || '—' }}</span></div>
            <div><span class="k">channel</span>
              <span class="v num">{{ r.ap.channel }}<span v-if="r.ap.freq_mhz"> ({{ r.ap.freq_mhz }} MHz)</span></span></div>
            <div><span class="k">width</span><span class="v num">{{ r.ap.width_mhz ? `${r.ap.width_mhz} MHz` : '—' }}</span></div>
            <div><span class="k">mode</span><span class="v num">{{ r.ap.mode || '—' }}</span></div>
            <div><span class="k">country</span><span class="v num">{{ r.ap.country || '—' }}</span></div>
            <div><span class="k">stations</span><span class="v num">{{ r.ap.stations }}</span></div>
            <div><span class="k">beacon / DTIM</span>
              <span class="v num">{{ r.ap.beacon_int_ms }} ms / {{ r.ap.dtim_period }}</span></div>
          </template>
        </div>

        <!-- Careful not to assert this radio's mode or width here: the facts
             table above says what they actually are, and a warning that
             contradicts the row above it teaches people to ignore warnings. -->
        <p v-if="degraded(r)" class="notice bad inline">
          This adapter negotiated USB 2 speed ({{ r.radio?.link_mbps }} Mb/s)
          <template v-if="r.radio?.usb_version">
            while declaring USB {{ r.radio.usb_version }}</template>.
          An adapter that enumerates at High-Speed still reports its full channel
          width and PHY rate while delivering roughly a sixth of the throughput,
          so nothing else here will look wrong — reseat it in a SuperSpeed port.
        </p>

        <!-- Box-wide controls. The blast radius is stated on the control, not
             in a tooltip: every one of these acts on the whole radio. -->
        <div v-if="r.ap" class="actions">
          <p class="warn-line scope-note">
            Everything below acts on <strong>every client on {{ r.name }}</strong>
            at once — {{ r.ap.stations }} associated right now.
          </p>

          <!-- THREE groups, and the line between the first two is the one that
               matters: does the client stay associated?
               
               Conditioning makes the link worse while the client stays on it.
               Connection control changes whether, or where, it is attached at
               all. They were interleaved before, which made a power cut look
               like a sibling of an RTS threshold when they answer completely
               different questions. -->
          <h4>Who is connected</h4>
          <!-- Drop, nudge, steer and the latching switch are on the radio in
               the picture above and ONLY there. Every one of them acts on a
               radio as a whole, which is exactly what a node in that picture
               is, so a second copy down here was two controls for one action --
               and the copy that is further from the thing it names is the one
               to lose. What stays is what a node cannot draw. -->
          <p class="meta group-note">
            The association itself. Nothing is degraded — it exists or it does
            not. Drop, nudge, steer and the switch are on the radio in the
            picture above.
          </p>

          <!-- The SILENT member of connection control: the announced actions in
               the picture all tell the client what happened, and this one does
               not. That difference is the whole reason it exists, so it keeps
               its own paragraph. -->
          <p class="warn-line">
            <strong>Silent.</strong> Clients are told nothing and must time out —
            a tripped breaker, not a disconnection.
          </p>
          <!-- The timed form, which the switch in the picture cannot express: a
               fixed-length outage that ends itself. The latch up there is for
               "leave it down and watch the player"; this is for "take it away
               for ten seconds and see what happens", where remembering to end
               it is part of what you are trying not to think about. -->
          <div class="action-row">
            <label class="k">cut it for</label>
            <div class="seg" role="group" aria-label="outage length">
              <button
                v-for="s in [5, 10, 30, 60]" :key="s"
                class="seg-btn" :class="{ on: outage[r.name] === s }"
                @click="outage = { ...outage, [r.name]: s }"
              >{{ s }}s</button>
            </div>
            <button
              :disabled="bridge.busy.value || !r.powered"
              @click="bridge.powerOutage(r.name, outage[r.name] ?? 10)"
            >cut and restore automatically</button>

            <!-- Why the button is dead, said next to the button. A radio that
                 is already off cannot be cut, and the switch that ends that
                 state is now in the picture above -- so the disabled control
                 has to say where to go, or it is just a control that does
                 nothing. -->
            <span v-if="!r.power_known" class="meta">
              power state unreadable on this radio
            </span>
            <span v-else-if="!r.powered" class="meta warn-line">
              <strong>OFF</strong> — switch it back on in the picture above
            </span>
          </div>

          <!-- Measured, not theorised: a deauthenticated iPhone came back under
               a new MAC within nine seconds. Policy is keyed by MAC, so the
               device returns as a stranger with no conditioning. Issue #45. -->
          <p class="meta">
            A client with a randomised MAC may return as a <strong>new
            device</strong>, leaving its policy behind on the old address (#45).
          </p>

          <!-- The channel plan under this radio in the picture above is the
               whole of "move to another channel": it picks a channel and a
               width together, which is the choice that actually exists, and it
               does it on the radio being moved. The duplicate segmented control
               that stood here offered the same two lists a second time.

               The two findings it carried moved with it: the 802.11h refusal
               (#154) and hostapd's adjacent-channel swap are on the diagram's
               caption and its per-cell tooltips, because that is now where the
               channel is chosen. -->

          <h4>Conditioning the link</h4>
          <p class="meta group-note">Clients stay associated; the link they are on gets worse.</p>
          <h5>Radio profile</h5>
          <p class="warn-line">
            Restarts the access point, dropping all {{ r.ap.stations }} client(s).
          </p>
          <div class="action-row">
            <button
              v-for="p in PROFILES" :key="p.name"
              :class="{ accent: p.name === 'clean' }"
              :disabled="bridge.busy.value"
              :title="p.desc"
              @click="bridge.applyProfile(r.name, p.name)"
            >{{ p.label }}</button>
          </div>


          <h5>Thresholds</h5>
          <p class="meta">Live on the next frame. Nobody dropped.</p>
          <div class="action-row">
            <label class="k">RTS/CTS</label>
            <button
              :disabled="bridge.busy.value"
              title="RTS/CTS before every frame — roughly halves throughput and adds two control frames of latency per data frame. What a real AP does in a dense environment."
              @click="bridge.setThreshold(r.name, 'rts', 0)"
            >every frame</button>
            <button :disabled="bridge.busy.value" @click="bridge.setThreshold(r.name, 'rts', 'off')">off</button>
            <label class="k">fragment</label>
            <button
              :disabled="bridge.busy.value"
              title="Fragment every frame at 256 bytes. With any error rate the retry cost explodes superlinearly, because losing one fragment costs the whole frame."
              @click="bridge.setThreshold(r.name, 'frag', 256)"
            >at 256</button>
            <button :disabled="bridge.busy.value" @click="bridge.setThreshold(r.name, 'frag', 'off')">off</button>
          </div>

          <h4>Measurement</h4>
          <p class="meta group-note">Changes nothing. A scan costs a few beacon gaps, or an outage on a radio that will not scan while serving.</p>
          <!-- Scan. Disruptive by construction and says so, with the cost
               reported afterwards as the measured out-of-service time. -->
          <h5>Channel scan</h5>

          <!-- Measure-only scanning is the `scan` button on this radio in the
               picture, where its result lands: the colours it produces are on
               the channel plan up there, not here. What is left is the variant
               that ACTS on the answer, which is a different thing to press and
               belongs with the other controls that change something. -->
          <div class="action-row">
            <button
              class="accent" :disabled="bridge.busy.value"
              :title="`Scan ${r.name}'s band and move it to the quietest channel found. Takes the radio down and back up, so all ${r.ap.stations} client(s) are dropped and not told.`"
              @click="bridge.scanBand(r.name, true)"
            >scan and move to the quietest</button>
            <span class="meta">— to only look, press <em>scan</em> on the radio above</span>
          </div>


          <div v-if="bridge.scan.value?.iface === r.name" class="survey">
            <table class="counters">
              <thead>
                <tr><th>channel</th><th>neighbours</th><th>strongest</th><th></th></tr>
              </thead>
              <tbody>
                <tr v-for="c in bridge.scan.value.channels" :key="c.channel">
                  <td class="num">{{ c.channel }}</td>
                  <td class="num">{{ c.aps }}</td>
                  <td class="num">{{ c.strongest_dbm ? `${c.strongest_dbm} dBm` : '—' }}</td>
                  <td>
                    <span v-if="c.recommended" class="badge" style="color: var(--ok)">
                      quietest
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
            <p class="meta">{{ bridge.scan.value.note }}</p>
            <p class="meta">Only non-overlapping channels are recommended.</p>
          </div>
          <div v-if="bridge.survey.value?.iface === r.name" class="survey">
            <template v-for="c in bridge.survey.value.channels" :key="c.reported_freq_mhz">
              <div class="facts">
                <div><span class="k">channel busy</span>
                  <span class="v num">
                    {{ c.busy_pct !== undefined
                      ? `${c.busy_pct.toFixed(1)}% over the last ${(c.delta_active_ms! / 1000).toFixed(0)}s`
                      : `${((c.busy_ms / c.active_ms) * 100).toFixed(1)}% since the AP started` }}
                  </span></div>
                <div><span class="k">transmit</span>
                  <span class="v num">{{ ((c.transmit_ms / c.active_ms) * 100).toFixed(1) }}%</span></div>
                <div><span class="k">receive</span>
                  <span class="v num">{{ ((c.receive_ms / c.active_ms) * 100).toFixed(1) }}%</span></div>
              </div>
            </template>
            <p class="meta">{{ bridge.survey.value.note }}</p>
            <p class="meta">
              Transmit and receive overlap busy rather than dividing it.
            </p>
          </div>
        </div>
          <!-- Steering only exists when there is somewhere to steer TO. On a
               one-radio box the button would be permanently dead, so it is
               absent rather than disabled. -->

        <p v-else class="meta disabled-note">
          <template v-if="!r.up">
            {{ r.name }} is switched off — on this image the onboard radio is
            rfkilled whenever a USB adapter is serving, so exactly one runs.
            Nothing to drive here.
          </template>
          <template v-else>
            No hostapd control socket on {{ r.name }}, so there is nothing to
            drive here. The radio is idle, or is being run by something other
            than hostapd.
          </template>
        </p>
      </section>

      <section class="card">
        <div class="card-head"><span class="name">Wired and bridge</span></div>
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
      </section>

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
