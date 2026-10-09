<script setup lang="ts">
import type { Channel, ChannelScope, ChannelType, ChannelView, EnabledStatus, HealthState } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Cable, Eye, Loader2, MoreHorizontal, Pencil, Plus, RefreshCw, Search, Trash2, Zap } from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ChannelBadges from '@/components/plugin-ui/ChannelBadges.vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { errorMessage, isApiError, isVersionConflict } from '@/lib/api'
import { channelsApi } from '@/lib/endpoints'
import { formatDateTime, formatRelative } from '@/lib/format'
import { CHANNEL_TYPE_LABELS, HEALTH_LABELS, SCOPE_LABELS } from '@/lib/labels'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { isFullChannel } from '@/lib/types'
import { useAuthStore } from '@/stores/auth'
import { useChannelSharesStore } from '@/stores/channelShares'
import ChannelFormSheet from './ChannelFormSheet.vue'
import IncomingSharesPanel from './IncomingSharesPanel.vue'

const PAGE_SIZE = 20
const ALL = 'all'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const canWrite = computed(() => auth.can('channels.write'))
const canManage = computed(() => auth.can('channels.manage'))

const page = computed(() => queryInt(route.query.page, 1))
const q = computed(() => queryStr(route.query.q))
const typeFilter = computed(() => queryStr(route.query.type) || ALL)
const scopeFilter = computed(() => queryStr(route.query.scope) || ALL)
const statusFilter = computed(() => queryStr(route.query.status) || ALL)
const search = ref(q.value)

// phase5-api.md §5.3: `?tab=shared` lists channels shared with me (invitations to accept).
type Tab = 'all' | 'shared'
const shares = useChannelSharesStore()
const tab = computed<Tab>(() => (queryStr(route.query.tab) === 'shared' ? 'shared' : 'all'))
function setTab(v: string | number) {
  void router.replace({ query: { ...route.query, tab: v === 'shared' ? 'shared' : undefined } })
}
void shares.load()
function refresh() {
  if (tab.value === 'shared')
    void shares.load()
  else
    void load()
}

const items = ref<ChannelView[]>([])
const total = ref(0)
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  if (tab.value === 'shared')
    return
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await channelsApi.list({
      page: page.value,
      pageSize: PAGE_SIZE,
      q: q.value || undefined,
      type: typeFilter.value === ALL ? undefined : typeFilter.value as ChannelType,
      scope: scopeFilter.value === ALL ? undefined : scopeFilter.value as ChannelScope,
      status: statusFilter.value === ALL ? undefined : statusFilter.value as EnabledStatus,
    }, ctrl.signal)
    items.value = res.items
    total.value = res.total
  }
  catch (err) {
    if (isAbortError(err))
      return
    loadError.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}

function setQuery(next: Record<string, string | number | undefined>) {
  const merged = { ...route.query, ...Object.fromEntries(Object.entries(next).map(([k, v]) => [k, v === undefined || v === '' || v === ALL || v === 1 ? undefined : String(v)])) }
  void router.replace({ query: merged })
}

watch([page, q, typeFilter, scopeFilter, statusFilter, tab], load, { immediate: true })
watch(q, (v) => {
  if (v !== search.value)
    search.value = v
})
watchDebounced(search, (v) => {
  const t = v.trim()
  if (t !== q.value)
    setQuery({ q: t, page: undefined })
}, { debounce: 300 })
onBeforeUnmount(() => controller?.abort())

const hasFilters = computed(() => !!q.value || typeFilter.value !== ALL || scopeFilter.value !== ALL || statusFilter.value !== ALL)

function replaceItem(c: ChannelView) {
  items.value = items.value.map(i => (i.id === c.id ? c : i))
}

// ---------- status toggle ----------
const toggling = ref<Set<string>>(new Set())
async function toggleStatus(c: Channel, enabled: boolean) {
  const status: EnabledStatus = enabled ? 'enabled' : 'disabled'
  if (status === c.status)
    return
  toggling.value = new Set(toggling.value).add(c.id)
  try {
    replaceItem(await channelsApi.update(c.id, { status, version: c.version }))
    toast.success(enabled ? `已启用「${c.name}」` : `已停用「${c.name}」`)
  }
  catch (err) {
    if (isVersionConflict(err)) {
      toast.warning('渠道已被他人修改，已刷新列表')
      await load()
    }
    else {
      toast.error('操作失败', { description: errorMessage(err) })
    }
  }
  finally {
    const next = new Set(toggling.value)
    next.delete(c.id)
    toggling.value = next
  }
}

