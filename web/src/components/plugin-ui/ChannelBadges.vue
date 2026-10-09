<script setup lang="ts">
// Resolved `channel.list.badge` contributions (server-side resolution, same formatter as the renderer).
import type { ChannelBadge } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { formatUiValue } from '@/lib/pluginFormat'

defineProps<{ badges: ChannelBadge[] }>()
</script>

<template>
  <span class="inline-flex flex-wrap gap-1">
    <Badge
      v-for="(b, i) in badges"
      :key="i"
      variant="secondary"
      class="font-normal tabular-nums"
      :title="formatUiValue(b.value, b.format, { currency: b.currency }).title"
    >
      {{ formatUiValue(b.value, b.format, { currency: b.currency }).text }}
    </Badge>
  </span>
</template>
