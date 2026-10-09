<script setup lang="ts">
import type { RouteFormState } from '@/lib/routeForm'
import type { Channel, ChannelView, RouteRule } from '@/lib/types'
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowDown, ArrowUp, Copy, GripVertical, MoreHorizontal, Pencil, Plus, RefreshCw, Route as RouteIcon, Trash2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { errorMessage, isApiError, isVersionConflict } from '@/lib/api'
import { fetchAllChannels, modelsApi, routesApi } from '@/lib/endpoints'
import { ROLE_LABELS } from '@/lib/format'
import { isAbortError } from '@/lib/query'
import { retrySummary } from '@/lib/retry'
import { duplicateForm, isGlob, moveItem, sortRules, STRATEGY_LABELS, targetsSummary } from '@/lib/routeForm'
import { isFullChannel } from '@/lib/types'
import { useAuthStore } from '@/stores/auth'
import RouteFormSheet from './RouteFormSheet.vue'
import RoutePreviewPanel from './RoutePreviewPanel.vue'

const auth = useAuthStore()

// ---------- data ----------
const rules = ref<RouteRule[]>([])
const loading = ref(false)
const loaded = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    rules.value = sortRules((await routesApi.list(ctrl.signal)).items)
    loaded.value = true
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

const channels = ref<ChannelView[]>([])
const models = ref<string[]>([])
const modelsLoading = ref(false)
async function loadRefs() {
  modelsLoading.value = true
  const [ch, md] = await Promise.allSettled([
    fetchAllChannels(),
    auth.can('models.manage') ? modelsApi.listAll() : modelsApi.list(),
  ])
  if (ch.status === 'fulfilled')
    channels.value = ch.value
  if (md.status === 'fulfilled')
    models.value = md.value.items.map(m => m.model).sort()
  modelsLoading.value = false
}

onMounted(() => {
  void load()
  void loadRefs()
})
onBeforeUnmount(() => controller?.abort())

const manageable = computed<Channel[]>(() => channels.value.filter(isFullChannel))
const channelName = computed(() => {
  const m = new Map(channels.value.map(c => [c.id, c.name]))
  return (id: string) => m.get(id)
})

// ---------- create / edit / duplicate ----------
const sheetOpen = ref(false)
const editing = ref<RouteRule | null>(null)
const draft = ref<RouteFormState | null>(null)
function openCreate() {
  editing.value = null
  draft.value = null
  sheetOpen.value = true
}
function openEdit(r: RouteRule) {
  editing.value = r
  draft.value = null
  sheetOpen.value = true
}
function openDuplicate(r: RouteRule) {
  editing.value = null
  draft.value = duplicateForm(r)
  sheetOpen.value = true
}
function openById(id: string) {
  const r = rules.value.find(x => x.id === id)
  if (r)
    openEdit(r)
}
function onSaved(r: RouteRule, created: boolean) {
  if (created)
    rules.value = [...rules.value, r]
  else
    rules.value = rules.value.map(x => (x.id === r.id ? r : x))
  void load()
}

// ---------- enable ----------
const toggling = ref(new Set<string>())
async function setEnabled(r: RouteRule, enabled: boolean) {
  toggling.value = new Set(toggling.value).add(r.id)
  try {
    const updated = await routesApi.update(r.id, { enabled, version: r.version })
    rules.value = rules.value.map(x => (x.id === r.id ? updated : x))
    toast.success(enabled ? `已启用「${r.name}」` : `已停用「${r.name}」`)
  }
  catch (err) {
    if (isVersionConflict(err)) {
      toast.warning('规则已被他人修改，已刷新列表，请重试')
      void load()
    }
    else {
      toast.error('操作失败', { description: errorMessage(err) })
    }
  }
  finally {
    const next = new Set(toggling.value)
    next.delete(r.id)
    toggling.value = next
  }
}

