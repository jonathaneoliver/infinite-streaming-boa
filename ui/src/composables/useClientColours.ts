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
 * The device palette: SEPARATED BY HUE, and it used to be separated by nothing.
 *
 * It was eight muted colours inside one lightness band, on the argument that
 * the reserved hues -- the direction pair, ok/warn/bad, the adapter tokens --
 * are all strongly saturated, so a quieter family would read as identity
 * rather than as a state that had come on. The argument was sound and the
 * result was unusable: reported as colours "just too close for my eyes to tell
 * apart", and it was right. Muted blue against muted indigo, and muted green
 * against muted teal and muted olive, are four ways of saying grey-ish.
 *
 * So hue separation wins and the family resemblance goes. What remains of the
 * old reasoning is the part that still holds: THESE COLOURS ARE NEVER THE ONLY
 * THING CARRYING A MEANING. Every band is named in a legend and every node in
 * this interface is labelled, so a device sharing a hue with `--warn` costs a
 * moment's hesitation rather than a wrong reading -- while two devices no
 * reader can separate cost the chart its point.
 *
 * Ordered so that the first few, which is all most boxes will use, are the
 * furthest apart of the set.
 *
 * Honest limitation, unchanged: this set has not been through the contrast and
 * colour-vision checks that `--down` / `--up` documented in style.css. Red
 * against green in particular will fail for some readers, which is survivable
 * only because of the labelling above.
 */
const CLIENT_COLOURS = [
  '#4c9aff', // blue
  '#ffa726', // orange
  '#46d160', // green
  '#ff5c5c', // red
  '#b98cff', // violet
  '#24c9d8', // cyan
  '#ff7ad9', // pink
  '#d7d34a', // olive
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
