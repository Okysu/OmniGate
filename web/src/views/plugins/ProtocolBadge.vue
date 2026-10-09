<script setup lang="ts">
// phase9 §2: "自定义协议" vs "继承 OpenAI / Anthropic"; billing-only plugins show "计费插件".
import type { ProtocolSource } from '@/lib/pluginProtocol'
import { computed } from 'vue'
import { Badge } from '@/components/ui/badge'
import { protocolInfo } from '@/lib/pluginProtocol'

const props = defineProps<{ source: ProtocolSource | null | undefined }>()
const info = computed(() => protocolInfo(props.source))

const CLASSES: Record<string, string> = {
  custom: 'border-violet-500/50 bg-violet-500/10 text-violet-800 dark:text-violet-300',
  billing: 'border-sky-500/50 bg-sky-500/10 text-sky-800 dark:text-sky-300',
}
</script>

<template>
  <span v-if="info.kind === 'unknown' && info.label === '—'" class="text-muted-foreground">—</span>
  <Badge v-else variant="outline" class="font-normal" :class="CLASSES[info.kind]" :title="info.description || undefined" :data-protocol="info.kind">
    {{ info.label }}
  </Badge>
</template>
