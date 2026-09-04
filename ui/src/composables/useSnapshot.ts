import { ref, shallowRef, onUnmounted } from 'vue';
import type { Snapshot, Client, Series } from '@/types';
import { adapterChannel } from '@/composables/useAdapters';

/**
 * Live server state.
 *
 * The server emits COMPLETE snapshots rather than deltas, so a dropped frame
 * cannot cause drift -- the next event repaints everything. That property is
 * what lets the transport degrade gracefully: if server-sent events are
 * unavailable, polling the same endpoint produces identical results, just less
 * often, with no reconciliation logic on either path.
 */

// One hour at a 1s tick -- the longest selectable range. Shorter ranges are
// drawn as a slice of this, so switching between them is instant and needs no
// round trip; only lengthening the range refetches.
const HISTORY = 3600;

export function useSnapshot() {
  const snap = shallowRef<Snapshot | null>(null);
  const connected = ref(false);
  const transport = ref<'sse' | 'poll' | 'offline'>('offline');
  const error = ref<string | null>(null);

  // Throughput history is accumulated on the client. The server deliberately
  // does not keep it: storing time series on an SD card is how a Pi appliance
  // wears out its storage, and any viewer can rebuild it in two minutes.
  const series = ref<Record<string, Series>>({});

  // What one seeded point covers, so the chart can say "6s avg" on a long range
  // rather than implying every range is raw 1 Hz data.
  const bucketMs = ref(1000);
  // The longest range fetched so far. Ranges shorter than this are served from
  // what is already in memory.
  const loadedSec = ref(300);

  function record(clients: Client[]) {
    const now = Date.now();
    const next = { ...series.value };
    for (const c of clients) {
      const s = next[c.mac] ??
        { t: [], down: [], up: [], cap: [], phyDown: [], phyUp: [], iface: [], chan: [] };
      next[c.mac] = {
        t: [...s.t, now].slice(-HISTORY),
        down: [...s.down, c.down_counters.throughput_mbps].slice(-HISTORY),
        up: [...s.up, c.up_counters.throughput_mbps].slice(-HISTORY),
        // The ENFORCED cap, not the configured one: a shaping failure should
        // draw as the old value rather than as what was asked for.
        cap: [...s.cap, c.down_counters.cap_mbps].slice(-HISTORY),
        // The link's ceiling beside what crossed it. Zero for a wired client,
        // which the chart draws as a gap rather than as a floor.
        phyDown: [...s.phyDown, c.station?.tx_phy_mbps ?? 0].slice(-HISTORY),
        phyUp: [...s.phyUp, c.station?.rx_phy_mbps ?? 0].slice(-HISTORY),
        // Where it was attached, and on what channel. Empty while it is not
        // present: a device that has gone away keeps its port for display, and
        // recording that would draw an unbroken band under a client that was
        // not there.
        //
        // The channel comes from the adapter store rather than from the client,
        // because the client does not carry one -- and the store is the same
        // one the rack and the tokens read, so the band cannot disagree with
        // the fold above it.
        iface: [...s.iface, c.present ? c.port ?? '' : ''].slice(-HISTORY),
        chan: [...s.chan, c.present ? adapterChannel(c.port ?? '') : 0].slice(-HISTORY),
      };
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
   * Only the window being drawn is fetched. An hour at full resolution is
   * around 140 KB of JSON per client, and this page is routinely served over a
   * link the operator has just throttled on purpose -- so the server decimates
   * to at most 600 points and reports the bucket width it used.
   *
   * Samples arrive with their own timestamps and are stored as they are. The
   * previous version rebuilt a fixed 300-slot 1 Hz array and zero-filled the
   * gaps, which quietly turned "not observed" into "no traffic"; now the
   * timestamps are kept and the chart positions by time, so a gap looks like a
   * gap.
   */
  async function seedFromServer(windowSec: number) {
    try {
      const r = await fetch(`/api/history?window=${windowSec}&points=600`);
      if (!r.ok) return;
      const body = await r.json();
      bucketMs.value = body.bucket_ms ?? 1000;

      const next: Record<string, Series> = {};
      for (const [mac, samples] of Object.entries(
        (body.clients ?? {}) as Record<
          string,
          {
            t: number; down: number; up: number; cap?: number;
            phy_down?: number; phy_up?: number; iface?: string; channel?: number;
          }[]
        >,
      )) {
        next[mac] = {
          t: samples.map((x) => x.t),
          down: samples.map((x) => x.down),
          up: samples.map((x) => x.up),
          // Absent on history written before caps were recorded; 0 reads as
          // unlimited, which draws no line rather than a wrong one.
          cap: samples.map((x) => x.cap ?? 0),
          // Absent on history written before PHY was recorded; 0 draws no line.
          phyDown: samples.map((x) => x.phy_down ?? 0),
          phyUp: samples.map((x) => x.phy_up ?? 0),
          // Absent on history written before the adapter was recorded. An
          // empty interface reads as "not attached", which draws a gap — the
          // honest rendering of "this box does not know".
          iface: samples.map((x) => x.iface ?? ''),
          chan: samples.map((x) => x.channel ?? 0),
        };
      }

      // The seed replaces history up to its last sample and LIVE DATA WINS
      // after it. Both halves matter: the stream connects immediately, so by
      // the time this resolves it has already appended samples that the seed
      // does not contain (an earlier version let a one-sample live entry
      // discard the entire seed, and the guard threw away what it was meant to
      // protect). Splicing on the timestamp keeps both without duplicating the
      // overlap.
      const merged = { ...series.value };
      for (const [mac, seed] of Object.entries(next)) {
        const live = series.value[mac];
        const edge = seed.t[seed.t.length - 1] ?? 0;
        if (!live) {
          merged[mac] = seed;
          continue;
        }
        const from = live.t.findIndex((t) => t > edge);
        merged[mac] =
          from < 0
            ? seed
            : {
                t: [...seed.t, ...live.t.slice(from)],
                down: [...seed.down, ...live.down.slice(from)],
                up: [...seed.up, ...live.up.slice(from)],
                cap: [...seed.cap, ...live.cap.slice(from)],
                phyDown: [...seed.phyDown, ...live.phyDown.slice(from)],
                phyUp: [...seed.phyUp, ...live.phyUp.slice(from)],
                iface: [...seed.iface, ...live.iface.slice(from)],
                chan: [...seed.chan, ...live.chan.slice(from)],
              };
      }
      series.value = merged;
    } catch {
      /* history is a convenience; its absence must not break the page */
    }
  }

  /**
   * Refetch for a newly chosen range.
   *
   * Shortening never refetches: the client already holds an hour, so a shorter
   * window is a slice of it drawn at full 1 Hz resolution, which is strictly
   * better than the decimated version the server would return.
   */
  function setRange(sec: number) {
    if (sec <= loadedSec.value) return;
    loadedSec.value = sec;
    void seedFromServer(sec);
  }

  void seedFromServer(loadedSec.value);

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

  return { snap, connected, transport, error, series, bucketMs, setRange };
}
