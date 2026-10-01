import { ref, watch } from 'vue';

/**
 * Dark or light, per browser.
 *
 * Dark is the instrument boa has always been. Light follows the palette of
 * OpenWrt's default LuCI theme, so a box whose interface opens beside LuCI does
 * not look like a different product glued on. The choice is a view preference
 * like the chart range, so it lives in localStorage and travels in an exported
 * configuration (see useConfig's UI_KEYS).
 *
 * Applied as `data-theme` on <html>, where style.css swaps the tokens. The same
 * attribute is set by an inline script in index.html before the first paint,
 * so a light-mode reload does not flash dark first; this module only has to
 * keep it in step afterwards.
 */
export type Theme = 'dark' | 'light';

const KEY = 'boa.theme';

function load(): Theme {
  try {
    return localStorage.getItem(KEY) === 'light' ? 'light' : 'dark';
  } catch {
    return 'dark';
  }
}

/** Read by the chart palettes, so a series recolours with the page. */
export const theme = ref<Theme>(load());

watch(
  theme,
  (t) => {
    document.documentElement.dataset.theme = t;
    try {
      localStorage.setItem(KEY, t);
    } catch {
      // A private window, or a browser refusing storage. The page is still the
      // chosen colour; it just will not survive a reload.
    }
  },
  { immediate: true },
);

export function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark';
}
