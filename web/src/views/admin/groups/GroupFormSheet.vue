<script setup lang="ts">
import type { GroupForm } from '@/lib/groups'
import type { UserGroup } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { ArrowRight, Info, Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import TimezoneSelect from '@/components/TimezoneSelect.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { groupsApi } from '@/lib/endpoints'
import {
  buildGroupInput,
  buildGroupPatch,
  emptyGroupForm,
  formFromGroup,
  GROUP_DESCRIPTION_MAX,
  GROUP_ERROR_MESSAGES,
  GROUP_NAME_MAX,
  isPatchEmpty,
  isValidMultiplier,
  multiplierShort,
  multiplierWords,
  multiplyAmount,
  validateGroupForm,
} from '@/lib/groups'

/** Create / edit a user group (phase8 §1). */
const props = defineProps<{
  /** Group being edited; null = create. */
  group: UserGroup | null
  /** Current default group (for the "replaces …" hint). */
  defaultGroup: UserGroup | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [group: UserGroup, created: boolean], stale: [] }>()

const { currency, money } = useCurrency()

const form = ref<GroupForm>(emptyGroupForm())
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)
const isEdit = computed(() => props.group !== null)

watch(open, (v) => {
  if (!v)
    return
  form.value = props.group ? formFromGroup(props.group) : emptyGroupForm()
  errors.value = {}
  formError.value = null
})

const MULTIPLIER_PRESETS = ['1', '0.9', '0.8', '0.5', '0']

const multiplierValid = computed(() => isValidMultiplier(form.value.priceMultiplier.trim()))
const example = computed(() => {
  if (!multiplierValid.value)
    return null
  const after = multiplyAmount('1', form.value.priceMultiplier.trim())
  return after === null ? null : { before: money('1'), after: money(after), words: multiplierWords(form.value.priceMultiplier) }
})
const limitsEmpty = computed(() => !form.value.rpm.trim() && !form.value.rpd.trim() && !form.value.dailySpend.trim() && !form.value.monthlySpend.trim())

const LIMIT_FIELDS = [
  { key: 'rpm', label: '每分钟请求数（RPM）', unit: '次/分钟', money: false },
  { key: 'rpd', label: '每天请求数', unit: '次/天', money: false },
  { key: 'dailySpend', label: '每天消费上限', unit: '/天', money: true },
  { key: 'monthlySpend', label: '每月消费上限', unit: '/月', money: true },
] as const

const replacesDefault = computed(() => form.value.isDefault && !props.group?.isDefault && props.defaultGroup && props.defaultGroup.id !== props.group?.id ? props.defaultGroup.name : null)

