<script setup lang="ts">
import type { NormalizedLimits } from '@/lib/limits'
import { computed, onMounted, ref } from 'vue'
import { Gauge, RefreshCw } from '@lucide/vue'
import ErrorState from '@/components/ErrorState.vue'
import QuotaBar from '@/components/quota/QuotaBar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useCurrency } from '@/composables/useCurrency'
import { isApiError } from '@/lib/api'
import { billingApi } from '@/lib/endpoints'
import { formatDateTime, formatNumber } from '@/lib/format'
import { isNonUnitMultiplier, multiplierLabel, MULTIPLIER_TONE_CLASSES, multiplierTone } from '@/lib/groups'
import { amountRatio, countRatio, levelOf, normalizeBillingLimits, percentText, spendLimitText } from '@/lib/limits'
import { formatUntil } from '@/lib/quota'

/** "用量限额" of 钱包与订阅 (phase8 §2.3 `GET /api/billing/limits`); hidden on older backends. */
const { money } = useCurrency()

const data = ref<NormalizedLimits | null>(null)
const loading = ref(false)
const error = ref<unknown>(null)
/** The backend does not have the endpoint (404 / 501): hide the card. */
const unsupported = ref(false)

async function load() {
  loading.value = true
  error.value = null
  try {
    data.value = normalizeBillingLimits(await billingApi.limits())
  }
  catch (err) {
    if (isApiError(err) && (err.status === 404 || err.status === 501))
      unsupported.value = true
    else
      error.value = err
  }
  finally {
    loading.value = false
  }
}
onMounted(load)
defineExpose({ load })

interface Row {
  key: string
  label: string
  used: string
  limit: string | null
  ratio: number | null
  resetsAt: string | null
}

const rows = computed<Row[]>(() => {
  const d = data.value
  if (!d)
    return []
  const l = d.group.limits
  const u = d.usage
  return [
    {
      key: 'rpd',
      label: '今日请求',
      used: `${formatNumber(u.rpdUsed)} 次`,
      limit: l.rpd === null ? null : `${formatNumber(l.rpd)} 次`,
      ratio: l.rpd === null ? null : countRatio(u.rpdUsed, l.rpd),
      resetsAt: u.dayResetsAt,
    },
    {
      key: 'daily',
      label: '今日消费',
      used: money(u.dailySpent),
      limit: l.dailySpend === null ? null : money(l.dailySpend),
      ratio: l.dailySpend === null ? null : amountRatio(u.dailySpent, l.dailySpend),
      resetsAt: u.dayResetsAt,
    },
    {
      key: 'monthly',
      label: '本月消费',
      used: money(u.monthlySpent),
      limit: l.monthlySpend === null ? null : money(l.monthlySpend),
      ratio: l.monthlySpend === null ? null : amountRatio(u.monthlySpent, l.monthlySpend),
      resetsAt: u.monthResetsAt,
    },
  ]
})
const limitedKeys = computed(() => (data.value?.keys ?? []).filter(k => k.spendLimit))
const multiplier = computed(() => data.value?.group.priceMultiplier ?? null)
const description = computed(() => {
  const tz = data.value?.group.timezone
  return `由所在用户组决定，按每位用户计算${tz ? `（按 ${tz} 计日 / 月）` : ''}。消费只统计平台渠道的计费金额，自有 / 共享渠道不受消费限额约束；达到上限后网关返回 429。`
})
</script>

