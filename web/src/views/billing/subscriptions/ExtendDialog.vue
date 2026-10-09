<script setup lang="ts">
import type { ExtendUnit, TargetMode } from '@/lib/subscriptionBulk'
import type { BulkSubscriptionResult, ExtendSubscriptionsInput, Plan, Subscription } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { ArrowRight, Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useDryRun } from '@/composables/useDryRun'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { plansApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { formatDuration } from '@/lib/quota'
import {
  ALL_PLANS,
  BULK_NOTE_MAX,
  bodyKey,
  buildDuration,
  buildExtendInput,
  buildTarget,
  EXTEND_UNIT_LABELS,
  extendDurationError,
  extendedEnd,
  noteError,
  targetIsEmpty,
} from '@/lib/subscriptionBulk'
import BulkTargetFields from './BulkTargetFields.vue'
import DryRunPreview from './DryRunPreview.vue'

/** 延长有效期 (phase7-api.md §3.2): moves `endsAt` of active subscriptions by a duration. */
const props = defineProps<{
  selected: Subscription[]
  plans: Plan[]
  single?: Subscription | null
  initialPlanId?: string
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ done: [result: BulkSubscriptionResult] }>()

const UNITS: ExtendUnit[] = ['d', 'h']
const PRESETS: { label: string, n: number, unit: ExtendUnit }[] = [
  { label: '1 天', n: 1, unit: 'd' },
  { label: '3 天', n: 3, unit: 'd' },
  { label: '7 天', n: 7, unit: 'd' },
  { label: '30 天', n: 30, unit: 'd' },
]

const mode = ref<TargetMode>('plan')
const planId = ref(ALL_PLANS)
const amount = ref<string | number>('7')
const unit = ref<ExtendUnit>('d')
const note = ref('')
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

watch(open, (v) => {
  if (!v)
    return
  mode.value = props.single || props.selected.length ? 'selected' : 'plan'
  planId.value = props.initialPlanId && props.plans.some(p => p.id === props.initialPlanId) ? props.initialPlanId : ALL_PLANS
  amount.value = '7'
  unit.value = 'd'
  note.value = ''
  errors.value = {}
  formError.value = null
}, { flush: 'sync' })

const effMode = computed<TargetMode>(() => (props.single ? 'selected' : mode.value))
const target = computed(() => buildTarget(effMode.value, (props.single ? [props.single] : props.selected).map(s => s.id), planId.value))
const durationErr = computed(() => extendDurationError(amount.value, unit.value))
const duration = computed(() => (durationErr.value ? null : buildDuration(amount.value, unit.value)))

const body = computed<ExtendSubscriptionsInput | null>(() =>
  targetIsEmpty(target.value) || !duration.value ? null : buildExtendInput(target.value, duration.value, note.value))
// The affected set depends on the target only.
const dry = useDryRun(body, b => bodyKey(b.target), b => plansApi.extend(b, { dryRun: true }), open)
const canSubmit = computed(() => !!body.value && dry.ready.value && (dry.preview.value?.affected ?? 0) > 0 && !saving.value)

const newEnd = computed(() => (props.single && duration.value ? extendedEnd(props.single.endsAt, duration.value) : null))

function setUnit(v: unknown) {
  if (v === 'h' || v === 'd')
    unit.value = v
}
function preset(p: { n: number, unit: ExtendUnit }) {
  amount.value = String(p.n)
  unit.value = p.unit
}

async function submit() {
  formError.value = null
  const e: Record<string, string> = {}
  if (durationErr.value)
    e.duration = durationErr.value
  const ne = noteError(note.value)
  if (ne)
    e.note = ne
  errors.value = e
  if (Object.keys(e).length || !body.value || !canSubmit.value)
    return
  saving.value = true
  try {
    const res = await plansApi.extend(body.value)
    toast.success(`已为 ${res.affected} 份订阅延长 ${formatDuration(body.value.duration)}`, { description: '受影响的用户会收到通知。' })
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
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="extend-dialog">
      <DialogHeader>
        <DialogTitle>延长有效期</DialogTitle>
        <DialogDescription>
          把所选<strong>有效</strong>订阅的到期时间统一延后（规则与用量不变）。操作写入审计，并通知受影响的用户。
        </DialogDescription>
      </DialogHeader>

      <form id="extend-form" class="space-y-4" novalidate @submit.prevent="submit">
        <BulkTargetFields v-model:mode="mode" v-model:plan-id="planId" :selected-count="selected.length" :plans="plans" :single="single" id-prefix="extend" />

        <FormField label="延长时长" for="extend-amount" required :error="errors.duration ?? (amount === '' ? null : durationErr)" hint="1 小时到 366 天。">
          <div class="space-y-2">
            <div class="flex gap-2">
              <Input id="extend-amount" v-model="amount" type="number" min="1" step="1" class="min-w-20 flex-1 tabular-nums" :aria-invalid="!!durationErr" data-testid="extend-amount" />
              <Select :model-value="unit" @update:model-value="setUnit">
                <SelectTrigger class="w-24 shrink-0" aria-label="时长单位" data-testid="extend-unit">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="u in UNITS" :key="u" :value="u">
                    {{ EXTEND_UNIT_LABELS[u] }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="flex flex-wrap gap-1.5">
              <button
                v-for="p in PRESETS"
                :key="p.label"
                type="button"
                class="rounded-md border px-2 py-0.5 text-xs outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
                :class="String(amount) === String(p.n) && unit === p.unit ? 'border-primary bg-primary text-primary-foreground' : 'hover:bg-muted'"
                @click="preset(p)"
              >
                {{ p.label }}
              </button>
            </div>
          </div>
        </FormField>

        <p v-if="single && newEnd" class="bg-muted/40 flex flex-wrap items-center gap-1.5 rounded-lg border px-3 py-2 text-xs tabular-nums" data-testid="extend-new-end">
          <span class="text-muted-foreground">到期时间</span>
          <span>{{ formatDateTime(single.endsAt) }}</span>
          <ArrowRight class="text-muted-foreground size-3.5" />
          <span class="font-medium">{{ formatDateTime(newEnd) }}</span>
        </p>

        <FormField label="备注" for="extend-note" :error="errors.note" :hint="`写入审计日志与用户通知 · 最多 ${BULK_NOTE_MAX} 字`">
          <Textarea id="extend-note" v-model="note" rows="2" :maxlength="BULK_NOTE_MAX" placeholder="例如：服务中断 2 天，全员补偿" />
        </FormField>

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
        <Button type="submit" form="extend-form" :disabled="!canSubmit" data-testid="extend-submit">
          <Loader2 v-if="saving" class="animate-spin" />
          确认延长{{ dry.ready.value && dry.preview.value ? `（${dry.preview.value.affected}）` : '' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
