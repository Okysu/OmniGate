<script setup lang="ts">
import type { TargetMode } from '@/lib/subscriptionBulk'
import type { Plan, Subscription, UserGroup } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { errorMessage } from '@/lib/api'
import { groupsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'

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

const auth = useAuthStore()
const canListGroups = computed(() => auth.can('users.read'))
const groups = ref<UserGroup[]>([])
const groupsLoading = ref(false)
const groupsError = ref<string | null>(null)
onMounted(async () => {
  if (!canListGroups.value)
    return
  groupsLoading.value = true
  try {
    groups.value = (await groupsApi.list()).items
  }
  catch (err) {
    groupsError.value = errorMessage(err)
  }
  finally {
    groupsLoading.value = false
  }
})

function setMode(v: unknown) {
  if (v === 'plan' || (v === 'selected' && props.selectedCount > 0))
    mode.value = v
}
const planOptions = computed(() => props.plans.map(p => ({ value: p.id, label: `${p.name}${p.status === 'archived' ? '（已下架）' : ''}` })))
const groupOptions = computed(() => groups.value.map(g => ({ value: g.id, label: g.name, hint: `${g.members} 人` })))
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
    <div v-if="mode === 'plan'" class="grid gap-4 sm:grid-cols-2">
      <FormField label="套餐" :for="`${idPrefix}-plans`" hint="可多选；不选表示全部套餐">
        <MultiSelect :id="`${idPrefix}-plans`" v-model="planIds" :options="planOptions" empty-label="全部套餐" placeholder="搜索套餐" data-testid="bulk-plans" />
      </FormField>
      <FormField label="用户组" :for="`${idPrefix}-groups`" :error="groupsError" :hint="canListGroups ? '可多选；不选表示全部用户组' : '没有查看用户组的权限'">
        <MultiSelect :id="`${idPrefix}-groups`" v-model="groupIds" :options="groupOptions" :loading="groupsLoading" :disabled="!canListGroups" empty-label="全部用户组" placeholder="搜索用户组" data-testid="bulk-groups" />
      </FormField>
      <p class="text-muted-foreground text-xs sm:col-span-2">
        套餐与用户组同时选择时取交集：只影响所选用户组成员持有的所选套餐订阅。
      </p>
    </div>
  </div>
</template>
