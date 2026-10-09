<script setup lang="ts">
import type { Plan, Subscription, SubscriptionStatus, User } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Ban, CalendarClock, CalendarPlus, MoreHorizontal, Plus, RefreshCw, RotateCcw, UserRound, X } from '@lucide/vue'
import { useIntervalFn, useNow } from '@vueuse/core'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import FormField from '@/components/FormField.vue'
import PageHeader from '@/components/PageHeader.vue'
import RuleUsageItem from '@/components/quota/RuleUsageItem.vue'
import SubscriptionStatusBadge from '@/components/quota/SubscriptionStatusBadge.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage, isApiError } from '@/lib/api'
import { fetchAllPlans, plansApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { formatRemaining, PLAN_ERROR_MESSAGES, PLAN_STATUS_LABELS, SUBSCRIPTION_SOURCE_LABELS } from '@/lib/quota'
import ExtendDialog from './ExtendDialog.vue'
import GrantDialog from './GrantDialog.vue'
import ResetQuotaDialog from './ResetQuotaDialog.vue'

const PAGE_SIZE = 20
const ALL = 'all'

const route = useRoute()
const router = useRouter()
const now = useNow({ scheduler: cb => useIntervalFn(cb, 30_000) })

const page = computed(() => queryInt(route.query.page, 1))
const status = computed(() => {
  const s = queryStr(route.query.status)
  return s === 'active' || s === 'expired' || s === 'cancelled' ? s : ALL
})
const planId = computed(() => queryStr(route.query.planId) || ALL)
const userId = computed(() => queryStr(route.query.userId))

function setQuery(next: Record<string, string | number | undefined>) {
  const q: Record<string, unknown> = { ...route.query }
  for (const [k, v] of Object.entries(next))
    q[k] = v === undefined || v === '' || v === ALL || (k === 'page' && v === 1) ? undefined : String(v)
  void router.replace({ query: q as Record<string, string> })
}

// ---------- plans (filter + grant) ----------
const plans = ref<Plan[]>([])
const plansLoading = ref(false)
async function loadPlans() {
  plansLoading.value = true
  try {
    plans.value = await fetchAllPlans()
  }
  catch (err) {
    toast.error('无法加载套餐列表', { description: errorMessage(err) })
  }
  finally {
    plansLoading.value = false
  }
}
onMounted(loadPlans)
const activePlans = computed(() => plans.value.filter(p => p.status === 'active'))

// ---------- list ----------
const items = ref<Subscription[]>([])
const total = ref(0)
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null
async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await plansApi.listSubscriptions({
      page: page.value,
      pageSize: PAGE_SIZE,
      status: status.value === ALL ? undefined : status.value as SubscriptionStatus,
      planId: planId.value === ALL ? undefined : planId.value,
      userId: userId.value || undefined,
    }, ctrl.signal)
    items.value = res.items
    total.value = res.total
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
watch(() => [page.value, status.value, planId.value, userId.value], load, { immediate: true })
onBeforeUnmount(() => controller?.abort())

// ---------- user filter ----------
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
const userFilterLabel = computed(() => {
  if (!userId.value)
    return '全部用户'
  return userLabel.value ?? items.value.find(s => s.user.id === userId.value)?.user.displayName ?? `${userId.value.slice(0, 8)}…`
})

function filterUser(s: Subscription) {
  userLabel.value = s.user.displayName
  userPick.value = [s.user.id]
}

const hasFilters = computed(() => status.value !== ALL || planId.value !== ALL || !!userId.value)
function clearFilters() {
  setQuery({ status: undefined, planId: undefined, userId: undefined, page: undefined })
}

// ---------- grant ----------
const grantOpen = ref(false)
function onGranted(s: Subscription, renewed: boolean) {
  toast.success(renewed ? '已续期' : '已开通', {
    description: `${s.user.displayName} 的「${s.plan.name}」有效期至 ${formatDateTime(s.endsAt)}`,
  })
  void loadPlans()
  if (page.value !== 1)
    setQuery({ page: undefined })
  else
    void load()
}

// ---------- cancel ----------
const cancelTarget = ref<Subscription | null>(null)
const cancelOpen = ref(false)
const cancelNote = ref('')
const cancelSaving = ref(false)
const cancelError = ref<string | null>(null)
function requestCancel(s: Subscription) {
  cancelTarget.value = s
  cancelNote.value = ''
  cancelError.value = null
  cancelOpen.value = true
}
async function applyCancel() {
  const s = cancelTarget.value
  if (!s)
    return
  if (cancelNote.value.trim().length > 500) {
    cancelError.value = '备注最多 500 个字符'
    return
  }
  cancelSaving.value = true
  try {
    const updated = await plansApi.cancel(s.id, cancelNote.value.trim() || undefined)
    toast.success(`已取消 ${s.user.displayName} 的「${s.plan.name}」`)
    cancelOpen.value = false
    items.value = items.value.map(x => (x.id === s.id ? updated : x))
    void loadPlans()
  }
  catch (err) {
    if (isApiError(err) && err.code === 'subscription_not_active') {
      cancelOpen.value = false
      toast.warning(PLAN_ERROR_MESSAGES.subscription_not_active!)
      void load()
    }
    else {
      cancelError.value = errorMessage(err)
    }
  }
  finally {
    cancelSaving.value = false
  }
}

// ---------- selection & bulk actions (phase7 §3) ----------
/** Selected subscriptions (kept across pages so the dialogs still know their rules). */
const selected = ref<Map<string, Subscription>>(new Map())
watch(() => [status.value, planId.value, userId.value], () => (selected.value = new Map()))
// Keep the selection in sync with reloaded rows (fresh usage; no longer active → dropped).
watch(items, (list) => {
  if (selected.value.size === 0)
    return
  const next = new Map(selected.value)
  for (const s of list) {
    if (!next.has(s.id))
      continue
    if (s.status === 'active')
      next.set(s.id, s)
    else
      next.delete(s.id)
  }
  selected.value = next
})
const selectable = computed(() => items.value.filter(s => s.status === 'active'))
const selectedList = computed(() => [...selected.value.values()])
const pageChecked = computed<boolean | 'indeterminate'>(() => {
  const rows = selectable.value
  const n = rows.filter(s => selected.value.has(s.id)).length
  return n === 0 ? false : n === rows.length ? true : 'indeterminate'
})
function togglePage(v: boolean | 'indeterminate') {
  const next = new Map(selected.value)
  for (const s of selectable.value) {
    if (v === true)
      next.set(s.id, s)
    else
      next.delete(s.id)
  }
  selected.value = next
}
function toggleRow(s: Subscription, v: boolean | 'indeterminate') {
  const next = new Map(selected.value)
  if (v === true)
    next.set(s.id, s)
  else
    next.delete(s.id)
  selected.value = next
}

const resetOpen = ref(false)
const extendOpen = ref(false)
/** Row action target; null = toolbar / bulk bar (selected rows or by plan). */
const single = ref<Subscription | null>(null)
function openReset(s: Subscription | null = null) {
  single.value = s
  resetOpen.value = true
}
function openExtend(s: Subscription | null = null) {
  single.value = s
  extendOpen.value = true
}
function onBulkDone() {
  if (!single.value)
    selected.value = new Map()
  void load()
  void loadPlans()
}

function planLabel(id: string): string {
  const p = plans.value.find(x => x.id === id)
  return p ? `${p.name}${p.status === 'archived' ? `（${PLAN_STATUS_LABELS.archived}）` : ''}` : `${id.slice(0, 8)}…`
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="订阅管理" description="（管理员）查看所有用户的套餐订阅与当前用量，开通、续期、取消订阅，或批量重置额度、延长有效期。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button variant="outline" size="sm" data-testid="toolbar-reset" @click="openReset()">
          <RotateCcw />
          重置额度
        </Button>
        <Button variant="outline" size="sm" data-testid="toolbar-extend" @click="openExtend()">
          <CalendarPlus />
          延长有效期
        </Button>
        <Button size="sm" @click="grantOpen = true">
          <Plus />
          开通 / 续期
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <div class="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center">
          <Select :model-value="status" @update:model-value="(v) => setQuery({ status: String(v), page: undefined })">
            <SelectTrigger class="w-full sm:w-32" aria-label="按状态过滤">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ALL">
                全部状态
              </SelectItem>
              <SelectItem value="active">
                有效
              </SelectItem>
              <SelectItem value="expired">
                已过期
              </SelectItem>
              <SelectItem value="cancelled">
                已取消
              </SelectItem>
            </SelectContent>
          </Select>
          <Select :model-value="planId" @update:model-value="(v) => setQuery({ planId: String(v), page: undefined })">
            <SelectTrigger class="w-full sm:w-48" aria-label="按套餐过滤">
              <SelectValue>{{ planId === ALL ? '全部套餐' : planLabel(planId) }}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ALL">
                全部套餐
              </SelectItem>
              <SelectItem v-for="p in plans" :key="p.id" :value="p.id">
                {{ p.name }}{{ p.status === 'archived' ? '（已下架）' : '' }}
              </SelectItem>
              <SelectItem v-if="planId !== ALL && !plans.some(p => p.id === planId)" :value="planId">
                {{ planId.slice(0, 8) }}…
              </SelectItem>
            </SelectContent>
          </Select>
          <Popover v-model:open="userPopover">
            <PopoverTrigger as-child>
              <Button variant="outline" class="col-span-2 w-full justify-start font-normal sm:w-auto">
                <UserRound />
                <span class="truncate">{{ userFilterLabel }}</span>
              </Button>
            </PopoverTrigger>
            <PopoverContent align="start" class="w-80 gap-2">
              <Label>按用户过滤</Label>
              <UserPicker v-model="userPick" :multiple="false" @pick="onUserPicked" />
            </PopoverContent>
          </Popover>
          <Button v-if="hasFilters" variant="ghost" size="sm" class="justify-self-start" @click="clearFilters">
            <X />
            清除过滤
          </Button>
        </div>

        <div
          v-if="selected.size > 0"
          class="bg-muted/50 flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2"
          role="toolbar"
          aria-label="批量操作"
          data-testid="sub-bulk-bar"
        >
          <span class="mr-auto text-sm">已选 <strong class="tabular-nums">{{ selected.size }}</strong> 份有效订阅</span>
          <Button variant="outline" size="sm" @click="openReset()">
            <RotateCcw />
            重置额度
          </Button>
          <Button variant="outline" size="sm" @click="openExtend()">
            <CalendarPlus />
            延长有效期
          </Button>
          <Button variant="ghost" size="sm" @click="selected = new Map()">
            <X />
            取消选择
          </Button>
        </div>

        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="loading && items.length === 0" class="space-y-2">
          <Skeleton v-for="i in 5" :key="i" class="h-14 w-full" />
        </div>
        <EmptyState
          v-else-if="items.length === 0"
          :icon="CalendarClock"
          :title="hasFilters ? '没有符合条件的订阅' : '还没有订阅'"
          :description="hasFilters ? undefined : '为用户开通套餐，或生成套餐类兑换码分发给用户。'"
        >
          <Button v-if="!hasFilters" size="sm" @click="grantOpen = true">
            <Plus />
            开通 / 续期
          </Button>
        </EmptyState>
        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-8 pr-0">
                  <Checkbox :model-value="pageChecked" :disabled="selectable.length === 0" aria-label="选择本页全部有效订阅" @update:model-value="togglePage" />
                </TableHead>
                <TableHead>用户 / 套餐</TableHead>
                <TableHead>状态</TableHead>
                <TableHead class="hidden md:table-cell">
                  有效期
                </TableHead>
                <TableHead class="hidden xl:table-cell">
                  来源
                </TableHead>
                <TableHead class="hidden min-w-56 sm:table-cell">
                  当前用量
                </TableHead>
                <TableHead class="w-10">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="s in items" :key="s.id" class="align-top" :data-state="selected.has(s.id) ? 'selected' : undefined" :data-sub-id="s.id">
                <TableCell class="w-8 pr-0">
                  <Checkbox
                    :model-value="selected.has(s.id)"
                    :disabled="s.status !== 'active'"
                    :aria-label="`选择 ${s.user.displayName} 的「${s.plan.name}」`"
                    :title="s.status !== 'active' ? '只能选择有效订阅' : undefined"
                    @update:model-value="(v) => toggleRow(s, v)"
                  />
                </TableCell>
                <TableCell class="max-w-56">
                  <button type="button" class="hover:text-primary block max-w-full truncate text-left font-medium hover:underline" :title="`只看 ${s.user.displayName} 的订阅`" @click="filterUser(s)">
                    {{ s.user.displayName }}
                  </button>
                  <p class="text-muted-foreground truncate text-xs" :title="s.plan.name">
                    {{ s.plan.name }}
                  </p>
                  <p class="text-muted-foreground text-xs tabular-nums md:hidden">
                    至 {{ formatDateTime(s.endsAt) }}
                  </p>
                </TableCell>
                <TableCell>
                  <SubscriptionStatusBadge :status="s.status" />
                </TableCell>
                <TableCell class="hidden text-xs whitespace-nowrap tabular-nums md:table-cell">
                  <p>{{ formatDateTime(s.startsAt) }}</p>
                  <p>至 {{ formatDateTime(s.endsAt) }}</p>
                  <p v-if="s.status === 'active'" class="text-muted-foreground">
                    {{ formatRemaining(s.endsAt, now.getTime()) }}
                  </p>
                </TableCell>
                <TableCell class="text-muted-foreground hidden text-xs xl:table-cell">
                  {{ SUBSCRIPTION_SOURCE_LABELS[s.source] ?? s.source }}
                </TableCell>
                <TableCell class="hidden sm:table-cell">
                  <div class="max-w-72 space-y-2">
                    <RuleUsageItem v-for="r in s.rules" :key="r.id" :rule="r" compact :live="s.status === 'active'" />
                  </div>
                </TableCell>
                <TableCell>
                  <DropdownMenu v-if="s.status === 'active'">
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon-sm" :aria-label="`${s.user.displayName} 的「${s.plan.name}」的操作`" data-testid="sub-row-menu">
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem @select="openReset(s)">
                        <RotateCcw />
                        重置额度
                      </DropdownMenuItem>
                      <DropdownMenuItem @select="openExtend(s)">
                        <CalendarPlus />
                        延长有效期
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" @select="requestCancel(s)">
                        <Ban />
                        取消订阅
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

    <GrantDialog
      v-model:open="grantOpen"
      :plans="activePlans"
      :plans-loading="plansLoading"
      :initial-plan-id="planId === ALL ? undefined : planId"
      :initial-user-id="userId || undefined"
      @granted="onGranted"
    />

    <ResetQuotaDialog
      v-model:open="resetOpen"
      :selected="single ? [] : selectedList"
      :single="single"
      :plans="plans"
      :initial-plan-id="planId === ALL ? undefined : planId"
      @done="onBulkDone"
    />
    <ExtendDialog
      v-model:open="extendOpen"
      :selected="single ? [] : selectedList"
      :single="single"
      :plans="plans"
      :initial-plan-id="planId === ALL ? undefined : planId"
      @done="onBulkDone"
    />

    <ConfirmDialog
      v-model:open="cancelOpen"
      :title="`取消 ${cancelTarget?.user.displayName ?? ''} 的「${cancelTarget?.plan.name ?? ''}」？`"
      confirm-text="取消订阅"
      cancel-text="返回"
      destructive
      :loading="cancelSaving"
      @confirm="applyCancel"
    >
      <p>取消立即生效，且不可恢复：该用户之后的请求不再使用此订阅的额度（改为按钱包计费或使用其他订阅）。如需恢复，请重新开通。</p>
      <FormField label="备注（可选）" for="cancel-note" :error="cancelError" class="text-left">
        <Textarea id="cancel-note" v-model="cancelNote" rows="2" maxlength="500" placeholder="例如：用户申请退款" class="text-foreground" />
      </FormField>
    </ConfirmDialog>
  </div>
</template>
