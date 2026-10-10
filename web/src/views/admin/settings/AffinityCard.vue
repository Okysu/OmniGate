<script setup lang="ts">
import type { AffinityConfig, AffinityMode, AffinityRule, AffinityStats, SettingSource, SettingsResponse } from '@/lib/types'
import { computed, onMounted, ref, watch } from 'vue'
import {
  ArrowDown,
  ArrowUp,
  Braces,
  Copy,
  Eraser,
  LayoutList,
  Link2,
  Link2Off,
  Loader2,
  Lock,
  MoreHorizontal,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  Sparkles,
  Trash2,
  Undo2,
  Waypoints,
} from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import {
  configEquals,
  defaultConfig,
  effectiveMode,
  errorLines,
  keySourceLabel,
  LIMITS,
  mergeRules,
  MODE_DESCRIPTIONS,
  MODE_LABELS,
  modeIsOwn,
  MODES,
  moveRule,
  normalizeConfig,
  omnigatePresets,
  parseConfigJson,
  passHeaders,
  scopeBadges,
  stringifyConfig,
  ttlLabel,
  uniqueName,
  validateConfig,
} from '@/lib/affinity'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { affinityApi, settingsApi } from '@/lib/endpoints'
import { formatNumber } from '@/lib/format'
import { SOURCE_LABELS } from '@/lib/settingsForm'
import AffinityRuleSheet from './AffinityRuleSheet.vue'

/**
 * 「会话亲和」 (gateway.affinity, phase12-api.md): rules table / JSON editor, global
 * options and the binding cache. Saved on its own with the settings version.
 */
const props = defineProps<{
  data: SettingsResponse
  /** Another settings save is running. */
  busy: boolean
  conflict: boolean
}>()
const emit = defineEmits<{
  saved: [res: SettingsResponse]
  conflict: []
  dirty: [dirty: boolean]
}>()

const supported = computed(() => !!props.data.settings.gateway.affinity)
const saved = computed<AffinityConfig>(() => normalizeConfig(props.data.settings.gateway.affinity ?? defaultConfig()))
const source = computed(() => (props.data.sources['gateway.affinity'] ?? null) as SettingSource | null)

const config = ref<AffinityConfig>(structuredClone(saved.value))
const view = ref<'visual' | 'json'>('visual')
const jsonText = ref('')
const errorMsg = ref<string | null>(null)
const saving = ref(false)

function reset() {
  config.value = structuredClone(saved.value)
  jsonText.value = stringifyConfig(config.value)
  errorMsg.value = null
}
watch(saved, reset)

// ---------- JSON view ----------
const parsed = computed(() => (view.value === 'json' ? parseConfigJson(jsonText.value) : { config: config.value, error: null }))
/** The config being edited (null: the JSON text does not parse). */
const current = computed(() => parsed.value.config)
const validation = computed(() => (current.value ? validateConfig(current.value) : {}))
const jsonProblems = computed(() => (parsed.value.error ? [parsed.value.error] : errorLines(validation.value)))

function setView(v: unknown) {
  if (v === 'json' && view.value === 'visual') {
    jsonText.value = stringifyConfig(config.value)
    view.value = 'json'
  }
  else if (v === 'visual' && view.value === 'json') {
    const p = parseConfigJson(jsonText.value)
    if (!p.config) {
      toast.error('JSON 无法解析，请先修正', { description: p.error ?? undefined })
      return
    }
    config.value = p.config
    view.value = 'visual'
  }
}

const dirty = computed(() => !current.value || !configEquals(current.value, saved.value))
watch(dirty, v => emit('dirty', v), { immediate: true })

// ---------- rules ----------
const rules = computed(() => config.value.rules)
function ruleMode(r: AffinityRule): AffinityMode {
  return effectiveMode(r, config.value.session_mode)
}
const MODE_ICONS = { off: Link2Off, prefer: Link2, strict: Lock } as const
const MODE_CLASSES: Record<AffinityMode, string> = {
  off: 'text-muted-foreground',
  prefer: 'text-sky-700 dark:text-sky-400',
  strict: 'text-amber-700 dark:text-amber-400',
}

