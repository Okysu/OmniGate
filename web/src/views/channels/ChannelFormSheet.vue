<script setup lang="ts">
import type { ChannelFormState } from '@/lib/channelForm'
import type { PluginFormState } from './pluginForm'
import type { Channel, ChannelScope, ChannelType, MaxTokensField, PluginManifest } from '@/lib/types'
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { CircleAlert, Download, KeyRound, Loader2, Plus, RefreshCw, Trash2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import SharePicker from '@/components/SharePicker.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { balanceThresholdError, buildChannelPayload, emptyChannelForm, formFromChannel, hasBalanceOutput, normalizeSharedWith } from '@/lib/channelForm'
import { channelsApi } from '@/lib/endpoints'
import { CHANNEL_BASE_URL_HINTS, CHANNEL_TYPE_LABELS, manifestChannelType, SCOPE_DESCRIPTIONS, SCOPE_LABELS } from '@/lib/labels'
import { addedGroups, GROUP_SHARE_ADMIN_ONLY } from '@/lib/shares'
import { isFullChannel } from '@/lib/types'
import { isCustomProtocol } from '@/lib/pluginProtocol'
import DiscoverModelsDialog from './DiscoverModelsDialog.vue'
import { emptyPluginForm, pluginPayload, validatePluginForm } from './pluginForm'
import PluginSection from './PluginSection.vue'

const props = defineProps<{
  /** Channel being edited; null = create. */
  channel: Channel | null
  canManage: boolean
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ saved: [channel: Channel, created: boolean] }>()

const form = reactive<ChannelFormState>(emptyChannelForm())
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const conflict = ref(false)
const saving = ref(false)
const reloading = ref(false)
/** Latest server copy (refreshed after a conflict reload). */
const current = ref<Channel | null>(null)

const isEdit = computed(() => current.value !== null)

// ---------- plugin ----------
const pluginState = ref<PluginFormState>(emptyPluginForm())
/** Remounts the plugin section (fresh load) on every open / reload. */
const pluginKey = ref(0)
/** Values last prefilled from manifest.defaults (create), to avoid clobbering user edits. */
let prefilledBaseUrl = ''
let prefilledModels = ''

function reset(c: Channel | null) {
  current.value = c
  Object.assign(form, c ? formFromChannel(c) : emptyChannelForm())
  errors.value = {}
  formError.value = null
  conflict.value = false
  pluginState.value = emptyPluginForm()
  pluginKey.value++
  prefilledBaseUrl = ''
  prefilledModels = ''
}

const modelGridClass = computed(() => form.type === 'openai' ? 'grid-cols-[1fr_1fr_10rem_auto]' : 'grid-cols-[1fr_1fr_auto]')

function modelsKey(rows: ChannelFormState['models']): string {
  return JSON.stringify(rows.filter(m => m.model.trim() || m.upstreamModel.trim()).map(m => [m.model.trim(), m.upstreamModel.trim(), m.upstreamProtocol ?? 'chat']))
}

/** Create: lock the type to the plugin's protocol and prefill manifest.defaults. */
function onPluginManifest(m: PluginManifest) {
  if (isEdit.value)
    return
  const t = manifestChannelType(m)
  if (t)
    form.type = t
  const base = m.defaults.baseUrl ?? ''
  if (form.baseUrl.trim() === '' || form.baseUrl === prefilledBaseUrl) {
    form.baseUrl = base
    prefilledBaseUrl = base
  }
  const defaults = (m.defaults.models ?? []).map(d => ({ model: d.model, upstreamModel: d.upstreamModel || d.model }))
  const currentKey = modelsKey(form.models)
  if (currentKey === '[]' || currentKey === prefilledModels) {
    form.models = defaults.length ? defaults : [{ model: '', upstreamModel: '' }]
    prefilledModels = modelsKey(form.models)
  }
}

/**
 * Whether the selected plugin version has a capability whose output kind is
 * `balance` (e.g. DeepSeek's `balance.get`) — the backend raises upstream
 * balance alerts from those results (null = unknown / no plugin yet).
 */
const hasBalanceCapability = computed<boolean | null>(() => {
  const m = pluginState.value.manifest
  if (!m)
    return null
  return hasBalanceOutput(m.capabilities)
})

/** phase9 §2: the selected plugin version implements a custom upstream protocol. */
const customProtocol = computed(() => isCustomProtocol(pluginState.value.manifest))
const typeLockedBy = computed(() => (!isEdit.value && pluginState.value.manifest ? pluginState.value.manifest.name : null))

watch(open, (v) => {
  if (v)
    reset(props.channel)
})

const TYPES: ChannelType[] = ['openai', 'anthropic']
/** `custom` only comes from a custom-protocol plugin (the select is then locked). */
const typeOptions = computed<ChannelType[]>(() => (form.type === 'custom' ? [...TYPES, 'custom'] : TYPES))
const SCOPES: ChannelScope[] = ['private', 'shared', 'global']
const FORBIDDEN_HEADERS = new Set(['authorization', 'x-api-key', 'host', 'content-length', 'connection', 'transfer-encoding', 'cookie', 'te', 'upgrade', 'keep-alive', 'proxy-authorization', 'proxy-connection', 'content-type', 'accept-encoding'])

const baseUrlHint = computed(() => CHANNEL_BASE_URL_HINTS[form.type])

/** Groups the channel is shared with on the server (non-admins may keep or remove them). */
const currentGroups = computed(() => (current.value?.scope === 'shared' ? normalizeSharedWith(current.value.sharedWith).groups : []))

function validate(): Record<string, string> {
  const e: Record<string, string> = {}
  const name = form.name.trim()
  if (!name || name.length > 64)
    e.name = '长度应为 1–64 个字符'
  try {
    const u = new URL(form.baseUrl.trim())
    if (u.protocol !== 'http:' && u.protocol !== 'https:')
      e.baseUrl = '必须是 http(s) 绝对地址'
    else if (u.username || u.password || u.search || u.hash)
      e.baseUrl = '不能包含用户信息、查询参数或片段'
  }
  catch {
    e.baseUrl = '必须是 http(s) 绝对地址'
  }
  if ((!isEdit.value || form.replaceKey) && !form.apiKey.trim())
    e.apiKey = '请填写上游 API Key'
  else if (form.apiKey.includes('\n'))
    e.apiKey = 'API Key 不能包含换行'
  const rows = form.models.filter(m => m.model.trim() || m.upstreamModel.trim())
  if (rows.length === 0)
    e.models = '至少配置一个模型'
  else if (rows.some(m => !m.model.trim()))
    e.models = '逻辑模型名不能为空'
  else {
    const seen = new Set<string>()
    for (const m of rows) {
      const k = m.model.trim()
      if (seen.has(k)) {
        e.models = `模型「${k}」重复`
        break
      }
      seen.add(k)
    }
  }
  if (!Number.isInteger(form.priority) || form.priority < -1000 || form.priority > 1000)
    e.priority = '范围为 -1000 到 1000 的整数'
  if (!Number.isInteger(form.weight) || form.weight < 1 || form.weight > 1000)
    e.weight = '范围为 1 到 1000 的整数'
  if (!Number.isInteger(form.timeoutSeconds) || form.timeoutSeconds < 0 || form.timeoutSeconds > 600)
    e['config.timeoutSeconds'] = '范围为 0–600 秒（0 表示默认 60 秒）'
  for (const h of form.headers) {
    const k = h.key.trim()
    if (!k && !h.value)
      continue
    if (!/^[\w-]{1,64}$/.test(k) || FORBIDDEN_HEADERS.has(k.toLowerCase()) || /[\r\n]/.test(h.value)) {
      e['config.headers'] = `请求头「${k || '（空）'}」不允许或格式无效（认证头由 API Key 自动设置）`
      break
    }
  }
  const threshold = balanceThresholdError(form.balanceBelow)
  if (threshold)
    e['alerts.balanceBelow'] = threshold
  if (form.scope === 'global' && !props.canManage)
    e.scope = '只有渠道管理员可以发布全局渠道'
  // phase5-api.md §5.2: only channels.manage may add groups (existing ones may stay or go).
  if (form.scope === 'shared' && !props.canManage && addedGroups(currentGroups.value, form.sharedWith.groups).length)
    e['sharedWith.groups'] = GROUP_SHARE_ADMIN_ONLY
  Object.assign(e, validatePluginForm(pluginState.value, current.value?.secretFields))
  return e
}

const otherErrors = computed(() => {
  const known = new Set(['name', 'type', 'baseUrl', 'status', 'apiKey', 'models', 'priority', 'weight', 'config.headers', 'config.timeoutSeconds', 'config.maxTokensField', 'scope', 'sharedWith', 'sharedWith.users', 'sharedWith.groups', 'pluginVersionId', 'alerts', 'alerts.balanceBelow'])
  const schemaProps = pluginState.value.manifest?.configSchema?.properties ?? {}
  // Plugin config / secret errors render inline when the field is on screen.
  const inline = (k: string) => {
    const m = /^(pluginConfig|secrets)\.(.+)$/.exec(k)
    return m !== null && Object.prototype.hasOwnProperty.call(schemaProps, m[2]!)
  }
  return Object.entries(errors.value).filter(([k]) => !known.has(k) && !inline(k))
})

/** Brings the first inline error (or the conflict banner) into view. */
function revealFirstError() {
  void nextTick(() => {
    document.querySelector('#channel-form [role=alert]')?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  })
}

async function submit() {
  formError.value = null
  errors.value = validate()
  if (Object.keys(errors.value).length > 0) {
    formError.value = '请修正标记的字段后再保存'
    revealFirstError()
    return
  }
  if (pluginState.value.loading) {
    formError.value = '插件信息仍在加载，请稍候'
    return
  }
  if (!current.value && !pluginState.value.versionId) {
    formError.value = '请选择插件与版本'
    return
  }
  saving.value = true
  try {
    const body = { ...buildChannelPayload(form, current.value ?? undefined), ...pluginPayload(pluginState.value, !current.value) }
    const saved = current.value
      ? await channelsApi.update(current.value.id, body)
      : await channelsApi.create(body)
    toast.success(current.value ? '渠道已保存' : '渠道已创建')
    emit('saved', saved, !current.value)
    open.value = false
  }
  catch (err) {
    if (isVersionConflict(err)) {
      conflict.value = true
      formError.value = null
    }
    else if (isApiError(err) && (err.code === 'plugin_not_approved' || err.code === 'plugin_disabled')) {
      errors.value = { pluginVersionId: err.message }
      formError.value = err.message
    }
    else if (isApiError(err) && err.code === 'base_url_not_allowed') {
      errors.value = { baseUrl: err.message }
      formError.value = '渠道地址不被允许'
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = fieldErrors(err)
      formError.value = err.message
    }
    else {
      formError.value = errorMessage(err)
    }
    // After the state changes above so the error nodes exist when the tick flushes.
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
    const latest = await channelsApi.get(current.value.id)
    if (!isFullChannel(latest)) {
      formError.value = '你已无权管理该渠道'
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

function addModelRow() {
  form.models.push({ model: '', upstreamModel: '' })
}
function removeModelRow(i: number) {
  form.models.splice(i, 1)
}
function addHeaderRow() {
  form.headers.push({ key: '', value: '' })
}

const discoverOpen = ref(false)
function onDiscoverAdd(rows: ChannelFormState['models']) {
  form.models = rows
  toast.success('已添加到模型映射，保存后生效')
}

function setMaxTokensField(v: unknown) {
  if (v === 'max_tokens' || v === 'max_completion_tokens')
    form.maxTokensField = v as MaxTokensField
}
function setType(v: unknown) {
  if (v === 'openai' || v === 'anthropic')
    form.type = v
}
function setScope(v: unknown) {
  if (v === 'private' || v === 'shared' || v === 'global')
    form.scope = v
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-6xl" @interact-outside="(e: Event) => saving && e.preventDefault()">
      <SheetHeader class="border-b">
        <SheetTitle>{{ isEdit ? `编辑渠道：${current?.name}` : '新建渠道' }}</SheetTitle>
        <SheetDescription>
          渠道是一个上游服务端点及其凭据。网关按模型映射、优先级和权重在可用渠道间路由请求。
        </SheetDescription>
      </SheetHeader>

      <form id="channel-form" class="flex-1 space-y-6 overflow-y-auto p-4" novalidate @submit.prevent="submit">
        <div v-if="conflict" class="border-destructive/40 bg-destructive/5 space-y-2 rounded-lg border p-3 text-sm" role="alert">
          <p class="text-destructive flex items-center gap-1.5 font-medium">
            <CircleAlert class="size-4" />
            该渠道已被他人修改
          </p>
          <p class="text-muted-foreground">
            你的修改尚未保存。加载最新数据会丢弃当前表单中的修改。
          </p>
          <Button type="button" variant="outline" size="sm" :disabled="reloading" @click="reloadLatest">
            <RefreshCw :class="reloading ? 'animate-spin' : ''" />
            加载最新数据
          </Button>
        </div>

        <PluginSection
          :key="pluginKey"
          v-model:state="pluginState"
          :channel="current"
          :errors="errors"
          @manifest="onPluginManifest"
        />

        <Separator />

        <!-- 基本信息 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            基本信息
          </h3>
          <FormField label="名称" for="ch-name" required :error="errors.name">
            <Input id="ch-name" v-model="form.name" maxlength="64" placeholder="例如：OpenAI 主账号" :aria-invalid="!!errors.name" />
          </FormField>
          <div class="grid gap-4 sm:grid-cols-2">
            <FormField label="类型" for="ch-type" required :error="errors.type" :hint="isEdit ? '类型创建后不能修改' : typeLockedBy ? (customProtocol ? `由插件「${typeLockedBy}」决定（自定义协议）` : `由插件「${typeLockedBy}」的继承协议决定`) : undefined">
              <Select :model-value="form.type" :disabled="isEdit || !!typeLockedBy" @update:model-value="setType">
                <SelectTrigger id="ch-type" class="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="t in typeOptions" :key="t" :value="t">
                    {{ CHANNEL_TYPE_LABELS[t] }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </FormField>
            <FormField label="启用" for="ch-status">
              <div class="flex h-9 items-center gap-2">
                <Switch id="ch-status" :model-value="form.status === 'enabled'" @update:model-value="(v: boolean) => (form.status = v ? 'enabled' : 'disabled')" />
                <span class="text-muted-foreground text-sm">{{ form.status === 'enabled' ? '已启用' : '已停用（不参与路由）' }}</span>
              </div>
            </FormField>
          </div>
          <FormField label="Base URL" for="ch-base" required :error="errors.baseUrl" :hint="baseUrlHint.hint">
            <Input id="ch-base" v-model="form.baseUrl" type="url" :placeholder="baseUrlHint.example" class="font-mono text-xs" :aria-invalid="!!errors.baseUrl" />
          </FormField>
        </section>

        <Separator />

        <!-- 凭据 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            凭据
          </h3>
          <div v-if="isEdit && current?.secret.set" class="flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3">
            <div class="flex items-center gap-2 text-sm">
              <KeyRound class="text-muted-foreground size-4" />
              <span>已设置</span>
              <span class="font-mono text-xs">{{ current.secret.hint ?? '' }}</span>
            </div>
            <label class="flex items-center gap-2 text-sm">
              <Switch :model-value="form.replaceKey" @update:model-value="(v: boolean) => { form.replaceKey = v; if (!v) form.apiKey = '' }" />
              更换 API Key
            </label>
          </div>
          <FormField
            v-if="!isEdit || form.replaceKey"
            :label="isEdit ? '新的 API Key' : 'API Key'"
            for="ch-key"
            required
            :error="errors.apiKey"
            hint="加密存储，保存后不再显示明文。"
          >
            <Input id="ch-key" v-model="form.apiKey" type="password" autocomplete="new-password" placeholder="sk-…" class="font-mono text-xs" :aria-invalid="!!errors.apiKey" />
          </FormField>
        </section>

        <Separator />

        <!-- 模型映射 -->
        <section class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-semibold">
              模型映射
            </h3>
            <Button
              v-if="isEdit"
              type="button"
              variant="outline"
              size="sm"
              title="使用已保存的 Base URL 与 API Key 请求上游模型列表"
              @click="discoverOpen = true"
            >
              <Download />
              从上游获取模型
            </Button>
          </div>
          <p class="text-muted-foreground text-xs">
            左侧为对外的逻辑模型名（客户端请求中的 model），右侧为发往上游的模型名；右侧留空表示与逻辑名相同。
            <template v-if="form.type === 'openai'">
              「上游协议」选择该模型在上游使用的 OpenAI 接口：只支持 Responses 的模型（如部分 GPT 新模型）选 Responses，网关会在 Chat / Anthropic / Responses 之间自动转换。
            </template>
            <template v-if="!isEdit">
              保存渠道后可从上游自动获取模型列表。
            </template>
          </p>
          <div class="space-y-2">
            <div class="text-muted-foreground hidden gap-2 text-xs sm:grid" :class="modelGridClass">
              <span>逻辑模型</span><span>上游模型</span><span v-if="form.type === 'openai'">上游协议</span><span class="w-8" />
            </div>
            <div v-for="(m, i) in form.models" :key="i" class="grid gap-2" :class="modelGridClass">
              <Input v-model="m.model" placeholder="gpt-4o" class="font-mono text-xs" :aria-label="`第 ${i + 1} 行逻辑模型`" />
              <Input v-model="m.upstreamModel" :placeholder="m.model || '同逻辑名'" class="font-mono text-xs" :aria-label="`第 ${i + 1} 行上游模型`" />
              <Select v-if="form.type === 'openai'" :model-value="m.upstreamProtocol ?? 'chat'" @update:model-value="(v) => { m.upstreamProtocol = v === 'responses' ? 'responses' : undefined }">
                <SelectTrigger class="w-full text-xs" :aria-label="`第 ${i + 1} 行上游协议`">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="chat">
                    Chat Completions
                  </SelectItem>
                  <SelectItem value="responses">
                    Responses
                  </SelectItem>
                </SelectContent>
              </Select>
              <Button type="button" variant="ghost" size="icon" :aria-label="`删除第 ${i + 1} 行`" @click="removeModelRow(i)">
                <Trash2 />
              </Button>
            </div>
          </div>
          <p v-if="errors.models" class="text-destructive text-xs" role="alert">
            {{ errors.models }}
          </p>
          <Button type="button" variant="ghost" size="sm" @click="addModelRow">
            <Plus />
            添加一行
          </Button>
        </section>

        <Separator />

        <!-- 路由 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            路由
          </h3>
          <div class="grid gap-4 sm:grid-cols-2">
            <FormField label="优先级" for="ch-priority" :error="errors.priority" hint="越大越优先；失败时回退到下一优先级。">
              <Input id="ch-priority" v-model.number="form.priority" type="number" min="-1000" max="1000" step="1" />
            </FormField>
            <FormField label="权重" for="ch-weight" :error="errors.weight" hint="同优先级内按权重加权随机（1–1000）。">
              <Input id="ch-weight" v-model.number="form.weight" type="number" min="1" max="1000" step="1" />
            </FormField>
          </div>
        </section>

        <Separator />

        <!-- 告警 -->
        <section class="space-y-4" data-testid="channel-alerts">
          <h3 class="text-sm font-semibold">
            告警
          </h3>
          <div class="grid gap-4 sm:grid-cols-2">
            <FormField label="上游余额告警阈值" for="ch-balance-below" :error="errors['alerts.balanceBelow'] ?? errors.alerts">
              <Input id="ch-balance-below" v-model="form.balanceBelow" inputmode="decimal" placeholder="不告警" class="font-mono tabular-nums" :aria-invalid="!!(errors['alerts.balanceBelow'] ?? errors.alerts)" data-testid="channel-balance-below" />
              <template #hint>
                渠道插件 balance 能力返回的余额低于此值时，通知渠道所有者（平台渠道另通知渠道管理员）。按插件返回的币种比较，币种不同时不比较；留空表示不告警。
                <span v-if="hasBalanceCapability === false" class="text-amber-700 dark:text-amber-400">当前插件没有 balance 能力，此阈值不会生效。</span>
              </template>
            </FormField>
          </div>
        </section>

        <Separator />

        <!-- 高级 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            高级
          </h3>
          <div class="space-y-2">
            <Label>自定义请求头</Label>
            <div v-for="(h, i) in form.headers" :key="i" class="grid grid-cols-[1fr_1fr_auto] gap-2">
              <Input v-model="h.key" placeholder="X-Header-Name" class="font-mono text-xs" :aria-label="`第 ${i + 1} 个请求头名称`" />
              <Input v-model="h.value" placeholder="值" class="font-mono text-xs" :aria-label="`第 ${i + 1} 个请求头值`" />
              <Button type="button" variant="ghost" size="icon" :aria-label="`删除第 ${i + 1} 个请求头`" @click="form.headers.splice(i, 1)">
                <Trash2 />
              </Button>
            </div>
            <p v-if="errors['config.headers']" class="text-destructive text-xs" role="alert">
              {{ errors['config.headers'] }}
            </p>
            <p v-else class="text-muted-foreground text-xs">
              最多 20 个；不能包含 Authorization、x-api-key 等认证与连接相关的头。
            </p>
            <Button type="button" variant="ghost" size="sm" @click="addHeaderRow">
              <Plus />
              添加请求头
            </Button>
          </div>
          <div v-if="form.type === 'openai'" class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="ch-responses">支持 Responses API</Label>
              <p class="text-muted-foreground text-xs">
                开启后，客户端的 /v1/responses 请求可以直通到此渠道（上游需支持 OpenAI Responses 接口）。
              </p>
            </div>
            <Switch id="ch-responses" v-model="form.supportsResponses" />
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <FormField label="超时（秒）" for="ch-timeout" :error="errors['config.timeoutSeconds']" hint="等待上游响应头的超时；0 表示默认 60 秒，最大 600。">
              <Input id="ch-timeout" v-model.number="form.timeoutSeconds" type="number" min="0" max="600" step="1" />
            </FormField>
            <FormField
              v-if="form.type === 'openai'"
              label="输出上限字段"
              for="ch-maxtokens"
              :error="errors['config.maxTokensField']"
              hint="跨协议转换到此渠道时，输出 token 上限使用的字段名。"
            >
              <Select :model-value="form.maxTokensField" @update:model-value="setMaxTokensField">
                <SelectTrigger id="ch-maxtokens" class="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="max_tokens">
                    max_tokens（默认）
                  </SelectItem>
                  <SelectItem value="max_completion_tokens">
                    max_completion_tokens
                  </SelectItem>
                </SelectContent>
              </Select>
            </FormField>
          </div>
        </section>

        <Separator />

        <!-- 共享 -->
        <section class="space-y-4">
          <h3 class="text-sm font-semibold">
            共享
          </h3>
          <FormField label="范围" for="ch-scope" :error="errors.scope" :hint="SCOPE_DESCRIPTIONS[form.scope]">
            <Select :model-value="form.scope" @update:model-value="setScope">
              <SelectTrigger id="ch-scope" class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="s in SCOPES" :key="s" :value="s" :disabled="s === 'global' && !canManage && current?.scope !== 'global'">
                  {{ SCOPE_LABELS[s] }}{{ s === 'global' && !canManage ? '（需要渠道管理权限）' : '' }}
                </SelectItem>
              </SelectContent>
            </Select>
          </FormField>
          <FormField v-if="form.scope === 'shared'" label="共享给" for="ch-share" :error="errors.sharedWith ?? errors['sharedWith.users'] ?? errors['sharedWith.groups']" :hint="canManage ? '被邀请的用户需要在「渠道 → 共享给我的」中接受后才能使用此渠道；用户组的成员无需接受。他们用自己的 API Key 调用，看不到 Base URL 与凭据。' : '被邀请的用户需要在「渠道 → 共享给我的」中接受后才能使用此渠道（对他们不计费，上游费用由你承担）。他们看不到 Base URL 与凭据。'">
            <SharePicker id="ch-share" v-model="form.sharedWith" :can-share-groups="canManage" :shares="current ? current.shares : []" />
          </FormField>
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
        <Button type="submit" form="channel-form" :disabled="saving || conflict">
          <Loader2 v-if="saving" class="animate-spin" />
          {{ isEdit ? '保存' : '创建' }}
        </Button>
      </SheetFooter>
    </SheetContent>
  </Sheet>

  <DiscoverModelsDialog
    v-if="current"
    v-model:open="discoverOpen"
    :channel-id="current.id"
    :models="form.models"
    @add="onDiscoverAdd"
  />
</template>