// ---------- test connection ----------
const testing = ref<Set<string>>(new Set())
async function testChannel(c: Channel) {
  testing.value = new Set(testing.value).add(c.id)
  try {
    const res = await channelsApi.test(c.id)
    if (res.ok)
      toast.success(`「${c.name}」连接正常`, { description: `延迟 ${res.latencyMs} ms · HTTP ${res.statusCode}` })
    else
      toast.error(`「${c.name}」连接失败`, { description: `${res.error ?? '未知错误'}${res.statusCode ? `（HTTP ${res.statusCode}）` : ''} · ${res.latencyMs} ms` })
    // Test updates the health state: refresh the row.
    try {
      replaceItem(await channelsApi.get(c.id))
    }
    catch { /* row refresh is best-effort */ }
  }
  catch (err) {
    toast.error('测试失败', { description: errorMessage(err) })
  }
  finally {
    const next = new Set(testing.value)
    next.delete(c.id)
    testing.value = next
  }
}

// ---------- create / edit ----------
const sheetOpen = ref(false)
const editing = ref<Channel | null>(null)
function openCreate() {
  editing.value = null
  sheetOpen.value = true
}
async function openEdit(c: Channel) {
  // Fetch the latest copy so the version is fresh.
  try {
    const latest = await channelsApi.get(c.id)
    if (!isFullChannel(latest)) {
      toast.error('你没有管理该渠道的权限')
      return
    }
    replaceItem(latest)
    editing.value = latest
    sheetOpen.value = true
  }
  catch (err) {
    toast.error('无法加载渠道', { description: errorMessage(err) })
  }
}
// Deep link `?new=1` (e.g. "添加我的渠道" on 我的模型) opens the create sheet once.
watch(() => route.query.new, (v) => {
  if (queryStr(v) !== '1')
    return
  const rest = { ...route.query }
  delete rest.new
  void router.replace({ query: rest })
  if (canWrite.value)
    openCreate()
}, { immediate: true })

function onSaved(c: Channel, created: boolean) {
  if (created)
    void load()
  else
    replaceItem(c)
}

// ---------- delete ----------
const deleting = ref<Channel | null>(null)
const deleteOpen = ref(false)
const deleteBusy = ref(false)
function askDelete(c: Channel) {
  deleting.value = c
  deleteOpen.value = true
}
async function confirmDelete() {
  const c = deleting.value
  if (!c)
    return
  deleteBusy.value = true
  try {
    await channelsApi.remove(c.id)
    toast.success(`已删除渠道「${c.name}」`)
    deleteOpen.value = false
    if (items.value.length === 1 && page.value > 1)
      setQuery({ page: page.value - 1 })
    else
      await load()
  }
  catch (err) {
    deleteOpen.value = false
    toast.error('删除失败', { description: isApiError(err) && err.status === 404 ? '渠道不存在或已被删除' : errorMessage(err) })
    await load()
  }
  finally {
    deleteBusy.value = false
  }
}

