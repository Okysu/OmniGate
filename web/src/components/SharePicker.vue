<script setup lang="ts">
import type { SelectionStatus } from '@/lib/shares'
import type { ChannelShare, SharedWith, User } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Check, RotateCcw, User as UserIcon, UsersRound, X } from '@lucide/vue'
import UserPicker from '@/components/UserPicker.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatDateTime } from '@/lib/format'
import { multiplierShort } from '@/lib/groups'
import { declinedShares, GROUP_SHARE_ADMIN_ONLY, reinvite, SELECTION_STATUS_LABELS, selectionStatus, SHARE_STATUS_CLASSES, shareIndex } from '@/lib/shares'
import { useAuthStore } from '@/stores/auth'
import { useGroupsStore } from '@/stores/groups'

/**
 * Channel share targets (phase8 §1.2): users and / or user groups. The selection is shown
 * as one list with type badges; tabs pick users (searchable with users.read) or groups
 * (listed with users.read, otherwise pasted as IDs).
 *
 * phase5-api.md §5: users must accept an invitation before the channel is usable by them;
 * with `shares` (owner view) every selected user shows its invitation status and declined
 * recipients can be invited again. Only `channels.manage` may add groups (`canShareGroups`);
 * groups already on the channel stay visible and removable.
 */
const props = withDefaults(defineProps<{
  id?: string
  disabled?: boolean
  /** Holder of `channels.manage`: may add user groups. */
  canShareGroups?: boolean
  /** Server invitation status per user; undefined / null = unknown (older backend), no badges. */
  shares?: ChannelShare[] | null
}>(), { id: undefined, disabled: false, canShareGroups: true, shares: undefined })
const model = defineModel<SharedWith>({ required: true })

const auth = useAuthStore()
const groups = useGroupsStore()
const canListGroups = computed(() => auth.can('users.read'))
watch(canListGroups, (v) => {
  if (v)
    void groups.load()
}, { immediate: true })

const tab = ref<'users' | 'groups'>('users')
const userNames = ref(new Map<string, string>())
watch(() => props.shares, (list) => {
  for (const s of list ?? []) {
    if (s.displayName && !userNames.value.has(s.userId))
      userNames.value.set(s.userId, s.displayName)
  }
}, { immediate: true })
watch(() => props.canShareGroups, (v) => {
  if (!v && tab.value === 'groups')
    tab.value = 'users'
}, { immediate: true })

const statusKnown = computed(() => props.shares !== undefined && props.shares !== null)
const index = computed(() => shareIndex(props.shares))
const declined = computed(() => declinedShares(props.shares, model.value))
function statusOf(userId: string): SelectionStatus | null {
  return statusKnown.value ? selectionStatus(userId, index.value) : null
}
const STATUS_CLASS: Record<SelectionStatus, string> = {
  ...SHARE_STATUS_CLASSES,
  new: 'border-sky-500/40 text-sky-700 dark:text-sky-300',
  reinvite: 'border-sky-500/40 text-sky-700 dark:text-sky-300',
}
function invitedAgain(userId: string) {
  model.value = reinvite(model.value, userId)
}

const users = computed({
  get: () => model.value.users,
  set: v => (model.value = { ...model.value, users: v }),
})

function onPick(u: User | null) {
  if (u)
    userNames.value.set(u.id, u.displayName)
}
function onResolve(list: User[]) {
  for (const u of list)
    userNames.value.set(u.id, u.displayName)
}

