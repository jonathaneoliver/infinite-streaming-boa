import { ref, watch, onUnmounted, type Ref } from 'vue';
import type { BridgeInfo, ScanResult, SurveyResult } from '@/types';

/**
 * The box's own interfaces, and the box-wide radio actions.
 *
 * Polled rather than streamed, and only while the Bridge tab is the one being
 * looked at. The snapshot stream exists because per-client telemetry moves
 * every second; an interface inventory moves when somebody plugs a cable in,
 * and each poll costs two hostapd round-trips and a station dump per radio.
 * Putting that on the 1 Hz frame would spend it continuously on behalf of
 * browsers showing the client list.
 */

const POLL_MS = 5000;

export function useBridge(active: Ref<boolean>) {
  const info = ref<BridgeInfo | null>(null);
  const survey = ref<SurveyResult | null>(null);
  const error = ref('');
  /** The last action's outcome, shown inline. Actions here hit every client on
   *  a radio at once, so the result is stated rather than left to be inferred
   *  from the interface changing shape a moment later. */
  const actionMsg = ref('');
  const busy = ref(false);
  let timer = 0;

  async function load() {
    try {
      const r = await fetch('/api/bridge');
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      info.value = (await r.json()) as BridgeInfo;
      error.value = '';
    } catch (e) {
      // Kept rather than blanked: a stale inventory with a visible error beats
      // an empty panel that looks like a box with no interfaces.
      error.value = `could not read the bridge state: ${(e as Error).message}`;
    }
  }

  /**
   * Send one box-wide action.
   *
   * No debounce and no base_revision: these are commands, not edits to a
   * policy someone is looking at, so there is no concurrent edit to lose. The
   * daemon's error text is surfaced verbatim — a channel switch hostapd
   * refused must not read the same as one a client simply followed.
   */
  async function act(path: string, describe: (body: any) => string) {
    busy.value = true;
    actionMsg.value = '';
    error.value = '';
    try {
      const r = await fetch(path, { method: 'POST' });
      const body = await r.json().catch(() => ({}));
      if (!r.ok) {
        error.value = body.error ?? `HTTP ${r.status}`;
        return false;
      }
      actionMsg.value = describe(body);
      await load();
      return true;
    } catch (e) {
      error.value = String(e);
      return false;
    } finally {
      busy.value = false;
    }
  }

  /**
   * Move a radio to a chosen channel by taking it down and bringing it back up
   * there. The working counterpart to chanSwitch, which announces the move and
   * is refused by both drivers on this box.
   */
  const moveChannel = (iface: string, channel: number, width: number) =>
    act(
      `/api/bridge/radios/${encodeURIComponent(iface)}/move-channel` +
        `?channel=${channel}&width=${width}`,
      (b) =>
        `${b.iface}: now on channel ${b.channel} at ${b.width_mhz} MHz` +
        (b.stations_dropped
          ? `, ${b.stations_dropped} client(s) dropped — they were not told, so they have to rediscover it.`
          : '.'),
    );

  const chanSwitch = (iface: string, channel: number, width: number) =>
    act(
      `/api/bridge/radios/${encodeURIComponent(iface)}/channel` +
        `?channel=${channel}&width=${width}`,
      (b) =>
        `${b.iface}: channel switch announced to ${b.channel} at ${b.width_mhz} MHz. ` +
        `Whether a client followed the announcement or dropped and rescanned ` +
        `shows in its connected time.`,
    );

  /**
   * Cut a radio's power, telling its clients nothing.
   *
   * The only SILENT action here. Everything else announces itself, so a client
   * knows and reconnects in a second or two; a client whose AP loses power has
   * to notice the beacons stopped, which takes tens of seconds of believing it
   * is still connected.
   */
  const setPower = (iface: string, on: boolean) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/power?on=${on ? 1 : 0}`, (b) =>
      `${b.iface}: radio ${b.on ? 'powered back on' : 'powered OFF — no client was told'}.`,
    );

  const powerOutage = (iface: string, sec: number) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/power?dur=${sec}`, (b) =>
      `${b.iface}: power cut for ${b.dur_sec}s. Nothing was announced — clients ` +
      `have to time out and rediscover the network, which is slower than a ` +
      `deauthentication and is the point.`,
    );

  const scan = ref<ScanResult | null>(null);

  /**
   * Scan the band, optionally arriving on the quietest channel found.
   *
   * Takes the radio out of service to do it: a beaconing radio cannot survey
   * other channels. On a two-radio box its clients land on the other band and
   * come back.
   */
  async function scanBand(iface: string, apply: boolean) {
    busy.value = true;
    actionMsg.value = '';
    error.value = '';
    try {
      const r = await fetch(
        `/api/bridge/radios/${encodeURIComponent(iface)}/scan?apply=${apply ? 1 : 0}`,
        { method: 'POST' },
      );
      const body = await r.json().catch(() => ({}));
      if (!r.ok) {
        error.value = body.error ?? `HTTP ${r.status}`;
        return false;
      }
      scan.value = body as ScanResult;
      const s = scan.value;
      actionMsg.value = s.applied
        ? `${s.iface}: moved from channel ${s.was_channel} to ${s.now_channel}. ` +
          `Out of service ${s.outage_sec.toFixed(1)}s.`
        : `${s.iface}: scanned ${s.band}, ${s.aps.length} access point(s) seen. ` +
          (s.best_channel
            ? `Quietest is channel ${s.best_channel}. `
            : 'No clear winner. ') +
          `Out of service ${s.outage_sec.toFixed(1)}s.`;
      await load();
      return true;
    } catch (e) {
      error.value = String(e);
      return false;
    } finally {
      busy.value = false;
    }
  }

  /** A named PHY or power-save profile. Drops every client on the radio: these
   *  parameters live in the beacon and are negotiated at association, so an
   *  associated station cannot be told about them. */
  const applyProfile = (iface: string, name: string) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/profile?name=${name}`, (b) =>
      `${b.iface}: profile "${b.profile}" applied` +
      (b.stations_dropped ? `, ${b.stations_dropped} client(s) dropped` : '') +
      (b.warning ? ` — ${b.warning}` : '.'),
    );

  /** RTS or fragmentation threshold. The one radio impairment that costs
   *  nothing: live on the next frame, nobody dropped. */
  const setThreshold = (iface: string, kind: 'rts' | 'frag', value: number | 'off') =>
    act(
      `/api/bridge/radios/${encodeURIComponent(iface)}/threshold` +
        `?kind=${kind}&value=${value}`,
      (b) =>
        b.value < 0
          ? `${b.iface}: ${b.kind} threshold off.`
          : `${b.iface}: ${b.kind} threshold ${b.value}` +
            (b.kind === 'rts' && b.value === 0
              ? ' — RTS/CTS before every frame.'
              : '.'),
    );

  /** 802.11v transition request. A REQUEST: the decision stays with the client,
   *  and whether a given phone honours it is the behaviour worth testing. */
  const steerAll = (iface: string) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/steer`, (b) =>
      `${b.iface}: asked ${b.asked} client(s) to move to ${b.to}. They may ` +
      `refuse — 802.11v is a suggestion. Watch the stations counts to see who went.`,
    );

  /**
   * A per-client link event applied to every station on a radio.
   *
   * Both are ANNOUNCED: the clients are told and reconnect knowing why, which
   * is the whole distinction from switching the radio off.
   */
  const linkAll = (iface: string, kind: 'drop' | 'nudge') =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/link-all?kind=${kind}`, (b) =>
      kind === 'drop'
        ? `${b.iface}: ${b.stations} station(s) deauthenticated. They were told, so they reconnect quickly.`
        : `${b.iface}: ${b.stations} station(s) disassociated — the softer transition. Some clients ride it out without a full reconnect.`,
    );

  const deauthAll = (iface: string) => linkAll(iface, 'drop');

  async function loadSurvey(iface: string) {
    try {
      const r = await fetch(`/api/bridge/radios/${encodeURIComponent(iface)}/survey`);
      const body = await r.json();
      if (!r.ok) throw new Error(body.error ?? `HTTP ${r.status}`);
      survey.value = body as SurveyResult;
      error.value = '';
    } catch (e) {
      error.value = `survey failed: ${(e as Error).message}`;
    }
  }

  function start() {
    void load();
    if (!timer) timer = window.setInterval(load, POLL_MS);
  }
  function stop() {
    if (timer) window.clearInterval(timer);
    timer = 0;
  }

  watch(active, (on) => (on ? start() : stop()), { immediate: true });
  onUnmounted(stop);

  return {
    info, survey, scan, error, actionMsg, busy,
    load, loadSurvey, chanSwitch, deauthAll, setPower, powerOutage, scanBand,
    applyProfile, setThreshold, steerAll, linkAll, moveChannel,
  };
}
