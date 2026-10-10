<script setup lang="ts">
import type { ReferralInfo, ReferralRebate } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Coins, Gift, Info, Link2, RefreshCw, Users } from '@lucide/vue'
import CopyButton from '@/components/CopyButton.vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatTile from '@/components/StatTile.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { billingApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isAbortError, queryInt } from '@/lib/query'
import { formatRate, referralRuleText } from '@/lib/referral'

const PAGE_SIZE = 20

const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

// ---------- referral info ----------
const info = ref<ReferralInfo | null>(null)
const infoLoading = ref(false)
const infoError = ref<unknown>(null)
async function loadInfo() {
  infoLoading.value = true
  infoError.value = null
  try {
    info.value = await billingApi.referral()
  }
  catch (err) {
    infoError.value = err
  }
  finally {
    infoLoading.value = false
  }
}
onMounted(loadInfo)
const invitees = computed(() => info.value?.invitees ?? [])

// ---------- rebates ----------
const page = computed(() => queryInt(route.query.page, 1))
const rebates = ref<ReferralRebate[]>([])
const total = ref(0)
const rebatesLoading = ref(false)
const rebatesError = ref<unknown>(null)
let controller: AbortController | null = null
async function loadRebates() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  rebatesLoading.value = true
  rebatesError.value = null
  try {
    const res = await billingApi.referralRebates({ page: page.value, pageSize: PAGE_SIZE }, ctrl.signal)
    rebates.value = res.items ?? []
    total.value = res.total ?? 0
  }
  catch (err) {
    if (!isAbortError(err))
      rebatesError.value = err
  }
  finally {
    if (controller === ctrl)
      rebatesLoading.value = false
  }
}
watch(page, loadRebates, { immediate: true })
onBeforeUnmount(() => controller?.abort())

