<script setup lang="ts">
import type { BillingSettings, RedeemBatch, RedeemBatchCreated } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Package, Plus, RefreshCw, Scale, ShieldCheck, Ticket } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isVersionConflict } from '@/lib/api'
import { adminBillingApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isAbortError, queryInt } from '@/lib/query'
import AdjustWalletDialog from './AdjustWalletDialog.vue'
import BatchCreateDialog from './BatchCreateDialog.vue'
import CodesOnceDialog from './CodesOnceDialog.vue'

const PAGE_SIZE = 20

const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

// ---------- billing settings ----------
const settings = ref<BillingSettings | null>(null)
const settingsError = ref<unknown>(null)
const settingsLoading = ref(false)
async function loadSettings() {
  settingsLoading.value = true
  settingsError.value = null
  try {
    settings.value = await adminBillingApi.settings()
  }
  catch (err) {
    settingsError.value = err
  }
  finally {
    settingsLoading.value = false
  }
}
onMounted(loadSettings)

const enforceConfirm = ref(false)
const enforceTarget = ref(false)
const enforceSaving = ref(false)
function requestEnforce(v: boolean) {
  enforceTarget.value = v
  enforceConfirm.value = true
}
async function applyEnforce() {
  if (!settings.value)
    return
  enforceSaving.value = true
  try {
    settings.value = await adminBillingApi.updateSettings({ enforce: enforceTarget.value, version: settings.value.version })
    toast.success(enforceTarget.value ? '已开启余额强制（预付费）' : '已关闭余额强制（仅记录费用）')
    enforceConfirm.value = false
  }
  catch (err) {
    enforceConfirm.value = false
    if (isVersionConflict(err)) {
      toast.warning('计费设置已被他人修改，已刷新')
      await loadSettings()
    }
    else {
      toast.error('保存失败', { description: errorMessage(err) })
    }
  }
  finally {
    enforceSaving.value = false
  }
}

// ---------- batches ----------
const page = computed(() => queryInt(route.query.page, 1))
const batches = ref<RedeemBatch[]>([])
const total = ref(0)
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
    const res = await adminBillingApi.listBatches({ page: page.value, pageSize: PAGE_SIZE }, ctrl.signal)
    batches.value = res.items
    total.value = res.total
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
watch(page, load, { immediate: true })
onBeforeUnmount(() => controller?.abort())

const toggling = ref<Set<string>>(new Set())
async function toggleBatch(b: RedeemBatch, active: boolean) {
  toggling.value = new Set(toggling.value).add(b.id)
  try {
    const updated = await adminBillingApi.setBatchStatus(b.id, active ? 'active' : 'disabled', b.version)
    batches.value = batches.value.map(x => (x.id === b.id ? updated : x))
    toast.success(active ? '批次已启用' : '批次已停用，其中未使用的兑换码将无法兑换')
  }
  catch (err) {
    if (isVersionConflict(err)) {
      toast.warning('批次已被修改，已刷新')
      await load()
    }
    else {
      toast.error('操作失败', { description: errorMessage(err) })
    }
  }
  finally {
    const next = new Set(toggling.value)
    next.delete(b.id)
    toggling.value = next
  }
}

function validity(b: RedeemBatch): string {
  if (!b.validFrom && !b.expiresAt)
    return '长期有效'
  return `${b.validFrom ? formatDateTime(b.validFrom) : '立即'} 至 ${b.expiresAt ? formatDateTime(b.expiresAt) : '长期'}`
}
function isExpired(b: RedeemBatch): boolean {
  return !!b.expiresAt && new Date(b.expiresAt).getTime() <= Date.now()
}

