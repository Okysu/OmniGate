<script setup lang="ts">
import type { Session } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { LogOut, Monitor, RefreshCw } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import GithubIcon from '@/components/GithubIcon.vue'
import PageHeader from '@/components/PageHeader.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { errorMessage } from '@/lib/api'
import { meApi } from '@/lib/endpoints'
import {
  describeUserAgent,
  formatDateTime,
  formatRelative,
  PROVIDER_LABELS,
  ROLE_DESCRIPTIONS,
  ROLE_LABELS,
  STATUS_LABELS,
} from '@/lib/format'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const user = computed(() => auth.user)

const sessions = ref<Session[]>([])
const sessionsLoading = ref(true)
const sessionsError = ref<unknown>(null)

const otherSessionCount = computed(() => sessions.value.filter(s => !s.current).length)

async function loadSessions() {
  sessionsLoading.value = true
  sessionsError.value = null
  try {
    const res = await meApi.sessions()
    // Current session first, then most recently active.
    sessions.value = [...res.items].sort((a, b) =>
      Number(b.current) - Number(a.current) || b.lastSeenAt.localeCompare(a.lastSeenAt))
  }
  catch (err) {
    sessionsError.value = err
  }
  finally {
    sessionsLoading.value = false
  }
}

// --- revoke a single session ---
const revokeTarget = ref<Session | null>(null)
const revokeOpen = ref(false)
const revoking = ref(false)

function askRevoke(s: Session) {
  revokeTarget.value = s
  revokeOpen.value = true
}

async function confirmRevoke() {
  const target = revokeTarget.value
  if (!target)
    return
  revoking.value = true
  try {
    await meApi.revokeSession(target.id)
    sessions.value = sessions.value.filter(s => s.id !== target.id)
    toast.success('已撤销该会话')
    revokeOpen.value = false
  }
  catch (err) {
    toast.error('撤销失败', { description: errorMessage(err) })
  }
  finally {
    revoking.value = false
  }
}

// --- revoke all other sessions ---
const revokeOthersOpen = ref(false)
const revokingOthers = ref(false)

async function confirmRevokeOthers() {
  revokingOthers.value = true
  try {
    const res = await meApi.revokeOtherSessions()
    toast.success(res.revoked > 0 ? `已退出其他 ${res.revoked} 个设备` : '没有其他需要退出的设备')
    revokeOthersOpen.value = false
    await loadSessions()
  }
  catch (err) {
    toast.error('操作失败', { description: errorMessage(err) })
  }
  finally {
    revokingOthers.value = false
  }
}

