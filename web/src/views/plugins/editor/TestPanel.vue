<script setup lang="ts">
// Mock tests (`POST draft/test`): a tests/*.json case from the draft, a custom
// case typed here, or a streaming case built from upstream chunks (phase9 §2,
// custom-protocol plugins). og.fetch only hits the case's `fetch` mocks, never the network.
import type { StreamCaseForm } from '@/lib/pluginStream'
import type { BuildResult, PluginTestCase, PluginTestResult } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { CircleCheck, CircleX, FlaskConical, Loader2, Play } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatMs } from '@/lib/format'
import { buildStreamCase, emptyStreamForm, isStreamCase, normalizeStreamResult, streamFormFromCase } from '@/lib/pluginStream'
import StreamCaseEditor from './StreamCaseEditor.vue'
import StreamResult from './StreamResult.vue'

const props = defineProps<{
  pluginId: string
  /** tests/*.json paths in the draft. */
  testFiles: string[]
  /** Current editor content by path (to read a case's capability). */
  readFile: (path: string) => string | undefined
  /** Saves pending changes first (the server runs the saved draft); false aborts. */
  prepare: () => Promise<boolean>
  /** The draft's manifest declares `protocol: "custom"` (preselects the streaming builder when there are no test files). */
  customProtocol?: boolean
}>()
const emit = defineEmits<{
  result: [result: PluginTestResult, capability: string | null]
  buildFailed: [build: BuildResult]
}>()

const CUSTOM = '__custom__'
const STREAM = '__stream__'
const selected = ref<string>(props.testFiles[0] ?? (props.customProtocol ? STREAM : CUSTOM))
const streamForm = ref<StreamCaseForm>(emptyStreamForm())
const streamErrors = ref<Record<string, string>>({})
/** Input chunks of the last run (timeline previews). */
const lastInputs = ref<string[]>([])
/** The last run was a streaming case (an empty event array is still a streaming result). */
const lastWasStream = ref(false)
const custom = ref(JSON.stringify({
  name: '自定义用例',
  capability: 'models.list',
  secrets: { apiKey: 'sk-test' },
  fetch: [{ match: { method: 'GET', url: 'https://api.example.com/v1/models' }, response: { status: 200, json: { data: [{ id: 'model-a' }] } } }],
}, null, 2))
const customError = ref<string | null>(null)
const running = ref(false)
const runError = ref<string | null>(null)
const result = ref<PluginTestResult | null>(null)
const lastName = ref('')

watch(() => props.testFiles, (list) => {
  if (selected.value !== CUSTOM && selected.value !== STREAM && !list.includes(selected.value))
    selected.value = list[0] ?? (props.customProtocol ? STREAM : CUSTOM)
})

function parseFile(path: string): unknown {
  try {
    return JSON.parse(props.readFile(path) ?? 'null')
  }
  catch {
    return null
  }
}
const selectedIsStreamFile = computed(() => selected.value !== CUSTOM && selected.value !== STREAM && isStreamCase(parseFile(selected.value)))

function useAsCustom() {
  if (selected.value === CUSTOM || selected.value === STREAM)
    return
  const parsed = parseFile(selected.value)
  if (isStreamCase(parsed)) {
    streamForm.value = streamFormFromCase(parsed as PluginTestCase)
    streamErrors.value = {}
    selected.value = STREAM
    return
  }
  const text = props.readFile(selected.value)
  if (text !== undefined)
    custom.value = text
  selected.value = CUSTOM
}

function chunksOf(c: unknown): string[] {
  const list = (c as { chunks?: unknown } | null)?.chunks
  return Array.isArray(list) ? list.filter((x): x is string => typeof x === 'string') : []
}

function caseCapability(c: unknown): string | null {
  return c !== null && typeof c === 'object' && typeof (c as Record<string, unknown>).capability === 'string'
    ? (c as Record<string, string>).capability ?? null
    : null
}

