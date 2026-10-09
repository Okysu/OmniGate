<script setup lang="ts">
import type { ColumnSeries } from '@/components/charts/ColumnChart.vue'
import type { StatsSummary, User } from '@/lib/types'
import { computed, defineAsyncComponent, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Activity, ArrowDownToLine, ArrowUpFromLine, CircleCheck, Coins, Gauge, RefreshCw, Timer, UserRound, Wallet } from '@lucide/vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatTile from '@/components/StatTile.vue'
import TimeRangePicker from '@/components/TimeRangePicker.vue'
import UserPicker from '@/components/UserPicker.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { useRangeQuery } from '@/composables/useRangeQuery'
import { logsApi } from '@/lib/endpoints'
import { formatCompact, formatDateTime, formatMs, formatNumber, formatPercent } from '@/lib/format'
import { isAbortError, queryStr } from '@/lib/query'
import { errorRate, fillDaily, shortDate } from '@/lib/stats'
import { useAuthStore } from '@/stores/auth'

const ColumnChart = defineAsyncComponent(() => import('@/components/charts/ColumnChart.vue'))

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

const seeAll = computed(() => auth.can('stats.all'))
const range = useRangeQuery('7d', ['24h', '7d', '30d', 'custom'])
const userId = computed(() => (seeAll.value ? queryStr(route.query.userId) : ''))

const data = ref<StatsSummary | null>(null)
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
    const { from, to } = range.resolve()
    data.value = await logsApi.summary({ from, to, userId: userId.value || undefined }, ctrl.signal)
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
watch(() => [range.preset.value, range.customFrom.value, range.customTo.value, userId.value], load, { immediate: true })
onBeforeUnmount(() => controller?.abort())

// User filter
const userPick = ref<string[]>(userId.value ? [userId.value] : [])
const userLabel = ref<string | null>(null)
const userPopover = ref(false)
watch(userPick, (v) => {
  void router.replace({ query: { ...route.query, userId: v[0] || undefined } })
})
function onUserPicked(u: User | null) {
  userLabel.value = u?.displayName ?? null
  userPopover.value = false
}

const totals = computed(() => data.value?.totals ?? null)
const daily = computed(() => (data.value ? fillDaily(data.value.daily, data.value.from, data.value.to) : []))
const categories = computed(() => daily.value.map(d => d.date))

const requestSeries = computed<ColumnSeries[]>(() => [
  { key: 'success', label: '成功', color: 'blue', values: daily.value.map(d => d.requests - d.errors) },
  { key: 'errors', label: '失败', color: 'red', values: daily.value.map(d => d.errors) },
])
const tokenSeries = computed<ColumnSeries[]>(() => [
  { key: 'input', label: '输入 Tokens', color: 'blue', values: daily.value.map(d => d.inputTokens) },
  { key: 'output', label: '输出 Tokens', color: 'orange', values: daily.value.map(d => d.outputTokens) },
])
const showDailyTable = ref(false)

