<script setup lang="ts">
import type { Plan, RedeemBatchCreated, RedeemBatchInput, RedeemBatchKind } from '@/lib/types'
import { computed, reactive, ref, watch } from 'vue'
import { Loader2 } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { adminBillingApi, fetchAllPlans } from '@/lib/endpoints'
import { amountSign, isValidAmount } from '@/lib/money'
import { formatDuration, PLAN_ERROR_MESSAGES, rulesSummary } from '@/lib/quota'
import { fromLocalInput } from '@/lib/timeRange'

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ created: [result: RedeemBatchCreated] }>()
const { currency, money } = useCurrency()

const form = reactive({
  kind: 'wallet_credit' as RedeemBatchKind,
  amount: '',
  planId: '',
  periods: 1 as number | string,
  count: 10 as number | string,
  maxRedemptionsPerCode: 1 as number | string,
  perUserLimit: 1 as number | string,
  validFrom: '',
  expiresAt: '',
  note: '',
})
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

watch(open, (v) => {
  if (!v)
    return
  Object.assign(form, { kind: 'wallet_credit', amount: '', planId: '', periods: 1, count: 10, maxRedemptionsPerCode: 1, perUserLimit: 1, validFrom: '', expiresAt: '', note: '' })
  errors.value = {}
  formError.value = null
  void loadPlans()
})

// ---------- plans (kind = plan) ----------
const plans = ref<Plan[]>([])
const plansLoading = ref(false)
const plansError = ref<string | null>(null)
async function loadPlans() {
  plansLoading.value = true
  plansError.value = null
  try {
    plans.value = await fetchAllPlans('active')
  }
  catch (err) {
    plansError.value = errorMessage(err)
  }
  finally {
    plansLoading.value = false
  }
}
const plan = computed(() => plans.value.find(p => p.id === form.planId) ?? null)
function setKind(v: unknown) {
  if (v === 'wallet_credit' || v === 'plan') {
    form.kind = v
    errors.value = {}
  }
}

function intIn(v: number | string, min: number, max: number): boolean {
  const n = Number(v)
  return v !== '' && Number.isInteger(n) && n >= min && n <= max
}

function validate(): Record<string, string> {
  const e: Record<string, string> = {}
  if (form.kind === 'wallet_credit' && (!isValidAmount(form.amount) || amountSign(form.amount) <= 0))
    e.amount = '请输入大于 0 的金额（最多 9 位小数）'
  if (form.kind === 'plan') {
    if (!form.planId)
      e.planId = '请选择套餐'
    if (!intIn(form.periods, 1, 120))
      e.periods = '份数为 1–120 的整数'
  }
  if (!intIn(form.count, 1, 1000))
    e.count = '数量为 1–1000'
  if (!intIn(form.maxRedemptionsPerCode, 1, 1_000_000))
    e.maxRedemptionsPerCode = '范围为 1–1000000'
  if (!intIn(form.perUserLimit, 1, 1_000_000))
    e.perUserLimit = '范围为 1–1000000'
  const vf = fromLocalInput(form.validFrom)
  const ex = fromLocalInput(form.expiresAt)
  if (ex && new Date(ex).getTime() <= Date.now())
    e.expiresAt = '必须晚于当前时间'
  if (vf && ex && new Date(ex) <= new Date(vf))
    e.expiresAt = '必须晚于生效时间'
  if (form.note.length > 200)
    e.note = '最多 200 个字符'
  return e
}

