<script setup lang="ts">
import type { PurchaseIntent } from '@/lib/purchase'
import type { PurchaseOptions, PurchaseRecord, PurchaseResult } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowRight, ArrowUpCircle, CircleAlert, CircleCheck, Loader2, Package, ReceiptText, RefreshCw, ShoppingCart, Ticket, Wallet, X } from '@lucide/vue'
import { toast } from 'vue-sonner'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { isApiError } from '@/lib/api'
import { billingApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { amountSign } from '@/lib/money'
import {
  balanceAfter,
  buildPurchaseCards,
  canAfford,
  changedPrice,
  findIntent,
  intentButtonLabel,
  intentTitle,
  isLongDescription,
  PURCHASE_ACTION_LABELS,
  purchaseBody,
  purchaseErrorMessage,
  purchaseSuccessText,
  shortfall,
} from '@/lib/purchase'
import { isAbortError, queryInt } from '@/lib/query'
import { formatDuration, modelsSummary, rulesSummary } from '@/lib/quota'

const PAGE_SIZE = 10
const BILLING_PATH = '/console/billing'

const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

// ---------- options ----------
const options = ref<PurchaseOptions | null>(null)
const optionsLoading = ref(false)
const optionsError = ref<unknown>(null)
async function loadOptions() {
  optionsLoading.value = true
  optionsError.value = null
  try {
    options.value = await billingApi.purchaseOptions()
  }
  catch (err) {
    optionsError.value = err
  }
  finally {
    optionsLoading.value = false
  }
}
onMounted(loadOptions)
const cards = computed(() => buildPurchaseCards(options.value))
const available = computed(() => options.value?.available ?? null)

/** Long descriptions collapse behind 展开 / 收起. */
const expandedPlans = ref(new Set<string>())
function togglePlanDescription(id: string) {
  const next = new Set(expandedPlans.value)
  if (!next.delete(id))
    next.add(id)
  expandedPlans.value = next
}

// ---------- purchase records ----------
const page = computed(() => queryInt(route.query.page, 1))
const records = ref<PurchaseRecord[]>([])
const recordsTotal = ref(0)
const recordsLoading = ref(false)
const recordsError = ref<unknown>(null)
let controller: AbortController | null = null
async function loadRecords() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  recordsLoading.value = true
  recordsError.value = null
  try {
    const res = await billingApi.purchases({ page: page.value, pageSize: PAGE_SIZE }, ctrl.signal)
    records.value = res.items ?? []
    recordsTotal.value = res.total ?? 0
  }
  catch (err) {
    if (!isAbortError(err))
      recordsError.value = err
  }
  finally {
    if (controller === ctrl)
      recordsLoading.value = false
  }
}
watch(page, loadRecords, { immediate: true })
onBeforeUnmount(() => controller?.abort())

function refresh() {
  void loadOptions()
  void loadRecords()
}

// ---------- confirm & purchase ----------
const pending = ref<PurchaseIntent | null>(null)
const confirmOpen = ref(false)
const submitting = ref(false)
const confirmError = ref<string | null>(null)
const lastResult = ref<PurchaseResult | null>(null)

function openConfirm(i: PurchaseIntent) {
  pending.value = i
  confirmError.value = null
  confirmOpen.value = true
}
watch(confirmOpen, (v) => {
  if (!v && !submitting.value)
    confirmError.value = null
})

const pendingAfter = computed(() => balanceAfter(available.value, pending.value?.price))
const pendingAffordable = computed(() => canAfford(available.value, pending.value?.price))
const pendingShortfall = computed(() => shortfall(available.value, pending.value?.price))

/** Errors after which the options are stale (plan archived, not for sale, source expired …). */
const REFRESH_CODES = new Set(['plan_archived', 'plan_not_for_sale', 'not_an_upgrade', 'subscription_not_active', 'not_found', 'insufficient_balance'])

