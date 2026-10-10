<script setup lang="ts">
import type { GatewayKey, RequestLog, User } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ChevronDown, ChevronRight, RefreshCw, Radio, ScrollText, UserRound, X } from '@lucide/vue'
import { useIntervalFn } from '@vueuse/core'
import CopyButton from '@/components/CopyButton.vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import SuggestInput from '@/components/SuggestInput.vue'
import TimeRangePicker from '@/components/TimeRangePicker.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useCurrency } from '@/composables/useCurrency'
import { useRangeQuery } from '@/composables/useRangeQuery'
import { keysApi, logsApi, modelsApi } from '@/lib/endpoints'
import { formatAudioSeconds, isAudioInbound } from '@/lib/audio'
import { formatDateTime, formatMs, formatNumber } from '@/lib/format'
import { logMultiplier } from '@/lib/groups'
import PriceTierLogBadge from '@/components/pricing/PriceTierLogBadge.vue'
import { tierLabel } from '@/lib/priceTiers'
import { ERROR_CLASS_HINTS, INBOUND_LABELS } from '@/lib/labels'
import { isOutcome, OUTCOME_CLASSES, OUTCOME_HINTS, OUTCOME_LABELS, OUTCOMES } from '@/lib/affinity'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { useAuthStore } from '@/stores/auth'

const PAGE_SIZE = 50
const ALL = 'all'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

const seeAll = computed(() => auth.can('stats.all'))
const canSeeSubscriptions = computed(() => auth.can('billing.manage'))

/** Served by a fallback model of a route rule (Round 5 `servedModel`). */
function isFallback(l: RequestLog): l is RequestLog & { servedModel: string } {
  return typeof l.servedModel === 'string' && l.servedModel !== '' && l.servedModel !== l.model
}

/** Served by the user's own or a shared channel (phase5-api.md §1): not billed. */
function freeTier(l: RequestLog): 'own' | 'shared' | null {
  return l.channelTier === 'own' || l.channelTier === 'shared' ? l.channelTier : null
}
const TIER_BADGES = {
  own: { label: '自有渠道', cls: 'border-emerald-500/50 text-emerald-700 dark:text-emerald-400', title: '由该用户自己的渠道提供服务：不计费、不计入套餐额度' },
  shared: { label: '共享渠道', cls: 'border-sky-500/50 text-sky-700 dark:text-sky-400', title: '由其他用户共享的渠道提供服务：不计费、不计入套餐额度' },
} as const
function tierBadge(l: RequestLog) {
  const t = freeTier(l)
  return t ? TIER_BADGES[t] : null
}

/** Output images of an image request (phase7 §1.1); 0 for everything else / older backends. */
function imageCount(l: RequestLog): number {
  return typeof l.imageCount === 'number' && l.imageCount > 0 ? l.imageCount : 0
}
function isImageRequest(l: RequestLog): boolean {
  return l.inbound.startsWith('openai.images') || imageCount(l) > 0 || (l.usage.imageInputTokens ?? 0) > 0
}

/** phase9 §1.1: seconds of input audio (transcriptions / translations); 0 otherwise / older backends. */
function audioSeconds(l: RequestLog): number {
  return typeof l.audioSeconds === 'number' && l.audioSeconds > 0 ? l.audioSeconds : 0
}
function isAudioRequest(l: RequestLog): boolean {
  return isAudioInbound(l.inbound) || audioSeconds(l) > 0 || (l.usage.audioInputTokens ?? 0) > 0 || (l.usage.audioOutputTokens ?? 0) > 0
}
const USAGE_ESTIMATED_HINT = '上游未返回用量，仅按每次请求费用计费（估算）'
/**
 * Audio request without upstream usage (`usageEstimated`; the server mirrors it in
 * `usage.estimated`): shown as "估算" instead of the token-estimate badge.
 */
function audioUsageEstimated(l: RequestLog): boolean {
  return isAudioRequest(l) && (l.usageEstimated === true || l.usage.estimated)
}

function isOwn(l: RequestLog): boolean {
  return !l.user || l.user.id === auth.user?.id
}
const range = useRangeQuery('24h', ['1h', '24h', '7d', 'custom'])

