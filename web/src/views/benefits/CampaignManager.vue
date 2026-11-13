<script setup lang="ts">
import type { Campaign } from '@/lib/benefits'
import type { Plan } from '@/lib/types'
import { ref, toRaw, watch } from 'vue'
import { Plus, Trash2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import AudienceFilterFields from '@/components/AudienceFilterFields.vue'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import PlanFormSheet from '@/views/billing/plans/PlanFormSheet.vue'
import CampaignRewardFields from './CampaignRewardFields.vue'
import { normalizeAudience } from '@/lib/audience'
import { errorMessage } from '@/lib/api'
import { benefitsApi, benefitDateInput, benefitDateISO, campaignRewards, campaignRewardTitle, emptyCampaign, emptyReward } from '@/lib/benefits'
import { fetchAllPlans } from '@/lib/endpoints'
const props = defineProps<{ startWithCreate: boolean }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [] }>()
const items = ref<Campaign[]>([])
const plans = ref<Plan[]>([])
const form = ref<Campaign | null>(null)
const saving = ref(false)
const error = ref('')
const showPlan = ref(false)
const planIndex = ref(0)
function edit(c?: Campaign) {
  const value = structuredClone(toRaw(c ?? emptyCampaign()))
  value.startsAt = benefitDateInput(value.startsAt)
  value.endsAt = benefitDateInput(value.endsAt)
  value.conditions = normalizeAudience(value.conditions)
  value.rewards = campaignRewards(value)
  form.value = value
  error.value = ''
}
async function load() {
  try { const [events, catalog] = await Promise.all([benefitsApi.adminList(), fetchAllPlans()]); items.value = events.items; plans.value = catalog }
  catch (err) { error.value = errorMessage(err) }
}
watch(open, (value) => { if (value) { form.value = null; error.value = ''; if (props.startWithCreate) edit(); void load() } })
async function save() {
  if (!form.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const body: Campaign = { ...form.value }
    body.startsAt = benefitDateISO(body.startsAt)
    body.endsAt = benefitDateISO(body.endsAt)
    if (!body.rewards?.some(r => r.type === 'subscription')) body.price = '0'
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
  const reward = form.value?.rewards?.[planIndex.value]
  if (reward?.type === 'subscription') reward.planId = plan.id
}
async function share(c: Campaign) {
  try { await navigator.clipboard.writeText(location.origin + '/benefits/' + c.id); toast.success('活动链接已复制') }
  catch { toast.error('复制失败') }
}
</script>
<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90dvh] overflow-y-auto sm:max-w-3xl">
      <DialogHeader><DialogTitle>{{ form ? (form.id ? '编辑福利活动' : '新建福利活动') : '管理福利活动' }}</DialogTitle><DialogDescription>一次领取整组奖励，可设置一项或多项福利，统一校验参与条件与库存。</DialogDescription></DialogHeader>
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
          <Label for="event-description">活动介绍</Label><Textarea id="event-description" v-model="form.description" rows="4" maxlength="10000" />
        </div>
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
        <section class="space-y-3" aria-label="活动奖励组合">
          <div class="flex items-center justify-between gap-3">
            <h3 class="font-medium">
              奖励组合
            </h3><span class="text-muted-foreground text-xs">一次领取全部奖励 · 最多 10 项</span>
          </div>
          <Card v-for="(_, index) in form.rewards" :key="index">
            <CardHeader class="flex-row items-center justify-between">
              <CardTitle class="text-sm">
                奖励 {{ index + 1 }}
              </CardTitle><Button type="button" variant="ghost" size="sm" :disabled="(form.rewards?.length ?? 0) <= 1" @click="form.rewards?.splice(index, 1)">
                <Trash2 class="size-4" />移除
              </Button>
            </CardHeader><CardContent><CampaignRewardFields v-if="form.rewards?.[index]" v-model="form.rewards[index]!" :plans="plans" @create-plan="planIndex = index; showPlan = true" /></CardContent>
          </Card>
          <Button type="button" variant="outline" :disabled="(form.rewards?.length ?? 0) >= 10" @click="form.rewards?.push(emptyReward())">
            <Plus class="size-4" />添加一项奖励
          </Button>
        </section>
        <div v-if="form.rewards?.some(r => r.type === 'subscription')" class="space-y-2">
          <Label for="event-price">整组售价（0 为免费）</Label><Input id="event-price" v-model="form.price" inputmode="decimal" required /><p class="text-muted-foreground text-xs">
            只收取一次整组售价。套餐奖励还受套餐自身库存限制。
          </p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label for="event-stock">剩余活动库存（组）</Label><Input id="event-stock" :model-value="form.stock" type="number" min="-1" step="1" required @update:model-value="value => { if (form) form.stock = Number(value) }" /><p class="text-muted-foreground text-xs">
              -1 不限；0 停止领取。每次领取消耗一组库存。
            </p>
          </div>
          <div class="space-y-2">
            <Label for="event-limit">每人最多领取次数</Label><Input id="event-limit" :model-value="form.perUserLimit" type="number" min="1" max="1000" required @update:model-value="value => { if (form) form.perUserLimit = Number(value) }" />
          </div>
          <div v-for="field in [{ key: 'startsAt' as const, label: '开始时间（北京时间，可留空）' }, { key: 'endsAt' as const, label: '结束时间（北京时间，可留空）' }]" :key="field.key" class="space-y-2">
            <Label :for="'event-' + field.key">{{ field.label }}</Label><Input :id="'event-' + field.key" :model-value="form[field.key] ?? ''" type="datetime-local" @update:model-value="value => { if (form) form[field.key] = String(value) || null }" />
          </div>
        </div>
        <fieldset class="space-y-4 rounded-lg border p-4">
          <legend class="px-2 text-sm font-medium">
            领取条件（整组共用）
          </legend><AudienceFilterFields v-model="form.conditions" :plans="plans" />
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
              {{ c.status === 'active' ? '已发布' : c.status === 'draft' ? '草稿' : '已下线' }} · {{ campaignRewardTitle(c) }}
            </p><p class="text-sm">
              库存 {{ c.stock === -1 ? '不限' : c.stock }} 组 · 每人 {{ c.perUserLimit }} 次
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
