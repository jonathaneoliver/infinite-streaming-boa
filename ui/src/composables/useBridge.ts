import { computed, ref, watch, onUnmounted, type Ref } from 'vue';
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
   * there. The ONLY way to change channel here: 802.11h CSA would move clients
   * without dropping them and is refused by both radios on this box (#154), so
   * POST /channel has no caller in the interface and is reachable only by hand.
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

  /**
   * Cut a radio's power, telling its clients nothing.
   *
   * The only SILENT action here. Everything else announces itself, so a client
   * knows and reconnects in a second or two; a client whose AP loses power has
   * to notice the beacons stopped, which takes tens of seconds of believing it
   * is still connected.
   */
  /**
   * Switch a radio's transmitter on or off, and say what that does and does not
   * mean.
   *
   * Powering ON returns as soon as power is restored, which is what the switch
   * controls. The access point re-forming is a separate thing that takes its
   * own time -- about a second on the onboard radio and up to half a minute on
   * the USB adapter, whose control socket goes unresponsive while the driver
   * re-initialises -- so the message promises power, not service, and the
   * activity log records when service actually came back.
   */
  const setPower = (iface: string, on: boolean) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/power?on=${on ? 1 : 0}`, (b) =>
      b.on
        ? `${b.iface}: power restored. The access point takes a few seconds to ` +
          `come back — up to about 25 on the USB adapter — and the activity log ` +
          `says when it did.`
        : `${b.iface}: powered OFF — no client was told.`,
    );

  const powerOutage = (iface: string, sec: number) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/power?dur=${sec}`, (b) =>
      `${b.iface}: power cut for ${b.dur_sec}s. Nothing was announced — clients ` +
      `have to time out and rediscover the network, which is slower than a ` +
      `deauthentication and is the point.`,
    );

  /**
   * The last scan taken, PER RADIO.
   *
   * Keyed by interface rather than a single slot, because the channel buttons
   * are colour-coded from it and a scan of the 5GHz radio says nothing about
   * which 2.4GHz channel is quiet. One slot meant the second radio's buttons
   * would have been coloured with the first radio's band.
   *
   * Never fetched automatically. A scan is not free -- a few beacon gaps at
   * best, an outage on a radio that will not scan while serving -- so it
   * happens when someone asks, and the colours appear then and not before.
   */
  const scans = ref<Record<string, ScanResult>>({});
  /**
   * The scans the DAEMON remembers, which is what the channel plan is coloured
   * from. Served in the inventory, so the colours survive a page reload and two
   * people looking at the same box see the same thing -- a measurement that
   * evaporates when you press F5 is one nobody comes to trust.
   */
  const scanSummaries = computed(() => info.value?.scans ?? {});
  /**
   * Busy airtime per radio, as a percentage, ONLY for radios that report one.
   *
   * An absent entry is not zero and must not be rendered as zero: brcmfmac
   * returns no survey data at all, so a 0% there would describe an idle channel
   * on a radio nobody can ask. Callers render absence as an em dash.
   */
  const airtimePct = computed(() => info.value?.airtime ?? {});
  /** Which radio was scanned most recently, for the panel's single readout. */
  const lastScanned = ref('');
  const scan = computed<ScanResult | null>(() =>
    lastScanned.value ? (scans.value[lastScanned.value] ?? null) : null,
  );

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
      const s = body as ScanResult;
      scans.value = { ...scans.value, [iface]: s };
      lastScanned.value = iface;
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

  /*
   * EVICT and GATHER are the same endpoint read in opposite directions.
   *
   * `POST /radios/{iface}/steer?to={other}` asks everyone on `iface` to move to
   * `other`. Evicting a radio is that call named on the radio being emptied;
   * gathering to a radio is the same call named on the radio being filled, with
   * the two interfaces swapped. There is no second endpoint and no new daemon
   * code -- `to` has always been a parameter, and only the button was missing.
   *
   * Both are REQUESTS. 802.11v hands the decision to the client, and whether a
   * given phone honours it is the behaviour this box exists to test: a steer
   * that is refused is a result, not a failure.
   */
  const evict = (iface: string) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/steer`, (b) =>
      `${b.iface}: asked ${b.asked} client(s) to move to ${b.to}. They may ` +
      `refuse — 802.11v is a suggestion. Watch the stations counts to see who went.`,
    );

  /** `from` is the radio being emptied, `iface` the one being filled. */
  const gather = (iface: string, from: string) =>
    act(
      `/api/bridge/radios/${encodeURIComponent(from)}/steer` +
        `?to=${encodeURIComponent(iface)}`,
      (b) =>
        `${b.to}: asked ${b.asked} client(s) on ${b.iface} to come here. They ` +
        `may refuse — 802.11v is a suggestion. Watch the stations counts to ` +
        `see who came.`,
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
    scans, scanSummaries, airtimePct,
    load, loadSurvey, deauthAll, setPower, powerOutage, scanBand,
    applyProfile, setThreshold, evict, gather, linkAll, moveChannel,
  };
}
