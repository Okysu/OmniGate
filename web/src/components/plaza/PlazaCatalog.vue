<script setup lang="ts">
import type { CurrencyInfo } from '@/lib/money'
import type { PlazaFilters, PlazaSort, SourceFilter } from '@/lib/plaza'
import type { ModelCapability, PlazaModel } from '@/lib/types'
import { computed, ref } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { Boxes, Search, SearchX, X } from '@lucide/vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  CAPABILITIES,
  EMPTY_FILTERS,
  filterPlaza,
  hasActiveFilters,
  isMyPlazaModel,
  PROTOCOL_DESCRIPTIONS,
  protocolsOf,
  SORT_LABELS,
  SOURCE_FILTER_LABELS,
  sortPlaza,
  vendorsOf,
} from '@/lib/plaza'
import PlazaModelCard from './PlazaModelCard.vue'
import PlazaModelSheet from './PlazaModelSheet.vue'

/**
 * Searchable, filterable model catalog shared by the public `/models` page, the
 * console "模型广场" (`mode="platform"`) and "我的模型" (`mode="mine"`, adds the
 * source filter and per-model tier / billing badges).
 */
const props = withDefaults(defineProps<{
  items: PlazaModel[]
  loading?: boolean
  error?: unknown
  currency: CurrencyInfo | null
  mode?: 'platform' | 'mine'
  /** Empty state when the catalog itself has no models (not caused by filters). */
  emptyTitle?: string
  emptyDescription?: string
}>(), {
  loading: false,
  error: undefined,
  mode: 'platform',
  emptyTitle: '暂无模型',
  emptyDescription: undefined,
})
defineEmits<{ retry: [] }>()

const ANY = '__any__'
const filters = ref<PlazaFilters>({ ...EMPTY_FILTERS, capabilities: [] })
const sort = ref<PlazaSort>('default')

const vendors = computed(() => vendorsOf(props.items))
const protocols = computed(() => protocolsOf(props.items))
const visible = computed(() => sortPlaza(filterPlaza(props.items, filters.value), sort.value))
const filtered = computed(() => hasActiveFilters(filters.value))

const sourceCounts = computed<Record<SourceFilter, number>>(() => {
  const out = { all: props.items.length, own: 0, shared: 0, platform: 0 }
  for (const m of props.items) {
    if (!isMyPlazaModel(m))
      continue
    for (const t of ['own', 'shared', 'platform'] as const) {
      if (m.sources[t] > 0)
        out[t]++
    }
  }
  return out
})

function toggleCapability(c: ModelCapability) {
  const list = filters.value.capabilities
  filters.value.capabilities = list.includes(c) ? list.filter(x => x !== c) : [...list, c]
}
function clearFilters() {
  filters.value = { ...EMPTY_FILTERS, capabilities: [] }
}
function onVendor(v: unknown) {
  filters.value.vendor = v === ANY || typeof v !== 'string' ? '' : v
}
function onProtocol(v: unknown) {
  filters.value.protocol = v === ANY || typeof v !== 'string' ? '' : v
}
function onSort(v: unknown) {
  if (v === 'default' || v === 'price' || v === 'context')
    sort.value = v
}

const wide = useMediaQuery('(min-width: 640px)')

// ---------- detail sheet ----------
const sheetOpen = ref(false)
const selected = ref<PlazaModel | null>(null)
function openModel(m: PlazaModel) {
  selected.value = m
  sheetOpen.value = true
}
</script>

