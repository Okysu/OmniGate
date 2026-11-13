<script setup lang="ts">
import type { AudienceFilter, AudiencePreview } from '@/lib/audience'
import type { Plan } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, ChevronsUpDown, RefreshCw } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Combobox, ComboboxAnchor, ComboboxEmpty, ComboboxGroup, ComboboxInput, ComboboxItem, ComboboxItemIndicator, ComboboxList, ComboboxTrigger, ComboboxViewport } from '@/components/ui/combobox'
import { api, errorMessage } from '@/lib/api'
import { normalizeAudience, previewAudience } from '@/lib/audience'
import { fetchAllPlans } from '@/lib/endpoints'
import { useAuthStore } from '@/stores/auth'
const props = defineProps<{ plans?: Plan[], subscriptionOnly?: boolean }>()
const model = defineModel<AudienceFilter>({ required: true })
const auth = useAuthStore()
const prefix = 'audience-' + crypto.randomUUID()
type Option = { value: string, label: string }
const loadedPlans = ref<Plan[]>([])
const groups = ref<Option[]>([])
const users = ref<Option[]>([])
const refsError = ref('')
const preview = ref<AudiencePreview | null>(null)
const loading = ref(false)
const error = ref('')
const normalized = computed(() => normalizeAudience(model.value))
const planOptions = computed(() => (props.plans ?? loadedPlans.value).map(p => ({ value: p.id, label: p.name + (p.hidden ? '（隐藏）' : '') + (p.status === 'archived' ? '（已下架）' : '') })))
const sources: Option[] = [{ value: 'purchase', label: '购买' }, { value: 'redeem', label: '兑换码' }, { value: 'admin', label: '管理员发放 / 免费活动' }]
const selectors = computed(() => [
  { key: 'planIds' as const, label: '持有套餐', options: planOptions.value, hint: '不选表示不限；选择后须持有其中任一有效订阅' },
  { key: 'excludePlanIds' as const, label: '排除套餐', options: planOptions.value, hint: '这些套餐的订阅不参与匹配；同时持有其他符合条件的订阅仍可入选' },
  ...(auth.can('users.read') ? [
    { key: 'groupIds' as const, label: '用户组', options: groups.value, hint: '不选表示不限用户组' },
    { key: 'userIds' as const, label: '指定用户', options: users.value, hint: '不选表示不限用户' },
  ] : []),
  { key: 'sources' as const, label: '订阅来源', options: sources, hint: '不选表示不限；兑换码来源不等于付费购买' },
])
function selected(key: 'planIds' | 'excludePlanIds' | 'groupIds' | 'userIds' | 'sources', options: Option[]) { return options.filter(o => normalized.value[key].includes(o.value)) }
function set(key: keyof AudienceFilter, value: unknown) { model.value = { ...normalized.value, [key]: value } }
const local = (value: string | null) => value ? new Date(Date.parse(value) - new Date(value).getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ''
function setDate(key: 'registeredAfter' | 'registeredBefore', value: unknown) { set(key, value ? new Date(String(value)).toISOString() : null) }
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
async function refresh() {
  const current = ++generation
  loading.value = true
  error.value = ''
  try { const result = await previewAudience(normalized.value); if (current === generation) preview.value = result }
  catch (err) { if (current === generation) error.value = errorMessage(err) }
  finally { if (current === generation) loading.value = false }
}
watch(normalized, () => {
  ++generation
  preview.value = null
  loading.value = true
  clearTimeout(timer)
  timer = setTimeout(refresh, 400)
}, { deep: true, immediate: true })
onBeforeUnmount(() => { clearTimeout(timer); ++generation })
onMounted(async () => {
  try {
    const tasks: Promise<unknown>[] = []
    if (!props.plans) tasks.push(fetchAllPlans().then(value => { loadedPlans.value = value }))
    if (auth.can('users.read')) {
      tasks.push(api.get<{ items: { id: string, name: string }[] }>('/api/admin/groups').then(value => { groups.value = value.items.map(g => ({ value: g.id, label: g.name })) }))
      tasks.push((async () => {
        const all: Option[] = []
        for (let page = 1; ; page++) {
          const value = await api.get<{ items: { id: string, displayName: string }[], total: number }>(`/api/admin/users?page=${page}&pageSize=200`)
          all.push(...value.items.map(u => ({ value: u.id, label: u.displayName })))
          if (!value.items.length || all.length >= value.total) break
        }
        users.value = all
      })())
    }
    const results = await Promise.allSettled(tasks)
    const failures = results.filter(r => r.status === 'rejected')
    if (failures.length) refsError.value = failures.map(r => errorMessage(r.reason)).join('；')
  }
  catch (err) { refsError.value = errorMessage(err) }
})
</script>
<template>
  <div class="space-y-4">
    <p class="text-muted-foreground text-xs">
      同一项内任选其一，各项之间全部满足。套餐、排除套餐、来源与额度窗口须匹配同一份匹配订阅；只统计正常状态用户。
    </p>
    <Alert v-if="refsError" variant="destructive">
      <AlertDescription>{{ refsError }}</AlertDescription>
    </Alert>
    <div class="grid gap-4 sm:grid-cols-2">
      <div v-for="field in selectors" :key="field.key" class="space-y-2">
        <Label :for="prefix + field.key">{{ field.label }}</Label>
        <Combobox :model-value="selected(field.key, field.options)" multiple by="value" @update:model-value="value => set(field.key, (value as Option[]).map(o => o.value))">
          <ComboboxAnchor as-child>
            <ComboboxTrigger as-child :aria-label="field.label">
              <Button :id="prefix + field.key" type="button" variant="outline" class="w-full justify-between font-normal">
                <span class="truncate">{{ normalized[field.key].map(id => field.options.find(o => o.value === id)?.label ?? ('未加载 ' + id.slice(0, 8))).join('、') || '不限制' }}</span><ChevronsUpDown class="size-4 shrink-0 opacity-50" />
              </Button>
            </ComboboxTrigger>
          </ComboboxAnchor>
          <ComboboxList align="start" class="w-(--reka-combobox-trigger-width)">
            <ComboboxInput placeholder="搜索…" /><ComboboxViewport>
              <ComboboxEmpty>没有匹配项</ComboboxEmpty><ComboboxGroup>
                <ComboboxItem v-for="option in field.options" :key="option.value" :value="option">
                  {{ option.label }}<ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
                </ComboboxItem>
              </ComboboxGroup>
            </ComboboxViewport>
          </ComboboxList>
        </Combobox>
        <p class="text-muted-foreground text-xs">
          {{ field.hint }}
        </p>
        <Button v-if="normalized[field.key].length" type="button" variant="ghost" size="sm" @click="set(field.key, [])">
          清空选择（{{ normalized[field.key].length }}）
        </Button>
      </div>
      <div class="space-y-2">
        <Label :for="prefix + 'quota'">订阅额度窗口</Label>
        <Select :model-value="normalized.quotaKind || 'any'" @update:model-value="value => set('quotaKind', value === 'any' ? '' : value)">
          <SelectTrigger :id="prefix + 'quota'" class="w-full">
            <SelectValue />
          </SelectTrigger><SelectContent>
            <SelectItem value="any">
              不限制
            </SelectItem><SelectItem value="weekly">
              含周额度
            </SelectItem><SelectItem value="5h">
              含 5 小时额度
            </SelectItem><SelectItem value="both">
              含 5 小时或周额度
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div v-for="field in [{ key: 'registeredAfter' as const, label: '注册时间不早于' }, { key: 'registeredBefore' as const, label: '注册时间早于' }]" :key="field.key" class="space-y-2">
        <Label :for="prefix + field.key">{{ field.label }}</Label><Input :id="prefix + field.key" type="datetime-local" :model-value="local(normalized[field.key])" @update:model-value="value => setDate(field.key, value)" />
      </div>
      <div class="space-y-2">
        <Label :for="prefix + 'spend'">累计模型消费至少</Label><Input :id="prefix + 'spend'" :model-value="normalized.minSpend" inputmode="decimal" @update:model-value="value => set('minSpend', String(value))" /><p class="text-muted-foreground text-xs">
          含钱包及套餐用量；购买套餐不计入，0 为不限。
        </p>
      </div>
    </div>
    <div v-if="!subscriptionOnly" class="flex items-center gap-2">
      <Checkbox :id="prefix + 'active'" :model-value="normalized.requireActiveSubscription" @update:model-value="value => set('requireActiveSubscription', value === true)" /><Label :for="prefix + 'active'">须持有有效订阅</Label>
    </div>
    <div class="flex items-center gap-2">
      <Checkbox :id="prefix + 'hidden'" :model-value="normalized.excludeHiddenPlans" @update:model-value="value => set('excludeHiddenPlans', value === true)" /><Label :for="prefix + 'hidden'">排除隐藏活动套餐的订阅</Label>
    </div>
    <div class="flex items-center gap-2">
      <Checkbox :id="prefix + 'purchase'" :model-value="normalized.requirePurchase" @update:model-value="value => set('requirePurchase', value === true)" /><Label :for="prefix + 'purchase'">须有付费套餐购买记录</Label>
    </div>
    <div class="bg-muted/40 space-y-2 rounded-lg border p-3 text-sm" aria-live="polite">
      <div class="flex items-center justify-between gap-3">
        <span>{{ loading ? '正在预估影响用户…' : preview ? `预计匹配 ${preview.users} 位用户 · ${preview.subscriptions} 份匹配订阅` : '暂未获得预估' }}</span><Button type="button" variant="ghost" size="sm" :disabled="loading" @click="refresh">
          <RefreshCw class="size-4" />刷新
        </Button>
      </div>
      <p v-if="error" class="text-destructive">
        {{ error }}
      </p>
      <p v-if="preview?.sample.length" class="break-words">
        {{ preview.sample.map(u => u.name).join('、') }}{{ preview.truncated ? '…（仅展示前 50 位）' : '' }}
      </p>
      <p class="text-muted-foreground text-xs">
        按当前条件估算，实际操作时重新校验。活动库存、已领取次数与活动时间还会限制最终领取人数。
      </p>
    </div>
  </div>
</template>
