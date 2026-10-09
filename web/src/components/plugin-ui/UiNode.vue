<script setup lang="ts">
// Recursive renderer for one declarative component (ADR-0003, server/internal/plugin/ui.go).
// Plugins cannot ship markup: every node maps to a fixed host component and all
// text is rendered as text (never v-html).
import type { ResultMap } from './bindings'
import type { UiColumn, UiNode as UiNodeT } from '@/lib/types'
import { computed } from 'vue'
import { CircleAlert, CircleCheck, ExternalLink, Info, TriangleAlert } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { resolveBind, resolvePointer } from '@/lib/jsonPointer'
import { formatUiValue } from '@/lib/pluginFormat'
import { plainParagraphs, progressRatio, safeHttpsUrl } from './bindings'
import UiValue from './UiValue.vue'

defineOptions({ name: 'UiNode' })

const props = withDefaults(defineProps<{
  node: UiNodeT
  results: ResultMap
  /** Inside a statGroup: render stats as tiles. */
  inGroup?: boolean
}>(), { inGroup: false })

function bound(bind: string | undefined): unknown {
  if (!bind)
    return undefined
  const r = resolveBind(bind, props.results)
  return r.state === 'ok' ? r.value : undefined
}

const value = computed(() => bound(props.node.bind))
const currency = computed(() => {
  const c = bound(props.node.currencyBind)
  return typeof c === 'string' ? c : null
})

// ---- table ----
const rows = computed<unknown[] | null>(() => {
  if (props.node.type !== 'table')
    return null
  const v = bound(props.node.rowsBind)
  return Array.isArray(v) ? v : null
})
const columns = computed<UiColumn[]>(() => props.node.columns ?? [])
function cell(row: unknown, col: UiColumn): unknown {
  if (col.key.startsWith('/'))
    return resolvePointer(row, col.key).value
  if (row !== null && typeof row === 'object' && Object.prototype.hasOwnProperty.call(row, col.key))
    return (row as Record<string, unknown>)[col.key]
  return undefined
}
function rowCurrency(row: unknown): string | null {
  if (row !== null && typeof row === 'object') {
    const c = (row as Record<string, unknown>).currency
    if (typeof c === 'string')
      return c
  }
  return currency.value
}

// ---- progress ----
const progress = computed(() => {
  if (props.node.type !== 'progress')
    return null
  const v = bound(props.node.valueBind)
  const m = bound(props.node.maxBind)
  return { value: v, max: m, ratio: progressRatio(v, m) }
})
const progressText = computed(() => {
  const p = progress.value
  if (!p)
    return ''
  const fmt = props.node.format || 'number'
  const a = formatUiValue(p.value, fmt, { currency: currency.value }).text
  const b = formatUiValue(p.max, fmt, { currency: currency.value }).text
  return `${a} / ${b}`
})

// ---- keyValue ----
const kvEntries = computed<Array<{ key: string, label: string, value: unknown, format?: string, currency: string | null }>>(() => {
  if (props.node.type !== 'keyValue')
    return []
  if (props.node.items?.length) {
    return props.node.items.map((it, i) => {
      const c = bound(it.currencyBind)
      return { key: `${i}`, label: it.label ?? '', value: it.bind ? bound(it.bind) : it.text, format: it.format, currency: typeof c === 'string' ? c : null }
    })
  }
  const obj = value.value
  if (obj !== null && typeof obj === 'object' && !Array.isArray(obj))
    return Object.entries(obj as Record<string, unknown>).map(([k, v]) => ({ key: k, label: k, value: v, format: undefined, currency: null }))
  return []
})

const alertText = computed(() => props.node.text || (value.value !== undefined ? formatUiValue(value.value, props.node.format).text : ''))
const ALERT_CLASSES: Record<string, string> = {
  info: 'border-sky-500/30 bg-sky-500/5 text-sky-900 dark:text-sky-200',
  success: 'border-emerald-500/30 bg-emerald-500/5 text-emerald-900 dark:text-emerald-200',
  warning: 'border-amber-500/40 bg-amber-500/5 text-amber-900 dark:text-amber-200',
  error: 'border-destructive/40 bg-destructive/5 text-destructive',
}
const alertIcon = computed(() => ({ success: CircleCheck, warning: TriangleAlert, error: CircleAlert }[props.node.level ?? ''] ?? Info))

const badge = computed(() => formatUiValue(props.node.bind ? value.value : props.node.text, props.node.format, { currency: currency.value }))

const href = computed(() => (props.node.type === 'link' ? safeHttpsUrl(props.node.href) : null))
const paragraphs = computed(() => (props.node.type === 'markdown' ? plainParagraphs(props.node.text) : []))
</script>

