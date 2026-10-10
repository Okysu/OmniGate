<script setup lang="ts">
import type { ResetCardBatch } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Ban, Plus, RefreshCw, RotateCcw } from '@lucide/vue'
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { errorMessage } from '@/lib/api'
import { adminResetCardsApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isAbortError, queryInt } from '@/lib/query'
import { planRestrictionText, RESET_CARD_KIND_LABELS, targetSummary } from '@/lib/resetCards'
import ResetCardIssueDialog from './ResetCardIssueDialog.vue'

const PAGE_SIZE = 20

const route = useRoute()
const router = useRouter()

const page = computed(() => queryInt(route.query.page, 1))
const batches = ref<ResetCardBatch[]>([])
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
    const res = await adminResetCardsApi.listBatches({ page: page.value, pageSize: PAGE_SIZE }, ctrl.signal)
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

const issueOpen = ref(false)
function onIssued() {
  if (page.value === 1)
    void load()
  else
    void router.replace({ query: { ...route.query, page: undefined } })
}

const revokeTarget = ref<ResetCardBatch | null>(null)
const revokeOpen = ref(false)
const revoking = ref(false)
function askRevoke(b: ResetCardBatch) {
  revokeTarget.value = b
  revokeOpen.value = true
}
async function revoke() {
  const b = revokeTarget.value
  if (!b)
    return
  revoking.value = true
  try {
    const updated = await adminResetCardsApi.revoke(b.id)
    batches.value = batches.value.map(x => (x.id === b.id ? updated : x))
    toast.success(`已作废 ${updated.counts.revoked} 张未使用的重置卡`, { description: '已使用的卡不受影响。' })
    revokeOpen.value = false
  }
  catch (err) {
    revokeOpen.value = false
    toast.error('作废失败', { description: errorMessage(err) })
    void load()
  }
  finally {
    revoking.value = false
  }
}

function isExpired(b: ResetCardBatch): boolean {
  return !!b.expiresAt && new Date(b.expiresAt).getTime() <= Date.now()
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="重置卡" description="（管理员）向用户发放 5 小时 / 周 / 双重置卡。用户使用后，对应额度窗口立即清零并从使用时起重新计时。">
      <template #actions>
        <Button size="sm" data-testid="card-issue" @click="issueOpen = true">
          <Plus />
          发放重置卡
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardHeader>
        <div class="flex items-center justify-between gap-2">
          <div class="space-y-1.5">
            <CardTitle class="text-base">
              发放批次
            </CardTitle>
            <CardDescription>作废批次后，其中尚未使用的卡无法再使用；已使用的卡与已重置的额度不受影响。</CardDescription>
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
        <EmptyState v-else-if="batches.length === 0" :icon="RotateCcw" title="还没有发放过重置卡" description="发放给指定用户、用户组、某个套餐的有效订阅用户或全部用户。">
          <Button size="sm" @click="issueOpen = true">
            <Plus />
            发放重置卡
          </Button>
        </EmptyState>
        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table data-testid="card-batches">
            <TableHeader>
              <TableRow>
                <TableHead>卡类型</TableHead>
                <TableHead>发放对象</TableHead>
                <TableHead class="text-right">
                  已用 / 可用 / 过期 / 作废
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  有效期
                </TableHead>
                <TableHead class="hidden md:table-cell">
                  备注
                </TableHead>
                <TableHead class="hidden xl:table-cell">
                  发放时间
                </TableHead>
                <TableHead class="text-right">
                  操作
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="b in batches" :key="b.id">
                <TableCell class="whitespace-nowrap">
                  <div class="flex flex-col items-start gap-1">
                    <Badge variant="secondary">
                      {{ RESET_CARD_KIND_LABELS[b.kind] ?? b.kind }}
                    </Badge>
                    <Badge v-if="b.status === 'revoked'" variant="outline" class="text-destructive">
                      已作废
                    </Badge>
                  </div>
                </TableCell>
                <TableCell class="max-w-56">
                  <p class="truncate text-sm" :title="targetSummary(b.target)">
                    {{ targetSummary(b.target) }}
                  </p>
                  <p class="text-muted-foreground text-xs tabular-nums">
                    {{ b.recipients }} 人 × {{ b.quantity }} 张 = {{ b.counts.issued }} 张
                  </p>
                  <p v-if="b.plans.length" class="text-muted-foreground truncate text-xs" :title="planRestrictionText(b.plans)">
                    {{ planRestrictionText(b.plans) }}
                  </p>
                </TableCell>
                <TableCell class="text-right whitespace-nowrap tabular-nums">
                  <span class="font-medium">{{ b.counts.used }}</span>
                  <span class="text-muted-foreground"> / </span>
                  <span>{{ b.counts.available }}</span>
                  <span class="text-muted-foreground"> / {{ b.counts.expired }} / {{ b.counts.revoked }}</span>
                </TableCell>
                <TableCell class="hidden text-xs lg:table-cell" :class="isExpired(b) ? 'text-destructive' : ''">
                  {{ b.expiresAt ? `至 ${formatDateTime(b.expiresAt)}` : '长期有效' }}
                  <span v-if="isExpired(b)">（已过期）</span>
                </TableCell>
                <TableCell class="text-muted-foreground hidden max-w-48 truncate text-xs md:table-cell" :title="b.note || undefined">
                  {{ b.note || '—' }}
                </TableCell>
                <TableCell class="text-muted-foreground hidden text-xs xl:table-cell">
                  {{ formatDateTime(b.createdAt) }}
                  <span class="block">{{ b.createdBy.displayName }}</span>
                </TableCell>
                <TableCell class="text-right">
                  <Button
                    v-if="b.status === 'active' && b.counts.available + b.counts.expired > 0"
                    variant="ghost"
                    size="sm"
                    class="text-destructive"
                    data-testid="card-revoke"
                    @click="askRevoke(b)"
                  >
                    <Ban />
                    作废
                  </Button>
                  <span v-else class="text-muted-foreground text-xs">—</span>
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

    <ResetCardIssueDialog v-model:open="issueOpen" @issued="onIssued" />

    <ConfirmDialog
      v-model:open="revokeOpen"
      title="作废这批重置卡？"
      confirm-text="作废"
      destructive
      :loading="revoking"
      @confirm="revoke"
    >
      <p v-if="revokeTarget">
        {{ RESET_CARD_KIND_LABELS[revokeTarget.kind] }} · {{ targetSummary(revokeTarget.target) }}：
        <strong class="text-foreground">{{ revokeTarget.counts.available + revokeTarget.counts.expired }} 张未使用的卡</strong>将无法再使用，此操作不可撤销。已使用的 {{ revokeTarget.counts.used }} 张不受影响。
      </p>
    </ConfirmDialog>
  </div>
</template>
