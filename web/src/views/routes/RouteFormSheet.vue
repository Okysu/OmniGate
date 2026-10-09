<script setup lang="ts">
import type { RouteFormState } from '@/lib/routeForm'
import type { Channel, Role, RouteRule, RouteStrategy } from '@/lib/types'
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { ArrowDown, ArrowUp, CircleAlert, Info, Loader2, Plus, RefreshCw, Trash2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import MultiSelect from '@/components/MultiSelect.vue'
import RetryOnPicker from '@/components/RetryOnPicker.vue'
import SuggestInput from '@/components/SuggestInput.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { routesApi } from '@/lib/endpoints'
import { ROLE_LABELS, ROLES } from '@/lib/format'
import { CHANNEL_TYPE_LABELS, SCOPE_LABELS } from '@/lib/labels'
import {
  buildRoutePatch,
  buildRoutePayload,
  channelServedModels,
  emptyRouteForm,
  emptyTarget,
  formFromRule,
  isGlob,
  isInlineRouteErrorKey,
  listError,
  MAX_DESCRIPTION,
  MAX_FALLBACKS,
  MAX_NAME,
  MAX_TARGETS,
  matchesAny,
  moveItem,
  STRATEGIES,
  STRATEGY_DESCRIPTIONS,
  STRATEGY_LABELS,
  targetErrorKey,
  targetErrors,
  validateRouteForm,
} from '@/lib/routeForm'
import ModelPatternsField from './ModelPatternsField.vue'

const props = defineProps<{
  /** Rule being edited; null = create. */
  rule: RouteRule | null
  /** Prefilled form for "duplicate" (create mode only). */
  draft?: RouteFormState | null
  /** Channels the admin can manage (full views). */
  channels: Channel[]
  /** Current logical models (`/api/models?scope=all`). */
  models: string[]
  modelsLoading?: boolean
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [rule: RouteRule, created: boolean] }>()

const form = reactive<RouteFormState>(emptyRouteForm())
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const conflict = ref(false)
const saving = ref(false)
const reloading = ref(false)
const current = ref<RouteRule | null>(null)
const isEdit = computed(() => current.value !== null)

function reset(r: RouteRule | null, draft?: RouteFormState | null) {
  current.value = r
  Object.assign(form, r ? formFromRule(r) : (draft ?? emptyRouteForm()))
  errors.value = {}
  formError.value = null
  conflict.value = false
}
watch(open, (v) => {
  if (v)
    reset(props.rule, props.draft)
})

// ---------- options ----------
const roleOptions = ROLES.map(r => ({ value: r, label: ROLE_LABELS[r] }))
const roles = computed<string[]>({
  get: () => form.roles,
  set: v => (form.roles = v.filter((x): x is Role => (ROLES as string[]).includes(x))),
})

const channelById = computed(() => new Map(props.channels.map(c => [c.id, c])))
const sortedChannels = computed(() => [...props.channels].sort((a, b) => b.priority - a.priority || a.name.localeCompare(b.name)))

function served(c: Channel): string[] {
  return channelServedModels(c, form.models)
}
const servingChannels = computed(() => sortedChannels.value.filter(c => c.status === 'enabled' && served(c).length > 0))
const unusedServing = computed(() => servingChannels.value.filter(c => !form.targets.some(t => t.channelId === c.id)))

// ---------- targets ----------
function addTarget(channelId = '') {
  if (form.targets.length >= MAX_TARGETS)
    return
  form.targets.push(emptyTarget(channelId))
}
function addAllServing() {
  for (const c of unusedServing.value)
    addTarget(c.id)
}
function removeTarget(i: number) {
  form.targets.splice(i, 1)
  // Positional server keys no longer line up.
  errors.value = Object.fromEntries(Object.entries(errors.value).filter(([k]) => !targetErrorKey(k)))
}
function setTargetChannel(i: number, v: unknown) {
  const row = form.targets[i]
  if (row && typeof v === 'string')
    row.channelId = v
}
function takenElsewhere(channelId: string, i: number): boolean {
  return form.targets.some((t, j) => j !== i && t.channelId === channelId)
}

const overrideHint = computed(() => {
  switch (form.strategy) {
    case 'priority':
      return '优先级与权重都会生效：先按优先级分组，组内按权重随机。'
    case 'weighted':
      return '「权重」策略忽略优先级，只有权重覆盖生效。'
    default:
      return `「${STRATEGY_LABELS[form.strategy]}」策略不使用优先级与权重排序；覆盖值仅在命中预览中显示。`
  }
})

// ---------- retry ----------
// ---------- fallbacks ----------
const fallbackDraft = ref('')
const fallbackSuggestions = computed(() => props.models.filter(m => !form.fallbackModels.includes(m) && !matchesAny(form.models, m)))
function addFallback() {
  const v = fallbackDraft.value.trim()
  if (!v || form.fallbackModels.includes(v) || form.fallbackModels.length >= MAX_FALLBACKS)
    return
  form.fallbackModels.push(v)
  fallbackDraft.value = ''
}
function moveFallback(i: number, d: number) {
  form.fallbackModels = moveItem(form.fallbackModels, i, i + d)
}
const fallbackMatchesRule = computed(() => form.fallbackModels.filter(m => !isGlob(m) && matchesAny(form.models, m)))

// ---------- strategy ----------
function setStrategy(s: RouteStrategy) {
  form.strategy = s
}

// ---------- errors ----------
const otherErrors = computed(() => Object.entries(errors.value).filter(([k]) => !isInlineRouteErrorKey(k, form.targets.length)))
const modelsError = computed(() => listError(errors.value, 'match.models'))
const rolesError = computed(() => listError(errors.value, 'match.roles'))
const fallbackError = computed(() => listError(errors.value, 'fallbackModels'))
const retryError = computed(() => errors.value['retry.retryOn'] ?? errors.value.retry)

function revealFirstError() {
  void nextTick(() => {
    document.querySelector('#route-form [role=alert]')?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  })
}

async function submit() {
  formError.value = null
  errors.value = validateRouteForm(form)
  if (Object.keys(errors.value).length > 0) {
    formError.value = '请修正标记的字段后再保存'
    revealFirstError()
    return
  }
  saving.value = true
  try {
    const saved = current.value
      ? await routesApi.update(current.value.id, buildRoutePatch(form, current.value.version))
      : await routesApi.create(buildRoutePayload(form))
    toast.success(current.value ? '路由规则已保存' : '路由规则已创建')
    emit('saved', saved, !current.value)
    open.value = false
  }
  catch (err) {
    if (isVersionConflict(err)) {
      conflict.value = true
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.code === 'route_target_invalid' && Object.keys(errors.value).length === 0
        ? `${err.message}（目标渠道不存在或不提供匹配的模型）`
        : err.message
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
    const latest = (await routesApi.list()).items.find(r => r.id === id)
    if (!latest) {
      formError.value = '该规则已被删除'
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
        <SheetTitle>{{ isEdit ? `编辑路由规则：${current?.name}` : '新建路由规则' }}</SheetTitle>
        <SheetDescription>
          规则命中后，按这里的策略、目标渠道、重试与回退设置为请求选择渠道。规则只会缩小候选范围，永远不会让用户使用本来无权使用的渠道。
        </SheetDescription>
      </SheetHeader>

      <form id="route-form" class="flex-1 space-y-6 overflow-y-auto p-4" novalidate @submit.prevent="submit">
        <div v-if="conflict" class="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3 text-sm" role="alert">
          <p class="text-destructive flex items-center gap-1.5 font-medium">
            <CircleAlert class="size-4" />
            该规则已被他人修改
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
          <div class="grid gap-4 lg:grid-cols-2">
            <div class="space-y-4">
              <FormField label="名称" for="route-name" required :error="errors.name">
                <Input id="route-name" v-model="form.name" :maxlength="MAX_NAME" placeholder="例如：GPT 优先走低价渠道" :aria-invalid="!!errors.name" />
              </FormField>
              <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
                <div class="space-y-1">
                  <Label for="route-enabled">启用</Label>
                  <p class="text-muted-foreground text-xs">
                    停用的规则不参与匹配，请求继续向下匹配其他规则或使用默认路由。
                  </p>
                </div>
                <Switch id="route-enabled" v-model="form.enabled" />
              </div>
            </div>
            <FormField label="描述" for="route-desc" :error="errors.description" :hint="`可选，最多 ${MAX_DESCRIPTION} 字。`">
              <Textarea id="route-desc" v-model="form.description" rows="4" :maxlength="MAX_DESCRIPTION" placeholder="规则的用途，方便其他管理员理解" :aria-invalid="!!errors.description" />
            </FormField>
          </div>
        </section>

        <Separator />

        <!-- 匹配条件 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            匹配条件
          </h3>
          <div class="grid gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
            <FormField label="模型" for="route-models" required :error="modelsError" hint="请求的逻辑模型名；支持 * 通配（gpt-* 匹配所有以 gpt- 开头的模型，* 匹配全部）。">
              <ModelPatternsField id="route-models" v-model="form.models" :models="models" :invalid="!!modelsError" />
            </FormField>
            <FormField label="用户角色" for="route-roles" :error="rolesError" hint="只对这些角色的用户生效；不选表示所有角色。">
              <MultiSelect id="route-roles" v-model="roles" :options="roleOptions" empty-label="所有角色" placeholder="搜索角色" />
            </FormField>
          </div>
        </section>

        <Separator />

        <!-- 选路策略 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            选路策略
          </h3>
          <div role="radiogroup" aria-label="选路策略" class="grid gap-2 sm:grid-cols-2 xl:grid-cols-5">
            <label
              v-for="s in STRATEGIES"
              :key="s"
              class="has-checked:border-primary has-checked:bg-primary/5 has-focus-visible:ring-ring/50 hover:bg-muted/50 flex cursor-pointer flex-col gap-1 rounded-lg border p-3 has-focus-visible:ring-3"
              :data-testid="`strategy-${s}`"
            >
              <span class="flex items-center gap-2">
                <input type="radio" name="route-strategy" class="accent-primary size-3.5" :value="s" :checked="form.strategy === s" @change="setStrategy(s)">
                <span class="text-sm font-medium">{{ STRATEGY_LABELS[s] }}</span>
              </span>
              <span class="text-muted-foreground text-xs leading-relaxed">{{ STRATEGY_DESCRIPTIONS[s] }}</span>
            </label>
          </div>
          <p v-if="errors.strategy" class="text-destructive text-xs" role="alert">
            {{ errors.strategy }}
          </p>
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="route-native">优先原生协议</Label>
              <p class="text-muted-foreground text-xs">
                开启（默认）：同一档候选中，优先选择与客户端协议相同、无需协议转换的渠道，减少转换损耗。关闭：完全按策略排序，不考虑协议。
              </p>
            </div>
            <Switch id="route-native" v-model="form.nativeFirst" />
          </div>
        </section>

        <Separator />

        <!-- 目标渠道 -->
        <section class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-semibold">
              目标渠道
              <span class="text-muted-foreground font-normal">（可选，{{ form.targets.length }} / {{ MAX_TARGETS }}）</span>
            </h3>
            <Button v-if="unusedServing.length && form.models.length" type="button" variant="ghost" size="sm" @click="addAllServing">
              <Plus />
              添加全部提供匹配模型的渠道（{{ unusedServing.length }}）
            </Button>
          </div>
          <div class="bg-muted/40 text-muted-foreground flex gap-2 rounded-lg border border-dashed p-3 text-xs">
            <Info class="mt-0.5 size-3.5 shrink-0" />
            <div class="space-y-1">
              <p>不添加目标渠道时，使用该模型的全部可用渠道（沿用渠道自身的优先级与权重）。添加后，只在这些渠道中选择。</p>
              <p><strong class="text-foreground">规则永远不会扩大访问范围</strong>：最终候选 = 用户本来可以使用的渠道 ∩ 目标渠道。用户无权使用的渠道即使列在这里也会被跳过。</p>
            </div>
          </div>
          <p v-if="errors.targets" class="text-destructive text-xs" role="alert">
            {{ errors.targets }}
          </p>
          <div v-if="form.targets.length" class="space-y-2">
            <div class="text-muted-foreground hidden grid-cols-[minmax(0,1fr)_9rem_9rem_auto] gap-2 text-xs md:grid">
              <span>渠道</span><span>优先级覆盖</span><span>权重覆盖</span><span class="w-8" />
            </div>
            <div v-for="(t, i) in form.targets" :key="t.uid" class="grid gap-2 rounded-lg border p-2 md:grid-cols-[minmax(0,1fr)_9rem_9rem_auto] md:border-0 md:p-0" :data-target-index="i">
              <div class="min-w-0 space-y-1">
                <Select :model-value="t.channelId || undefined" @update:model-value="(v) => setTargetChannel(i, v)">
                  <SelectTrigger class="w-full" :aria-label="`第 ${i + 1} 个目标渠道`" :aria-invalid="!!targetErrors(errors, i).channelId">
                    <SelectValue placeholder="选择渠道">
                      <span v-if="t.channelId" class="truncate">{{ channelById.get(t.channelId)?.name ?? t.channelId }}</span>
                      <span v-else class="text-muted-foreground">选择渠道</span>
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="c in sortedChannels" :key="c.id" :value="c.id" :disabled="takenElsewhere(c.id, i)">
                      <span class="truncate">{{ c.name }}</span>
                      <span class="text-muted-foreground ml-2 text-xs">{{ CHANNEL_TYPE_LABELS[c.type] ?? c.type }} · {{ c.status === 'enabled' ? `优先级 ${c.priority} / 权重 ${c.weight}` : '已停用' }}</span>
                    </SelectItem>
                  </SelectContent>
                </Select>
                <p v-if="targetErrors(errors, i).channelId" class="text-destructive text-xs" role="alert">
                  {{ targetErrors(errors, i).channelId }}
                </p>
                <template v-else-if="t.channelId">
                  <p v-if="channelById.get(t.channelId)" class="flex flex-wrap items-center gap-1.5 text-xs">
                    <Badge variant="outline" class="h-4 px-1 text-[10px]">
                      {{ CHANNEL_TYPE_LABELS[channelById.get(t.channelId)!.type] ?? channelById.get(t.channelId)!.type }}
                    </Badge>
                    <Badge variant="outline" class="h-4 px-1 text-[10px]">
                      {{ SCOPE_LABELS[channelById.get(t.channelId)!.scope] }}
                    </Badge>
                    <Badge v-if="channelById.get(t.channelId)!.status !== 'enabled'" variant="secondary" class="h-4 px-1 text-[10px]">
                      已停用
                    </Badge>
                    <span v-if="!form.models.length" class="text-muted-foreground">先填写匹配模型</span>
                    <span v-else-if="served(channelById.get(t.channelId)!).length" class="text-emerald-700 dark:text-emerald-400">
                      提供 {{ served(channelById.get(t.channelId)!).length }} 个匹配模型
                    </span>
                    <span v-else class="text-amber-700 dark:text-amber-400">不提供任何匹配的模型，将被跳过</span>
                  </p>
                  <p v-else class="text-xs text-amber-700 dark:text-amber-400">
                    渠道不存在或你无权管理（{{ t.channelId }}）
                  </p>
                </template>
                <p v-if="targetErrors(errors, i)['']" class="text-destructive text-xs" role="alert">
                  {{ targetErrors(errors, i)[''] }}
                </p>
              </div>
              <div class="space-y-1">
                <Label :for="`target-pri-${t.uid}`" class="text-muted-foreground text-xs md:sr-only">优先级覆盖</Label>
                <Input
                  :id="`target-pri-${t.uid}`"
                  v-model="t.priority"
                  inputmode="numeric"
                  :placeholder="channelById.get(t.channelId) ? `沿用渠道设置（${channelById.get(t.channelId)!.priority}）` : '沿用渠道设置'"
                  class="text-xs tabular-nums"
                  :aria-invalid="!!targetErrors(errors, i).priority"
                />
                <p v-if="targetErrors(errors, i).priority" class="text-destructive text-xs" role="alert">
                  {{ targetErrors(errors, i).priority }}
                </p>
              </div>
              <div class="space-y-1">
                <Label :for="`target-weight-${t.uid}`" class="text-muted-foreground text-xs md:sr-only">权重覆盖</Label>
                <Input
                  :id="`target-weight-${t.uid}`"
                  v-model="t.weight"
                  inputmode="numeric"
                  :placeholder="channelById.get(t.channelId) ? `沿用渠道设置（${channelById.get(t.channelId)!.weight}）` : '沿用渠道设置'"
                  class="text-xs tabular-nums"
                  :aria-invalid="!!targetErrors(errors, i).weight"
                />
                <p v-if="targetErrors(errors, i).weight" class="text-destructive text-xs" role="alert">
                  {{ targetErrors(errors, i).weight }}
                </p>
              </div>
              <Button type="button" variant="ghost" size="icon" class="justify-self-end" :aria-label="`移除第 ${i + 1} 个目标渠道`" @click="removeTarget(i)">
                <Trash2 />
              </Button>
            </div>
            <p class="text-muted-foreground text-xs">
              覆盖值留空表示沿用渠道设置（权重 1–1000；优先级为整数，越大越优先）。{{ overrideHint }}
            </p>
          </div>
          <p v-if="!channels.length" class="text-muted-foreground text-xs">
            没有你可以管理的渠道。
          </p>
          <Button type="button" variant="outline" size="sm" :disabled="form.targets.length >= MAX_TARGETS || !channels.length" @click="addTarget()">
            <Plus />
            添加目标渠道
          </Button>
        </section>

        <Separator />

        <!-- 重试与回退 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            重试与回退
          </h3>
          <div class="grid gap-4 lg:grid-cols-2">
            <div class="space-y-4">
              <FormField label="最多尝试次数" for="route-attempts" required :error="errors['retry.maxAttempts']" hint="1–5，含首次请求。每次失败后换下一个候选渠道重试。">
                <Input id="route-attempts" v-model="form.maxAttempts" type="number" min="1" max="5" step="1" class="w-28 tabular-nums" :aria-invalid="!!errors['retry.maxAttempts']" />
              </FormField>
              <fieldset class="space-y-2">
                <legend class="text-sm font-medium">
                  可以换渠道重试的失败
                </legend>
                <RetryOnPicker v-model="form.retryOn" id-prefix="route-retry" />
                <p v-if="retryError" class="text-destructive text-xs" role="alert">
                  {{ retryError }}
                </p>
                <p v-else class="text-muted-foreground text-xs">
                  未勾选的失败类型直接返回给客户端，不再尝试其他渠道。全部不勾选 = 不重试。
                </p>
              </fieldset>
            </div>

            <div class="space-y-2">
              <Label for="route-fallback">回退模型 <span class="text-muted-foreground font-normal">（按顺序，最多 {{ MAX_FALLBACKS }} 个）</span></Label>
              <p class="text-muted-foreground text-xs">
                请求模型的所有候选渠道都失败后，依次改用这些模型（同样经过 API Key 策略与路由规则）。回退只发生在流式响应开始之前；
                <strong class="text-foreground">按实际提供服务的模型计价</strong>并计入套餐配额，请求日志会标注「回退 → 模型」。
              </p>
              <ol v-if="form.fallbackModels.length" class="space-y-1.5">
                <li v-for="(m, i) in form.fallbackModels" :key="m" class="flex items-center gap-2 rounded-md border px-2 py-1">
                  <span class="text-muted-foreground w-4 text-xs tabular-nums">{{ i + 1 }}</span>
                  <span class="min-w-0 flex-1 truncate font-mono text-xs">{{ m }}</span>
                  <Button type="button" variant="ghost" size="icon-sm" :disabled="i === 0" :aria-label="`上移 ${m}`" @click="moveFallback(i, -1)">
                    <ArrowUp />
                  </Button>
                  <Button type="button" variant="ghost" size="icon-sm" :disabled="i === form.fallbackModels.length - 1" :aria-label="`下移 ${m}`" @click="moveFallback(i, 1)">
                    <ArrowDown />
                  </Button>
                  <Button type="button" variant="ghost" size="icon-sm" :aria-label="`移除 ${m}`" @click="form.fallbackModels.splice(i, 1)">
                    <Trash2 />
                  </Button>
                </li>
              </ol>
              <div class="flex gap-2">
                <SuggestInput
                  id="route-fallback"
                  v-model="fallbackDraft"
                  :options="fallbackSuggestions"
                  :exclude="form.fallbackModels"
                  placeholder="模型名，回车添加"
                  mono
                  class="flex-1"
                  :disabled="form.fallbackModels.length >= MAX_FALLBACKS"
                  :invalid="!!fallbackError"
                  @select="addFallback"
                  @enter="(e) => { e.preventDefault(); addFallback() }"
                />
                <Button type="button" variant="outline" :disabled="!fallbackDraft.trim() || form.fallbackModels.length >= MAX_FALLBACKS" @click="addFallback">
                  <Plus />
                  添加
                </Button>
              </div>
              <p v-if="fallbackError" class="text-destructive text-xs" role="alert">
                {{ fallbackError }}
              </p>
              <p v-else-if="fallbackMatchesRule.length" class="text-xs text-amber-700 dark:text-amber-400">
                {{ fallbackMatchesRule.join('、') }} 也匹配本规则，回退时会再次使用本规则的渠道设置。
              </p>
            </div>
          </div>
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
        <Button type="submit" form="route-form" :disabled="saving || conflict">
          <Loader2 v-if="saving" class="animate-spin" />
          {{ isEdit ? '保存' : '创建' }}
        </Button>
      </SheetFooter>
    </SheetContent>
  </Sheet>
</template>
