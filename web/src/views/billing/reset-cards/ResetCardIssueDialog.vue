<script setup lang="ts">
import type { CardTargetType, IssueForm } from '@/lib/resetCards'
import type { BulkSubscriptionResult, Plan, ResetCardBatch, ResetCardIssueInput, UserGroup } from '@/lib/types'
import { computed, reactive, ref, watch } from 'vue'
import { Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { useDryRun } from '@/composables/useDryRun'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { adminResetCardsApi, fetchAllPlans, groupsApi } from '@/lib/endpoints'
import {
  buildIssueInput,
  CARD_NOTE_MAX,
  CARD_QUANTITY_MAX,
  CARD_TARGET_LABELS,
  emptyIssueForm,
  issueFormErrors,
  issueKey,
  RESET_CARD_KIND_HINTS,
  RESET_CARD_KIND_LABELS,
  RESET_CARD_KINDS,
  recipientsText,
} from '@/lib/resetCards'
import { useAuthStore } from '@/stores/auth'
import DryRunPreview from '../subscriptions/DryRunPreview.vue'

/** 发放重置卡 (phase11-api.md §2.1): a dry run counts the recipients before issuing. */
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ issued: [batch: ResetCardBatch] }>()

const auth = useAuthStore()
const canListGroups = computed(() => auth.can('users.read'))

const form = reactive<IssueForm>(emptyIssueForm())
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

const plans = ref<Plan[]>([])
const groups = ref<UserGroup[]>([])
const refsLoading = ref(false)
const refsError = ref<string | null>(null)
async function loadRefs() {
  refsLoading.value = true
  refsError.value = null
  try {
    const [ps, gs] = await Promise.all([fetchAllPlans(), canListGroups.value ? groupsApi.list() : Promise.resolve({ items: [] })])
    plans.value = ps
    groups.value = gs.items
  }
  catch (err) {
    refsError.value = errorMessage(err)
  }
  finally {
    refsLoading.value = false
  }
}

watch(open, (v) => {
  if (!v)
    return
  Object.assign(form, emptyIssueForm())
  errors.value = {}
  formError.value = null
  void loadRefs()
}, { flush: 'sync' })

const TARGET_TYPES: CardTargetType[] = ['users', 'group', 'plan', 'all']
function setKind(v: unknown) {
  if (typeof v === 'string' && (RESET_CARD_KINDS as string[]).includes(v))
    form.kind = v as IssueForm['kind']
}
function setTargetType(v: unknown) {
  if (typeof v === 'string' && (TARGET_TYPES as string[]).includes(v))
    form.targetType = v as CardTargetType
}
function planLabel(p: Plan): string {
  return `${p.name}${p.status === 'archived' ? '（已下架）' : ''}`
}
const planOptions = computed(() => plans.value.map(p => ({ value: p.id, label: planLabel(p) })))

const body = computed<ResetCardIssueInput | null>(() => buildIssueInput(form))
async function dryRun(b: ResetCardIssueInput): Promise<BulkSubscriptionResult> {
  const res = await adminResetCardsApi.issue(b, { dryRun: true })
  return { affected: res.recipients, subscriptions: [] }
}
const dry = useDryRun(body, issueKey, dryRun, open)
const recipients = computed(() => (dry.ready.value ? dry.preview.value?.affected ?? 0 : 0))
const quantity = computed(() => Number(form.quantity) || 0)
const canSubmit = computed(() => !!body.value && dry.ready.value && recipients.value > 0 && !saving.value)
function previewText(n: number): string {
  return recipientsText(n, n * quantity.value)
}

