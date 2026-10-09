<script setup lang="ts">
import type { Component } from 'vue'
import type { BillingMode, PriceFieldKey, PriceFormAmounts } from '@/lib/billingMode'
import type { Channel, Price, PriceInput, PriceKind } from '@/lib/types'
import { computed, reactive, ref, watch } from 'vue'
import { AudioLines, Binary, Image, Info, Loader2, SlidersHorizontal, Zap } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import SuggestInput from '@/components/SuggestInput.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { modelsApi } from '@/lib/endpoints'
import {
  BILLING_MODE_META,
  BILLING_MODES,
  FALLBACK_PRICE_KEYS,
  inferBillingMode,
  isSetPrice,
  PRICE_FIELD_KEYS,
  priceBodyForMode,
  priceSummary,
  priceSummaryText,
  validateModeAmounts,
} from '@/lib/billingMode'
import { PRICE_KIND_LABELS } from '@/lib/labels'
import { buildSchedule, emptyScheduleForm, normalizeScheduleErrorKeys, scheduleFormFrom, validateSchedule } from '@/lib/priceSchedule'
import type { ScheduleForm } from '@/lib/priceSchedule'
import PriceAmountField from './PriceAmountField.vue'
import PriceScheduleEditor from './PriceScheduleEditor.vue'
import PriceTierEditor from './PriceTierEditor.vue'
import type { TierRow } from '@/lib/priceTiers'
import { buildTiers, normalizeTierErrorKeys, tierRowsFrom, tiersAllowed, validateTiers } from '@/lib/priceTiers'
import { fromLocalInput, toLocalInput } from '@/lib/timeRange'

export interface PricePrefill {
  kind?: PriceKind
  model?: string
  channelId?: string
  from?: Price | null
}

const props = defineProps<{
  /** Logical model names (for sell prices). */
  models: string[]
  /** Full channels (for cost prices). */
  channels: Channel[]
  channelsLoading?: boolean
  prefill?: PricePrefill | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ created: [price: Price] }>()

const { currency, money } = useCurrency()

interface AmountFieldDef {
  key: PriceFieldKey
  label: string
  hint?: string
  placeholder?: string
  required?: boolean
}

/** 自定义组合: every field, grouped as before (token + per request, image, audio). */
const AMOUNT_FIELDS: AmountFieldDef[] = [
  { key: 'inputPerM', label: '输入（每 1M token）' },
  { key: 'outputPerM', label: '输出（每 1M token）' },
  { key: 'cacheReadPerM', label: '缓存读取（每 1M token）' },
  { key: 'cacheWritePerM', label: '缓存写入（每 1M token）' },
  { key: 'perRequest', label: '每次请求固定费用' },
]

/** phase7 §1.1: optional image prices, omitted from the body when blank. */
const IMAGE_FIELDS: AmountFieldDef[] = [
  { key: 'perImage', label: '每张输出图片', placeholder: '不计', hint: '按响应中的图片张数计费。' },
  { key: 'imageInputPerM', label: '图片输入（每 1M token）', placeholder: '按 inputPerM', hint: '留空时按「输入」单价计。' },
]

/** phase9 §1.1: optional audio prices, omitted from the body when blank. */
const AUDIO_FIELDS: AmountFieldDef[] = [
  { key: 'audioInputPerM', label: '音频输入（每 1M token）', placeholder: '按 inputPerM', hint: '上游按 token 返回音频用量时使用；留空按「输入」单价计。' },
  { key: 'audioOutputPerM', label: '音频输出（每 1M token）', placeholder: '按 outputPerM', hint: '语音合成返回 token 用量时使用；留空按「输出」单价计。' },
  { key: 'perMinute', label: '每分钟音频', placeholder: '不计', hint: '转写 / 翻译按上游报告的输入音频时长计费：向上取整到整秒，费用 = 秒数 × 单价 ÷ 60。' },
  { key: 'perMCharacters', label: '每 1M 输入字符', placeholder: '不计', hint: '语音合成没有 token 用量时，按 input 的 Unicode 字符（码点）数计费。' },
]