async function confirmPurchase() {
  const intent = pending.value
  if (!intent || submitting.value)
    return
  submitting.value = true
  confirmError.value = null
  try {
    const res = await billingApi.purchase(purchaseBody(intent))
    const { title, description } = purchaseSuccessText(res, money)
    toast.success(title, {
      description,
      action: { label: '查看订阅', onClick: () => void router.push(BILLING_PATH) },
    })
    lastResult.value = res
    if (options.value)
      options.value = { ...options.value, available: res.wallet.available }
    confirmOpen.value = false
    void loadOptions()
    if (page.value === 1)
      void loadRecords()
    else
      void router.replace({ query: { ...route.query, page: undefined } })
  }
  catch (err) {
    confirmError.value = purchaseErrorMessage(err, money)
    const newPrice = changedPrice(err)
    if (newPrice !== null || (isApiError(err) && REFRESH_CODES.has(err.code))) {
      await loadOptions()
      const fresh = findIntent(options.value, intent.planId, intent.fromSubscriptionId)
      if (fresh) {
        // Keep the dialog open with the current price so the user can confirm again.
        pending.value = fresh
      }
      else if (newPrice === null) {
        confirmOpen.value = false
        toast.error('无法完成购买', { description: confirmError.value })
      }
    }
  }
  finally {
    submitting.value = false
  }
}

