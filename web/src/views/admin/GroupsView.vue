<script setup lang="ts">
import type { UserGroup } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { MoreHorizontal, Pencil, Plus, RefreshCw, Star, Trash2, UsersRound } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isApiError, isVersionConflict } from '@/lib/api'
import { groupsApi } from '@/lib/endpoints'
import { GROUP_ERROR_MESSAGES, limitParts, MULTIPLIER_TONE_CLASSES, multiplierShort, multiplierTone, multiplierWords, normalizeLimits } from '@/lib/groups'
import { utcOffsetLabel } from '@/lib/notifications'
import { useAuthStore } from '@/stores/auth'
import { useGroupsStore } from '@/stores/groups'
import GroupFormSheet from './groups/GroupFormSheet.vue'

const auth = useAuthStore()
const store = useGroupsStore()
const { money } = useCurrency()
const canWrite = computed(() => auth.can('users.write'))

const groups = computed(() => store.sorted)
onMounted(() => void store.load(true))
function refresh() {
  void store.load(true)
}

function multiplierClass(m: string): string {
  return MULTIPLIER_TONE_CLASSES[multiplierTone(m)]
}

function limits(g: UserGroup) {
  return limitParts(normalizeLimits(g.limits), money)
}
function limitsTitle(g: UserGroup): string {
  const l = normalizeLimits(g.limits)
  return [
    `每分钟请求：${l.rpm ?? '不限'}`,
    `每天请求：${l.rpd ?? '不限'}`,
    `每天消费：${l.dailySpend ? money(l.dailySpend) : '不限'}`,
    `每月消费：${l.monthlySpend ? money(l.monthlySpend) : '不限'}`,
  ].join('\n')
}

// ---------- create / edit ----------
const sheetOpen = ref(false)
const editing = ref<UserGroup | null>(null)
function openCreate() {
  editing.value = null
  sheetOpen.value = true
}
function openEdit(g: UserGroup) {
  editing.value = g
  sheetOpen.value = true
}
function onSaved(g: UserGroup) {
  store.upsert(g)
}

