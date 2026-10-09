<script setup lang="ts">
import type { Plan, Subscription } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Info, Loader2 } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { plansApi } from '@/lib/endpoints'
import { formatDuration, PLAN_ERROR_MESSAGES, rulesSummary } from '@/lib/quota'
import { useCurrency } from '@/composables/useCurrency'

const props = defineProps<{
  /** Active plans to choose from. */
  plans: Plan[]
  plansLoading?: boolean
  /** Preselected plan / user (e.g. from the list filters). */
  initialPlanId?: string
  initialUserId?: string
  /** Fixed user (e.g. from the user detail sheet): no picker is shown. */
  user?: { id: string, displayName: string } | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ granted: [subscription: Subscription, renewed: boolean] }>()
const { money } = useCurrency()

const userIds = ref<string[]>([])
const planId = ref('')
const periods = ref<number | string>(1)
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

watch(open, (v) => {
  if (!v)
    return
  userIds.value = props.user ? [props.user.id] : (props.initialUserId ? [props.initialUserId] : [])
  planId.value = props.initialPlanId && props.plans.some(p => p.id === props.initialPlanId) ? props.initialPlanId : ''
  periods.value = 1
  errors.value = {}
  formError.value = null
})

const plan = computed(() => props.plans.find(p => p.id === planId.value) ?? null)
const total = computed(() => {
  const n = Number(periods.value)
  if (!plan.value || !Number.isInteger(n) || n < 1)
    return null
  const m = /^(\d+)([mhd])$/.exec(plan.value.duration)
  if (!m)
    return null
  return formatDuration(`${Number(m[1]) * n}${m[2]}`)
})

function validate(): Record<string, string> {
  const e: Record<string, string> = {}
  if (!userIds.value[0])
    e.userId = '请选择用户'
  if (!planId.value)
    e.planId = '请选择套餐'
  const n = Number(periods.value)
  if (periods.value === '' || !Number.isInteger(n) || n < 1 || n > 120)
    e.periods = '份数为 1–120 的整数'
  return e
}

async function submit() {
  formError.value = null
  errors.value = validate()
  if (Object.keys(errors.value).length)
    return
  saving.value = true
  try {
    const res = await plansApi.grant({ userId: userIds.value[0]!, planId: planId.value, periods: Number(periods.value) })
    emit('granted', res.subscription, res.renewed)
    open.value = false
  }
  catch (err) {
    if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
    }
    else if (isApiError(err) && PLAN_ERROR_MESSAGES[err.code]) {
      formError.value = PLAN_ERROR_MESSAGES[err.code]!
    }
    else if (isApiError(err) && err.status === 404) {
      formError.value = `${err.message}（用户或套餐不存在）`
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
        <DialogTitle>开通 / 续期套餐</DialogTitle>
        <DialogDescription>为用户开通套餐，立即生效。有效期 = 份数 × 套餐每份有效期。</DialogDescription>
      </DialogHeader>
      <form id="grant-form" class="space-y-4" novalidate @submit.prevent="submit">
        <FormField v-if="!user" label="用户" for="grant-user" required :error="errors.userId">
          <UserPicker id="grant-user" v-model="userIds" :multiple="false" />
        </FormField>
        <div v-else class="bg-muted/40 flex items-center gap-2 rounded-lg border px-3 py-2 text-sm">
          <span class="text-muted-foreground text-xs">用户</span>
          <span class="truncate font-medium">{{ user.displayName }}</span>
        </div>
        <FormField label="套餐" for="grant-plan" required :error="errors.planId" :hint="plans.length === 0 && !plansLoading ? '没有上架中的套餐，请先在「套餐」页面创建。' : '只能开通上架中的套餐。'">
          <Select v-model="planId" :disabled="plansLoading">
            <SelectTrigger id="grant-plan" class="w-full">
              <SelectValue :placeholder="plansLoading ? '加载中…' : '选择套餐'" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="p in plans" :key="p.id" :value="p.id">
                {{ p.name }}（{{ formatDuration(p.duration) }}）
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="份数" for="grant-periods" required :error="errors.periods" :hint="total ? `共 ${total}` : '1–120'">
          <Input id="grant-periods" v-model="periods" type="number" min="1" max="120" step="1" class="tabular-nums" />
        </FormField>
        <div v-if="plan" class="bg-muted/40 space-y-1.5 rounded-lg border p-3 text-xs">
          <p class="font-medium">
            {{ plan.name }}
          </p>
          <p class="text-muted-foreground">
            {{ rulesSummary(plan.rules, money) }}
          </p>
          <p class="flex gap-1.5">
            <Info class="text-muted-foreground mt-px size-3.5 shrink-0" />
            <span v-if="plan.stackable">该套餐<strong>可叠加</strong>：每次开通都会生成一份新的独立订阅。</span>
            <span v-else>该套餐<strong>不可叠加</strong>：若用户已有该套餐的有效订阅，将<strong>延长现有订阅</strong>的到期时间（规则快照不变），否则新开通一份订阅。</span>
          </p>
        </div>
      </form>
      <DialogFooter class="items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="grant-form" :disabled="saving">
          <Loader2 v-if="saving" class="animate-spin" />
          确认开通
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
