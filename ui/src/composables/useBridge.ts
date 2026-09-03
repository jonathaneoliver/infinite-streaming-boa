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

  const deauthAll = (iface: string) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/deauth-all`, (b) =>
      `${b.iface}: ${b.stations} station(s) deauthenticated. They reconnect on their own.`,
    );

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
  };
}
