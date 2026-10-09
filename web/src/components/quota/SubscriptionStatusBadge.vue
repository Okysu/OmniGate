<script setup lang="ts">
import { Badge } from '@/components/ui/badge'
import { SUBSCRIPTION_STATUS_LABELS } from '@/lib/quota'

defineProps<{ status: string }>()
</script>

<template>
  <Badge
    variant="outline"
    :class="status === 'active'
      ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400'
      : status === 'cancelled' ? 'border-destructive/40 text-destructive' : 'text-muted-foreground'"
  >
    {{ SUBSCRIPTION_STATUS_LABELS[status] ?? status }}
  </Badge>
</template>
