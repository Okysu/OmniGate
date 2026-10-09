<script setup lang="ts">
import type { PlazaPrice } from '@/lib/types'
import { computed } from 'vue'
import { Clock } from '@lucide/vue'
import { useIntervalFn, useNow } from '@vueuse/core'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { browserTimezone } from '@/lib/notifications'
import { scheduleNote } from '@/lib/priceSchedule'

/**
 * Time-of-day price note (phase8 §3): "当前时段 ×0.5，08:30 恢复原价" while a period
 * applies, otherwise the period list. `badge` renders a compact chip (phone rows).
 */
const props = withDefaults(defineProps<{
  price: PlazaPrice | null
  variant?: 'line' | 'badge'
}>(), { variant: 'line' })

const now = useNow({ scheduler: cb => useIntervalFn(cb, 60_000) })
const note = computed(() => scheduleNote(props.price, now.value, browserTimezone()))
</script>

<template>
  <template v-if="note">
    <Tooltip>
      <TooltipTrigger as-child>
        <span
          v-if="variant === 'badge'"
          class="relative z-10 inline-flex h-4 shrink-0 cursor-help items-center gap-0.5 rounded-sm px-1 text-[10px] font-medium tabular-nums"
          :class="note.active ? 'bg-sky-500/15 text-sky-700 dark:text-sky-300' : 'bg-muted text-muted-foreground'"
          tabindex="0"
          data-testid="schedule-note"
          :data-active="note.active"
        >
          <Clock class="size-2.5" />{{ note.active ? `×${note.current}` : '分时' }}
        </span>
        <p
          v-else
          class="relative z-10 flex w-fit max-w-full cursor-help items-start gap-1 text-[11px] leading-snug"
          :class="note.active ? 'text-sky-700 dark:text-sky-400' : 'text-muted-foreground'"
          tabindex="0"
          data-testid="schedule-note"
          :data-active="note.active"
        >
          <Clock class="mt-px size-3 shrink-0" />
          <span class="min-w-0">{{ note.text }}</span>
        </p>
      </TooltipTrigger>
      <TooltipContent class="max-w-80">
        <p class="font-medium">
          分时价格（{{ price?.scheduleTimezone || 'Asia/Shanghai' }}）
        </p>
        <ul class="mt-0.5 space-y-0.5 tabular-nums">
          <li v-for="(l, i) in note.periods" :key="i">
            {{ l }}
          </li>
        </ul>
        <p class="mt-0.5 opacity-80">
          按请求开始时间计价，其余时段按原价。
        </p>
      </TooltipContent>
    </Tooltip>
  </template>
</template>