/** The fields of the single-purpose modes (自定义组合 uses the three groups above). */
const MODE_FIELDS: Record<Exclude<BillingMode, 'custom'>, AmountFieldDef[]> = {
  token: AMOUNT_FIELDS.slice(0, 4),
  request: [
    { key: 'perRequest', label: '每次请求价格', required: true, hint: '每次请求固定收取，与用量无关。' },
  ],
  image: [
    { key: 'perImage', label: '每张输出图片', hint: '按响应中的图片张数计费。' },
    { key: 'perRequest', label: '每次请求固定费用（可选）', placeholder: '不计', hint: '在图片费用之外，每次请求另收的费用。' },
  ],
  audio: [
    { key: 'perMinute', label: '每分钟音频', placeholder: '不计', hint: '转写 / 翻译：按上游报告的输入音频时长，向上取整到整秒，费用 = 秒数 × 单价 ÷ 60。' },
    { key: 'perMCharacters', label: '每 1M 输入字符', placeholder: '不计', hint: '语音合成：按 input 的 Unicode 字符（码点）数计费。' },
    { key: 'audioInputPerM', label: '音频输入（每 1M token）', placeholder: '不计', hint: '上游返回音频 token 用量时按此计费（优先于时长）；留空不计。' },
    { key: 'audioOutputPerM', label: '音频输出（每 1M token）', placeholder: '不计', hint: '语音合成返回 token 用量时按此计费（优先于字符数）；留空不计。' },
    { key: 'perRequest', label: '每次请求固定费用（可选）', placeholder: '不计', hint: '每次请求另收；上游没有返回任何用量时只收这一项。' },
  ],
}

const MODE_ICONS: Record<BillingMode, Component> = { token: Binary, request: Zap, image: Image, audio: AudioLines, custom: SlidersHorizontal }
const MODE_UNITS: Record<BillingMode, string> = { token: '每 1M token', request: '每次请求', image: '每张图片', audio: '分钟 / 字符', custom: '多项相加' }

const form = reactive({
  kind: 'sell' as PriceKind,
  model: '',
  channelId: '',
  effectiveAt: '',
})
/**
 * Amounts of every mode: switching modes keeps what was typed (switching back
 * restores it); only the active mode's fields are validated and submitted.
 */
const amounts = reactive<PriceFormAmounts>(Object.fromEntries(PRICE_FIELD_KEYS.map(k => [k, ''])) as PriceFormAmounts)
const mode = ref<BillingMode>('token')
const errors = ref<Record<string, string>>({})
const AMOUNT_KEYS = new Set<string>(PRICE_FIELD_KEYS)
watch(mode, () => {
  // Amount errors of the previous mode no longer apply.
  errors.value = Object.fromEntries(Object.entries(errors.value).filter(([k]) => !AMOUNT_KEYS.has(k)))
})
const formError = ref<string | null>(null)
const saving = ref(false)
const schedule = ref<ScheduleForm>(emptyScheduleForm())
/** Schedule errors from the last 422 (cleared on edit). */
const serverScheduleErrors = ref<Record<string, string>>({})
const scheduleErrors = computed(() => ({ ...validateSchedule(schedule.value), ...serverScheduleErrors.value }))
watch(schedule, () => (serverScheduleErrors.value = {}), { deep: true })
/** phase10 §1: context-length tiers (Token / 自定义组合 only; kept while switching modes). */
const tiers = ref<TierRow[]>([])
const serverTierErrors = ref<Record<string, string>>({})
const showTiers = computed(() => tiersAllowed(mode.value))
const tierErrors = computed(() => (showTiers.value ? { ...validateTiers(tiers.value, mode.value), ...serverTierErrors.value } : {}))
watch([tiers, mode], () => (serverTierErrors.value = {}), { deep: true })

