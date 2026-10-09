<script setup lang="ts">
import type { NotificationCategory } from '@/lib/notifications'
import type { AppNotification } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useIntervalFn } from '@vueuse/core'
import { BellOff, CheckCheck, Inbox, Loader2, RefreshCw } from '@lucide/vue'
import { toast } from 'vue-sonner'
import DataPagination from '@/components/DataPagination.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import NotificationRow from '@/components/notifications/NotificationRow.vue'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { errorMessage } from '@/lib/api'
import { notificationsApi } from '@/lib/endpoints'
import { CATEGORIES, CATEGORY_LABELS, isCategory, typesParam } from '@/lib/notifications'
import { isAbortError, queryInt, queryStr } from '@/lib/query'
import { useNotificationsStore } from '@/stores/notifications'
import NotificationTabs from './NotificationTabs.vue'

const PAGE_SIZE = 20
const ALL = 'all'

const route = useRoute()
const router = useRouter()
const store = useNotificationsStore()
const now = ref(Date.now())
useIntervalFn(() => {
  now.value = Date.now()
}, 30_000)

const page = computed(() => queryInt(route.query.page, 1))
const unreadOnly = computed(() => queryStr(route.query.filter) === 'unread')
const category = computed<NotificationCategory | ''>(() => {
  const c = queryStr(route.query.category)
  return isCategory(c) ? c : ''
})

const items = ref<AppNotification[]>([])
const total = ref(0)
const loading = ref(false)
const loaded = ref(false)
const loadError = ref<unknown>(null)
const expanded = ref<string | null>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await notificationsApi.list({
      page: page.value,
      pageSize: PAGE_SIZE,
      unread: unreadOnly.value || undefined,
      type: typesParam(category.value),
    }, ctrl.signal)
    items.value = res.items
    total.value = res.total
    loaded.value = true
    expanded.value = null
    // Past the last page (e.g. after everything was marked read): step back.
    if (res.items.length === 0 && res.total > 0 && page.value > 1)
      setQuery({ page: Math.max(1, Math.ceil(res.total / PAGE_SIZE)) })
  }
  catch (err) {
    if (!isAbortError(err))
      loadError.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
  // Keep the sidebar badge in sync with what the page shows.
  void store.refresh()
}

function setQuery(next: { page?: number, unread?: boolean, category?: NotificationCategory | '' }) {
  const p = next.page ?? page.value
  const u = next.unread ?? unreadOnly.value
  const c = next.category ?? category.value
  void router.replace({
    query: {
      ...route.query,
      page: p > 1 ? String(p) : undefined,
      filter: u ? 'unread' : undefined,
      category: c || undefined,
    },
  })
}

watch([page, unreadOnly, category], load, { immediate: true })
onBeforeUnmount(() => controller?.abort())

const hasFilters = computed(() => unreadOnly.value || category.value !== '')

function onFilterTab(v: string | number) {
  setQuery({ unread: v === 'unread', page: 1 })
}
function onCategory(v: unknown) {
  setQuery({ category: isCategory(v) ? v : '', page: 1 })
}

// ---------- read state ----------
const marking = ref(new Set<string>())
const markingAll = ref(false)

async function markRead(n: AppNotification) {
  if (n.readAt || marking.value.has(n.id))
    return
  marking.value = new Set(marking.value).add(n.id)
  const readAt = new Date().toISOString()
  try {
    await notificationsApi.markRead([n.id])
    items.value = items.value.map(i => (i.id === n.id ? { ...i, readAt } : i))
    store.decrement()
  }
  catch (err) {
    toast.error('标为已读失败', { description: errorMessage(err) })
  }
  finally {
    const next = new Set(marking.value)
    next.delete(n.id)
    marking.value = next
  }
}

function onOpen(n: AppNotification) {
  expanded.value = expanded.value === n.id ? null : n.id
  void markRead(n)
}

async function markAll() {
  markingAll.value = true
  try {
    await notificationsApi.markAllRead()
    const readAt = new Date().toISOString()
    items.value = items.value.map(i => (i.readAt ? i : { ...i, readAt }))
    store.set(0)
    toast.success('已全部标为已读')
    if (unreadOnly.value)
      await load()
  }
  catch (err) {
    toast.error('操作失败', { description: errorMessage(err) })
  }
  finally {
    markingAll.value = false
  }
}

