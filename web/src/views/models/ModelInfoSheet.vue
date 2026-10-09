<script setup lang="ts">
import type { ModelInfoForm, ModelInfoRow } from '@/lib/modelInfoForm'
import type { ModelInfo } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { CircleAlert, Loader2, RefreshCw } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import SuggestInput from '@/components/SuggestInput.vue'
import TagsInput from '@/components/TagsInput.vue'
import PlazaModelCard from '@/components/plaza/PlazaModelCard.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { modelInfoApi } from '@/lib/endpoints'
import {
  buildModelInfoInput,
  formFromModelInfo,
  isModelInfoDirty,
  MODEL_INFO_LIMITS,
  parseTokenLimit,
  previewPlazaModel,
  validateModelInfo,
  VENDOR_SUGGESTIONS,
} from '@/lib/modelInfoForm'
import { CAPABILITIES, formatTokenCount } from '@/lib/plaza'

/** Create / edit the model-info row of one logical model (PUT with `version`). */
const props = defineProps<{
  row: ModelInfoRow | null
  /** Vendors already in use (suggestions). */
  vendors: string[]
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [info: ModelInfo] }>()

const { currency } = useCurrency()
const L = MODEL_INFO_LIMITS

/** The row being edited (replaced by the latest copy after a conflict reload). */
const base = ref<ModelInfo | null>(null)
const form = ref<ModelInfoForm>(formFromModelInfo(null))
const errors = ref<Record<string, string>>({})
const formError = ref('')
const conflict = ref(false)
const saving = ref(false)
const reloading = ref(false)

watch([open, () => props.row], ([o, row]) => {
  if (!o || !row)
    return
  base.value = row.info
  form.value = formFromModelInfo(row.info)
  errors.value = {}
  formError.value = ''
  conflict.value = false
}, { immediate: true })

const model = computed(() => props.row?.model ?? '')
const isEdit = computed(() => base.value !== null)
const dirty = computed(() => isModelInfoDirty(form.value, base.value))
const vendorOptions = computed(() => [...new Set([...props.vendors, ...VENDOR_SUGGESTIONS])])
const preview = computed(() => previewPlazaModel(model.value, form.value, props.row?.entry ?? null))

/** Fields with an inline error slot; anything else (capabilities, hidden, …) is listed at the bottom. */
const INLINE = new Set(['displayName', 'description', 'vendor', 'tags', 'contextWindow', 'maxOutput', 'sortOrder'])
const otherErrors = computed(() => Object.entries(errors.value).filter(([k]) => !INLINE.has(k)))
function errorLabel(key: string): string {
  if (key === 'hidden')
    return '在模型广场中隐藏'
  const cap = CAPABILITIES.find(c => key === `capabilities.${c.key}`)
  if (cap)
    return `能力「${cap.label}」`
  return key === 'capabilities' ? '能力' : key
}

/** "= 128,000 tokens" under the K / M inputs. */
function tokenHint(v: string): string | undefined {
  const n = parseTokenLimit(v)
  if (n === null || !Number.isSafeInteger(n) || n <= 0)
    return undefined
  return `= ${n.toLocaleString('zh-CN')} tokens（显示为 ${formatTokenCount(n)}）`
}

/** 422 details may use `tags.3` / `capabilities.vision`; fold them onto the field. */
function foldErrors(details: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(details)) {
    const head = k.split(/[.[]/)[0] ?? k
    const key = head === 'tags' ? 'tags' : k
    out[key] ??= v
  }
  return out
}

