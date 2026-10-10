<script setup lang="ts">
import type { LedgerEntry, QuotaOverflow, Subscription, Wallet } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ChevronDown, Loader2, Package, ReceiptText, RefreshCw, ShoppingCart, Ticket } from '@lucide/vue'
import { useIntervalFn, useNow } from '@vueuse/core'
import { toast } from 'vue-sonner'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import SubscriptionCard from '@/components/quota/SubscriptionCard.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isApiError } from '@/lib/api'
import { billingApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { LEDGER_KIND_LABELS, REF_TYPE_LABELS } from '@/lib/labels'
import { amountSign } from '@/lib/money'
import { isAbortError, queryInt } from '@/lib/query'
import { isQuotaOverflow, PLAN_ERROR_MESSAGES, QUOTA_OVERFLOW_OPTIONS, QUOTA_OVERFLOW_SHORT, sortSubscriptions } from '@/lib/quota'
import { normalizeRedeemCode, REDEEM_ERROR_MESSAGES } from '@/lib/redeem'
import ResetCardsSection from './reset-cards/ResetCardsSection.vue'
import UsageLimitsCard from './UsageLimitsCard.vue'

const PAGE_SIZE = 20

const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

// ---------- wallet ----------
const wallet = ref<Wallet | null>(null)
const walletLoading = ref(false)
const walletError = ref<unknown>(null)
async function loadWallet() {
  walletLoading.value = true
  walletError.value = null
  try {
    wallet.value = await billingApi.wallet()
  }
  catch (err) {
    walletError.value = err
  }
  finally {
    walletLoading.value = false
  }
}
onMounted(loadWallet)

// ---------- ledger ----------
const page = computed(() => queryInt(route.query.page, 1))
const ledger = ref<LedgerEntry[]>([])
const total = ref(0)
const ledgerLoading = ref(false)
const ledgerError = ref<unknown>(null)
let controller: AbortController | null = null
async function loadLedger() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  ledgerLoading.value = true
  ledgerError.value = null
  try {
    const res = await billingApi.ledger({ page: page.value, pageSize: PAGE_SIZE }, ctrl.signal)
    ledger.value = res.items
    total.value = res.total
  }
  catch (err) {
    if (!isAbortError(err))
      ledgerError.value = err
  }
  finally {
    if (controller === ctrl)
      ledgerLoading.value = false
  }
}
watch(page, loadLedger, { immediate: true })
onBeforeUnmount(() => controller?.abort())

// ---------- subscriptions ----------
const now = useNow({ scheduler: cb => useIntervalFn(cb, 30_000) })
const subscriptions = ref<Subscription[] | null>(null)
const subsLoading = ref(false)
const subsError = ref<unknown>(null)
async function loadSubscriptions() {
  subsLoading.value = true
  subsError.value = null
  try {
    subscriptions.value = sortSubscriptions((await billingApi.subscriptions()).items)
  }
  catch (err) {
    subsError.value = err
  }
  finally {
    subsLoading.value = false
  }
}
onMounted(loadSubscriptions)
const activeSubs = computed(() => (subscriptions.value ?? []).filter(s => s.status === 'active'))
const pastSubs = computed(() => (subscriptions.value ?? []).filter(s => s.status !== 'active'))
const pastOpen = ref(false)

// ---------- quota overflow preference ----------
const overflow = ref<QuotaOverflow | null>(null)
const prefLoading = ref(false)
const prefError = ref<unknown>(null)
const prefSaving = ref(false)
async function loadPreferences() {
  prefLoading.value = true
  prefError.value = null
  try {
    const res = await billingApi.preferences()
    overflow.value = isQuotaOverflow(res.quotaOverflow) ? res.quotaOverflow : 'block'
  }
  catch (err) {
    prefError.value = err
  }
  finally {
    prefLoading.value = false
  }
}
onMounted(loadPreferences)

async function setOverflow(value: QuotaOverflow) {
  if (prefSaving.value || value === overflow.value)
    return
  const previous = overflow.value
  overflow.value = value
  prefSaving.value = true
  try {
    const res = await billingApi.updatePreferences({ quotaOverflow: value })
    overflow.value = res.quotaOverflow
    toast.success('设置已保存', { description: `套餐额度用完后：${QUOTA_OVERFLOW_SHORT[res.quotaOverflow]}` })
  }
  catch (err) {
    overflow.value = previous
    toast.error('保存失败', { description: errorMessage(err) })
  }
  finally {
    prefSaving.value = false
  }
}

