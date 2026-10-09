<script setup lang="ts">
import type { AlertsSummary } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { ArrowRight, BellRing, CircleCheck, CircleX, RefreshCw, TriangleAlert } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { errorMessage, isApiError } from '@/lib/api'
import { alertsApi } from '@/lib/endpoints'
import { formatDateTime, formatRelative } from '@/lib/format'
import { formatUiValue } from '@/lib/pluginFormat'
import { isAbortError } from '@/lib/query'
import NotificationRow from './NotificationRow.vue'

/**
 * Overview card "告警与上游余额" (phase6-api.md §5): health counts, upstream balances
 * and recent alert notifications of the channels the user can manage. Hidden when the
 * user manages no channels (or the backend does not offer the endpoint yet).
 */
const props = defineProps<{
  /** Show a skeleton while loading (users likely to manage channels). */
  expectChannels: boolean
}>()

const summary = ref<AlertsSummary | null>(null)
const error = ref<unknown>(null)
const unsupported = ref(false)
const loading = ref(false)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  error.value = null
  try {
    const res = await alertsApi.summary(ctrl.signal)
    summary.value = {
      channels: res?.channels ?? { total: 0, healthy: 0, degraded: 0, down: 0 },
      balances: res?.balances ?? [],
      recent: res?.recent ?? [],
    }
  }
  catch (err) {
    if (isAbortError(err))
      return
    if (isApiError(err) && (err.status === 404 || err.status === 501))
      unsupported.value = true
    else
      error.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}
onMounted(load)
onBeforeUnmount(() => controller?.abort())

const now = ref(Date.now())
useIntervalFn(() => {
  now.value = Date.now()
}, 30_000)

const visible = computed(() => {
  if (unsupported.value)
    return false
  if (error.value)
    return props.expectChannels
  if (!summary.value)
    return props.expectChannels
  const s = summary.value
  return s.channels.total > 0 || s.balances.length > 0 || s.recent.length > 0
})

const lowCount = computed(() => summary.value?.balances.filter(b => b.low).length ?? 0)
const recent = computed(() => (summary.value?.recent ?? []).slice(0, 5))
const balances = computed(() => [...(summary.value?.balances ?? [])].sort((a, b) => Number(b.low) - Number(a.low) || a.channelName.localeCompare(b.channelName, 'zh-CN')))

function money(value: string | null, currency: string) {
  return formatUiValue(value, 'money', { currency })
}

const PILLS = [
  { key: 'healthy', label: '健康', icon: CircleCheck, cls: 'border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-400' },
  { key: 'degraded', label: '降级', icon: TriangleAlert, cls: 'border-amber-500/40 bg-amber-500/5 text-amber-700 dark:text-amber-400' },
  { key: 'down', label: '异常', icon: CircleX, cls: 'border-destructive/40 bg-destructive/5 text-destructive' },
] as const
</script>

