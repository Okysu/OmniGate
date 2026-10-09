<script setup lang="ts">
import type { RangePreset } from '@/lib/timeRange'
import { ref, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { RANGE_PRESET_LABELS, validateRange, fromLocalInput } from '@/lib/timeRange'

/**
 * Preset selector with an optional custom range (two datetime-local inputs).
 * Emits `apply` with the custom ISO range once validated.
 */
const props = withDefaults(defineProps<{
  presets?: RangePreset[]
  /** Current custom range (local `datetime-local` values). */
  customFrom?: string
  customTo?: string
}>(), {
  presets: () => ['1h', '24h', '7d', 'custom'],
  customFrom: '',
  customTo: '',
})
const preset = defineModel<RangePreset>('preset', { required: true })
const emit = defineEmits<{ apply: [range: { from: string, to: string, fromLocal: string, toLocal: string }] }>()

const from = ref(props.customFrom)
const to = ref(props.customTo)
const error = ref<string | null>(null)
watch(() => [props.customFrom, props.customTo], ([f, t]) => {
  from.value = f ?? ''
  to.value = t ?? ''
})

function apply() {
  const f = fromLocalInput(from.value)
  const t = fromLocalInput(to.value)
  error.value = validateRange(f, t)
  if (!error.value && f && t)
    emit('apply', { from: f, to: t, fromLocal: from.value, toLocal: to.value })
}

function setPreset(v: unknown) {
  if (typeof v === 'string' && (props.presets as string[]).includes(v))
    preset.value = v as RangePreset
}
</script>

<template>
  <div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
    <Select :model-value="preset" @update:model-value="setPreset">
      <SelectTrigger class="w-full sm:w-36" aria-label="时间范围">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem v-for="p in presets" :key="p" :value="p">
          {{ RANGE_PRESET_LABELS[p] }}
        </SelectItem>
      </SelectContent>
    </Select>
    <template v-if="preset === 'custom'">
      <div class="grid grid-cols-[1fr_auto_1fr] items-center gap-1.5 sm:flex">
        <Input v-model="from" type="datetime-local" class="sm:w-48" aria-label="开始时间" />
        <span class="text-muted-foreground text-xs">至</span>
        <Input v-model="to" type="datetime-local" class="sm:w-48" aria-label="结束时间" />
      </div>
      <Button variant="outline" size="sm" @click="apply">
        应用
      </Button>
      <p v-if="error" class="text-destructive text-xs">
        {{ error }}
      </p>
    </template>
  </div>
</template>