watch(open, (v) => {
  if (!v)
    return
  const p = props.prefill
  form.kind = p?.kind ?? 'sell'
  form.model = p?.model ?? ''
  form.channelId = p?.channelId ?? ''
  // Unset amounts start blank ("0" of a zero-default price = not charged); explicit
  // fallback prices (image / audio token prices) keep their value, even "0".
  for (const k of PRICE_FIELD_KEYS) {
    const v = p?.from?.[k]
    amounts[k] = typeof v === 'string' && (isSetPrice(k, v) || (FALLBACK_PRICE_KEYS.includes(k) && v !== '')) ? v : ''
  }
  mode.value = inferBillingMode(p?.from)
  form.effectiveAt = ''
  schedule.value = p?.from ? scheduleFormFrom(p.from) : emptyScheduleForm()
  tiers.value = tierRowsFrom(p?.from)
  errors.value = {}
  serverScheduleErrors.value = {}
  serverTierErrors.value = {}
  formError.value = null
})

const selectedChannel = computed(() => props.channels.find(c => c.id === form.channelId) ?? null)
/** Model suggestions: logical models for sell, the channel's upstream models for cost. */
const modelOptions = computed(() => {
  if (form.kind === 'sell')
    return props.models
  const ch = selectedChannel.value
  return ch ? [...new Set((ch.models ?? []).map(m => m.upstreamModel || m.model))].sort() : []
})

const minLocal = computed(() => toLocalInput(new Date()))
const symbol = computed(() => currency.value?.symbol ?? '')

/** What the active mode would bill, e.g. "$0.040 / 次" (display amounts, 3 decimals). */
const preview = computed(() => {
  if (Object.keys(validateModeAmounts(mode.value, amounts)).length)
    return null
  return priceSummaryText(priceSummary(priceBodyForMode(mode.value, amounts)), money)
})

function validate(): Record<string, string> {
  const e: Record<string, string> = {}
  if (!form.model.trim() || form.model.trim().length > 128)
    e.model = '请填写模型名（1–128 个字符）'
  if (form.kind === 'cost' && !form.channelId)
    e.channelId = '成本价必须选择渠道'
  Object.assign(e, validateModeAmounts(mode.value, amounts))
  if (form.effectiveAt) {
    const iso = fromLocalInput(form.effectiveAt)
    if (!iso)
      e.effectiveAt = '无效的时间'
    else if (new Date(iso).getTime() < Date.now() - 60_000)
      e.effectiveAt = '生效时间不能早于现在'
  }
  return e
}

