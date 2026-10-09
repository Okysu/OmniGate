<script setup lang="ts">
import type { PlazaPrice } from '@/lib/types'
import { computed } from 'vue'
import { Clock } from '@lucide/vue'
import { useIntervalFn, useNow } from '@vueuse/core'
import ScheduleHoverCard from '@/components/pricing/ScheduleHoverCard.vue'
import { browserTimezone } from '@/lib/notifications'
import { scheduleNote } from '@/lib/priceSchedule'

/**
 * Time-of-day price note (phase8 §3): "当前时段 ×0.5，08:30 恢复原价" while a period
 * applies, otherwise the period list. `badge` renders a compact chip (phone rows).
 * Hover / focus / tap shows the period table.
 */
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<{
  price: PlazaPrice | null
  variant?: 'line' | 'badge'
}>(), { variant: 'line' })

const now = useNow({ scheduler: cb => useIntervalFn(cb, 60_000) })
const note = computed(() => scheduleNote(props.price, now.value, browserTimezone()))
</script>

<template>
  <ScheduleHoverCard
    v-if="note && price?.schedule"
    :schedule="price.schedule"
    :timezone="price.scheduleTimezone"
    :lead="note.active ? note.text : null"
    footer="按请求开始时间计价，其余时段按原价"
    :align="variant === 'badge' ? 'end' : 'start'"
  >
    <span
      v-if="variant === 'badge'"
      class="focus-visible:ring-ring/50 relative z-10 inline-flex h-4 shrink-0 cursor-help items-center gap-0.5 rounded-sm px-1 text-[10px] font-medium whitespace-nowrap tabular-nums outline-none focus-visible:ring-3"
      :class="note.active ? 'bg-sky-500/15 text-sky-700 dark:text-sky-300' : 'bg-muted text-muted-foreground'"
      data-testid="schedule-note"
      v-bind="$attrs"
      :data-active="note.active"
    >
      <Clock class="size-2.5" aria-hidden="true" />{{ note.active ? `×${note.current}` : '分时' }}
    </span>
    <p
      v-else
      class="focus-visible:ring-ring/50 relative z-10 flex w-fit max-w-full cursor-help items-start gap-1 rounded-sm text-[11px] leading-snug outline-none focus-visible:ring-3"
      :class="note.active ? 'text-sky-700 dark:text-sky-400' : 'text-muted-foreground'"
      data-testid="schedule-note"
      v-bind="$attrs"
      :data-active="note.active"
    >
      <Clock class="mt-px size-3 shrink-0" aria-hidden="true" />
      <span class="min-w-0">{{ note.text }}</span>
    </p>
  </ScheduleHoverCard>
</template>
