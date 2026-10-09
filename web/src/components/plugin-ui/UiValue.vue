<script setup lang="ts">
import { computed } from 'vue'
import { Badge } from '@/components/ui/badge'
import { formatUiValue } from '@/lib/pluginFormat'

const props = defineProps<{
  value: unknown
  format?: string | null
  currency?: string | null
}>()

const f = computed(() => formatUiValue(props.value, props.format, { currency: props.currency }))
</script>

<template>
  <Badge
    v-if="f.bool !== undefined"
    variant="outline"
    :class="f.bool ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400' : 'text-muted-foreground'"
  >
    {{ f.text }}
  </Badge>
  <span v-else :class="f.missing ? 'text-muted-foreground' : ''" :title="f.title">{{ f.text }}</span>
</template>