async function submit() {
  formError.value = null
  errors.value = validate()
  const nErr = Object.keys(errors.value).length
  if (nErr || Object.keys(scheduleErrors.value).length || Object.keys(tierErrors.value).length) {
    formError.value = nErr
      ? '请修正标记的字段'
      : Object.keys(tierErrors.value).length ? '请修正阶梯价格中标记的档位' : '请修正分时价格中标记的时段'
    return
  }
  // Fields of other modes are sent as "not set" (required amounts "0", optional ones omitted).
  const body: PriceInput = {
    kind: form.kind,
    model: form.model.trim(),
    ...priceBodyForMode(mode.value, amounts),
  }
  if (form.kind === 'cost')
    body.channelId = form.channelId
  // phase8 §3: only sent when periods exist (older backends reject unknown fields).
  if (schedule.value.rows.length) {
    body.schedule = buildSchedule(schedule.value.rows)
    body.scheduleTimezone = schedule.value.timezone
  }
  // phase10 §1: only sent when tiers exist in a mode that offers them.
  if (showTiers.value && tiers.value.length)
    body.tiers = buildTiers(tiers.value, mode.value)
  const eff = fromLocalInput(form.effectiveAt)
  if (eff)
    body.effectiveAt = eff
  saving.value = true
  try {
    const created = await modelsApi.createPrice(body)
    toast.success('价格版本已创建', { description: eff ? '将在指定时间生效' : '已立即生效' })
    emit('created', created)
    open.value = false
  }
  catch (err) {
    if (isApiError(err) && err.status === 422) {
      const all = normalizeTierErrorKeys(normalizeScheduleErrorKeys(fieldErrors(err)))
      const sched: Record<string, string> = {}
      const tier: Record<string, string> = {}
      const rest: Record<string, string> = {}
      for (const [k, v] of Object.entries(all)) {
        if (k === 'tiers' || k.startsWith('tiers.'))
          tier[k] = v
        else
          (k === 'schedule' || k.startsWith('schedule.') || k === 'scheduleTimezone' ? sched : rest)[k] = v
      }
      errors.value = rest
      serverScheduleErrors.value = sched
      serverTierErrors.value = tier
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

function setKind(v: unknown) {
  if (v === 'sell' || v === 'cost') {
    form.kind = v
    form.model = ''
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>新增价格版本</DialogTitle>
        <DialogDescription>
          金额单位：{{ currency?.code ?? '结算币种' }}。先选择计费方式，只需填写该方式的单价。
        </DialogDescription>
      </DialogHeader>

      <form id="price-form" class="space-y-4" novalidate @submit.prevent="submit">
        <div class="bg-muted/50 text-muted-foreground flex gap-2 rounded-lg p-3 text-xs">
          <Info class="mt-0.5 size-3.5 shrink-0" />
          <p>
            价格版本一经创建<strong class="text-foreground">不可修改或删除</strong>，改价即新增一个版本。
            每次请求按其开始时间生效的版本计价，历史请求<strong class="text-foreground">不会被重新计价</strong>。
          </p>
        </div>

        <div class="grid gap-4 sm:grid-cols-2">
          <FormField label="类型" for="pr-kind" required :error="errors.kind">
            <Select :model-value="form.kind" @update:model-value="setKind">
              <SelectTrigger id="pr-kind" class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="sell">
                  {{ PRICE_KIND_LABELS.sell }}（向用户收取）
                </SelectItem>
                <SelectItem value="cost">
                  {{ PRICE_KIND_LABELS.cost }}（上游成本）
                </SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField v-if="form.kind === 'cost'" label="渠道" for="pr-channel" required :error="errors.channelId">
            <Select :model-value="form.channelId" @update:model-value="(v) => { form.channelId = String(v); form.model = '' }">
              <SelectTrigger id="pr-channel" class="w-full">
                <SelectValue :placeholder="channelsLoading ? '加载中…' : '选择渠道'" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="c in channels" :key="c.id" :value="c.id">
                  {{ c.name }}
                </SelectItem>
                <div v-if="!channelsLoading && channels.length === 0" class="text-muted-foreground px-2 py-1.5 text-xs">
                  没有可管理的渠道
                </div>
              </SelectContent>
            </Select>
          </FormField>
        </div>

        <FormField
          :label="form.kind === 'sell' ? '逻辑模型' : '上游模型'"
          for="pr-model"
          required
          :error="errors.model"
          :hint="form.kind === 'sell' ? '售价按客户端请求的逻辑模型名计价。' : '成本按「渠道 + 上游模型名」计价。'"
        >
          <SuggestInput id="pr-model" v-model="form.model" :options="modelOptions" mono :placeholder="modelOptions[0] ?? 'gpt-4o'" :invalid="!!errors.model" />
        </FormField>

        <fieldset class="space-y-2" data-testid="billing-mode">
          <legend class="mb-2 text-sm font-medium">
            计费方式
          </legend>
          <div class="grid grid-cols-2 gap-2 sm:grid-cols-5">
            <label
              v-for="m in BILLING_MODES"
              :key="m"
              class="hover:bg-muted/50 has-[:checked]:border-primary has-[:checked]:bg-primary/5 has-[:focus-visible]:ring-ring/50 relative flex min-w-0 cursor-pointer flex-col items-center gap-1 rounded-lg border px-2 py-2.5 text-center transition-colors has-[:checked]:ring-1 has-[:checked]:ring-primary has-[:focus-visible]:ring-3"
              :class="m === 'custom' ? 'col-span-2 sm:col-span-1' : ''"
              :data-mode="m"
            >
              <input v-model="mode" type="radio" name="pr-mode" :value="m" class="sr-only">
              <component :is="MODE_ICONS[m]" class="size-4" :class="mode === m ? 'text-primary' : 'text-muted-foreground'" aria-hidden="true" />
              <span class="text-sm leading-tight font-medium whitespace-nowrap">{{ BILLING_MODE_META[m].label }}</span>
              <span class="text-muted-foreground text-[11px] leading-tight whitespace-nowrap">{{ MODE_UNITS[m] }}</span>
            </label>
          </div>
          <p class="text-muted-foreground text-xs" data-testid="billing-mode-description">
            {{ BILLING_MODE_META[mode].description }}
          </p>
        </fieldset>

        <div v-if="mode !== 'custom'" class="grid gap-4 sm:grid-cols-2" :data-testid="`price-fields-${mode}`">
          <PriceAmountField
            v-for="f in MODE_FIELDS[mode]"
            :id="`pr-${f.key}`"
            :key="f.key"
            v-model="amounts[f.key]"
            :label="f.label"
            :hint="f.hint"
            :placeholder="f.placeholder"
            :required="f.required"
            :symbol="symbol"
            :error="errors[f.key]"
          />
        </div>

        <template v-else>
          <div class="grid gap-4 sm:grid-cols-2" data-testid="price-fields-custom">
            <PriceAmountField
              v-for="f in AMOUNT_FIELDS"
              :id="`pr-${f.key}`"
              :key="f.key"
              v-model="amounts[f.key]"
              :label="f.label"
              :symbol="symbol"
              :error="errors[f.key]"
            />
          </div>

          <div class="space-y-3 rounded-lg border p-3" role="group" aria-labelledby="pr-image-title">
            <div>
              <p id="pr-image-title" class="text-sm font-medium">
                图片接口（可选）
              </p>
              <p class="text-muted-foreground text-xs">
                用于 /v1/images/*：费用 = 每次请求 + 文本输入 × 输入单价 + 图片输入 × 图片输入单价 + 输出 × 输出单价 + 图片张数 × 每张图片。
              </p>
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <PriceAmountField
                v-for="f in IMAGE_FIELDS"
                :id="`pr-${f.key}`"
                :key="f.key"
                v-model="amounts[f.key]"
                :label="f.label"
                :hint="f.hint"
                :placeholder="f.placeholder"
                :symbol="symbol"
                :error="errors[f.key]"
              />
            </div>
          </div>

          <div class="space-y-3 rounded-lg border p-3" role="group" aria-labelledby="pr-audio-title" data-testid="price-audio">
            <div>
              <p id="pr-audio-title" class="text-sm font-medium">
                音频接口（可选）
              </p>
              <p class="text-muted-foreground text-xs">
                用于 /v1/audio/*：上游返回 token 用量时按 token 计（音频部分用音频单价）；转写 / 翻译只返回时长时按每分钟计；语音合成没有用量时按输入字符数计；都没有时只收每次请求费用。
              </p>
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <PriceAmountField
                v-for="f in AUDIO_FIELDS"
                :id="`pr-${f.key}`"
                :key="f.key"
                v-model="amounts[f.key]"
                :label="f.label"
                :hint="f.hint"
                :placeholder="f.placeholder"
                :symbol="symbol"
                :error="errors[f.key]"
              />
            </div>
          </div>
        </template>

        <p class="bg-muted/40 flex min-h-9 flex-wrap items-center gap-x-2 gap-y-0.5 rounded-lg border px-3 py-2 text-xs" data-testid="price-preview" aria-live="polite">
          <span class="text-muted-foreground">计费预览</span>
          <span v-if="preview" class="font-medium tabular-nums">{{ preview }}</span>
          <span v-else class="text-muted-foreground">填写大于 0 的单价后显示</span>
        </p>

        <PriceTierEditor v-if="showTiers" v-model="tiers" :mode="mode" :base="amounts" :symbol="symbol" :money="money" :errors="tierErrors" />
        <p v-else-if="tiers.length" class="text-muted-foreground rounded-lg border border-dashed px-3 py-2 text-xs" data-testid="tier-hidden-note">
          阶梯价格只适用于按 Token / 自定义组合计费，当前计费方式下不会提交（切换回去可继续编辑）。
        </p>

        <PriceScheduleEditor v-model="schedule" :errors="scheduleErrors" />

        <FormField label="生效时间" for="pr-eff" :error="errors.effectiveAt" hint="留空表示立即生效；只能选择未来的时间（按你的本地时区）。">
          <Input id="pr-eff" v-model="form.effectiveAt" type="datetime-local" :min="minLocal" />
        </FormField>
      </form>

      <DialogFooter class="items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="price-form" :disabled="saving">
          <Loader2 v-if="saving" class="animate-spin" />
          创建价格版本
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
