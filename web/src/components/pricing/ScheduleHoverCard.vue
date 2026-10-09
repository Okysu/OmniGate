<script setup lang="ts">
import type { PriceSchedulePeriod } from '@/lib/types'
import { computed } from 'vue'
import { Clock } from '@lucide/vue'
import InfoHoverCard from '@/components/InfoHoverCard.vue'
import { DEFAULT_TIMEZONE } from '@/lib/notifications'
import { schedulePeriodRows } from '@/lib/priceSchedule'

/**
 * Hover card listing a price's time-of-day periods (phase8 §3): "分时价格 · Asia/Shanghai",
 * one row per period — days, time range, discount ("5 折" ×0.5) — and "其余时段按原价".
 * The `trigger` slot is the anchor element.
 */
const props = withDefaults(defineProps<{
  schedule: PriceSchedulePeriod[]
  timezone?: string | null
  /** Extra line above the periods, e.g. "当前时段 ×0.5，08:30 恢复原价". */
  lead?: string | null
  footer?: string
  side?: 'top' | 'bottom'
  align?: 'start' | 'center' | 'end'
}>(), { timezone: null, lead: null, footer: '其余时段按原价', side: 'top', align: 'center' })

const rows = computed(() => schedulePeriodRows(props.schedule))
const TONE: Record<string, string> = {
  free: 'text-emerald-700 dark:text-emerald-400',
  discount: 'text-emerald-700 dark:text-emerald-400',
  markup: 'text-amber-700 dark:text-amber-400',
  list: 'text-foreground',
}
</script>

<template>
  <InfoHoverCard title="分时价格" :subtitle="timezone || DEFAULT_TIMEZONE" :footer="footer" :side="side" :align="align" testid="schedule-card">
    <template #icon>
      <Clock class="size-3.5 shrink-0 text-sky-600 dark:text-sky-400" aria-hidden="true" />
    </template>
    <template #trigger>
      <slot />
    </template>
    <p v-if="lead" class="mb-1.5 font-medium text-sky-700 dark:text-sky-400" data-testid="schedule-card-lead">
      {{ lead }}
    </p>
    <table class="w-full border-collapse tabular-nums" data-testid="schedule-badge-periods">
      <caption class="sr-only">
        分时价格时段
      </caption>
      <tbody>
        <tr v-for="(r, i) in rows" :key="i" class="align-baseline" data-testid="schedule-period">
          <th scope="row" class="py-0.5 pr-4 text-left font-normal whitespace-nowrap" data-col="days">
            {{ r.days }}
          </th>
          <td class="text-muted-foreground py-0.5 pr-4 whitespace-nowrap" data-col="time">
            {{ r.time }}
          </td>
          <td class="py-0.5 text-right font-medium whitespace-nowrap" :class="TONE[r.tone]" data-col="discount">
            {{ r.discount }}
          </td>
          <td class="text-muted-foreground py-0.5 pl-1.5 text-right whitespace-nowrap" data-col="multiplier">
            <template v-if="r.tone === 'discount'">
              {{ r.multiplier }}
            </template>
          </td>
        </tr>
      </tbody>
    </table>
  </InfoHoverCard>
</template>