// ---------- dialogs ----------
const createOpen = ref(false)
const created = ref<RedeemBatchCreated | null>(null)
const codesOpen = ref(false)
function onCreated(res: RedeemBatchCreated) {
  created.value = res
  codesOpen.value = true
  if (page.value === 1)
    void load()
  else
    void router.replace({ query: { ...route.query, page: undefined } })
}
function onCodesOpenChange(v: boolean) {
  codesOpen.value = v
  if (!v)
    created.value = null
}
const adjustOpen = ref(false)
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="兑换码" description="（管理员）生成与管理钱包充值 / 套餐兑换码、计费模式和用户余额。">
      <template #actions>
        <Button variant="outline" size="sm" @click="adjustOpen = true">
          <Scale />
          调整用户余额
        </Button>
        <Button size="sm" @click="createOpen = true">
          <Plus />
          生成兑换码
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardHeader>
        <CardTitle class="flex items-center gap-2 text-base">
          <ShieldCheck class="size-4" />
          计费设置
        </CardTitle>
        <CardDescription>控制网关是否在调用前校验用户余额。</CardDescription>
      </CardHeader>
      <CardContent>
        <ErrorState v-if="settingsError" :error="settingsError" @retry="loadSettings" />
        <Skeleton v-else-if="!settings" class="h-16 w-full" />
        <div v-else class="flex items-start justify-between gap-4 rounded-lg border p-3">
          <div class="space-y-1 text-sm">
            <p class="font-medium">
              余额强制（billing.enforce）
              <Badge :variant="settings.enforce ? 'default' : 'secondary'" class="ml-1">
                {{ settings.enforce ? '已开启' : '已关闭' }}
              </Badge>
            </p>
            <ul class="text-muted-foreground list-disc space-y-0.5 pl-4 text-xs">
              <li><strong class="text-foreground">关闭</strong>：只记录费用，不拦截请求（适合个人自用）。</li>
              <li><strong class="text-foreground">开启</strong>：预付费模式。可用余额大于 0 时放行请求，并预留「输入估算 + max_tokens（如请求指定）」的金额（不超过可用余额）；可用余额为 0 或负数时拒绝（402）；结束后按实际用量结算。没有售价的模型不受限制。</li>
            </ul>
          </div>
          <Switch
            :model-value="settings.enforce"
            :disabled="enforceSaving || settingsLoading"
            aria-label="余额强制"
            @update:model-value="(v: boolean) => requestEnforce(v)"
          />
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <div class="flex items-center justify-between gap-2">
          <div class="space-y-1.5">
            <CardTitle class="text-base">
              兑换码批次
            </CardTitle>
            <CardDescription>停用批次后，其中尚未用完的兑换码将无法兑换；已到账金额与已开通的订阅不受影响。</CardDescription>
          </div>
          <Button variant="ghost" size="icon-sm" aria-label="刷新" :disabled="loading" @click="load">
            <RefreshCw :class="loading ? 'animate-spin' : ''" />
          </Button>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="loading && batches.length === 0" class="space-y-2">
          <Skeleton v-for="i in 4" :key="i" class="h-10 w-full" />
        </div>
        <EmptyState v-else-if="batches.length === 0" :icon="Ticket" title="还没有兑换码批次" description="生成一批兑换码，分发给用户为钱包充值或开通套餐。">
          <Button size="sm" @click="createOpen = true">
            <Plus />
            生成兑换码
          </Button>
        </EmptyState>
        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="text-right">
                  面额 / 套餐
                </TableHead>
                <TableHead class="text-right">
                  已兑换 / 可兑换次数
                </TableHead>
                <TableHead class="hidden md:table-cell">
                  限制
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  有效期
                </TableHead>
                <TableHead>状态</TableHead>
                <TableHead class="hidden sm:table-cell">
                  备注
                </TableHead>
                <TableHead class="hidden xl:table-cell">
                  创建时间
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="b in batches" :key="b.id">
                <TableCell v-if="b.kind === 'plan'" class="max-w-48 text-right">
                  <span class="inline-flex max-w-full items-center gap-1">
                    <Package class="text-muted-foreground size-3.5 shrink-0" aria-hidden="true" />
                    <span class="truncate font-medium" :title="`套餐 ${b.planName ?? b.planId ?? ''} × ${b.periods ?? '?'}`">套餐 {{ b.planName ?? '—' }} × {{ b.periods ?? '?' }}</span>
                  </span>
                </TableCell>
                <TableCell v-else class="text-right font-medium tabular-nums">
                  {{ money(b.amount) }}
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ b.redeemed }} / {{ b.count * b.maxRedemptionsPerCode }}
                </TableCell>
                <TableCell class="text-muted-foreground hidden text-xs md:table-cell">
                  每码 {{ b.maxRedemptionsPerCode }} 次 · 每人 {{ b.perUserLimit }} 次
                </TableCell>
                <TableCell class="hidden text-xs lg:table-cell" :class="isExpired(b) ? 'text-destructive' : ''">
                  {{ validity(b) }}
                  <span v-if="isExpired(b)">（已过期）</span>
                </TableCell>
                <TableCell>
                  <div class="flex items-center gap-2">
                    <Switch
                      :model-value="b.status === 'active'"
                      :disabled="toggling.has(b.id)"
                      aria-label="批次是否启用"
                      @update:model-value="(v: boolean) => toggleBatch(b, v)"
                    />
                    <span class="text-xs" :class="b.status === 'active' ? 'text-muted-foreground' : 'text-destructive'">
                      {{ b.status === 'active' ? '启用' : '停用' }}
                    </span>
                  </div>
                </TableCell>
                <TableCell class="text-muted-foreground hidden max-w-48 truncate text-xs sm:table-cell" :title="b.note ?? undefined">
                  {{ b.note || '—' }}
                </TableCell>
                <TableCell class="text-muted-foreground hidden text-xs xl:table-cell">
                  {{ formatDateTime(b.createdAt) }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <DataPagination
          v-if="!loadError && total > 0"
          :page="page"
          :page-size="PAGE_SIZE"
          :total="total"
          :disabled="loading"
          @update:page="(p) => router.replace({ query: { ...route.query, page: p > 1 ? String(p) : undefined } })"
        />
      </CardContent>
    </Card>

    <BatchCreateDialog v-model:open="createOpen" @created="onCreated" />
    <CodesOnceDialog v-if="created" :open="codesOpen" :result="created" @update:open="onCodesOpenChange" />
    <AdjustWalletDialog v-model:open="adjustOpen" />

    <ConfirmDialog
      v-model:open="enforceConfirm"
      :title="enforceTarget ? '开启余额强制？' : '关闭余额强制？'"
      :confirm-text="enforceTarget ? '开启' : '关闭'"
      :destructive="enforceTarget"
      :loading="enforceSaving"
      @confirm="applyEnforce"
    >
      <p v-if="enforceTarget">
        开启后，所有用户调用有售价的模型前都会校验可用余额并预留金额，<strong class="text-foreground">可用余额为 0 或负数的请求将被拒绝</strong>。请确认用户已有足够余额。
      </p>
      <p v-else>
        关闭后，网关只记录费用而不再拦截请求，用户余额可能变为负数。
      </p>
    </ConfirmDialog>
  </div>
</template>