async function run(): Promise<void> {
  customError.value = null
  runError.value = null
  let body: PluginTestCase
  let capability: string | null = null
  if (selected.value === STREAM) {
    const built = buildStreamCase(streamForm.value)
    streamErrors.value = built.errors
    if (!built.body)
      return
    body = built.body
    lastName.value = body.name ?? '流式用例'
    lastInputs.value = body.chunks ?? []
  }
  else if (selected.value === CUSTOM) {
    try {
      const parsed: unknown = JSON.parse(custom.value)
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed))
        throw new Error('用例必须是 JSON 对象')
      body = parsed as PluginTestCase
      capability = caseCapability(parsed)
      lastName.value = body.name || '自定义用例'
      lastInputs.value = chunksOf(parsed)
    }
    catch (err) {
      customError.value = `用例不是合法 JSON：${err instanceof Error ? err.message : String(err)}`
      return
    }
  }
  else {
    body = { case: selected.value }
    try {
      const parsed: unknown = JSON.parse(props.readFile(selected.value) ?? 'null')
      capability = caseCapability(parsed)
      lastInputs.value = chunksOf(parsed)
      lastName.value = (parsed as { name?: string } | null)?.name || selected.value
    }
    catch {
      lastName.value = selected.value
    }
  }
  lastWasStream.value = selected.value === STREAM || isStreamCase(body) || (body.case !== undefined && selectedIsStreamFile.value)
  running.value = true
  try {
    if (!(await props.prepare()))
      return
    const res = await pluginsApi.test(props.pluginId, body)
    result.value = res
    if (res.build && !res.build.ok)
      emit('buildFailed', res.build)
    else
      emit('result', res, capability)
  }
  catch (err) {
    runError.value = errorMessage(err)
  }
  finally {
    running.value = false
  }
}

defineExpose({ run })

const outputText = computed(() => {
  const r = result.value
  if (!r || r.output === null || r.output === undefined)
    return ''
  try {
    return JSON.stringify(r.output, null, 2)
  }
  catch {
    return String(r.output)
  }
})
const builtFailed = computed(() => !!result.value?.build && !result.value.build.ok)
const streamView = computed(() => normalizeStreamResult(result.value, { streamCase: lastWasStream.value }))
</script>