<template>
  <Card v-if="!unsupported" data-testid="usage-limits">
    <CardHeader>
      <div class="flex items-start justify-between gap-2">
        <div class="min-w-0 space-y-1.5">
          <CardTitle class="flex flex-wrap items-center gap-2 text-base">
            <Gauge class="size-4" />
            用量限额
            <Badge v-if="data?.group.name" variant="secondary" class="font-normal" data-testid="limits-group">
              分组：{{ data.group.name }}
            </Badge>
            <Badge v-if="multiplier && isNonUnitMultiplier(multiplier)" variant="outline" class="font-normal tabular-nums" :class="MULTIPLIER_TONE_CLASSES[multiplierTone(multiplier)]">
              价格 {{ multiplierLabel(multiplier) }}
            </Badge>
          </CardTitle>
          <CardDescription>
            {{ description }}
          </CardDescription>
        </div>
        <Button variant="ghost" size="icon-sm" aria-label="刷新用量限额" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
        </Button>
      </div>
    </CardHeader>
    <CardContent class="space-y-4">
      <ErrorState v-if="error" :error="error" @retry="load" />
      <div v-else-if="!data" class="grid gap-3 md:grid-cols-3">
        <Skeleton v-for="i in 3" :key="i" class="h-20 w-full" />
      </div>
      <template v-else>
        <div class="grid gap-3 md:grid-cols-3">
          <div v-for="r in rows" :key="r.key" class="space-y-2 rounded-lg border p-3" :data-testid="`limit-${r.key}`">
            <div class="flex items-baseline justify-between gap-2">
              <span class="text-muted-foreground text-xs">{{ r.label }}</span>
              <span v-if="r.ratio !== null" class="text-xs tabular-nums" :class="levelOf(r.ratio) === 'exceeded' ? 'text-destructive' : levelOf(r.ratio) === 'warn' ? 'text-amber-700 dark:text-amber-400' : 'text-muted-foreground'">
                {{ percentText(r.ratio) }}
              </span>
            </div>
            <p class="text-sm tabular-nums">
              <span class="text-lg font-semibold">{{ r.used }}</span>
              <span class="text-muted-foreground"> / </span>
              <span v-if="r.limit">{{ r.limit }}</span>
              <span v-else class="text-muted-foreground">不限</span>
            </p>
            <QuotaBar v-if="r.ratio !== null" :ratio="r.ratio" :level="levelOf(r.ratio)" size="sm" :label="`${r.label}用量`" />
            <p class="text-muted-foreground text-xs" :title="r.resetsAt ? formatDateTime(r.resetsAt) : undefined">
              <template v-if="r.resetsAt">
                {{ formatUntil(r.resetsAt) }}重置
              </template>
              <template v-else>
                {{ r.key === 'monthly' ? '每月 1 日零点重置' : '每天零点重置' }}
              </template>
            </p>
          </div>
        </div>
        <p v-if="data.group.limits.rpm !== null" class="text-muted-foreground text-xs" data-testid="limit-rpm">
          每分钟最多 {{ formatNumber(data.group.limits.rpm) }} 次请求（所有 API Key 合计）。
        </p>

        <div class="space-y-2">
          <p class="text-sm font-medium">
            API Key 消费上限
          </p>
          <ul v-if="limitedKeys.length" class="divide-y rounded-lg border" data-testid="limits-keys">
            <li v-for="k in limitedKeys" :key="k.id" class="grid gap-2 p-3 sm:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] sm:items-center">
              <span class="truncate text-sm font-medium" :title="k.name">{{ k.name }}</span>
              <div class="min-w-0 space-y-1">
                <div class="flex items-baseline justify-between gap-2 text-xs tabular-nums">
                  <span>{{ money(k.spent) }} / {{ spendLimitText(k.spendLimit!, money) }}</span>
                  <span class="text-muted-foreground">{{ percentText(amountRatio(k.spent, k.spendLimit!.amount)) }}</span>
                </div>
                <QuotaBar :ratio="amountRatio(k.spent, k.spendLimit!.amount)" :level="levelOf(amountRatio(k.spent, k.spendLimit!.amount))" size="sm" :label="`${k.name} 消费上限用量`" />
              </div>
              <span class="text-muted-foreground text-xs whitespace-nowrap" :title="k.resetsAt ? formatDateTime(k.resetsAt) : undefined">
                {{ k.spendLimit!.window === 'total' ? '不重置' : k.resetsAt ? `${formatUntil(k.resetsAt)}重置` : '' }}
              </span>
            </li>
          </ul>
          <p v-else class="text-muted-foreground rounded-lg border border-dashed p-3 text-xs">
            没有设置消费上限的 API Key。可以在「API Keys」中为单个 Key 设置每天 / 每周 / 每月 / 总计的消费上限。
          </p>
        </div>
      </template>
    </CardContent>
  </Card>
</template>
