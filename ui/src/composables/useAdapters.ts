import { computed, ref } from 'vue';
import type { IfaceInfo } from '@/types';

/*
 * The adapters, shared by everything that names one.
 *
 * Module-level rather than per-caller, unlike the other composables here: the
 * rack, every client row and the activity log must agree on an adapter's
 * colour, its channel and whether its fold is open. Three copies of that state
 * would drift, and the whole point of the token is that the same adapter looks
 * identical wherever it appears.
 */

/**
 * The rack's order, by ROLE rather than by name.
 *
 * A rack that reorders itself is a rack you have to re-read, so the order is
 * still fixed -- but it is now fixed by what an interface IS, not by a list of
 * the names this box happened to have. Radios first because they are what a
 * Wi-Fi test is about, USB adapters before the onboard radio because they are
 * the faster and more capable, and the wired port last.
 *
 * The name list this replaced ended at wlan-usb2, and its fallthrough sorted
 * anything unknown AFTER everything on it -- so a third USB radio, which udev
 * already names and radioplan already serves, rendered BELOW the Ethernet
 * adapter. Ranked by role, a radio the code has never heard of still sorts
 * among the radios.
 */
function rackRank(i: IfaceInfo): number {
  if (!i.wireless) return 2;
  return i.radio?.bus === 'onboard' ? 1 : 0;
}

/**
 * Adapters whose colour is fixed by name, because these names are the same on
 * every box.
 *
 * The USB radios are deliberately absent. They used to be listed here as
 * `wlan-usb` and `wlan-usb2`, which was true while a name came from the socket
 * an adapter was plugged into; they are now named after the adapter itself --
 * `wlan-usb-46c7` -- so the name cannot be known when this file is written.
 * Everything not listed here is assigned a colour at runtime, uniquely, by
 * `adapterColours` below.
 */
const ADAPTER_COLOUR_FIXED = ['wlan0', 'lan0'];

/**
 * Adapter identity colours.
 *
 * NOT the direction pair and NOT the status colours. `--down` and `--up` mean
 * downlink and uplink everywhere else in this interface, and `--ok` / `--warn`
 * / `--bad` mean a state; an adapter swatch borrowing any of them would read as
 * a direction or as an alarm. So identity gets its own hues -- violet, teal,
 * pink -- which are far apart from each other and from both reserved families.
 *
 * Honest limitation: these are picked to sit in the same lightness band and the
 * same shade family as the existing tokens, but the CVD separation the
 * direction pair documents in style.css has NOT been re-run for this set. If
 * adapter colour ever has to carry meaning on its own rather than alongside a
 * name, that check is owed.
 */
/*
 * Five hues that are actually five. It was
 * ['#a78bfa', '#2dd4bf', '#f472b6', '#c084fc', '#22d3ee'] -- which is violet,
 * teal, pink, ANOTHER violet and a cyan a shade off the teal, so five slots
 * carried about three distinguishable colours. Reported as too close to tell
 * apart, on a token whose whole job is telling one adapter from another.
 */
const ADAPTER_COLOURS = [
  '#a78bfa', // violet
  '#2dd4bf', // teal
  '#f472b6', // pink
  '#fbbf24', // amber
  '#38bdf8', // sky
];

/** Interfaces the rack shows. The bridge and the WAN port are the fabric, not
 *  adapters -- they carry every client rather than any particular one. */
// 'scanner' is in the rack for the same reason it is in the diagram: it is a
// radio the box has, and a row is where its last reading and its age are
// readable. What the role changes is the row's badge and its controls, not
// whether the hardware is shown at all.
const RACK_ROLES = ['ap', 'scanner', 'radio', 'lan'];

const ifaces = ref<IfaceInfo[]>([]);
const open = ref<Record<string, boolean>>(loadOpen());

const OPEN_KEY = 'boa.adapters.open';

function loadOpen(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(OPEN_KEY) ?? '{}');
  } catch {
    return {};
  }
}

function saveOpen() {
  try {
    localStorage.setItem(OPEN_KEY, JSON.stringify(open.value));
  } catch {
    // A browser refusing storage is not a reason to break the fold.
  }
}

/** Feed the store from the bridge poll. */
export function setAdapterIfaces(list: IfaceInfo[]) {
  ifaces.value = list;
}