<template>
  <Card v-if="visible" data-testid="alerts-card">
    <CardHeader>
      <CardTitle class="flex items-center gap-2">
        <BellRing class="size-4" />
        告警与上游余额
      </CardTitle>
      <CardDescription>
        <template v-if="summary">
          你可管理的 {{ summary.channels.total }} 个渠道<template v-if="lowCount">
            ，<span class="text-destructive font-medium">{{ lowCount }} 个上游余额偏低</span>
          </template>
        </template>
        <template v-else>
          你可管理的渠道的健康状态与上游余额
        </template>
      </CardDescription>
      <CardAction>
        <Button variant="ghost" size="sm" as-child>
          <RouterLink to="/console/notifications?category=channel">
            通知中心
            <ArrowRight />
          </RouterLink>
        </Button>
      </CardAction>
    </CardHeader>

    <CardContent v-if="error && !summary">
      <p class="text-destructive text-sm">
        无法加载告警摘要：{{ errorMessage(error) }}
        <Button variant="link" size="sm" class="h-auto p-0" @click="load">
          重试
        </Button>
      </p>
    </CardContent>

    <CardContent v-else-if="!summary" class="space-y-3">
      <div class="flex gap-2">
        <Skeleton v-for="i in 3" :key="i" class="h-7 w-20 rounded-full" />
      </div>
      <Skeleton class="h-24 w-full" />
    </CardContent>

    <CardContent v-else class="grid gap-6 lg:grid-cols-2">
      <div class="min-w-0 space-y-4">
        <div class="flex flex-wrap items-center gap-2" data-testid="health-pills">
          <RouterLink
            v-for="p in PILLS"
            :key="p.key"
            to="/console/channels"
            class="focus-visible:ring-ring/50 inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium outline-none hover:opacity-80 focus-visible:ring-3"
            :class="summary.channels[p.key] > 0 || p.key === 'healthy' ? p.cls : 'text-muted-foreground'"
            :data-testid="`health-${p.key}`"
          >
            <component :is="p.icon" class="size-3.5" />
            {{ p.label }}
            <span class="tabular-nums">{{ summary.channels[p.key] }}</span>
          </RouterLink>
        </div>

        <div class="space-y-2">
          <h3 class="text-sm font-medium">
            上游余额
          </h3>
          <p v-if="balances.length === 0" class="text-muted-foreground text-xs">
            暂无数据：渠道的插件提供 balance 能力并完成一次查询后，余额会显示在这里。
          </p>
          <div v-else class="divide-y rounded-lg border text-sm" data-testid="balances">
            <div class="text-muted-foreground hidden grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1fr)_5.5rem] gap-3 px-3 py-1.5 text-xs sm:grid">
              <span>渠道</span><span class="text-right">余额</span><span class="text-right">告警阈值</span><span class="text-right">查询时间</span>
            </div>
            <div
              v-for="b in balances"
              :key="b.channelId"
              class="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-0.5 px-3 py-2 sm:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1fr)_5.5rem] sm:items-center"
              :class="b.low ? 'bg-destructive/5' : ''"
              :data-low="b.low ? 'true' : undefined"
              data-testid="balance-row"
            >
              <RouterLink :to="`/console/channels/${encodeURIComponent(b.channelId)}`" class="truncate font-medium hover:underline" :title="b.channelName">
                {{ b.channelName }}
              </RouterLink>
              <span class="flex items-center justify-end gap-1.5 text-right tabular-nums">
                <template v-if="b.available">
                  <Badge v-if="b.low" variant="destructive" class="h-4 px-1.5 text-[10px]">偏低</Badge>
                  <span :class="b.low ? 'text-destructive font-semibold' : ''" :title="money(b.total, b.currency).title">{{ money(b.total, b.currency).text }}</span>
                </template>
                <span v-else class="text-muted-foreground text-xs">无法获取</span>
              </span>
              <span class="text-muted-foreground text-xs sm:text-right sm:text-sm">
                <span class="sm:hidden">阈值 </span>{{ b.threshold ? money(b.threshold, b.currency).text : '未设置' }}
              </span>
              <Tooltip>
                <TooltipTrigger as-child>
                  <time :datetime="b.checkedAt" tabindex="0" class="text-muted-foreground justify-self-end text-xs">{{ formatRelative(b.checkedAt, now) }}</time>
                </TooltipTrigger>
                <TooltipContent>{{ formatDateTime(b.checkedAt) }}</TooltipContent>
              </Tooltip>
            </div>
          </div>
        </div>
      </div>

      <div class="min-w-0 space-y-2">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-sm font-medium">
            最近告警
          </h3>
          <Button variant="ghost" size="icon-xs" :disabled="loading" aria-label="刷新告警摘要" @click="load">
            <RefreshCw :class="loading ? 'animate-spin' : ''" />
          </Button>
        </div>
        <p v-if="recent.length === 0" class="text-muted-foreground rounded-lg border border-dashed px-3 py-4 text-center text-xs">
          最近没有渠道告警。
        </p>
        <ul v-else class="divide-y" data-testid="recent-alerts">
          <NotificationRow v-for="n in recent" :key="n.id" :notification="n" :now="now" compact />
        </ul>
        <Button v-if="(summary.recent.length ?? 0) > recent.length" variant="link" size="sm" class="h-auto p-0" as-child>
          <RouterLink to="/console/notifications?category=channel">
            查看全部 {{ summary.recent.length }} 条
          </RouterLink>
        </Button>
      </div>
    </CardContent>
  </Card>
</template>
