<script setup lang="ts">
import type { IncomingShare } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { Check, Inbox, LogOut, ShieldAlert, X } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { errorMessage, isApiError } from '@/lib/api'
import { formatDateTime, formatRelative } from '@/lib/format'
import { CHANNEL_TYPE_LABELS } from '@/lib/labels'
import { SHARE_STATUS_CLASSES, SHARE_STATUS_LABELS, splitIncoming } from '@/lib/shares'
import { useChannelSharesStore } from '@/stores/channelShares'

/**
 * "共享给我的" (phase5-api.md §5.3 / §5.6): invitations to accept or decline and accepted
 * shares to leave. Accepting is preceded by a privacy warning: the owner's upstream sees
 * the requests sent through the channel.
 */
const store = useChannelSharesStore()
onMounted(() => void store.load())

const sections = computed(() => {
  const g = splitIncoming(store.items ?? [])
  return [
    { key: 'pending', title: '待接受的邀请', list: g.pending },
    { key: 'accepted', title: '已接受的共享', list: g.accepted },
  ].filter(s => s.list.length > 0)
})
const MODEL_PREVIEW = 6

type Action = 'accept' | 'decline' | 'leave'
const target = ref<IncomingShare | null>(null)
const action = ref<Action>('accept')
const dialogOpen = ref(false)
const busy = ref(false)

function ask(item: IncomingShare, a: Action) {
  target.value = item
  action.value = a
  dialogOpen.value = true
}

const DIALOG: Record<Action, { title: (n: string) => string, confirm: string, destructive: boolean }> = {
  accept: { title: n => `接受共享渠道「${n}」？`, confirm: '我了解，接受', destructive: false },
  decline: { title: n => `拒绝共享渠道「${n}」？`, confirm: '拒绝', destructive: true },
  leave: { title: n => `退出共享渠道「${n}」？`, confirm: '退出共享', destructive: true },
}

