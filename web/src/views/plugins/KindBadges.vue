<script setup lang="ts">
// phase9 §3: a plugin that also provides plan meters gets a "计费" badge.
import { computed } from 'vue'
import { Badge } from '@/components/ui/badge'
import { isBillingPlugin, isChannelPlugin } from '@/lib/customMeters'

const props = defineProps<{ source: { kind?: unknown } | null | undefined }>()
/** Only shown for channel plugins that are also billing plugins (billing-only ones use the protocol badge). */
const show = computed(() => isBillingPlugin(props.source) && isChannelPlugin(props.source))
</script>

<template>
  <Badge v-if="show" variant="outline" class="border-sky-500/50 bg-sky-500/10 font-normal text-sky-800 dark:text-sky-300" title="提供套餐计量（计费插件）">
    计费
  </Badge>
</template>
