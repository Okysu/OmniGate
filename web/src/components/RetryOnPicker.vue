<script setup lang="ts">
import type { RetryOn } from '@/lib/types'
import { Checkbox } from '@/components/ui/checkbox'
import { CLIENT_ERROR_HINT, normalizeRetryOn, RETRY_ON, RETRY_ON_LABELS } from '@/lib/retry'

/** Retry-class checkboxes (route rules and the `gateway.retryOn` setting). */
withDefaults(defineProps<{ idPrefix?: string, disabled?: boolean }>(), { idPrefix: 'retry', disabled: false })
const model = defineModel<RetryOn[]>({ required: true })

function toggle(r: RetryOn, on: boolean | 'indeterminate') {
  model.value = on === true ? normalizeRetryOn([...model.value, r]) : model.value.filter(x => x !== r)
}
</script>

<template>
  <div class="grid gap-2 sm:grid-cols-2" data-testid="retry-on-picker">
    <div
      v-for="r in RETRY_ON"
      :key="r"
      :class="r === 'client_error' ? 'sm:col-span-full' : ''"
    >
      <label :for="`${idPrefix}-${r}`" class="hover:bg-muted/50 flex h-full cursor-pointer items-start gap-2 rounded-md border px-2.5 py-2 text-sm" :data-retry="r">
        <Checkbox :id="`${idPrefix}-${r}`" class="mt-0.5" :model-value="model.includes(r)" :disabled="disabled" @update:model-value="(v) => toggle(r, v)" />
        <span class="min-w-0">
          <span class="block">{{ RETRY_ON_LABELS[r] }}</span>
          <span v-if="r === 'client_error'" class="text-muted-foreground mt-0.5 block text-xs leading-relaxed">{{ CLIENT_ERROR_HINT }}</span>
        </span>
      </label>
    </div>
  </div>
</template>
