<script setup lang="ts">
import type { TargetMode } from '@/lib/subscriptionBulk'
import type { BulkSubscriptionResult, Plan, ResetQuotaInput, Subscription } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Loader2, TriangleAlert } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useDryRun } from '@/composables/useDryRun'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { plansApi } from '@/lib/endpoints'
import {
  ALL_PLANS,
  BULK_NOTE_MAX,
  bodyKey,
  buildResetInput,
  buildTarget,
  lifetimeWarning,
  noteError,
  ruleOptionsForTarget,
  targetIsEmpty,
} from '@/lib/subscriptionBulk'
import BulkTargetFields from './BulkTargetFields.vue'
import DryRunPreview from './DryRunPreview.vue'

/** 重置额度 (phase7-api.md §3.1): clears the current window usage of the chosen rules. */
const props = defineProps<{
  /** Checked active rows. */
  selected: Subscription[]
  plans: Plan[]
  /** Row action: only this subscription. */
  single?: Subscription | null
  /** Preselected plan (list filter) for the "按套餐" mode. */
  initialPlanId?: string
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ done: [result: BulkSubscriptionResult] }>()

const mode = ref<TargetMode>('plan')
const planId = ref(ALL_PLANS)
const rules = ref<string[]>([])
const includeLifetime = ref(false)
const note = ref('')
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

watch(open, (v) => {
  if (!v)
    return
  mode.value = props.single || props.selected.length ? 'selected' : 'plan'
  planId.value = props.initialPlanId && props.plans.some(p => p.id === props.initialPlanId) ? props.initialPlanId : ALL_PLANS
  rules.value = []
  includeLifetime.value = false
  note.value = ''
  errors.value = {}
  formError.value = null
}, { flush: 'sync' })

const effMode = computed<TargetMode>(() => (props.single ? 'selected' : mode.value))
const sources = computed(() => (props.single ? [props.single] : props.selected))
const target = computed(() => buildTarget(effMode.value, sources.value.map(s => s.id), planId.value))
const options = computed(() => ruleOptionsForTarget(effMode.value, sources.value, props.plans, planId.value))
watch(options, (o) => {
  const ids = new Set(o.map(x => x.id))
  if (rules.value.some(r => !ids.has(r)))
    rules.value = rules.value.filter(r => ids.has(r))
})
const ruleSelectOptions = computed(() => options.value.map(o => ({
  value: o.id,
  label: o.label,
  hint: o.lifetimeOnly ? `${o.id} · 总量` : o.lifetime ? `${o.id} · 部分套餐为总量` : o.id,
})))
const warning = computed(() => lifetimeWarning(options.value, rules.value.length ? rules.value : null, includeLifetime.value))
const hasLifetime = computed(() => options.value.some(o => o.lifetime))

const body = computed<ResetQuotaInput | null>(() =>
  targetIsEmpty(target.value) ? null : buildResetInput(target.value, rules.value.length ? rules.value : null, includeLifetime.value, note.value))
const dry = useDryRun(body, b => bodyKey({ ...b, note: '' }), b => plansApi.resetQuota(b, { dryRun: true }), open)
const canSubmit = computed(() => !!body.value && dry.ready.value && (dry.preview.value?.affected ?? 0) > 0 && !saving.value)

async function submit() {
  formError.value = null
  const ne = noteError(note.value)
  errors.value = ne ? { note: ne } : {}
  if (ne || !body.value || !canSubmit.value)
    return
  saving.value = true
  try {
    const res = await plansApi.resetQuota(body.value)
    toast.success(`已重置 ${res.affected} 份订阅的额度`, { description: '受影响的用户会收到通知，被阻断的请求立即恢复。' })
    emit('done', res)
    open.value = false
  }
  catch (err) {
    if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
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
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="reset-dialog">
      <DialogHeader>
        <DialogTitle>重置套餐额度</DialogTitle>
        <DialogDescription>
          清空所选订阅在<strong>当前窗口</strong>内的用量（会话窗口从现在起重新计时，例如 5 小时窗口在 5 小时后刷新），被阻断的用户立即恢复可用。操作写入审计，并通知受影响的用户。
        </DialogDescription>
      </DialogHeader>

      <form id="reset-form" class="space-y-4" novalidate @submit.prevent="submit">
        <BulkTargetFields v-model:mode="mode" v-model:plan-id="planId" :selected-count="selected.length" :plans="plans" :single="single" id-prefix="reset" />

        <FormField label="规则" for="reset-rules" :error="errors.rules" :hint="options.length ? '不选表示全部规则。' : '目标订阅没有可重置的规则。'">
          <MultiSelect id="reset-rules" v-model="rules" :options="ruleSelectOptions" empty-label="全部规则" placeholder="搜索规则" data-testid="reset-rules" />
        </FormField>

        <div class="space-y-2 rounded-lg border p-3">
          <div class="flex items-center justify-between gap-3">
            <Label for="reset-lifetime" class="flex-col items-start gap-0.5">
              <span>包含总量规则</span>
              <span class="text-muted-foreground text-xs font-normal">「订阅期内总量」（lifetime）规则默认不重置</span>
            </Label>
            <Switch id="reset-lifetime" v-model="includeLifetime" data-testid="reset-lifetime" />
          </div>
          <p v-if="includeLifetime" class="text-destructive flex gap-1.5 text-xs" role="alert">
            <TriangleAlert class="mt-px size-3.5 shrink-0" />
            <span>总量规则（如按次包）的累计用量将被清零，相当于再次发放整份额度，且无法撤销。</span>
          </p>
          <p v-else-if="!hasLifetime && options.length" class="text-muted-foreground text-xs">
            目标订阅没有总量规则。
          </p>
        </div>

        <FormField label="备注" for="reset-note" :error="errors.note" :hint="`写入审计日志与用户通知 · 最多 ${BULK_NOTE_MAX} 字`">
          <Textarea id="reset-note" v-model="note" rows="2" :maxlength="BULK_NOTE_MAX" placeholder="例如：故障补偿，重置本周额度" />
        </FormField>

        <p v-if="warning" class="flex gap-1.5 text-xs text-amber-700 dark:text-amber-400" role="status">
          <TriangleAlert class="mt-px size-3.5 shrink-0" />
          <span>{{ warning }}</span>
        </p>

        <DryRunPreview
          :idle="!body"
          :loading="dry.loading.value"
          :error="dry.error.value"
          :ready="dry.ready.value"
          :affected="dry.preview.value?.affected ?? null"
          @retry="dry.refresh"
        />
      </form>

      <DialogFooter class="items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="reset-form" :variant="includeLifetime ? 'destructive' : 'default'" :disabled="!canSubmit" data-testid="reset-submit">
          <Loader2 v-if="saving" class="animate-spin" />
          确认重置{{ dry.ready.value && dry.preview.value ? `（${dry.preview.value.affected}）` : '' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
