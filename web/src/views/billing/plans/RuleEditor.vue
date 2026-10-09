<script setup lang="ts">
import type { RuleFormState } from '@/lib/planForm'
import type { DurationUnit } from '@/lib/quota'
import type { CalendarUnit, WindowKind } from '@/lib/types'
import { computed, onMounted, ref, watch } from 'vue'
import { Plus, Puzzle, Trash2, TriangleAlert } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import SuggestInput from '@/components/SuggestInput.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useCurrency } from '@/composables/useCurrency'
import { isCustomMeter, parseCustomMeter } from '@/lib/customMeters'
import { COMMON_TIMEZONES, suggestRuleId } from '@/lib/planForm'
import { audioMinutesNote, CALENDAR_UNIT_LABELS, METER_LABELS, meterHint, meterLabel, meterUnit, METERS, WINDOW_KIND_HINTS, WINDOW_KIND_LABELS } from '@/lib/quota'
import { useCustomMetersStore } from '@/stores/customMeters'

const props = defineProps<{
  index: number
  /** Errors of this rule, keyed by field (`limit`, `window.duration`, `modelWeights.gpt-x` …). */
  errors: Record<string, string>
  /** Ids of the other rules (for unique id suggestions). */
  takenIds: string[]
  /** Models offered for the rule's model filter / weights. */
  modelOptions: string[]
  /** Model names suggested for the weight rows. */
  weightModelOptions: string[]
  canRemove: boolean
}>()
const rule = defineModel<RuleFormState>('rule', { required: true })
defineEmits<{ remove: [] }>()

const { currency } = useCurrency()
const customMeters = useCustomMetersStore()
onMounted(() => {
  void customMeters.load()
})

const fid = (name: string) => `rule-${rule.value.uid}-${name}`
const WINDOW_KINDS: WindowKind[] = ['calendar', 'session', 'rolling', 'period', 'lifetime']
const UNITS: DurationUnit[] = ['m', 'h', 'd']
const UNIT_LABELS: Record<DurationUnit, string> = { m: '分钟', h: '小时', d: '天' }
const CUSTOM_TZ = '__custom__'

// Keep the suggested id in sync with the window until the admin edits it.
watch(
  () => [rule.value.windowKind, rule.value.unit, rule.value.durationN, rule.value.durationUnit, rule.value.everyN, rule.value.everyUnit, props.takenIds.join(',')],
  () => {
    if (!rule.value.idEdited)
      rule.value.id = suggestRuleId(rule.value, props.takenIds)
  },
)
function onIdInput(v: string | number) {
  rule.value.id = String(v)
  rule.value.idEdited = true
}

const customTz = ref(!COMMON_TIMEZONES.includes(rule.value.timezone))
const tzSelect = computed(() => (customTz.value ? CUSTOM_TZ : rule.value.timezone))
function onTzSelect(v: unknown) {
  if (v === CUSTOM_TZ) {
    customTz.value = true
    return
  }
  if (typeof v === 'string') {
    customTz.value = false
    rule.value.timezone = v
  }
}

function setMeter(v: unknown) {
  if (typeof v === 'string' && ((METERS as string[]).includes(v) || isCustomMeter(v)))
    rule.value.meter = v
}

const isCustom = computed(() => isCustomMeter(rule.value.meter))
/** Selected plugin meter (null for built-in meters or meters of plugins no longer listed). */
const customInfo = computed(() => customMeters.byId.get(rule.value.meter) ?? null)
/** The rule uses a plugin meter that is not offered (plugin disabled, unapproved or unreadable). */
const customMissing = computed(() => isCustom.value && !customMeters.loading && customMeters.items !== null && !customInfo.value)
const meterText = computed(() => {
  if (!isCustom.value)
    return meterLabel(rule.value.meter)
  const info = customInfo.value
  if (info)
    return `${info.label}（${info.pluginName || info.pluginKey}）`
  const ref = parseCustomMeter(rule.value.meter)
  return ref ? `${ref.meter}（${ref.pluginKey}）` : rule.value.meter
})
function setWindowKind(v: unknown) {
  if (typeof v === 'string' && (WINDOW_KINDS as string[]).includes(v))
    rule.value.windowKind = v as WindowKind
}
function setUnit(v: unknown) {
  if (v === 'day' || v === 'week' || v === 'month')
    rule.value.unit = v as CalendarUnit
}
function setDurationUnit(field: 'durationUnit' | 'everyUnit', v: unknown) {
  if (v === 'm' || v === 'h' || v === 'd')
    rule.value[field] = v
}