function toggleGroup(id: string) {
  const has = model.value.groups.includes(id)
  model.value = { ...model.value, groups: has ? model.value.groups.filter(g => g !== id) : [...model.value.groups, id] }
}
function removeUser(id: string) {
  model.value = { ...model.value, users: model.value.users.filter(u => u !== id) }
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const manual = ref('')
const manualError = ref<string | null>(null)
function addManualGroups() {
  const ids = manual.value.split(/[\s,，]+/).map(s => s.trim()).filter(Boolean)
  const bad = ids.filter(s => !UUID_RE.test(s))
  if (bad.length) {
    manualError.value = `不是合法的分组 ID：${bad.join('、')}`
    return
  }
  manualError.value = null
  model.value = { ...model.value, groups: [...new Set([...model.value.groups, ...ids.map(s => s.toLowerCase())])] }
  manual.value = ''
}

interface Chip { kind: 'user' | 'group', id: string, label: string, known: boolean, status: SelectionStatus | null }
const chips = computed<Chip[]>(() => [
  ...model.value.groups.map(id => ({ kind: 'group' as const, id, label: groups.nameOf(id) ?? id, known: groups.byId.has(id), status: null })),
  ...model.value.users.map(id => ({ kind: 'user' as const, id, label: userNames.value.get(id) ?? id, known: userNames.value.has(id), status: statusOf(id) })),
])
const memberTotal = computed(() => model.value.groups.reduce((n, id) => n + (groups.byId.get(id)?.members ?? 0), 0))
</script>

<template>
  <div class="space-y-2" data-testid="share-picker">
    <div v-if="chips.length" class="flex flex-wrap gap-1.5" data-testid="share-chips">
      <Badge
        v-for="c in chips"
        :key="`${c.kind}:${c.id}`"
        variant="secondary"
        class="h-6 max-w-full gap-1 pr-1 pl-1"
        :title="c.id"
        :data-kind="c.kind"
      >
        <span
          class="inline-flex h-4 shrink-0 items-center gap-0.5 rounded-sm px-1 text-[10px] font-medium"
          :class="c.kind === 'group' ? 'bg-violet-500/15 text-violet-700 dark:text-violet-300' : 'bg-sky-500/15 text-sky-700 dark:text-sky-300'"
        >
          <UsersRound v-if="c.kind === 'group'" class="size-3" />
          <UserIcon v-else class="size-3" />
          {{ c.kind === 'group' ? '组' : '用户' }}
        </span>
        <span class="truncate" :class="c.known ? '' : 'font-mono text-[11px]'">{{ c.label }}</span>
        <span
          v-if="c.status"
          class="inline-flex h-4 shrink-0 items-center rounded-sm border px-1 text-[10px] font-medium"
          :class="STATUS_CLASS[c.status]"
          :data-status="c.status"
          data-testid="share-status"
        >{{ SELECTION_STATUS_LABELS[c.status] }}</span>
        <button
          type="button"
          class="hover:bg-foreground/10 rounded-sm p-0.5"
          :aria-label="`移除 ${c.label}`"
          :disabled="disabled"
          @click="c.kind === 'group' ? toggleGroup(c.id) : removeUser(c.id)"
        >
          <X class="size-3" />
        </button>
      </Badge>
    </div>
    <p v-if="model.groups.length && memberTotal" class="text-muted-foreground text-xs">
      所选分组共 {{ memberTotal }} 位成员（成员变化时自动生效）。
    </p>
    <div v-if="declined.length" class="space-y-1 rounded-md border border-dashed p-2" data-testid="share-declined">
      <p class="text-muted-foreground text-xs">
        以下用户拒绝了邀请或已退出共享，渠道对他们不可用。重新邀请后需要对方再次接受：
      </p>
      <ul class="space-y-1">
        <li v-for="d in declined" :key="d.userId" class="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <span class="min-w-0 truncate" :title="d.userId">{{ d.displayName || d.userId }}</span>
          <span class="inline-flex h-4 shrink-0 items-center rounded-sm border px-1 text-[10px] font-medium" :class="SHARE_STATUS_CLASSES.declined">{{ SELECTION_STATUS_LABELS.declined }}</span>
          <span v-if="d.respondedAt" class="text-muted-foreground text-xs">{{ formatDateTime(d.respondedAt) }}</span>
          <Button type="button" variant="ghost" size="xs" class="ml-auto" :disabled="disabled" data-testid="share-reinvite" @click="invitedAgain(d.userId)">
            <RotateCcw />
            重新邀请
          </Button>
        </li>
      </ul>
    </div>

    <Tabs v-model="tab">
      <TabsList class="w-full sm:w-auto">
        <TabsTrigger value="users" data-testid="share-tab-users">
          用户<span class="text-muted-foreground tabular-nums">（{{ model.users.length }}）</span>
        </TabsTrigger>
        <TabsTrigger value="groups" data-testid="share-tab-groups" :disabled="!canShareGroups" :title="canShareGroups ? undefined : GROUP_SHARE_ADMIN_ONLY">
          用户组<span class="text-muted-foreground tabular-nums">（{{ model.groups.length }}）</span>
        </TabsTrigger>
      </TabsList>
      <TabsContent value="users" class="pt-1">
        <UserPicker :id="props.id" v-model="users" hide-selected :disabled="disabled" @pick="onPick" @resolve="onResolve" />
      </TabsContent>
      <TabsContent value="groups" class="pt-1">
        <template v-if="canListGroups">
          <ul class="max-h-48 divide-y overflow-y-auto rounded-md border" data-testid="share-groups">
            <li v-for="g in groups.sorted" :key="g.id">
              <button
                type="button"
                class="hover:bg-muted flex w-full items-center gap-2 px-2.5 py-1.5 text-left disabled:opacity-50"
                :disabled="disabled"
                :aria-pressed="model.groups.includes(g.id)"
                @click="toggleGroup(g.id)"
              >
                <span class="flex size-4 shrink-0 items-center justify-center rounded-[4px] border" :class="model.groups.includes(g.id) ? 'border-primary bg-primary text-primary-foreground' : ''">
                  <Check v-if="model.groups.includes(g.id)" class="size-3" />
                </span>
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-sm">{{ g.name }}<span v-if="g.isDefault" class="text-muted-foreground text-xs">（默认分组）</span></span>
                  <span class="text-muted-foreground block truncate text-xs">{{ g.members }} 位成员 · 倍率 {{ multiplierShort(g.priceMultiplier) }}</span>
                </span>
              </button>
            </li>
            <li v-if="groups.loading && !groups.items" class="text-muted-foreground px-2.5 py-3 text-center text-xs">
              加载中…
            </li>
            <li v-else-if="groups.items && groups.items.length === 0" class="text-muted-foreground px-2.5 py-3 text-center text-xs">
              没有用户组
            </li>
          </ul>
          <p class="text-muted-foreground mt-1.5 text-xs">
            共享给默认分组相当于共享给所有新老用户，请谨慎选择。
          </p>
        </template>
        <template v-else>
          <div class="flex gap-2">
            <Input v-model="manual" placeholder="粘贴分组 ID（UUID），多个用逗号分隔" class="font-mono text-xs" :disabled="disabled" @keydown.enter.prevent="addManualGroups" />
            <Button type="button" variant="outline" :disabled="disabled || !manual.trim()" @click="addManualGroups">
              添加
            </Button>
          </div>
          <p v-if="manualError" class="text-destructive mt-1.5 text-xs">
            {{ manualError }}
          </p>
          <p v-else class="text-muted-foreground mt-1.5 text-xs">
            你没有查看用户组的权限（users.read），请向管理员索取分组 ID。
          </p>
        </template>
      </TabsContent>
    </Tabs>
    <p v-if="!canShareGroups" class="text-muted-foreground text-xs" data-testid="share-groups-admin-only">
      {{ GROUP_SHARE_ADMIN_ONLY }}，你只能共享给具体的用户{{ model.groups.length ? '（已有的用户组可以移除）' : '' }}。
    </p>
  </div>
</template>
