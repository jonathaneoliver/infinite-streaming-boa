<script setup lang="ts">
import { computed } from 'vue';
import { adapterChannel, adapterColour, revealAdapter } from '@/composables/useAdapters';

/**
 * One adapter, named the same way everywhere.
 *
 * The same token appears on the rack fold, on every client row and in the
 * activity log, which is the whole point: "wlan-usb" in three places with three
 * different treatments is three things the reader has to connect for
 * themselves. Swatch, name, channel — the swatch carries identity at a glance
 * and the name carries it unambiguously, because colour alone is not a label.
 *
 * The channel is part of the identity rather than decoration. A radio moved
 * from 149 to 40 is the same interface and a different link, and a token that
 * said only "wlan-usb" would make those look identical.
 */
const props = defineProps<{
  name: string;
  /** Offer the jump to this adapter's fold. Off in the rack header itself,
   *  which is the thing being jumped TO. */
  jump?: boolean;
  /** Suppress the channel, for contexts too narrow to carry it. */
  bare?: boolean;
  /**
   * Heading scale, for the rack fold that this token titles.
   *
   * The same token at a different weight, not a different component. A rack
   * fold is a peer of a client card, so its header has to carry the same
   * visual weight as one -- at body scale the adapters read as a strip of
   * annotations above the real content, which is backwards: they are the thing
   * every client below is attached to.
   *
   * Mono at heading size rather than the sans a client name uses, because an
   * interface name is an identifier and a device label is a name someone chose.
   */
  head?: boolean;
}>();

const colour = computed(() => adapterColour(props.name));
const channel = computed(() => adapterChannel(props.name));
</script>

<template>
  <span class="tok" :class="{ head }" :style="{ '--tok': colour }">
    <span class="swatch" aria-hidden="true" />
    <span class="name">{{ name }}</span>
    <span v-if="!bare && channel" class="ch">ch {{ channel }}</span>
    <!-- A button rather than an anchor: it opens the fold as well as scrolling
         to it, and a link that also mutates state is a link that lies about
         what it does. -->
    <button
      v-if="jump"
      class="jump"
      :title="`Show ${name} in the adapter rack`"
      @click.stop="revealAdapter(name)"
    >↑</button>
  </span>
</template>

<style scoped>
.tok {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-family: var(--mono);
  font-size: 11px;
  color: var(--ink-dim);
  white-space: nowrap;
}
/* A bar rather than a dot: at 11px a dot is a full stop, and this has to be
   readable as a colour rather than as punctuation. */
.swatch {
  width: 3px;
  height: 12px;
  border-radius: 2px;
  background: var(--tok);
  flex: none;
}
.name { color: var(--ink); }
.ch { color: var(--ink-faint); }
.jump {
  background: none;
  border: 0;
  padding: 0 2px;
  color: var(--ink-faint);
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1;
  cursor: pointer;
}
.jump:hover { color: var(--tok); }

/* Heading scale, matching a client card's own head: 15px/600 and a swatch tall
   enough to read as a colour rather than a tick. The rack folds are peers of
   those cards and have to look like it. */
.tok.head { gap: 8px; font-size: 15px; }
.tok.head .swatch { width: 4px; height: 18px; border-radius: 2px; }
.tok.head .name { font-weight: 600; }
.tok.head .ch { font-size: 12px; }
</style>
