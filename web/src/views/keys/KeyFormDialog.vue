<script setup lang="ts">
import type { MultiSelectOption } from '@/components/MultiSelect.vue'
import type { CompatMode, GatewayKey, KeyCreated, KeyPatch, KeyPolicy, KeyQuotaOverflow, QuotaOverflow, SpendWindow } from '@/lib/types'
import { computed, reactive, ref, watch } from 'vue'
import { Loader2 } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { keysApi } from '@/lib/endpoints'
import { COMPAT_MODE_DESCRIPTIONS, COMPAT_MODE_LABELS } from '@/lib/labels'
import { isSpendWindow, spendFormFrom, spendLimitFromForm, SPEND_WINDOW_LABELS, SPEND_WINDOWS, validateSpendForm, withSpendLimit } from '@/lib/limits'
import { isQuotaOverflow, KEY_QUOTA_OVERFLOW_LABELS, QUOTA_OVERFLOW_SHORT } from '@/lib/quota'
import { fromLocalInput, toLocalInput } from '@/lib/timeRange'

const props = defineProps<{
  /** Key being edited; null = create. */
  gatewayKey: GatewayKey | null
  modelOptions: MultiSelectOption[]
  channelOptions: MultiSelectOption[]
  optionsLoading?: boolean
  /** The account's 「套餐额度用完后」 setting, shown next to 「跟随账户设置」 (null = unknown). */
  accountOverflow?: QuotaOverflow | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{
  created: [result: KeyCreated]
  updated: [key: GatewayKey]
  conflict: []
}>()

const form = reactive({
  name: '',
  expiresAt: '',
  allowedModels: [] as string[],
  allowedChannels: [] as string[],
  ipAllowlist: '',
  rpm: '' as string | number,
  compatMode: 'strict' as CompatMode,
  quotaOverflow: '' as KeyQuotaOverflow,
  /** phase8 §2.1: '' = unlimited. */
  spendAmount: '',
  spendWindow: 'month' as SpendWindow,
})
const { currency } = useCurrency()
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)
const isEdit = computed(() => props.gatewayKey !== null)

watch(open, (v) => {
  if (!v)
    return
  const k = props.gatewayKey
  form.name = k?.name ?? ''
  form.expiresAt = k?.expiresAt ? toLocalInput(k.expiresAt) : ''
  form.allowedModels = [...(k?.policy.allowedModels ?? [])]
  form.allowedChannels = [...(k?.policy.allowedChannels ?? [])]
  form.ipAllowlist = (k?.policy.ipAllowlist ?? []).join('\n')
  form.rpm = k?.policy.rpm ?? ''
  form.compatMode = k?.policy.compatMode ?? 'strict'
  form.quotaOverflow = isQuotaOverflow(k?.policy.quotaOverflow) ? k.policy.quotaOverflow : ''
  const spend = spendFormFrom(k?.policy.spendLimit)
  form.spendAmount = spend.amount
  form.spendWindow = spend.window
  errors.value = {}
  formError.value = null
})

const IP_RE = /^(?:\d{1,3}(?:\.\d{1,3}){3}|[0-9a-f:]+)(?:\/\d{1,3})?$/i

function ipList(): string[] {
  return form.ipAllowlist.split(/[\n,，\s]+/).map(s => s.trim()).filter(Boolean)
}

function validate(): Record<string, string> {
  const e: Record<string, string> = {}
  const name = form.name.trim()
  if (!name || name.length > 64)
    e.name = '长度应为 1–64 个字符'
  const bad = ipList().filter(s => !IP_RE.test(s))
  if (bad.length)
    e['policy.ipAllowlist'] = `无效的 IP 或 CIDR：${bad.join('、')}`
  if (ipList().length > 100)
    e['policy.ipAllowlist'] = '最多 100 条'
  if (form.rpm !== '' && (!Number.isInteger(Number(form.rpm)) || Number(form.rpm) < 1 || Number(form.rpm) > 100000))
    e['policy.rpm'] = '范围为 1–100000 的整数，留空表示不限'
  const spendError = validateSpendForm({ amount: form.spendAmount, window: form.spendWindow })
  if (spendError)
    e['policy.spendLimit'] = spendError
  if (form.expiresAt) {
    const iso = fromLocalInput(form.expiresAt)
    if (!iso)
      e.expiresAt = '无效的时间'
    else if (new Date(iso).getTime() <= Date.now() && (!isEdit.value || toLocalInput(props.gatewayKey?.expiresAt ?? '') !== form.expiresAt))
      e.expiresAt = '必须晚于当前时间'
  }
  return e
}

