<script setup lang="ts">
import type { Plan, PlanStatus } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Archive, ArchiveRestore, MoreHorizontal, Package, Pencil, Plus, RefreshCw, Users } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, isVersionConflict } from '@/lib/api'
import { plansApi } from '@/lib/endpoints'
import { amountSign } from '@/lib/money'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { formatDuration, modelsSummary, PLAN_STATUS_LABELS, ruleSummary } from '@/lib/quota'
import { useCustomMetersStore } from '@/stores/customMeters'
import PlanFormSheet from './PlanFormSheet.vue'

const PAGE_SIZE = 20
const ALL = 'all'

const route = useRoute()
const router = useRouter()
const { money } = useCurrency()

const page = computed(() => queryInt(route.query.page, 1))
const status = computed(() => {
  const s = queryStr(route.query.status)
  return s === 'active' || s === 'archived' ? s : ALL
})

function setQuery(next: Record<string, string | number | undefined>) {
  const q: Record<string, unknown> = { ...route.query }
  for (const [k, v] of Object.entries(next))
    q[k] = v === undefined || v === '' || v === ALL || (k === 'page' && v === 1) ? undefined : String(v)
  void router.replace({ query: q as Record<string, string> })
}

const plans = ref<Plan[]>([])
const total = ref(0)
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null
// Labels of billing-plugin meters in rule summaries (phase9 §3).
void useCustomMetersStore().load()

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await plansApi.list({ page: page.value, pageSize: PAGE_SIZE, status: status.value === ALL ? undefined : status.value as PlanStatus }, ctrl.signal)
    plans.value = res.items
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
watch([page, status], load, { immediate: true })
onBeforeUnmount(() => controller?.abort())

// ---------- create / edit ----------
const sheetOpen = ref(false)
const editing = ref<Plan | null>(null)
function openCreate() {
  editing.value = null
  sheetOpen.value = true
}
function openEdit(p: Plan) {
  editing.value = p
  sheetOpen.value = true
}
function onSaved(p: Plan, created: boolean) {
  if (created && page.value !== 1)
    setQuery({ page: undefined })
  else if (created)
    void load()
  else
    plans.value = plans.value.map(x => (x.id === p.id ? p : x))
}