const limitsCard = ref<InstanceType<typeof UsageLimitsCard> | null>(null)
const cardsSection = ref<InstanceType<typeof ResetCardsSection> | null>(null)
function refresh() {
  void limitsCard.value?.load()
  void cardsSection.value?.load()
  void loadWallet()
  void loadLedger()
  void loadSubscriptions()
  void loadPreferences()
}

// ---------- redeem ----------
const code = ref('')
const redeeming = ref(false)
const redeemError = ref<string | null>(null)
const normalized = computed(() => normalizeRedeemCode(code.value))
const showPreview = computed(() => code.value.trim().length > 0)
watch(code, () => {
  redeemError.value = null
})

async function redeem() {
  redeemError.value = null
  const c = normalized.value
  if (!c) {
    redeemError.value = '兑换码格式不正确，应为 OG-XXXXX-XXXXX-XXXXX-XXXXX'
    return
  }
  redeeming.value = true
  try {
    const res = await billingApi.redeem(c)
    code.value = ''
    if (res.kind === 'plan') {
      // A renewal extends (and returns) a subscription we already list.
      const renewed = (subscriptions.value ?? []).some(s => s.id === res.subscription.id)
      toast.success(renewed ? '套餐已续期' : '套餐已开通', {
        description: `已${renewed ? '续期' : '开通'}套餐「${res.subscription.plan.name}」，有效期至 ${formatDateTime(res.subscription.endsAt)}`,
      })
      void loadSubscriptions()
      return
    }
    wallet.value = res.wallet
    toast.success('兑换成功', { description: `已到账 ${money(res.amount)}，当前余额 ${money(res.wallet.balance)}` })
    if (page.value === 1)
      void loadLedger()
    else
      void router.replace({ query: { ...route.query, page: undefined } })
  }
  catch (err) {
    redeemError.value = isApiError(err) && (REDEEM_ERROR_MESSAGES[err.code] ?? PLAN_ERROR_MESSAGES[err.code])
      ? (REDEEM_ERROR_MESSAGES[err.code] ?? PLAN_ERROR_MESSAGES[err.code])!
      : errorMessage(err)
  }
  finally {
    redeeming.value = false
  }
}

