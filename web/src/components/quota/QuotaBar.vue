<script setup lang="ts">
import type { UsageLevel } from '@/lib/quota'
import { computed } from 'vue'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{
  /** used / limit (values above 1 are clamped). */
  ratio: number
  level?: UsageLevel
  /** Accessible name, e.g. the rule title. */
  label?: string
  size?: 'sm' | 'md'
  class?: string
}>(), { level: 'ok', label: undefined, size: 'md', class: undefined })

const pct = computed(() => Math.round(Math.min(1, Math.max(0, props.ratio)) * 1000) / 10)
const fill = computed(() => {
  if (props.level === 'exceeded')
    return 'bg-destructive'
  if (props.level === 'warn')
    return 'bg-amber-500 dark:bg-amber-400'
  return 'bg-primary'
})
</script>

<template>
  <div
    role="progressbar"
    :aria-label="label"
    aria-valuemin="0"
    aria-valuemax="100"
    :aria-valuenow="pct"
    :class="cn('bg-muted w-full overflow-hidden rounded-full', size === 'sm' ? 'h-1.5' : 'h-2', props.class)"
  >
    <div class="h-full rounded-full transition-[width] duration-300" :class="fill" :style="{ width: `${pct}%` }" />
  </div>
</template>
