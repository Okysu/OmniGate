<script setup lang="ts">
import type { PricePrefill } from './PriceDialog.vue'
import type { Channel, ModelEntry, Price, PriceKind } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Boxes, Plus, RefreshCw, Search, Tags } from '@lucide/vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useCurrency } from '@/composables/useCurrency'
import { fetchAllChannels, modelsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { PRICE_KIND_LABELS } from '@/lib/labels'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { isFullChannel } from '@/lib/types'
import { useAuthStore } from '@/stores/auth'
import ModelInfoTab from './ModelInfoTab.vue'
import ScheduleBadge from '@/components/ScheduleBadge.vue'
import SuggestInput from '@/components/SuggestInput.vue'
import { hasSchedule } from '@/lib/priceSchedule'
import { hasTiers, resolveTiers } from '@/lib/priceTiers'
import TierBadge from '@/components/pricing/TierBadge.vue'
import BillingModeBadge from '@/components/pricing/BillingModeBadge.vue'
import PriceSummaryLine from '@/components/pricing/PriceSummaryLine.vue'
import { priceSummary, priceSummaryTitle } from '@/lib/billingMode'
import PriceDialog from './PriceDialog.vue'

const PAGE_SIZE = 20
const ALL = 'all'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

// The route requires models.manage (non-managers are redirected to 模型广场).
const canManage = computed(() => auth.can('models.manage'))
type Tab = 'catalog' | 'prices' | 'info'
const tab = computed<Tab>(() => {
  const t = queryStr(route.query.tab)
  return t === 'prices' || t === 'info' ? t : 'catalog'
})
function setTab(v: string | number) {
  const t = v === 'prices' || v === 'info' ? v : undefined
  void router.replace({ query: { ...route.query, tab: t, page: undefined } })
}
const infoTab = ref<InstanceType<typeof ModelInfoTab> | null>(null)
function refresh() {
  if (tab.value === 'prices')
    void loadPrices()
  else if (tab.value === 'info')
    void infoTab.value?.load()
  else
    void loadModels()
}

// ---------- catalog ----------
const models = ref<ModelEntry[]>([])
const modelsLoading = ref(false)
const modelsError = ref<unknown>(null)
const modelSearch = ref('')

async function loadModels() {
  modelsLoading.value = true
  modelsError.value = null
  try {
    models.value = (await modelsApi.list()).items
  }
  catch (err) {
    modelsError.value = err
  }
  finally {
    modelsLoading.value = false
  }
}
onMounted(loadModels)

const filteredModels = computed(() => {
  const q = modelSearch.value.trim().toLowerCase()
  return q ? models.value.filter(m => m.model.toLowerCase().includes(q)) : models.value
})

/** Summary per price (计费方式 + main / other prices), computed once per list. */
const catalogSummaries = computed(() => new Map(models.value.map(m => [m.model, priceSummary(m.price)])))

function isFuture(iso: string): boolean {
  return new Date(iso).getTime() > Date.now()
}

// ---------- prices (models.manage) ----------
const page = computed(() => queryInt(route.query.page, 1))
const kindFilter = computed(() => queryStr(route.query.kind) || ALL)
const modelFilter = computed(() => queryStr(route.query.model))
const modelFilterInput = ref(modelFilter.value)

const prices = ref<Price[]>([])
const pricesTotal = ref(0)
const pricesLoading = ref(false)
const pricesError = ref<unknown>(null)
let controller: AbortController | null = null

async function loadPrices() {
  if (!canManage.value || tab.value !== 'prices')
    return
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  pricesLoading.value = true
  pricesError.value = null
  try {
    const res = await modelsApi.listPrices({
      page: page.value,
      pageSize: PAGE_SIZE,
      kind: kindFilter.value === ALL ? undefined : kindFilter.value as PriceKind,
      model: modelFilter.value || undefined,
    }, ctrl.signal)
    prices.value = res.items
    pricesTotal.value = res.total
  }
  catch (err) {
    if (!isAbortError(err))
      pricesError.value = err
  }
  finally {
    if (controller === ctrl)
      pricesLoading.value = false
  }
}
watch([tab, page, kindFilter, modelFilter], loadPrices, { immediate: true })
onBeforeUnmount(() => controller?.abort())

function setPriceQuery(next: Record<string, string | number | undefined>) {
  const q: Record<string, unknown> = { ...route.query }
  for (const [k, v] of Object.entries(next))
    q[k] = v === undefined || v === '' || v === ALL || v === 1 ? undefined : String(v)
  void router.replace({ query: q as Record<string, string> })
}
function applyModelFilter() {
  setPriceQuery({ model: modelFilterInput.value.trim(), page: undefined })
}

// Channels: names for cost prices and the channel picker in the dialog.
const channels = ref<Channel[]>([])
const channelsLoading = ref(false)
let channelsLoaded = false
async function ensureChannels() {
  if (channelsLoaded || !canManage.value || !auth.can('channels.read'))
    return
  channelsLoaded = true
  channelsLoading.value = true
  try {
    channels.value = (await fetchAllChannels()).filter(isFullChannel)
  }
  catch {
    channelsLoaded = false
  }
  finally {
    channelsLoading.value = false
  }
}
watch(tab, (t) => {
  if (t === 'prices')
    void ensureChannels()
}, { immediate: true })
const channelName = computed(() => new Map(channels.value.map(c => [c.id, c.name])))
const priceSummaries = computed(() => new Map(prices.value.map(p => [p.id, priceSummary(p)])))

// ---------- dialog ----------
const dialogOpen = ref(false)
const prefill = ref<PricePrefill | null>(null)
function openCreate(p: PricePrefill | null = null) {
  void ensureChannels()
  prefill.value = p
  dialogOpen.value = true
}
function onCreated() {
  void loadModels()
  if (tab.value === 'prices')
    void loadPrices()
}
// Price managers may price models on channels they cannot use themselves.
const allModelNames = ref<string[]>([])
async function loadAllModelNames() {
  if (!canManage.value)
    return
  try {
    allModelNames.value = (await modelsApi.listAll()).items.map(m => m.model)
  }
  catch {
    allModelNames.value = []
  }
}
onMounted(loadAllModelNames)
const modelNames = computed(() =>
  [...new Set([...models.value.map(m => m.model), ...allModelNames.value])].sort())
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="模型管理" description="逻辑模型、售价与成本价版本，以及在模型广场中展示的模型资料。计费方式可按 token、按次、按张或按音频时长 / 字符，也可自由组合。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="modelsLoading || pricesLoading" @click="refresh">
          <RefreshCw :class="modelsLoading || pricesLoading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button v-if="canManage && tab !== 'info'" size="sm" @click="openCreate()">
          <Plus />
          新增价格版本
        </Button>
      </template>
    </PageHeader>

    <Tabs :model-value="tab" @update:model-value="setTab">
      <TabsList>
        <TabsTrigger value="catalog">
          模型列表
        </TabsTrigger>
        <TabsTrigger value="prices">
          价格版本
        </TabsTrigger>
        <TabsTrigger value="info" data-testid="tab-model-info">
          模型资料
        </TabsTrigger>
      </TabsList>

      <TabsContent value="catalog">
        <Card>
          <CardContent class="space-y-4">
            <div class="relative w-full sm:max-w-xs">
              <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input v-model="modelSearch" placeholder="搜索模型" class="pl-8" aria-label="搜索模型" />
            </div>

            <ErrorState v-if="modelsError" :error="modelsError" @retry="loadModels" />
            <div v-else-if="modelsLoading && models.length === 0" class="space-y-2">
              <Skeleton v-for="i in 5" :key="i" class="h-10 w-full" />
            </div>
            <EmptyState
              v-else-if="filteredModels.length === 0"
              :icon="Boxes"
              :title="modelSearch ? '没有匹配的模型' : '暂无可用模型'"
              :description="modelSearch ? undefined : '模型来自你可以使用的渠道中的模型映射。先在「渠道」页面添加渠道并配置模型。'"
            />
            <div v-else class="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>模型</TableHead>
                    <TableHead class="text-right">
                      渠道数
                    </TableHead>
                    <TableHead>计费方式</TableHead>
                    <TableHead class="text-right">
                      价格
                    </TableHead>
                    <TableHead class="hidden text-right md:table-cell" title="缓存、图片输入、音频 token 等其余单价">
                      其他单价
                    </TableHead>
                    <TableHead v-if="canManage" class="w-10">
                      <span class="sr-only">操作</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="m in filteredModels" :key="m.model">
                    <TableCell class="font-mono text-xs">
                      <span class="inline-flex items-center gap-1.5">
                        {{ m.model }}
                        <ScheduleBadge v-if="m.price && hasSchedule(m.price)" :schedule="m.price.schedule" :timezone="m.price.scheduleTimezone" class="font-sans" />
                        <TierBadge v-if="m.price && hasTiers(m.price)" :base="m.price" :tiers="resolveTiers(m.price, m.price.tiers)" :money="money" />
                      </span>
                    </TableCell>
                    <TableCell class="text-right tabular-nums">
                      {{ m.channels }}
                    </TableCell>
                    <template v-if="catalogSummaries.get(m.model)">
                      <TableCell data-testid="catalog-mode">
                        <BillingModeBadge :mode="catalogSummaries.get(m.model)!.mode" />
                      </TableCell>
                      <TableCell class="text-right" :title="priceSummaryTitle(catalogSummaries.get(m.model)!, money)" data-testid="catalog-price">
                        <PriceSummaryLine :summary="catalogSummaries.get(m.model)!" :money="money" />
                      </TableCell>
                      <TableCell class="hidden text-right md:table-cell">
                        <PriceSummaryLine :summary="catalogSummaries.get(m.model)!" :money="money" parts="extra" />
                      </TableCell>
                    </template>
                    <template v-else>
                      <TableCell class="text-muted-foreground">
                        —
                      </TableCell>
                      <TableCell class="text-right">
                        <Badge variant="secondary">
                          免费
                        </Badge>
                      </TableCell>
                      <TableCell class="hidden md:table-cell" />
                    </template>
                    <TableCell v-if="canManage">
                      <Button variant="ghost" size="xs" @click="openCreate({ kind: 'sell', model: m.model, from: m.price })">
                        <Tags />
                        改价
                      </Button>
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <p class="text-muted-foreground text-xs">
              「免费」表示该模型没有售价，调用不扣费。价格变更以新版本形式生效，不影响已产生的账单。
            </p>
          </CardContent>
        </Card>
      </TabsContent>

      <TabsContent v-if="canManage" value="prices">
        <Card>
          <CardContent class="space-y-4">
            <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
              <Select :model-value="kindFilter" @update:model-value="(v) => setPriceQuery({ kind: String(v), page: undefined })">
                <SelectTrigger class="w-full sm:w-36" aria-label="按类型过滤">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem :value="ALL">
                    全部类型
                  </SelectItem>
                  <SelectItem value="sell">
                    售价
                  </SelectItem>
                  <SelectItem value="cost">
                    成本价
                  </SelectItem>
                </SelectContent>
              </Select>
              <form class="flex w-full gap-2 sm:max-w-sm" @submit.prevent="applyModelFilter">
                <SuggestInput v-model="modelFilterInput" :options="modelNames" placeholder="按模型名精确过滤" mono clearable class="flex-1" aria-label="按模型过滤" @select="applyModelFilter" @clear="applyModelFilter" />
                <Button type="submit" variant="outline">
                  过滤
                </Button>
              </form>
            </div>

            <ErrorState v-if="pricesError" :error="pricesError" @retry="loadPrices" />
            <div v-else-if="pricesLoading && prices.length === 0" class="space-y-2">
              <Skeleton v-for="i in 5" :key="i" class="h-10 w-full" />
            </div>
            <EmptyState
              v-else-if="prices.length === 0"
              :icon="Tags"
              title="没有价格版本"
              description="没有价格的模型按 0 计价（免费、无成本）。"
            />
            <div v-else class="overflow-x-auto" :class="pricesLoading ? 'opacity-60 transition-opacity' : ''">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>生效时间</TableHead>
                    <TableHead>类型</TableHead>
                    <TableHead>模型</TableHead>
                    <TableHead>渠道</TableHead>
                    <TableHead>计费方式</TableHead>
                    <TableHead class="text-right">
                      价格
                    </TableHead>
                    <TableHead class="hidden text-right lg:table-cell" title="缓存、图片输入、音频 token 等其余单价">
                      其他单价
                    </TableHead>
                    <TableHead class="hidden xl:table-cell">
                      创建时间
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="p in prices" :key="p.id">
                    <TableCell class="whitespace-nowrap">
                      {{ formatDateTime(p.effectiveAt) }}
                      <Badge v-if="isFuture(p.effectiveAt)" variant="outline" class="ml-1 border-amber-500/50 text-amber-700 dark:text-amber-400">
                        待生效
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Badge :variant="p.kind === 'sell' ? 'secondary' : 'outline'">
                        {{ PRICE_KIND_LABELS[p.kind] ?? p.kind }}
                      </Badge>
                    </TableCell>
                    <TableCell class="font-mono text-xs">
                      <span class="inline-flex items-center gap-1.5">
                        {{ p.model }}
                        <ScheduleBadge v-if="hasSchedule(p)" :schedule="p.schedule" :timezone="p.scheduleTimezone" class="font-sans" />
                        <TierBadge v-if="hasTiers(p)" :base="p" :tiers="resolveTiers(p, p.tiers)" :money="money" />
                      </span>
                    </TableCell>
                    <TableCell class="text-xs">
                      <template v-if="p.channelId">
                        {{ channelName.get(p.channelId) ?? p.channelId.slice(0, 8) }}
                      </template>
                      <span v-else class="text-muted-foreground">—</span>
                    </TableCell>
                    <TableCell data-testid="price-mode">
                      <BillingModeBadge :mode="priceSummaries.get(p.id)!.mode" />
                    </TableCell>
                    <TableCell class="text-right" :title="priceSummaryTitle(priceSummaries.get(p.id)!, money)" data-testid="price-summary-cell">
                      <PriceSummaryLine :summary="priceSummaries.get(p.id)!" :money="money" />
                    </TableCell>
                    <TableCell class="hidden text-right lg:table-cell">
                      <PriceSummaryLine :summary="priceSummaries.get(p.id)!" :money="money" parts="extra" />
                    </TableCell>
                    <TableCell class="text-muted-foreground hidden text-xs whitespace-nowrap xl:table-cell">
                      {{ formatDateTime(p.createdAt) }}
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <DataPagination
              v-if="!pricesError && pricesTotal > 0"
              :page="page"
              :page-size="PAGE_SIZE"
              :total="pricesTotal"
              :disabled="pricesLoading"
              @update:page="(p) => setPriceQuery({ page: p })"
            />
          </CardContent>
        </Card>
      </TabsContent>

      <TabsContent value="info">
        <ModelInfoTab v-if="tab === 'info'" ref="infoTab" />
      </TabsContent>
    </Tabs>

    <PriceDialog
      v-if="canManage"
      v-model:open="dialogOpen"
      :models="modelNames"
      :channels="channels"
      :channels-loading="channelsLoading"
      :prefill="prefill"
      @created="onCreated"
    />
  </div>
</template>
