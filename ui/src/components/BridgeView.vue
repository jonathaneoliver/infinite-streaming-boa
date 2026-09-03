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

/** Channels the box will accept, mirroring apChannels in radioctl.go. DFS is
 *  excluded because the Pi cannot serve an access point on one. */
const CHANNELS_24 = [1, 6, 11];
const CHANNELS_5 = [36, 40, 44, 48];

const target = ref<Record<string, { channel: number; width: number }>>({});
/** Chosen outage length per radio; 10s is long enough to drain a player's
 *  buffer without being long enough for a phone to give up and leave for
 *  another network. */
const outage = ref<Record<string, number>>({});
function pick(i: IfaceInfo) {
  if (!target.value[i.name]) {
    target.value = {
      ...target.value,
      [i.name]: { channel: i.ap?.channel ?? 36, width: i.ap?.width_mhz ?? 80 },
    };
  }
  return target.value[i.name];
}
/** 2.4GHz is 20MHz only: 80 does not exist there and 40 is antisocial. The
 *  daemon refuses the combination too; this keeps it from being offered. */
const widthsFor = (ch: number) => (ch < 15 ? [20] : [20, 40, 80]);

function setChannel(name: string, ch: number) {
  const cur = target.value[name];
  const widths = widthsFor(ch);
  target.value = {
    ...target.value,
    [name]: { channel: ch, width: widths.includes(cur.width) ? cur.width : widths[0] },
  };
}

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
  kind: 'power-off' | 'power-on' | 'scan' | 'drop' | 'nudge' | 'steer',
  iface: string,
) {
  if (kind === 'drop' || kind === 'nudge') return void bridge.linkAll(iface, kind);
  if (kind === 'steer') return void bridge.steerAll(iface);
  // A latch, matching the panel below: the diagram switch turns the radio off
  // and leaves it off. A button on a picture of a radio should behave like the
  // switch it is drawn as, not fire a timed pulse.
  if (kind === 'power-off') bridge.setPower(iface, false);
  else if (kind === 'power-on') bridge.setPower(iface, true);
  else bridge.scanBand(iface, false);
}

/** The radio a client could be steered to: another one that is serving. Empty
 *  on a single-radio box, where the control is absent rather than dead. */
