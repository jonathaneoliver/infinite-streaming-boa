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
 * The names whose colours are already spoken for, in the order they were
 * assigned.
 *
 * NOT the rack's order -- see rackRank for that. This exists only so an
 * adapter keeps the colour it has always had. Position in the RACK cannot be
 * the colour key: unplugging wlan-usb would promote wlan-usb2 into its slot and
 * recolour it, silently invalidating every screenshot and every log line
 * already read. A name never moves.
 */
const ADAPTER_COLOUR_ORDER = ['wlan-usb', 'wlan-usb2', 'wlan0', 'lan0'];

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
const ADAPTER_COLOURS = ['#a78bfa', '#2dd4bf', '#f472b6', '#c084fc', '#22d3ee'];

/** Interfaces the rack shows. The bridge and the WAN port are the fabric, not
 *  adapters -- they carry every client rather than any particular one. */
const RACK_ROLES = ['ap', 'radio', 'lan'];

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

/**
 * An adapter's colour, stable for the life of the box.
 *
 * Keyed off the NAME rather than the order the kernel happened to list them in,
 * and deliberately not off the rack position either, so unplugging one adapter
 * does not recolour the others -- which would silently invalidate every
 * screenshot and every log line already read.
 */
export function adapterColour(name: string): string {
  const i = ADAPTER_COLOUR_ORDER.indexOf(name);
  if (i >= 0) return ADAPTER_COLOURS[i % ADAPTER_COLOURS.length];
  // Not a known adapter: derive something stable from the name rather than
  // reusing a colour that belongs to one of the four above.
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return ADAPTER_COLOURS[
    (ADAPTER_COLOUR_ORDER.length + (h % 2)) % ADAPTER_COLOURS.length
  ];
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
