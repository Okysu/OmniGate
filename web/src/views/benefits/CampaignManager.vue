<script setup lang="ts">
import type { Campaign } from '@/lib/benefits'
import type { Plan } from '@/lib/types'
import { computed, ref, toRaw, watch } from 'vue'
import { toast } from 'vue-sonner'
import { Check, ChevronsUpDown } from '@lucide/vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Combobox, ComboboxAnchor, ComboboxEmpty, ComboboxGroup, ComboboxInput, ComboboxItem, ComboboxItemIndicator, ComboboxList, ComboboxTrigger, ComboboxViewport } from '@/components/ui/combobox'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import PlanFormSheet from '@/views/billing/plans/PlanFormSheet.vue'
import { api, errorMessage } from '@/lib/api'
import { benefitsApi, emptyCampaign } from '@/lib/benefits'
import { fetchAllPlans } from '@/lib/endpoints'
const props = defineProps<{ startWithCreate: boolean }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [] }>()
const items = ref<Campaign[]>([])
const plans = ref<Plan[]>([])
const groups = ref<{ value: string, label: string }[]>([])
const users = ref<{ value: string, label: string }[]>([])
const form = ref<Campaign | null>(null)
const saving = ref(false)
const error = ref('')
const showPlan = ref(false)
const hiddenPlans = computed(() => plans.value.filter(p => p.hidden && p.stackable && p.status === 'active'))

