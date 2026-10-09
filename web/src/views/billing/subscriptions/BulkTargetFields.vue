<script setup lang="ts">
import type { TargetMode } from '@/lib/subscriptionBulk'
import type { Plan, Subscription } from '@/lib/types'
import FormField from '@/components/FormField.vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { ALL_PLANS } from '@/lib/subscriptionBulk'
import { formatDateTime } from '@/lib/format'

/** "作用范围": the checked rows, or every active subscription of a plan (or all plans). */
const props = defineProps<{
  /** Checked rows (only active ones are selectable). */
  selectedCount: number
  plans: Plan[]
  /** Row action: the target is this one subscription (no choice). */
  single?: Subscription | null
  idPrefix: string
}>()
const mode = defineModel<TargetMode>('mode', { required: true })
const planId = defineModel<string>('planId', { required: true })

function setMode(v: unknown) {
  if (v === 'plan' || (v === 'selected' && props.selectedCount > 0))
    mode.value = v
}
function planText(id: string): string {
  if (id === ALL_PLANS)
    return '全部套餐'
  const p = props.plans.find(x => x.id === id)
  return p ? `${p.name}${p.status === 'archived' ? '（已下架）' : ''}` : `${id.slice(0, 8)}…`
}
</script>

<template>
  <div v-if="single" class="bg-muted/40 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 rounded-lg border p-3 text-sm" data-testid="bulk-single">
    <span class="text-muted-foreground">用户</span>
    <span class="truncate font-medium">{{ single.user.displayName }}</span>
    <span class="text-muted-foreground">套餐</span>
    <span class="truncate">{{ single.plan.name }}</span>
    <span class="text-muted-foreground">有效期至</span>
    <span class="tabular-nums">{{ formatDateTime(single.endsAt) }}</span>
  </div>
  <div v-else class="grid gap-4 sm:grid-cols-2">
    <FormField label="作用范围" :for="`${idPrefix}-mode`" required>
      <Select :model-value="mode" @update:model-value="setMode">
        <SelectTrigger :id="`${idPrefix}-mode`" class="w-full" data-testid="bulk-mode">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="selected" :disabled="selectedCount === 0">
            所选订阅（{{ selectedCount }} 份）
          </SelectItem>
          <SelectItem value="plan">
            按套餐（全部有效订阅）
          </SelectItem>
        </SelectContent>
      </Select>
    </FormField>
    <FormField label="套餐" :for="`${idPrefix}-plan`" :required="mode === 'plan'">
      <Select v-model="planId" :disabled="mode !== 'plan'">
        <SelectTrigger :id="`${idPrefix}-plan`" class="w-full" data-testid="bulk-plan">
          <SelectValue>{{ mode === 'plan' ? planText(planId) : '—' }}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL_PLANS">
            全部套餐
          </SelectItem>
          <SelectItem v-for="p in plans" :key="p.id" :value="p.id">
            {{ p.name }}{{ p.status === 'archived' ? '（已下架）' : '' }}
          </SelectItem>
        </SelectContent>
      </Select>
    </FormField>
  </div>
</template>
