<script setup lang="ts">
import type { Role, User, UserBatchAction } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Ban, LogOut, RefreshCw, Search, ShieldCheck, Users, UsersRound, X } from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { adminApi } from '@/lib/endpoints'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { formatDateTime, formatRelative, ROLE_LABELS, ROLES, STATUS_LABELS } from '@/lib/format'
import { disabledDetails } from '@/lib/userAdmin'
import { multiplierShort } from '@/lib/groups'
import { useAuthStore } from '@/stores/auth'
import { useGroupsStore } from '@/stores/groups'
import UserBatchDialog from './users/UserBatchDialog.vue'
import UserChangeDialogs from './users/UserChangeDialogs.vue'
import UserDetailSheet from './users/UserDetailSheet.vue'

const PAGE_SIZE = 20

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const canWrite = computed(() => auth.can('users.write'))
const groups = useGroupsStore()
void groups.load()

// URL is the source of truth for page + search so reload / back button work.
const page = computed(() => queryInt(route.query.page, 1))
const q = computed(() => queryStr(route.query.q))
/** phase8 §1.3: `?groupId=` (the groups page links member counts here). */
const groupId = computed(() => queryStr(route.query.groupId))
const search = ref(q.value)

const items = ref<User[]>([])
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
    const res = await adminApi.listUsers(
      { page: page.value, pageSize: PAGE_SIZE, q: q.value || undefined, sort: '-createdAt', groupId: groupId.value || undefined },
      ctrl.signal,
    )
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

function setQuery(next: { page?: number, q?: string, groupId?: string }) {
  const nextPage = next.page ?? page.value
  const nextQ = next.q ?? q.value
  const nextGroup = next.groupId ?? groupId.value
  void router.replace({
    query: {
      ...route.query,
      page: nextPage > 1 ? String(nextPage) : undefined,
      q: nextQ || undefined,
      groupId: nextGroup || undefined,
    },
  })
}

watch([page, q, groupId], load, { immediate: true })

const ALL_GROUPS = 'all'
function onGroupFilter(v: unknown) {
  setQuery({ groupId: typeof v === 'string' && v !== ALL_GROUPS ? v : '', page: 1 })
}
const filterGroupName = computed(() => (groupId.value ? groups.nameOf(groupId.value) ?? `${groupId.value.slice(0, 8)}…` : ''))
/** Group column: shown once the backend reports groups (phase8). */
const showGroups = computed(() => (groups.items?.length ?? 0) > 0 || items.value.some(u => u.group))
function groupName(u: User): string | null {
  return u.group?.name || (u.group?.id ? groups.nameOf(u.group.id) : null)
}
watch(q, (v) => {
  if (v !== search.value)
    search.value = v
})
watchDebounced(search, (v) => {
  const trimmed = v.trim()
  if (trimmed !== q.value)
    setQuery({ q: trimmed, page: 1 })
}, { debounce: 300 })

onBeforeUnmount(() => controller?.abort())

// ---------- role / status (single user) ----------
const changes = ref<InstanceType<typeof UserChangeDialogs> | null>(null)

function isSelf(u: User): boolean {
  return auth.user?.id === u.id
}

function requestRoleChange(u: User, value: unknown) {
  if (typeof value !== 'string' || !(ROLES as string[]).includes(value) || value === u.role)
    return
  changes.value?.requestRole(u, value as Role)
}

function requestStatusChange(u: User, enabled: boolean) {
  const status = enabled ? 'active' : 'disabled'
  if (status === u.status)
    return
  if (enabled)
    changes.value?.requestEnable(u)
  else
    changes.value?.requestDisable(u)
}

function onUpdated(u: User) {
  items.value = items.value.map(x => (x.id === u.id ? { ...x, ...u } : x))
}

// ---------- selection & batch ----------
const selected = ref<Set<string>>(new Set())
watch([page, q, groupId], () => (selected.value = new Set()))
const selectedUsers = computed(() => items.value.filter(u => selected.value.has(u.id)))
const allChecked = computed<boolean | 'indeterminate'>(() => {
  if (items.value.length === 0 || selected.value.size === 0)
    return false
  return items.value.every(u => selected.value.has(u.id)) ? true : 'indeterminate'
})
function toggleAll(v: boolean | 'indeterminate') {
  selected.value = v === true ? new Set(items.value.map(u => u.id)) : new Set()
}
function toggleOne(id: string, v: boolean | 'indeterminate') {
  const next = new Set(selected.value)
  if (v === true)
    next.add(id)
  else
    next.delete(id)
  selected.value = next
}

