/*
 * A stable colour per device, for charts that stack one device on another.
 *
 * Everywhere else in this interface colour means DIRECTION -- `--down` blue and
 * `--up` orange -- or a STATE, and those meanings are load-bearing enough that
 * the adapter tokens were given their own hues rather than borrowing either.
 * A stacked chart needs a third meaning again: identity, one band per device.
 *
 * That is only safe because of how the adapter charts are split. Download and
 * upload are drawn as two charts with the direction in the heading, not as one
 * chart with two colours, so no direction hue appears inside a plot where these
 * colours are used. Colour there is a key into the legend beside it, and never
 * the only thing carrying the meaning: every band is named.
 */

/**
 * The device palette: MUTED, and that is what separates it from everything else.
 *
 * The reserved colours -- the direction pair, ok/warn/bad, and the adapter
 * tokens -- are all strongly saturated. These sit at a visibly lower saturation
 * in the same lightness band, so a device band reads as one of a family rather
 * than as a state that has come on. Hue alone could not do this any more: with
 * five status colours and five adapter colours already spoken for, the wheel is
 * genuinely crowded, and saturation is the axis still free.
 *
 * Honest limitation, the same one the adapter palette carries: this set has not
 * been through the contrast and colour-vision checks that `--down` / `--up`
 * documented in style.css. It is used only where a legend names each band, so
 * nothing is lost by failing to tell two bands apart -- but it must not be
 * given work that colour alone has to do.
 */
const CLIENT_COLOURS = [
  '#6ea8d8', // muted blue
  '#8fbf7f', // muted green
  '#d4a15a', // muted gold
  '#b98ec4', // muted violet
  '#6fbdb0', // muted teal
  '#cf8b8b', // muted rose
  '#9aa87c', // muted olive
  '#8f9ecf', // muted indigo
];

const KEY = 'boa.clients.colour';

/*
 * Assigned on FIRST SIGHT and then kept, rather than derived from position.
 *
 * The obvious implementations are both wrong here. Indexing into a sorted list
 * of MACs re-colours every device whenever one joins or leaves, so a chart
 * changes meaning while being watched. Hashing the MAC is stable but collides,
 * and two adjacent bands sharing a colour in a stacked chart is exactly the
 * failure the colour exists to prevent.
 *
 * So: a registry, next free colour wins, persisted per browser so a reload does
 * not reshuffle a chart someone is reading. Keyed by MAC for the same reason
 * the rest of the client state is -- a device that roams between radios is the
 * same device and keeps its colour across the move.
 */
function load(): Record<string, number> {
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) ?? '{}');
    return typeof raw === 'object' && raw ? raw : {};
  } catch {
    return {};
  }
}

const assigned: Record<string, number> = load();

function save() {
  try {
    localStorage.setItem(KEY, JSON.stringify(assigned));
  } catch {
    // A private window, or a browser refusing storage. The colours are still
    // correct for this page; they just will not survive a reload.
  }
}

export function clientColour(mac: string): string {
  if (!(mac in assigned)) {
    const taken = new Set(Object.values(assigned));
    let next = -1;
    for (let i = 0; i < CLIENT_COLOURS.length; i++) {
      if (!taken.has(i)) {
        next = i;
        break;
      }
    }
    // Past the end of the palette this wraps and two devices share a colour.
    // Acceptable, and stated rather than hidden: the legend still names them,
    // and a box with more than eight devices on one radio has a busier problem
    // than a chart legend.
    if (next < 0) next = Object.keys(assigned).length % CLIENT_COLOURS.length;
    assigned[mac] = next;
    save();
  }
  return CLIENT_COLOURS[assigned[mac] % CLIENT_COLOURS.length];
}