const sheetOpen = ref(false)
const editIndex = ref<number | null>(null)
const editing = computed(() => (editIndex.value === null ? null : rules.value[editIndex.value] ?? null))
const otherNames = computed(() => rules.value.filter((_, i) => i !== editIndex.value).map(r => r.name))

function openAdd() {
  if (rules.value.length >= LIMITS.rules) {
    toast.error(`最多 ${LIMITS.rules} 条规则`)
    return
  }
  editIndex.value = null
  sheetOpen.value = true
}
function openEdit(i: number) {
  editIndex.value = i
  sheetOpen.value = true
}
function onRuleSave(r: AffinityRule) {
  const list = [...config.value.rules]
  if (editIndex.value === null)
    list.push(r)
  else
    list[editIndex.value] = r
  config.value = { ...config.value, rules: list }
}
function duplicate(i: number) {
  const r = rules.value[i]
  if (!r || rules.value.length >= LIMITS.rules)
    return
  const copy = { ...structuredClone(r), name: uniqueName(r.name, rules.value) }
  const list = [...config.value.rules]
  list.splice(i + 1, 0, copy)
  config.value = { ...config.value, rules: list }
}
function move(i: number, d: number) {
  config.value = { ...config.value, rules: moveRule(config.value.rules, i, i + d) }
}
const deleteIndex = ref<number | null>(null)
const deleteOpen = ref(false)
function requestDelete(i: number) {
  deleteIndex.value = i
  deleteOpen.value = true
}
function applyDelete() {
  if (deleteIndex.value !== null)
    config.value = { ...config.value, rules: config.value.rules.filter((_, i) => i !== deleteIndex.value) }
  deleteOpen.value = false
}

function fillTemplate() {
  const base = view.value === 'json' ? parseConfigJson(jsonText.value).config : config.value
  if (!base) {
    toast.error('JSON 无法解析，请先修正')
    return
  }
  const { rules: next, replaced, added } = mergeRules(base.rules, omnigatePresets())
  const merged = { ...base, rules: next }
  config.value = merged
  if (view.value === 'json')
    jsonText.value = stringifyConfig(merged)
  toast.success('已填充内置预设', { description: `新增 ${added} 条${replaced ? `，替换同名 ${replaced} 条` : ''}；保存后生效。` })
}

// ---------- save ----------
async function save() {
  const cfg = current.value
  errorMsg.value = null
  if (!cfg) {
    errorMsg.value = parsed.value.error
    return
  }
  const errs = validateConfig(cfg)
  if (Object.keys(errs).length) {
    errorMsg.value = view.value === 'json' ? '请修正 JSON 中标记的错误后再保存' : errorLines(errs).slice(0, 3).join('；')
    return
  }
  saving.value = true
  try {
    const res = await settingsApi.update({ version: props.data.version, settings: { gateway: { affinity: cfg } } })
    emit('saved', res)
    toast.success('「会话亲和」设置已保存', { description: '新请求立即按新规则匹配；已有的会话绑定保留。' })
    void loadStats()
  }
  catch (err) {
    handleError(err)
  }
  finally {
    saving.value = false
  }
}

async function resetDefault() {
  saving.value = true
  errorMsg.value = null
  try {
    const res = await settingsApi.update({ version: props.data.version, settings: { gateway: { affinity: null } } })
    emit('saved', res)
    toast.success('「会话亲和」已恢复为默认值（OmniGate 预设）')
  }
  catch (err) {
    handleError(err)
  }
  finally {
    saving.value = false
  }
}

function handleError(err: unknown) {
  if (isVersionConflict(err)) {
    emit('conflict')
    errorMsg.value = '设置已被其他人修改，请重新加载后再保存'
  }
  else if (isApiError(err) && err.status === 422) {
    errorMsg.value = fieldErrors(err)['gateway.affinity'] ?? err.message
  }
  else {
    errorMsg.value = errorMessage(err)
  }
}

