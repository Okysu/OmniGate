<script setup lang="ts">
import type { PricePart, PriceSummary } from '@/lib/billingMode'
import { computed } from 'vue'

/**
 * A price summary on one (wrapping) line: token prices as "输入 $2.50 · 输出 $10.00 / 1M",
 * other parts as "$0.04 / 次", joined with "+". `parts: 'extra'` lists the secondary
 * prices (cache, image input, audio tokens …) one per line instead.
 * `money` formats an amount (the caller decides precision and currency).
 */
const props = withDefaults(defineProps<{
  summary: PriceSummary
  money: (amount: string) => string
  parts?: 'main' | 'extra'
}>(), { parts: 'main' })

const tokens = computed(() => props.summary.main.filter(p => p.token))
const others = computed(() => props.summary.main.filter(p => !p.token))
const extra = computed<PricePart[]>(() => props.summary.extra)
</script>

<template>
  <span v-if="parts === 'main'" class="inline-flex flex-wrap items-baseline justify-end gap-x-1.5 tabular-nums" data-testid="price-summary" :data-mode="summary.mode">
    <span v-if="tokens.length" class="whitespace-nowrap">
      <template v-for="(p, i) in tokens" :key="p.key">
        <span v-if="i > 0" class="text-muted-foreground"> · </span>
        <span class="text-muted-foreground text-xs">{{ p.label }}</span>
        {{ money(p.amount) }}
      </template>
      <span class="text-muted-foreground text-xs"> / 1M</span>
    </span>
    <span v-for="(p, i) in others" :key="p.key" class="whitespace-nowrap" :data-part="p.key">
      <span v-if="i > 0 || tokens.length" class="text-muted-foreground">+ </span>
      {{ money(p.amount) }}<span class="text-muted-foreground text-xs"> / {{ p.unit }}</span>
    </span>
  </span>
  <span v-else class="inline-flex flex-col items-end text-xs leading-tight tabular-nums" data-testid="price-summary-extra">
    <span v-if="!extra.length" class="text-muted-foreground">—</span>
    <span v-for="p in extra" v-else :key="p.key" class="whitespace-nowrap">
      <span class="text-muted-foreground">{{ p.label }}</span>
      {{ money(p.amount) }}<span class="text-muted-foreground"> / {{ p.unit }}</span>
    </span>
  </span>
</template>