<template>
  <!-- statGroup -->
  <div v-if="node.type === 'statGroup'" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
    <UiNode v-for="(child, i) in node.items ?? []" :key="i" :node="child" :results="results" in-group />
  </div>

  <!-- stat -->
  <div
    v-else-if="node.type === 'stat'"
    class="flex min-w-0 flex-col gap-1"
    :class="inGroup ? 'bg-muted/40 rounded-lg p-3' : ''"
  >
    <span class="text-muted-foreground truncate text-xs">{{ node.label }}</span>
    <span class="truncate text-lg font-semibold tabular-nums">
      <UiValue :value="node.bind ? value : node.text" :format="node.format" :currency="currency" />
    </span>
  </div>

  <!-- keyValue -->
  <dl v-else-if="node.type === 'keyValue'" class="grid grid-cols-[minmax(6rem,auto)_1fr] gap-x-4 gap-y-2 text-sm">
    <template v-if="kvEntries.length">
      <template v-for="e in kvEntries" :key="e.key">
        <dt class="text-muted-foreground truncate">
          {{ e.label }}
        </dt>
        <dd class="min-w-0 break-words tabular-nums">
          <UiValue :value="e.value" :format="e.format" :currency="e.currency" />
        </dd>
      </template>
    </template>
    <dd v-else class="text-muted-foreground col-span-2">
      —
    </dd>
  </dl>

  <!-- table -->
  <div v-else-if="node.type === 'table'" class="overflow-x-auto">
    <p v-if="node.label" class="mb-2 text-sm font-medium">
      {{ node.label }}
    </p>
    <table class="w-full text-sm">
      <thead>
        <tr class="border-b">
          <th v-for="col in columns" :key="col.key" class="text-muted-foreground h-8 px-2 text-left text-xs font-medium whitespace-nowrap">
            {{ col.label }}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, ri) in rows ?? []" :key="ri" class="border-b last:border-0">
          <td v-for="col in columns" :key="col.key" class="px-2 py-1.5 align-top tabular-nums">
            <UiValue :value="cell(row, col)" :format="col.format" :currency="rowCurrency(row)" />
          </td>
        </tr>
        <tr v-if="!rows?.length">
          <td :colspan="Math.max(1, columns.length)" class="text-muted-foreground px-2 py-3 text-center text-xs">
            {{ rows ? '没有数据' : '—' }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>

  <!-- progress -->
  <div v-else-if="node.type === 'progress'" class="space-y-1.5">
    <div class="flex items-baseline justify-between gap-2 text-sm">
      <span class="text-muted-foreground truncate text-xs">{{ node.label }}</span>
      <span class="text-xs tabular-nums">
        {{ progressText }}<template v-if="progress?.ratio != null"> · {{ Math.round(progress.ratio * 100) }}%</template>
      </span>
    </div>
    <div
      class="bg-muted h-2 overflow-hidden rounded-full"
      role="progressbar"
      :aria-label="node.label || '进度'"
      aria-valuemin="0"
      aria-valuemax="100"
      :aria-valuenow="progress?.ratio != null ? Math.round(progress.ratio * 100) : undefined"
    >
      <div
        v-if="progress?.ratio != null"
        class="h-full rounded-full transition-[width]"
        :class="progress.ratio >= 0.9 ? 'bg-destructive' : progress.ratio >= 0.75 ? 'bg-amber-500' : 'bg-primary'"
        :style="{ width: `${progress.ratio * 100}%` }"
      />
    </div>
  </div>

  <!-- badge -->
  <Badge v-else-if="node.type === 'badge'" variant="outline" class="tabular-nums">
    <span v-if="node.label" class="text-muted-foreground">{{ node.label }}</span>
    <span :title="badge.title">{{ badge.text }}</span>
  </Badge>

  <!-- alert -->
  <div v-else-if="node.type === 'alert'" class="flex items-start gap-2 rounded-lg border p-3 text-sm" :class="ALERT_CLASSES[node.level ?? ''] ?? ALERT_CLASSES.info" role="note">
    <component :is="alertIcon" class="mt-0.5 size-4 shrink-0" />
    <div class="min-w-0 space-y-0.5">
      <p v-if="node.label" class="font-medium">
        {{ node.label }}
      </p>
      <p class="break-words whitespace-pre-line">
        {{ alertText || '—' }}
      </p>
    </div>
  </div>

  <!-- markdown: rendered as plain-text paragraphs (no HTML, no markdown syntax) -->
  <div v-else-if="node.type === 'markdown'" class="space-y-2 text-sm leading-relaxed">
    <p v-for="(p, i) in paragraphs" :key="i" class="break-words whitespace-pre-line">
      {{ p }}
    </p>
  </div>

  <!-- link: https only -->
  <a
    v-else-if="node.type === 'link' && href"
    :href="href"
    target="_blank"
    rel="noopener noreferrer"
    class="text-primary inline-flex items-center gap-1 text-sm underline-offset-4 hover:underline"
    :title="`外部链接：${href}`"
  >
    {{ node.label || node.text || href }}
    <ExternalLink class="size-3.5" aria-hidden="true" />
    <span class="sr-only">（在新窗口打开外部链接）</span>
  </a>
  <span v-else-if="node.type === 'link'" class="text-muted-foreground text-sm">
    {{ node.label || node.text || '链接' }}（仅允许 https 链接）
  </span>

  <p v-else class="text-muted-foreground text-xs">
    不支持的组件类型「{{ node.type }}」
  </p>
</template>