// ---------- binding cache ----------
const stats = ref<AffinityStats | null>(null)
const statsLoading = ref(false)
async function loadStats() {
  statsLoading.value = true
  try {
    stats.value = await affinityApi.stats()
  }
  catch {
    stats.value = null
  }
  finally {
    statsLoading.value = false
  }
}
onMounted(() => {
  reset()
  if (supported.value)
    void loadStats()
})
const clearOpen = ref(false)
const clearing = ref(false)
async function clear(rule?: string) {
  clearing.value = true
  try {
    const res = await affinityApi.clear(rule)
    stats.value = res.stats
    toast.success(rule ? `已清空「${rule}」的 ${formatNumber(res.cleared)} 条会话绑定` : `已清空全部 ${formatNumber(res.cleared)} 条会话绑定`)
    clearOpen.value = false
  }
  catch (err) {
    toast.error('清空失败', { description: errorMessage(err) })
  }
  finally {
    clearing.value = false
  }
}

const disabled = computed(() => props.busy || saving.value)
const numberModel = (key: 'max_entries' | 'default_ttl_seconds') => computed({
  get: () => String(config.value[key]),
  set: (v: string | number) => (config.value = { ...config.value, [key]: String(v).trim() === '' ? Number.NaN : Number(v) }),
})
const maxEntries = numberModel('max_entries')
const defaultTtl = numberModel('default_ttl_seconds')
function setGlobalMode(v: unknown) {
  if (v === 'off' || v === 'prefer' || v === 'strict')
    config.value = { ...config.value, session_mode: v }
}
</script>

