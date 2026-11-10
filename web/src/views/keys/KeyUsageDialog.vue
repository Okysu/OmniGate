<script setup lang="ts">
import type { GatewayKey, KeyUsage, KeyUsageRange } from '@/lib/types'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { BarChart3, ScrollText } from '@lucide/vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import StatTile from '@/components/StatTile.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useCurrency } from '@/composables/useCurrency'
import { keysApi } from '@/lib/endpoints'
import { formatCompact, formatNumber } from '@/lib/format'
import { isAbortError } from '@/lib/query'

const props = defineProps<{ gatewayKey: GatewayKey | null }>()
const open = defineModel<boolean>('open', { required: true })

const RANGES: { value: KeyUsageRange, label: string }[] = [
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
  { value: 'all', label: '全部' },
]
function isRange(v: unknown): v is KeyUsageRange {
  return RANGES.some(r => r.value === v)
}

const router = useRouter()
const { money } = useCurrency()
const range = ref<KeyUsageRange>('24h')
const data = ref<KeyUsage | null>(null)
const loading = ref(false)
const loadError = ref<unknown>(null)
let controller: AbortController | null = null

async function load() {
  const k = props.gatewayKey
  if (!k)
    return
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    data.value = await keysApi.usage(k.id, range.value, ctrl.signal)
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

watch([open, () => props.gatewayKey?.id, range], ([o], [prevOpen, prevId]) => {
  if (!o) {
    controller?.abort()
    return
  }
  if (!prevOpen || prevId !== props.gatewayKey?.id)
    data.value = null
  void load()
})
watch(() => props.gatewayKey?.id, () => (range.value = '24h'))
onBeforeUnmount(() => controller?.abort())

const totals = computed(() => data.value?.totals ?? null)
const tokenTiles = computed(() => {
  const t = totals.value
  return [
    { label: '总 Token', value: t?.totalTokens, hint: t ? `${formatNumber(t.requests)} 次请求` : undefined },
    { label: '输入', value: t?.inputTokens, hint: '不含缓存' },
    { label: '缓存读取', value: t?.cacheReadTokens, hint: t && t.cacheWriteTokens > 0 ? `缓存写入 ${formatCompact(t.cacheWriteTokens)}` : undefined },
    { label: '输出', value: t?.outputTokens, hint: t && t.reasoningTokens > 0 ? `含推理 ${formatCompact(t.reasoningTokens)}` : undefined },
  ]
})

function share(n: number): number {
  const total = totals.value?.totalTokens ?? 0
  return total > 0 ? n / total : 0
}

function openLogs(model?: string) {
  const k = props.gatewayKey
  if (!k)
    return
  open.value = false
  void router.push({ name: 'logs', query: { keyId: k.id, model } })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-3xl">
      <DialogHeader>
        <DialogTitle>用量统计 · {{ gatewayKey?.name }}</DialogTitle>
        <DialogDescription>
          该 Key 的模型请求 Token 用量。「全部」覆盖保留期内的所有日志。
        </DialogDescription>
      </DialogHeader>

      <div class="flex flex-wrap items-center justify-between gap-2">
        <Tabs :model-value="range" @update:model-value="(v) => isRange(v) && (range = v)">
          <TabsList>
            <TabsTrigger v-for="r in RANGES" :key="r.value" :value="r.value" :data-testid="`key-usage-range-${r.value}`">
              {{ r.label }}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <span v-if="totals" class="text-muted-foreground text-xs tabular-nums" :title="money(totals.charge)">
          计费 {{ money(totals.charge) }}
        </span>
      </div>

      <ErrorState v-if="loadError" :error="loadError" @retry="load" />
      <template v-else>
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatTile
            v-for="t in tokenTiles"
            :key="t.label"
            :label="t.label"
            :value="formatCompact(t.value)"
            :title="formatNumber(t.value)"
            :hint="t.hint"
            :loading="loading && !data"
          />
        </div>

        <div v-if="loading && !data" class="space-y-2">
          <Skeleton v-for="i in 3" :key="i" class="h-9 w-full" />
        </div>
        <EmptyState v-else-if="data && data.byModel.length === 0" :icon="BarChart3" title="该时段没有模型请求" description="切换时间范围，或用这个 Key 调用一次模型。" />
        <div v-else-if="data" class="overflow-x-auto" :class="loading ? 'opacity-60 transition-opacity' : ''">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>模型</TableHead>
                <TableHead class="text-right">
                  请求
                </TableHead>
                <TableHead class="text-right">
                  输入
                </TableHead>
                <TableHead class="text-right">
                  缓存读取
                </TableHead>
                <TableHead class="hidden text-right sm:table-cell">
                  缓存写入
                </TableHead>
                <TableHead class="text-right">
                  输出
                </TableHead>
                <TableHead class="text-right">
                  合计
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="m in data.byModel" :key="m.model" class="cursor-pointer" title="查看该模型的请求日志" @click="openLogs(m.model)">
                <TableCell class="max-w-56">
                  <div class="truncate font-mono text-xs" :title="m.model">
                    {{ m.model || '（未知）' }}
                  </div>
                  <div class="bg-muted mt-1 h-1 w-full overflow-hidden rounded-full">
                    <div class="bg-primary h-full rounded-full" :style="{ width: `${(share(m.totalTokens) * 100).toFixed(1)}%` }" />
                  </div>
                </TableCell>
                <TableCell class="text-right tabular-nums">
                  {{ formatNumber(m.requests) }}
                </TableCell>
                <TableCell class="text-right tabular-nums" :title="formatNumber(m.inputTokens)">
                  {{ formatCompact(m.inputTokens) }}
                </TableCell>
                <TableCell class="text-right tabular-nums" :title="formatNumber(m.cacheReadTokens)">
                  {{ formatCompact(m.cacheReadTokens) }}
                </TableCell>
                <TableCell class="hidden text-right tabular-nums sm:table-cell" :title="formatNumber(m.cacheWriteTokens)">
                  {{ formatCompact(m.cacheWriteTokens) }}
                </TableCell>
                <TableCell class="text-right tabular-nums" :title="m.reasoningTokens > 0 ? `${formatNumber(m.outputTokens)}（含推理 ${formatNumber(m.reasoningTokens)}）` : formatNumber(m.outputTokens)">
                  {{ formatCompact(m.outputTokens) }}
                </TableCell>
                <TableCell class="text-right font-medium tabular-nums" :title="`${formatNumber(m.totalTokens)} · 计费 ${money(m.charge)}`">
                  {{ formatCompact(m.totalTokens) }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </template>

      <div class="flex justify-end">
        <Button variant="outline" size="sm" @click="openLogs()">
          <ScrollText />
          查看请求日志
        </Button>
      </div>
    </DialogContent>
  </Dialog>
</template>