/** The adapters, in rack order. */
export const rackAdapters = computed(() =>
  [...ifaces.value.filter((i) => RACK_ROLES.includes(i.role))].sort((a, b) => {
    const ra = rackRank(a);
    const rb = rackRank(b);
    if (ra !== rb) return ra - rb;
    return a.name.localeCompare(b.name);
  }),
);

/** A small stable hash of a name, so a colour preference does not depend on
 *  the order adapters were discovered in. */
function nameHash(name: string): number {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return h;
}

/**
 * Every adapter's colour, allocated so that no two on screen share one.
 *
 * A colour that identifies an adapter is useless if two adapters can have the
 * same one, and a plain hash of the name allows exactly that -- with a
 * five-colour palette two radios collided about a fifth of the time, and the
 * previous fallback narrowed that to two colours, so a box with two USB radios
 * showed them in the same colour half the time. That is the case this exists
 * for: telling two identical dongles apart is the whole job.
 *
 * So each name states a PREFERENCE by hash and the first taker keeps it;
 * anything already spoken for walks to the next free slot. Names are visited in
 * sorted order rather than discovery order, so the result depends only on WHICH
 * adapters are present, never on when they appeared or how the kernel listed
 * them.
 *
 * A name still does not move on its own: unplugging an adapter cannot recolour
 * the others unless it was the one holding a contested slot, which is the
 * narrowest version of that behaviour available while keeping uniqueness. The
 * alternative -- position in the rack -- recolours on every change, which is
 * what the old fixed list was written to avoid.
 *
 * Beyond ADAPTER_COLOURS.length adapters the palette necessarily repeats. Five
 * covers two USB radios, the onboard radio, the wired port and one spare.
 */
const adapterColours = computed<Record<string, string>>(() => {
  // Only the adapters the rack actually shows. The bridge and the WAN port are
  // fabric, not adapters, and giving them a colour would spend palette slots on
  // things that never wear one -- which is enough, with five colours and four
  // adapters, to force two real radios onto the same swatch.
  const names = ifaces.value
    .filter((i) => RACK_ROLES.includes(i.role))
    .map((i) => i.name)
    .sort();
  const taken = new Set<number>();
  const out: Record<string, string> = {};

  // The fixed names first, so a runtime adapter can never take a colour that
  // belongs to one of them on every box.
  for (const name of ADAPTER_COLOUR_FIXED) {
    const idx = ADAPTER_COLOUR_FIXED.indexOf(name);
    taken.add(idx);
    out[name] = ADAPTER_COLOURS[idx % ADAPTER_COLOURS.length];
  }

  for (const name of names) {
    if (out[name]) continue;
    const want = nameHash(name) % ADAPTER_COLOURS.length;
    let idx = want;
    for (let k = 0; k < ADAPTER_COLOURS.length; k++) {
      const cand = (want + k) % ADAPTER_COLOURS.length;
      if (!taken.has(cand)) {
        idx = cand;
        break;
      }
    }
    taken.add(idx);
    out[name] = ADAPTER_COLOURS[idx];
  }
  return out;
});

/**
 * An adapter's colour, stable for the life of the box and unique among the
 * adapters on screen.
 *
 * Keyed off the NAME rather than the order the kernel happened to list them in,
 * and deliberately not off the rack position either, so unplugging one adapter
 * does not recolour the others -- which would silently invalidate every
 * screenshot and every log line already read.
 */
export function adapterColour(name: string): string {
  return adapterColours.value[name] ?? ADAPTER_COLOURS[0];
}

/** What the token prints beside the name: the channel, where there is one. */
export function adapterChannel(name: string): number {
  return ifaces.value.find((i) => i.name === name)?.ap?.channel ?? 0;
}

export function adapterFor(name: string): IfaceInfo | undefined {
  return ifaces.value.find((i) => i.name === name);
}

export function isOpen(name: string): boolean {
  return !!open.value[name];
}

export function toggleAdapter(name: string) {
  open.value = { ...open.value, [name]: !open.value[name] };
  saveOpen();
}

/**
 * Bring an adapter's fold into view and open it.
 *
 * Opening as well as scrolling, because the jump exists to answer "what is this
 * client attached to" -- and arriving at a closed fold answers it with a row
 * the reader has to then click. The scroll is deferred a frame so it measures
 * the fold at its opened height rather than its collapsed one.
 */
export function revealAdapter(name: string) {
  if (!open.value[name]) {
    open.value = { ...open.value, [name]: true };
    saveOpen();
  }
  requestAnimationFrame(() => {
    document
      .getElementById(`adapter-${name}`)
      ?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  });
}
