<script setup lang="ts">
import type { PlazaPrice } from '@/lib/types'
import { computed } from 'vue'
import { Layers } from '@lucide/vue'
import TierHoverCard from '@/components/pricing/TierHoverCard.vue'
import { formatTokenThreshold, tierTable } from '@/lib/priceTiers'

/**
 * Plaza note for a price with context-length tiers: ">272K 输入 $20.000 · 输出 $75.000"
 * (`line`) or a compact "阶梯" chip (`badge`, phone rows); hover / focus / tap shows every tier.
 */
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<{
  price: PlazaPrice | null
  money: (amount: string) => string
  variant?: 'line' | 'badge'
}>(), { variant: 'line' })

const tiers = computed(() => props.price?.tiers ?? [])
const rows = computed(() => (props.price ? tierTable(props.price, tiers.value) : []))
const text = computed(() => {
  const t = tiers.value[0]
  if (!t)
    return ''
  const more = tiers.value.length > 1 ? ` 等 ${tiers.value.length} 档` : ''
  return `长上下文 >${formatTokenThreshold(t.aboveInputTokens)}：输入 ${props.money(t.inputPerM)} · 输出 ${props.money(t.outputPerM)}${more}`
})
</script>

<template>
  <TierHoverCard v-if="tiers.length" :rows="rows" :money="money" :align="variant === 'badge' ? 'end' : 'start'">
    <span
      v-if="variant === 'badge'"
      class="focus-visible:ring-ring/50 relative z-10 inline-flex h-4 shrink-0 cursor-help items-center gap-0.5 rounded-sm bg-indigo-500/10 px-1 text-[10px] font-medium whitespace-nowrap text-indigo-700 outline-none focus-visible:ring-3 dark:text-indigo-300"
      data-testid="tier-note"
      v-bind="$attrs"
    >
      <Layers class="size-2.5" aria-hidden="true" />阶梯
    </span>
    <p
      v-else
      class="focus-visible:ring-ring/50 relative z-10 flex w-fit max-w-full cursor-help items-start gap-1 rounded-sm text-[11px] leading-snug text-indigo-700 tabular-nums outline-none focus-visible:ring-3 dark:text-indigo-300"
      data-testid="tier-note"
      v-bind="$attrs"
    >
      <Layers class="mt-px size-3 shrink-0" aria-hidden="true" />
      <span class="min-w-0">{{ text }}</span>
    </p>
  </TierHoverCard>
</template>