async function submit() {
  formError.value = ''
  const local = validateModelInfo(form.value)
  errors.value = local
  if (Object.keys(local).length) {
    formError.value = '请修正标出的字段'
    return
  }
  saving.value = true
  try {
    const saved = await modelInfoApi.save(model.value, buildModelInfoInput(form.value, base.value?.version))
    toast.success(isEdit.value ? `已更新「${model.value}」的模型资料` : `已添加「${model.value}」的模型资料`)
    emit('saved', saved)
    open.value = false
  }
  catch (err) {
    if (isVersionConflict(err) || (isApiError(err) && err.status === 409)) {
      conflict.value = true
      formError.value = '模型资料已被他人修改，请加载最新数据后再保存'
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = foldErrors(fieldErrors(err))
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

/** Conflict: reload the latest row (drops the local edits). */
async function reloadLatest() {
  reloading.value = true
  try {
    const { items } = await modelInfoApi.list()
    const latest = items.find(i => i.model === model.value) ?? null
    base.value = latest
    form.value = formFromModelInfo(latest)
    errors.value = {}
    formError.value = ''
    conflict.value = false
    if (latest)
      emit('saved', latest)
  }
  catch (err) {
    toast.error('无法加载最新数据', { description: errorMessage(err) })
  }
  finally {
    reloading.value = false
  }
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-5xl" data-testid="model-info-sheet" @interact-outside="(e: Event) => saving && e.preventDefault()">
      <SheetHeader class="border-b pr-12">
        <SheetTitle class="truncate">
          {{ isEdit ? '编辑模型资料' : '填写模型资料' }}：<span class="font-mono">{{ model }}</span>
        </SheetTitle>
        <SheetDescription>
          展示在模型广场与「我的模型」中的名称、简介、规格与能力。不影响路由与计费；没有资料的模型照常可用，广场中只显示模型名。
        </SheetDescription>
      </SheetHeader>

      <div class="grid flex-1 overflow-y-auto xl:grid-cols-[minmax(0,1fr)_360px] xl:overflow-hidden">
        <form id="model-info-form" class="min-w-0 space-y-5 p-4 sm:p-6 xl:overflow-y-auto" novalidate @submit.prevent="submit">
          <div v-if="conflict" class="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3 text-sm" role="alert" data-testid="model-info-conflict">
            <p class="text-destructive flex items-center gap-1.5 font-medium">
              <CircleAlert class="size-4" />
              模型资料已被他人修改
            </p>
            <p class="text-muted-foreground">
              你的修改尚未保存。加载最新数据会丢弃当前表单中的修改。
            </p>
            <Button type="button" variant="outline" size="sm" :disabled="reloading" @click="reloadLatest">
              <RefreshCw :class="reloading ? 'animate-spin' : ''" />
              加载最新数据
            </Button>
          </div>

          <div class="grid gap-4 sm:grid-cols-2" data-row="name-vendor">
            <FormField label="显示名称" for="mi-name" :error="errors.displayName" :hint="`留空则显示模型名；最多 ${L.displayName} 个字符`">
              <Input id="mi-name" v-model="form.displayName" :maxlength="L.displayName" :placeholder="model" :aria-invalid="!!errors.displayName" />
            </FormField>
            <FormField label="厂商" for="mi-vendor" :error="errors.vendor" :hint="`如 OpenAI、DeepSeek；最多 ${L.vendor} 个字符`">
              <SuggestInput id="mi-vendor" v-model="form.vendor" :options="vendorOptions" :maxlength="L.vendor" placeholder="选择或输入厂商" clearable :invalid="!!errors.vendor" />
            </FormField>
          </div>

          <FormField label="简介" for="mi-desc" :error="errors.description">
            <Textarea id="mi-desc" v-model="form.description" rows="4" :maxlength="L.description" placeholder="一两句话介绍模型的特点与适用场景（纯文本）" :aria-invalid="!!errors.description" />
            <template #hint>
              纯文本，显示在模型详情中。{{ form.description.trim().length }} / {{ L.description }}
            </template>
          </FormField>

          <FormField label="标签" for="mi-tags" :error="errors.tags" :hint="`回车或逗号添加；最多 ${L.tags} 个，每个不超过 ${L.tag} 个字符。可用于搜索。`">
            <TagsInput id="mi-tags" v-model="form.tags" :max-length="L.tag" placeholder="如 旗舰、长上下文，回车添加" :invalid="!!errors.tags" />
          </FormField>

          <div class="grid gap-4 sm:grid-cols-3" data-row="limits">
            <FormField label="上下文长度" for="mi-ctx" :error="errors.contextWindow" :hint="tokenHint(form.contextWindow) ?? '单位 token，可写 128K、1M；留空表示未知'">
              <Input id="mi-ctx" v-model="form.contextWindow" inputmode="numeric" placeholder="如 128K" class="tabular-nums" :aria-invalid="!!errors.contextWindow" />
            </FormField>
            <FormField label="最大输出" for="mi-out" :error="errors.maxOutput" :hint="tokenHint(form.maxOutput) ?? '单次回复的最大 token 数；留空表示未知'">
              <Input id="mi-out" v-model="form.maxOutput" inputmode="numeric" placeholder="如 8K" class="tabular-nums" :aria-invalid="!!errors.maxOutput" />
            </FormField>
            <FormField label="排序" for="mi-sort" :error="errors.sortOrder" hint="越小越靠前，默认 0">
              <Input id="mi-sort" v-model="form.sortOrder" inputmode="numeric" class="tabular-nums" :aria-invalid="!!errors.sortOrder" />
            </FormField>
          </div>

          <fieldset class="space-y-2">
            <legend class="mb-2 text-sm font-medium">
              能力
            </legend>
            <div class="grid gap-2 sm:grid-cols-2">
              <label v-for="c in CAPABILITIES" :key="c.key" class="hover:bg-muted/40 flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3">
                <span class="min-w-0">
                  <span class="block text-sm font-medium">{{ c.label }}</span>
                  <span class="text-muted-foreground block text-xs">{{ c.description }}</span>
                </span>
                <Switch v-model="form.capabilities[c.key]" :data-testid="`cap-${c.key}`" />
              </label>
            </div>
          </fieldset>

          <label class="hover:bg-muted/40 flex cursor-pointer items-center justify-between gap-3 rounded-lg border p-3">
            <span class="min-w-0">
              <span class="block text-sm font-medium">在模型广场中隐藏</span>
              <span class="text-muted-foreground block text-xs">隐藏后不出现在任何模型广场中，但仍可调用，并显示在可调用用户的「我的模型」中。</span>
            </span>
            <Switch v-model="form.hidden" data-testid="mi-hidden" />
          </label>

          <div v-if="otherErrors.length" class="text-destructive space-y-0.5 text-xs" role="alert">
            <p v-for="[k, v] in otherErrors" :key="k">
              {{ errorLabel(k) }}：{{ v }}
            </p>
          </div>
        </form>

        <!-- Live preview -->
        <aside class="bg-muted/30 min-w-0 space-y-3 border-t p-4 sm:p-6 xl:overflow-y-auto xl:border-t-0 xl:border-l" aria-label="广场预览" data-testid="model-info-preview">
          <p class="text-sm font-medium">
            在广场中预览
          </p>
          <PlazaModelCard :model="preview" :currency="currency" static />
          <p class="text-muted-foreground text-xs">
            价格取当前售价；可用协议与覆盖套餐由渠道和套餐决定，保存后在模型广场中显示。
            <template v-if="form.hidden">
              <span class="text-amber-700 dark:text-amber-400">已设置隐藏：该模型不会出现在模型广场中。</span>
            </template>
          </p>
        </aside>
      </div>

      <SheetFooter class="border-t sm:flex-row sm:items-center sm:justify-end">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert" data-testid="model-info-error">
          {{ formError }}
        </p>
        <Button type="button" variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="model-info-form" :disabled="saving || conflict || (isEdit && !dirty)" data-testid="model-info-save">
          <Loader2 v-if="saving" class="animate-spin" />
          {{ isEdit ? '保存' : '创建' }}
        </Button>
      </SheetFooter>
    </SheetContent>
  </Sheet>
</template>
