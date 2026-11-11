<script setup lang="ts">
import type { Campaign } from '@/lib/benefits'
import type { Plan } from '@/lib/types'
import { computed, onMounted, ref, toRaw } from 'vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/PageHeader.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import PlanFormSheet from '@/views/billing/plans/PlanFormSheet.vue'
import { api, errorMessage } from '@/lib/api'
import { benefitsApi, emptyCampaign } from '@/lib/benefits'
import { fetchAllPlans } from '@/lib/endpoints'
const items = ref<Campaign[]>([])
const plans = ref<Plan[]>([])
const groups = ref<{ value: string, label: string }[]>([])
const users = ref<{ value: string, label: string }[]>([])
const form = ref<Campaign | null>(null)
const saving = ref(false)
const error = ref('')
const showPlan = ref(false)
const hiddenPlans = computed(() => plans.value.filter(p => p.hidden && p.stackable && p.status === 'active'))
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
onMounted(load)
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
  <div class="space-y-6">
    <PageHeader title="福利活动">
      <template #actions>
        <Button @click="edit()">
          新建活动
        </Button>
      </template>
    </PageHeader>
    <p v-if="error" role="alert" class="text-destructive rounded-lg border p-3">
      {{ error }}
    </p>
    <Card v-if="form">
      <CardHeader><CardTitle>{{ form.id ? '编辑活动' : '新建活动' }}</CardTitle></CardHeader>
      <CardContent>
        <form class="space-y-5" @submit.prevent="save">
          <div class="space-y-2">
            <Label for="event-title">活动名称</Label><Input id="event-title" v-model="form.title" required maxlength="100" />
          </div>
          <div class="space-y-2">
            <Label for="event-description">活动介绍</Label><Textarea id="event-description" v-model="form.description" rows="4" maxlength="10000" placeholder="活动奖励、适用人群与参与说明" />
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="space-y-2">
              <Label for="event-status">发布状态</Label><select id="event-status" v-model="form.status" class="w-full rounded-md border bg-background p-2">
                <option value="draft">
                  草稿
                </option><option value="active">
                  发布
                </option><option value="archived">
                  下线
                </option>
              </select>
            </div>
            <div class="space-y-2">
              <Label for="event-reward">奖励类型</Label><select id="event-reward" v-model="form.rewardType" class="w-full rounded-md border bg-background p-2">
                <option value="balance">
                  余额奖励
                </option><option value="subscription">
                  限时套餐
                </option>
              </select>
            </div>
          </div>
          <div v-if="form.rewardType === 'balance'" class="space-y-2">
            <Label for="event-amount">赠送余额</Label><Input id="event-amount" v-model="form.amount" inputmode="decimal" required /><p class="text-muted-foreground text-xs">
              余额奖励免费领取，金额使用站点结算币种。
            </p>
          </div>
          <div v-else class="space-y-3 rounded-lg border p-4">
            <Label for="event-plan">活动套餐</Label>
            <select id="event-plan" v-model="form.planId" required class="w-full rounded-md border bg-background p-2">
              <option :value="null" disabled>
                选择隐藏套餐
              </option><option v-for="p in hiddenPlans" :key="p.id" :value="p.id">
                {{ p.name }} · {{ p.duration }}
              </option>
            </select>
            <Button type="button" variant="outline" @click="showPlan = true">
              新建活动套餐
            </Button>
            <p class="text-muted-foreground text-xs">
              复用套餐编辑器，默认 7 天、独立额度。隐藏套餐不会出现在正式购买页。
            </p>
            <Label for="event-price">活动售价（0 为免费）</Label><Input id="event-price" v-model="form.price" inputmode="decimal" required />
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="space-y-2">
              <Label for="event-stock">剩余活动库存</Label><input id="event-stock" v-model.number="form.stock" type="number" min="-1" step="1" required class="w-full rounded-md border bg-transparent p-2"><p class="text-muted-foreground text-xs">
                -1 不限量；0 停止领取。套餐奖励还受套餐自身库存限制。
              </p>
            </div>
            <div class="space-y-2">
              <Label for="event-limit">每人最多领取次数</Label><input id="event-limit" v-model.number="form.perUserLimit" type="number" min="1" max="1000" required class="w-full rounded-md border bg-transparent p-2">
            </div>
            <div class="space-y-2">
              <Label for="event-start">开始时间（可留空）</Label><input id="event-start" v-model="form.startsAt" type="datetime-local" class="w-full rounded-md border bg-transparent p-2">
            </div>
            <div class="space-y-2">
              <Label for="event-end">结束时间（可留空）</Label><input id="event-end" v-model="form.endsAt" type="datetime-local" class="w-full rounded-md border bg-transparent p-2">
            </div>
          </div>
          <fieldset class="space-y-4 rounded-lg border p-4">
            <legend class="px-2 text-sm font-medium">
              领取条件（全部满足；不设置即登录可领）
            </legend>
            <div class="space-y-2">
              <Label>用户组</Label><MultiSelect v-model="form.conditions.groupIds" :options="groups" placeholder="全部用户组" />
            </div>
            <div class="space-y-2">
              <Label>指定用户</Label><MultiSelect v-model="form.conditions.userIds" :options="users" placeholder="不限制用户" />
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <div class="space-y-2">
                <Label for="registered-after">注册时间不早于</Label><input id="registered-after" v-model="form.conditions.registeredAfter" type="datetime-local" class="w-full rounded-md border bg-transparent p-2">
              </div><div class="space-y-2">
                <Label for="registered-before">注册时间早于</Label><input id="registered-before" v-model="form.conditions.registeredBefore" type="datetime-local" class="w-full rounded-md border bg-transparent p-2">
              </div>
            </div>
            <div class="space-y-2">
              <Label for="event-spend">累计模型消费至少</Label><Input id="event-spend" v-model="form.conditions.minSpend" inputmode="decimal" /><p class="text-muted-foreground text-xs">
                包含钱包及套餐模型消费；购买套餐本身不计入。0 为不限。
              </p>
            </div>
            <label class="flex items-center gap-2 text-sm"><input v-model="form.conditions.requirePurchase" type="checkbox">必须有付费套餐购买记录</label>
          </fieldset>
          <div class="flex gap-3">
            <Button type="submit" :disabled="saving">
              {{ saving ? '保存中…' : '保存活动' }}
            </Button><Button type="button" variant="outline" @click="form = null">
              取消
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
    <div class="grid gap-4 md:grid-cols-2">
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
    <PlanFormSheet v-model:open="showPlan" :plan="null" activity @saved="planSaved" />
  </div>
</template>