function refresh() {
  void loadInfo()
  void loadRebates()
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="邀请返利" description="分享你的邀请链接，好友注册后用兑换码充值，你将获得余额返利。">
      <template #badge>
        <Badge v-if="info" :variant="info.enabled ? 'default' : 'secondary'">
          {{ info.enabled ? '返利进行中' : '返利未开启' }}
        </Badge>
      </template>
      <template #actions>
        <Button variant="outline" size="sm" :disabled="infoLoading || rebatesLoading" @click="refresh">
          <RefreshCw :class="infoLoading || rebatesLoading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <ErrorState v-if="infoError" :error="infoError" @retry="loadInfo" />
    <template v-else>
      <div
        v-if="info && !info.enabled"
        role="status"
        class="bg-muted/50 flex gap-2 rounded-xl border p-4 text-sm"
        data-testid="referral-disabled"
      >
        <Info class="text-muted-foreground mt-0.5 size-4 shrink-0" />
        <span>邀请返利活动暂未开启，邀请关系仍会记录。</span>
      </div>

      <Card>
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            <Link2 class="size-4" />
            我的邀请链接
          </CardTitle>
          <CardDescription v-if="info" data-testid="referral-rule">
            {{ referralRuleText(info, money) }}
          </CardDescription>
          <Skeleton v-else class="h-4 w-72 max-w-full" />
        </CardHeader>
        <CardContent class="space-y-3">
          <Skeleton v-if="!info" class="h-9 w-full" />
          <div v-else class="flex gap-2">
            <Input :model-value="info.link" readonly class="font-mono text-xs" aria-label="邀请链接" data-testid="invite-link" @focus="(e: FocusEvent) => (e.target as HTMLInputElement).select()" />
            <CopyButton :value="info.link" label="复制链接" show-label variant="outline" size="default" />
          </div>
          <ul class="text-muted-foreground list-disc space-y-1 pl-4 text-xs">
            <li v-if="info">
              邀请码：<span class="text-foreground font-mono">{{ info.code }}</span>
            </li>
            <li>只有通过链接<strong class="text-foreground">首次注册</strong>的新用户会与你绑定，已有账号登录不会绑定；绑定后永久有效。</li>
            <li>仅好友兑换<strong class="text-foreground">余额兑换码</strong>时返利；套餐兑换码、余额购买套餐、管理员调整与注册赠送不返利。</li>
            <li v-if="info">
              当前返利比例 {{ formatRate(info.rate) }}，返利直接计入你的钱包余额。
            </li>
          </ul>
        </CardContent>
      </Card>

      <div class="grid gap-4 sm:grid-cols-2">
        <StatTile label="已邀请好友" :icon="Users" :value="info ? String(info.invitedCount) : '—'" :loading="!info" />
        <StatTile label="累计返利" :icon="Coins" :value="info ? money(info.rebateTotal) : '—'" :loading="!info" />
      </div>

      <Card>
        <CardHeader>
          <CardTitle class="text-base">
            我邀请的好友
          </CardTitle>
          <CardDescription>最近 50 位通过你的链接注册的用户（名称已脱敏）。</CardDescription>
        </CardHeader>
        <CardContent>
          <div v-if="!info" class="space-y-2">
            <Skeleton v-for="i in 3" :key="i" class="h-10 w-full" />
          </div>
          <EmptyState v-else-if="invitees.length === 0" :icon="Gift" title="还没有邀请到好友" description="复制上方链接分享给好友，好友注册后会出现在这里。" />
          <div v-else class="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>好友</TableHead>
                  <TableHead>注册时间</TableHead>
                  <TableHead class="text-right">
                    为你带来的返利
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="(u, i) in invitees" :key="`${u.joinedAt}-${i}`">
                  <TableCell>{{ u.displayName }}</TableCell>
                  <TableCell class="whitespace-nowrap tabular-nums">
                    {{ formatDateTime(u.joinedAt) }}
                  </TableCell>
                  <TableCell class="text-right whitespace-nowrap tabular-nums">
                    {{ money(u.rebateTotal) }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
        </CardContent>
      </Card>
    </template>

    <Card>
      <CardHeader>
        <CardTitle class="text-base">
          返利记录
        </CardTitle>
        <CardDescription>每笔返利同时记入「钱包与订阅」的资金流水（倒序）。</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <ErrorState v-if="rebatesError" :error="rebatesError" @retry="loadRebates" />
        <div v-else-if="rebatesLoading && rebates.length === 0" class="space-y-2">
          <Skeleton v-for="i in 3" :key="i" class="h-10 w-full" />
        </div>
        <EmptyState v-else-if="rebates.length === 0" :icon="Coins" title="暂无返利记录" description="好友使用兑换码充值后，返利记录会出现在这里。" />
        <div v-else class="overflow-x-auto" :class="rebatesLoading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>好友</TableHead>
                <TableHead class="text-right">
                  充值金额
                </TableHead>
                <TableHead class="hidden text-right sm:table-cell">
                  比例
                </TableHead>
                <TableHead class="text-right">
                  返利
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="r in rebates" :key="r.id">
                <TableCell class="whitespace-nowrap tabular-nums">
                  {{ formatDateTime(r.createdAt) }}
                </TableCell>
                <TableCell>{{ r.inviteeName }}</TableCell>
                <TableCell class="text-right whitespace-nowrap tabular-nums">
                  {{ money(r.recharge) }}
                </TableCell>
                <TableCell class="hidden text-right tabular-nums sm:table-cell">
                  {{ formatRate(r.rate) }}
                </TableCell>
                <TableCell class="text-right whitespace-nowrap text-emerald-700 tabular-nums dark:text-emerald-400">
                  {{ money(r.rebate, { signed: true }) }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <DataPagination
          v-if="!rebatesError && total > 0"
          :page="page"
          :page-size="PAGE_SIZE"
          :total="total"
          :disabled="rebatesLoading"
          @update:page="(p) => router.replace({ query: { ...route.query, page: p > 1 ? String(p) : undefined } })"
        />
      </CardContent>
    </Card>
  </div>
</template>