function amountClass(v: string): string {
  const s = amountSign(v)
  return s > 0 ? 'text-emerald-700 dark:text-emerald-400' : s < 0 ? 'text-destructive' : ''
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="钱包与订阅" description="账户余额、套餐订阅与额度、重置卡、兑换码兑换与资金流水。购买套餐请前往「购买套餐」。金额以系统结算币种计。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="walletLoading || ledgerLoading || subsLoading" @click="refresh">
          <RefreshCw :class="walletLoading || ledgerLoading || subsLoading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <div class="grid gap-4 lg:grid-cols-3">
      <Card class="lg:col-span-2">
        <CardHeader>
          <CardTitle class="text-base">
            钱包
          </CardTitle>
          <CardDescription>可用余额 = 余额 − 进行中请求的预留金额</CardDescription>
        </CardHeader>
        <CardContent>
          <ErrorState v-if="walletError" :error="walletError" @retry="loadWallet" />
          <div v-else class="grid gap-4 sm:grid-cols-3">
            <div class="space-y-1">
              <p class="text-muted-foreground text-xs">
                可用余额
              </p>
              <Skeleton v-if="!wallet" class="h-9 w-32" />
              <p v-else class="text-3xl font-semibold tabular-nums" :class="amountSign(wallet.available) < 0 ? 'text-destructive' : ''">
                {{ money(wallet.available) }}
              </p>
            </div>
            <div class="space-y-1">
              <p class="text-muted-foreground text-xs">
                余额
              </p>
              <Skeleton v-if="!wallet" class="h-6 w-24" />
              <p v-else class="text-lg font-medium tabular-nums">
                {{ money(wallet.balance) }}
              </p>
            </div>
            <div class="space-y-1">
              <p class="text-muted-foreground text-xs">
                预留中
              </p>
              <Skeleton v-if="!wallet" class="h-6 w-24" />
              <p v-else class="text-lg font-medium tabular-nums">
                {{ money(wallet.reserved) }}
              </p>
            </div>
            <p v-if="wallet" class="text-muted-foreground text-xs sm:col-span-3">
              结算币种：{{ wallet.currency }}
            </p>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            <Ticket class="size-4" />
            兑换码
          </CardTitle>
          <CardDescription>输入兑换码为钱包充值或开通套餐，大小写、空格和连字符可忽略。</CardDescription>
        </CardHeader>
        <CardContent>
          <form class="space-y-2" novalidate @submit.prevent="redeem">
            <Input v-model="code" placeholder="OG-XXXXX-XXXXX-XXXXX-XXXXX" class="font-mono uppercase" autocomplete="off" spellcheck="false" aria-label="兑换码" :aria-invalid="!!redeemError" />
            <p v-if="redeemError" class="text-destructive text-xs" role="alert">
              {{ redeemError }}
            </p>
            <p v-else-if="showPreview" class="text-xs" :class="normalized ? 'text-muted-foreground' : 'text-amber-700 dark:text-amber-400'">
              <template v-if="normalized">
                将兑换：<span class="text-foreground font-mono">{{ normalized }}</span>
              </template>
              <template v-else>
                兑换码应包含 20 位字符（不含 OG 前缀）
              </template>
            </p>
            <Button type="submit" class="w-full" :disabled="redeeming || !code.trim()">
              <Loader2 v-if="redeeming" class="animate-spin" />
              兑换
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>

    <UsageLimitsCard ref="limitsCard" />

    <section class="space-y-3" aria-labelledby="my-subs-title">
      <div class="flex flex-wrap items-end justify-between gap-2">
        <div class="space-y-1">
          <h2 id="my-subs-title" class="flex items-center gap-2 text-base font-semibold">
            <Package class="size-4" />
            我的订阅
          </h2>
          <p class="text-muted-foreground text-sm">
            套餐覆盖的模型优先使用套餐额度，不扣钱包余额；额度用完后的处理方式由下方设置决定。
          </p>
        </div>
        <Button size="sm" as-child data-testid="store-link">
          <RouterLink to="/console/store">
            <ShoppingCart />
            购买 / 续费 / 升级套餐
          </RouterLink>
        </Button>
      </div>
      <div class="bg-card ring-foreground/10 space-y-3 rounded-xl p-4 ring-1" data-testid="overflow-setting">
        <div class="flex items-start justify-between gap-2">
          <div class="space-y-1">
            <h3 id="overflow-title" class="text-sm font-medium">
              套餐额度用完后
            </h3>
            <p class="text-muted-foreground text-xs">
              当覆盖所请求模型的有效订阅额度都已用完时生效。单个 API Key 可在「API Keys」中单独覆盖此设置。
            </p>
          </div>
          <Loader2 v-if="prefSaving" class="text-muted-foreground size-4 shrink-0 animate-spin" aria-label="正在保存" />
        </div>
        <ErrorState v-if="prefError" :error="prefError" @retry="loadPreferences" />
        <div v-else-if="overflow === null" class="grid gap-2 sm:grid-cols-2">
          <Skeleton v-for="i in 2" :key="i" class="h-16 w-full" />
        </div>
        <div v-else class="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-labelledby="overflow-title" :aria-busy="prefSaving">
          <label
            v-for="opt in QUOTA_OVERFLOW_OPTIONS"
            :key="opt.value"
            class="hover:bg-muted/50 has-checked:border-primary has-checked:bg-primary/5 flex cursor-pointer gap-2.5 rounded-lg border p-3 text-sm has-disabled:cursor-wait"
          >
            <input
              type="radio"
              name="quota-overflow"
              class="accent-primary mt-0.5"
              :value="opt.value"
              :checked="overflow === opt.value"
              :disabled="prefSaving"
              @change="setOverflow(opt.value)"
            >
            <span class="space-y-1">
              <span class="block font-medium">{{ opt.title }}</span>
              <span class="text-muted-foreground block text-xs">{{ opt.description }}</span>
            </span>
          </label>
        </div>
      </div>
      <ErrorState v-if="subsError" :error="subsError" @retry="loadSubscriptions" />
      <div v-else-if="subscriptions === null" class="grid gap-4 lg:grid-cols-2">
        <Skeleton v-for="i in 2" :key="i" class="h-48 w-full rounded-xl" />
      </div>
      <template v-else>
        <div v-if="activeSubs.length" class="grid gap-4 lg:grid-cols-2">
          <SubscriptionCard v-for="s in activeSubs" :key="s.id" :subscription="s" :now="now.getTime()" :overflow="overflow" store-link />
        </div>
        <EmptyState
          v-else
          :icon="Package"
          title="暂无有效订阅"
          description="可在「购买套餐」中用余额购买，也可通过兑换码或由管理员开通；没有订阅时，请求按钱包余额计费。"
          class="rounded-xl border"
        />
        <Collapsible v-if="pastSubs.length" v-model:open="pastOpen">
          <CollapsibleTrigger as-child>
            <Button variant="ghost" size="sm" class="text-muted-foreground">
              <ChevronDown class="transition-transform" :class="pastOpen ? 'rotate-180' : ''" />
              已过期 / 已取消（{{ pastSubs.length }}）
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div class="grid gap-4 pt-2 opacity-80 lg:grid-cols-2">
              <SubscriptionCard v-for="s in pastSubs" :key="s.id" :subscription="s" :now="now.getTime()" />
            </div>
          </CollapsibleContent>
        </Collapsible>
      </template>
    </section>

    <ResetCardsSection ref="cardsSection" :now="now.getTime()" @used="loadSubscriptions" />

    <Card>
      <CardHeader>
        <CardTitle class="text-base">
          资金流水
        </CardTitle>
        <CardDescription>充值、扣费、退款与管理员调整记录（倒序）</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <ErrorState v-if="ledgerError" :error="ledgerError" @retry="loadLedger" />
        <div v-else-if="ledgerLoading && ledger.length === 0" class="space-y-2">
          <Skeleton v-for="i in 5" :key="i" class="h-10 w-full" />
        </div>
        <EmptyState v-else-if="ledger.length === 0" :icon="ReceiptText" title="暂无流水" description="兑换充值或产生费用后，记录会出现在这里。" />
        <div v-else class="overflow-x-auto" :class="ledgerLoading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>类型</TableHead>
                <TableHead class="text-right">
                  金额
                </TableHead>
                <TableHead class="text-right">
                  变动后余额
                </TableHead>
                <TableHead class="hidden md:table-cell">
                  关联
                </TableHead>
                <TableHead class="hidden sm:table-cell">
                  备注
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="e in ledger" :key="e.id">
                <TableCell class="whitespace-nowrap tabular-nums">
                  {{ formatDateTime(e.createdAt) }}
                </TableCell>
                <TableCell>
                  <Badge :variant="e.kind === 'charge' ? 'outline' : e.kind === 'adjust' ? 'secondary' : 'default'">
                    {{ LEDGER_KIND_LABELS[e.kind as keyof typeof LEDGER_KIND_LABELS] ?? e.kind }}
                  </Badge>
                </TableCell>
                <TableCell class="text-right whitespace-nowrap tabular-nums" :class="amountClass(e.amount)">
                  {{ money(e.amount, { signed: true }) }}
                </TableCell>
                <TableCell class="text-right whitespace-nowrap tabular-nums">
                  {{ money(e.balanceAfter) }}
                </TableCell>
                <TableCell class="hidden md:table-cell">
                  <span class="text-muted-foreground text-xs">{{ REF_TYPE_LABELS[e.refType] ?? e.refType }}</span>
                  <span class="ml-1 font-mono text-xs" :title="e.refId">{{ e.refId.length > 12 ? `${e.refId.slice(0, 12)}…` : e.refId }}</span>
                </TableCell>
                <TableCell class="text-muted-foreground hidden max-w-64 truncate text-xs sm:table-cell" :title="e.note ?? undefined">
                  {{ e.note ?? '—' }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <DataPagination
          v-if="!ledgerError && total > 0"
          :page="page"
          :page-size="PAGE_SIZE"
          :total="total"
          :disabled="ledgerLoading"
          @update:page="(p) => router.replace({ query: { ...route.query, page: p > 1 ? String(p) : undefined } })"
        />
      </CardContent>
    </Card>
  </div>
</template>