<template>
  <Card data-section="affinity">
    <CardHeader class="gap-3">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0 space-y-1.5">
          <CardTitle class="flex flex-wrap items-center gap-2 text-base">
            <Waypoints class="size-4" />
            会话亲和
            <Badge v-if="source" variant="outline" class="h-4 px-1.5 text-[10px] font-normal" :class="source === 'db' ? 'border-sky-500/40 text-sky-700 dark:text-sky-400' : 'text-muted-foreground'">
              {{ SOURCE_LABELS[source] }}
            </Badge>
            <Badge v-if="dirty" variant="outline" class="border-amber-500/50 text-amber-700 dark:text-amber-400">
              未保存
            </Badge>
          </CardTitle>
          <CardDescription>
            按客户端自己的会话标识（Codex CLI 的 prompt_cache_key / Session_id、Claude Code 的 metadata.user_id 等）把同一会话固定到同一渠道，并透传会话请求头，提升上游提示词缓存与号池的命中率。规则格式与 new-api 的「渠道亲和」相同，可直接粘贴其 JSON。
          </CardDescription>
        </div>
        <div v-if="supported" class="flex flex-wrap items-center gap-2">
          <Tabs :model-value="view" @update:model-value="setView">
            <TabsList>
              <TabsTrigger value="visual" data-testid="affinity-view-visual">
                <LayoutList />
                可视化
              </TabsTrigger>
              <TabsTrigger value="json" data-testid="affinity-view-json">
                <Braces />
                JSON
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <Button variant="outline" size="sm" :disabled="disabled" title="加入内置的 Codex CLI / Claude Code 预设（任意模型、优先保持）；同名规则会被替换，其他规则保留" data-testid="affinity-template" @click="fillTemplate">
            <Sparkles />
            填充模板
          </Button>
          <Button v-if="view === 'visual'" size="sm" :disabled="disabled" data-testid="affinity-add" @click="openAdd">
            <Plus />
            添加规则
          </Button>
        </div>
      </div>
    </CardHeader>

    <CardContent v-if="!supported">
      <p class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
        当前服务端版本未返回会话亲和设置（gateway.affinity），升级服务端后可在此配置。
      </p>
    </CardContent>

    <template v-else>
      <CardContent class="space-y-5">
        <!-- 全局选项 -->
        <div v-if="view === 'visual'" class="grid gap-3 md:grid-cols-2 xl:grid-cols-3" data-testid="affinity-globals">
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="aff-enabled">启用会话亲和</Label>
              <p class="text-muted-foreground text-xs">
                关闭后不匹配任何规则：不固定渠道，也不透传请求头。
              </p>
            </div>
            <Switch id="aff-enabled" :model-value="config.enabled" data-testid="affinity-enabled" @update:model-value="(v: boolean) => (config = { ...config, enabled: v })" />
          </div>
          <div class="space-y-1.5 rounded-lg border p-3">
            <Label for="aff-global-mode">默认会话保持</Label>
            <Select :model-value="config.session_mode" @update:model-value="setGlobalMode">
              <SelectTrigger id="aff-global-mode" class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="m in MODES" :key="m" :value="m">
                  {{ MODE_LABELS[m] }}
                </SelectItem>
              </SelectContent>
            </Select>
            <p class="text-muted-foreground text-xs">
              「继承全局」的规则使用此模式。{{ MODE_DESCRIPTIONS[config.session_mode] }}
            </p>
          </div>
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="aff-switch">成功后切换绑定</Label>
              <p class="text-muted-foreground text-xs">
                会话由其他渠道成功处理时，把绑定改到该渠道（switch_on_success）。
              </p>
            </div>
            <Switch id="aff-switch" :model-value="config.switch_on_success" @update:model-value="(v: boolean) => (config = { ...config, switch_on_success: v })" />
          </div>
          <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
            <div class="space-y-1">
              <Label for="aff-keep-disabled">渠道不可用时保留绑定</Label>
              <p class="text-muted-foreground text-xs">
                绑定的渠道被停用、熔断或不再可用时保留绑定，恢复后会话回到原渠道（keep_on_channel_disabled）；关闭则丢弃绑定。
              </p>
            </div>
            <Switch id="aff-keep-disabled" :model-value="config.keep_on_channel_disabled" @update:model-value="(v: boolean) => (config = { ...config, keep_on_channel_disabled: v })" />
          </div>
          <div class="space-y-1.5 rounded-lg border p-3">
            <Label for="aff-ttl-default">默认 TTL（秒）</Label>
            <Input id="aff-ttl-default" v-model="defaultTtl" type="number" min="1" :max="LIMITS.ttl" step="1" class="tabular-nums" :aria-invalid="!!validation.default_ttl_seconds" />
            <p class="text-xs" :class="validation.default_ttl_seconds ? 'text-destructive' : 'text-muted-foreground'">
              {{ validation.default_ttl_seconds ?? '绑定闲置多久后失效（每次命中续期），规则未单独设置 TTL 时使用。' }}
            </p>
          </div>
          <div class="space-y-1.5 rounded-lg border p-3">
            <Label for="aff-max">最大缓存条目</Label>
            <Input id="aff-max" v-model="maxEntries" type="number" min="1" :max="LIMITS.maxEntries" step="1" class="tabular-nums" :aria-invalid="!!validation.max_entries" />
            <p class="text-xs" :class="validation.max_entries ? 'text-destructive' : 'text-muted-foreground'">
              {{ validation.max_entries ?? '内存中的会话绑定上限，超出时淘汰最久未使用的绑定（单实例，重启后清空）。' }}
            </p>
          </div>
        </div>

        <!-- 规则表 -->
        <div v-if="view === 'visual'" class="overflow-x-auto rounded-lg border" data-testid="affinity-rules">
          <Table class="text-xs">
            <TableHeader>
              <TableRow>
                <TableHead class="w-8">
                  #
                </TableHead>
                <TableHead>名称</TableHead>
                <TableHead>Key 来源</TableHead>
                <TableHead>会话保持</TableHead>
                <TableHead>TTL</TableHead>
                <TableHead>作用域</TableHead>
                <TableHead class="text-right">
                  缓存
                </TableHead>
                <TableHead class="w-20">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-if="!rules.length">
                <TableCell colspan="8" class="text-muted-foreground py-6 text-center">
                  还没有规则。点击「添加规则」，或用「填充模板」加入内置的 Codex CLI / Claude Code 预设。
                </TableCell>
              </TableRow>
              <TableRow v-for="(r, i) in rules" :key="`${i}:${r.name}`" :data-affinity-rule="r.name" :class="validation[`rules[${i}].name`] || Object.keys(validation).some(k => k.startsWith(`rules[${i}].`)) ? 'bg-destructive/5' : ''">
                <TableCell class="text-muted-foreground tabular-nums">
                  {{ i + 1 }}
                </TableCell>
                <TableCell class="max-w-56 min-w-36 whitespace-normal">
                  <button type="button" class="hover:text-primary block max-w-full truncate text-left text-sm font-medium hover:underline" :title="r.name" @click="openEdit(i)">
                    {{ r.name }}
                  </button>
                  <p class="text-muted-foreground truncate font-mono text-[11px]" :title="r.model_regex.join('  ')">
                    {{ r.model_regex.length ? r.model_regex.join(' · ') : '任意模型' }}
                  </p>
                  <p v-if="r.path_regex.length" class="text-muted-foreground truncate font-mono text-[11px]" :title="r.path_regex.join('  ')">
                    {{ r.path_regex.join(' · ') }}
                  </p>
                </TableCell>
                <TableCell class="min-w-44">
                  <div class="flex flex-col gap-1">
                    <div v-for="(ks, j) in r.key_sources.slice(0, 3)" :key="j" class="flex min-w-0 items-center gap-1.5">
                      <Badge variant="outline" class="h-4 shrink-0 px-1 font-mono text-[10px]" :class="keySourceLabel(ks).type === 'gjson' ? 'border-violet-500/50 text-violet-700 dark:text-violet-400' : 'border-emerald-500/50 text-emerald-700 dark:text-emerald-400'">
                        {{ keySourceLabel(ks).type }}
                      </Badge>
                      <span class="truncate font-mono">{{ keySourceLabel(ks).value }}</span>
                    </div>
                    <span v-if="r.key_sources.length > 3" class="text-muted-foreground text-[10px]">+{{ r.key_sources.length - 3 }}</span>
                  </div>
                </TableCell>
                <TableCell class="whitespace-nowrap">
                  <div class="flex items-center gap-1.5" :class="MODE_CLASSES[ruleMode(r)]">
                    <component :is="MODE_ICONS[ruleMode(r)]" class="size-3.5" />
                    <span class="font-medium">{{ MODE_LABELS[ruleMode(r)] }}</span>
                  </div>
                  <p class="text-muted-foreground text-[11px]">
                    {{ modeIsOwn(r) ? '规则单独设置' : '继承全局' }}
                  </p>
                </TableCell>
                <TableCell class="whitespace-nowrap tabular-nums" :class="r.ttl_seconds ? '' : 'text-muted-foreground'">
                  {{ ttlLabel(r) }}
                </TableCell>
                <TableCell>
                  <div class="flex flex-wrap gap-1">
                    <Badge v-for="s in scopeBadges(r)" :key="s" variant="secondary" class="h-4 px-1 text-[10px]">
                      {{ s }}
                    </Badge>
                    <span v-if="!scopeBadges(r).length" class="text-muted-foreground">仅用户</span>
                  </div>
                  <p v-if="passHeaders(r).names.length" class="text-muted-foreground mt-0.5 text-[11px] whitespace-nowrap" :title="passHeaders(r).names.join(', ')">
                    透传 {{ passHeaders(r).names.length }} 个请求头
                  </p>
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ stats ? formatNumber(stats.rules[r.name] ?? 0) : '—' }}
                </TableCell>
                <TableCell>
                  <div class="flex items-center justify-end gap-0.5">
                    <Button variant="ghost" size="icon-sm" :aria-label="`编辑「${r.name}」`" @click="openEdit(i)">
                      <Pencil />
                    </Button>
                    <DropdownMenu>
                      <DropdownMenuTrigger as-child>
                        <Button variant="ghost" size="icon-sm" :aria-label="`${r.name} 的更多操作`">
                          <MoreHorizontal />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem :disabled="i === 0" @select="move(i, -1)">
                          <ArrowUp />
                          上移
                        </DropdownMenuItem>
                        <DropdownMenuItem :disabled="i === rules.length - 1" @select="move(i, 1)">
                          <ArrowDown />
                          下移
                        </DropdownMenuItem>
                        <DropdownMenuItem :disabled="rules.length >= LIMITS.rules" @select="duplicate(i)">
                          <Copy />
                          复制
                        </DropdownMenuItem>
                        <DropdownMenuItem :disabled="clearing || !stats?.rules[r.name]" @select="clear(r.name)">
                          <Eraser />
                          清空该规则缓存
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem variant="destructive" @select="requestDelete(i)">
                          <Trash2 />
                          删除
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <ul v-if="view === 'visual' && Object.keys(validation).length" class="text-destructive list-disc space-y-0.5 pl-5 text-xs" role="alert">
          <li v-for="line in errorLines(validation).slice(0, 5)" :key="line">
            {{ line }}
          </li>
        </ul>

        <!-- JSON -->
        <div v-if="view === 'json'" class="space-y-2">
          <Textarea
            v-model="jsonText"
            rows="22"
            spellcheck="false"
            class="max-h-[60vh] font-mono text-xs leading-relaxed"
            aria-label="会话亲和设置 JSON"
            :aria-invalid="jsonProblems.length > 0"
            data-testid="affinity-json"
          />
          <ul v-if="jsonProblems.length" class="text-destructive list-disc space-y-0.5 pl-5 text-xs" role="alert" data-testid="affinity-json-errors">
            <li v-for="line in jsonProblems.slice(0, 8)" :key="line" class="break-all">
              {{ line }}
            </li>
          </ul>
          <p v-else class="text-muted-foreground text-xs">
            与 new-api「渠道亲和」相同的 JSON 结构：可直接粘贴 new-api 的设置。Key 来源支持 gjson 与 request_header；param_override_template 只支持 pass_headers 操作；未知字段会被忽略。
          </p>
        </div>
      </CardContent>

      <CardFooter class="flex-wrap justify-end gap-2 border-t">
        <p v-if="errorMsg" class="text-destructive w-full text-xs break-all" role="alert" data-testid="affinity-error">
          {{ errorMsg }}
        </p>
        <div class="mr-auto flex flex-wrap items-center gap-2 text-xs" data-testid="affinity-cache">
          <span class="text-muted-foreground">缓存条目</span>
          <span class="font-medium tabular-nums">{{ stats ? `${formatNumber(stats.entries)} / ${formatNumber(stats.maxEntries)}` : '—' }}</span>
          <Button variant="ghost" size="xs" :disabled="statsLoading" @click="loadStats">
            <RefreshCw :class="statsLoading ? 'animate-spin' : ''" />
            刷新缓存
          </Button>
          <Button variant="ghost" size="xs" class="text-destructive" :disabled="clearing || !stats?.entries" data-testid="affinity-clear-all" @click="clearOpen = true">
            <Eraser />
            清空全部缓存
          </Button>
        </div>
        <Button v-if="source === 'db'" variant="ghost" size="sm" :disabled="disabled" title="删除数据库中的值，恢复为内置的 OmniGate 预设" @click="resetDefault">
          <RotateCcw />
          恢复默认
        </Button>
        <Button variant="ghost" size="sm" :disabled="!dirty || disabled" @click="reset">
          <Undo2 />
          放弃修改
        </Button>
        <Button size="sm" :disabled="!dirty || disabled || conflict" data-testid="save-affinity" @click="save">
          <Loader2 v-if="saving" class="animate-spin" />
          <Save v-else />
          保存
        </Button>
      </CardFooter>
    </template>

    <AffinityRuleSheet
      v-model:open="sheetOpen"
      :rule="editing"
      :other-names="otherNames"
      :global-mode="config.session_mode"
      :default-ttl="Number.isFinite(config.default_ttl_seconds) ? config.default_ttl_seconds : 3600"
      @save="onRuleSave"
    />

    <ConfirmDialog
      v-model:open="deleteOpen"
      :title="`删除规则「${deleteIndex !== null ? rules[deleteIndex]?.name : ''}」？`"
      confirm-text="删除"
      destructive
      @confirm="applyDelete"
    >
      <p>删除后点击「保存」才会生效；该规则已有的会话绑定会在 TTL 到期后自然失效，也可以先清空它的缓存。</p>
    </ConfirmDialog>

    <ConfirmDialog
      v-model:open="clearOpen"
      title="清空全部会话绑定？"
      confirm-text="清空"
      destructive
      :loading="clearing"
      @confirm="clear()"
    >
      <p>所有会话下一次请求会重新选择渠道（可能暂时降低上游缓存命中率）。规则不受影响。</p>
    </ConfirmDialog>
  </Card>
</template>