// ---------- set default / delete ----------
type Pending = { kind: 'default' | 'delete', group: UserGroup }
const pending = ref<Pending | null>(null)
const confirmOpen = ref(false)
const busy = ref(false)
function ask(kind: Pending['kind'], group: UserGroup) {
  pending.value = { kind, group }
  confirmOpen.value = true
}
async function confirmPending() {
  const p = pending.value
  if (!p)
    return
  busy.value = true
  try {
    if (p.kind === 'default') {
      const updated = await groupsApi.update(p.group.id, { isDefault: true, version: p.group.version })
      store.upsert(updated)
      toast.success(`「${updated.name}」已设为默认分组`)
    }
    else {
      await groupsApi.remove(p.group.id)
      toast.success(`已删除分组「${p.group.name}」`, { description: p.group.members ? `${p.group.members} 位成员已移到默认分组` : undefined })
      // Member counts of the default group changed: reload.
      await store.load(true)
    }
    confirmOpen.value = false
  }
  catch (err) {
    confirmOpen.value = false
    if (isVersionConflict(err)) {
      toast.warning('分组已被他人修改，已刷新列表')
      void store.load(true)
    }
    else {
      toast.error(p.kind === 'default' ? '设置失败' : '删除失败', { description: isApiError(err) && GROUP_ERROR_MESSAGES[err.code] ? GROUP_ERROR_MESSAGES[err.code] : errorMessage(err) })
    }
  }
  finally {
    busy.value = false
  }
}
const defaultName = computed(() => store.defaultGroup?.name ?? '默认')
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="用户组" description="用户组决定成员调用平台渠道时的售价倍率与用量限额；渠道也可以按分组共享。每位用户恰好属于一个分组，新用户加入默认分组。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="store.loading" @click="refresh">
          <RefreshCw :class="store.loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button v-if="canWrite" size="sm" data-testid="group-create" @click="openCreate">
          <Plus />
          新建分组
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <p v-if="!canWrite" class="text-muted-foreground text-xs">
          你只有查看权限（缺少 users.write），无法新建或修改分组。
        </p>
        <ErrorState v-if="store.error && !store.items" :error="store.error" @retry="refresh" />
        <div v-else-if="!store.items" class="space-y-2">
          <Skeleton v-for="i in 4" :key="i" class="h-12 w-full" />
        </div>
        <EmptyState v-else-if="groups.length === 0" :icon="UsersRound" title="暂无用户组" description="后端升级后会自动创建「默认」分组并放入全部用户。" />
        <div v-else class="overflow-x-auto" :class="store.loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>分组</TableHead>
                <TableHead>价格倍率</TableHead>
                <TableHead>用量限额</TableHead>
                <TableHead class="hidden lg:table-cell">
                  时区
                </TableHead>
                <TableHead class="text-right">
                  成员
                </TableHead>
                <TableHead v-if="canWrite" class="w-10">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="g in groups" :key="g.id" :data-group-id="g.id">
                <TableCell>
                  <div class="flex max-w-72 min-w-36 flex-col gap-0.5">
                    <div class="flex min-w-0 items-center gap-1.5">
                      <button v-if="canWrite" type="button" class="hover:text-primary truncate text-left font-medium hover:underline" @click="openEdit(g)">
                        {{ g.name }}
                      </button>
                      <span v-else class="truncate font-medium">{{ g.name }}</span>
                      <Badge v-if="g.isDefault" variant="secondary" class="h-4 shrink-0 px-1.5 text-[10px]" title="新用户加入此分组；不能删除" data-testid="default-badge">
                        默认
                      </Badge>
                    </div>
                    <p v-if="g.description" class="text-muted-foreground truncate text-xs" :title="g.description">
                      {{ g.description }}
                    </p>
                  </div>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="font-normal tabular-nums" :class="multiplierClass(g.priceMultiplier)" :title="`平台渠道售价 ${multiplierShort(g.priceMultiplier)}`" data-testid="group-multiplier">
                    {{ multiplierShort(g.priceMultiplier) }} / {{ multiplierWords(g.priceMultiplier) }}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div class="flex max-w-80 flex-wrap gap-1" :title="limitsTitle(g)" data-testid="group-limits">
                    <Badge v-for="p in limits(g)" :key="p.key" variant="secondary" class="font-normal tabular-nums">
                      {{ p.text }}
                    </Badge>
                    <span v-if="limits(g).length === 0" class="text-muted-foreground text-xs">不限</span>
                  </div>
                </TableCell>
                <TableCell class="text-muted-foreground hidden text-xs lg:table-cell">
                  {{ g.timezone }} <span class="tabular-nums">{{ utcOffsetLabel(g.timezone) }}</span>
                </TableCell>
                <TableCell class="text-right">
                  <RouterLink
                    :to="{ name: 'admin-users', query: { groupId: g.id } }"
                    class="text-primary tabular-nums hover:underline"
                    :title="`查看「${g.name}」的成员`"
                    data-testid="group-members"
                  >
                    {{ g.members.toLocaleString('zh-CN') }} 人
                  </RouterLink>
                </TableCell>
                <TableCell v-if="canWrite">
                  <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon-sm" :aria-label="`${g.name} 的操作`">
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem @select="openEdit(g)">
                        <Pencil />
                        编辑
                      </DropdownMenuItem>
                      <DropdownMenuItem :disabled="g.isDefault" @select="ask('default', g)">
                        <Star />
                        设为默认
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="destructive" :disabled="g.isDefault" :title="g.isDefault ? '默认分组不能删除' : undefined" @select="ask('delete', g)">
                        <Trash2 />
                        {{ g.isDefault ? '默认分组不能删除' : '删除' }}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <p v-if="store.items?.length" class="text-muted-foreground text-xs">
          倍率作用于平台渠道售价（与分时价格倍率相乘）；限额按每位成员计算。点击成员数查看该分组的用户。
        </p>
      </CardContent>
    </Card>

    <GroupFormSheet v-if="canWrite" v-model:open="sheetOpen" :group="editing" :default-group="store.defaultGroup" @saved="onSaved" @stale="refresh" />

    <ConfirmDialog
      v-model:open="confirmOpen"
      :title="pending?.kind === 'delete' ? `删除分组「${pending.group.name}」？` : `把「${pending?.group.name ?? ''}」设为默认分组？`"
      :confirm-text="pending?.kind === 'delete' ? '删除' : '设为默认'"
      :destructive="pending?.kind === 'delete'"
      :loading="busy"
      @confirm="confirmPending"
    >
      <template v-if="pending?.kind === 'delete'">
        <p v-if="pending.group.members">
          该分组的 <strong class="text-foreground">{{ pending.group.members }} 位成员</strong>将移到默认分组「{{ defaultName }}」，按默认分组的倍率与限额计费。
        </p>
        <p v-else>
          该分组没有成员。
        </p>
        <p>共享给该分组的渠道将不再对其成员可用。此操作不可撤销。</p>
      </template>
      <template v-else-if="pending">
        <p>之后首次登录的新用户将加入「{{ pending.group.name }}」；「{{ defaultName }}」不再是默认分组。已有用户的分组不变。</p>
      </template>
    </ConfirmDialog>
  </div>
</template>
