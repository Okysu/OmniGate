<script setup lang="ts">
// Streaming test result (phase9 §2): per-call timeline of canonical events with
// timing against the 50 ms / 5 s limits, and the converted Chat chunks.
import type { StreamResultView } from '@/lib/pluginStream'
import type { CanonicalEvent } from '@/lib/types'
import { computed, ref } from 'vue'
import { Badge } from '@/components/ui/badge'
import { formatMs } from '@/lib/format'
import { STREAM_CALL_LIMIT_MS, STREAM_TOTAL_LIMIT_MS, TEST_TIMEOUT_SCALE } from '@/lib/pluginProtocol'
import { chatChunksText, chunkPreview, EVENT_TYPE_LABELS, eventSummary, limitPercent } from '@/lib/pluginStream'

const props = defineProps<{
  view: StreamResultView
  /** Input chunks as sent (for previews); empty when the case came from a file. */
  inputs: string[]
}>()

const tab = ref<'timeline' | 'chunks'>('timeline')
const chunksText = computed(() => chatChunksText(props.view.chatChunks))

const EVENT_CLASSES: Record<string, string> = {
  delta: 'border-sky-500/40 text-sky-800 dark:text-sky-300',
  finish: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  usage: 'border-violet-500/40 text-violet-800 dark:text-violet-300',
  error: 'border-destructive/50 text-destructive',
}
function eventClass(e: CanonicalEvent): string {
  return EVENT_CLASSES[e.type] ?? ''
}
</script>

