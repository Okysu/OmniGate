<script setup lang="ts">
import { computed } from 'vue'
import { Layers } from '@lucide/vue'
import InfoHoverCard from '@/components/InfoHoverCard.vue'
import { formatTokenThreshold, tierLabel } from '@/lib/priceTiers'

/**
 * Request log: the sell-price context-length tier the request was charged at (phase10 §1).
 * Compact in the (wide) log table: ">272K" with the tier icon; the hover card and the
 * expanded row spell out "长上下文档 >272K".
 */
defineOptions({ inheritAttrs: false })
const props = defineProps<{ above: number }>()
const t = computed(() => formatTokenThreshold(props.above))
</script>

<template>
  <InfoHoverCard :title="tierLabel(above)" testid="log-tier-card" align="end">
    <template #icon>
      <Layers class="size-3.5 shrink-0 text-indigo-600 dark:text-indigo-400" aria-hidden="true" />
    </template>
    <template #trigger>
      <span
        class="focus-visible:ring-ring/50 inline-flex h-4 shrink-0 cursor-help items-center gap-0.5 rounded-4xl border border-indigo-500/50 px-1 align-middle font-sans text-[10px] leading-none font-normal whitespace-nowrap text-indigo-700 outline-none focus-visible:ring-3 data-[state=open]:bg-indigo-500/10 dark:text-indigo-300"
        data-testid="price-tier"
        :data-above="above"
        v-bind="$attrs"
      >
        <Layers class="size-2.5" aria-hidden="true" /><span class="sr-only">长上下文档 </span>&gt;{{ t }}
      </span>
    </template>
    <p class="max-w-64 leading-relaxed">
      输入 + 缓存 token 超过 {{ t }}，整次请求的全部 token 按长上下文单价计费。
    </p>
  </InfoHoverCard>
</template>