function policy(): KeyPolicy {
  const base: KeyPolicy = {
    allowedModels: form.allowedModels,
    allowedChannels: form.allowedChannels,
    ipAllowlist: ipList(),
    rpm: form.rpm === '' ? null : Number(form.rpm),
    compatMode: form.compatMode,
    quotaOverflow: form.quotaOverflow,
  }
  return withSpendLimit(base, spendLimitFromForm({ amount: form.spendAmount, window: form.spendWindow }), props.gatewayKey?.policy)
}
function onSpendWindow(v: unknown) {
  if (isSpendWindow(v))
    form.spendWindow = v
}

// reka-ui's Select cannot hold '' as a value, so 「跟随账户设置」 is 'inherit' in the picker.
const INHERIT = 'inherit'
const overflowSelect = computed(() => form.quotaOverflow || INHERIT)
function onOverflowSelect(v: unknown) {
  form.quotaOverflow = isQuotaOverflow(v) ? v : ''
}
const overflowOptions = computed(() => [
  { value: INHERIT, label: props.accountOverflow ? `${KEY_QUOTA_OVERFLOW_LABELS['']}（当前：${QUOTA_OVERFLOW_SHORT[props.accountOverflow]}）` : KEY_QUOTA_OVERFLOW_LABELS[''] },
  { value: 'block', label: `${KEY_QUOTA_OVERFLOW_LABELS.block}（返回 429）` },
  { value: 'wallet', label: `${KEY_QUOTA_OVERFLOW_LABELS.wallet}余额按量计费` },
])