// ---------- archive / unarchive ----------
const statusTarget = ref<Plan | null>(null)
const statusOpen = ref(false)
const statusSaving = ref(false)
function requestStatus(p: Plan) {
  statusTarget.value = p
  statusOpen.value = true
}
async function applyStatus() {
  const p = statusTarget.value
  if (!p)
    return
  const next: PlanStatus = p.status === 'active' ? 'archived' : 'active'
  statusSaving.value = true
  try {
    const updated = await plansApi.update(p.id, { status: next, version: p.version })
    toast.success(next === 'archived' ? `已下架「${p.name}」` : `已重新上架「${p.name}」`)
    statusOpen.value = false
    if (status.value !== ALL && status.value !== updated.status)
      void load()
    else
      plans.value = plans.value.map(x => (x.id === p.id ? updated : x))
  }
  catch (err) {
    statusOpen.value = false
    if (isVersionConflict(err)) {
      toast.warning('套餐已被他人修改，已刷新列表，请重试')
      void load()
    }
    else {
      toast.error('操作失败', { description: errorMessage(err) })
    }
  }
  finally {
    statusSaving.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="套餐" description="（管理员）定义套餐的有效期、覆盖模型与周期配额规则。设置售价后用户可用余额购买 / 续费 / 升级，也可通过兑换码或管理员开通。">
      <template #actions>
        <Button variant="outline" size="sm" as-child>
          <RouterLink to="/console/billing/subscriptions">
            <Users />
            订阅管理
          </RouterLink>
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus />
          新建套餐
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <div class="flex flex-wrap items-center gap-2">
          <Select :model-value="status" @update:model-value="(v) => setQuery({ status: String(v), page: undefined })">
            <SelectTrigger class="w-36" aria-label="按状态过滤">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="ALL">
                全部状态
              </SelectItem>
              <SelectItem value="active">
                上架中
              </SelectItem>
              <SelectItem value="archived">
                已下架
              </SelectItem>
            </SelectContent>
          </Select>
          <span class="text-muted-foreground text-xs">共 {{ total }} 个</span>
          <Button variant="ghost" size="icon-sm" class="ml-auto" aria-label="刷新" :disabled="loading" @click="load">
            <RefreshCw :class="loading ? 'animate-spin' : ''" />
          </Button>
        </div>

        <ErrorState v-if="loadError" :error="loadError" @retry="load" />
        <div v-else-if="loading && plans.length === 0" class="space-y-2">
          <Skeleton v-for="i in 4" :key="i" class="h-12 w-full" />
        </div>
        <EmptyState
          v-else-if="plans.length === 0"
          :icon="Package"
          :title="status === ALL ? '还没有套餐' : '没有符合条件的套餐'"
          :description="status === ALL ? '创建一个套餐，例如“5 小时会话 200 次 + 每周 500 万 tokens”。' : undefined"
        >
          <Button v-if="status === ALL" size="sm" @click="openCreate">
            <Plus />
            新建套餐
          </Button>
        </EmptyState>
        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead class="hidden sm:table-cell">
                  有效期
                </TableHead>
                <TableHead class="hidden lg:table-cell">
                  模型
                </TableHead>
                <TableHead class="hidden md:table-cell">
                  规则
                </TableHead>
                <TableHead class="hidden xl:table-cell">
                  叠加
                </TableHead>
                <TableHead>状态</TableHead>
                <TableHead class="text-right">
                  有效订阅
                </TableHead>
                <TableHead class="hidden text-right sm:table-cell">
                  售价
                </TableHead>
                <TableHead class="w-10">
                  <span class="sr-only">操作</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="p in plans" :key="p.id" :class="p.status === 'archived' ? 'text-muted-foreground' : ''">
                <TableCell class="max-w-56">
                  <button type="button" class="hover:text-primary block max-w-full truncate text-left font-medium hover:underline" :title="p.name" @click="openEdit(p)">
                    {{ p.name }}
                  </button>
                  <p v-if="p.description" class="text-muted-foreground truncate text-xs" :title="p.description">
                    {{ p.description }}
                  </p>
                  <p class="text-muted-foreground text-xs md:hidden">
                    {{ p.rules.length }} 条规则 · {{ formatDuration(p.duration) }}
                  </p>
                </TableCell>
                <TableCell class="hidden whitespace-nowrap sm:table-cell">
                  {{ formatDuration(p.duration) }}
                </TableCell>
                <TableCell class="hidden max-w-48 text-xs lg:table-cell">
                  <span class="block truncate" :title="p.models.join('\n') || '全部模型'">{{ modelsSummary(p.models) }}</span>
                </TableCell>
                <TableCell class="hidden max-w-80 text-xs md:table-cell">
                  <ul class="space-y-0.5">
                    <li v-for="r in p.rules" :key="r.id" class="truncate" :title="`${r.id} · ${ruleSummary(r, money)}`">
                      {{ ruleSummary(r, money) }}
                    </li>
                  </ul>
                </TableCell>
                <TableCell class="hidden text-xs xl:table-cell">
                  {{ p.stackable ? '可叠加' : '续期' }}
                </TableCell>
                <TableCell>
                  <Badge :variant="p.status === 'active' ? 'default' : 'secondary'">
                    {{ PLAN_STATUS_LABELS[p.status] ?? p.status }}
                  </Badge>
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  <RouterLink v-if="p.subscribers > 0" :to="{ path: '/console/billing/subscriptions', query: { planId: p.id, status: 'active' } }" class="hover:text-primary hover:underline">
                    {{ p.subscribers }}
                  </RouterLink>
                  <span v-else>0</span>
                </TableCell>
                <TableCell class="hidden text-right whitespace-nowrap tabular-nums sm:table-cell">
                  {{ amountSign(p.listPrice) > 0 ? money(p.listPrice) : '不售卖' }}
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon-sm" :aria-label="`${p.name} 的操作`">
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem @select="openEdit(p)">
                        <Pencil />
                        编辑
                      </DropdownMenuItem>
                      <DropdownMenuItem as-child>
                        <RouterLink :to="{ path: '/console/billing/subscriptions', query: { planId: p.id } }">
                          <Users />
                          查看订阅
                        </RouterLink>
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem v-if="p.status === 'active'" @select="requestStatus(p)">
                        <Archive />
                        下架
                      </DropdownMenuItem>
                      <DropdownMenuItem v-else @select="requestStatus(p)">
                        <ArchiveRestore />
                        重新上架
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
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
          @update:page="(p) => setQuery({ page: p })"
        />
      </CardContent>
    </Card>

    <PlanFormSheet v-model:open="sheetOpen" :plan="editing" @saved="onSaved" />

    <ConfirmDialog
      v-model:open="statusOpen"
      :title="statusTarget?.status === 'active' ? `下架套餐「${statusTarget?.name}」？` : `重新上架套餐「${statusTarget?.name}」？`"
      :confirm-text="statusTarget?.status === 'active' ? '下架' : '上架'"
      :destructive="statusTarget?.status === 'active'"
      :loading="statusSaving"
      @confirm="applyStatus"
    >
      <template v-if="statusTarget?.status === 'active'">
        <p>下架后该套餐不会出现在用户的购买页中，也不能再通过余额购买、管理员或兑换码开通 / 续期（对应兑换码将兑换失败），也不能作为升级目标；持有该套餐的用户仍可补差价升级到其他在售套餐。</p>
        <p><strong class="text-foreground">已开通的订阅不受影响</strong>，会按原有规则继续生效直到到期（当前有效订阅 {{ statusTarget?.subscribers ?? 0 }} 个）。</p>
      </template>
      <p v-else>
        重新上架后，套餐会出现在用户的购买页中，并可再次购买或开通。
      </p>
    </ConfirmDialog>
  </div>
</template>
