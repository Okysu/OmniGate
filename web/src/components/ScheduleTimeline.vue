<script setup lang="ts">
import type { PriceSchedulePeriod } from '@/lib/types'
import { computed } from 'vue'
import { DAY_CHARS, DAY_MINUTES, formatTime, timelineByDay, WEEKDAYS } from '@/lib/priceSchedule'
import { isValidAmount, toNano } from '@/lib/money'

/** 24h preview bar per weekday (Sunday first, like the chips) for a price schedule. */
const props = defineProps<{
  periods: Pick<PriceSchedulePeriod, 'days' | 'start' | 'end' | 'multiplier'>[]
  /** Highlights the segments of this period index. */
  highlight?: number | null
}>()

const days = computed(() => timelineByDay(props.periods))
const TICKS = [0, 6, 12, 18, 24]

function tone(m: string): string {
  if (!isValidAmount(m))
    return 'bg-muted-foreground/40'
  const n = toNano(m)
  if (n < 1_000_000_000n)
    return 'bg-emerald-500/70 dark:bg-emerald-400/60 text-white dark:text-emerald-950'
  if (n > 1_000_000_000n)
    return 'bg-amber-500/80 dark:bg-amber-400/70 text-white dark:text-amber-950'
  return 'bg-muted-foreground/40'
}
const pct = (min: number) => `${(min / DAY_MINUTES) * 100}%`
</script>

<template>
  <div class="space-y-1" data-testid="schedule-timeline" aria-label="分时价格预览（每天 24 小时）">
    <div v-for="d in WEEKDAYS" :key="d" class="flex items-center gap-2">
      <span class="text-muted-foreground w-4 shrink-0 text-center text-[11px]">{{ DAY_CHARS[d] }}</span>
      <div class="bg-muted relative h-4 min-w-0 flex-1 overflow-hidden rounded-sm" :data-day="d">
        <div
          v-for="(seg, i) in days[d]"
          :key="i"
          class="absolute inset-y-0 flex items-center justify-center overflow-hidden text-[10px] leading-none font-medium tabular-nums"
          :class="[tone(seg.multiplier), highlight != null && highlight !== seg.index ? 'opacity-40' : '']"
          :style="{ left: pct(seg.from), width: pct(seg.to - seg.from) }"
          :title="`时段 ${seg.index + 1}：${formatTime(seg.from)}–${formatTime(seg.to)} ×${seg.multiplier}`"
          data-testid="timeline-segment"
        >
          <span v-if="seg.to - seg.from >= 150" class="truncate px-0.5">×{{ seg.multiplier }}</span>
        </div>
      </div>
    </div>
    <div class="text-muted-foreground relative ml-6 h-3 text-[10px] tabular-nums" aria-hidden="true">
      <span
        v-for="t in TICKS"
        :key="t"
        class="absolute"
        :class="t === 0 ? '' : t === 24 ? '-translate-x-full' : '-translate-x-1/2'"
        :style="{ left: `${(t / 24) * 100}%` }"
      >{{ String(t).padStart(2, '0') }}:00</span>
    </div>
  </div>
</template>