function recordTitle(r: PurchaseRecord): string {
  if (r.action === 'upgrade' && r.fromPlanName)
    return `${r.fromPlanName} → ${r.planName}`
  return r.planName
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="购买套餐" description="使用钱包余额购买、续费套餐，或补差价升级到更高档位。金额以系统结算币种计。">
      <template #actions>
        <Button variant="outline" size="sm" as-child>
          <RouterLink :to="BILLING_PATH">
            <Wallet />
            钱包与订阅
          </RouterLink>
        </Button>
        <Button variant="outline" size="sm" :disabled="optionsLoading || recordsLoading" @click="refresh">
          <RefreshCw :class="optionsLoading || recordsLoading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <div
      v-if="lastResult"
      role="status"
      class="flex items-start gap-3 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-4 text-sm"
      data-testid="purchase-success"
    >
      <CircleCheck class="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" aria-hidden="true" />
      <div class="min-w-0 flex-1 space-y-1">
        <p class="font-medium">
          {{ purchaseSuccessText(lastResult, money).title }}
        </p>
        <p class="text-muted-foreground">
          {{ purchaseSuccessText(lastResult, money).description }}
        </p>
        <RouterLink :to="BILLING_PATH" class="text-primary inline-flex items-center gap-1 font-medium underline-offset-4 hover:underline">
          前往「钱包与订阅」查看额度
          <ArrowRight class="size-3.5" />
        </RouterLink>
      </div>
      <Button variant="ghost" size="icon-xs" aria-label="关闭提示" @click="lastResult = null">
        <X />
      </Button>
    </div>

    <Card>
      <CardContent class="flex flex-wrap items-center justify-between gap-4">
        <div class="space-y-1">
          <p class="text-muted-foreground text-xs">
            钱包可用余额
          </p>
          <Skeleton v-if="!options && !optionsError" class="h-9 w-32" />
          <p v-else-if="options" class="text-3xl font-semibold tabular-nums" :class="amountSign(options.available) < 0 ? 'text-destructive' : ''" data-testid="store-available">
            {{ money(options.available) }}
          </p>
          <p v-else class="text-muted-foreground text-sm">
            —
          </p>
        </div>
        <div class="flex flex-col items-start gap-1 sm:items-end">
          <Button variant="outline" size="sm" as-child>
            <RouterLink :to="BILLING_PATH">
              <Ticket />
              使用兑换码充值
            </RouterLink>
          </Button>
          <p class="text-muted-foreground text-xs">
            余额不足时，可在「钱包与订阅」中兑换余额兑换码。
          </p>
        </div>
      </CardContent>
    </Card>

    <ErrorState v-if="optionsError" :error="optionsError" @retry="loadOptions" />
    <div v-else-if="!options" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <Skeleton v-for="i in 3" :key="i" class="h-72 w-full rounded-xl" />
    </div>
    <EmptyState
      v-else-if="cards.length === 0"
      :icon="Package"
      title="暂无在售套餐"
      description="管理员上架套餐后会出现在这里；你也可以通过兑换码开通套餐。"
      class="rounded-xl border"
    />
    <ul v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3" data-testid="plan-cards">
      <li
        v-for="c in cards"
        :key="c.option.plan.id"
        class="bg-card text-card-foreground ring-foreground/10 flex min-w-0 flex-col gap-4 rounded-xl p-5 ring-1"
        :aria-label="`套餐：${c.option.plan.name}`"
      >
        <div class="space-y-2">
          <div class="flex items-start justify-between gap-2">
            <h2 class="min-w-0 truncate text-base font-semibold" :title="c.option.plan.name">
              {{ c.option.plan.name }}
            </h2>
            <Badge v-if="c.direct?.action === 'renew'" variant="secondary" class="shrink-0">
              已订阅
            </Badge>
          </div>
          <p v-if="c.direct" class="flex items-baseline gap-1">
            <span class="text-3xl font-semibold tabular-nums">{{ money(c.direct.price) }}</span>
            <span class="text-muted-foreground text-sm">/ {{ formatDuration(c.option.plan.duration) }}</span>
          </p>
          <p v-else class="text-muted-foreground text-sm">
            暂不支持余额购买 · 每份 {{ formatDuration(c.option.plan.duration) }}
          </p>
        </div>

        <div class="space-y-2 text-sm">
          <p>{{ rulesSummary(c.option.plan.rules, money) }}</p>
          <div class="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
            <span :title="c.option.plan.models.join('\n') || '全部模型'">适用模型：{{ modelsSummary(c.option.plan.models, 3) }}</span>
            <span>{{ c.option.plan.stackable ? '可叠加' : '重复购买将续期' }}</span>
          </div>
        </div>

        <div v-if="c.option.plan.description" class="text-muted-foreground text-xs">
          <p
            :id="`store-plan-desc-${c.option.plan.id}`"
            class="break-words whitespace-pre-line"
            :class="isLongDescription(c.option.plan.description) && !expandedPlans.has(c.option.plan.id) ? 'line-clamp-6' : ''"
            data-testid="plan-description"
          >
            {{ c.option.plan.description.trim() }}
          </p>
          <button
            v-if="isLongDescription(c.option.plan.description)"
            type="button"
            class="text-foreground mt-1 rounded-sm font-medium underline-offset-4 outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
            :aria-expanded="expandedPlans.has(c.option.plan.id)"
            :aria-controls="`store-plan-desc-${c.option.plan.id}`"
            @click="togglePlanDescription(c.option.plan.id)"
          >
            {{ expandedPlans.has(c.option.plan.id) ? '收起' : '展开' }}
          </button>
        </div>

        <div class="mt-auto space-y-2 border-t pt-4">
          <p v-if="c.direct?.action === 'renew' && c.direct.currentEndsAt" class="text-muted-foreground text-xs tabular-nums">
            当前到期 {{ formatDateTime(c.direct.currentEndsAt) }}，续费后至 {{ formatDateTime(c.direct.endsAt) }}
          </p>
          <Button v-if="c.showDirect && c.direct" class="w-full" @click="openConfirm(c.direct)">
            <ShoppingCart />
            {{ intentButtonLabel(c.direct, money) }}
          </Button>
          <template v-else-if="!c.direct">
            <Button class="w-full" disabled>
              <ShoppingCart />
              购买
            </Button>
            <p class="text-muted-foreground text-center text-xs">
              暂不支持余额购买，可通过兑换码或联系管理员开通。
            </p>
          </template>
          <Button
            v-for="u in c.upgrades"
            :key="u.fromSubscriptionId ?? ''"
            variant="outline"
            class="w-full whitespace-normal"
            data-testid="upgrade-button"
            @click="openConfirm(u)"
          >
            <ArrowUpCircle />
            {{ intentButtonLabel(u, money) }}
          </Button>
        </div>
      </li>
    </ul>

    <Card>
      <CardHeader>
        <CardTitle class="text-base">
          购买记录
        </CardTitle>
        <CardDescription>用余额购买、续费与升级套餐的记录（倒序）；扣款同时记入「钱包与订阅」的资金流水。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <ErrorState v-if="recordsError" :error="recordsError" @retry="loadRecords" />
        <div v-else-if="recordsLoading && records.length === 0" class="space-y-2">
          <Skeleton v-for="i in 3" :key="i" class="h-10 w-full" />
        </div>
        <EmptyState v-else-if="records.length === 0" :icon="ReceiptText" title="暂无购买记录" description="用余额购买套餐后，记录会出现在这里。" />
        <div v-else class="overflow-x-auto" :class="recordsLoading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>操作</TableHead>
                <TableHead>套餐</TableHead>
                <TableHead class="text-right">
                  金额
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="r in records" :key="r.id">
                <TableCell class="whitespace-nowrap tabular-nums">
                  {{ formatDateTime(r.createdAt) }}
                </TableCell>
                <TableCell>
                  <Badge :variant="r.action === 'upgrade' ? 'default' : r.action === 'renew' ? 'secondary' : 'outline'">
                    {{ PURCHASE_ACTION_LABELS[r.action as keyof typeof PURCHASE_ACTION_LABELS] ?? r.action }}
                  </Badge>
                </TableCell>
                <TableCell class="max-w-64 truncate" :title="recordTitle(r)">
                  {{ recordTitle(r) }}
                </TableCell>
                <TableCell class="text-right whitespace-nowrap tabular-nums">
                  {{ money(r.price) }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <DataPagination
          v-if="!recordsError && recordsTotal > 0"
          :page="page"
          :page-size="PAGE_SIZE"
          :total="recordsTotal"
          :disabled="recordsLoading"
          @update:page="(p) => router.replace({ query: { ...route.query, page: p > 1 ? String(p) : undefined } })"
        />
      </CardContent>
    </Card>

    <Dialog v-model:open="confirmOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{{ pending ? intentTitle(pending) : '确认购买' }}</DialogTitle>
          <DialogDescription>确认后将立即从钱包可用余额中扣款。</DialogDescription>
        </DialogHeader>

        <dl v-if="pending" class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm" data-testid="purchase-summary">
          <dt class="text-muted-foreground">
            操作
          </dt>
          <dd class="text-right">
            {{ PURCHASE_ACTION_LABELS[pending.action] }}
          </dd>
          <dt class="text-muted-foreground">
            套餐
          </dt>
          <dd class="truncate text-right" :title="pending.planName">
            {{ pending.action === 'upgrade' ? `${pending.fromPlanName ?? '当前套餐'} → ${pending.planName}` : pending.planName }}
          </dd>
          <dt class="text-muted-foreground">
            {{ pending.action === 'upgrade' ? '补差价' : '价格' }}
          </dt>
          <dd class="text-right font-semibold tabular-nums">
            {{ money(pending.price) }}
          </dd>
          <dt class="text-muted-foreground">
            当前可用余额
          </dt>
          <dd class="text-right tabular-nums">
            {{ money(available) }}
          </dd>
          <dt class="text-muted-foreground">
            购买后余额
          </dt>
          <dd class="text-right tabular-nums" :class="pendingAffordable ? '' : 'text-destructive'">
            {{ money(pendingAfter) }}
          </dd>
          <dt class="text-muted-foreground">
            到期时间
          </dt>
          <dd class="text-right tabular-nums">
            <template v-if="pending.action === 'renew' && pending.currentEndsAt">
              {{ formatDateTime(pending.currentEndsAt) }} → {{ formatDateTime(pending.endsAt) }}
            </template>
            <template v-else-if="pending.action === 'upgrade'">
              {{ formatDateTime(pending.endsAt) }}（不变）
            </template>
            <template v-else>
              {{ formatDateTime(pending.endsAt) }}
            </template>
          </dd>
        </dl>

        <p v-if="pending?.action === 'upgrade'" class="bg-muted/50 rounded-md p-3 text-xs">
          到期时间不变；已用额度保留，按新套餐上限重新计算百分比。补差价按剩余时间折算两档套餐的价差。
        </p>

        <div
          v-if="pending && !pendingAffordable"
          role="alert"
          class="border-destructive/30 bg-destructive/5 flex gap-2 rounded-md border p-3 text-sm"
          data-testid="insufficient-balance"
        >
          <CircleAlert class="text-destructive mt-0.5 size-4 shrink-0" />
          <div class="space-y-1">
            <p class="text-destructive">
              {{ pendingShortfall ? `余额不足，还差 ${money(pendingShortfall)}。` : '余额不足。' }}
            </p>
            <RouterLink :to="BILLING_PATH" class="text-primary inline-flex items-center gap-1 text-xs font-medium underline-offset-4 hover:underline">
              使用兑换码充值
              <ArrowRight class="size-3.5" />
            </RouterLink>
          </div>
        </div>

        <p v-if="confirmError" class="text-destructive text-sm" role="alert">
          {{ confirmError }}
        </p>

        <DialogFooter>
          <Button variant="outline" :disabled="submitting" @click="confirmOpen = false">
            取消
          </Button>
          <Button :disabled="!pending || !pendingAffordable || submitting" data-testid="confirm-purchase" @click="confirmPurchase">
            <Loader2 v-if="submitting" class="animate-spin" />
            确认{{ pending ? PURCHASE_ACTION_LABELS[pending.action] : '购买' }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
