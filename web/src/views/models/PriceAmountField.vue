<script setup lang="ts">
import FormField from '@/components/FormField.vue'
import { Input } from '@/components/ui/input'

/** One amount input of the price dialog: currency symbol prefix, decimal keyboard. */
defineProps<{
  id: string
  label: string
  symbol: string
  hint?: string
  placeholder?: string
  error?: string
  required?: boolean
}>()
const value = defineModel<string>({ required: true })
</script>

<template>
  <FormField :label="label" :for="id" :error="error" :hint="hint" :required="required">
    <div class="relative">
      <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ symbol }}</span>
      <Input :id="id" v-model="value" inputmode="decimal" :placeholder="placeholder ?? '0'" class="pl-7 font-mono tabular-nums" :aria-invalid="!!error" />
    </div>
  </FormField>
</template>
