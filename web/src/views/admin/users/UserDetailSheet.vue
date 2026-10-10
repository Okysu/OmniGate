<script setup lang="ts">
import type { AdminUserDetail, GroupRef, Plan, Role, Subscription, User } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Ban, Cable, CalendarClock, Coins, ExternalLink, Hash, KeyRound, LogOut, Plus, RefreshCw, ShieldCheck, Sigma, UsersRound, Wallet } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import CopyButton from '@/components/CopyButton.vue'
import ErrorState from '@/components/ErrorState.vue'
import RuleUsageItem from '@/components/quota/RuleUsageItem.vue'
import SubscriptionStatusBadge from '@/components/quota/SubscriptionStatusBadge.vue'
import StatTile from '@/components/StatTile.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage } from '@/lib/api'
import { adminApi, fetchAllPlans } from '@/lib/endpoints'
import { formatCompact, formatDateTime, formatNumber, formatRelative, ROLE_LABELS, ROLES, STATUS_LABELS } from '@/lib/format'
import { limitParts, multiplierLabel, normalizeLimits } from '@/lib/groups'
import { isAbortError } from '@/lib/query'
import { sortSubscriptions } from '@/lib/quota'
import { useAuthStore } from '@/stores/auth'
import { useGroupsStore } from '@/stores/groups'
import AdjustWalletDialog from '@/views/billing/AdjustWalletDialog.vue'
import UserResetCards from '@/views/billing/reset-cards/UserResetCards.vue'
import GrantDialog from '@/views/billing/subscriptions/GrantDialog.vue'
import UserChangeDialogs from './UserChangeDialogs.vue'
import UserGroupDialog from './UserGroupDialog.vue'

/** Admin user detail (phase7-api.md §2.2): profile, controls, wallet, subscriptions, reset cards, keys, sessions. */
const props = defineProps<{
  userId: string | null
  /** List row shown while the detail loads. */
  preview?: User | null
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ updated: [user: User] }>()

const auth = useAuthStore()
const { money } = useCurrency()
const canWrite = computed(() => auth.can('users.write'))
const canBilling = computed(() => auth.can('billing.manage'))

const detail = ref<AdminUserDetail | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  const id = props.userId
  if (!id)
    return
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await adminApi.getUser(id, ctrl.signal)
    if (props.userId === id)
      detail.value = res
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
watch(() => [open.value, props.userId] as const, ([o, id], old) => {
  if (!o || !id)
    return
  if (old?.[1] !== id)
    detail.value = null
  void load()
}, { immediate: true })

const user = computed<User | null>(() => detail.value?.user ?? props.preview ?? null)
const isSelf = computed(() => !!user.value && user.value.id === auth.user?.id)
const subs = computed(() => sortSubscriptions(detail.value?.subscriptions ?? []))
const enabledKeys = computed(() => (detail.value?.keys ?? []).filter(k => k.status === 'enabled').length)
const contact = computed(() => user.value?.email ?? user.value?.identities?.[0]?.login ?? null)

function onUserUpdated(u: User) {
  if (detail.value)
    detail.value = { ...detail.value, user: { ...detail.value.user, ...u, disabledReason: u.disabledReason ?? null, disabledUntil: u.disabledUntil ?? null } }
  emit('updated', u)
  void load()
}

// ---------- group (phase8 §1.3) ----------
const groups = useGroupsStore()
const groupDialogOpen = ref(false)
const userGroup = computed<GroupRef | null>(() => detail.value?.user.group ?? props.preview?.group ?? null)
const groupInfo = computed(() => (userGroup.value ? groups.byId.get(userGroup.value.id) ?? null : null))
const groupLimits = computed(() => (groupInfo.value ? limitParts(normalizeLimits(groupInfo.value.limits), money) : []))
watch(open, (v) => {
  if (v)
    void groups.load()
})
function onGroupChanged(g: GroupRef) {
  if (detail.value)
    detail.value = { ...detail.value, user: { ...detail.value.user, group: g } }
  const base = detail.value?.user ?? props.preview
  if (base)
    emit('updated', { ...base, group: g })
}

// ---------- role / status ----------
const changes = ref<InstanceType<typeof UserChangeDialogs> | null>(null)
function onRole(v: unknown) {
  if (user.value && typeof v === 'string' && (ROLES as string[]).includes(v))
    changes.value?.requestRole(user.value, v as Role)
}

