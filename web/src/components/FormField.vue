<script setup lang="ts">
import { Label } from '@/components/ui/label'

defineProps<{
  label?: string
  /** id of the control, for the label's `for`. */
  for?: string
  hint?: string
  error?: string | null
  required?: boolean
}>()
</script>

<template>
  <div class="space-y-1.5">
    <Label v-if="label" :for="$props.for" class="gap-1">
      {{ label }}<span v-if="required" class="text-destructive" aria-hidden="true">*</span>
    </Label>
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