<template>
  <!-- narrow screens: the whole panel scrolls; wide: the two columns scroll separately -->
  <div class="flex h-full min-h-0 flex-col gap-3 overflow-y-auto p-3 lg:flex-row lg:overflow-hidden">
    <!-- case picker -->
    <div class="flex flex-col gap-2 lg:min-h-0 lg:w-[22rem] lg:shrink-0">
      <div class="flex gap-2">
        <Select v-model="selected">
          <SelectTrigger class="h-8 min-w-0 flex-1 text-xs" aria-label="测试用例">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="f in testFiles" :key="f" :value="f" class="font-mono text-xs">
              {{ f }}
            </SelectItem>
            <SelectItem :value="CUSTOM">
              自定义用例（JSON）
            </SelectItem>
            <SelectItem :value="STREAM">
              流式用例（parseStream）
            </SelectItem>
          </SelectContent>
        </Select>
        <Button size="sm" :disabled="running" @click="run">
          <Loader2 v-if="running" class="animate-spin" />
          <Play v-else />
          运行
        </Button>
      </div>
      <div v-if="selected === STREAM" class="pr-1 lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
        <StreamCaseEditor v-model:form="streamForm" :errors="streamErrors" />
      </div>
      <template v-else-if="selected === CUSTOM">
        <Textarea
          v-model="custom"
          class="min-h-32 flex-1 resize-none font-mono text-xs"
          spellcheck="false"
          aria-label="自定义用例 JSON"
        />
        <p v-if="customError" class="text-destructive text-xs" role="alert">
          {{ customError }}
        </p>
        <p v-else class="text-muted-foreground text-xs">
          字段同 tests/*.json：capability 或 hook + request、input、config、secrets、baseUrl、fetch、expect；流式用例为 hook: "parseStream" + chunks。未知字段会被拒绝。
        </p>
      </template>
      <template v-else>
        <p class="text-muted-foreground text-xs">
          运行前会先保存草稿（服务端使用已保存的文件）。
        </p>
        <Button variant="link" size="xs" class="w-fit px-0" @click="useAsCustom">
          {{ selectedIsStreamFile ? '复制到流式用例编辑器并修改' : '复制为自定义用例并修改' }}
        </Button>
      </template>
      <p v-if="testFiles.length === 0 && selected !== CUSTOM && selected !== STREAM" class="text-muted-foreground text-xs">
        草稿中没有 tests/*.json 用例。
      </p>
    </div>

    <!-- result -->
    <div class="min-w-0 lg:min-h-0 lg:flex-1 lg:overflow-y-auto" data-testid="test-result-pane">
      <p v-if="runError" class="text-destructive text-sm" role="alert">
        {{ runError }}
      </p>
      <div v-else-if="!result" class="text-muted-foreground flex flex-col items-center justify-center gap-2 py-6 text-sm lg:h-full lg:py-0">
        <FlaskConical class="size-6" />
        选择用例后点击「运行」。
      </div>
      <div v-else-if="builtFailed" class="text-destructive text-sm">
        草稿未通过编译检查，测试未执行。详见「问题」。
      </div>
      <div v-else class="space-y-3">
        <div class="flex flex-wrap items-center gap-2 text-sm">
          <CircleCheck v-if="result.ok" class="size-4 text-emerald-600 dark:text-emerald-400" />
          <CircleX v-else class="text-destructive size-4" />
          <span class="font-medium">{{ result.ok ? '通过' : '未通过' }}</span>
          <span class="text-muted-foreground">{{ lastName }}</span>
          <span class="text-muted-foreground ml-auto text-xs tabular-nums">{{ formatMs(result.durationMs) }}</span>
        </div>
        <p v-if="result.expectation" class="rounded-md border border-amber-500/40 bg-amber-500/5 px-2.5 py-1.5 text-sm">
          期望不符：{{ result.expectation }}
        </p>
        <p v-if="result.error" class="border-destructive/40 bg-destructive/5 text-destructive rounded-md border px-2.5 py-1.5 font-mono text-xs break-words whitespace-pre-wrap">
          {{ result.error }}
        </p>

        <StreamResult v-if="streamView" :view="streamView" :inputs="lastInputs" />

        <section v-else-if="outputText" class="space-y-1">
          <h4 class="text-muted-foreground text-xs font-medium">
            输出
          </h4>
          <pre class="bg-muted/40 max-h-64 overflow-auto rounded-md border p-2 font-mono text-xs">{{ outputText }}</pre>
        </section>

        <section class="space-y-1">
          <h4 class="text-muted-foreground text-xs font-medium">
            og.fetch 记录（{{ result.fetches?.length ?? 0 }}）
          </h4>
          <p v-if="!result.fetches?.length" class="text-muted-foreground text-xs">
            无
          </p>
          <div v-else class="overflow-x-auto rounded-md border">
            <table class="w-full text-xs">
              <thead>
                <tr class="text-muted-foreground border-b text-left">
                  <th class="px-2 py-1 font-medium">
                    方法
                  </th>
                  <th class="px-2 py-1 font-medium">
                    URL
                  </th>
                  <th class="px-2 py-1 font-medium">
                    状态
                  </th>
                  <th class="px-2 py-1 text-right font-medium">
                    耗时
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(f, i) in result.fetches" :key="i" class="border-b font-mono last:border-0">
                  <td class="px-2 py-1">
                    {{ f.method }}
                  </td>
                  <td class="px-2 py-1 break-all">
                    {{ f.url }}
                    <span v-if="f.error" class="text-destructive block font-sans">{{ f.error }}</span>
                  </td>
                  <td class="px-2 py-1">
                    <Badge variant="outline" :class="f.status >= 400 || !f.status ? 'text-destructive' : ''">
                      {{ f.status || '—' }}
                    </Badge>
                  </td>
                  <td class="px-2 py-1 text-right tabular-nums">
                    {{ formatMs(f.durationMs) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section class="space-y-1">
          <h4 class="text-muted-foreground text-xs font-medium">
            日志（{{ result.logs?.length ?? 0 }}）
          </h4>
          <p v-if="!result.logs?.length" class="text-muted-foreground text-xs">
            无
          </p>
          <ul v-else class="bg-muted/40 space-y-0.5 rounded-md border p-2 font-mono text-xs">
            <li v-for="(l, i) in result.logs" :key="i" class="flex gap-2">
              <span class="w-10 shrink-0 uppercase" :class="l.level === 'error' ? 'text-destructive' : l.level === 'warn' ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'">{{ l.level }}</span>
              <span class="break-all whitespace-pre-wrap">{{ l.message }}</span>
            </li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>
