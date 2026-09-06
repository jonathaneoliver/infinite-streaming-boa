import { ref } from 'vue';
import type { Policy, Shape, Match, Pattern } from '@/types';

/**
 * Writes to a device's policy.
 *
 * Two rules make the controls behave, both learned the hard way on the
 * streaming test harness:
 *
 * 1. DEBOUNCE. A slider drag fires an input event per pixel. Sending a request
 *    per event means the first one advances the server's revision and every
 *    later in-flight request is rejected as stale, which then rolls the slider
 *    back to where the drag started. One request per drag, 200 ms after the
 *    last movement, avoids the whole class of problem.
 *
 * 2. base_revision. Every write carries the revision the operator was looking
 *    at. If someone edited the same device from another tab in the meantime the
 *    server refuses rather than silently discarding their change, and hands
 *    back the current state so the UI can resync.
 */

const DEBOUNCE_MS = 200;

export function useDevice() {
  const writing = ref(false);
  const conflict = ref<string | null>(null);
  const timers = new Map<string, number>();

  async function send(
    path: string,
    method: string,
    body: unknown,
    retried = false,
  ): Promise<Policy | null> {
    writing.value = true;
    conflict.value = null;
    try {
      const r = await fetch(path, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (r.status === 409) {
        const j = await r.json();
        const current = j.current as Policy | undefined;
        /*
         * RETRY ONCE against the revision the server just handed back.
         *
         * A 409 means two different things wearing one status code. It can be a
         * real collision -- someone editing this device elsewhere -- and it can
         * be this tab racing its own previous write, because the revision is
         * read from the SNAPSHOT and the snapshot lags the store by up to a
         * tick. The second case is not a conflict with anyone; it is the same
         * operator, and it happens whenever a debounced control is used twice
         * inside a second.
         *
         * Left alone it fails SILENTLY: the write is dropped, the interface
         * keeps showing the value that was asked for, and the control reads as
         * broken. Retrying once with the returned revision settles the
         * self-race, and a second refusal is a genuine collision, which is
         * reported.
         */
        if (current && !retried) {
          const b = body as Record<string, unknown>;
          return send(path, method, { ...b, base_revision: current.rev }, true);
        }
        conflict.value =
          'This device was changed somewhere else. Showing the current settings.';
        return current ?? null;
      }
      if (!r.ok) {
        conflict.value = (await r.json()).error ?? `HTTP ${r.status}`;
        return null;
      }
      return (await r.json()) as Policy;
    } catch (e) {
      conflict.value = String(e);
      return null;
    } finally {
      writing.value = false;
    }
  }

  /** Coalesces rapid calls sharing a key into one request. */
  function debounced(key: string, fn: () => void) {
    const t = timers.get(key);
    if (t) window.clearTimeout(t);
    timers.set(key, window.setTimeout(fn, DEBOUNCE_MS));
  }

  function patchShape(
    mac: string,
    rev: number,
    dir: 'down' | 'up',
    shape: Shape,
  ) {
    debounced(`${mac}:${dir}`, () => {
      void send(`/api/devices/${mac}/policy`, 'PATCH', {
        base_revision: rev,
        [dir]: shape,
      });
    });
  }

  function patchPolicy(mac: string, rev: number, patch: Record<string, unknown>) {
    return send(`/api/devices/${mac}/policy`, 'PATCH', {
      base_revision: rev,
      ...patch,
    });
  }

  /**
   * The same write, debounced, for a control that is DRAGGED rather than
   * clicked.
   *
   * patchPolicy above is deliberately immediate: a preset button or a rename is
   * one event and should not wait. A slider is not -- it emits continuously, and
   * every send bumps `Policy.Rev`, so an undebounced drag races its own
   * in-flight requests and 409s against the revision it just moved. That is the
   * same reasoning patchShape carries; this is the whole-object variant of it.
   *
   * Keyed per MAC rather than per field, because two policy patches for one
   * device would collide on the revision even if they touched different fields.
   */
  function patchPolicySoon(
    mac: string,
    rev: number,
    patch: Record<string, unknown>,
  ) {
    debounced(`${mac}:policy`, () => {
      void send(`/api/devices/${mac}/policy`, 'PATCH', {
        base_revision: rev,
        ...patch,
      });
    });
  }

  function addSub(mac: string, rev: number, name: string, match: Match) {
    return send(`/api/devices/${mac}/sub`, 'POST', {
      base_revision: rev,
      name,
      match,
    });
  }

  function patchSub(
    mac: string,
    id: string,
    rev: number,
    patch: Record<string, unknown>,
  ) {
    debounced(`${mac}:${id}`, () => {
      void send(`/api/devices/${mac}/sub/${id}`, 'PATCH', {
        base_revision: rev,
        ...patch,
      });
    });
  }

  function deleteSub(mac: string, id: string) {
    return send(`/api/devices/${mac}/sub/${id}`, 'DELETE', undefined);
  }

  /**
   * Start a ladder sweep.
   *
   * Not debounced and not carrying base_revision: this is a command, not an
   * edit to the policy the operator is looking at. It starts a measurement
   * process; nothing about the stored device configuration changes, so there is
   * no concurrent-edit to lose.
   */
  function startSweep(mac: string, service: string) {
    return send(`/api/devices/${mac}/sweep`, 'POST', { service });
  }

  function stopSweep(mac: string) {
    return send(`/api/devices/${mac}/sweep`, 'DELETE', undefined);
  }

  function removeLadder(mac: string, service: string) {
    return send(
      `/api/devices/${mac}/ladders/${encodeURIComponent(service)}`,
      'DELETE',
      undefined,
    );
  }

  /**
   * Store a device's timeline.
   *
   * Debounced on the same reasoning as patchShape: editing a keyframe IS a
   * slider drag, so an undebounced write would fire per pixel, and every
   * request after the first would be refused as stale and roll the keyframe
   * back to where the drag started.
   *
   * The whole pattern goes every time. A keyframe list is only meaningful as an
   * ordered whole, and a per-keyframe patch would let two edits interleave into
   * a timeline neither operator authored.
   */
  function putPattern(mac: string, rev: number, pattern: Pattern) {
    debounced(`${mac}:pattern`, () => {
      void send(`/api/devices/${mac}/pattern`, 'PUT', {
        base_revision: rev,
        pattern,
      });
    });
  }

  function deletePattern(mac: string) {
    return send(`/api/devices/${mac}/pattern`, 'DELETE', undefined);
  }

  /** Start the stored pattern, or resume a run paused by a manual edit. */
  function playPattern(mac: string) {
    return send(`/api/devices/${mac}/pattern/play`, 'POST', undefined);
  }

  function stopPattern(mac: string) {
    return send(`/api/devices/${mac}/pattern/play`, 'DELETE', undefined);
  }

  function reset(mac: string) {
    return send(`/api/devices/${mac}/reset`, 'POST', undefined);
  }

  /** Drop a device's stored configuration so it stops being listed. */
  function forget(mac: string) {
    return send(`/api/devices/${mac}`, 'DELETE', undefined);
  }

  // Group A per-client link events. Unlike shaping these act on the Wi-Fi
  // association: deauth takes the link down (the client reconnects), disassoc
  // is the softer form. reason is an optional 802.11 reason code. See #135.
  function linkDeauth(mac: string, reason?: number) {
    const q = reason ? `?reason=${reason}` : '';
    return send(`/api/devices/${mac}/link/deauth${q}`, 'POST', undefined);
  }
  function linkDisassoc(mac: string, reason?: number) {
    const q = reason ? `?reason=${reason}` : '';
    return send(`/api/devices/${mac}/link/disassoc${q}`, 'POST', undefined);
  }
  // deadzone: a sustained outage held for `sec` seconds (repeated deauth).
  function linkDeadzone(mac: string, sec: number) {
    return send(`/api/devices/${mac}/link/deadzone?dur=${sec}`, 'POST', undefined);
  }
  // steer: ask this one client to move to the box's other radio (802.11v).
  // A REQUEST -- the client decides, and whether it complies is the result
  // being looked for. The daemon resolves both radios, so nothing here needs
  // to know which band the client is on.
  function linkSteer(mac: string) {
    return send(`/api/devices/${mac}/link/steer`, 'POST', undefined);
  }

  return {
    linkDeauth,
    linkDisassoc,
    linkDeadzone,
    linkSteer,
    writing,
    conflict,
    patchShape,
    patchPolicy,
    patchPolicySoon,
    addSub,
    patchSub,
    deleteSub,
    startSweep,
    stopSweep,
    removeLadder,
    putPattern,
    deletePattern,
    playPattern,
    stopPattern,
    reset,
    forget,
  };
}
