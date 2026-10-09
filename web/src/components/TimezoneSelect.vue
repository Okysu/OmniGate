<script setup lang="ts">
import { computed } from 'vue'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from '@/components/ui/select'
import { timezoneOptions, utcOffsetLabel } from '@/lib/notifications'

/** IANA time-zone picker: common zones first (with UTC offsets), then every zone the browser knows. */
const props = defineProps<{
  id?: string
  disabled?: boolean
  invalid?: boolean
}>()
const model = defineModel<string>({ required: true })
const tz = computed(() => timezoneOptions(model.value))

function onUpdate(v: unknown) {
  if (typeof v === 'string' && v)
    model.value = v
}
</script>

<template>
  <Select :model-value="model" :disabled="props.disabled" @update:model-value="onUpdate">
    <SelectTrigger :id="id" class="w-full" :aria-invalid="invalid || undefined" data-testid="timezone-select">
      <SelectValue />
    </SelectTrigger>
    <SelectContent class="max-h-80">
      <SelectGroup>
        <SelectLabel>常用</SelectLabel>
        <SelectItem v-for="z in tz.common" :key="z" :value="z">
          {{ z }} <span class="text-muted-foreground text-xs">{{ utcOffsetLabel(z) }}</span>
        </SelectItem>
      </SelectGroup>
      <template v-if="tz.others.length">
        <SelectSeparator />
        <SelectGroup>
          <SelectLabel>全部</SelectLabel>
          <SelectItem v-for="z in tz.others" :key="z" :value="z">
            {{ z }}
          </SelectItem>
        </SelectGroup>
      </template>
    </SelectContent>
  </Select>
</template>