const selectedGroups = computed({
  get: () => groups.value.filter(g => form.value?.conditions.groupIds.includes(g.value)),
  set: (value: { value: string, label: string }[]) => { if (form.value) form.value.conditions.groupIds = value.map(g => g.value) },
})
const selectedUsers = computed({
  get: () => users.value.filter(u => form.value?.conditions.userIds.includes(u.value)),
  set: (value: { value: string, label: string }[]) => { if (form.value) form.value.conditions.userIds = value.map(u => u.value) },
})
const local = (iso: string | null) => iso ? new Date(Date.parse(iso) - new Date(iso).getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ''
function edit(c?: Campaign) {
  const value = structuredClone(toRaw(c ?? emptyCampaign()))
  value.startsAt = local(value.startsAt)
  value.endsAt = local(value.endsAt)
  value.conditions.registeredAfter = local(value.conditions.registeredAfter)
  value.conditions.registeredBefore = local(value.conditions.registeredBefore)
  form.value = value
  error.value = ''
}
async function load() {
  try {
    const [events, catalog, gs] = await Promise.all([benefitsApi.adminList(), fetchAllPlans(), api.get<{ items: { id: string, name: string }[] }>('/api/admin/groups')])
    items.value = events.items
    plans.value = catalog
    groups.value = gs.items.map(g => ({ value: g.id, label: g.name }))
    const all: { value: string, label: string }[] = []
    for (let page = 1; ; page++) {
      const result = await api.get<{ items: { id: string, displayName: string }[], total: number }>(`/api/admin/users?page=${page}&pageSize=200`)
      all.push(...result.items.map(u => ({ value: u.id, label: u.displayName })))
      if (all.length >= result.total || !result.items.length) break
    }
    users.value = all
  }
  catch (err) { error.value = errorMessage(err) }
}
watch(open, (value) => { if (value) { form.value = null; error.value = ''; if (props.startWithCreate) edit(); void load() } })
async function save() {
  if (!form.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const body = structuredClone(toRaw(form.value))
    const iso = (v: string | null) => v ? new Date(v).toISOString() : null
    body.startsAt = iso(body.startsAt)
    body.endsAt = iso(body.endsAt)
    body.conditions.registeredAfter = iso(body.conditions.registeredAfter)
    body.conditions.registeredBefore = iso(body.conditions.registeredBefore)
    if (body.rewardType === 'balance') { body.planId = null; body.price = '0' }
    else { body.amount = '0' }
    await benefitsApi.save(body)
    form.value = null
    toast.success('活动已保存')
    emit('saved')
    open.value = false
    await load()
  }
  catch (err) { error.value = errorMessage(err) }
  finally { saving.value = false }
}
async function planSaved(plan: Plan) {
  plans.value = await fetchAllPlans()
  if (form.value) form.value.planId = plan.id
}
async function share(c: Campaign) {
  try { await navigator.clipboard.writeText(`${location.origin}/benefits/${c.id}`); toast.success('活动链接已复制') }
  catch { toast.error('复制失败') }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90dvh] overflow-y-auto sm:max-w-3xl">
      <DialogHeader><DialogTitle>{{ form ? (form.id ? '编辑福利活动' : '新建福利活动') : '管理福利活动' }}</DialogTitle><DialogDescription>配置活动奖励、库存与参与条件，保存后在福利页展示。</DialogDescription></DialogHeader>
      <Button v-if="!form" variant="outline" @click="edit()">
        新建活动
      </Button>
      <Alert v-if="error" variant="destructive">
        <AlertDescription>{{ error }}</AlertDescription>
      </Alert>
      <form v-if="form" class="space-y-5" @submit.prevent="save">
        <div class="space-y-2">
          <Label for="event-title">活动名称</Label><Input id="event-title" v-model="form.title" required maxlength="100" />
        </div>
        <div class="space-y-2">
          <Label for="event-description">活动介绍</Label><Textarea id="event-description" v-model="form.description" rows="4" maxlength="10000" placeholder="活动奖励、适用人群与参与说明" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label for="event-status">发布状态</Label><Select v-model="form.status">
              <SelectTrigger id="event-status" class="w-full">
                <SelectValue />
              </SelectTrigger><SelectContent>
                <SelectItem value="draft">
                  草稿
                </SelectItem><SelectItem value="active">
                  发布
                </SelectItem><SelectItem value="archived">
                  下线
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <Label for="event-reward">奖励类型</Label><Select v-model="form.rewardType">
              <SelectTrigger id="event-reward" class="w-full">
                <SelectValue />
              </SelectTrigger><SelectContent>
                <SelectItem value="balance">
                  余额奖励
                </SelectItem><SelectItem value="subscription">
                  限时套餐
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <div v-if="form.rewardType === 'balance'" class="space-y-2">
          <Label for="event-amount">赠送余额</Label><Input id="event-amount" v-model="form.amount" inputmode="decimal" required /><p class="text-muted-foreground text-xs">
            余额奖励免费领取，金额使用站点结算币种。
          </p>
        </div>
        <div v-else class="space-y-3 rounded-lg border p-4">
          <Label for="event-plan">活动套餐</Label>
          <Select :model-value="form.planId ?? undefined" @update:model-value="value => { if (form) form.planId = value ? String(value) : null }">
            <SelectTrigger id="event-plan" class="w-full">
              <SelectValue placeholder="选择活动套餐" />
            </SelectTrigger><SelectContent>
              <SelectItem v-for="p in hiddenPlans" :key="p.id" :value="p.id">
                {{ p.name }} · {{ p.duration }}
              </SelectItem>
            </SelectContent>
          </Select>
          <Button type="button" variant="outline" @click="showPlan = true">
            新建活动套餐
          </Button>
          <p class="text-muted-foreground text-xs">
            套餐默认有效期 7 天，每次领取独立额度，仅在活动中提供。
          </p>
          <Label for="event-price">活动售价（0 为免费）</Label><Input id="event-price" v-model="form.price" inputmode="decimal" required />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label for="event-stock">剩余活动库存</Label><Input id="event-stock" :model-value="form.stock" type="number" min="-1" step="1" required @update:model-value="value => { if (form) form.stock = Number(value) }" /><p class="text-muted-foreground text-xs">
              -1 不限量；0 停止领取。套餐奖励还受套餐自身库存限制。
            </p>
          </div>
          <div class="space-y-2">
            <Label for="event-limit">每人最多领取次数</Label><Input id="event-limit" :model-value="form.perUserLimit" type="number" min="1" max="1000" required @update:model-value="value => { if (form) form.perUserLimit = Number(value) }" />
          </div>
          <div class="space-y-2">
            <Label for="event-start">开始时间（可留空）</Label><Input id="event-start" :model-value="form.startsAt ?? ''" type="datetime-local" @update:model-value="value => { if (form) form.startsAt = String(value) || null }" />
          </div>
          <div class="space-y-2">
            <Label for="event-end">结束时间（可留空）</Label><Input id="event-end" :model-value="form.endsAt ?? ''" type="datetime-local" @update:model-value="value => { if (form) form.endsAt = String(value) || null }" />
          </div>
        </div>
        <fieldset class="space-y-4 rounded-lg border p-4">
          <legend class="px-2 text-sm font-medium">
            领取条件（全部满足；不设置即登录可领）
          </legend>
          <div class="space-y-2">
            <Label for="event-groups">用户组</Label>
            <Combobox v-model="selectedGroups" multiple by="value">
              <ComboboxAnchor as-child>
                <ComboboxTrigger as-child>
                  <Button id="event-groups" type="button" variant="outline" class="w-full justify-between font-normal">
                    <span class="truncate">{{ selectedGroups.length ? selectedGroups.map(item => item.label).join('、') : '不限制用户组' }}</span><ChevronsUpDown class="size-4 shrink-0 opacity-50" />
                  </Button>
                </ComboboxTrigger>
              </ComboboxAnchor>
              <ComboboxList align="start" class="w-(--reka-combobox-trigger-width)">
                <ComboboxInput placeholder="搜索用户组…" /><ComboboxViewport>
                  <ComboboxEmpty>没有匹配项</ComboboxEmpty><ComboboxGroup>
                    <ComboboxItem v-for="option in groups" :key="option.value" :value="option">
                      {{ option.label }}<ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
                    </ComboboxItem>
                  </ComboboxGroup>
                </ComboboxViewport>
              </ComboboxList>
            </Combobox>
            <Button v-if="form.conditions.groupIds.length" type="button" variant="ghost" size="sm" @click="form.conditions.groupIds = []">
              清空选择
            </Button>
          </div>
          <div class="space-y-2">
            <Label for="event-users">指定用户</Label>
            <Combobox v-model="selectedUsers" multiple by="value">
              <ComboboxAnchor as-child>
                <ComboboxTrigger as-child>
                  <Button id="event-users" type="button" variant="outline" class="w-full justify-between font-normal">
                    <span class="truncate">{{ selectedUsers.length ? selectedUsers.map(item => item.label).join('、') : '不限制用户' }}</span><ChevronsUpDown class="size-4 shrink-0 opacity-50" />
                  </Button>
                </ComboboxTrigger>
              </ComboboxAnchor>
              <ComboboxList align="start" class="w-(--reka-combobox-trigger-width)">
                <ComboboxInput placeholder="搜索用户…" /><ComboboxViewport>
                  <ComboboxEmpty>没有匹配项</ComboboxEmpty><ComboboxGroup>
                    <ComboboxItem v-for="option in users" :key="option.value" :value="option">
                      {{ option.label }}<ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
                    </ComboboxItem>
                  </ComboboxGroup>
                </ComboboxViewport>
              </ComboboxList>
            </Combobox>
            <Button v-if="form.conditions.userIds.length" type="button" variant="ghost" size="sm" @click="form.conditions.userIds = []">
              清空选择
            </Button>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="space-y-2">
              <Label for="registered-after">注册时间不早于</Label><Input id="registered-after" :model-value="form.conditions.registeredAfter ?? ''" type="datetime-local" @update:model-value="value => { if (form) form.conditions.registeredAfter = String(value) || null }" />
            </div><div class="space-y-2">
              <Label for="registered-before">注册时间早于</Label><Input id="registered-before" :model-value="form.conditions.registeredBefore ?? ''" type="datetime-local" @update:model-value="value => { if (form) form.conditions.registeredBefore = String(value) || null }" />
            </div>
          </div>
          <div class="space-y-2">
            <Label for="event-spend">累计模型消费至少</Label><Input id="event-spend" v-model="form.conditions.minSpend" inputmode="decimal" /><p class="text-muted-foreground text-xs">
              包含钱包及套餐模型消费；购买套餐本身不计入。0 为不限。
            </p>
          </div>
          <div class="flex items-center gap-2">
            <Checkbox id="event-require-purchase" v-model="form.conditions.requirePurchase" /><Label for="event-require-purchase">必须有付费套餐购买记录</Label>
          </div>
        </fieldset>
        <div class="flex gap-3">
          <Button type="submit" :disabled="saving">
            {{ saving ? '保存中…' : '保存活动' }}
          </Button><Button type="button" variant="outline" @click="open = false">
            取消
          </Button>
        </div>
      </form>
      <div v-if="!form" class="grid gap-4 md:grid-cols-2">
        <Card v-for="c in items" :key="c.id">
          <CardHeader><CardTitle>{{ c.title }}</CardTitle></CardHeader><CardContent class="space-y-3">
            <p class="text-muted-foreground text-sm">
              {{ c.status === 'active' ? '已发布' : c.status === 'draft' ? '草稿' : '已下线' }} · {{ c.rewardType === 'balance' ? `$${c.amount} 余额` : '套餐奖励' }} · {{ Number(c.price) > 0 ? `$${c.price}` : '免费' }}
            </p><p class="text-sm">
              库存 {{ c.stock === -1 ? '不限' : c.stock }} · 每人 {{ c.perUserLimit }} 次
            </p><div class="flex gap-2">
              <Button variant="outline" @click="edit(c)">
                编辑
              </Button><Button v-if="c.status === 'active'" variant="outline" @click="share(c)">
                复制活动链接
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
      <p v-if="!items.length && !form" class="text-muted-foreground">
        还没有福利活动，点击“新建活动”开始。
      </p>
    </DialogContent>
  </Dialog>
  <PlanFormSheet v-model:open="showPlan" :plan="null" activity @saved="planSaved" />
</template>