function otherRadio(r: IfaceInfo): string {
  return radios.value.find((o) => o.name !== r.name && o.ap?.enabled)?.name ?? '';
}
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
      <InterfaceDiagram :info="bridge.info.value" @action="onDiagramAction" />

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
          <p class="meta group-note">
            The association itself. Nothing is degraded — it exists or it does not.
          </p>

          <!-- The announced events, in the same words the device cards use.
               Whatever can be done to one client can be done to every client on
               a radio; only the blast radius differs. -->
          <div class="action-row">
            <label class="k">all {{ r.ap.stations }} clients</label>
            <button
              :disabled="bridge.busy.value || !r.ap.stations"
              title="Deauthenticate every client. The link goes down and they reconnect — they are TOLD, so it is quick."
              @click="bridge.linkAll(r.name, 'drop')"
            >drop all</button>
            <button
              :disabled="bridge.busy.value || !r.ap.stations"
              title="Disassociate every client — a softer 802.11 transition some clients ride out without a full reconnect."
              @click="bridge.linkAll(r.name, 'nudge')"
            >nudge all</button>
            <button
              v-if="otherRadio(r)"
              :disabled="bridge.busy.value || !r.ap.stations"
              :title="`Ask every client to move to ${otherRadio(r)} (802.11v). They may refuse.`"
              @click="bridge.steerAll(r.name)"
            >steer all to {{ otherRadio(r) }}</button>
          </div>

          <!-- Still connection control, but the SILENT member of it: every
               action above announces itself, this one does not. That difference
               is the whole reason it exists, so it keeps its own paragraph. -->
          <p class="warn-line">
            <strong>Silent.</strong> Clients are told nothing and must time out —
            a tripped breaker, not a disconnection.
          </p>
          <!-- A latching toggle, not a timed pulse. The radio stays off until
               it is switched back on, the way a power switch behaves -- which
               is what makes it usable for "leave it down and watch what the
               player does for the next ten minutes". -->
          <div class="action-row">
            <button
              class="power-toggle"
              :class="{ accent: r.power_known && !r.powered, off: r.power_known && !r.powered }"
              :disabled="bridge.busy.value || !r.power_known"
              :title="r.powered
                ? `Switch ${r.name} off. It stays off until switched back on. No client is told.`
                : `Switch ${r.name} back on.`"
              @click="bridge.setPower(r.name, !r.powered)"
            >{{ r.powered ? `switch ${r.name} off` : `switch ${r.name} on` }}</button>

            <span v-if="!r.power_known" class="meta">
              power state unreadable on this radio
            </span>
            <span v-else-if="!r.powered" class="meta warn-line">
              <strong>OFF</strong> — silent, and staying off until you switch it back
            </span>
            <span v-else class="meta">on</span>
          </div>

          <!-- The timed form is a convenience on top of the toggle, for the
               common case of a fixed-length outage you do not want to have to
               remember to end. -->
          <div class="action-row">
            <label class="k">or cut it for</label>
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
          </div>

          <!-- Measured, not theorised: a deauthenticated iPhone came back under
               a new MAC within nine seconds. Policy is keyed by MAC, so the
               device returns as a stranger with no conditioning. Issue #45. -->
          <p class="meta">
            A client with a randomised MAC may return as a <strong>new
            device</strong>, leaving its policy behind on the old address (#45).
          </p>

          <h5>Move to another channel</h5>
          <p class="warn-line">
            Down and back up on the new channel. All {{ r.ap.stations }} client(s)
            dropped, and not told.
          </p>
          <div class="action-row">
            <div class="seg" role="group" aria-label="channel">
              <button
                v-for="c in (r.ap.channel && r.ap.channel < 15 ? CHANNELS_24 : CHANNELS_5)" :key="c"
                class="seg-btn" :class="{ on: pick(r).channel === c }"
                @click="setChannel(r.name, c)"
              >{{ c }}</button>
            </div>
            <div class="seg" role="group" aria-label="width">
              <button
                v-for="wd in widthsFor(pick(r).channel)" :key="wd"
                class="seg-btn" :class="{ on: pick(r).width === wd }"
                @click="target = { ...target, [r.name]: { ...pick(r), width: wd } }"
              >{{ wd }}&#8239;MHz</button>
            </div>
            <button
              class="accent"
              :disabled="bridge.busy.value || pick(r).channel === r.ap.channel"
              @click="bridge.moveChannel(r.name, pick(r).channel, pick(r).width)"
            >move to {{ pick(r).channel }}</button>
          </div>

          <!-- The other way to change channel, and why it is not a button.
               802.11h CSA counts the switch down in the beacons and clients
               follow WITHOUT dropping -- which is strictly better, and which
               both chips on this box refuse (measured on brcmfmac and mt7921u,
               #154). A control that can only ever report a refusal is worse
               than a sentence saying so, so this is the sentence. POST
               .../channel still exists and still works the day a driver gains
               support. -->
          <p class="meta">
            802.11h would move them without dropping anyone, but
            <strong>both radios here refuse it</strong> (#154).
          </p>

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

          <div class="action-row">
            <button :disabled="bridge.busy.value" @click="bridge.scanBand(r.name, false)">
              scan {{ r.ap.channel && r.ap.channel < 15 ? '2.4GHz' : '5GHz' }}
            </button>
            <button
              class="accent" :disabled="bridge.busy.value"
              @click="bridge.scanBand(r.name, true)"
            >scan and move to the quietest</button>
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

/* The power switch reads as a switch: when the radio is off it is the loud
   thing on the card, because an access point that is deliberately silent must
   not be mistaken for one that is merely quiet. */
.power-toggle { min-width: 170px; }
.power-toggle.off { border-color: var(--warn); color: var(--bg); background: var(--warn); }

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
</style>
