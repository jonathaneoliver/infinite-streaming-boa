/**
 * Saving and restoring everything the operator has set up.
 *
 * Two halves that live in different places, which is the whole reason this
 * exists as one function rather than two buttons:
 *
 *   - The BOX holds the work: every device's conditioning, its sub-classes, the
 *     measured ladders, and any saved or merged pattern. That is what
 *     GET /api/config already returns and POST /api/config already accepts.
 *   - The BROWSER holds the view: chart range, y-axis rule, tall, the mean
 *     window, sort order and which cards are folded. Those are localStorage,
 *     per-browser, and the box has never heard of them.
 *
 * A file carrying only the first restores a box that behaves right and looks
 * wrong; only the second, the reverse. So the document carries both, under a
 * `ui` key the daemon ignores -- its decoder does not reject unknown fields --
 * which means the same file still works with scripts/config.sh and a file
 * written by that script still imports here. One format, two tools, neither
 * needing to know about the other.
 */

/** Every preference key the interface keeps. Adding one here is the whole cost
 *  of including it in a saved configuration. */
const UI_KEYS = ['pifi.sort', 'pifi.folded', 'pifi.chart', 'pifi.extras'] as const;

export interface ConfigDoc {
  version?: number;
  exported_at?: number;
  devices?: unknown[];
  patterns?: unknown[];
  ui?: Record<string, string>;
}

function readUIPrefs(): Record<string, string> {
  const out: Record<string, string> = {};
  for (const k of UI_KEYS) {
    try {
      const v = localStorage.getItem(k);
      if (v !== null) out[k] = v;
    } catch {
      /* a private window or blocked storage must not fail the export */
    }
  }
  return out;
}

function writeUIPrefs(ui: Record<string, string> | undefined) {
  if (!ui) return;
  for (const k of UI_KEYS) {
    // Only the keys this build knows. A document written by a newer version
    // may carry others, and writing them would leave settings this code cannot
    // clear and did not ask for.
    if (typeof ui[k] !== 'string') continue;
    try {
      localStorage.setItem(k, ui[k]);
    } catch {
      /* as above */
    }
  }
}

/** A filename that sorts by date and says which box it came from. */
function filename(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, '0');
  const stamp = `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
  return `pifi-${location.hostname.split('.')[0]}-${stamp}.json`;
}

export async function exportConfig(): Promise<string> {
  const r = await fetch('/api/config');
  if (!r.ok) throw new Error((await r.json()).error ?? `HTTP ${r.status}`);
  const doc: ConfigDoc = await r.json();
  doc.ui = readUIPrefs();

  const blob = new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename();
  a.click();
  // Revoked on the next turn of the loop: revoking immediately can race the
  // browser's own read of the blob in some builds.
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  return a.download;
}

/**
 * Imports a configuration file.
 *
 * MERGE only. The API also offers replace, which DELETES every device the
 * document does not mention -- scripts/config.sh makes that an explicit
 * argument and then asks for confirmation before doing it. A button that could
 * silently delete a device's measured ladder, an hour of real streaming each,
 * is not a button worth having; that operation stays where it has to be typed.
 */
export async function importConfig(file: File): Promise<{ devices: number; patterns: number }> {
  const text = await file.text();
  let doc: ConfigDoc;
  try {
    doc = JSON.parse(text);
  } catch (e) {
    throw new Error(`not valid JSON: ${(e as Error).message}`);
  }
  if (!doc || typeof doc !== 'object' || !Array.isArray(doc.devices)) {
    throw new Error('not a pifi configuration: no "devices" array');
  }
  const r = await fetch('/api/config?mode=merge', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(doc),
  });
  if (!r.ok) throw new Error((await r.json()).error ?? `HTTP ${r.status}`);
  // The box first: if it refused the document, the view should not have moved.
  writeUIPrefs(doc.ui);
  return { devices: doc.devices.length, patterns: (doc.patterns ?? []).length };
}