<template>
  <section class="space-y-2" data-testid="stream-result">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
      <span class="text-muted-foreground">JS 总耗时</span>
      <span class="tabular-nums" :class="view.totalOverLimit ? 'text-destructive font-medium' : ''">{{ formatMs(view.totalMs) }} / {{ STREAM_TOTAL_LIMIT_MS / 1000 }} s</span>
      <span class="bg-muted relative h-1.5 w-24 overflow-hidden rounded-full" aria-hidden="true">
        <span class="absolute inset-y-0 left-0 rounded-full" :class="view.totalOverLimit ? 'bg-destructive' : 'bg-emerald-500'" :style="{ width: `${Math.max(limitPercent(view.totalMs, STREAM_TOTAL_LIMIT_MS), view.totalMs > 0 ? 2 : 0)}%` }" />
      </span>
      <span class="text-muted-foreground">{{ view.calls.length ? `${view.calls.length} 次调用 · ` : '' }}{{ view.events.length }} 个事件 · 单次调用上限 {{ STREAM_CALL_LIMIT_MS }} ms</span>
    </div>
    <p class="text-muted-foreground text-xs" data-testid="stream-limits-note">
      生产环境中每次 parseStream 调用限时 {{ STREAM_CALL_LIMIT_MS }} ms、整个请求的 JS 总耗时上限 {{ STREAM_TOTAL_LIMIT_MS / 1000 }} s；编辑器测试放宽为 {{ TEST_TIMEOUT_SCALE }} 倍，接近上限时请优化代码。<template v-if="!view.calls.length">
        服务端只返回整个会话（全部 parseStream + endStream）的总耗时，没有逐次调用的耗时。
      </template>
    </p>

    <div class="flex gap-1 border-b" role="tablist" aria-label="流式结果">
      <button
        v-for="t in ([['timeline', '事件时间线'], ['chunks', `Chat chunks（${view.chatChunks.length}）`]] as const)"
        :key="t[0]"
        type="button"
        role="tab"
        :aria-selected="tab === t[0]"
        class="relative h-8 px-2 text-xs font-medium"
        :class="tab === t[0] ? 'text-foreground after:bg-foreground after:absolute after:inset-x-1 after:bottom-0 after:h-0.5' : 'text-muted-foreground hover:text-foreground'"
        @click="tab = t[0]"
      >
        {{ t[1] }}
      </button>
    </div>

    <template v-if="tab === 'timeline'">
      <!-- per-call rows when the server reports them -->
      <ol v-if="view.calls.length" class="divide-y rounded-md border text-xs" data-testid="stream-timeline">
        <li v-for="c in view.calls" :key="c.index" class="grid gap-x-3 gap-y-1 px-2 py-1.5 sm:grid-cols-[9rem_minmax(0,1fr)]">
          <div class="min-w-0 space-y-1">
            <p class="flex items-center gap-1.5">
              <span class="font-mono">{{ c.hook }}</span>
              <span v-if="c.chunk !== null" class="text-muted-foreground tabular-nums">#{{ c.chunk + 1 }}</span>
            </p>
            <p class="flex items-center gap-1.5">
              <span class="bg-muted relative h-1.5 w-14 overflow-hidden rounded-full" aria-hidden="true">
                <span class="absolute inset-y-0 left-0 rounded-full" :class="c.overLimit ? 'bg-destructive' : 'bg-emerald-500'" :style="{ width: `${Math.max(limitPercent(c.durationMs, STREAM_CALL_LIMIT_MS), c.durationMs > 0 ? 4 : 0)}%` }" />
              </span>
              <span class="tabular-nums" :class="c.overLimit ? 'text-destructive font-medium' : 'text-muted-foreground'" :title="`单次调用上限 ${STREAM_CALL_LIMIT_MS} ms`">{{ formatMs(c.durationMs) }}</span>
            </p>
          </div>
          <div class="min-w-0 space-y-1">
            <p v-if="c.chunk !== null && inputs[c.chunk] !== undefined" class="text-muted-foreground truncate font-mono" :title="inputs[c.chunk]">
              ← {{ chunkPreview(inputs[c.chunk] ?? '') }}
            </p>
            <p v-if="!c.events.length && !c.error" class="text-muted-foreground">
              无事件
            </p>
            <ul v-else class="flex flex-wrap gap-1">
              <li v-for="(e, ei) in c.events" :key="ei">
                <Badge variant="outline" class="max-w-full gap-1 font-normal" :class="eventClass(e)">
                  <span class="font-medium">{{ EVENT_TYPE_LABELS[e.type] ?? e.type }}</span>
                  <span class="truncate font-mono">{{ eventSummary(e) }}</span>
                </Badge>
              </li>
            </ul>
            <p v-if="c.error" class="text-destructive font-mono break-words whitespace-pre-wrap">
              {{ c.error }}
            </p>
          </div>
        </li>
      </ol>
      <!-- flat event list otherwise -->
      <ol v-else-if="view.events.length" class="divide-y rounded-md border text-xs" data-testid="stream-events">
        <li v-for="(e, i) in view.events" :key="i" class="flex items-center gap-2 px-2 py-1">
          <span class="text-muted-foreground w-6 shrink-0 tabular-nums">{{ i + 1 }}</span>
          <Badge variant="outline" class="font-normal" :class="eventClass(e)">
            {{ EVENT_TYPE_LABELS[e.type] ?? e.type }}
          </Badge>
          <span class="min-w-0 truncate font-mono">{{ eventSummary(e) }}</span>
        </li>
      </ol>
      <p v-else class="text-muted-foreground text-xs">
        没有输出任何事件。
      </p>
    </template>
    <template v-else>
      <p v-if="view.chatChunksLocal" class="text-muted-foreground text-xs">
        按宿主的转换规则在浏览器中生成（客户端为 Chat Completions 时收到的 chunk；Messages / Responses 客户端再由网关转换）。
      </p>
      <p v-if="view.chatChunksError" class="text-destructive text-xs" role="alert">
        {{ view.chatChunksError }}
      </p>
      <pre v-if="chunksText" class="bg-muted/40 max-h-64 overflow-auto rounded-md border p-2 font-mono text-xs">{{ chunksText }}</pre>
      <p v-else class="text-muted-foreground text-xs">
        没有可转换的事件。
      </p>
    </template>
  </section>
</template>