async function submit() {
  formError.value = null
  errors.value = validate()
  if (Object.keys(errors.value).length) {
    formError.value = '请修正标记的字段'
    return
  }
  const body: RedeemBatchInput = {
    ...(form.kind === 'plan'
      ? { kind: 'plan', planId: form.planId, periods: Number(form.periods) }
      : { kind: 'wallet_credit', amount: form.amount.trim() }),
    count: Number(form.count),
    maxRedemptionsPerCode: Number(form.maxRedemptionsPerCode),
    perUserLimit: Number(form.perUserLimit),
  }
  const vf = fromLocalInput(form.validFrom)
  const ex = fromLocalInput(form.expiresAt)
  if (vf)
    body.validFrom = vf
  if (ex)
    body.expiresAt = ex
  if (form.note.trim())
    body.note = form.note.trim()
  saving.value = true
  try {
    emit('created', await adminBillingApi.createBatch(body))
    open.value = false
  }
  catch (err) {
    if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
    }
    else if (isApiError(err) && PLAN_ERROR_MESSAGES[err.code]) {
      errors.value = { planId: PLAN_ERROR_MESSAGES[err.code]! }
      formError.value = PLAN_ERROR_MESSAGES[err.code]!
      void loadPlans()
    }
    else {
      formError.value = errorMessage(err)
    }
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>生成兑换码</DialogTitle>
        <DialogDescription>
          {{ form.kind === 'plan' ? '每个兑换码兑换后为用户开通（或续期）指定套餐。' : '每个兑换码兑换后为钱包增加固定金额。' }}生成后明文只显示一次，请及时导出。
        </DialogDescription>
      </DialogHeader>
      <form id="batch-form" class="space-y-4" novalidate @submit.prevent="submit">
        <Tabs :model-value="form.kind" @update:model-value="setKind">
          <TabsList class="w-full">
            <TabsTrigger value="wallet_credit">
              充值余额
            </TabsTrigger>
            <TabsTrigger value="plan">
              开通套餐
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div v-if="form.kind === 'plan'" class="space-y-4">
          <div class="grid gap-4 sm:grid-cols-[minmax(0,1fr)_8rem]">
            <FormField label="套餐" for="b-plan" required :error="errors.planId ?? plansError" :hint="!plansLoading && plans.length === 0 ? '没有上架中的套餐，请先在「套餐」页面创建。' : '只能选择上架中的套餐；套餐下架后对应兑换码将无法兑换。'">
              <Select v-model="form.planId" :disabled="plansLoading">
                <SelectTrigger id="b-plan" class="w-full">
                  <SelectValue :placeholder="plansLoading ? '加载中…' : '选择套餐'" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="p in plans" :key="p.id" :value="p.id">
                    {{ p.name }}（{{ formatDuration(p.duration) }}）
                  </SelectItem>
                </SelectContent>
              </Select>
            </FormField>
            <FormField label="份数" for="b-periods" required :error="errors.periods" hint="1–120">
              <Input id="b-periods" v-model="form.periods" type="number" min="1" max="120" step="1" class="tabular-nums" />
            </FormField>
          </div>
          <div v-if="plan" class="bg-muted/40 space-y-1 rounded-lg border p-3 text-xs">
            <p>
              每个码：<strong>{{ plan.name }}</strong> × {{ form.periods || '?' }} 份（每份 {{ formatDuration(plan.duration) }}）
            </p>
            <p class="text-muted-foreground">
              {{ rulesSummary(plan.rules, money) }}
            </p>
            <p class="text-muted-foreground">
              {{ plan.stackable ? '可叠加：每次兑换生成一份新订阅。' : '不可叠加：用户已有该套餐的有效订阅时，兑换将延长其到期时间。' }}
            </p>
          </div>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField v-if="form.kind === 'wallet_credit'" :label="`面额（${currency?.code ?? '结算币种'}）`" for="b-amount" required :error="errors.amount">
            <div class="relative">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
              <Input id="b-amount" v-model="form.amount" inputmode="decimal" placeholder="10" class="pl-7 font-mono tabular-nums" />
            </div>
          </FormField>
          <FormField label="数量" for="b-count" required :error="errors.count" hint="单次最多 1000 个">
            <Input id="b-count" v-model="form.count" type="number" min="1" max="1000" step="1" />
          </FormField>
          <FormField label="每个码可兑换次数" for="b-max" :error="errors.maxRedemptionsPerCode" hint="默认 1（一次性码）">
            <Input id="b-max" v-model="form.maxRedemptionsPerCode" type="number" min="1" step="1" />
          </FormField>
          <FormField label="每用户可兑换次数" for="b-user" :error="errors.perUserLimit" hint="同一用户在本批次内的上限">
            <Input id="b-user" v-model="form.perUserLimit" type="number" min="1" step="1" />
          </FormField>
          <FormField label="生效时间" for="b-from" :error="errors.validFrom" hint="留空表示立即生效">
            <Input id="b-from" v-model="form.validFrom" type="datetime-local" />
          </FormField>
          <FormField label="过期时间" for="b-exp" :error="errors.expiresAt" hint="留空表示永不过期">
            <Input id="b-exp" v-model="form.expiresAt" type="datetime-local" />
          </FormField>
        </div>
        <FormField label="备注" for="b-note" :error="errors.note">
          <Input id="b-note" v-model="form.note" maxlength="200" placeholder="例如：10 月活动" />
        </FormField>
      </form>
      <DialogFooter class="items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="batch-form" :disabled="saving">
          <Loader2 v-if="saving" class="animate-spin" />
          生成
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
