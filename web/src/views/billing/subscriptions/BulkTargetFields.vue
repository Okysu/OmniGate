<script setup lang="ts">
import type { TargetMode } from '@/lib/subscriptionBulk'
import type { AudienceFilter } from '@/lib/audience'
import AudienceFilterFields from '@/components/AudienceFilterFields.vue'
import type { Plan, Subscription } from '@/lib/types'
import { computed } from 'vue'
import FormField from '@/components/FormField.vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { formatDateTime } from '@/lib/format'

/**
 * "作用范围": the checked rows, or every active subscription matching the filters —
 * any of the chosen plans AND held by a member of any of the chosen groups (empty = no restriction).
 */
const props = defineProps<{
  /** Checked rows (only active ones are selectable). */
  selectedCount: number
  plans: Plan[]
  /** Row action: the target is this one subscription (no choice). */
  single?: Subscription | null
  idPrefix: string
}>()
const mode = defineModel<TargetMode>('mode', { required: true })
const planIds = defineModel<string[]>('planIds', { required: true })
const groupIds = defineModel<string[]>('groupIds', { required: true })

const filter = defineModel<AudienceFilter>('filter', { required: true })
const audience = computed({
  get: () => ({ ...filter.value, planIds: planIds.value, groupIds: groupIds.value, requireActiveSubscription: true }),
  set: (value: AudienceFilter) => { filter.value = value; planIds.value = value.planIds; groupIds.value = value.groupIds },
})

function setMode(v: unknown) {
  if (v === 'plan' || (v === 'selected' && props.selectedCount > 0))
    mode.value = v
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
  <div v-else class="space-y-4">
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
            按条件（全部有效订阅）
          </SelectItem>
        </SelectContent>
      </Select>
    </FormField>
    <AudienceFilterFields v-if="mode === 'plan'" v-model="audience" :plans="plans" subscription-only />
  </div>
</template>