// ---------- logout / disable keys ----------
type Action = 'logout' | 'keys'
const action = ref<Action | null>(null)
const actionOpen = ref(false)
const actionSaving = ref(false)
function requestAction(a: Action) {
  action.value = a
  actionOpen.value = true
}
async function applyAction() {
  const u = user.value
  if (!u || !action.value)
    return
  actionSaving.value = true
  try {
    if (action.value === 'logout') {
      const res = await adminApi.logoutUser(u.id)
      toast.success(`已强制下线 ${u.displayName}`, { description: `撤销了 ${res?.revoked ?? 0} 个会话` })
    }
    else {
      const res = await adminApi.disableUserKeys(u.id)
      toast.success(`已禁用 ${u.displayName} 的 API Key`, { description: `共 ${res?.disabled ?? 0} 个，可由用户或管理员逐个重新启用` })
    }
    actionOpen.value = false
    void load()
  }
  catch (err) {
    actionOpen.value = false
    toast.error('操作失败', { description: errorMessage(err) })
  }
  finally {
    actionSaving.value = false
  }
}

// ---------- wallet / grant (billing.manage) ----------
const adjustOpen = ref(false)
const grantOpen = ref(false)
const plans = ref<Plan[]>([])
const plansLoading = ref(false)
async function openGrant() {
  grantOpen.value = true
  if (plans.value.length)
    return
  plansLoading.value = true
  try {
    plans.value = await fetchAllPlans('active')
  }
  catch (err) {
    toast.error('无法加载套餐列表', { description: errorMessage(err) })
  }
  finally {
    plansLoading.value = false
  }
}
function onGranted(s: Subscription, renewed: boolean) {
  toast.success(renewed ? '已续期' : '已开通', { description: `「${s.plan.name}」有效期至 ${formatDateTime(s.endsAt)}` })
  void load()
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent class="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[90vw] data-[side=right]:xl:max-w-4xl" data-testid="user-sheet">
      <SheetHeader class="border-b pr-12">
        <div v-if="user" class="flex min-w-0 items-center gap-3">
          <UserAvatar :name="user.displayName" :src="user.avatarUrl" size="lg" />
          <div class="min-w-0 flex-1 space-y-1">
            <SheetTitle class="flex min-w-0 flex-wrap items-center gap-1.5 text-lg">
              <span class="truncate">{{ user.displayName }}</span>
              <Badge v-if="isSelf" variant="secondary" class="h-4 px-1.5 text-[10px]">
                我
              </Badge>
              <Badge variant="outline" class="font-normal">
                {{ ROLE_LABELS[user.role] }}
              </Badge>
              <Badge v-if="user.status === 'disabled'" variant="outline" class="border-destructive/40 text-destructive font-normal">
                {{ STATUS_LABELS.disabled }}
              </Badge>
            </SheetTitle>
            <SheetDescription class="truncate text-xs">
              {{ contact ?? '无邮箱' }}
            </SheetDescription>
          </div>
        </div>
        <template v-else>
          <SheetTitle>用户详情</SheetTitle>
          <SheetDescription>加载中…</SheetDescription>
        </template>
      </SheetHeader>

      <div class="flex-1 space-y-6 overflow-y-auto p-4 sm:p-6">
        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="!detail" class="space-y-3">
          <Skeleton class="h-24 w-full" />
          <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Skeleton v-for="i in 4" :key="i" class="h-20" />
          </div>
          <Skeleton class="h-40 w-full" />
        </div>

        <template v-else>
          <!-- 停用提示 -->
          <div v-if="detail.user.status === 'disabled'" class="border-destructive/30 bg-destructive/5 space-y-1 rounded-lg border p-3 text-sm" role="status" data-testid="disabled-notice">
            <p class="text-destructive flex items-center gap-1.5 font-medium">
              <Ban class="size-4" />
              该用户已停用
            </p>
            <p v-if="detail.user.disabledReason" class="break-all">
              原因：{{ detail.user.disabledReason }}
            </p>
            <p class="text-muted-foreground text-xs">
              {{ detail.user.disabledUntil ? `将于 ${formatDateTime(detail.user.disabledUntil)} 自动启用` : '永久停用，需管理员手动启用' }}
            </p>
          </div>

          <!-- 账户 -->
          <section class="space-y-3" aria-labelledby="ud-account">
            <div class="flex items-center justify-between gap-2">
              <h3 id="ud-account" class="text-sm font-semibold">
                账户
              </h3>
              <Button variant="ghost" size="icon-sm" aria-label="刷新" :disabled="loading" @click="load">
                <RefreshCw :class="loading ? 'animate-spin' : ''" />
              </Button>
            </div>
            <div class="grid gap-4 lg:grid-cols-2">
              <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-sm">
                <dt class="text-muted-foreground">
                  用户 ID
                </dt>
                <dd class="flex min-w-0 items-center gap-1">
                  <span class="truncate font-mono text-xs">{{ detail.user.id }}</span>
                  <CopyButton :value="detail.user.id" label="复制用户 ID" class="shrink-0" />
                </dd>
                <dt class="text-muted-foreground">
                  邮箱
                </dt>
                <dd class="truncate">
                  {{ detail.user.email ?? '—' }}
                </dd>
                <dt class="text-muted-foreground">
                  注册时间
                </dt>
                <dd class="tabular-nums">
                  {{ formatDateTime(detail.user.createdAt) }}
                </dd>
                <dt class="text-muted-foreground">
                  最近登录
                </dt>
                <dd class="tabular-nums" :title="formatDateTime(detail.user.lastLoginAt)">
                  {{ detail.user.lastLoginAt ? formatRelative(detail.user.lastLoginAt) : '从未登录' }}
                </dd>
              </dl>

              <div class="bg-muted/30 space-y-3 rounded-lg border p-3">
                <div class="grid grid-cols-[4rem_minmax(0,1fr)] items-center gap-2">
                  <span class="text-muted-foreground text-sm">角色</span>
                  <Select v-if="canWrite && !isSelf" :model-value="detail.user.role" @update:model-value="onRole">
                    <SelectTrigger class="w-full sm:w-48" aria-label="角色" data-testid="ud-role">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="r in ROLES" :key="r" :value="r">
                        {{ ROLE_LABELS[r] }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                  <span v-else class="text-sm" :title="isSelf ? '不能修改自己的角色' : undefined">{{ ROLE_LABELS[detail.user.role] }}</span>
                </div>
                <div class="grid grid-cols-[4rem_minmax(0,1fr)] items-center gap-2">
                  <span class="text-muted-foreground text-sm">状态</span>
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="text-sm" :class="detail.user.status === 'disabled' ? 'text-destructive' : ''">{{ STATUS_LABELS[detail.user.status] }}</span>
                    <template v-if="canWrite && !isSelf">
                      <Button v-if="detail.user.status === 'active'" variant="outline" size="sm" class="text-destructive" data-testid="ud-disable" @click="changes?.requestDisable(detail.user)">
                        <Ban />
                        停用
                      </Button>
                      <Button v-else variant="outline" size="sm" data-testid="ud-enable" @click="changes?.requestEnable(detail.user)">
                        <ShieldCheck />
                        启用
                      </Button>
                    </template>
                  </div>
                </div>
                <div v-if="userGroup || groups.items?.length" class="grid grid-cols-[4rem_minmax(0,1fr)] items-start gap-2" data-testid="ud-group">
                  <span class="text-muted-foreground text-sm leading-8">分组</span>
                  <div class="min-w-0 space-y-1">
                    <div class="flex min-h-8 flex-wrap items-center gap-2">
                      <span class="truncate text-sm font-medium">{{ userGroup?.name ?? '—' }}</span>
                      <Badge v-if="groupInfo" variant="outline" class="font-normal tabular-nums">
                        {{ multiplierLabel(groupInfo.priceMultiplier) }}
                      </Badge>
                      <Button v-if="canWrite" variant="outline" size="sm" data-testid="ud-change-group" @click="groupDialogOpen = true">
                        <UsersRound />
                        更改分组
                      </Button>
                    </div>
                    <p v-if="groupInfo" class="text-muted-foreground text-xs">
                      限额：{{ groupLimits.length ? groupLimits.map(p => p.text).join(' · ') : '不限' }}
                    </p>
                  </div>
                </div>
                <p v-if="!canWrite" class="text-muted-foreground text-xs">
                  只读：缺少 users.write 权限。
                </p>
                <p v-else-if="isSelf" class="text-muted-foreground text-xs">
                  不能修改自己的角色或状态。
                </p>
              </div>
            </div>
          </section>

          <!-- 近 30 天 -->
          <section class="space-y-3" aria-labelledby="ud-usage">
            <h3 id="ud-usage" class="text-sm font-semibold">
              近 30 天
            </h3>
            <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
              <StatTile label="请求数" :value="formatNumber(detail.usage30d.requests)" :icon="Hash" />
              <StatTile label="钱包扣费" :value="money(detail.usage30d.charge)" :icon="Coins" />
              <StatTile label="Tokens" :value="formatCompact(detail.usage30d.tokens)" :title="formatNumber(detail.usage30d.tokens)" :icon="Sigma" />
              <StatTile label="自有渠道" :value="formatNumber(detail.channels.own)" :icon="Cable" hint="该用户自己创建的渠道" />
            </div>
          </section>

          <div class="grid gap-6 lg:grid-cols-2">
            <!-- 钱包 -->
            <section class="space-y-3" aria-labelledby="ud-wallet">
              <div class="flex min-h-8 items-center justify-between gap-2">
                <h3 id="ud-wallet" class="flex items-center gap-1.5 text-sm font-semibold">
                  <Wallet class="size-4" />
                  钱包
                </h3>
                <Button v-if="canBilling" variant="outline" size="sm" data-testid="ud-adjust" @click="adjustOpen = true">
                  调整余额
                </Button>
              </div>
              <dl v-if="detail.wallet" class="grid grid-cols-2 gap-3">
                <div class="bg-muted/40 rounded-lg border p-3">
                  <dt class="text-muted-foreground text-xs">
                    余额
                  </dt>
                  <dd class="mt-1 text-lg font-semibold tabular-nums">
                    {{ money(detail.wallet.balance) }}
                  </dd>
                </div>
                <div class="bg-muted/40 rounded-lg border p-3">
                  <dt class="text-muted-foreground text-xs">
                    预留中
                  </dt>
                  <dd class="mt-1 text-lg font-semibold tabular-nums">
                    {{ money(detail.wallet.reserved) }}
                  </dd>
                </div>
              </dl>
              <p v-else class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
                尚未创建钱包（首次计费或调整余额时自动创建）。
              </p>
            </section>

            <!-- 会话 -->
            <section class="space-y-3" aria-labelledby="ud-sessions">
              <div class="flex min-h-8 items-center justify-between gap-2">
                <h3 id="ud-sessions" class="flex items-center gap-1.5 text-sm font-semibold">
                  <LogOut class="size-4" />
                  登录会话
                </h3>
                <Button
                  v-if="canWrite && !isSelf"
                  variant="outline"
                  size="sm"
                  :disabled="detail.sessions.active === 0"
                  data-testid="ud-logout"
                  @click="requestAction('logout')"
                >
                  强制下线
                </Button>
              </div>
              <div class="bg-muted/40 rounded-lg border p-3">
                <p class="text-muted-foreground text-xs">
                  活跃会话
                </p>
                <p class="mt-1 text-lg font-semibold tabular-nums">
                  {{ detail.sessions.active }}
                </p>
              </div>
            </section>
          </div>

          <!-- 订阅 -->
          <section class="space-y-3" aria-labelledby="ud-subs">
            <div class="flex min-h-8 flex-wrap items-center justify-between gap-2">
              <h3 id="ud-subs" class="flex items-center gap-1.5 text-sm font-semibold">
                <CalendarClock class="size-4" />
                套餐订阅
                <span class="text-muted-foreground font-normal">（最近 20 条）</span>
              </h3>
              <div class="flex items-center gap-1">
                <Button v-if="canBilling" variant="ghost" size="sm" as-child>
                  <RouterLink :to="{ path: '/console/billing/subscriptions', query: { userId: detail.user.id } }">
                    <ExternalLink />
                    订阅管理
                  </RouterLink>
                </Button>
                <Button v-if="canBilling" variant="outline" size="sm" data-testid="ud-grant" @click="openGrant">
                  <Plus />
                  开通 / 续期
                </Button>
              </div>
            </div>
            <ul v-if="subs.length" class="divide-y rounded-lg border">
              <li v-for="s in subs" :key="s.id" class="space-y-2 p-3">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="min-w-0 truncate text-sm font-medium">{{ s.plan.name }}</span>
                  <SubscriptionStatusBadge :status="s.status" />
                  <span class="text-muted-foreground ml-auto text-xs tabular-nums">
                    {{ formatDateTime(s.startsAt) }} 至 {{ formatDateTime(s.endsAt) }}
                  </span>
                </div>
                <div v-if="s.status === 'active' && s.rules.length" class="grid gap-2 sm:grid-cols-2">
                  <RuleUsageItem v-for="r in s.rules" :key="r.id" :rule="r" compact live />
                </div>
              </li>
            </ul>
            <p v-else class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
              没有订阅。
            </p>
          </section>

          <!-- 重置卡 (phase11 §2) -->
          <UserResetCards v-if="canBilling" :user-id="detail.user.id" />

          <!-- API Keys -->
          <section class="space-y-3" aria-labelledby="ud-keys">
            <div class="flex min-h-8 items-center justify-between gap-2">
              <h3 id="ud-keys" class="flex items-center gap-1.5 text-sm font-semibold">
                <KeyRound class="size-4" />
                API Keys
                <span class="text-muted-foreground font-normal">（{{ enabledKeys }} / {{ detail.keys.length }} 启用）</span>
              </h3>
              <Button v-if="canWrite" variant="outline" size="sm" class="text-destructive" :disabled="enabledKeys === 0" data-testid="ud-disable-keys" @click="requestAction('keys')">
                禁用全部 Key
              </Button>
            </div>
            <div v-if="detail.keys.length" class="overflow-x-auto rounded-lg border">
              <table class="w-full text-sm">
                <thead class="bg-muted/40 text-muted-foreground text-xs">
                  <tr>
                    <th class="px-3 py-2 text-left font-medium">
                      名称
                    </th>
                    <th class="px-3 py-2 text-left font-medium">
                      状态
                    </th>
                    <th class="hidden px-3 py-2 text-left font-medium sm:table-cell">
                      最近使用
                    </th>
                    <th class="hidden px-3 py-2 text-left font-medium md:table-cell">
                      创建时间
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="k in detail.keys" :key="k.id" class="border-t">
                    <td class="max-w-56 px-3 py-2">
                      <p class="truncate">
                        {{ k.name }}
                      </p>
                      <p class="text-muted-foreground truncate font-mono text-xs">
                        {{ k.prefix }}…
                      </p>
                    </td>
                    <td class="px-3 py-2">
                      <Badge variant="outline" :class="k.status === 'enabled' ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400' : 'text-muted-foreground'">
                        {{ k.status === 'enabled' ? '启用' : '已禁用' }}
                      </Badge>
                    </td>
                    <td class="text-muted-foreground hidden px-3 py-2 text-xs sm:table-cell" :title="formatDateTime(k.lastUsedAt)">
                      {{ k.lastUsedAt ? formatRelative(k.lastUsedAt) : '从未使用' }}
                    </td>
                    <td class="text-muted-foreground hidden px-3 py-2 text-xs tabular-nums md:table-cell">
                      {{ formatDateTime(k.createdAt) }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-else class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm">
              没有 API Key。
            </p>
          </section>

          <!-- 登录身份 -->
          <section class="space-y-3" aria-labelledby="ud-ids">
            <h3 id="ud-ids" class="text-sm font-semibold">
              登录身份
            </h3>
            <ul v-if="detail.identities.length" class="divide-y rounded-lg border text-sm">
              <li v-for="i in detail.identities" :key="`${i.provider}:${i.subject}`" class="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                <Badge variant="secondary" class="font-normal">
                  {{ i.provider }}
                </Badge>
                <span class="min-w-0 truncate font-mono text-xs" :title="i.subject">{{ i.subject }}</span>
                <span class="text-muted-foreground min-w-0 truncate text-xs">{{ i.email ?? '' }}</span>
                <span class="text-muted-foreground ml-auto text-xs tabular-nums">{{ formatDateTime(i.createdAt) }} 绑定</span>
              </li>
            </ul>
            <p v-else class="text-muted-foreground text-sm">
              没有登录身份。
            </p>
          </section>
        </template>
      </div>
    </SheetContent>
  </Sheet>

  <UserChangeDialogs ref="changes" @updated="onUserUpdated" @stale="load" />
  <UserGroupDialog v-if="canWrite" v-model:open="groupDialogOpen" :user="user ? { id: user.id, displayName: user.displayName, group: userGroup } : null" @changed="onGroupChanged" />

  <ConfirmDialog
    v-model:open="actionOpen"
    :title="action === 'logout' ? `强制 ${user?.displayName ?? ''} 下线？` : `禁用 ${user?.displayName ?? ''} 的全部 API Key？`"
    :confirm-text="action === 'logout' ? '强制下线' : '禁用全部 Key'"
    destructive
    :loading="actionSaving"
    @confirm="applyAction"
  >
    <p v-if="action === 'logout'">
      撤销该用户的全部 {{ detail?.sessions.active ?? 0 }} 个登录会话，用户需要重新登录。API Key 不受影响。
    </p>
    <p v-else>
      该用户的 {{ enabledKeys }} 个启用中的 API Key 将被立即禁用，使用它们的请求会被拒绝。之后可由用户或管理员在 Key 列表中逐个重新启用。
    </p>
  </ConfirmDialog>

  <AdjustWalletDialog v-if="canBilling && user" v-model:open="adjustOpen" :user="{ id: user.id, displayName: user.displayName }" @adjusted="load" />
  <GrantDialog
    v-if="canBilling && user"
    v-model:open="grantOpen"
    :plans="plans"
    :plans-loading="plansLoading"
    :user="{ id: user.id, displayName: user.displayName }"
    @granted="onGranted"
  />
</template>