// ---------- reorder ----------
const reordering = ref(false)
async function applyOrder(next: RouteRule[], focusKey?: string) {
  const prev = rules.value
  rules.value = next
  if (focusKey) {
    void nextTick(() => {
      const el = document.querySelector<HTMLButtonElement>(`[data-move="${focusKey}"]`)
      if (el && !el.disabled)
        el.focus()
      else
        document.querySelector<HTMLElement>(`[data-rule-row="${focusKey.split(':')[1]}"] [data-move]:not([disabled])`)?.focus()
    })
  }
  reordering.value = true
  try {
    await routesApi.reorder(next.map(r => r.id))
    toast.success('规则顺序已保存')
    void load()
  }
  catch (err) {
    rules.value = prev
    if (isApiError(err) && (err.status === 409 || err.status === 422)) {
      toast.warning('规则列表已被他人修改，已刷新，请重新调整顺序')
      void load()
    }
    else {
      toast.error('保存顺序失败', { description: errorMessage(err) })
    }
  }
  finally {
    reordering.value = false
  }
}
function move(i: number, delta: number) {
  const r = rules.value[i]
  if (!r || reordering.value)
    return
  const to = i + delta
  if (to < 0 || to >= rules.value.length)
    return
  void applyOrder(moveItem(rules.value, i, to), `${delta < 0 ? 'up' : 'down'}:${r.id}`)
}

// Native drag and drop (mouse); the arrow buttons are the keyboard / touch path.
const dragFrom = ref<number | null>(null)
const dragOver = ref<number | null>(null)
function onDragStart(i: number, e: DragEvent) {
  if (reordering.value) {
    e.preventDefault()
    return
  }
  dragFrom.value = i
  e.dataTransfer?.setData('text/plain', String(i))
  if (e.dataTransfer)
    e.dataTransfer.effectAllowed = 'move'
}
function onDragOver(i: number, e: DragEvent) {
  if (dragFrom.value === null)
    return
  e.preventDefault()
  dragOver.value = i
}
function onDrop(i: number) {
  const from = dragFrom.value
  dragFrom.value = null
  dragOver.value = null
  if (from === null || from === i)
    return
  void applyOrder(moveItem(rules.value, from, i))
}
function onDragEnd() {
  dragFrom.value = null
  dragOver.value = null
}

// ---------- delete ----------
const deleteTarget = ref<RouteRule | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)
function requestDelete(r: RouteRule) {
  deleteTarget.value = r
  deleteOpen.value = true
}
async function applyDelete() {
  const r = deleteTarget.value
  if (!r)
    return
  deleting.value = true
  try {
    await routesApi.remove(r.id)
    rules.value = rules.value.filter(x => x.id !== r.id)
    toast.success(`已删除「${r.name}」`)
    deleteOpen.value = false
    void load()
  }
  catch (err) {
    deleteOpen.value = false
    toast.error('删除失败', { description: errorMessage(err) })
  }
  finally {
    deleting.value = false
  }
}