async function submit() {
  formError.value = null
  errors.value = issueFormErrors(form)
  if (Object.keys(errors.value).length) {
    formError.value = '请修正标记的字段'
    return
  }
  if (!body.value || !canSubmit.value)
    return
  saving.value = true
  try {
    const res = await adminResetCardsApi.issue(body.value)
    toast.success(`已向 ${res.recipients} 位用户发放 ${res.cards} 张${RESET_CARD_KIND_LABELS[form.kind]}`, { description: '用户会收到站内通知（以及按其偏好发送的邮件）。' })
    if (res.batch)
      emit('issued', res.batch)
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
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-xl" data-testid="card-issue-dialog">
      <DialogHeader>
        <DialogTitle>发放重置卡</DialogTitle>
        <DialogDescription>
          用户在「钱包与订阅」中对自己的订阅使用重置卡：对应额度立即清零，5 小时 / 每周窗口从使用时起重新计时。卡按窗口时长匹配规则，与规则 ID 无关。
        </DialogDescription>
      </DialogHeader>

      <form id="card-issue-form" class="space-y-4" novalidate @submit.prevent="submit">
        <FormField label="卡类型" required :hint="RESET_CARD_KIND_HINTS[form.kind]">
          <Tabs :model-value="form.kind" @update:model-value="setKind">
            <TabsList class="w-full">
              <TabsTrigger v-for="k in RESET_CARD_KINDS" :key="k" :value="k" :data-testid="`card-kind-${k}`">
                {{ RESET_CARD_KIND_LABELS[k] }}
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </FormField>

        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="每人数量" for="card-qty" required :error="errors.quantity" :hint="`1–${CARD_QUANTITY_MAX} 张`">
            <Input id="card-qty" v-model="form.quantity" type="number" min="1" :max="CARD_QUANTITY_MAX" step="1" class="tabular-nums" />
          </FormField>
          <FormField label="过期时间" for="card-exp" :error="errors.expiresAt" hint="留空表示永不过期">
            <Input id="card-exp" v-model="form.expiresAt" type="datetime-local" />
          </FormField>
        </div>

        <FormField label="发放对象" for="card-target" required :error="errors.target ?? refsError">
          <Select :model-value="form.targetType" @update:model-value="setTargetType">
            <SelectTrigger id="card-target" class="w-full" data-testid="card-target">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="t in TARGET_TYPES" :key="t" :value="t" :disabled="t === 'group' && !canListGroups">
                {{ CARD_TARGET_LABELS[t] }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <div v-if="form.targetType === 'users'" class="space-y-1.5">
          <UserPicker id="card-users" v-model="form.userIds" />
          <p class="text-muted-foreground text-xs">
            已选 {{ form.userIds.length }} 位（最多 1000 位）；停用的用户不会收到。
          </p>
        </div>
        <FormField v-else-if="form.targetType === 'group'" label="用户组" for="card-group" required>
          <Select v-model="form.groupId" :disabled="refsLoading">
            <SelectTrigger id="card-group" class="w-full">
              <SelectValue :placeholder="refsLoading ? '加载中…' : '选择用户组'" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="g in groups" :key="g.id" :value="g.id">
                {{ g.name }}（{{ g.members }} 人）
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField v-else-if="form.targetType === 'plan'" label="套餐" for="card-plan" required hint="发给持有该套餐有效订阅的用户。">
          <Select v-model="form.planId" :disabled="refsLoading">
            <SelectTrigger id="card-plan" class="w-full">
              <SelectValue :placeholder="refsLoading ? '加载中…' : '选择套餐'" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="p in plans" :key="p.id" :value="p.id">
                {{ planLabel(p) }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <p v-else class="text-muted-foreground rounded-lg border px-3 py-2 text-xs">
          发给发放时的全部正常状态用户；之后注册的用户不会收到。
        </p>

        <FormField label="限定套餐" for="card-plans" :error="errors.planIds" hint="不选表示可用于任何套餐的订阅。">
          <MultiSelect id="card-plans" v-model="form.planIds" :options="planOptions" :loading="refsLoading" empty-label="不限套餐" placeholder="搜索套餐" />
        </FormField>

        <FormField label="备注" for="card-note" :error="errors.note" :hint="`显示在用户的卡片与通知中 · 最多 ${CARD_NOTE_MAX} 字`">
          <Textarea id="card-note" v-model="form.note" rows="2" :maxlength="CARD_NOTE_MAX" placeholder="例如：国庆福利" />
        </FormField>

        <DryRunPreview
          :idle="!body"
          :loading="dry.loading.value"
          :error="dry.error.value"
          :ready="dry.ready.value"
          :affected="dry.preview.value?.affected ?? null"
          :format="previewText"
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
        <Button type="submit" form="card-issue-form" :disabled="!canSubmit" data-testid="card-issue-submit">
          <Loader2 v-if="saving" class="animate-spin" />
          发放{{ canSubmit ? `（${recipients} 人）` : '' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
