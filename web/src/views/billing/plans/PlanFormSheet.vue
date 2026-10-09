<script setup lang="ts">
import type { PlanFormState, PlanPresetKey } from '@/lib/planForm'
import type { Plan } from '@/lib/types'
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { CircleAlert, Loader2, Plus, RefreshCw, Sparkles } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { fetchAllPlans, modelsApi, plansApi } from '@/lib/endpoints'
import { buildPlanPayload, emptyPlanForm, emptyRuleForm, formFromPlan, MAX_RULES, PLAN_PRESETS, presetRules, ruleErrorKey, validatePlanForm } from '@/lib/planForm'
import { useAuthStore } from '@/stores/auth'
import RuleEditor from './RuleEditor.vue'

const props = defineProps<{
  /** Plan being edited; null = create. */
  plan: Plan | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [plan: Plan, created: boolean] }>()

const auth = useAuthStore()
const { currency } = useCurrency()

const form = reactive<PlanFormState>(emptyPlanForm())
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const conflict = ref(false)
const saving = ref(false)
const reloading = ref(false)
const current = ref<Plan | null>(null)
const isEdit = computed(() => current.value !== null)

function reset(p: Plan | null) {
  current.value = p
  Object.assign(form, p ? formFromPlan(p) : emptyPlanForm())
  errors.value = {}
  formError.value = null
  conflict.value = false
}
watch(open, (v) => {
  if (v)
    reset(props.plan)
})

// ---------- model options ----------
const allModels = ref<string[]>([])
const modelsLoading = ref(false)
onMounted(async () => {
  modelsLoading.value = true
  try {
    const res = auth.can('models.manage') ? await modelsApi.listAll() : await modelsApi.list()
    allModels.value = res.items.map(m => m.model).sort()
  }
  catch {
    // Free-text entry still works.
  }
  finally {
    modelsLoading.value = false
  }
})
const planModelOptions = computed(() => [...new Set([...allModels.value, ...form.models])].map(m => ({ value: m, label: m })))
/** A rule can only meter models the plan covers. */
const ruleModelOptions = computed(() => (form.models.length ? form.models : allModels.value))
const weightModelOptions = computed(() => [...new Set([...form.models, ...allModels.value])])

// ---------- duration ----------
const DURATION_UNITS = [{ v: 'h', label: '小时' }, { v: 'd', label: '天' }, { v: 'm', label: '分钟' }] as const
function setDurationUnit(v: unknown) {
  if (v === 'm' || v === 'h' || v === 'd')
    form.durationUnit = v
}

// ---------- rules ----------
function takenIds(uid: number): string[] {
  return form.rules.filter(r => r.uid !== uid).map(r => r.id.trim())
}
function ruleErrors(i: number): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(errors.value)) {
    const m = ruleErrorKey(k)
    if (m && m.index === i)
      out[m.field] = v
  }
  return out
}
function clearRuleErrors() {
  errors.value = Object.fromEntries(Object.entries(errors.value).filter(([k]) => !ruleErrorKey(k)))
}
function addRule() {
  if (form.rules.length >= MAX_RULES)
    return
  form.rules.push(emptyRuleForm(form.rules.map(r => r.id.trim())))
}
function removeRule(i: number) {
  form.rules.splice(i, 1)
  // Server error keys are positional; they no longer line up.
  clearRuleErrors()
}
function applyPreset(key: PlanPresetKey) {
  form.rules = presetRules(key)
  clearRuleErrors()
  toast.info('已填入预设规则', { description: '可继续修改上限、窗口与计量模型。' })
}

// ---------- errors ----------
const KNOWN = new Set(['name', 'description', 'listPrice', 'duration', 'models', 'rules', 'stackable'])
const otherErrors = computed(() => Object.entries(errors.value).filter(([k]) => {
  if (KNOWN.has(k))
    return false
  const m = ruleErrorKey(k)
  // Rule errors render inside their rule editor when the rule exists.
  return !(m && m.index < form.rules.length)
}))

function revealFirstError() {
  void nextTick(() => {
    document.querySelector('#plan-form [role=alert]')?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  })
}