const HEALTH_CLASSES: Record<HealthState, string> = {
  healthy: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  degraded: 'border-amber-500/50 text-amber-700 dark:text-amber-400',
  open: 'border-destructive/50 text-destructive',
}
const HEALTH_DOT: Record<HealthState, string> = {
  healthy: 'bg-emerald-500',
  degraded: 'bg-amber-500',
  open: 'bg-destructive',
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="渠道" description="上游服务端点与凭据。你可以看到自己创建的、共享给你的以及全局渠道。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="tab === 'shared' ? shares.loading : loading" @click="refresh">
          <RefreshCw :class="(tab === 'shared' ? shares.loading : loading) ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button v-if="canWrite" size="sm" @click="openCreate">
          <Plus />
          新建渠道
        </Button>
      </template>
    </PageHeader>

    <Tabs :model-value="tab" @update:model-value="setTab">
      <TabsList v-if="shares.available || tab === 'shared'">
        <TabsTrigger value="all" data-testid="channels-tab-all">
          渠道列表
        </TabsTrigger>
        <TabsTrigger value="shared" data-testid="channels-tab-shared">
          共享给我的
          <span v-if="shares.pending" class="bg-destructive ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] font-semibold text-white tabular-nums" :aria-label="`${shares.pending} 个待接受的邀请`" data-testid="channels-shared-pending">{{ shares.pending }}</span>
        </TabsTrigger>
      </TabsList>
      <TabsContent value="shared" class="pt-2">
        <IncomingSharesPanel />
      </TabsContent>
      <TabsContent value="all" class="pt-2">
        <Card>
          <CardContent class="space-y-4">
            <div class="flex flex-col gap-2 lg:flex-row lg:items-center">
              <div class="relative w-full lg:max-w-xs">
                <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input v-model="search" placeholder="搜索名称或模型" class="pl-8" aria-label="搜索渠道" />
              </div>
              <div class="grid grid-cols-3 gap-2 lg:flex">
                <Select :model-value="typeFilter" @update:model-value="(v) => setQuery({ type: String(v), page: undefined })">
                  <SelectTrigger class="w-full lg:w-36" aria-label="按类型过滤">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="ALL">
                      全部类型
                    </SelectItem>
                    <SelectItem v-for="(label, t) in CHANNEL_TYPE_LABELS" :key="t" :value="t">
                      {{ label }}
                    </SelectItem>
                  </SelectContent>
                </Select>
                <Select :model-value="scopeFilter" @update:model-value="(v) => setQuery({ scope: String(v), page: undefined })">
                  <SelectTrigger class="w-full lg:w-32" aria-label="按范围过滤">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="ALL">
                      全部范围
                    </SelectItem>
                    <SelectItem v-for="(label, s) in SCOPE_LABELS" :key="s" :value="s">
                      {{ label }}
                    </SelectItem>
                  </SelectContent>
                </Select>
                <Select :model-value="statusFilter" @update:model-value="(v) => setQuery({ status: String(v), page: undefined })">
                  <SelectTrigger class="w-full lg:w-32" aria-label="按状态过滤">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem :value="ALL">
                      全部状态
                    </SelectItem>
                    <SelectItem value="enabled">
                      已启用
                    </SelectItem>
                    <SelectItem value="disabled">
                      已停用
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>

            <ErrorState v-if="loadError" :error="loadError" @retry="load" />

            <div v-else-if="loading && items.length === 0" class="space-y-2">
              <Skeleton v-for="i in 5" :key="i" class="h-12 w-full" />
            </div>

            <EmptyState
              v-else-if="items.length === 0"
              :icon="Cable"
              :title="hasFilters ? '没有匹配的渠道' : '还没有渠道'"
              :description="hasFilters ? '调整搜索或过滤条件后重试。' : '添加一个上游渠道（OpenAI 兼容或 Anthropic）后，即可通过网关调用其模型。'"
            >
              <Button v-if="!hasFilters && canWrite" size="sm" @click="openCreate">
                <Plus />
                新建渠道
              </Button>
            </EmptyState>

            <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>名称</TableHead>
                    <TableHead>类型</TableHead>
                    <TableHead>范围</TableHead>
                    <TableHead>启用</TableHead>
                    <TableHead>模型</TableHead>
                    <TableHead>健康</TableHead>
                    <TableHead class="hidden md:table-cell">
                      优先级 / 权重
                    </TableHead>
                    <TableHead class="hidden lg:table-cell">
                      所有者
                    </TableHead>
                    <TableHead class="w-10">
                      <span class="sr-only">操作</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="c in items" :key="c.id">
                    <TableCell>
                      <div class="min-w-36">
                        <div class="flex flex-wrap items-center gap-1.5">
                          <RouterLink :to="{ name: 'channel-detail', params: { id: c.id } }" class="truncate font-medium hover:underline">
                            {{ c.name }}
                          </RouterLink>
                          <ChannelBadges v-if="isFullChannel(c) && c.badges?.length" :badges="c.badges" />
                        </div>
                        <p v-if="isFullChannel(c)" class="text-muted-foreground max-w-56 truncate font-mono text-xs" :title="c.baseUrl">
                          {{ c.baseUrl }}
                        </p>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">
                        {{ CHANNEL_TYPE_LABELS[c.type] ?? c.type }}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Badge :variant="c.scope === 'global' ? 'default' : 'secondary'">
                        {{ SCOPE_LABELS[c.scope] ?? c.scope }}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div class="flex items-center gap-2">
                        <template v-if="isFullChannel(c)">
                          <Switch
                            :model-value="c.status === 'enabled'"
                            :disabled="toggling.has(c.id)"
                            :aria-label="`${c.name} 是否启用`"
                            @update:model-value="(v: boolean) => toggleStatus(c, v)"
                          />
                        </template>
                        <span class="text-xs" :class="c.status === 'enabled' ? 'text-muted-foreground' : 'text-destructive'">
                          {{ c.status === 'enabled' ? '启用' : '停用' }}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Popover>
                        <PopoverTrigger as-child>
                          <Button variant="ghost" size="xs" class="tabular-nums" :disabled="!c.models?.length">
                            {{ c.models?.length ?? 0 }} 个
                          </Button>
                        </PopoverTrigger>
                        <PopoverContent align="start" class="w-80 gap-2 p-3">
                          <p class="text-xs font-medium">
                            模型映射（逻辑名 → 上游）
                          </p>
                          <ul class="max-h-64 space-y-1 overflow-y-auto font-mono text-xs">
                            <li v-for="m in c.models ?? []" :key="m.model" class="flex gap-1.5">
                              <span class="truncate">{{ m.model }}</span>
                              <template v-if="m.upstreamModel && m.upstreamModel !== m.model">
                                <span class="text-muted-foreground">→</span>
                                <span class="text-muted-foreground truncate">{{ m.upstreamModel }}</span>
                              </template>
                            </li>
                          </ul>
                        </PopoverContent>
                      </Popover>
                    </TableCell>
                    <TableCell>
                      <Tooltip>
                        <TooltipTrigger as-child>
                          <Badge variant="outline" :class="HEALTH_CLASSES[c.health.state]" tabindex="0">
                            <span class="inline-block size-1.5 rounded-full" :class="HEALTH_DOT[c.health.state]" />
                            {{ HEALTH_LABELS[c.health.state] ?? c.health.state }}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent class="flex-col items-start">
                          <p>连续失败 {{ c.health.consecutiveFailures }} 次</p>
                          <p v-if="c.health.lastCheckedAt">
                            最近检查：{{ formatRelative(c.health.lastCheckedAt) }}
                          </p>
                          <p v-if="c.health.lastError" class="max-w-64 break-all">
                            最近错误：{{ c.health.lastError }}
                          </p>
                          <p v-if="!c.health.lastCheckedAt && !c.health.lastError">
                            尚无检查记录（健康状态在进程内维护，重启后重置）
                          </p>
                        </TooltipContent>
                      </Tooltip>
                    </TableCell>
                    <TableCell class="hidden tabular-nums md:table-cell">
                      <template v-if="isFullChannel(c)">
                        {{ c.priority }} / {{ c.weight }}
                      </template>
                      <span v-else class="text-muted-foreground">—</span>
                    </TableCell>
                    <TableCell class="hidden lg:table-cell">
                      <span v-if="isFullChannel(c)" :title="`创建于 ${formatDateTime(c.createdAt)}`">
                        {{ c.owner.displayName }}<span v-if="c.owner.id === auth.user?.id" class="text-muted-foreground">（我）</span>
                      </span>
                      <span v-else class="text-muted-foreground text-xs">仅可使用</span>
                    </TableCell>
                    <TableCell>
                      <DropdownMenu v-if="isFullChannel(c)">
                        <DropdownMenuTrigger as-child>
                          <Button variant="ghost" size="icon-sm" :aria-label="`${c.name} 的操作`">
                            <Loader2 v-if="testing.has(c.id)" class="animate-spin" />
                            <MoreHorizontal v-else />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem :disabled="testing.has(c.id)" @select="testChannel(c)">
                            <Zap />
                            测试连接
                          </DropdownMenuItem>
                          <DropdownMenuItem @select="router.push({ name: 'channel-detail', params: { id: c.id } })">
                            <Eye />
                            详情
                          </DropdownMenuItem>
                          <DropdownMenuItem @select="openEdit(c)">
                            <Pencil />
                            编辑
                          </DropdownMenuItem>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem variant="destructive" @select="askDelete(c)">
                            <Trash2 />
                            删除
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableCell>
                  </TableRow>
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
      </TabsContent>
    </Tabs>

    <ChannelFormSheet v-model:open="sheetOpen" :channel="editing" :can-manage="canManage" @saved="onSaved" />

    <ConfirmDialog
      v-model:open="deleteOpen"
      :title="`删除渠道「${deleting?.name ?? ''}」？`"
      confirm-text="删除"
      destructive
      :loading="deleteBusy"
      @confirm="confirmDelete"
    >
      <p>删除后，该渠道立即停止参与路由，其凭据会被一并删除，此操作无法撤销。</p>
      <p>已有的请求日志会保留渠道名称快照，统计数据不受影响。</p>
    </ConfirmDialog>
  </div>
</template>