const latencyHint = computed(() => {
  const t = totals.value
  if (!t || t.latencyP50Ms == null)
    return undefined
  return `P50 ${formatMs(t.latencyP50Ms)} · P99 ${formatMs(t.latencyP99Ms)}`
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="统计" :description="seeAll ? '请求量、成功率、延迟与费用（全部用户，可按用户筛选）。日期按 UTC 聚合。' : '你的请求量、成功率、延迟与费用。日期按 UTC 聚合。'">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
      <TimeRangePicker
        v-model:preset="range.preset.value"
        :presets="['24h', '7d', '30d', 'custom']"
        :custom-from="range.customFromLocal.value"
        :custom-to="range.customToLocal.value"
        @apply="range.applyCustom"
      />
      <Popover v-if="seeAll" v-model:open="userPopover">
        <PopoverTrigger as-child>
          <Button variant="outline" class="justify-start font-normal">
            <UserRound />
            <span class="truncate">{{ userId ? (userLabel ?? `${userId.slice(0, 8)}…`) : '全部用户' }}</span>
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" class="w-80 gap-2">
          <Label>按用户统计</Label>
          <UserPicker v-model="userPick" :multiple="false" @pick="onUserPicked" />
        </PopoverContent>
      </Popover>
      <p v-if="data" class="text-muted-foreground text-xs sm:ml-auto">
        {{ formatDateTime(data.from) }} – {{ formatDateTime(data.to) }}
      </p>
    </div>

    <ErrorState v-if="loadError" :error="loadError" @retry="load" />
    <template v-else>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
        <StatTile label="请求数" :icon="Activity" :value="formatNumber(totals?.requests)" :hint="totals ? `失败 ${formatNumber(totals.errors)}` : undefined" :loading="loading && !totals" />
        <StatTile label="成功率" :icon="CircleCheck" :value="totals && totals.requests > 0 ? formatPercent(totals.successRate) : '—'" :loading="loading && !totals" />
        <StatTile label="延迟 P95" :icon="Timer" :value="formatMs(totals?.latencyP95Ms)" :hint="latencyHint" :loading="loading && !totals" />
        <StatTile label="首字节 TTFT P50" :icon="Gauge" :value="formatMs(totals?.ttftP50Ms)" :loading="loading && !totals" />
        <StatTile label="输入 Tokens" :icon="ArrowDownToLine" :value="formatCompact(totals?.inputTokens)" :title="formatNumber(totals?.inputTokens)" hint="含缓存读写" :loading="loading && !totals" />
        <StatTile label="输出 Tokens" :icon="ArrowUpFromLine" :value="formatCompact(totals?.outputTokens)" :title="formatNumber(totals?.outputTokens)" :loading="loading && !totals" />
        <StatTile label="收费" :icon="Wallet" :value="money(totals?.charge)" :loading="loading && !totals" />
        <StatTile v-if="totals?.cost != null" label="上游成本" :icon="Coins" :value="money(totals.cost)" :loading="loading && !totals" />
      </div>

      <div class="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle class="text-base">
              每日请求
            </CardTitle>
            <CardDescription>成功与失败请求数（UTC 日期）</CardDescription>
          </CardHeader>
          <CardContent>
            <Skeleton v-if="loading && !data" class="h-56 w-full" />
            <EmptyState v-else-if="!totals?.requests" title="暂无数据" description="所选时间范围内没有请求。" />
            <ColumnChart
              v-else
              chart-label="每日请求数柱状图"
              :categories="categories"
              :category-label="shortDate"
              :series="requestSeries"
              stacked
            />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle class="text-base">
              每日 Tokens
            </CardTitle>
            <CardDescription>输入（含缓存）与输出 Token 数（UTC 日期）</CardDescription>
          </CardHeader>
          <CardContent>
            <Skeleton v-if="loading && !data" class="h-56 w-full" />
            <EmptyState v-else-if="!totals?.requests" title="暂无数据" description="所选时间范围内没有请求。" />
            <ColumnChart
              v-else
              chart-label="每日 Token 数柱状图"
              :categories="categories"
              :category-label="shortDate"
              :series="tokenSeries"
              :format-value="(n: number) => formatNumber(n)"
            />
          </CardContent>
        </Card>
      </div>

      <Card v-if="totals?.requests">
        <CardHeader>
          <div class="flex items-center justify-between gap-2">
            <CardTitle class="text-base">
              每日明细
            </CardTitle>
            <Button variant="ghost" size="sm" @click="showDailyTable = !showDailyTable">
              {{ showDailyTable ? '收起' : '查看数据表' }}
            </Button>
          </div>
        </CardHeader>
        <CardContent v-if="showDailyTable" class="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>日期（UTC）</TableHead>
                <TableHead class="text-right">
                  请求
                </TableHead>
                <TableHead class="text-right">
                  失败
                </TableHead>
                <TableHead class="text-right">
                  输入 Tokens
                </TableHead>
                <TableHead class="text-right">
                  输出 Tokens
                </TableHead>
                <TableHead class="text-right">
                  收费
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="d in daily" :key="d.date">
                <TableCell class="tabular-nums">
                  {{ d.date }}
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ formatNumber(d.requests) }}
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ formatNumber(d.errors) }}
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ formatNumber(d.inputTokens) }}
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ formatNumber(d.outputTokens) }}
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ money(d.charge) }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <div class="grid gap-4 xl:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle class="text-base">
              按模型
            </CardTitle>
            <CardDescription>请求量前 50 的模型</CardDescription>
          </CardHeader>
          <CardContent class="overflow-x-auto">
            <Skeleton v-if="loading && !data" class="h-32 w-full" />
            <p v-else-if="!data?.byModel.length" class="text-muted-foreground py-6 text-center text-sm">
              暂无数据
            </p>
            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>模型</TableHead>
                  <TableHead class="text-right">
                    请求
                  </TableHead>
                  <TableHead class="text-right">
                    失败率
                  </TableHead>
                  <TableHead class="text-right">
                    输入 / 输出
                  </TableHead>
                  <TableHead class="text-right">
                    收费
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="m in data.byModel" :key="m.model">
                  <TableCell class="max-w-48 truncate font-mono text-xs">
                    {{ m.model }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ formatNumber(m.requests) }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums" :class="m.errors > 0 ? 'text-destructive' : ''">
                    {{ formatPercent(errorRate(m.requests, m.errors)) }}
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums">
                    {{ formatCompact(m.inputTokens) }} / {{ formatCompact(m.outputTokens) }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ money(m.charge) }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle class="text-base">
              按渠道
            </CardTitle>
            <CardDescription>请求量前 50 的渠道（已删除渠道显示名称快照）</CardDescription>
          </CardHeader>
          <CardContent class="overflow-x-auto">
            <Skeleton v-if="loading && !data" class="h-32 w-full" />
            <p v-else-if="!data?.byChannel.length" class="text-muted-foreground py-6 text-center text-sm">
              暂无数据
            </p>
            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>渠道</TableHead>
                  <TableHead class="text-right">
                    请求
                  </TableHead>
                  <TableHead class="text-right">
                    失败率
                  </TableHead>
                  <TableHead class="text-right">
                    P95 延迟
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="c in data.byChannel" :key="c.channelId ?? 'none'">
                  <TableCell class="max-w-48 truncate">
                    {{ c.channelName ?? (c.channelId ? c.channelId.slice(0, 8) : '（未路由）') }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ formatNumber(c.requests) }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums" :class="c.errors > 0 ? 'text-destructive' : ''">
                    {{ formatPercent(errorRate(c.requests, c.errors)) }}
                  </TableCell>
                  <TableCell class="text-right tabular-nums">
                    {{ formatMs(c.latencyP95Ms) }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </div>
    </template>
  </div>
</template>
