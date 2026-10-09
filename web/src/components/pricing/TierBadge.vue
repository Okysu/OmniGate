<script setup lang="ts">
import type { PlazaPriceTier } from '@/lib/types'
import { computed } from 'vue'
import { Layers } from '@lucide/vue'
import TierHoverCard from './TierHoverCard.vue'
import { tierTable } from '@/lib/priceTiers'

/** "阶梯" badge of a price with context-length tiers; the hover card shows every tier. */
defineOptions({ inheritAttrs: false })
const props = defineProps<{
  /** Base (lowest-tier) token prices. */
  base: { inputPerM: string, outputPerM: string, cacheReadPerM?: string | null, cacheWritePerM?: string | null }
  /** Resolved tiers (`resolveTiers` for admin prices; the plaza sends them resolved). */
  tiers: PlazaPriceTier[]
  money: (amount: string) => string
}>()
const rows = computed(() => tierTable(props.base, props.tiers))
</script>

<template>
  <TierHoverCard :rows="rows" :money="money">
    <span
      class="focus-visible:ring-ring/50 relative z-10 inline-flex h-4 shrink-0 cursor-help items-center gap-0.5 rounded-4xl border border-indigo-500/50 px-1 font-sans text-[10px] leading-none font-normal whitespace-nowrap text-indigo-700 outline-none focus-visible:ring-3 data-[state=open]:bg-indigo-500/10 dark:text-indigo-300"
      data-testid="tier-badge"
      v-bind="$attrs"
      :data-tiers="tiers.length"
    >
      <Layers class="size-2.5" aria-hidden="true" />
      阶梯
    </span>
  </TierHoverCard>
</template>
