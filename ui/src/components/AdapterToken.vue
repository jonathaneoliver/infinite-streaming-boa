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
}>();

const colour = computed(() => adapterColour(props.name));
const channel = computed(() => adapterChannel(props.name));
</script>

<template>
  <span class="tok" :style="{ '--tok': colour }">
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
</style>
