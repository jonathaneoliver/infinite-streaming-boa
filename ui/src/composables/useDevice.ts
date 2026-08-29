import { ref } from 'vue';
import type { Policy, Shape, Match } from '@/types';

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
    reset,
    forget,
  };
}
