<script setup lang="ts">
import type { StatsSummary, Subscription, Wallet as WalletInfo } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { Activity, ArrowRight, CircleCheck, Coins, Package, Timer, Wallet } from '@lucide/vue'
import PageHeader from '@/components/PageHeader.vue'
import QuotaBar from '@/components/quota/QuotaBar.vue'
import AlertsSummaryCard from '@/components/notifications/AlertsSummaryCard.vue'
import StatTile from '@/components/StatTile.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage } from '@/lib/api'
import { billingApi, logsApi } from '@/lib/endpoints'
import { formatCompact, formatDateTime, formatMs, formatNumber, formatPercent, ROLE_LABELS } from '@/lib/format'
import { formatMeterAmount, meterUnit, mostUsedRule, resetInfo, ruleTitle, usageLevel } from '@/lib/quota'
import { presetRange } from '@/lib/timeRange'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'

const auth = useAuthStore()
const system = useSystemStore()
const { money } = useCurrency()

const canStats = computed(() => auth.can('stats.own'))
const canBilling = computed(() => auth.can('billing.own'))

const stats = ref<StatsSummary | null>(null)
const statsError = ref<string | null>(null)
const statsLoading = ref(false)
const wallet = ref<WalletInfo | null>(null)
const walletError = ref<string | null>(null)

async function loadStats() {
  statsLoading.value = true
  statsError.value = null
  try {
    stats.value = await logsApi.summary(presetRange('24h'))
  }
  catch (err) {
    statsError.value = errorMessage(err)
  }
  finally {
    statsLoading.value = false
  }
}

async function loadWallet() {
  walletError.value = null
  try {
    wallet.value = await billingApi.wallet()
  }
  catch (err) {
    walletError.value = errorMessage(err)
  }
}

const subscriptions = ref<Subscription[]>([])
async function loadSubscriptions() {
  try {
    subscriptions.value = (await billingApi.subscriptions()).items
  }
  catch {
    // The tile is optional; the billing page shows the error.
  }
}
const activeSubs = computed(() => subscriptions.value.filter(s => s.status === 'active'))
const topRule = computed(() => mostUsedRule(activeSubs.value))

onMounted(() => {
  void system.ensureLoaded()
  if (canStats.value)
    void loadStats()
  if (canBilling.value) {
    void loadWallet()
    void loadSubscriptions()
  }
})

const totals = computed(() => stats.value?.totals ?? null)

const REGISTRATION_LABELS = { open: '开放注册', restricted: '受限注册（名单）', closed: '关闭注册' } as const

