import { ref, shallowRef, onUnmounted } from 'vue';
import type { Snapshot, Client, Series } from '@/types';

/**
 * Live server state.
 *
 * The server emits COMPLETE snapshots rather than deltas, so a dropped frame
 * cannot cause drift -- the next event repaints everything. That property is
 * what lets the transport degrade gracefully: if server-sent events are
 * unavailable, polling the same endpoint produces identical results, just less
 * often, with no reconciliation logic on either path.
 */

// 5 minutes at a 1s tick. Long enough to show a video player's segment
// cadence and its adaptation after a cap changes, which two minutes clipped.
const HISTORY = 300;

export function useSnapshot() {
  const snap = shallowRef<Snapshot | null>(null);
  const connected = ref(false);
  const transport = ref<'sse' | 'poll' | 'offline'>('offline');
  const error = ref<string | null>(null);

  // Throughput history is accumulated on the client. The server deliberately
  // does not keep it: storing time series on an SD card is how a Pi appliance
  // wears out its storage, and any viewer can rebuild it in two minutes.
  const series = ref<Record<string, Series>>({});

  function record(clients: Client[]) {
    const next = { ...series.value };
    for (const c of clients) {
      const s = next[c.mac] ?? { down: [], up: [] };
      s.down = [...s.down, c.down_counters.throughput_mbps].slice(-HISTORY);
      s.up = [...s.up, c.up_counters.throughput_mbps].slice(-HISTORY);
      next[c.mac] = s;
    }
    series.value = next;
  }

  function apply(s: Snapshot) {
    snap.value = s;
    connected.value = true;
    error.value = null;
    record(s.clients ?? []);
  }

  /**
   * Seed the charts from the server's history so a reload does not start from a
   * blank plot and take five minutes to become useful again.
   *
   * Samples are placed by TIMESTAMP rather than appended in order. The chart's
   * x-axis assumes a contiguous 1 Hz series, so a gap -- a daemon restart, a
   * sleeping laptop, a device that went away -- would otherwise be squeezed out
   * and the plot would misrepresent when things happened. Missing slots are
   * filled with zero, which keeps the time axis truthful; distinguishing "no
   * traffic" from "not observed" would need the chart to handle nulls, and the
   * timestamps are there to make that possible later.
   */
  async function seedFromServer() {
    try {
      const r = await fetch('/api/history');
      if (!r.ok) return;
      const body = await r.json();
      const now: number = body.now ?? Date.now();
      const step: number = body.interval_ms ?? 1000;
      const next: Record<string, Series> = {};

      for (const [mac, samples] of Object.entries(
        (body.clients ?? {}) as Record<string, { t: number; down: number; up: number }[]>,
      )) {
        const down = new Array<number>(HISTORY).fill(0);
        const up = new Array<number>(HISTORY).fill(0);
        for (const s of samples) {
          const slot = HISTORY - 1 - Math.round((now - s.t) / step);
          if (slot >= 0 && slot < HISTORY) {
            down[slot] = s.down;
            up[slot] = s.up;
          }
        }
        next[mac] = { down, up };
      }
      // Only seed series the live stream has not already started, so a slow
      // history fetch cannot overwrite fresher data.
      series.value = { ...next, ...series.value };
    } catch {
      /* history is a convenience; its absence must not break the page */
    }
  }
  void seedFromServer();

  let es: EventSource | null = null;
  let pollTimer: number | undefined;
  let retry: number | undefined;

  function startPolling() {
    transport.value = 'poll';
    if (pollTimer) return;
    pollTimer = window.setInterval(async () => {
      try {
        const r = await fetch('/api/state');
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        apply(await r.json());
      } catch (e) {
        connected.value = false;
        transport.value = 'offline';
        error.value = String(e);
      }
    }, 1000);
  }

  function stopPolling() {
    if (pollTimer) window.clearInterval(pollTimer);
    pollTimer = undefined;
  }

  function connect() {
    if (typeof EventSource === 'undefined') {
      startPolling();
      return;
    }
    es = new EventSource('/api/state/stream');
    es.onopen = () => {
      transport.value = 'sse';
      stopPolling();
    };
    es.onmessage = (ev) => {
      try {
        apply(JSON.parse(ev.data));
      } catch {
        /* a malformed frame is not worth tearing the stream down for */
      }
    };
    es.onerror = () => {
      connected.value = false;
      es?.close();
      es = null;
      // Poll while the stream is down so the UI keeps updating, and retry the
      // stream periodically. Conditioning a link means the operator may have
      // just made their own connection to this box unreliable on purpose.
      startPolling();
      if (!retry) {
        retry = window.setTimeout(() => {
          retry = undefined;
          connect();
        }, 5000);
      }
    };
  }

  connect();

  onUnmounted(() => {
    es?.close();
    stopPolling();
    if (retry) window.clearTimeout(retry);
  });

  return { snap, connected, transport, error, series };
}
