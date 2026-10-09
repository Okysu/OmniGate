<script setup lang="ts">
// Build diagnostics (`POST draft/build`, import results, publish failures).
import type { BuildDiagnostic } from '@/lib/types'
import { CircleAlert, TriangleAlert } from '@lucide/vue'

defineProps<{
  diagnostics: BuildDiagnostic[]
  /** Rows become buttons that emit `select`. */
  interactive?: boolean
}>()
defineEmits<{ select: [d: BuildDiagnostic] }>()

function location(d: BuildDiagnostic): string {
  if (!d.file)
    return '（插件包）'
  return d.line > 0 ? `${d.file}:${d.line}${d.column > 0 ? `:${d.column}` : ''}` : d.file
}
</script>

<template>
  <ul class="divide-y text-sm">
    <li v-for="(d, i) in diagnostics" :key="i">
      <component
        :is="interactive ? 'button' : 'div'"
        :type="interactive ? 'button' : undefined"
        class="flex w-full items-start gap-2 px-2 py-1.5 text-left"
        :class="interactive ? 'hover:bg-muted/60 focus-visible:bg-muted/60 cursor-pointer outline-none' : ''"
        @click="interactive && $emit('select', d)"
      >
        <CircleAlert v-if="d.severity === 'error'" class="text-destructive mt-0.5 size-4 shrink-0" aria-label="错误" />
        <TriangleAlert v-else class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" aria-label="警告" />
        <span class="min-w-0 flex-1 break-words">{{ d.message }}</span>
        <span class="text-muted-foreground shrink-0 font-mono text-xs">{{ location(d) }}</span>
      </component>
    </li>
  </ul>
</template>
