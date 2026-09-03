import { onUnmounted, ref } from 'vue';

/**
 * The box's activity log: what CHANGED, as opposed to what is.
 *
 * Everything else in this interface is state on a 1Hz snapshot -- who is
 * connected, what each radio is doing. State cannot answer "did that phone just
 * move to 2.4GHz", because by the time you look it is simply on 2.4GHz with
 * nothing saying it moved. The daemon raises an event at the moment it notices,
 * and this reads them.
 *
 * POLLED, not streamed, and deliberately: events are bursty and rare -- nothing
 * for ten minutes, then six in a second when a radio is switched off -- so
 * attaching them to the SSE snapshot would carry an empty array every second
 * for the sake of the rare one that is not.
 */
export interface BoaEvent {
  seq: number;
  at: number;
  kind: 'join' | 'leave' | 'roam' | 'radio' | 'action' | 'warning';
  text: string;
  mac?: string;
  iface?: string;
}

/** Matches the daemon's ring, so the two cannot disagree about what is kept. */
const KEEP = 500;

export function useEvents(pollMs = 3000) {
  const events = ref<BoaEvent[]>([]);
  const err = ref('');
  /** Events raised since the log was last opened, for the collapsed badge. */
  const unseen = ref(0);
  let since = 0;
  let timer = 0;

  async function poll() {
    try {
      const r = await fetch(`/api/events?since=${since}`);
      if (!r.ok) throw new Error(`${r.status}`);
      const body = (await r.json()) as { events: BoaEvent[] };
      if (body.events.length) {
        since = body.events[body.events.length - 1].seq;
        // Newest first: the interesting event is the one that just happened,
        // and a log that has to be scrolled to reach it is a log nobody reads.
        events.value = [...body.events]
          .reverse()
          .concat(events.value)
          .slice(0, KEEP);
        unseen.value += body.events.length;
      }
      err.value = '';
    } catch (e) {
      // Named rather than swallowed. A silent poll failure looks exactly like a
      // quiet box, which is the one thing this panel must never fake.
      err.value = `activity log unavailable: ${(e as Error).message}`;
    }
  }

  function start() {
    if (timer) return;
    void poll();
    timer = window.setInterval(poll, pollMs);
  }
  function stop() {
    if (timer) window.clearInterval(timer);
    timer = 0;
  }
  const markSeen = () => {
    unseen.value = 0;
  };

  onUnmounted(stop);
  return { events, err, unseen, start, stop, markSeen };
}
