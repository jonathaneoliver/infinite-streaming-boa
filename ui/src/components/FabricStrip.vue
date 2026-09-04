<script setup lang="ts">
import { ref } from 'vue';
import type { BridgeInfo, IfaceInfo } from '@/types';

/**
 * The fabric: the WAN port and the bridge, on one line.
 *
 * These are not adapters and do not belong in the rack. An adapter carries some
 * clients; eth0 and br-lan carry all of them, so they answer "is the box
 * plugged in and bridged" rather than "what is this radio doing". That is a
 * question with a one-line answer nearly always, which is why it gets one line.
 *
 * The topology drawing sits behind a disclosure rather than on the page. It is
 * the best explanation of this box there is and it is worth the space exactly
 * once, when someone is asking how the thing is wired -- not on every visit to
 * a page whose actual content is the devices.
 */
defineProps<{ info: BridgeInfo | null }>();

const showTopology = ref(false);

function addr(i: IfaceInfo | undefined): string {
  if (!i) return '';
  return [...(i.ipv4 ?? []), ...(i.ipv6 ?? []).filter((a) => !a.startsWith('fe80'))].join(' · ');
}
function find(info: BridgeInfo | null, role: string): IfaceInfo | undefined {
  return info?.ifaces.find((i) => i.role === role);
}
function linkText(i: IfaceInfo | undefined): string {
  if (!i) return '—';
  if (!i.up) return 'down';
  if (!i.carrier_known) return 'up';
  return i.carrier ? 'up' : 'NO carrier';
}
</script>

<template>
  <section class="fabric">
    <div class="line">
      <!-- The same caret a client card and an adapter fold open with, in the
           same place, so everything on this page folds one way. It was a text
           button on the far right, which made the one collapsible thing at the
           top of the page behave unlike the two below it. -->
      <button
        class="caret" :aria-expanded="showTopology"
        :title="showTopology ? 'Hide the topology' : 'Show how the box is wired'"
        @click="showTopology = !showTopology"
      >{{ showTopology ? '▾' : '▸' }}</button>

      <span class="k">wan</span>
      <span class="v num">{{ find(info, 'wan')?.name ?? '—' }}</span>
      <span class="meta">{{ linkText(find(info, 'wan')) }}</span>
      <span
        v-if="find(info, 'wan')?.speed_mbps" class="meta num"
      >{{ find(info, 'wan')?.speed_mbps }} Mb/s</span>

      <span class="sep">→</span>

      <span class="k">bridge</span>
      <span class="v num">{{ find(info, 'bridge')?.name ?? '—' }}</span>
      <span class="meta num addr">{{ addr(find(info, 'bridge')) }}</span>

      <span class="spacer" />

      <span class="meta">topology</span>
    </div>

    <div v-if="showTopology" class="drawer">
      <slot name="topology" />
    </div>
  </section>
</template>

<style scoped>
.fabric {
  border: 1px solid var(--line-soft);
  border-radius: var(--r);
  background: var(--panel);
  margin-bottom: 10px;
}
.line {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  flex-wrap: wrap;
  font-size: 12px;
}
.k {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--ink-faint);
}
.v { color: var(--ink); font-family: var(--mono); font-size: 11px; }
.meta { color: var(--ink-faint); font-size: 11px; }
/* The addresses are the thing people actually copy, so they get the mono
   treatment and are allowed to truncate rather than wrap the row. */
.addr {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 46ch;
}
.sep { color: var(--ink-faint); }
.spacer { flex: 1; min-width: 0; }
/* The caret every fold on this page uses, at the same size and in the same
   place: leading the row, not trailing it. */
.caret {
  background: none;
  border: 0;
  color: var(--ink-faint);
  cursor: pointer;
  font-size: 13px;
  line-height: 1;
  padding: 3px 8px 3px 0;
}
.caret:hover { color: var(--ink); }
.drawer {
  border-top: 1px solid var(--line-soft);
  padding: 8px 10px;
}
</style>
