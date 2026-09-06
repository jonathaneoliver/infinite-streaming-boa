import { onUnmounted, ref, watch } from 'vue';
import { onStream, transport } from '@/composables/useStream';

/**
 * The box's activity log: what CHANGED, as opposed to what is.
 *
 * Everything else in this interface is state on a 1Hz snapshot -- who is
 * connected, what each radio is doing. State cannot answer "did that phone just
 * move to 2.4GHz", because by the time you look it is simply on 2.4GHz with
 * nothing saying it moved. The daemon raises an event at the moment it notices,
 * and this reads them.
 *
 * STREAMED when the connection is up, polled when it is not.
 *
 * This was polled outright, on the argument that events are bursty and rare --
 * nothing for ten minutes, then six in a second when a radio is switched off --
 * so attaching them to the SSE snapshot would carry an empty array every second
 * for the sake of the rare one that is not. That was right about riding the
 * SNAPSHOT, which goes out every tick whether or not anything happened.
 *
 * It is not an argument against a NAMED frame. The daemon writes one only when
 * the log has gained something, so an idle box carries nothing at all -- and
 * six events in a second arrive as they happen instead of batched up to three
 * seconds later. Sporadic turns out to be the best case for push, not the
 * worst.
 *
 * A frame and a poll response are the same payload and go through the same
 * apply(), so the restart detection below cannot come to work on one path and
 * not the other.
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

  /** What to do with a batch, however it arrived. */
  function apply(body: { events: BoaEvent[]; latest?: number }) {
      // The log RESTARTED under us: the daemon's ring is in memory, so a
      // deploy begins its sequence again at 1 while this page is still holding
      // a cursor from the previous run. Asking for events after that cursor
      // then returns an empty list -- correctly, and for as long as the page
      // stays open, because the new sequence may never reach the old one.
      //
      // Nothing about that looks like a failure: the fetch succeeds, the array
      // is valid, and the panel renders as a quiet box. It is the one lie this
      // panel must not tell, and it is why `latest` is sent with every
      // response rather than only with a populated one (#196).
      //
      // Within one run the sequence only grows, so latest < since cannot mean
      // anything else. Everything held is dropped, because those events belong
      // to a run that has ended.
    if (body.latest !== undefined && body.latest < since) {
      since = 0;
      events.value = [];
      unseen.value = 0;
    }
    if (body.events.length) {
      since = body.events[body.events.length - 1].seq;
      // CHRONOLOGICAL, oldest first, newest at the end.
      //
      // This was reversed, on the argument that "the interesting event is the
      // one that just happened, and a log that has to be scrolled to reach it
      // is a log nobody reads". The premise was right and the remedy was
      // wrong: the fix for that is to keep the newest line in view, which the
      // panel now does by following the bottom, and not to invert time.
      //
      // Reading order matters here because these lines are a CAUSAL sequence --
      // "asked to go down", then "is down", then "left wlan-usb", then "joined
      // wlan0" -- and reversed, every story on this box was told backwards.
      // Oldest at the top, scrolling up and off, is how every log an operator
      // has ever read behaves.
      // SORTED BY THE TIME SHOWN, not by the order they were recorded.
      //
      // Some events are deliberately backdated. An association is stamped from
      // hostapd's own report of when the station went, not from when the daemon
      // got round to logging it (#215), so "left wlan0" can carry a timestamp a
      // second earlier than a line recorded before it. In insertion order that
      // reads as a log that has lost the plot: an access point going down at
      // .201 above a client leaving it at .250.
      //
      // Ties break on seq, which keeps the causal order within a millisecond --
      // three events sharing 04:25:57.250 still read "told them", "asked it to
      // go down", "it went down" rather than in whatever order a sort felt like.
      //
      // A backdated event can therefore appear ABOVE the newest line rather
      // than at the bottom. That is correct: it happened earlier, and the log
      // claims to be chronological.
      events.value = events.value
        .concat(body.events)
        .sort((x, y) => x.at - y.at || x.seq - y.seq)
        .slice(-KEEP);
      unseen.value += body.events.length;
    }
    err.value = '';
  }

  async function poll() {
    try {
      const r = await fetch(`/api/events?since=${since}`);
      if (!r.ok) throw new Error(`${r.status}`);
      apply((await r.json()) as { events: BoaEvent[]; latest?: number });
    } catch (e) {
      // Named rather than swallowed. A silent poll failure looks exactly like a
      // quiet box, which is the one thing this panel must never fake.
      err.value = `activity log unavailable: ${(e as Error).message}`;
    }
  }

  let unsubscribe: (() => void) | undefined;

  function start() {
    if (!unsubscribe) {
      unsubscribe = onStream('activity', (data) =>
        apply(data as { events: BoaEvent[]; latest?: number }),
      );
    }
    syncPolling();
  }

  /**
   * Poll only while the stream is down.
   *
   * Not a legacy path: conditioning a link means the operator may have just
   * made their own connection to this box unreliable on purpose, and the
   * activity log is the surface they would look at to see whether they had.
   */
  function syncPolling() {
    if (transport.value === 'sse') {
      if (timer) window.clearInterval(timer);
      timer = 0;
      return;
    }
    if (timer) return;
    void poll();
    timer = window.setInterval(poll, pollMs);
  }

  function stop() {
    if (timer) window.clearInterval(timer);
    timer = 0;
    unsubscribe?.();
    unsubscribe = undefined;
  }

  watch(transport, syncPolling);
  const markSeen = () => {
    unseen.value = 0;
  };

  onUnmounted(stop);
  return { events, err, unseen, start, stop, markSeen };
}