const limitHint = computed(() => {
  if (rule.value.meter === 'charge')
    return `结算币种 ${currency.value?.code ?? ''} 金额，最多 9 位小数。`
  if (rule.value.meter === 'requests')
    return '正整数（次）。'
  if (rule.value.meter === 'images')
    return '正整数（张），按输出图片张数计。'
  if (rule.value.meter === 'audio_seconds') {
    const note = audioMinutesNote(rule.value.limit.trim())
    return `正整数（秒），如 3600 = 1 小时。${note ? `当前上限${note}` : ''}`
  }
  if (isCustom.value) {
    const unit = customInfo.value?.unit
    return `大于 0 的数值（最多 9 位小数）${unit ? `，单位「${unit}」由插件定义` : '，单位由插件定义'}。`
  }
  return '正整数（tokens），如 5000000 = 500 万。'
})

function addWeight() {
  rule.value.weights.push({ model: '', weight: '1' })
}

/** Suffix inside the limit input (none for charge, which shows the currency symbol instead). */
const limitUnit = computed(() => meterUnit(rule.value.meter, customInfo.value ? { meterUnit: customInfo.value.unit } : null))
const limitPlaceholder = computed(() => isCustom.value ? '1000' : ({ charge: '20', requests: '200', images: '500', audio_seconds: '3600' } as Record<string, string>)[rule.value.meter] ?? '5000000')

const weightErrors = computed(() => Object.entries(props.errors).filter(([k]) => k.startsWith('modelWeights.')))
</script>

