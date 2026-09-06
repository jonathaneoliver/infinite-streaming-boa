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
  es.addEventListener(name, (ev) => dispatch(name, (ev as MessageEvent).data));
}

function connect() {
  if (typeof EventSource === 'undefined') {
    transport.value = 'poll';
    return;
  }
  es = new EventSource('/api/state/stream');
  es.onopen = () => {
    transport.value = 'sse';
  };
  es.onerror = () => {
    es?.close();
    es = null;
    // Poll while the stream is down so the interface keeps updating, and retry
    // the stream periodically. Conditioning a link means the operator may have
    // just made their own connection to this box unreliable on purpose — the
    // reason this fallback exists at all.
    transport.value = 'poll';
    if (!retry) {
      retry = window.setTimeout(() => {
        retry = undefined;
        connect();
      }, 5000);
    }
  };
  for (const name of handlers.keys()) attach(name);
}

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
