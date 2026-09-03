import { onMounted, onUnmounted, ref } from 'vue';

/**
 * The window's width, as a shared reactive value.
 *
 * Module scope, so every card reads the same ref and there is ONE resize
 * listener on the page rather than one per device. A list of twenty clients
 * each attaching its own listener is twenty callbacks per resize frame, and
 * they would all compute the same number.
 *
 * This exists because the folded row's columns are built in script -- they
 * depend on which directions are shown and on whether the card is open -- so a
 * CSS media query cannot reach them. The breakpoints below are therefore in
 * JavaScript, unusually, and deliberately.
 */
const width = ref(typeof window === 'undefined' ? 1920 : window.innerWidth);

let listeners = 0;
let raf = 0;

function onResize() {
  // Coalesced to one measurement per frame: a drag-resize fires this
  // continuously, and rebuilding every card's grid template per event makes the
  // page crawl exactly when someone is trying to see how it reflows.
  if (raf) return;
  raf = requestAnimationFrame(() => {
    raf = 0;
    width.value = window.innerWidth;
  });
}

export function useViewportWidth() {
  onMounted(() => {
    if (listeners++ === 0) window.addEventListener('resize', onResize);
    width.value = window.innerWidth;
  });
  onUnmounted(() => {
    if (--listeners === 0) {
      window.removeEventListener('resize', onResize);
      if (raf) cancelAnimationFrame(raf);
      raf = 0;
    }
  });
  return width;
}
