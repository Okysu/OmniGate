<script setup lang="ts">
import type { SettingSource } from '@/lib/types'
import { computed } from 'vue'
import { Loader2, RotateCcw } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { SOURCE_DESCRIPTIONS, SOURCE_LABELS } from '@/lib/settingsForm'

/** One setting: label, value source badge, "恢复默认" and inline error / hint. */
const props = defineProps<{
  label: string
  for?: string
  source: SettingSource | null
  hint?: string
  error?: string | null
  /** A reset request for this field is in flight. */
  resetting?: boolean
  /** Disable the reset action (e.g. while another save runs). */
  busy?: boolean
}>()
defineEmits<{ reset: [] }>()

const SOURCE_CLASSES: Record<SettingSource, string> = {
  db: 'border-sky-500/40 text-sky-700 dark:text-sky-400',
  env: 'border-amber-500/40 text-amber-700 dark:text-amber-400',
  default: 'text-muted-foreground',
}
const canReset = computed(() => props.source === 'db')
</script>

<template>
  <div class="space-y-1.5">
    <div class="flex min-h-6 flex-wrap items-center gap-2">
      <Label :for="$props.for">{{ label }}</Label>
      <Tooltip v-if="source">
        <TooltipTrigger as-child>
          <Badge variant="outline" class="h-4 px-1.5 text-[10px] font-normal" :class="SOURCE_CLASSES[source]" tabindex="0" data-testid="source-badge">
            {{ SOURCE_LABELS[source] }}
          </Badge>
        </TooltipTrigger>
        <TooltipContent class="max-w-64">
          {{ SOURCE_DESCRIPTIONS[source] }}
        </TooltipContent>
      </Tooltip>
      <Button
        v-if="canReset"
        type="button"
        variant="ghost"
        size="xs"
        class="text-muted-foreground ml-auto h-6"
        :disabled="resetting || busy"
        :title="`删除数据库中的值，恢复为环境变量或默认值`"
        @click="$emit('reset')"
      >
        <Loader2 v-if="resetting" class="animate-spin" />
        <RotateCcw v-else />
        恢复默认
      </Button>
    </div>
    <slot />
    <p v-if="error" class="text-destructive text-xs" role="alert">
      {{ error }}
    </p>
    <p v-else-if="hint || $slots.hint" class="text-muted-foreground text-xs">
      <slot name="hint">
        {{ hint }}
      </slot>
    </p>
  </div>
</template>