// ---------- display ----------
function rolesText(r: RouteRule): string {
  return r.match.roles.length ? r.match.roles.map(x => ROLE_LABELS[x] ?? x).join('、') : '所有角色'
}
function retryText(r: RouteRule): string {
  if (r.retry.maxAttempts <= 1)
    return '不重试'
  return retrySummary(r.retry.retryOn ?? [])
}
const STRATEGY_CLASSES: Record<string, string> = {
  priority: 'border-sky-500/40 text-sky-700 dark:text-sky-400',
  weighted: 'border-violet-500/40 text-violet-700 dark:text-violet-400',
  round_robin: 'border-teal-500/40 text-teal-700 dark:text-teal-400',
  least_latency: 'border-amber-500/40 text-amber-700 dark:text-amber-400',
  lowest_cost: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="路由" description="（管理员）用规则改变特定模型的渠道选择方式、重试与回退。">
      <template #actions>
        <Button size="sm" @click="openCreate">
          <Plus />
          新建规则
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="grid gap-4 text-sm lg:grid-cols-2">
        <div class="space-y-2">
          <p class="font-medium">
            默认路由
          </p>
          <ol class="flex flex-wrap items-center gap-1.5 text-xs" aria-label="默认路由的排序依据">
            <li>
              <Badge variant="secondary">
                ① 渠道优先级（高者先）
              </Badge>
            </li>
            <li class="text-muted-foreground" aria-hidden="true">
              →
            </li>
            <li>
              <Badge variant="secondary">
                ② 协议匹配度（无需转换者先）
              </Badge>
            </li>
            <li class="text-muted-foreground" aria-hidden="true">
              →
            </li>
            <li>
              <Badge variant="secondary">
                ③ 权重随机
              </Badge>
            </li>
          </ol>
          <p class="text-muted-foreground text-xs">
            没有规则命中时，网关在用户可用的渠道中按上述顺序选择；失败（限流、5xx、超时、网络错误）时换下一个渠道重试，次数取系统设置中的「默认最多尝试次数」；熔断中的渠道会被跳过。
          </p>
        </div>
        <div class="space-y-2">
          <p class="font-medium">
            路由规则
          </p>
          <p class="text-muted-foreground text-xs">
            规则对匹配的模型（和用户角色）覆盖默认路由：可以限定目标渠道、改用其他策略、调整重试次数与可重试的失败类型，并在全部失败后回退到其他模型。
            规则<strong class="text-foreground">自上而下</strong>匹配，<strong class="text-foreground">第一条命中的规则生效</strong>，可拖动或用箭头按钮调整顺序。规则只会缩小候选渠道，不会让用户使用本来无权使用的渠道。
          </p>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader class="flex flex-row items-center justify-between gap-2">
        <div class="space-y-1">
          <CardTitle class="text-base">
            规则
          </CardTitle>
          <CardDescription>共 {{ rules.length }} 条，{{ rules.filter(r => r.enabled).length }} 条启用</CardDescription>
        </div>
        <Button variant="ghost" size="icon-sm" aria-label="刷新" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
        </Button>
      </CardHeader>
      <CardContent>
        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="!loaded" class="space-y-2">
          <Skeleton v-for="i in 3" :key="i" class="h-14 w-full" />
        </div>
        <EmptyState
          v-else-if="rules.length === 0"
          :icon="RouteIcon"
          title="还没有路由规则"
          description="所有请求都使用默认路由。创建规则，例如让 gpt-* 优先走低成本渠道，或在 claude 失败时回退到其他模型。"
        >
          <Button size="sm" @click="openCreate">
            <Plus />
            新建规则
          </Button>
        </EmptyState>
        <div v-else class="overflow-x-auto" :class="reordering ? 'opacity-70 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-14 sm:w-24">
                  顺序
                </TableHead>
                <TableHead>名称</TableHead>
                <TableHead class="hidden md:table-cell">
                  匹配
                </TableHead>
                <TableHead class="hidden sm:table-cell">
                  策略
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  目标渠道
                </TableHead>
                <TableHead class="hidden xl:table-cell">
                  重试
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  回退模型
                </TableHead>
                <TableHead>启用</TableHead>
                <TableHead class="w-10">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="(r, i) in rules"
                :key="r.id"
                :data-rule-row="r.id"
                draggable="true"
                :class="[
                  r.enabled ? '' : 'text-muted-foreground',
                  dragOver === i && dragFrom !== null && dragFrom !== i ? (dragFrom > i ? 'shadow-[inset_0_2px_0_var(--primary)]' : 'shadow-[inset_0_-2px_0_var(--primary)]') : '',
                  dragFrom === i ? 'opacity-50' : '',
                ]"
                @dragstart="onDragStart(i, $event)"
                @dragover="onDragOver(i, $event)"
                @drop.prevent="onDrop(i)"
                @dragend="onDragEnd"
              >
                <TableCell>
                  <div class="flex items-center gap-0.5">
                    <GripVertical class="text-muted-foreground hidden size-4 shrink-0 cursor-grab sm:block" aria-hidden="true" />
                    <span class="hidden w-5 text-center text-xs tabular-nums sm:inline">{{ i + 1 }}</span>
                    <Button variant="ghost" size="icon-xs" :data-move="`up:${r.id}`" :disabled="i === 0" :aria-label="`上移「${r.name}」`" @click="move(i, -1)">
                      <ArrowUp />
                    </Button>
                    <Button variant="ghost" size="icon-xs" :data-move="`down:${r.id}`" :disabled="i === rules.length - 1" :aria-label="`下移「${r.name}」`" @click="move(i, 1)">
                      <ArrowDown />
                    </Button>
                  </div>
                </TableCell>
                <TableCell class="max-w-64 min-w-28 whitespace-normal sm:min-w-40">
                  <button type="button" class="hover:text-primary block max-w-full truncate text-left font-medium hover:underline" :title="r.name" @click="openEdit(r)">
                    {{ r.name }}
                  </button>
                  <p v-if="r.description" class="text-muted-foreground truncate text-xs" :title="r.description">
                    {{ r.description }}
                  </p>
                  <div class="mt-1 flex flex-wrap gap-1 md:hidden">
                    <Badge v-for="m in r.match.models.slice(0, 3)" :key="m" variant="outline" class="h-4 px-1 font-mono text-[10px]">
                      {{ m }}
                    </Badge>
                    <span v-if="r.match.models.length > 3" class="text-muted-foreground text-[10px]">+{{ r.match.models.length - 3 }}</span>
                    <Badge variant="outline" class="h-4 px-1 text-[10px] sm:hidden" :class="STRATEGY_CLASSES[r.strategy]">
                      {{ STRATEGY_LABELS[r.strategy] ?? r.strategy }}
                    </Badge>
                  </div>
                </TableCell>
                <TableCell class="hidden max-w-64 md:table-cell">
                  <div class="flex flex-wrap gap-1" :title="r.match.models.join('\n')">
                    <Badge v-for="m in r.match.models.slice(0, 4)" :key="m" variant="secondary" class="h-5 max-w-40 font-mono text-[11px]" :class="isGlob(m) ? 'text-violet-700 dark:text-violet-400' : ''">
                      <span class="truncate">{{ m }}</span>
                    </Badge>
                    <span v-if="r.match.models.length > 4" class="text-muted-foreground self-center text-xs">+{{ r.match.models.length - 4 }}</span>
                  </div>
                  <p class="text-muted-foreground mt-1 truncate text-xs">
                    {{ rolesText(r) }}
                  </p>
                </TableCell>
                <TableCell class="hidden sm:table-cell">
                  <Badge variant="outline" :class="STRATEGY_CLASSES[r.strategy]">
                    {{ STRATEGY_LABELS[r.strategy] ?? r.strategy }}
                  </Badge>
                  <p v-if="r.protocolPreference === 'ignore'" class="text-muted-foreground mt-1 text-[11px]">
                    不考虑协议
                  </p>
                </TableCell>
                <TableCell class="hidden max-w-48 text-xs lg:table-cell">
                  <span class="block truncate" :title="r.targets.map(t => channelName(t.channelId) ?? t.channelId).join('\n') || '全部可用渠道'">
                    {{ targetsSummary(r, channelName) }}
                  </span>
                </TableCell>
                <TableCell class="hidden text-xs xl:table-cell">
                  <p class="tabular-nums">
                    最多 {{ r.retry.maxAttempts }} 次
                  </p>
                  <p class="text-muted-foreground max-w-40 truncate" :title="retryText(r)">
                    {{ retryText(r) }}
                  </p>
                </TableCell>
                <TableCell class="hidden max-w-48 text-xs lg:table-cell">
                  <span v-if="r.fallbackModels.length" class="block truncate font-mono" :title="r.fallbackModels.join(' → ')">
                    {{ r.fallbackModels.join(' → ') }}
                  </span>
                  <span v-else class="text-muted-foreground">—</span>
                </TableCell>
                <TableCell>
                  <Switch
                    :model-value="r.enabled"
                    :disabled="toggling.has(r.id)"
                    :aria-label="`启用「${r.name}」`"
                    @update:model-value="(v: boolean) => setEnabled(r, v)"
                  />
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon-sm" :aria-label="`${r.name} 的操作`">
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem @select="openEdit(r)">
                        <Pencil />
                        编辑
                      </DropdownMenuItem>
                      <DropdownMenuItem @select="openDuplicate(r)">
                        <Copy />
                        复制
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" @select="requestDelete(r)">
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
      </CardContent>
    </Card>

    <RoutePreviewPanel :models="models" @open-rule="openById" />

    <RouteFormSheet
      v-model:open="sheetOpen"
      :rule="editing"
      :draft="draft"
      :channels="manageable"
      :models="models"
      :models-loading="modelsLoading"
      @saved="onSaved"
    />

    <ConfirmDialog
      v-model:open="deleteOpen"
      :title="`删除路由规则「${deleteTarget?.name}」？`"
      confirm-text="删除"
      destructive
      :loading="deleting"
      @confirm="applyDelete"
    >
      <p>删除后，原本命中该规则的请求会继续向下匹配其他规则，或使用默认路由。此操作不可撤销。</p>
    </ConfirmDialog>
  </div>
</template>
