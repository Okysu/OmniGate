<script setup lang="ts">
import type { PriceSchedulePeriod } from '@/lib/types'
import { computed } from 'vue'
import { Clock } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { describePeriod } from '@/lib/priceSchedule'
import { DEFAULT_TIMEZONE } from '@/lib/notifications'

/** "分时" badge of a price with a time-of-day schedule; the tooltip lists the periods. */
const props = defineProps<{
  schedule: PriceSchedulePeriod[]
  timezone?: string | null
  /** Extra first line, e.g. the multiplier in effect now. */
  lead?: string | null
}>()
const lines = computed(() => props.schedule.map(describePeriod))
</script>

<template>
  <Tooltip>
    <TooltipTrigger as-child>
      <Badge variant="outline" class="relative z-10 h-4 cursor-help gap-0.5 border-sky-500/50 px-1 text-[10px] font-normal text-sky-700 dark:text-sky-400" tabindex="0" data-testid="schedule-badge">
        <Clock class="size-2.5" />
        分时
      </Badge>
    </TooltipTrigger>
    <TooltipContent class="max-w-80">
      <p v-if="lead" class="font-medium">
        {{ lead }}
      </p>
      <p class="opacity-80">
        分时价格（{{ timezone || DEFAULT_TIMEZONE }}）
      </p>
      <ul class="mt-0.5 space-y-0.5 tabular-nums" data-testid="schedule-badge-periods">
        <li v-for="(l, i) in lines" :key="i">
          {{ l }}
        </li>
      </ul>
      <p class="mt-0.5 opacity-80">
        其余时段按原价
      </p>
    </TooltipContent>
  </Tooltip>
</template>
