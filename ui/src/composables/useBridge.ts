import { computed, ref, watch, onUnmounted, type Ref } from 'vue';
import { onStream, transport } from '@/composables/useStream';
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
      // One reload, and it is honest but early: controls return as soon as a
      // command is ACCEPTED, so the state they change has usually not changed
      // yet. The daemon confirms a moment later and the change arrives on the
      // stream. This used to be followed by reloads at 1.2s and 4s -- guesses
      // at how long the box would take, made when nothing pushed.
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

  /**
   * Take the ACCESS POINT down, leaving the radio powered.
   *
   * The other half of the pair, and deliberately worded to contrast with
   * setPower above. Same visible outcome -- the network goes away -- by the
   * opposite mechanism: rfkill stops the transmitter so nothing can be said,
   * while this closes the BSS with the transmitter still running, so the
   * departure is announced and the client acts on being told rather than on
   * working it out. That difference is the whole reason both controls exist.
   */
  const setAPEnabled = (iface: string, on: boolean, deauth = false) =>
    act(
      `/api/bridge/radios/${encodeURIComponent(iface)}/ap?on=${on ? 1 : 0}` +
        `&deauth=${deauth ? 1 : 0}`,
      (b) =>
        b.enabled
          ? b.deauth
            ? `${b.iface}: access point back up, and its return was ANNOUNCED — ` +
              `any client still holding a stale association was told to start ` +
              `again rather than left to notice.`
            : `${b.iface}: access point back up. Clients can associate again.`
          : b.deauth
            ? `${b.iface}: clients were told to leave (${b.deauth}), then the ` +
              `access point went down. The goodbye is explicit here, rather ` +
              `than whatever hostapd does on its own — and it is the same ` +
              `frame type the AP broadcasts on its way back up.`
            : `${b.iface}: access point DOWN — the radio is still ` +
              `transmitting, so unlike a power cut the clients were told it ` +
              `went away.`,
    );

  /**
   * Start or stop one of the box's own observability services.
   *
   * Worth a control rather than an SSH session because of what these two cost a
   * MEASUREMENT, not what they cost the box. ntopng inspects every packet
   * crossing the bridge, so it works hardest exactly while a run is in
   * progress -- 7% of a core idle, 71% under load, measured 2026-09-05. Being
   * able to take it out of the picture for the duration of a test, and put it
   * back afterwards, is the difference between a number and a number with a
   * caveat.
   */
  const setService = (name: string, on: boolean) =>
    act(`/api/services/${encodeURIComponent(name)}?on=${on ? 1 : 0}`, (b) =>
      b.running
        ? `${b.service} started.`
        : `${b.service} stopped — it is no longer competing for CPU with what ` +
          `you are measuring.`,
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
   * How contested each radio's channel is, resolved by the daemon from
   * whichever scan measured it — usually not that radio's own.
   *
   * Absent for a channel nobody has scanned. That is 'no measurement', not
   * 0%, and callers must render the two differently.
   */
  const air = computed(() => info.value?.air ?? {});
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
   * EVICT and GATHER are the same endpoint, and they make OPPOSITE promises.
   *
   * `POST /radios/{iface}/steer?to={other}` asks everyone on `iface` to move to
   * `other`. Evicting a radio is that call named on the radio being emptied;
   * gathering to a radio is the same call named on the radio being filled, with
   * the two interfaces swapped.
   *
   * What differs is `insist`, and it is not a preference:
   *
   *   - EVICT says "get off this radio" and names no destination. A client that
   *     will not go is disassociated and then picks for itself, which is
   *     precisely what the control claims. `insist=1`.
   *   - GATHER says "come to THIS radio". Forcing a refusing client sends it
   *     wherever it likes while the interface claims it went where it was told
   *     -- observed 2026-09-06 as "gather to wlan-usb" landing a device on
   *     wlan0, three times running. So gather asks, and a refusal is an answer.
   *
   * Both remain REQUESTS in the 802.11v sense: the decision is the client's, and
   * whether a given phone honours it is the behaviour this box exists to test.
   * A steer that is refused is a result, not a failure.
   */
  const evict = (iface: string) =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/evict`, (b) =>
      b.moved
        ? `${iface}: pushed ${b.moved} client(s) off and denied them here, so ` +
          `they cannot come straight back. Each ban lifts the moment that ` +
          `client lands somewhere, or after ${b.pin_sec}s. Where each one goes ` +
          `is its own choice — that is what an evict is.`
        : `${iface}: nothing to evict — no clients here.`,
    );

  /**
   * Gather: PIN every other radio's clients onto this one.
   *
   * One call, not one per source radio, because this is no longer a steer sent
   * to each. The daemon denies each client on every radio except the
   * destination and then moves it off the one it is on, so its own rescan has a
   * single access point left to choose. See gather.go.
   *
   * That is a different promise from the one this button used to make. Asking
   * could not keep it: 802.11 has no request that PLACES a station on a BSS, so
   * a client that refused — or that was disassociated and rescanned — picked for
   * itself, and "gather to wlan-usb" was observed putting a device on wlan0.
   * Removing the alternatives is the only deterministic answer.
   *
   * The cost, stated plainly because it is real: this stops being a measurement
   * of whether the device honours a transition request. It cannot refuse what it
   * was never asked. The per-client `steer` button on the Clients tab is still
   * the control for that question.
   */
  const gather = (iface: string) =>
    act(
      `/api/bridge/radios/${encodeURIComponent(iface)}/gather`,
      (b) =>
        b.moved
          ? `${iface}: moved ${b.moved} client(s) here by denying them on the ` +
            `other radios. Each ban lifts the moment that client arrives, or ` +
            `after ${b.pin_sec}s if it never does. They could not refuse — this ` +
            `removes the alternatives rather than asking.`
          : `${iface}: nothing to gather — every client is already here.`,
    );

  /**
   * A per-client link event applied to every station on a radio.
   *
   * Both are ANNOUNCED: the clients are told and reconnect knowing why, which
   * is the whole distinction from switching the radio off.
   */
  const linkAll = (iface: string, kind: 'deauth' | 'disassoc') =>
    act(`/api/bridge/radios/${encodeURIComponent(iface)}/link-all?kind=${kind}`, (b) =>
      kind === 'deauth'
        ? `${b.iface}: ${b.stations} station(s) deauthenticated. They were told, so they reconnect quickly.`
        : `${b.iface}: ${b.stations} station(s) disassociated — the softer transition. Some clients ride it out without a full reconnect.`,
    );

  const deauthAll = (iface: string) => linkAll(iface, 'deauth');

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

  /**
   * The view arrives on the stream when it CHANGES, and is polled otherwise.
   *
   * The daemon rebuilds this every couple of seconds and emits only when the
   * content actually differs, so an idle box is quiet and a radio going down
   * shows up within a second instead of on the next five-second tick. That is
   * what let the post-action reload timers go: they were guesses at how long
   * the box would take, needed only because nothing pushed.
   *
   * Polling stays as the fallback and is not a legacy path. Conditioning a link
   * means the operator may have just made their own connection to this box
   * unreliable on purpose, and the interface has to keep working while they do
   * -- so when the stream is not up, this behaves exactly as it did before.
   */
  let unsubscribe: (() => void) | undefined;

  function start() {
    void load();
    if (!unsubscribe) {
      unsubscribe = onStream('bridge', (data) => {
        // Only while this view is on screen; the connection is shared and
        // stays open for the snapshot regardless.
        if (active.value) {
          info.value = data as BridgeInfo;
          error.value = '';
        }
      });
    }
    syncPolling();
  }

  function syncPolling() {
    if (!active.value || transport.value === 'sse') {
      if (timer) window.clearInterval(timer);
      timer = 0;
      return;
    }
    if (!timer) timer = window.setInterval(load, POLL_MS);
  }

  function stop() {
    if (timer) window.clearInterval(timer);
    timer = 0;
    unsubscribe?.();
    unsubscribe = undefined;
  }

  watch(active, (on) => (on ? start() : stop()), { immediate: true });
  // The stream can come and go under a page that stays put, so the decision to
  // poll is re-made whenever the transport changes rather than only on mount.
  watch(transport, syncPolling);
  onUnmounted(stop);

  return {
    info, survey, scan, error, actionMsg, busy,
    scans, scanSummaries, air,
    load, loadSurvey, deauthAll, setPower, setAPEnabled, setService, powerOutage,
    scanBand,
    applyProfile, setThreshold, evict, gather, linkAll, moveChannel,
  };
}