<template>
  <div role="group" class="bg-card min-w-0 space-y-4 rounded-lg border p-3 sm:p-4" :aria-label="`规则 ${index + 1}`" :data-rule-index="index">
    <div class="flex items-center justify-between gap-2">
      <p class="text-sm font-medium">
        规则 {{ index + 1 }}
        <span class="text-muted-foreground font-mono text-xs">{{ rule.id }}</span>
      </p>
      <Button type="button" variant="ghost" size="icon-sm" :disabled="!canRemove" :aria-label="`删除规则 ${index + 1}`" :title="canRemove ? '删除规则' : '至少保留一条规则'" @click="$emit('remove')">
        <Trash2 />
      </Button>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <FormField label="规则 ID" :for="fid('id')" required :error="errors.id" hint="小写字母、数字、- 和 _，1–32 位；套餐内唯一。">
        <Input :id="fid('id')" :model-value="rule.id" maxlength="32" class="font-mono text-xs" :aria-invalid="!!errors.id" @update:model-value="onIdInput" />
      </FormField>
      <FormField label="展示名" :for="fid('label')" :error="errors.label" hint="留空时按窗口生成，如“5 小时会话”。">
        <Input :id="fid('label')" v-model="rule.label" maxlength="64" placeholder="例如：5 小时会话" :aria-invalid="!!errors.label" />
      </FormField>
      <FormField label="计量" :for="fid('meter')" required :error="errors.meter" :hint="isCustom ? undefined : meterHint(rule.meter)" class="sm:col-span-2 lg:col-span-1">
        <Select :model-value="rule.meter" @update:model-value="setMeter">
          <SelectTrigger :id="fid('meter')" class="w-full min-w-0">
            <SelectValue :placeholder="meterText">
              <span class="truncate">{{ meterText }}</span>
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectLabel v-if="customMeters.groups.length || isCustom">
                内置计量
              </SelectLabel>
              <SelectItem v-for="m in METERS" :key="m" :value="m">
                {{ METER_LABELS[m] }}
              </SelectItem>
            </SelectGroup>
            <template v-if="customMeters.groups.length || (isCustom && !customInfo)">
              <SelectSeparator />
              <SelectGroup>
                <SelectLabel class="flex items-center gap-1.5">
                  <Puzzle class="size-3.5" />
                  插件计量
                </SelectLabel>
                <template v-for="g in customMeters.groups" :key="g.pluginKey">
                  <SelectItem v-for="m in g.meters" :key="m.id" :value="m.id" :text-value="`${m.label} ${g.pluginName}`">
                    <span class="flex min-w-0 flex-col">
                      <span>{{ m.label }}<span v-if="m.unit" class="text-muted-foreground">（{{ m.unit }}）</span></span>
                      <span class="text-muted-foreground font-mono text-xs">{{ g.pluginName }} · {{ m.meter }}</span>
                    </span>
                  </SelectItem>
                </template>
                <SelectItem v-if="isCustom && !customInfo" :value="rule.meter">
                  <span class="flex min-w-0 flex-col">
                    <span>{{ meterText }}</span>
                    <span class="text-muted-foreground text-xs">插件不可用或未批准</span>
                  </span>
                </SelectItem>
              </SelectGroup>
            </template>
          </SelectContent>
        </Select>
        <div v-if="isCustom" class="space-y-1 rounded-md border border-amber-500/40 bg-amber-500/5 px-2.5 py-1.5 text-xs" data-testid="custom-meter-warning">
          <p class="flex items-start gap-1.5">
            <TriangleAlert class="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
            <span>数值与单位由插件「{{ customInfo?.pluginName || parseCustomMeter(rule.meter)?.pluginKey || '—' }}」定义；插件超时、出错或不可用时，该次计量按 0 记（写入告警日志）。</span>
          </p>
          <p v-if="customMissing" class="text-destructive">
            当前没有启用且已批准的插件提供该计量，保存时会被服务端拒绝。
          </p>
        </div>
      </FormField>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <FormField label="窗口" :for="fid('window')" required :error="errors['window.kind']" :hint="WINDOW_KIND_HINTS[rule.windowKind]">
        <Select :model-value="rule.windowKind" @update:model-value="setWindowKind">
          <SelectTrigger :id="fid('window')" class="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="k in WINDOW_KINDS" :key="k" :value="k">
              {{ WINDOW_KIND_LABELS[k] }}
            </SelectItem>
          </SelectContent>
        </Select>
      </FormField>

      <template v-if="rule.windowKind === 'calendar'">
        <FormField label="周期" :for="fid('unit')" required :error="errors['window.unit']" hint="周从周一 00:00 开始。">
          <Select :model-value="rule.unit" @update:model-value="setUnit">
            <SelectTrigger :id="fid('unit')" class="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="u in ['day', 'week', 'month']" :key="u" :value="u">
                每{{ CALENDAR_UNIT_LABELS[u] }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
        <FormField label="时区" :for="fid('tz')" required :error="errors['window.timezone']" hint="按该时区的 00:00 重置。">
          <div class="space-y-2">
            <Select :model-value="tzSelect" @update:model-value="onTzSelect">
              <SelectTrigger :id="fid('tz')" class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="tz in COMMON_TIMEZONES" :key="tz" :value="tz">
                  {{ tz }}
                </SelectItem>
                <SelectItem :value="CUSTOM_TZ">
                  其他时区…
                </SelectItem>
              </SelectContent>
            </Select>
            <Input v-if="customTz" v-model="rule.timezone" placeholder="IANA 时区，如 Europe/Paris" class="font-mono text-xs" aria-label="自定义时区" :aria-invalid="!!errors['window.timezone']" />
          </div>
        </FormField>
      </template>

      <FormField v-else-if="rule.windowKind === 'rolling' || rule.windowKind === 'session'" label="窗口长度" :for="fid('dur')" required :error="errors['window.duration']" hint="5 分钟到 31 天。">
        <div class="flex gap-2">
          <Input :id="fid('dur')" v-model="rule.durationN" type="number" min="1" step="1" class="min-w-16 tabular-nums" :aria-invalid="!!errors['window.duration']" />
          <Select :model-value="rule.durationUnit" @update:model-value="(v) => setDurationUnit('durationUnit', v)">
            <SelectTrigger class="w-24 shrink-0" aria-label="窗口长度单位">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="u in UNITS" :key="u" :value="u">
                {{ UNIT_LABELS[u] }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
      </FormField>

      <FormField v-else-if="rule.windowKind === 'period'" label="每隔" :for="fid('every')" required :error="errors['window.every']" hint="1 小时到 366 天，从订阅开始时间对齐。">
        <div class="flex gap-2">
          <Input :id="fid('every')" v-model="rule.everyN" type="number" min="1" step="1" class="min-w-16 tabular-nums" :aria-invalid="!!errors['window.every']" />
          <Select :model-value="rule.everyUnit" @update:model-value="(v) => setDurationUnit('everyUnit', v)">
            <SelectTrigger class="w-24 shrink-0" aria-label="周期单位">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="u in UNITS" :key="u" :value="u">
                {{ UNIT_LABELS[u] }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
      </FormField>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <FormField :label="rule.meter === 'charge' ? `上限（${currency?.code ?? '金额'}）` : '上限'" :for="fid('limit')" required :error="errors.limit" :hint="limitHint">
        <div class="relative">
          <span v-if="rule.meter === 'charge'" class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
          <Input
            :id="fid('limit')"
            v-model="rule.limit"
            :inputmode="rule.meter === 'charge' || isCustom ? 'decimal' : 'numeric'"
            :placeholder="limitPlaceholder"
            class="font-mono tabular-nums"
            :class="[rule.meter === 'charge' ? 'pl-7' : '', rule.meter !== 'charge' ? 'pr-16' : '']"
            :aria-invalid="!!errors.limit"
          />
          <span v-if="rule.meter !== 'charge'" class="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 max-w-14 -translate-y-1/2 truncate text-xs" :title="limitUnit">{{ limitUnit }}</span>
        </div>
      </FormField>
    </div>

    <div class="grid gap-4 xl:grid-cols-2">
      <FormField label="仅计量这些模型" :for="fid('models')" :error="errors.models" hint="留空表示计量套餐覆盖的全部模型；可输入列表外的模型名。">
        <MultiSelect :id="fid('models')" v-model="rule.models" :options="modelOptions.map(m => ({ value: m, label: m }))" allow-custom empty-label="套餐覆盖的全部模型" placeholder="搜索或输入模型名" />
      </FormField>

      <div class="space-y-2">
        <Label>模型倍率</Label>
        <p class="text-muted-foreground text-xs">
          计量值 × 倍率（0–1000，可为小数）。未列出的模型按 ×1 计量；0 表示该模型不计入此规则。
        </p>
        <div v-if="rule.weights.length" class="space-y-2">
          <div class="text-muted-foreground grid grid-cols-[minmax(0,1fr)_7rem_auto] gap-2 text-xs">
            <span>模型</span><span>倍率</span><span class="w-8" />
          </div>
          <div v-for="(w, wi) in rule.weights" :key="wi" class="grid grid-cols-[minmax(0,1fr)_7rem_auto] gap-2">
            <SuggestInput v-model="w.model" :options="weightModelOptions" :exclude="rule.weights.filter((_, j) => j !== wi).map(x => x.model.trim())" placeholder="模型名" mono :aria-label="`倍率第 ${wi + 1} 行模型`" />
            <Input v-model="w.weight" inputmode="decimal" placeholder="1" class="font-mono text-xs tabular-nums" :aria-label="`倍率第 ${wi + 1} 行倍率`" :aria-invalid="!!errors[`modelWeights.${w.model.trim()}`]" />
            <Button type="button" variant="ghost" size="icon" :aria-label="`删除倍率第 ${wi + 1} 行`" @click="rule.weights.splice(wi, 1)">
              <Trash2 />
            </Button>
          </div>
        </div>
        <div v-if="errors.modelWeights || weightErrors.length" class="text-destructive space-y-0.5 text-xs" role="alert">
          <p v-if="errors.modelWeights">
            {{ errors.modelWeights }}
          </p>
          <p v-for="[k, v] in weightErrors" :key="k">
            {{ k.slice('modelWeights.'.length) }}：{{ v }}
          </p>
        </div>
        <Button type="button" variant="ghost" size="sm" @click="addWeight">
          <Plus />
          添加倍率
        </Button>
      </div>
    </div>
  </div>
</template>