const page = computed(() => queryInt(route.query.page, 1))
const model = computed(() => queryStr(route.query.model))
const status = computed(() => queryStr(route.query.status) || ALL)
const keyId = computed(() => queryStr(route.query.keyId) || ALL)
const userId = computed(() => (seeAll.value ? queryStr(route.query.userId) : ''))
/** phase12 §4: session affinity outcome filter ('any' = an affinity rule applied). */
const affinity = computed(() => {
  const v = queryStr(route.query.affinity)
  return v === 'any' || isOutcome(v) ? v : ALL
})
const modelInput = ref(model.value)
watch(model, v => (modelInput.value = v))

function setQuery(next: Record<string, string | number | undefined>) {
  const q: Record<string, unknown> = { ...route.query }
  for (const [k, v] of Object.entries(next))
    q[k] = v === undefined || v === '' || v === ALL || (k === 'page' && v === 1) ? undefined : String(v)
  void router.replace({ query: q as Record<string, string> })
}
function applyModelFilter() {
  if (modelInput.value.trim() !== model.value)
    setQuery({ model: modelInput.value.trim(), page: undefined })
}

const items = ref<RequestLog[]>([])
const total = ref(0)
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load(opts: { silent?: boolean } = {}) {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  if (!opts.silent)
    loading.value = true
  try {
    const { from, to } = range.resolve()
    const res = await logsApi.list({
      page: page.value,
      pageSize: PAGE_SIZE,
      from,
      to,
      model: model.value || undefined,
      status: status.value === ALL ? undefined : status.value as 'success' | 'error',
      keyId: keyId.value === ALL ? undefined : keyId.value,
      userId: userId.value || undefined,
      affinity: affinity.value === ALL ? undefined : affinity.value,
    }, ctrl.signal)
    items.value = res.items
    total.value = res.total
    loadError.value = null
  }
  catch (err) {
    if (!isAbortError(err))
      loadError.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}

watch(() => route.query, () => void load(), { immediate: true, deep: true })
onBeforeUnmount(() => controller?.abort())

// ---------- auto refresh ----------
const autoRefresh = ref(false)
const { pause, resume } = useIntervalFn(() => {
  if (!loading.value && document.visibilityState === 'visible')
    void load({ silent: true })
}, 10_000, { immediate: false })
watch(autoRefresh, v => (v ? resume() : pause()))

// ---------- filter options ----------
const modelOptions = ref<string[]>([])
const keys = ref<GatewayKey[]>([])
onMounted(async () => {
  const [m, k] = await Promise.allSettled([modelsApi.list(), auth.can('keys.own') ? keysApi.list() : Promise.resolve({ items: [] as GatewayKey[] })])
  if (m.status === 'fulfilled')
    modelOptions.value = m.value.items.map(e => e.model)
  if (k.status === 'fulfilled')
    keys.value = k.value.items
})

// User filter (stats.all)
const userPick = ref<string[]>(userId.value ? [userId.value] : [])
const userLabel = ref<string | null>(null)
const userPopover = ref(false)
watch(userId, (v) => {
  if (!v) {
    userPick.value = []
    userLabel.value = null
  }
})
function onUserPicked(u: User | null) {
  userLabel.value = u?.displayName ?? null
  userPopover.value = false
}
watch(userPick, v => setQuery({ userId: v[0], page: undefined }))

const hasFilters = computed(() => !!model.value || status.value !== ALL || keyId.value !== ALL || !!userId.value || affinity.value !== ALL)
function clearFilters() {
  setQuery({ model: undefined, status: undefined, keyId: undefined, userId: undefined, affinity: undefined, page: undefined })
}

// ---------- rows ----------
const expanded = ref<Set<string>>(new Set())
function toggleRow(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id))
    next.delete(id)
  else
    next.add(id)
  expanded.value = next
}

function statusClass(code: number): string {
  if (code >= 500)
    return 'border-destructive/50 text-destructive'
  if (code >= 400)
    return 'border-amber-500/50 text-amber-700 dark:text-amber-400'
  return 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400'
}

