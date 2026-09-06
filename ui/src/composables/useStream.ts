import { ref } from 'vue';

/**
 * The one connection to the daemon's event stream.
 *
 * Module scope, not a composable factory, because there must be exactly ONE of
 * these however many views want data from it. Browsers cap concurrent
 * connections per origin at around six on HTTP/1.1, and — more to the point —
 * the reconnect-and-fallback behaviour below is the part that must not be got
 * wrong on a box whose whole purpose is making links unreliable. Two copies of
 * it would be two things to keep in step.
 *
 * Frames are named: unnamed ones carry the device snapshot (so they arrive on
 * `message`, unchanged from when this was the only consumer), and `bridge`
 * carries the box's own interfaces. Named frames do not reach `onmessage`, so
 * adding one cannot disturb an existing listener.
 */

/** Which transport is live, so each consumer can decide whether to poll. */
export const transport = ref<'sse' | 'poll' | 'offline'>('offline');

type Handler = (data: unknown) => void;

const handlers = new Map<string, Set<Handler>>();
let es: EventSource | null = null;
let retry: number | undefined;

/**
 * How long the stream may say nothing before it is presumed dead.
 *
 * The daemon emits a snapshot every second, so silence this long is eight
 * missed frames and is not ambiguous. It is deliberately not tighter: a frame
 * arriving late is normal on a box whose job is delaying packets, and a
 * watchdog that trips on ordinary jitter would replace a stall with a reconnect
 * loop.
 */
const STALE_MS = 8000;

/** When a frame -- of any kind -- last arrived. */
let lastFrame = Date.now();

function dispatch(name: string, raw: string) {
  const set = handlers.get(name);
  if (!set?.size) return;
  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    return; // a malformed frame is not worth tearing the stream down for
  }
  for (const h of set) h(data);
}

function attach(name: string) {
  if (!es) return;
  // `message` is the built-in for unnamed frames; everything else is a named
  // event and needs its own listener.
  es.addEventListener(name, (ev) => {
    // Every frame counts as proof of life, whichever kind it is. Recorded here
    // rather than in dispatch, because dispatch returns early when nothing is
    // listening for that name -- and a frame nobody wants still says the
    // connection is alive.
    lastFrame = Date.now();
    dispatch(name, (ev as MessageEvent).data);
  });
}

/**
 * Drop the stream and go through the same recovery `onerror` uses.
 *
 * Separate from the handler because the watchdog needs it too, and the two must
 * not drift: whatever a detected error does, an undetected one has to do.
 */
function fallback() {
  es?.close();
  es = null;
  // Poll while the stream is down so the interface keeps updating, and retry
  // the stream periodically. Conditioning a link means the operator may have
  // just made their own connection to this box unreliable on purpose -- the
  // reason this fallback exists at all.
  transport.value = 'poll';
  if (!retry) {
    retry = window.setTimeout(() => {
      retry = undefined;
      connect();
    }, 5000);
  }
}

/**
 * Trip the fallback when the stream has gone quiet without saying so.
 *
 * THE FAILURE THIS EXISTS FOR IS SILENT. `EventSource.onerror` fires when the
 * browser observes the connection break; it does not fire for a socket that is
 * simply never answered again -- a slept laptop, a Wi-Fi roam, or this box
 * steering the operator's own machine off the radio it was reading the page
 * over. In that state transport stays 'sse', so the poll fallback below is
 * never armed and the page waits for a frame that is not coming.
 *
 * It presents as EMPTY CHARTS, which is the worst possible disguise here. The
 * plot's right edge is a wall clock (see useChartClock) rather than the newest
 * sample, so a stalled page keeps advancing its window until every sample has
 * aged out of it -- and an empty plot beside a frozen readout is exactly what a
 * genuinely idle device looks like. Observed 2026-09-06: a client charting
 * 50-100 Mbps bursts throughout drew as flat and empty until the page was
 * reloaded by hand.
 */
function watchdog() {
  if (!es || transport.value !== 'sse') return;
  if (Date.now() - lastFrame <= STALE_MS) return;
  fallback();
}

function connect() {
  if (typeof EventSource === 'undefined') {
    transport.value = 'poll';
    return;
  }
  es = new EventSource('/api/state/stream');
  es.onopen = () => {
    // A fresh connection has not gone quiet yet. Without this the watchdog
    // inherits the age of the DEAD stream's last frame and trips immediately,
    // turning one stall into a reconnect loop.
    lastFrame = Date.now();
    transport.value = 'sse';
  };
  es.onerror = fallback;
  for (const name of handlers.keys()) attach(name);
}

// Checked more often than the timeout it enforces, so a stall is caught within
// a couple of seconds of becoming certain rather than up to eight late.
window.setInterval(watchdog, 2000);

// A backgrounded tab has its timers throttled, so the interval above may not
// have run while the page was hidden -- and returning to a stalled page is the
// common way this is met. Check on the way back rather than waiting.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) watchdog();
});

/**
 * Listen for one kind of frame. Returns an unsubscribe function.
 *
 * The connection is opened by the first subscriber and then left open: this is
 * a single-page appliance interface, the stream is the thing it is for, and
 * closing it when one view unmounts would drop the other view's data too.
 */
export function onStream(name: string, fn: Handler): () => void {
  let set = handlers.get(name);
  if (!set) {
    set = new Set();
    handlers.set(name, set);
    // A listener added after the connection is open still needs attaching.
    if (es) attach(name);
  }
  set.add(fn);

  if (!es && !retry) connect();

  return () => {
    set!.delete(fn);
  };
}