const unreadOnPage = computed(() => items.value.filter(i => !i.readAt).length)
const canMarkAll = computed(() => store.unread > 0 || unreadOnPage.value > 0)

const emptyTitle = computed(() => {
  if (unreadOnly.value)
    return category.value ? `没有未读的${CATEGORY_LABELS[category.value]}通知` : '没有未读通知'
  return category.value ? `暂无${CATEGORY_LABELS[category.value]}通知` : '暂无通知'
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="通知中心" description="钱包、套餐、模型、API Key、渠道与插件相关的站内通知，保留 90 天。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button size="sm" :disabled="!canMarkAll || markingAll" data-testid="mark-all-read" @click="markAll">
          <Loader2 v-if="markingAll" class="animate-spin" />
          <CheckCheck v-else />
          全部标为已读
        </Button>
      </template>
    </PageHeader>

    <NotificationTabs />

    <div class="flex flex-wrap items-center gap-2" data-testid="notification-toolbar">
      <Tabs :model-value="unreadOnly ? 'unread' : ALL" @update:model-value="onFilterTab">
        <TabsList>
          <TabsTrigger :value="ALL" data-testid="filter-all">
            全部
          </TabsTrigger>
          <TabsTrigger value="unread" data-testid="filter-unread">
            未读<span v-if="store.unread > 0" class="text-muted-foreground tabular-nums">（{{ store.unread > 99 ? '99+' : store.unread }}）</span>
          </TabsTrigger>
        </TabsList>
      </Tabs>
      <Select :model-value="category || ALL" @update:model-value="onCategory">
        <SelectTrigger class="w-36" aria-label="事件类型" data-testid="category-filter">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem :value="ALL">
            全部类型
          </SelectItem>
          <SelectItem v-for="c in CATEGORIES" :key="c" :value="c">
            {{ CATEGORY_LABELS[c] }}
          </SelectItem>
        </SelectContent>
      </Select>
      <Button v-if="hasFilters" variant="ghost" size="sm" @click="setQuery({ unread: false, category: '', page: 1 })">
        清除筛选
      </Button>
    </div>

    <Card class="gap-0 overflow-hidden py-0">
      <ErrorState v-if="loadError && !loaded" :error="loadError" @retry="load" />
      <div v-else-if="!loaded" class="divide-y">
        <div v-for="i in 4" :key="i" class="flex gap-3 px-4 py-3">
          <Skeleton class="size-8 rounded-full" />
          <div class="flex-1 space-y-2">
            <Skeleton class="h-4 w-1/3" />
            <Skeleton class="h-3 w-2/3" />
          </div>
        </div>
      </div>
      <EmptyState
        v-else-if="items.length === 0"
        :title="emptyTitle"
        :icon="unreadOnly ? Inbox : BellOff"
        :description="hasFilters ? '换个筛选条件试试。' : '有新的余额、套餐、模型、API Key 或渠道事件时会显示在这里。可在「通知设置」中选择通过邮件或 Webhook 接收。'"
        data-testid="notifications-empty"
      >
        <Button v-if="!hasFilters" variant="outline" size="sm" as-child>
          <RouterLink to="/console/notifications/settings">
            通知设置
          </RouterLink>
        </Button>
      </EmptyState>
      <template v-else>
        <p v-if="loadError" class="text-destructive border-b px-4 py-2 text-xs" role="alert">
          刷新失败：{{ errorMessage(loadError) }}
        </p>
        <ul class="divide-y" :class="loading ? 'opacity-60' : ''" data-testid="notification-list">
          <NotificationRow
            v-for="n in items"
            :key="n.id"
            :notification="n"
            :now="now"
            :expanded="expanded === n.id"
            @open="onOpen(n)"
            @read="markRead(n)"
            @follow="markRead(n)"
          />
        </ul>
      </template>
    </Card>

    <DataPagination
      v-if="loaded && total > PAGE_SIZE"
      :page="page"
      :page-size="PAGE_SIZE"
      :total="total"
      :disabled="loading"
      @update:page="p => setQuery({ page: p })"
    />
  </div>
</template>
