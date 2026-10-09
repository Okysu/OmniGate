<script setup lang="ts">
import type { PluginSource } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { PLUGIN_SOURCE_DESCRIPTIONS, PLUGIN_SOURCE_LABELS } from '@/lib/labels'

defineProps<{ source: PluginSource | string }>()
</script>

<template>
  <Badge :variant="source === 'builtin' ? 'secondary' : 'outline'" :title="PLUGIN_SOURCE_DESCRIPTIONS[source as PluginSource]">
    {{ PLUGIN_SOURCE_LABELS[source as PluginSource] ?? source }}
  </Badge>
</template>
