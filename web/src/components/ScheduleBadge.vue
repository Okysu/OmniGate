<script setup lang="ts">
import type { PriceSchedulePeriod } from '@/lib/types'
import { Clock } from '@lucide/vue'
import ScheduleHoverCard from '@/components/pricing/ScheduleHoverCard.vue'

/** "分时" badge of a price with a time-of-day schedule; the hover card lists the periods. */
defineOptions({ inheritAttrs: false })
defineProps<{
  schedule: PriceSchedulePeriod[]
  timezone?: string | null
  /** Extra first line, e.g. the multiplier in effect now. */
  lead?: string | null
}>()
</script>

<template>
  <ScheduleHoverCard :schedule="schedule" :timezone="timezone" :lead="lead">
    <span
      class="focus-visible:ring-ring/50 relative z-10 inline-flex h-4 shrink-0 cursor-help items-center gap-0.5 rounded-4xl border border-sky-500/50 px-1 text-[10px] leading-none font-normal whitespace-nowrap text-sky-700 outline-none focus-visible:ring-3 data-[state=open]:bg-sky-500/10 dark:text-sky-400"
      data-testid="schedule-badge"
      v-bind="$attrs"
    >
      <Clock class="size-2.5" aria-hidden="true" />
      分时
    </span>
  </ScheduleHoverCard>
</template>