async function confirm() {
  const item = target.value
  if (!item)
    return
  busy.value = true
  try {
    if (action.value === 'accept') {
      await store.accept(item.channelId)
      toast.success(`已接受「${item.name}」`, { description: '该渠道会在平台渠道之前被尝试，可以随时退出共享。' })
    }
    else if (action.value === 'decline') {
      await store.decline(item.channelId)
      toast.success(`已拒绝「${item.name}」`)
    }
    else {
      await store.leave(item.channelId)
      toast.success(`已退出「${item.name}」的共享`, { description: '你的请求不会再经过该渠道。' })
    }
    dialogOpen.value = false
  }
  catch (err) {
    dialogOpen.value = false
    const gone = isApiError(err) && (err.status === 404 || err.code === 'share_state_conflict')
    toast.error('操作失败', { description: gone ? '共享状态已变化（可能已被所有者收回），已刷新列表' : errorMessage(err) })
    if (gone)
      await store.load()
  }
  finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="space-y-4" data-testid="incoming-shares">
    <div class="bg-muted/40 text-muted-foreground flex gap-2.5 rounded-lg border p-3 text-sm">
      <ShieldAlert class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
      <div class="space-y-1">
        <p>
          其他用户可以邀请你使用他们的渠道。<strong class="text-foreground font-medium">接受后</strong>，该渠道会在平台渠道<strong class="text-foreground font-medium">之前</strong>被尝试，且不计费（上游费用由所有者承担）。
        </p>
        <p>
          所有者看不到你账户的任何信息，但<strong class="text-foreground font-medium">能看到经由他的渠道发送的请求与响应</strong>。只接受你信任的人的邀请，可以随时退出共享。
        </p>
      </div>
    </div>

    <ErrorState v-if="store.error && !store.items" :error="store.error" @retry="store.load" />

    <div v-else-if="!store.items" class="space-y-2">
      <Skeleton v-for="i in 3" :key="i" class="h-20 w-full" />
    </div>

    <EmptyState
      v-else-if="store.items.length === 0"
      :icon="Inbox"
      title="没有共享给你的渠道"
      description="其他用户邀请你使用他们的渠道时，会在这里等待你接受。"
    />

    <template v-else>
      <Card v-for="section in sections" :key="section.key" :data-testid="`incoming-${section.key}`">
        <CardHeader>
          <CardTitle class="text-sm">
            {{ section.title }}<span class="text-muted-foreground font-normal tabular-nums">（{{ section.list.length }}）</span>
          </CardTitle>
          <CardDescription v-if="section.key === 'pending'">
            接受前不会有任何请求经过这些渠道。
          </CardDescription>
          <CardDescription v-else>
            你的请求会先尝试这些渠道，失败后再回退到平台渠道。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ul class="divide-y">
            <li v-for="item in section.list" :key="item.channelId" class="flex flex-col gap-3 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-start" data-testid="incoming-share">
              <div class="min-w-0 flex-1 space-y-1.5">
                <div class="flex flex-wrap items-center gap-1.5">
                  <span class="truncate font-medium">{{ item.name }}</span>
                  <Badge variant="outline">
                    {{ CHANNEL_TYPE_LABELS[item.type] ?? item.type }}
                  </Badge>
                  <Badge variant="outline" :class="SHARE_STATUS_CLASSES[item.status]" data-testid="incoming-status">
                    {{ SHARE_STATUS_LABELS[item.status] }}
                  </Badge>
                  <Badge v-if="item.channelStatus === 'disabled'" variant="destructive">
                    已停用
                  </Badge>
                </div>
                <p class="text-muted-foreground text-xs">
                  所有者 <span class="text-foreground">{{ item.owner.displayName }}</span>
                  ·
                  <span :title="formatDateTime(item.createdAt)">{{ item.status === 'pending' ? '邀请于' : '接受于' }} {{ formatRelative(item.status === 'accepted' && item.respondedAt ? item.respondedAt : item.createdAt) }}</span>
                </p>
                <div v-if="item.models.length" class="flex flex-wrap gap-1">
                  <Badge v-for="m in item.models.slice(0, MODEL_PREVIEW)" :key="m" variant="secondary" class="max-w-full font-mono text-[11px] font-normal">
                    <span class="truncate">{{ m }}</span>
                  </Badge>
                  <span v-if="item.models.length > MODEL_PREVIEW" class="text-muted-foreground self-center text-xs" :title="item.models.slice(MODEL_PREVIEW).join('、')">
                    等 {{ item.models.length }} 个模型
                  </span>
                </div>
              </div>
              <div class="flex shrink-0 gap-2">
                <template v-if="item.status === 'pending'">
                  <Button size="sm" data-testid="share-accept" @click="ask(item, 'accept')">
                    <Check />
                    接受
                  </Button>
                  <Button size="sm" variant="outline" data-testid="share-decline" @click="ask(item, 'decline')">
                    <X />
                    拒绝
                  </Button>
                </template>
                <Button v-else size="sm" variant="outline" data-testid="share-leave" @click="ask(item, 'leave')">
                  <LogOut />
                  退出共享
                </Button>
              </div>
            </li>
          </ul>
        </CardContent>
      </Card>
    </template>

    <ConfirmDialog
      v-model:open="dialogOpen"
      :title="DIALOG[action].title(target?.name ?? '')"
      :confirm-text="DIALOG[action].confirm"
      :destructive="DIALOG[action].destructive"
      :loading="busy"
      @confirm="confirm"
    >
      <template v-if="action === 'accept'">
        <p data-testid="share-privacy-warning">
          <strong class="text-foreground">隐私提示：</strong>渠道所有者「{{ target?.owner.displayName }}」能看到（也可能修改）你经由该渠道发送的全部请求内容与响应，包括提示词、上传的文件与模型输出。
        </p>
        <p>所有者看不到你账户的其他信息（钱包、API Key、请求日志等）。</p>
        <p>接受后，你对 {{ target?.models.length ?? 0 }} 个模型的请求会<strong class="text-foreground">先尝试该渠道</strong>（不计费，费用由所有者承担），失败时才回退到平台渠道。只接受你信任的人的邀请；你可以随时退出共享。</p>
      </template>
      <template v-else-if="action === 'decline'">
        <p>拒绝后该渠道不会被你使用，所有者会看到“已拒绝”。所有者之后可以重新邀请你。</p>
      </template>
      <template v-else>
        <p>退出后你的请求不再经过该渠道（对应模型改由你自己的渠道或平台渠道提供，平台渠道照常计费）。所有者会看到“已拒绝”，之后可以重新邀请你。</p>
      </template>
    </ConfirmDialog>
  </div>
</template>
