<script setup lang="ts">
import type { PluginApproval } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { APPROVAL_LABELS } from '@/lib/labels'

defineProps<{ approval: PluginApproval | string }>()

const CLASSES: Record<string, string> = {
  pending: 'border-amber-500/50 bg-amber-500/10 text-amber-800 dark:text-amber-300',
  approved: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  rejected: 'text-muted-foreground line-through decoration-muted-foreground/50',
}
</script>

<template>
  <Badge variant="outline" :class="CLASSES[approval]">
    <slot>{{ APPROVAL_LABELS[approval as PluginApproval] ?? approval }}</slot>
  </Badge>
</template>
