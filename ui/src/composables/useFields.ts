import { ref, watch } from 'vue';

/**
 * Which fields the operator has revealed even though they are currently zero.
 *
 * Shared between the card sliders and the pattern lanes and persisted, so a
 * field revealed in one is revealed in the other -- the two show/hide the same
 * way. A field with a non-zero value (or, for a link lane, an event) shows
 * regardless; this set governs only the empty ones, which is why an impairment
 * in force can never be hidden.
 *
 * Keys are the shape field names (`delay_ms`, `reorder_pct`, …) and the link
 * kinds (`drop`, `nudge`, `deadzone`). `rate_mbps` is deliberately absent: rate
 * is always shown, so it is never in this set.
 */
const KEY = 'boa.fields';

function load(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || '[]');
    return Array.isArray(v) ? v : [];
  } catch {
    // Private windows and blocked storage must not break the page.
    return [];
  }
}

// Module scope, so every component that calls useFields() shares one set.
const revealed = ref<Set<string>>(new Set(load()));

watch(
  revealed,
  (s) => {
    try {
      localStorage.setItem(KEY, JSON.stringify([...s]));
    } catch {
      /* not worth breaking a control over */
    }
  },
  { deep: true },
);

export function useFields() {
  return {
    revealed,
    reveal: (k: string) => revealed.value.add(k),
    hide: (k: string) => revealed.value.delete(k),
  };
}
