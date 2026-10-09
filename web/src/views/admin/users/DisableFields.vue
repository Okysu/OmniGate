<script setup lang="ts">
import type { DisableForm } from '@/lib/userAdmin'
import { computed } from 'vue'
import FormField from '@/components/FormField.vue'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { toLocalInput } from '@/lib/timeRange'
import { DISABLE_REASON_MAX, UNTIL_PRESETS } from '@/lib/userAdmin'

/** Reason (required) and optional "到期自动启用" time of a disable action. */
defineProps<{ errors: Record<string, string>, idPrefix: string }>()
const form = defineModel<DisableForm>({ required: true })

const minLocal = computed(() => toLocalInput(new Date(Date.now() + 60_000)))
const reasonLength = computed(() => [...form.value.reason.trim()].length)

function preset(hours: number | null) {
  form.value = { ...form.value, until: hours === null ? '' : toLocalInput(new Date(Date.now() + hours * 3_600_000)) }
}
</script>

<template>
  <div class="text-foreground space-y-4 text-left">
    <FormField label="停用原因" :for="`${idPrefix}-reason`" required :error="errors.disabledReason ?? errors.reason">
      <Textarea
        :id="`${idPrefix}-reason`"
        :model-value="form.reason"
        rows="2"
        :maxlength="DISABLE_REASON_MAX"
        placeholder="例如：违反使用条款，请联系 support@example.com"
        :aria-invalid="!!(errors.disabledReason ?? errors.reason)"
        @update:model-value="(v) => form = { ...form, reason: String(v) }"
      />
      <template #hint>
        用户登录时会看到此原因 · {{ reasonLength }}/{{ DISABLE_REASON_MAX }}
      </template>
    </FormField>
    <FormField label="到期自动启用" :for="`${idPrefix}-until`" :error="errors.disabledUntil ?? errors.until" hint="留空表示永久停用，需管理员手动启用（按你的本地时区）。">
      <div class="space-y-2">
        <Input
          :id="`${idPrefix}-until`"
          :model-value="form.until"
          type="datetime-local"
          :min="minLocal"
          :aria-invalid="!!(errors.disabledUntil ?? errors.until)"
          @update:model-value="(v) => form = { ...form, until: String(v) }"
        />
        <div class="flex flex-wrap gap-1.5">
          <button
            type="button"
            class="rounded-md border px-2 py-0.5 text-xs outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            :class="form.until === '' ? 'border-primary bg-primary text-primary-foreground' : 'hover:bg-muted'"
            @click="preset(null)"
          >
            永久
          </button>
          <button
            v-for="p in UNTIL_PRESETS"
            :key="p.hours"
            type="button"
            class="hover:bg-muted rounded-md border px-2 py-0.5 text-xs outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            @click="preset(p.hours)"
          >
            {{ p.label }}
          </button>
        </div>
      </div>
    </FormField>
  </div>
</template>