const batchOpen = ref(false)
const batchAction = ref<UserBatchAction>('disable')
function openBatch(a: UserBatchAction) {
  batchAction.value = a
  batchOpen.value = true
}
function onBatchDone() {
  selected.value = new Set()
  void load()
}

// ---------- detail sheet ----------
const detailOpen = ref(false)
const detailId = ref<string | null>(null)
const detailPreview = computed(() => items.value.find(u => u.id === detailId.value) ?? null)
function openDetail(u: User) {
  detailId.value = u.id
  detailOpen.value = true
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="用户" description="管理控制台用户的角色与状态。用户通过外部身份提供方登录后自动出现在此列表；点击用户查看详情。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div class="flex w-full flex-col gap-2 sm:flex-row sm:items-center">
            <div class="relative w-full sm:max-w-xs">
              <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input v-model="search" placeholder="搜索名称或邮箱" class="pl-8" aria-label="搜索用户" />
            </div>
            <Select v-if="showGroups || groupId" :model-value="groupId || ALL_GROUPS" @update:model-value="onGroupFilter">
              <SelectTrigger class="w-full sm:w-44" aria-label="按分组过滤" data-testid="group-filter">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ALL_GROUPS">
                  全部分组
                </SelectItem>
                <SelectItem v-for="g in groups.sorted" :key="g.id" :value="g.id">
                  {{ g.name }} <span class="text-muted-foreground text-xs tabular-nums">{{ g.members }} 人</span>
                </SelectItem>
                <SelectItem v-if="groupId && !groups.byId.has(groupId)" :value="groupId">
                  {{ filterGroupName }}
                </SelectItem>
              </SelectContent>
            </Select>
            <Button v-if="groupId" variant="ghost" size="sm" class="self-start sm:self-center" @click="setQuery({ groupId: '', page: 1 })">
              <X />
              清除分组过滤
            </Button>
          </div>
          <p v-if="!canWrite" class="text-muted-foreground text-xs">
            你只有查看权限（缺少 users.write），无法修改角色或状态。
          </p>
        </div>

        <div
          v-if="canWrite && selected.size > 0"
          class="bg-muted/50 flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2"
          role="toolbar"
          aria-label="批量操作"
          data-testid="user-bulk-bar"
        >
          <span class="mr-auto text-sm">已选 <strong class="tabular-nums">{{ selected.size }}</strong> 位用户</span>
          <Button variant="outline" size="sm" class="text-destructive" @click="openBatch('disable')">
            <Ban />
            停用
          </Button>
          <Button variant="outline" size="sm" @click="openBatch('enable')">
            <ShieldCheck />
            启用
          </Button>
          <Button variant="outline" size="sm" @click="openBatch('logout')">
            <LogOut />
            强制下线
          </Button>
          <Button v-if="showGroups" variant="outline" size="sm" data-testid="bulk-set-group" @click="openBatch('set_group')">
            <UsersRound />
            设置分组
          </Button>
          <Button variant="ghost" size="sm" @click="selected = new Set()">
            <X />
            取消选择
          </Button>
        </div>

        <ErrorState v-if="loadError" :error="loadError" @retry="load" />

        <div v-else-if="loading && items.length === 0" class="space-y-2">
          <Skeleton v-for="i in 6" :key="i" class="h-12 w-full" />
        </div>

        <EmptyState
          v-else-if="items.length === 0"
          :icon="Users"
          :title="q || groupId ? '没有匹配的用户' : '暂无用户'"
          :description="q ? `没有找到与「${q}」匹配的用户。` : groupId ? `分组「${filterGroupName}」没有成员。` : '用户首次登录后会出现在这里。'"
        />

        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead v-if="canWrite" class="w-8 pr-0">
                  <Checkbox :model-value="allChecked" aria-label="选择本页全部用户" @update:model-value="toggleAll" />
                </TableHead>
                <TableHead>用户</TableHead>
                <TableHead>角色</TableHead>
                <TableHead v-if="showGroups">
                  分组
                </TableHead>
                <TableHead>启用</TableHead>
                <TableHead class="hidden md:table-cell">
                  最近登录
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  注册时间
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="u in items"
                :key="u.id"
                class="cursor-pointer"
                :data-state="selected.has(u.id) ? 'selected' : undefined"
                :data-user-id="u.id"
                @click="openDetail(u)"
              >
                <TableCell v-if="canWrite" class="w-8 pr-0" @click.stop>
                  <Checkbox :model-value="selected.has(u.id)" :aria-label="`选择 ${u.displayName}`" @update:model-value="(v) => toggleOne(u.id, v)" />
                </TableCell>
                <TableCell>
                  <div class="flex min-w-48 items-center gap-3">
                    <UserAvatar :name="u.displayName" :src="u.avatarUrl" />
                    <div class="min-w-0">
                      <button type="button" class="hover:text-primary flex max-w-full items-center gap-1.5 truncate text-left font-medium hover:underline" @click.stop="openDetail(u)">
                        <span class="truncate">{{ u.displayName }}</span>
                        <Badge v-if="isSelf(u)" variant="secondary" class="h-4 px-1.5 text-[10px]">
                          我
                        </Badge>
                      </button>
                      <p class="text-muted-foreground truncate text-xs">
                        {{ u.email ?? u.identities[0]?.login ?? '—' }}
                      </p>
                    </div>
                  </div>
                </TableCell>
                <TableCell>
                  <Select
                    v-if="canWrite && !isSelf(u)"
                    :model-value="u.role"
                    @update:model-value="(v) => requestRoleChange(u, v)"
                  >
                    <SelectTrigger size="sm" class="w-32" :aria-label="`${u.displayName} 的角色`" @click.stop>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="r in ROLES" :key="r" :value="r">
                        {{ ROLE_LABELS[r] }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <Badge v-else variant="secondary" :title="isSelf(u) ? '不能修改自己的角色' : undefined">
                    {{ ROLE_LABELS[u.role] }}
                  </Badge>
                </TableCell>
                <TableCell v-if="showGroups">
                  <Badge
                    v-if="groupName(u)"
                    variant="outline"
                    class="max-w-32 font-normal"
                    :title="u.group && groups.byId.get(u.group.id) ? `价格倍率 ${multiplierShort(groups.byId.get(u.group.id)!.priceMultiplier)}` : undefined"
                    data-testid="user-group"
                  >
                    <span class="truncate">{{ groupName(u) }}</span>
                  </Badge>
                  <span v-else class="text-muted-foreground text-xs">—</span>
                </TableCell>
                <TableCell>
                  <div class="flex items-center gap-2">
                    <Switch
                      :model-value="u.status === 'active'"
                      :disabled="!canWrite || isSelf(u)"
                      :aria-label="`${u.displayName} 是否启用`"
                      @click.stop
                      @update:model-value="(v: boolean) => requestStatusChange(u, v)"
                    />
                    <Tooltip v-if="disabledDetails(u)">
                      <TooltipTrigger as-child>
                        <span class="text-destructive cursor-help text-xs underline decoration-dotted underline-offset-2" tabindex="0" data-testid="status-disabled" @click.stop>
                          {{ u.disabledUntil ? '停用中' : STATUS_LABELS.disabled }}
                        </span>
                      </TooltipTrigger>
                      <TooltipContent class="max-w-72 whitespace-pre-line">
                        {{ disabledDetails(u) }}
                      </TooltipContent>
                    </Tooltip>
                    <span v-else class="text-muted-foreground text-xs">
                      {{ STATUS_LABELS[u.status] }}
                    </span>
                  </div>
                </TableCell>
                <TableCell class="hidden md:table-cell" :title="formatDateTime(u.lastLoginAt)">
                  {{ u.lastLoginAt ? formatRelative(u.lastLoginAt) : '从未登录' }}
                </TableCell>
                <TableCell class="hidden lg:table-cell">
                  {{ formatDateTime(u.createdAt) }}
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

    <UserChangeDialogs ref="changes" @updated="onUpdated" @stale="load" />
    <UserBatchDialog
      v-model:open="batchOpen"
      :users="selectedUsers"
      :action="batchAction"
      :self-id="auth.user?.id ?? null"
      @done="onBatchDone"
    />
    <UserDetailSheet v-model:open="detailOpen" :user-id="detailId" :preview="detailPreview" @updated="onUpdated" />
  </div>
</template>