async function submit() {
  formError.value = null
  errors.value = validatePlanForm(form)
  if (Object.keys(errors.value).length > 0) {
    formError.value = '请修正标记的字段后再保存'
    revealFirstError()
    return
  }
  saving.value = true
  try {
    const body = buildPlanPayload(form)
    const saved = current.value
      ? await plansApi.update(current.value.id, { ...body, version: current.value.version })
      : await plansApi.create(body)
    toast.success(current.value ? '套餐已保存' : '套餐已创建')
    emit('saved', saved, !current.value)
    open.value = false
  }
  catch (err) {
    if (isVersionConflict(err)) {
      conflict.value = true
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
    }
    else {
      formError.value = errorMessage(err)
    }
    revealFirstError()
  }
  finally {
    saving.value = false
  }
}

async function reloadLatest() {
  if (!current.value)
    return
  reloading.value = true
  try {
    const id = current.value.id
    const latest = (await fetchAllPlans()).find(p => p.id === id)
    if (!latest) {
      formError.value = '套餐已不存在'
      return
    }
    reset(latest)
    toast.info('已加载最新数据，请重新修改后保存')
  }
  catch (err) {
    formError.value = errorMessage(err)
  }
  finally {
    reloading.value = false
  }
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-6xl" @interact-outside="(e: Event) => saving && e.preventDefault()">
      <SheetHeader class="border-b">
        <SheetTitle>{{ isEdit ? `编辑套餐：${current?.name}` : '新建套餐' }}</SheetTitle>
        <SheetDescription>
          套餐由有效期、覆盖模型和若干配额规则组成。修改套餐不影响已开通的订阅（订阅保存开通时的模型与规则快照）。
        </SheetDescription>
      </SheetHeader>

      <form id="plan-form" class="flex-1 space-y-6 overflow-y-auto p-4" novalidate @submit.prevent="submit">
        <div v-if="conflict" class="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3 text-sm" role="alert">
          <p class="text-destructive flex items-center gap-1.5 font-medium">
            <CircleAlert class="size-4" />
            该套餐已被他人修改
          </p>
          <p class="text-muted-foreground">
            你的修改尚未保存。加载最新数据会丢弃当前表单中的修改。
          </p>
          <Button type="button" variant="outline" size="sm" :disabled="reloading" @click="reloadLatest">
            <RefreshCw :class="reloading ? 'animate-spin' : ''" />
            加载最新数据
          </Button>
        </div>

        <!-- 基本信息 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            基本信息
          </h3>
          <div class="grid gap-4 xl:grid-cols-2">
            <div class="space-y-4">
              <FormField label="名称" for="plan-name" required :error="errors.name">
                <Input id="plan-name" v-model="form.name" maxlength="100" placeholder="例如：Pro 月度套餐" :aria-invalid="!!errors.name" />
              </FormField>
              <FormField label="描述" for="plan-desc" :error="errors.description" hint="展示在用户的套餐目录中，最多 2000 字。">
                <Textarea id="plan-desc" v-model="form.description" rows="3" maxlength="2000" placeholder="套餐包含的内容与适用场景" :aria-invalid="!!errors.description" />
              </FormField>
            </div>
            <div class="space-y-4">
              <div class="grid gap-4 sm:grid-cols-2">
                <FormField :label="`标价（${currency?.code ?? '结算币种'}）`" for="plan-price" :error="errors.listPrice" hint="仅用于展示，不会扣费；留空表示不标价。">
                  <div class="relative">
                    <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
                    <Input id="plan-price" v-model="form.listPrice" inputmode="decimal" placeholder="可选" class="pl-7 font-mono tabular-nums" :aria-invalid="!!errors.listPrice" />
                  </div>
                </FormField>
                <FormField label="每份有效期" for="plan-duration" required :error="errors.duration" hint="开通 N 份即 N × 有效期；1 小时到 366 天。">
                  <div class="flex gap-2">
                    <Input id="plan-duration" v-model="form.durationN" type="number" min="1" step="1" class="min-w-16 tabular-nums" :aria-invalid="!!errors.duration" />
                    <Select :model-value="form.durationUnit" @update:model-value="setDurationUnit">
                      <SelectTrigger class="w-24 shrink-0" aria-label="有效期单位">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem v-for="u in DURATION_UNITS" :key="u.v" :value="u.v">
                          {{ u.label }}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </FormField>
              </div>
              <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
                <div class="space-y-1">
                  <Label for="plan-stackable">可叠加</Label>
                  <p class="text-muted-foreground text-xs">
                    <template v-if="form.stackable">
                      开启：每次开通（兑换或管理员开通）都会生成一份新的独立订阅，额度相互叠加。
                    </template>
                    <template v-else>
                      关闭：用户已有该套餐的有效订阅时，再次开通只会延长现有订阅的到期时间，额度不叠加。
                    </template>
                  </p>
                </div>
                <Switch id="plan-stackable" v-model="form.stackable" />
              </div>
            </div>
          </div>
        </section>

        <Separator />

        <!-- 覆盖模型 -->
        <section class="space-y-3">
          <h3 class="text-sm font-semibold">
            覆盖模型
          </h3>
          <FormField for="plan-models" :error="errors.models" hint="订阅只覆盖这些逻辑模型的请求；留空表示全部模型。未覆盖的模型照常从钱包扣费。可输入列表外的模型名。">
            <MultiSelect id="plan-models" v-model="form.models" :options="planModelOptions" :loading="modelsLoading" allow-custom empty-label="全部模型" placeholder="搜索或输入模型名" />
          </FormField>
        </section>

        <Separator />

        <!-- 配额规则 -->
        <section class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-semibold">
              配额规则
              <span class="text-muted-foreground font-normal">（{{ form.rules.length }} / {{ MAX_RULES }}）</span>
            </h3>
          </div>
          <p class="text-muted-foreground text-xs">
            所有适用规则都未超限时，请求才使用套餐额度（不扣钱包）。规则按其计量与窗口独立统计；失败的请求不计入。额度用完后是阻断（429）还是改用钱包按量计费，由用户在「钱包与订阅」中自行设置（默认阻断），API Key 也可单独覆盖。
          </p>
          <div class="bg-muted/40 space-y-2 rounded-lg border border-dashed p-3">
            <p class="flex items-center gap-1.5 text-xs font-medium">
              <Sparkles class="size-3.5" />
              预设（替换当前规则）
            </p>
            <div class="flex flex-wrap gap-2">
              <Button v-for="p in PLAN_PRESETS" :key="p.key" type="button" variant="outline" size="sm" class="h-auto min-h-8 py-1.5 text-left whitespace-normal" :title="p.description" @click="applyPreset(p.key)">
                {{ p.label }}
              </Button>
            </div>
          </div>
          <p v-if="errors.rules" class="text-destructive text-xs" role="alert">
            {{ errors.rules }}
          </p>
          <div class="space-y-3">
            <RuleEditor
              v-for="(r, i) in form.rules"
              :key="r.uid"
              v-model:rule="form.rules[i]!"
              :index="i"
              :errors="ruleErrors(i)"
              :taken-ids="takenIds(r.uid)"
              :model-options="ruleModelOptions"
              :weight-model-options="weightModelOptions"
              :can-remove="form.rules.length > 1"
              @remove="removeRule(i)"
            />
          </div>
          <Button type="button" variant="outline" size="sm" :disabled="form.rules.length >= MAX_RULES" @click="addRule">
            <Plus />
            添加规则
          </Button>
        </section>

        <div v-if="otherErrors.length" class="text-destructive space-y-1 text-xs" role="alert">
          <p v-for="[k, v] in otherErrors" :key="k">
            {{ k }}：{{ v }}
          </p>
        </div>
      </form>

      <SheetFooter class="border-t sm:flex-row sm:items-center sm:justify-end">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button type="button" variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="plan-form" :disabled="saving || conflict">
          <Loader2 v-if="saving" class="animate-spin" />
          {{ isEdit ? '保存' : '创建' }}
        </Button>
      </SheetFooter>
    </SheetContent>
  </Sheet>
</template>