const greeting = computed(() => {
  const h = new Date().getHours()
  if (h < 6)
    return '夜深了'
  if (h < 12)
    return '早上好'
  if (h < 18)
    return '下午好'
  return '晚上好'
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader :title="`${greeting}，${auth.user?.displayName ?? ''}`" description="OmniGate 控制台概览" />

    <section v-if="canStats || canBilling" class="space-y-3">
      <div class="flex items-center justify-between gap-2">
        <h2 class="text-sm font-semibold">
          近 24 小时{{ auth.can('stats.all') ? '（全部用户）' : '' }}
        </h2>
        <Button v-if="canStats" variant="ghost" size="sm" as-child>
          <RouterLink to="/console/stats?range=24h">
            查看统计
            <ArrowRight />
          </RouterLink>
        </Button>
      </div>
      <p v-if="statsError" class="text-destructive text-sm">
        无法加载统计：{{ statsError }}
        <Button variant="link" size="sm" class="h-auto p-0" @click="loadStats">
          重试
        </Button>
      </p>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5">
        <template v-if="canStats && !statsError">
          <StatTile label="请求数" :icon="Activity" :value="formatNumber(totals?.requests)" :hint="totals ? `失败 ${formatNumber(totals.errors)}` : undefined" :loading="statsLoading && !totals" />
          <StatTile label="成功率" :icon="CircleCheck" :value="totals && totals.requests > 0 ? formatPercent(totals.successRate) : '—'" :loading="statsLoading && !totals" />
          <StatTile label="P95 延迟" :icon="Timer" :value="formatMs(totals?.latencyP95Ms)" :loading="statsLoading && !totals" />
          <StatTile
            label="Tokens 入 / 出"
            :icon="Coins"
            :value="totals ? `${formatCompact(totals.inputTokens)} / ${formatCompact(totals.outputTokens)}` : '—'"
            :hint="totals ? `收费 ${money(totals.charge)}` : undefined"
            :loading="statsLoading && !totals"
          />
        </template>
        <RouterLink v-if="canBilling" to="/console/billing" class="focus-visible:ring-ring/50 rounded-xl outline-none focus-visible:ring-3">
          <StatTile
            label="钱包可用余额"
            :icon="Wallet"
            :value="wallet ? money(wallet.available) : (walletError ? '加载失败' : '—')"
            :title="walletError ?? undefined"
            :hint="wallet ? `余额 ${money(wallet.balance)} · 预留 ${money(wallet.reserved)}` : undefined"
            :loading="!wallet && !walletError"
            class="hover:bg-muted/50 h-full transition-colors"
          />
        </RouterLink>
        <RouterLink v-if="topRule" to="/console/billing" class="focus-visible:ring-ring/50 rounded-xl outline-none focus-visible:ring-3" aria-label="套餐额度，查看我的订阅">
          <div class="bg-card text-card-foreground ring-foreground/10 hover:bg-muted/50 flex h-full min-w-0 flex-col gap-1 rounded-xl p-4 ring-1 transition-colors">
            <div class="text-muted-foreground flex items-center gap-1.5 text-xs">
              <Package class="size-3.5 shrink-0" />
              <span class="truncate">套餐额度{{ activeSubs.length > 1 ? `（${activeSubs.length} 个订阅）` : '' }}</span>
            </div>
            <p class="truncate text-xl font-semibold tabular-nums sm:text-2xl" :class="topRule.rule.exceeded ? 'text-destructive' : ''" :title="`${topRule.subscription.plan.name} · ${ruleTitle(topRule.rule)}`">
              {{ topRule.rule.exceeded ? '已达上限' : `${Math.min(100, Math.round(topRule.ratio * 100))}%` }}
            </p>
            <QuotaBar :ratio="topRule.ratio" :level="usageLevel(topRule.rule)" :label="ruleTitle(topRule.rule)" size="sm" />
            <p class="text-muted-foreground truncate text-xs" :title="resetInfo(topRule.rule).absolute ?? undefined">
              {{ ruleTitle(topRule.rule) }} {{ formatMeterAmount(topRule.rule.meter, topRule.rule.used, money) }} / {{ formatMeterAmount(topRule.rule.meter, topRule.rule.limit, money) }}{{ meterUnit(topRule.rule.meter, topRule.rule) ? ` ${meterUnit(topRule.rule.meter, topRule.rule)}` : '' }} · {{ resetInfo(topRule.rule).text }}
            </p>
          </div>
        </RouterLink>
      </div>
    </section>

    <!-- Hidden for users who manage no channels. -->
    <AlertsSummaryCard :expect-channels="auth.can('channels.manage')" />

    <div class="grid gap-4 md:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>当前账号</CardTitle>
        </CardHeader>
        <CardContent v-if="auth.user" class="space-y-4">
          <div class="flex items-center gap-3">
            <UserAvatar :name="auth.user.displayName" :src="auth.user.avatarUrl" size="lg" />
            <div class="min-w-0">
              <p class="truncate font-medium">
                {{ auth.user.displayName }}
              </p>
              <p class="text-muted-foreground truncate text-sm">
                {{ auth.user.email ?? '未设置邮箱' }}
              </p>
            </div>
            <Badge variant="secondary" class="ml-auto">
              {{ ROLE_LABELS[auth.user.role] }}
            </Badge>
          </div>
          <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
            <dt class="text-muted-foreground">
              上次登录
            </dt>
            <dd>{{ formatDateTime(auth.user.lastLoginAt) }}</dd>
            <dt class="text-muted-foreground">
              注册时间
            </dt>
            <dd>{{ formatDateTime(auth.user.createdAt) }}</dd>
            <dt class="text-muted-foreground">
              权限数量
            </dt>
            <dd>{{ auth.permissions.length }}</dd>
          </dl>
          <Button variant="outline" size="sm" as-child>
            <RouterLink to="/console/settings/profile">
              个人设置
            </RouterLink>
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>系统信息</CardTitle>
        </CardHeader>
        <CardContent>
          <dl v-if="system.info" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
            <dt class="text-muted-foreground">
              名称
            </dt>
            <dd>{{ system.info.name }}</dd>
            <dt class="text-muted-foreground">
              版本
            </dt>
            <dd class="font-mono">
              {{ system.info.version }}
            </dd>
            <dt class="text-muted-foreground">
              计费货币
            </dt>
            <dd>
              {{ system.info.currency.code }}（{{ system.info.currency.symbol }}）
            </dd>
            <dt class="text-muted-foreground">
              注册模式
            </dt>
            <dd>{{ REGISTRATION_LABELS[system.info.registrationMode] ?? system.info.registrationMode }}</dd>
          </dl>
          <div v-else-if="system.status === 'error'" class="space-y-2 text-sm">
            <p class="text-destructive">
              无法获取系统信息{{ system.error ? `：${system.error.message}` : '' }}
            </p>
            <Button variant="outline" size="sm" @click="system.load()">
              重试
            </Button>
          </div>
          <div v-else class="space-y-2">
            <Skeleton class="h-4 w-2/3" />
            <Skeleton class="h-4 w-1/2" />
            <Skeleton class="h-4 w-3/5" />
          </div>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