async function submit() {
  formError.value = null
  errors.value = validateGroupForm(form.value)
  if (Object.keys(errors.value).length) {
    formError.value = '请修正标记的字段'
    return
  }
  saving.value = true
  try {
    const g = props.group
    if (!g) {
      const created = await groupsApi.create(buildGroupInput(form.value))
      toast.success(`已创建分组「${created.name}」`)
      emit('saved', created, true)
    }
    else {
      const patch = buildGroupPatch(form.value, g)
      if (isPatchEmpty(patch)) {
        open.value = false
        return
      }
      const updated = await groupsApi.update(g.id, patch)
      toast.success(`已保存分组「${updated.name}」`)
      emit('saved', updated, false)
    }
    open.value = false
  }
  catch (err) {
    if (isVersionConflict(err)) {
      formError.value = '该分组已被他人修改，请关闭后刷新列表再试'
      emit('stale')
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
    }
    else if (isApiError(err) && GROUP_ERROR_MESSAGES[err.code]) {
      if (err.code.includes('name'))
        errors.value = { name: GROUP_ERROR_MESSAGES[err.code]! }
      formError.value = GROUP_ERROR_MESSAGES[err.code]!
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
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-2xl" data-testid="group-sheet" @interact-outside="(e: Event) => saving && e.preventDefault()">
      <SheetHeader class="border-b pr-12">
        <SheetTitle>{{ isEdit ? `编辑分组：${group?.name}` : '新建用户组' }}</SheetTitle>
        <SheetDescription>
          用户组决定成员调用平台渠道时的售价倍率与用量限额。每位用户恰好属于一个分组。
        </SheetDescription>
      </SheetHeader>

      <form id="group-form" class="flex-1 space-y-6 overflow-y-auto p-4 sm:p-6" novalidate @submit.prevent="submit">
        <section class="space-y-4" aria-labelledby="gf-basic">
          <h3 id="gf-basic" class="text-sm font-semibold">
            基本信息
          </h3>
          <FormField label="名称" for="gf-name" required :error="errors.name">
            <Input id="gf-name" v-model="form.name" :maxlength="GROUP_NAME_MAX" placeholder="例如：VIP" :aria-invalid="!!errors.name" />
          </FormField>
          <FormField label="描述" for="gf-desc" :error="errors.description">
            <Textarea id="gf-desc" v-model="form.description" rows="2" :maxlength="GROUP_DESCRIPTION_MAX" placeholder="可选，例如：年付客户，平台模型八折" />
            <template #hint>
              <span class="tabular-nums">{{ [...form.description].length }} / {{ GROUP_DESCRIPTION_MAX }}</span>
            </template>
          </FormField>
        </section>

        <section class="space-y-3" aria-labelledby="gf-mult">
          <h3 id="gf-mult" class="text-sm font-semibold">
            价格倍率
          </h3>
          <FormField for="gf-multiplier" :error="errors.priceMultiplier">
            <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
              <div class="relative w-full sm:w-40">
                <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">×</span>
                <Input id="gf-multiplier" v-model="form.priceMultiplier" inputmode="decimal" class="pl-6 font-mono tabular-nums" aria-label="价格倍率" :aria-invalid="!!errors.priceMultiplier" data-testid="gf-multiplier" />
              </div>
              <div class="flex flex-wrap gap-1.5">
                <button
                  v-for="m in MULTIPLIER_PRESETS"
                  :key="m"
                  type="button"
                  class="hover:bg-muted h-7 rounded-md border px-2 text-xs tabular-nums transition-colors"
                  :class="form.priceMultiplier.trim() === m ? 'border-primary bg-primary/5 text-foreground' : 'text-muted-foreground'"
                  @click="form.priceMultiplier = m"
                >
                  {{ multiplierShort(m) }} {{ multiplierWords(m) }}
                </button>
              </div>
            </div>
          </FormField>
          <div class="bg-muted/40 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border px-3 py-2 text-sm" data-testid="gf-example" aria-live="polite">
            <template v-if="example">
              <span class="text-muted-foreground">示例</span>
              <span class="tabular-nums">售价 {{ example.before }}</span>
              <ArrowRight class="text-muted-foreground size-3.5" aria-hidden="true" />
              <span class="font-medium tabular-nums">{{ example.after }}</span>
              <span class="text-muted-foreground text-xs">（{{ example.words }}）</span>
            </template>
            <span v-else class="text-muted-foreground">输入 0–100 之间的倍率后显示示例</span>
          </div>
          <p class="text-muted-foreground text-xs">
            作用于平台渠道的售价（在分时价格倍率之后再乘），套餐按售价折算的计量同样使用乘过倍率的金额；自有 / 共享渠道仍然免费。0 表示平台渠道免费。
          </p>
        </section>

        <section class="space-y-3" aria-labelledby="gf-limits">
          <div class="flex flex-wrap items-baseline justify-between gap-2">
            <h3 id="gf-limits" class="text-sm font-semibold">
              用量限额
              <span class="text-muted-foreground font-normal">（每位成员）</span>
            </h3>
            <span v-if="limitsEmpty" class="text-muted-foreground text-xs" data-testid="gf-unlimited">当前：不限</span>
          </div>
          <div class="grid gap-4 sm:grid-cols-2" data-testid="gf-limit-grid">
            <FormField v-for="f in LIMIT_FIELDS" :key="f.key" :label="f.label" :for="`gf-${f.key}`" :error="errors[`limits.${f.key}`]">
              <div class="relative">
                <span v-if="f.money" class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
                <Input
                  :id="`gf-${f.key}`"
                  v-model="form[f.key]"
                  :inputmode="f.money ? 'decimal' : 'numeric'"
                  placeholder="不限"
                  class="pr-16 tabular-nums"
                  :class="f.money ? 'pl-7' : ''"
                  :aria-invalid="!!errors[`limits.${f.key}`]"
                  :data-testid="`gf-${f.key}`"
                />
                <span class="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs">{{ f.unit }}</span>
              </div>
            </FormField>
          </div>
          <div class="text-muted-foreground flex gap-2 text-xs">
            <Info class="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            <p>
              留空表示不限。请求数限额对所有渠道生效（每位用户全部 API Key 合计，RPM 为每个网关实例的上限）；消费上限只统计平台渠道的计费金额，自有 / 共享渠道不受约束。超出时网关返回 429。限额为软上限，并发请求可能略微超出。
            </p>
          </div>
        </section>

        <section class="space-y-4" aria-labelledby="gf-misc">
          <h3 id="gf-misc" class="text-sm font-semibold">
            时区与默认
          </h3>
          <FormField label="时区" for="gf-tz" :error="errors.timezone" hint="每日 / 每月限额与 API Key 消费上限按此时区划分窗口（每周从周一开始）。">
            <TimezoneSelect id="gf-tz" v-model="form.timezone" :invalid="!!errors.timezone" />
          </FormField>
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="min-w-0 space-y-1">
              <label for="gf-default" class="text-sm font-medium">设为默认分组</label>
              <p class="text-muted-foreground text-xs">
                新用户首次登录时自动加入默认分组，删除其他分组时其成员也会移到这里。有且只有一个默认分组。
              </p>
              <p v-if="group?.isDefault" class="text-xs" data-testid="gf-default-locked">
                这是当前的默认分组。要更换，请把另一个分组设为默认。
              </p>
              <p v-else-if="replacesDefault" class="text-xs text-amber-700 dark:text-amber-400" data-testid="gf-default-replaces">
                保存后「{{ replacesDefault }}」将不再是默认分组（已有成员不受影响）。
              </p>
            </div>
            <Switch id="gf-default" v-model="form.isDefault" :disabled="group?.isDefault" data-testid="gf-default" />
          </div>
        </section>
      </form>

      <SheetFooter class="border-t sm:flex-row sm:items-center sm:justify-end">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button type="button" variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="group-form" :disabled="saving" data-testid="gf-submit">
          <Loader2 v-if="saving" class="animate-spin" />
          {{ isEdit ? '保存' : '创建' }}
        </Button>
      </SheetFooter>
    </SheetContent>
  </Sheet>
</template>
