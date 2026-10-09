<script setup lang="ts">
import type { AuditLog } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ChevronRight, FileClock, RefreshCw, X } from '@lucide/vue'
import { watchDebounced } from '@vueuse/core'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { adminApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { isAbortError, queryInt, queryStr } from '@/lib/query'

const PAGE_SIZE = 50

const route = useRoute()
const router = useRouter()

const page = computed(() => queryInt(route.query.page, 1))
const action = computed(() => queryStr(route.query.action))
const actorId = computed(() => queryStr(route.query.actorId))

const actionInput = ref(action.value)
const actorInput = ref(actorId.value)

const items = ref<AuditLog[]>([])
const total = ref(0)
const loading = ref(false)
const loadError = ref<unknown>(null)
const expanded = ref(new Set<string>())
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await adminApi.listAuditLogs({
      page: page.value,
      pageSize: PAGE_SIZE,
      action: action.value || undefined,
      actorId: actorId.value || undefined,
    }, ctrl.signal)
    items.value = res.items
    total.value = res.total
    expanded.value = new Set()
  }
  catch (err) {
    if (isAbortError(err))
      return
    loadError.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}

function setQuery(next: { page?: number, action?: string, actorId?: string }) {
  const p = next.page ?? page.value
  const a = next.action ?? action.value
  const actor = next.actorId ?? actorId.value
  void router.replace({
    query: {
      ...route.query,
      page: p > 1 ? String(p) : undefined,
      action: a || undefined,
      actorId: actor || undefined,
    },
  })
}

watch([page, action, actorId], load, { immediate: true })
watch(action, (v) => {
  if (v !== actionInput.value.trim())
    actionInput.value = v
})
watch(actorId, (v) => {
  if (v !== actorInput.value.trim())
    actorInput.value = v
})
watchDebounced([actionInput, actorInput], ([a, actor]) => {
  const na = a.trim()
  const nactor = actor.trim()
  if (na !== action.value || nactor !== actorId.value)
    setQuery({ action: na, actorId: nactor, page: 1 })
}, { debounce: 400 })

onBeforeUnmount(() => controller?.abort())

const hasFilters = computed(() => action.value !== '' || actorId.value !== '')

function clearFilters() {
  actionInput.value = ''
  actorInput.value = ''
  setQuery({ action: '', actorId: '', page: 1 })
}

function filterByActor(id: string | null) {
  if (!id)
    return
  actorInput.value = id
  setQuery({ actorId: id, page: 1 })
}

function filterByAction(a: string) {
  actionInput.value = a
  setQuery({ action: a, page: 1 })
}

function toggle(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id))
    next.delete(id)
  else
    next.add(id)
  expanded.value = next
}

function hasMetadata(log: AuditLog): boolean {
  return !!log.metadata && Object.keys(log.metadata).length > 0
}

function prettyJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2)
  }
  catch {
    return String(value)
  }
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="审计日志" description="记录登录、权限变更与配置修改等敏感操作。日志只读，不可修改或删除。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <Card>
      <CardContent class="space-y-4">
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Input v-model="actionInput" placeholder="按操作筛选，如 user.update" class="sm:max-w-60" aria-label="按操作筛选" />
          <Input v-model="actorInput" placeholder="按操作者 ID 筛选" class="font-mono sm:max-w-72" aria-label="按操作者 ID 筛选" />
          <Button v-if="hasFilters" variant="ghost" size="sm" @click="clearFilters">
            <X />
            清除筛选
          </Button>
        </div>

        <ErrorState v-if="loadError" :error="loadError" @retry="load" />

        <div v-else-if="loading && items.length === 0" class="space-y-2">
          <Skeleton v-for="i in 8" :key="i" class="h-10 w-full" />
        </div>

        <EmptyState
          v-else-if="items.length === 0"
          :icon="FileClock"
          :title="hasFilters ? '没有匹配的审计记录' : '暂无审计记录'"
          :description="hasFilters ? '尝试调整或清除筛选条件。' : undefined"
        />

        <div v-else class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-8" />
                <TableHead>时间</TableHead>
                <TableHead>操作者</TableHead>
                <TableHead>操作</TableHead>
                <TableHead>对象</TableHead>
                <TableHead class="hidden md:table-cell">
                  IP 段
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <template v-for="log in items" :key="log.id">
                <TableRow :data-state="expanded.has(log.id) ? 'selected' : undefined">
                  <TableCell class="w-8 pr-0">
                    <Button
                      v-if="hasMetadata(log)"
                      variant="ghost"
                      size="icon-xs"
                      :aria-expanded="expanded.has(log.id)"
                      :aria-label="expanded.has(log.id) ? '收起详情' : '展开详情'"
                      @click="toggle(log.id)"
                    >
                      <ChevronRight class="transition-transform" :class="expanded.has(log.id) ? 'rotate-90' : ''" />
                    </Button>
                  </TableCell>
                  <TableCell class="whitespace-nowrap">
                    {{ formatDateTime(log.createdAt) }}
                  </TableCell>
                  <TableCell>
                    <button
                      v-if="log.actorId"
                      type="button"
                      class="hover:underline"
                      :title="`仅看此操作者：${log.actorId}`"
                      @click="filterByActor(log.actorId)"
                    >
                      {{ log.actorName ?? log.actorId }}
                    </button>
                    <span v-else class="text-muted-foreground">系统</span>
                  </TableCell>
                  <TableCell>
                    <button type="button" :title="`仅看此操作：${log.action}`" @click="filterByAction(log.action)">
                      <Badge variant="outline" class="font-mono">
                        {{ log.action }}
                      </Badge>
                    </button>
                  </TableCell>
                  <TableCell class="max-w-64">
                    <span class="text-muted-foreground">{{ log.resourceType }}</span>
                    <span v-if="log.resourceId" class="ml-1 font-mono text-xs break-all">{{ log.resourceId }}</span>
                  </TableCell>
                  <TableCell class="hidden font-mono text-xs md:table-cell">
                    {{ log.ipPrefix || '—' }}
                  </TableCell>
                </TableRow>
                <TableRow v-if="expanded.has(log.id)" class="hover:bg-transparent">
                  <TableCell />
                  <TableCell colspan="5" class="pt-0">
                    <pre class="bg-muted max-h-80 overflow-auto rounded-md p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">{{ prettyJson(log.metadata) }}</pre>
                  </TableCell>
                </TableRow>
              </template>
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
  </div>
</template>