<template>
  <div class="space-y-4" data-testid="plaza-catalog">
    <!-- Source (我的模型) -->
    <div v-if="mode === 'mine'" class="bg-muted inline-flex max-w-full items-center gap-0.5 overflow-x-auto rounded-lg p-0.5" role="radiogroup" aria-label="按来源过滤" data-testid="source-filter">
      <button
        v-for="s in (['all', 'own', 'shared', 'platform'] as const)"
        :key="s"
        type="button"
        role="radio"
        :aria-checked="filters.source === s"
        class="focus-visible:ring-ring/50 flex h-8 shrink-0 items-center gap-1.5 rounded-md px-3 text-sm whitespace-nowrap outline-none focus-visible:ring-3"
        :class="filters.source === s ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'"
        :data-value="s"
        @click="filters.source = s"
      >
        {{ s === 'all' ? '全部' : SOURCE_FILTER_LABELS[s] }}
        <span class="text-muted-foreground text-xs tabular-nums">{{ sourceCounts[s] }}</span>
      </button>
    </div>

    <!-- Search, selects, sort -->
    <div class="flex flex-col gap-2 lg:flex-row lg:items-center" data-testid="plaza-toolbar">
      <div class="relative w-full lg:max-w-xs">
        <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
        <Input v-model="filters.q" placeholder="搜索模型名、厂商或标签" class="pl-8" aria-label="搜索模型" data-testid="plaza-search" />
      </div>
      <div class="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
        <Select :model-value="filters.vendor || ANY" @update:model-value="onVendor">
          <SelectTrigger class="w-full sm:w-40" aria-label="按厂商过滤" data-testid="vendor-filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem :value="ANY">
              全部厂商
            </SelectItem>
            <SelectItem v-for="v in vendors" :key="v" :value="v">
              {{ v }}
            </SelectItem>
          </SelectContent>
        </Select>
        <Select :model-value="filters.protocol || ANY" @update:model-value="onProtocol">
          <SelectTrigger class="w-full sm:w-44" aria-label="按协议过滤" data-testid="protocol-filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem :value="ANY">
              全部协议
            </SelectItem>
            <SelectItem v-for="p in protocols" :key="p" :value="p">
              {{ PROTOCOL_DESCRIPTIONS[p]?.replace(/（.*）$/, '') ?? p }}
            </SelectItem>
          </SelectContent>
        </Select>
        <Select :model-value="sort" @update:model-value="onSort">
          <SelectTrigger class="col-span-2 w-full sm:w-36" aria-label="排序" data-testid="plaza-sort">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="(label, key) in SORT_LABELS" :key="key" :value="key">
              {{ label }}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>

    <!-- Capability chips, plan coverage, count -->
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <div class="flex flex-wrap items-center gap-1.5" role="group" aria-label="按能力过滤">
        <button
          v-for="c in CAPABILITIES"
          :key="c.key"
          type="button"
          :aria-pressed="filters.capabilities.includes(c.key)"
          class="focus-visible:ring-ring/50 h-7 rounded-full border px-3 text-xs transition-colors outline-none focus-visible:ring-3"
          :class="filters.capabilities.includes(c.key) ? 'border-primary bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-muted'"
          :title="c.description"
          :data-capability="c.key"
          @click="toggleCapability(c.key)"
        >
          {{ c.label }}
        </button>
      </div>
      <label class="flex h-7 items-center gap-2 text-xs">
        <Switch v-model="filters.coveredOnly" size="sm" data-testid="covered-only" />
        {{ mode === 'mine' ? '仅显示有套餐 / 订阅覆盖' : '仅显示有套餐覆盖' }}
      </label>
      <div class="ml-auto flex h-7 items-center gap-2">
        <Button v-if="filtered" variant="ghost" size="xs" @click="clearFilters">
          <X />
          清除过滤
        </Button>
        <span v-if="!loading || items.length" class="text-muted-foreground text-xs tabular-nums" data-testid="plaza-count">
          {{ filtered ? `${visible.length} / ${items.length}` : items.length }} 个模型
        </span>
      </div>
    </div>

    <ErrorState v-if="error" :error="error" @retry="$emit('retry')" />

    <!-- Loading -->
    <div v-else-if="loading && items.length === 0" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-busy="true" aria-label="加载中" data-testid="plaza-skeleton">
      <div v-for="i in 6" :key="i" class="space-y-3 rounded-xl border p-4">
        <div class="flex items-start gap-3">
          <Skeleton class="size-10 rounded-lg" />
          <div class="flex-1 space-y-2">
            <Skeleton class="h-4 w-2/3" />
            <Skeleton class="h-3 w-1/2" />
          </div>
        </div>
        <Skeleton class="h-3 w-3/4" />
        <div class="flex gap-1">
          <Skeleton class="h-5 w-12 rounded-full" />
          <Skeleton class="h-5 w-16 rounded-full" />
        </div>
        <Skeleton class="h-10 w-full" />
      </div>
    </div>

    <!-- Empty -->
    <EmptyState
      v-else-if="visible.length === 0"
      :icon="filtered ? SearchX : Boxes"
      :title="filtered ? '没有匹配的模型' : emptyTitle"
      :description="filtered ? '换个关键词，或清除过滤条件。' : emptyDescription"
      data-testid="plaza-empty"
    >
      <Button v-if="filtered" variant="outline" size="sm" @click="clearFilters">
        清除过滤
      </Button>
      <slot v-else name="empty" />
    </EmptyState>

    <!-- Grid (wide) / list (phones) -->
    <ul v-else-if="wide" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" :class="loading ? 'opacity-60 transition-opacity' : ''" data-testid="plaza-grid">
      <li v-for="m in visible" :key="m.model" class="flex min-w-0">
        <PlazaModelCard :model="m" :currency="currency" class="flex-1" @open="openModel(m)" />
      </li>
    </ul>
    <ul v-else class="divide-y border-y" :class="loading ? 'opacity-60 transition-opacity' : ''" data-testid="plaza-list">
      <li v-for="m in visible" :key="m.model">
        <PlazaModelCard :model="m" :currency="currency" layout="row" @open="openModel(m)" />
      </li>
    </ul>

    <PlazaModelSheet v-model:open="sheetOpen" :model="selected" :currency="currency" />
  </div>
</template>
