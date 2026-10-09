<script setup lang="ts">
import type { NotificationSeverity } from '@/lib/types'
import { computed } from 'vue'
import { Info, OctagonAlert, TriangleAlert } from '@lucide/vue'
import { SEVERITY_LABELS } from '@/lib/notifications'

const props = withDefaults(defineProps<{ severity: NotificationSeverity, size?: 'sm' | 'md' }>(), { size: 'md' })

const STYLES: Record<NotificationSeverity, string> = {
  info: 'bg-sky-500/10 text-sky-700 dark:bg-sky-400/15 dark:text-sky-300',
  warning: 'bg-amber-500/15 text-amber-700 dark:bg-amber-400/15 dark:text-amber-300',
  critical: 'bg-destructive/10 text-destructive dark:bg-destructive/20',
}
const icon = computed(() => (props.severity === 'critical' ? OctagonAlert : props.severity === 'warning' ? TriangleAlert : Info))
</script>

<template>
  <span
    class="flex shrink-0 items-center justify-center rounded-full"
    :class="[STYLES[severity], size === 'sm' ? 'size-6' : 'size-8']"
    :title="SEVERITY_LABELS[severity]"
    :data-severity="severity"
  >
    <component :is="icon" :class="size === 'sm' ? 'size-3.5' : 'size-4'" aria-hidden="true" />
    <span class="sr-only">{{ SEVERITY_LABELS[severity] }}</span>
  </span>
</template>