onMounted(() => {
  void loadSessions()
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="个人设置" description="查看你的账号信息、已关联的登录方式与活跃会话。" />

    <div v-if="user" class="grid gap-4 lg:grid-cols-3">
      <Card class="lg:col-span-2">
        <CardHeader>
          <CardTitle>账号信息</CardTitle>
          <CardDescription>资料由身份提供方同步，暂不支持在此修改。</CardDescription>
        </CardHeader>
        <CardContent class="space-y-5">
          <div class="flex items-center gap-4">
            <UserAvatar :name="user.displayName" :src="user.avatarUrl" size="lg" />
            <div class="min-w-0">
              <p class="truncate text-lg font-medium">
                {{ user.displayName }}
              </p>
              <p class="text-muted-foreground truncate text-sm">
                {{ user.email ?? '未设置邮箱' }}
              </p>
            </div>
          </div>
          <dl class="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
            <div>
              <dt class="text-muted-foreground">
                角色
              </dt>
              <dd class="mt-0.5 flex flex-col gap-1">
                <span><Badge variant="secondary">{{ ROLE_LABELS[user.role] }}</Badge></span>
                <span class="text-muted-foreground text-xs">{{ ROLE_DESCRIPTIONS[user.role] }}</span>
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                状态
              </dt>
              <dd class="mt-0.5">
                <Badge :variant="user.status === 'active' ? 'outline' : 'destructive'">
                  {{ STATUS_LABELS[user.status] }}
                </Badge>
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                注册时间
              </dt>
              <dd class="mt-0.5">
                {{ formatDateTime(user.createdAt) }}
              </dd>
            </div>
            <div>
              <dt class="text-muted-foreground">
                上次登录
              </dt>
              <dd class="mt-0.5">
                {{ formatDateTime(user.lastLoginAt) }}
              </dd>
            </div>
            <div class="sm:col-span-2">
              <dt class="text-muted-foreground">
                用户 ID
              </dt>
              <dd class="mt-0.5 font-mono text-xs break-all">
                {{ user.id }}
              </dd>
            </div>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>已关联的登录方式</CardTitle>
          <CardDescription>可用于登录此账号的外部身份。</CardDescription>
        </CardHeader>
        <CardContent>
          <ul v-if="user.identities.length" class="divide-y">
            <li v-for="(idn, i) in user.identities" :key="`${idn.provider}-${i}`" class="flex items-center gap-3 py-2.5 first:pt-0 last:pb-0">
              <span class="bg-muted flex size-8 shrink-0 items-center justify-center rounded-md">
                <GithubIcon v-if="idn.provider === 'github'" class="size-4" />
                <span v-else class="text-xs font-medium uppercase">{{ idn.provider.slice(0, 2) }}</span>
              </span>
              <div class="min-w-0">
                <p class="text-sm font-medium">
                  {{ PROVIDER_LABELS[idn.provider] ?? idn.provider }}
                </p>
                <p class="text-muted-foreground truncate text-xs">
                  {{ idn.login ?? idn.email ?? '—' }}
                </p>
              </div>
            </li>
          </ul>
          <p v-else class="text-muted-foreground text-sm">
            暂无关联的身份。
          </p>
        </CardContent>
      </Card>
    </div>

    <Card>
      <CardHeader>
        <CardTitle>登录会话</CardTitle>
        <CardDescription>当前账号在各设备上的活跃会话。发现不认识的设备请立即撤销。</CardDescription>
        <CardAction class="flex gap-2">
          <Button variant="ghost" size="icon-sm" aria-label="刷新" :disabled="sessionsLoading" @click="loadSessions">
            <RefreshCw :class="sessionsLoading ? 'animate-spin' : ''" />
          </Button>
          <Button
            variant="outline"
            size="sm"
            :disabled="sessionsLoading || otherSessionCount === 0"
            @click="revokeOthersOpen = true"
          >
            <LogOut />
            <span class="hidden sm:inline">退出其他设备</span>
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent>
        <div v-if="sessionsLoading && sessions.length === 0" class="space-y-2">
          <Skeleton v-for="i in 3" :key="i" class="h-10 w-full" />
        </div>
        <ErrorState v-else-if="sessionsError" :error="sessionsError" @retry="loadSessions" />
        <EmptyState v-else-if="sessions.length === 0" title="没有活跃会话" :icon="Monitor" />
        <div v-else class="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>设备</TableHead>
                <TableHead>IP 段</TableHead>
                <TableHead>最近活跃</TableHead>
                <TableHead class="hidden md:table-cell">
                  登录时间
                </TableHead>
                <TableHead class="hidden md:table-cell">
                  过期时间
                </TableHead>
                <TableHead class="text-right">
                  操作
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="s in sessions" :key="s.id">
                <TableCell>
                  <div class="flex items-center gap-2">
                    <Tooltip>
                      <TooltipTrigger as-child>
                        <span class="cursor-default">{{ describeUserAgent(s.userAgent) }}</span>
                      </TooltipTrigger>
                      <TooltipContent class="max-w-sm break-all">
                        {{ s.userAgent || '未知 User-Agent' }}
                      </TooltipContent>
                    </Tooltip>
                    <Badge v-if="s.current" variant="secondary">
                      当前会话
                    </Badge>
                  </div>
                </TableCell>
                <TableCell class="font-mono text-xs">
                  {{ s.ipPrefix || '—' }}
                </TableCell>
                <TableCell :title="formatDateTime(s.lastSeenAt)">
                  {{ formatRelative(s.lastSeenAt) }}
                </TableCell>
                <TableCell class="hidden md:table-cell">
                  {{ formatDateTime(s.createdAt) }}
                </TableCell>
                <TableCell class="hidden md:table-cell">
                  {{ formatDateTime(s.expiresAt) }}
                </TableCell>
                <TableCell class="text-right">
                  <span v-if="s.current" class="text-muted-foreground text-xs">使用“退出登录”结束</span>
                  <Button v-else variant="ghost" size="sm" class="text-destructive" @click="askRevoke(s)">
                    撤销
                  </Button>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>

    <ConfirmDialog
      v-model:open="revokeOpen"
      title="撤销此会话？"
      confirm-text="撤销"
      destructive
      :loading="revoking"
      @confirm="confirmRevoke"
    >
      <p v-if="revokeTarget">
        设备 <strong class="text-foreground">{{ describeUserAgent(revokeTarget.userAgent) }}</strong>（{{ revokeTarget.ipPrefix || '未知 IP' }}）将立即退出登录，需要重新登录才能继续使用。
      </p>
    </ConfirmDialog>

    <ConfirmDialog
      v-model:open="revokeOthersOpen"
      title="退出其他所有设备？"
      :description="`除当前浏览器外的 ${otherSessionCount} 个会话将立即失效，这些设备需要重新登录。当前会话不受影响。`"
      confirm-text="全部退出"
      destructive
      :loading="revokingOthers"
      @confirm="confirmRevokeOthers"
    />
  </div>
</template>