const colCount = computed(() => 12 + (seeAll.value ? 2 : 0))
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="请求日志" :description="seeAll ? '网关请求明细（全部用户）。默认不记录请求与响应正文。' : '你的网关请求明细。默认不记录请求与响应正文。'">
      <template #actions>
        <label class="flex items-center gap-2 text-sm">
          <Switch v-model="autoRefresh" aria-label="自动刷新" />
          <span class="flex items-center gap-1">
            <Radio v-if="autoRefresh" class="size-3.5 animate-pulse text-emerald-600 dark:text-emerald-400" />
            自动刷新（10 秒）
          </span>
        </label>
        <Button variant="outline" size="sm" :disabled="loading" @click="load()">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <div class="flex flex-col gap-2 xl:flex-row xl:flex-wrap xl:items-center">
          <TimeRangePicker
            v-model:preset="range.preset.value"
            :custom-from="range.customFromLocal.value"
            :custom-to="range.customToLocal.value"
            @apply="range.applyCustom"
          />
          <div class="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
            <form class="col-span-2 flex gap-2 sm:col-span-1" @submit.prevent="applyModelFilter">
              <SuggestInput v-model="modelInput" :options="modelOptions" placeholder="模型" mono clearable class="w-full sm:w-44" aria-label="按模型过滤" @select="applyModelFilter" @clear="applyModelFilter" @change="applyModelFilter" />
            </form>
            <Select :model-value="status" @update:model-value="(v) => setQuery({ status: String(v), page: undefined })">
              <SelectTrigger class="w-full sm:w-32" aria-label="按状态过滤">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ALL">
                  全部状态
                </SelectItem>
                <SelectItem value="success">
                  成功
                </SelectItem>
                <SelectItem value="error">
                  失败
                </SelectItem>
              </SelectContent>
            </Select>
            <Select :model-value="keyId" @update:model-value="(v) => setQuery({ keyId: String(v), page: undefined })">
              <SelectTrigger class="w-full sm:w-40" aria-label="按 API Key 过滤">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ALL">
                  全部 Key
                </SelectItem>
                <SelectItem v-for="k in keys" :key="k.id" :value="k.id">
                  {{ k.name }}
                </SelectItem>
                <SelectItem v-if="keyId !== ALL && !keys.some(k => k.id === keyId)" :value="keyId">
                  {{ keyId.slice(0, 8) }}…
                </SelectItem>
              </SelectContent>
            </Select>
            <Select :model-value="affinity" @update:model-value="(v) => setQuery({ affinity: String(v), page: undefined })">
              <SelectTrigger class="w-full sm:w-36" aria-label="按会话亲和过滤" data-testid="affinity-filter">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ALL">
                  全部会话
                </SelectItem>
                <SelectItem value="any">
                  会话亲和生效
                </SelectItem>
                <SelectItem v-for="o in OUTCOMES" :key="o" :value="o">
                  亲和：{{ OUTCOME_LABELS[o] }}
                </SelectItem>
              </SelectContent>
            </Select>
            <Popover v-if="seeAll" v-model:open="userPopover">
              <PopoverTrigger as-child>
                <Button variant="outline" class="w-full justify-start font-normal sm:w-auto">
                  <UserRound />
                  <span class="truncate">{{ userId ? (userLabel ?? `${userId.slice(0, 8)}…`) : '全部用户' }}</span>
                </Button>
              </PopoverTrigger>
              <PopoverContent align="start" class="w-80 gap-2">
                <Label>按用户过滤</Label>
                <UserPicker v-model="userPick" :multiple="false" @pick="onUserPicked" />
              </PopoverContent>
            </Popover>
            <Button v-if="hasFilters" variant="ghost" size="sm" class="self-center" @click="clearFilters">
              <X />
              清除过滤
            </Button>
          </div>
        </div>

        <ErrorState v-if="loadError" :error="loadError" @retry="load()" />
        <div v-else-if="loading && items.length === 0" class="space-y-2">
          <Skeleton v-for="i in 8" :key="i" class="h-10 w-full" />
        </div>
        <EmptyState
          v-else-if="items.length === 0"
          :icon="ScrollText"
          title="没有请求记录"
          :description="hasFilters ? '当前时间范围与过滤条件下没有请求。' : '当前时间范围内没有请求。使用 API Key 调用网关后，请求会出现在这里。'"
        />
        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table class="text-xs">
            <TableHeader>
              <TableRow>
                <TableHead class="w-6" />
                <TableHead>时间</TableHead>
                <TableHead v-if="seeAll">
                  用户
                </TableHead>
                <TableHead>模型</TableHead>
                <TableHead>渠道</TableHead>
                <TableHead>协议</TableHead>
                <TableHead>流式</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>错误类型</TableHead>
                <TableHead class="text-right">
                  TTFT
                </TableHead>
                <TableHead class="text-right">
                  耗时
                </TableHead>
                <TableHead class="text-right">
                  Tokens 入 / 出
                </TableHead>
                <TableHead class="text-right" title="钱包扣费；套餐覆盖的请求显示按售价折算的套餐计费（不扣钱包）">
                  费用
                </TableHead>
                <TableHead v-if="seeAll" class="text-right">
                  成本
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <template v-for="l in items" :key="l.id">
                <TableRow class="cursor-pointer" :aria-expanded="expanded.has(l.id)" @click="toggleRow(l.id)">
                  <TableCell class="pr-0">
                    <ChevronDown v-if="expanded.has(l.id)" class="text-muted-foreground size-3.5" />
                    <ChevronRight v-else class="text-muted-foreground size-3.5" />
                  </TableCell>
                  <TableCell class="whitespace-nowrap tabular-nums">
                    {{ formatDateTime(l.startedAt) }}
                  </TableCell>
                  <TableCell v-if="seeAll" class="max-w-28 truncate">
                    {{ l.user?.displayName || '—' }}
                  </TableCell>
                  <TableCell class="font-mono">
                    <span class="block max-w-48 truncate" :title="l.upstreamModel && l.upstreamModel !== l.model ? `${l.model} → ${l.upstreamModel}` : l.model">
                      {{ l.model }}<template v-if="l.upstreamModel && l.upstreamModel !== l.model && l.upstreamModel !== l.servedModel">
                        <span class="text-muted-foreground"> → {{ l.upstreamModel }}</span>
                      </template>
                    </span>
                    <Badge
                      v-if="isFallback(l)"
                      variant="outline"
                      class="mt-0.5 h-4 max-w-48 border-violet-500/50 px-1 font-sans text-[10px] text-violet-700 dark:text-violet-400"
                      :title="`请求的模型 ${l.model} 的渠道均失败，改用回退模型 ${l.servedModel} 提供服务（按 ${l.servedModel} 计价）`"
                      data-testid="served-model"
                    >
                      <span class="truncate">回退 → {{ l.servedModel }}</span>
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <div class="flex max-w-48 items-center gap-1">
                      <span class="truncate" :title="l.channelName ?? undefined">{{ l.channelName ?? '—' }}</span>
                      <Badge
                        v-if="freeTier(l)"
                        variant="outline"
                        class="h-4 shrink-0 px-1 text-[10px]"
                        :class="tierBadge(l)?.cls"
                        :title="tierBadge(l)?.title"
                        data-testid="channel-tier"
                      >
                        {{ tierBadge(l)?.label }}
                      </Badge>
                      <Badge v-if="l.attempts > 1" variant="outline" class="h-4 shrink-0 px-1 text-[10px]" :title="`共尝试 ${l.attempts} 次`">
                        ×{{ l.attempts }}
                      </Badge>
                      <Badge
                        v-if="isOutcome(l.affinity)"
                        variant="outline"
                        class="h-4 shrink-0 px-1 text-[10px]"
                        :class="OUTCOME_CLASSES[l.affinity]"
                        :title="`会话亲和（${l.affinityRule ?? '—'}）：${OUTCOME_HINTS[l.affinity]}`"
                        data-testid="affinity-badge"
                      >
                        亲和·{{ OUTCOME_LABELS[l.affinity] }}
                      </Badge>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary" class="text-[10px]">
                      {{ INBOUND_LABELS[l.inbound] ?? l.inbound }}
                    </Badge>
                  </TableCell>
                  <TableCell>{{ l.stream ? '是' : '否' }}</TableCell>
                  <TableCell>
                    <Badge variant="outline" class="tabular-nums" :class="statusClass(l.statusCode)">
                      {{ l.statusCode }}
                    </Badge>
                  </TableCell>
                  <TableCell class="font-mono" :title="l.errorClass ? ERROR_CLASS_HINTS[l.errorClass] : undefined">
                    {{ l.errorClass ?? '—' }}
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums">
                    {{ formatMs(l.ttftMs) }}
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums">
                    {{ formatMs(l.durationMs) }}
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums">
                    <span :title="`不含缓存 ${formatNumber(l.usage.input)} · 缓存读 ${formatNumber(l.usage.cacheRead)} · 缓存写 ${formatNumber(l.usage.cacheWrite)}`">{{ formatNumber(l.usage.input + l.usage.cacheRead + l.usage.cacheWrite) }}</span> / {{ formatNumber(l.usage.output) }}
                    <Badge v-if="l.usage.estimated && !audioUsageEstimated(l)" variant="outline" class="ml-1 h-4 border-amber-500/50 px-1 text-[10px] text-amber-700 dark:text-amber-400" title="上游未返回用量，Token 数为网关估算">
                      估
                    </Badge>
                    <Badge
                      v-if="imageCount(l)"
                      variant="outline"
                      class="ml-1 h-4 border-pink-500/50 px-1 text-[10px] text-pink-700 dark:text-pink-400"
                      :title="`输出 ${imageCount(l)} 张图片${l.usage.imageInputTokens ? `；图片输入 ${formatNumber(l.usage.imageInputTokens)} tokens` : ''}`"
                      data-testid="image-count"
                    >
                      {{ imageCount(l) }} 张图
                    </Badge>
                    <Badge
                      v-if="audioSeconds(l)"
                      variant="outline"
                      class="ml-1 h-4 border-orange-500/50 px-1 text-[10px] text-orange-700 dark:text-orange-400"
                      :title="`输入音频 ${formatAudioSeconds(audioSeconds(l))}（${formatNumber(audioSeconds(l))} 秒）`"
                      data-testid="audio-seconds"
                    >
                      {{ formatAudioSeconds(audioSeconds(l)) }}
                    </Badge>
                    <Badge
                      v-if="audioUsageEstimated(l)"
                      variant="outline"
                      class="ml-1 h-4 border-amber-500/50 px-1 text-[10px] text-amber-700 dark:text-amber-400"
                      :title="USAGE_ESTIMATED_HINT"
                      data-testid="usage-estimated"
                    >
                      估算
                    </Badge>
                  </TableCell>
                  <TableCell v-if="freeTier(l)" class="text-muted-foreground text-right whitespace-nowrap" :title="tierBadge(l)?.title" data-testid="charge-free">
                    不计费
                  </TableCell>
                  <TableCell v-else-if="l.subscriptionId" class="text-right whitespace-nowrap tabular-nums" title="由套餐订阅覆盖，不扣钱包；按售价折算的金额计入套餐额度">
                    <PriceTierLogBadge v-if="l.priceTier" :above="l.priceTier" class="mr-1" />
                    <Tooltip v-if="logMultiplier(l)">
                      <TooltipTrigger as-child>
                        <Badge variant="outline" class="mr-1 h-4 cursor-help border-violet-500/50 px-1 text-[10px] text-violet-700 tabular-nums dark:text-violet-400" tabindex="0" data-testid="price-multiplier" @click.stop>
                          {{ logMultiplier(l)!.short }}
                        </Badge>
                      </TooltipTrigger>
                      <TooltipContent>{{ logMultiplier(l)!.detail }}</TooltipContent>
                    </Tooltip>
                    <Badge variant="outline" class="mr-1 h-4 border-sky-500/50 px-1 text-[10px] text-sky-700 dark:text-sky-400">
                      套餐
                    </Badge>
                    <span class="text-muted-foreground">套餐计费 {{ money(l.quotaCharge) }}</span>
                  </TableCell>
                  <TableCell v-else class="text-right whitespace-nowrap tabular-nums">
                    <PriceTierLogBadge v-if="l.priceTier" :above="l.priceTier" class="mr-1" />
                    <Tooltip v-if="logMultiplier(l)">
                      <TooltipTrigger as-child>
                        <Badge variant="outline" class="mr-1 h-4 cursor-help border-violet-500/50 px-1 text-[10px] text-violet-700 tabular-nums dark:text-violet-400" tabindex="0" data-testid="price-multiplier" @click.stop>
                          {{ logMultiplier(l)!.short }}
                        </Badge>
                      </TooltipTrigger>
                      <TooltipContent>{{ logMultiplier(l)!.detail }}</TooltipContent>
                    </Tooltip>
                    {{ money(l.charge) }}
                  </TableCell>
                  <TableCell v-if="seeAll" class="text-right whitespace-nowrap tabular-nums">
                    {{ l.cost === null ? '—' : money(l.cost) }}
                  </TableCell>
                </TableRow>
                <TableRow v-if="expanded.has(l.id)" class="bg-muted/30 hover:bg-muted/30">
                  <TableCell :colspan="colCount" class="p-0">
                    <div class="grid gap-4 p-4 text-xs whitespace-normal lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
                      <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5">
                        <dt class="text-muted-foreground">
                          请求 ID
                        </dt>
                        <dd class="flex items-center gap-1 font-mono break-all">
                          {{ l.requestId }}
                          <CopyButton :value="l.requestId" label="复制请求 ID" />
                        </dd>
                        <dt class="text-muted-foreground">
                          API Key
                        </dt>
                        <dd>{{ l.keyName ?? '—' }}</dd>
                        <template v-if="seeAll">
                          <dt class="text-muted-foreground">
                            用户
                          </dt>
                          <dd>{{ l.user?.displayName || '—' }} <span v-if="l.user" class="text-muted-foreground font-mono">{{ l.user.id }}</span></dd>
                        </template>
                        <dt class="text-muted-foreground">
                          Token 明细
                        </dt>
                        <dd class="tabular-nums">
                          输入（不含缓存） {{ formatNumber(l.usage.input) }} · 输出 {{ formatNumber(l.usage.output) }} · 缓存读 {{ formatNumber(l.usage.cacheRead) }} · 缓存写 {{ formatNumber(l.usage.cacheWrite) }} · 推理 {{ formatNumber(l.usage.reasoning) }}
                          <span v-if="l.usage.estimated && !audioUsageEstimated(l)" class="text-amber-700 dark:text-amber-400">（估算）</span>
                        </dd>
                        <template v-if="isImageRequest(l)">
                          <dt class="text-muted-foreground">
                            图片
                          </dt>
                          <dd class="tabular-nums" data-testid="image-usage">
                            输出图片 {{ formatNumber(imageCount(l)) }} 张 · 图片输入 {{ formatNumber(l.usage.imageInputTokens ?? 0) }} tokens
                          </dd>
                        </template>
                        <template v-if="isAudioRequest(l)">
                          <dt class="text-muted-foreground">
                            音频
                          </dt>
                          <dd class="tabular-nums" data-testid="audio-usage">
                            <template v-if="audioSeconds(l)">
                              输入音频 {{ formatAudioSeconds(audioSeconds(l)) }} ·
                            </template>
                            音频输入 {{ formatNumber(l.usage.audioInputTokens ?? 0) }} tokens · 音频输出 {{ formatNumber(l.usage.audioOutputTokens ?? 0) }} tokens
                            <template v-if="l.usage.inputCharacters">
                              · 输入字符 {{ formatNumber(l.usage.inputCharacters) }}
                            </template>
                          </dd>
                        </template>
                        <template v-if="audioUsageEstimated(l)">
                          <dt class="text-muted-foreground">
                            用量
                          </dt>
                          <dd class="text-amber-700 dark:text-amber-400" data-testid="usage-estimated-detail">
                            {{ USAGE_ESTIMATED_HINT }}。
                          </dd>
                        </template>
                        <dt class="text-muted-foreground">
                          计费
                        </dt>
                        <dd v-if="freeTier(l)">
                          由{{ freeTier(l) === 'own' ? '自有' : '共享' }}渠道提供服务，<strong>不计费</strong>（不扣钱包、不计入套餐额度）。
                        </dd>
                        <dd v-else-if="l.subscriptionId" class="space-y-0.5">
                          <p>
                            由套餐订阅覆盖，<strong>不扣钱包</strong>；按售价折算 <span class="tabular-nums">{{ money(l.quotaCharge) }}</span> 计入套餐额度。
                          </p>
                          <p class="text-muted-foreground flex items-center gap-1">
                            订阅 <span class="font-mono break-all">{{ l.subscriptionId }}</span>
                            <CopyButton :value="l.subscriptionId" label="复制订阅 ID" />
                            <RouterLink v-if="canSeeSubscriptions" :to="{ path: '/console/billing/subscriptions', query: l.user ? { userId: l.user.id } : {} }" class="text-primary hover:underline">
                              查看订阅
                            </RouterLink>
                            <RouterLink v-else-if="isOwn(l)" to="/console/billing" class="text-primary hover:underline">
                              我的订阅
                            </RouterLink>
                          </p>
                        </dd>
                        <dd v-else class="tabular-nums">
                          钱包扣费 <span>{{ money(l.charge) }}</span>
                        </dd>
                        <template v-if="!freeTier(l) && l.priceTier">
                          <dt class="text-muted-foreground">
                            计价档位
                          </dt>
                          <dd data-testid="price-tier-detail">
                            {{ tierLabel(l.priceTier) }}：输入 + 缓存 token 超过阈值，整次请求按该档单价计费
                          </dd>
                        </template>
                        <template v-if="!freeTier(l) && logMultiplier(l)">
                          <dt class="text-muted-foreground">
                            价格倍率
                          </dt>
                          <dd data-testid="price-multiplier-detail">
                            {{ logMultiplier(l)!.detail }}
                          </dd>
                        </template>
                        <template v-if="isOutcome(l.affinity)">
                          <dt class="text-muted-foreground">
                            会话亲和
                          </dt>
                          <dd data-testid="affinity-detail">
                            <span class="font-medium">{{ OUTCOME_LABELS[l.affinity] }}</span>
                            <span class="text-muted-foreground"> · 规则 <span class="font-mono">{{ l.affinityRule ?? '—' }}</span></span>
                            <span class="text-muted-foreground block">{{ OUTCOME_HINTS[l.affinity] }}</span>
                          </dd>
                        </template>
                        <dt class="text-muted-foreground">
                          错误信息
                        </dt>
                        <dd class="break-all" :class="l.errorMessage ? 'text-destructive' : ''">
                          {{ l.errorMessage ?? '—' }}
                          <span v-if="l.errorClass && ERROR_CLASS_HINTS[l.errorClass]" class="text-muted-foreground block" data-testid="error-hint">{{ ERROR_CLASS_HINTS[l.errorClass] }}</span>
                        </dd>
                      </dl>
                      <div class="space-y-2">
                        <p class="text-muted-foreground">
                          路由尝试（{{ l.attempts }} 次）
                        </p>
                        <ol v-if="l.fallbackPath?.length" class="space-y-1">
                          <li v-for="(a, i) in l.fallbackPath" :key="i" class="bg-background flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5">
                            <span class="text-muted-foreground tabular-nums">#{{ i + 1 }}</span>
                            <span class="font-medium">{{ a.channelName }}</span>
                            <Badge variant="outline" class="tabular-nums" :class="statusClass(a.statusCode)">
                              {{ a.statusCode || '—' }}
                            </Badge>
                            <span v-if="a.errorClass" class="font-mono">{{ a.errorClass }}</span>
                            <span class="text-muted-foreground ml-auto tabular-nums">{{ formatMs(a.durationMs) }}</span>
                          </li>
                        </ol>
                        <p v-else class="text-muted-foreground">
                          没有回退记录{{ l.channelName ? `，直接由「${l.channelName}」处理` : '' }}。
                        </p>
                      </div>
                    </div>
                  </TableCell>
                </TableRow>
              </template>
            </TableBody>
          </Table>
        </div>

        <DataPagination
          v-if="!loadError && total > 0"
          :page="page"
          :page-size="PAGE_SIZE"
          :total="total"
          :disabled="loading"
          @update:page="(p) => setQuery({ page: p })"
        />
      </CardContent>
    </Card>
  </div>
</template>
