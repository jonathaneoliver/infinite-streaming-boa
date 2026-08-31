import { ref, watch } from 'vue';

/**
 * Whether the second-tier impairments are on show.
 *
 * ONE switch for the whole page, deliberately. The sliders author a keyframe
 * and the timeline draws it, so a device card and its pattern editor are two
 * views of the same values -- and a reorder slider with no reorder lane beside
 * it is a control whose effect is invisible in the panel built to show it.
 * Splitting the setting in two would let exactly that happen, and the operator
 * would have to find both switches to explain it.
 *
 * Module scope rather than a prop threaded through App -> card -> panel: this
 * is genuinely one piece of shared state, and passing it down three levels
 * would make every component on the path restate a setting it does not use.
 *
 * # What this switch cannot do
 *
 * It cannot hide an impairment that is in force. Both consumers OR it with
 * "something here is non-zero", so switching off tidies away what is empty and
 * nothing else. That is the whole reason the setting is safe to have: a hidden
 * control that is doing something is the failure this design exists to avoid,
 * and a preference that could cause it would reintroduce it by the back door.
 */
const KEY = 'pifi.extras';

function load(): boolean {
  try {
    return localStorage.getItem(KEY) === '1';
  } catch {
    // Private windows and blocked storage must not break the page.
    return false;
  }
}

const show = ref(load());

watch(show, (v) => {
  try {
    localStorage.setItem(KEY, v ? '1' : '0');
  } catch {
    /* not worth breaking a control over */
  }
});

export function useExtras() {
  return { show, toggle: () => (show.value = !show.value) };
}
