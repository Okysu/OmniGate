<script setup lang="ts">
import { emptyAudience } from '@/lib/audience'
import type { AdjustQuotaInput, Plan, Subscription } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { useDryRun } from '@/composables/useDryRun'
import { errorMessage, fieldErrors } from '@/lib/api'
import { plansApi } from '@/lib/endpoints'
import { bodyKey, buildTarget, ruleOptionsForTarget, targetIsEmpty } from '@/lib/subscriptionBulk'
import type { TargetMode } from '@/lib/subscriptionBulk'
import BulkTargetFields from './BulkTargetFields.vue'
import DryRunPreview from './DryRunPreview.vue'

const props = defineProps<{ selected: Subscription[], plans: Plan[], single?: Subscription | null, initialPlanId?: string }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ done: [] }>()
const mode = ref<TargetMode>('selected')
const planIds = ref<string[]>([])
const groupIds = ref<string[]>([])
const audience = ref(emptyAudience())
const ruleId = ref('')
const limit = ref('')
const remaining = ref('')
const note = ref('')
const saving = ref(false)
const error = ref<string | null>(null)
const errors = ref<Record<string, string>>({})
const sources = computed(() => props.single ? [props.single] : props.selected)
const effectiveMode = computed(() => props.single ? 'selected' : mode.value)
const options = computed(() => ruleOptionsForTarget(effectiveMode.value, sources.value, props.plans, planIds.value))
watch(open, (v) => {
  if (!v)
    return
  mode.value = props.single || props.selected.length ? 'selected' : 'plan'
  planIds.value = props.initialPlanId ? [props.initialPlanId] : []
  groupIds.value = []
  audience.value = emptyAudience()
  ruleId.value = options.value[0]?.id ?? ''
  limit.value = ''
  remaining.value = ''
  note.value = ''
  error.value = null
  errors.value = {}
}, { flush: 'sync' })
watch(options, (v) => {
  if (!v.some(r => r.id === ruleId.value))
    ruleId.value = v[0]?.id ?? ''
})
const currentRule = computed(() => props.single?.rules.find(r => r.id === ruleId.value))
const body = computed<AdjustQuotaInput | null>(() => {
  const target = buildTarget(effectiveMode.value, sources.value.map(s => s.id), planIds.value, groupIds.value, audience.value)
  if (targetIsEmpty(target) || !ruleId.value || (!limit.value.trim() && !remaining.value.trim()))
    return null
  return { target, ruleId: ruleId.value, ...(limit.value.trim() ? { limit: limit.value.trim() } : {}), ...(remaining.value.trim() ? { remaining: remaining.value.trim() } : {}), note: note.value.trim() }
})
const dry = useDryRun(body, b => bodyKey({ ...b, note: '' }), b => plansApi.adjustQuota(b, { dryRun: true }), open)
const canSubmit = computed(() => !!body.value && dry.ready.value && (dry.preview.value?.affected ?? 0) > 0 && !saving.value)
async function submit() {
  if (!body.value || !canSubmit.value)
    return
  saving.value = true
  error.value = null
  try {
    const res = await plansApi.adjustQuota(body.value)
    toast.success(`已调整 ${res.affected} 份订阅的额度`)
    emit('done')
    open.value = false
  }
  catch (err) {
    error.value = errorMessage(err)
    errors.value = fieldErrors(err)
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="adjust-quota-dialog">
      <DialogHeader>
        <DialogTitle>调整已有订阅额度</DialogTitle>
        <DialogDescription>修改已开通订阅的额度上限或当前余量，保留原刷新时间与到期时间。只改上限时保留已用量；填写余量时调整当前窗口用量。操作写入审计。</DialogDescription>
      </DialogHeader>
      <form id="adjust-quota-form" class="space-y-4" @submit.prevent="submit">
        <BulkTargetFields v-model:filter="audience" v-model:mode="mode" v-model:plan-ids="planIds" v-model:group-ids="groupIds" :selected-count="selected.length" :plans="plans" :single="single" id-prefix="adjust" />
        <FormField label="额度规则" for="adjust-rule" :error="errors.ruleId" required>
          <Select v-model="ruleId">
            <SelectTrigger id="adjust-rule" class="w-full">
              <SelectValue />
            </SelectTrigger><SelectContent>
              <SelectItem v-for="r in options" :key="r.id" :value="r.id">
                {{ r.label }} · {{ r.id }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <p v-if="currentRule" class="text-muted-foreground text-sm">
          当前上限 {{ currentRule.limit }}，当前余量 {{ currentRule.remaining }}。
        </p>
        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="新上限（可选）" for="adjust-limit" :error="errors.limit" hint="只改上限时保留已用量">
            <Input id="adjust-limit" v-model="limit" inputmode="decimal" placeholder="保持原上限" :disabled="saving" />
          </FormField>
          <FormField label="当前余量（可选）" for="adjust-remaining" :error="errors.remaining" hint="只调整当前窗口；不能超过上限">
            <Input id="adjust-remaining" v-model="remaining" inputmode="decimal" placeholder="保留原用量计算" :disabled="saving" />
          </FormField>
        </div>
        <p class="text-muted-foreground text-xs">
          金额规则的单位为站点结算货币，其他规则沿用次数或 Token 等原计量单位。尚未开始的会话可先修改上限，首次使用后再调整余量。
        </p>
        <FormField label="备注（可选）" for="adjust-note" :error="errors.note">
          <Textarea id="adjust-note" v-model="note" maxlength="200" rows="2" />
        </FormField>
        <DryRunPreview :idle="!body" :loading="dry.loading.value" :ready="dry.ready.value" :affected="dry.preview.value?.affected ?? null" :error="dry.error.value" @retry="dry.refresh" />
        <p v-if="error" class="text-destructive text-sm" role="alert">
          {{ error }}
        </p>
      </form>
      <DialogFooter>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button><Button form="adjust-quota-form" type="submit" :disabled="!canSubmit">
          <Loader2 v-if="saving" class="animate-spin" />保存额度
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