async function submit() {
  formError.value = null
  errors.value = validate()
  if (Object.keys(errors.value).length) {
    formError.value = '请修正标记的字段'
    return
  }
  saving.value = true
  try {
    const expiresIso = fromLocalInput(form.expiresAt)
    const k = props.gatewayKey
    if (!k) {
      const res = await keysApi.create({ name: form.name.trim(), policy: policy(), ...(expiresIso ? { expiresAt: expiresIso } : {}) })
      emit('created', res)
    }
    else {
      const patch: KeyPatch = { name: form.name.trim(), policy: policy(), version: k.version }
      if (!expiresIso && k.expiresAt)
        patch.clearExpiry = true
      else if (expiresIso && toLocalInput(k.expiresAt ?? '') !== form.expiresAt)
        patch.expiresAt = expiresIso
      emit('updated', await keysApi.update(k.id, patch))
    }
    open.value = false
  }
  catch (err) {
    if (isVersionConflict(err)) {
      formError.value = '该 Key 已在其他地方被修改，请关闭后刷新列表再试'
      emit('conflict')
    }
    else if (isApiError(err) && err.status === 422) {
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

const minLocal = computed(() => toLocalInput(new Date()))
const COMPAT_MODES: CompatMode[] = ['strict', 'lenient']
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{{ isEdit ? '编辑 API Key' : '创建 API Key' }}</DialogTitle>
        <DialogDescription>
          Key 用于 SDK / 客户端调用网关的 /v1 接口。限制项留空表示不限。
        </DialogDescription>
      </DialogHeader>

      <form id="key-form" class="space-y-4" novalidate @submit.prevent="submit">
        <FormField label="名称" for="key-name" required :error="errors.name">
          <Input id="key-name" v-model="form.name" maxlength="64" placeholder="例如：本地开发" />
        </FormField>
        <FormField label="过期时间" for="key-exp" :error="errors.expiresAt" hint="留空表示永不过期（按你的本地时区）。">
          <Input id="key-exp" v-model="form.expiresAt" type="datetime-local" :min="minLocal" />
        </FormField>

        <Separator />
        <p class="text-sm font-semibold">
          访问策略
        </p>

        <FormField label="允许的模型" for="key-models" :error="errors['policy.allowedModels']" hint="未选择表示可以调用所有可用模型。">
          <MultiSelect id="key-models" v-model="form.allowedModels" :options="modelOptions" :loading="optionsLoading" allow-custom empty-label="不限" placeholder="搜索或输入模型名" />
        </FormField>
        <FormField label="允许的渠道" for="key-channels" :error="errors['policy.allowedChannels']" hint="未选择表示可以路由到所有可用渠道。">
          <MultiSelect id="key-channels" v-model="form.allowedChannels" :options="channelOptions" :loading="optionsLoading" empty-label="不限" placeholder="搜索渠道" />
        </FormField>
        <FormField label="IP 白名单" for="key-ips" :error="errors['policy.ipAllowlist']" hint="每行一个 IP 或 CIDR（如 203.0.113.0/24），最多 100 条；留空表示不限。">
          <Textarea id="key-ips" v-model="form.ipAllowlist" rows="3" class="font-mono text-xs" placeholder="203.0.113.10&#10;2001:db8::/32" />
        </FormField>
        <FormField label="每分钟请求数上限（RPM）" for="key-rpm" :error="errors['policy.rpm']" hint="留空表示不限。">
          <Input id="key-rpm" v-model="form.rpm" type="number" min="1" max="100000" step="1" placeholder="不限" />
        </FormField>

        <div class="space-y-2">
          <Label>跨协议兼容模式</Label>
          <div class="grid gap-2 sm:grid-cols-2">
            <label
              v-for="m in COMPAT_MODES"
              :key="m"
              class="hover:bg-muted/50 flex cursor-pointer gap-2 rounded-lg border p-3 text-sm has-checked:border-primary has-checked:bg-muted/50"
            >
              <input v-model="form.compatMode" type="radio" name="compat" :value="m" class="accent-primary mt-0.5">
              <span class="space-y-1">
                <span class="block font-medium">{{ COMPAT_MODE_LABELS[m] }}</span>
                <span class="text-muted-foreground block text-xs">{{ COMPAT_MODE_DESCRIPTIONS[m] }}</span>
              </span>
            </label>
          </div>
          <p v-if="errors['policy.compatMode']" class="text-destructive text-xs">
            {{ errors['policy.compatMode'] }}
          </p>
        </div>
        <FormField
          label="额度用完后"
          for="key-overflow"
          :error="errors['policy.quotaOverflow']"
          hint="套餐额度用完时，使用此 Key 的请求是被阻断还是改用钱包余额按量计费。账户设置可在「钱包与订阅」中修改。"
        >
          <Select :model-value="overflowSelect" @update:model-value="onOverflowSelect">
            <SelectTrigger id="key-overflow" class="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="o in overflowOptions" :key="o.value" :value="o.value">
                {{ o.label }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="消费上限" for="key-spend" :error="errors['policy.spendLimit'] ?? errors['policy.spendLimit.amount'] ?? errors['policy.spendLimit.window']">
          <div class="flex gap-2" data-testid="key-spend-row">
            <div class="relative min-w-0 flex-1">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
              <Input id="key-spend" v-model="form.spendAmount" inputmode="decimal" placeholder="不限" class="pl-7 tabular-nums" :aria-invalid="!!errors['policy.spendLimit']" data-testid="key-spend-amount" />
            </div>
            <Select :model-value="form.spendWindow" @update:model-value="onSpendWindow">
              <SelectTrigger class="w-28 shrink-0" aria-label="消费上限的统计窗口" data-testid="key-spend-window">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="w in SPEND_WINDOWS" :key="w" :value="w">
                  {{ SPEND_WINDOW_LABELS[w] }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <template #hint>
            只统计从<strong class="text-foreground font-medium">钱包扣费</strong>的金额（套餐覆盖的请求、自有 / 共享渠道的请求都不计入）。达到上限后返回 429，窗口按你所在用户组的时区划分（每周从周一开始）。留空表示不限。
          </template>
        </FormField>
        <p v-if="errors.policy" class="text-destructive text-xs">
          {{ errors.policy }}
        </p>
      </form>

      <DialogFooter class="items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="key-form" :disabled="saving">
          <Loader2 v-if="saving" class="animate-spin" />
          {{ isEdit ? '保存' : '创建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
