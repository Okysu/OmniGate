<script setup lang="ts">
import type { TierTableRow } from '@/lib/priceTiers'
import { computed } from 'vue'
import { Layers } from '@lucide/vue'
import InfoHoverCard from '@/components/InfoHoverCard.vue'

/**
 * Hover card of a price's context-length tiers (phase10 §1): one row per tier ("≤272K",
 * ">272K") with its per-1M token prices. Cache columns only when some tier charges them.
 * The `trigger` slot is the anchor element.
 */
const props = withDefaults(defineProps<{
  rows: TierTableRow[]
  money: (amount: string) => string
  side?: 'top' | 'bottom'
  align?: 'start' | 'center' | 'end'
}>(), { side: 'top', align: 'center' })

const showCacheRead = computed(() => props.rows.some(r => r.cacheReadPerM != null))
const showCacheWrite = computed(() => props.rows.some(r => r.cacheWritePerM != null))
</script>

<template>
  <InfoHoverCard
    title="阶梯价格"
    subtitle="按上下文长度 · 每 1M token"
    footer="按输入 + 缓存 token 总数选档；超过阈值时，整次请求的全部 token 按该档单价计费。"
    :side="side"
    :align="align"
    content-class="max-w-[min(26rem,calc(100vw-1.5rem))]"
    testid="tier-card"
  >
    <template #icon>
      <Layers class="size-3.5 shrink-0 text-indigo-600 dark:text-indigo-400" aria-hidden="true" />
    </template>
    <template #trigger>
      <slot />
    </template>
    <table class="w-full border-collapse tabular-nums" data-testid="tier-table">
      <thead>
        <tr class="text-muted-foreground text-[11px]">
          <th scope="col" class="pb-1 pr-4 text-left font-normal">
            输入 + 缓存
          </th>
          <th scope="col" class="pb-1 pl-3 text-right font-normal">
            输入
          </th>
          <th scope="col" class="pb-1 pl-3 text-right font-normal">
            输出
          </th>
          <th v-if="showCacheRead" scope="col" class="pb-1 pl-3 text-right font-normal">
            缓存读
          </th>
          <th v-if="showCacheWrite" scope="col" class="pb-1 pl-3 text-right font-normal">
            缓存写
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="r in rows" :key="r.label" class="border-t" data-testid="tier-row" :data-above="r.above ?? 'base'">
          <th scope="row" class="py-1 pr-4 text-left font-medium whitespace-nowrap">
            {{ r.label }}
          </th>
          <td class="py-1 pl-3 text-right whitespace-nowrap">
            {{ money(r.inputPerM) }}
          </td>
          <td class="py-1 pl-3 text-right whitespace-nowrap">
            {{ money(r.outputPerM) }}
          </td>
          <td v-if="showCacheRead" class="py-1 pl-3 text-right whitespace-nowrap" :class="r.cacheReadPerM == null ? 'text-muted-foreground' : ''">
            {{ r.cacheReadPerM == null ? '—' : money(r.cacheReadPerM) }}
          </td>
          <td v-if="showCacheWrite" class="py-1 pl-3 text-right whitespace-nowrap" :class="r.cacheWritePerM == null ? 'text-muted-foreground' : ''">
            {{ r.cacheWritePerM == null ? '—' : money(r.cacheWritePerM) }}
          </td>
        </tr>
      </tbody>
    </table>
  </InfoHoverCard>
</template>
