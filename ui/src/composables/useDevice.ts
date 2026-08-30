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

  async function send(path: string, method: string, body: unknown) {
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
        conflict.value =
          'This device was changed somewhere else. Showing the current settings.';
        return j.current as Policy;
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

  return {
    writing,
    conflict,
    patchShape,
    patchPolicy,
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
