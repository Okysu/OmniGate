// phase9-api.md §2: streaming test cases of custom-protocol plugins. The test
// panel builds a case from a list of upstream byte chunks (typed as text or
// JSON), runs it through `POST /api/plugins/{id}/draft/test`, and renders the
// canonical events per parseStream call, the Chat Completions chunks the host
// converts them to, and the per-call timing against the runtime limits.
//
// The request / response shapes of streaming tests are NOT pinned by the
// contract ("输入一组上游字节块，断言输出事件"); see normalizeStreamResult for
// the accepted response variants.
import type { CanonicalEvent, PluginStreamCall, PluginTestCase, PluginTestResult } from './types'
import { STREAM_CALL_LIMIT_MS, STREAM_TOTAL_LIMIT_MS } from './pluginProtocol'

export type ChunkKind = 'text' | 'json'

export interface StreamChunkRow {
  /** Stable key for v-for (not sent). */
  uid: number
  kind: ChunkKind
  text: string
}

export interface StreamCaseForm {
  name: string
  chunks: StreamChunkRow[]
  /** JSON chunks: append "\n" after the serialized value (JSON Lines). */
  jsonNewline: boolean
  /** Optional JSON object: plugin config passed in ctx.config. */
  config: string
  /** Optional JSON array: expected events (subset match on the server). */
  expectEvents: string
}

export const MAX_STREAM_CHUNKS = 64

let uidSeq = 0
export function chunkRow(kind: ChunkKind = 'json', text = ''): StreamChunkRow {
  uidSeq += 1
  return { uid: uidSeq, kind, text }
}

/** Example rows for the fictional Acme JSON Lines protocol of the custom-protocol template (server plugin/template_custom.go). */
export function emptyStreamForm(): StreamCaseForm {
  return {
    name: '流式用例',
    chunks: [
      chunkRow('json', '{"event":"text","text":"你好"}'),
      chunkRow('json', '{"event":"text","text":"，世界"}'),
      chunkRow('json', '{"event":"done","stop":"end","usage":{"in":12,"out":4}}'),
    ],
    jsonNewline: true,
    config: '',
    expectEvents: '',
  }
}

/** Bytes (as UTF-8 text) sent for one row, or an error message. */
export function chunkPayload(row: Pick<StreamChunkRow, 'kind' | 'text'>, jsonNewline = true): { ok: true, value: string } | { ok: false, error: string } {
  if (row.kind === 'text') {
    if (row.text === '')
      return { ok: false, error: '内容为空' }
    return { ok: true, value: row.text }
  }
  const t = row.text.trim()
  if (!t)
    return { ok: false, error: '内容为空' }
  try {
    const v: unknown = JSON.parse(t)
    return { ok: true, value: JSON.stringify(v) + (jsonNewline ? '\n' : '') }
  }
  catch (err) {
    return { ok: false, error: `不是合法 JSON：${err instanceof Error ? err.message : String(err)}` }
  }
}

function parseOptionalJson(text: string, want: 'object' | 'array'): { ok: true, value: unknown } | { ok: false, error: string } {
  const t = text.trim()
  if (!t)
    return { ok: true, value: undefined }
  try {
    const v: unknown = JSON.parse(t)
    if (want === 'array' ? !Array.isArray(v) : (v === null || typeof v !== 'object' || Array.isArray(v)))
      return { ok: false, error: want === 'array' ? '必须是 JSON 数组' : '必须是 JSON 对象' }
    return { ok: true, value: v }
  }
  catch (err) {
    return { ok: false, error: `不是合法 JSON：${err instanceof Error ? err.message : String(err)}` }
  }
}

/**
 * Request body of a streaming test: `{name, hook: "parseStream", chunks, config?, expect?: {output: events}}`.
 * Errors are keyed `chunks.<i>`, `chunks`, `config`, `expectEvents`.
 */
export function buildStreamCase(f: StreamCaseForm): { body: PluginTestCase | null, errors: Record<string, string> } {
  const errors: Record<string, string> = {}
  const chunks: string[] = []
  if (!f.chunks.length)
    errors.chunks = '至少需要一个输入块'
  else if (f.chunks.length > MAX_STREAM_CHUNKS)
    errors.chunks = `最多 ${MAX_STREAM_CHUNKS} 个输入块`
  f.chunks.forEach((row, i) => {
    const p = chunkPayload(row, f.jsonNewline)
    if (p.ok)
      chunks.push(p.value)
    else
      errors[`chunks.${i}`] = p.error
  })
  const config = parseOptionalJson(f.config, 'object')
  if (!config.ok)
    errors.config = config.error
  const expectEvents = parseOptionalJson(f.expectEvents, 'array')
  if (!expectEvents.ok)
    errors.expectEvents = expectEvents.error
  if (Object.keys(errors).length)
    return { body: null, errors }
  const body: PluginTestCase = { name: f.name.trim() || '流式用例', hook: 'parseStream', chunks }
  if (config.ok && config.value !== undefined)
    body.config = config.value as Record<string, unknown>
  // The server subset-matches `expect.output` against the event array.
  if (expectEvents.ok && expectEvents.value !== undefined)
    body.expect = { output: expectEvents.value as CanonicalEvent[] }
  return { body, errors }
}

/** Whether a parsed test case is a streaming one. */
export function isStreamCase(c: unknown): boolean {
  if (c === null || typeof c !== 'object' || Array.isArray(c))
    return false
  const o = c as Record<string, unknown>
  return o.hook === 'parseStream' || Array.isArray(o.chunks)
}

/** A tests/*.json streaming case → form rows (JSON when a chunk is one JSON value per line). */
export function streamFormFromCase(c: PluginTestCase): StreamCaseForm {
  const raw = Array.isArray(c.chunks) ? c.chunks.filter((x): x is string => typeof x === 'string') : []
  const allJsonLines = raw.length > 0 && raw.every(s => s.endsWith('\n') && isSingleJson(s.slice(0, -1)))
  return {
    name: c.name ?? '流式用例',
    chunks: raw.map((s) => {
      if (allJsonLines)
        return chunkRow('json', s.slice(0, -1))
      return chunkRow('text', s)
    }),
    jsonNewline: true,
    config: c.config ? JSON.stringify(c.config, null, 2) : '',
    expectEvents: Array.isArray(c.expect?.output) ? JSON.stringify(c.expect.output, null, 2) : '',
  }
}

function isSingleJson(s: string): boolean {
  if (s.includes('\n'))
    return false
  try {
    JSON.parse(s)
    return true
  }
  catch {
    return false
  }
}

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

export interface StreamCallRow {
  index: number
  hook: string
  /** Input chunk index (parseStream). */
  chunk: number | null
  durationMs: number
  /** Over the 50 ms per-call limit. */
  overLimit: boolean
  events: CanonicalEvent[]
  error: string | null
}

export interface StreamResultView {
  calls: StreamCallRow[]
  events: CanonicalEvent[]
  chatChunks: unknown[]
  /** The chunks were converted in the browser (the server did not return them). */
  chatChunksLocal: boolean
  /** Conversion error (malformed events), when converted locally. */
  chatChunksError: string | null
  /** Sum of call durations (or the test's durationMs when no per-call timing is reported). */
  totalMs: number
  totalOverLimit: boolean
}

const EVENT_TYPES = new Set(['delta', 'finish', 'usage', 'error'])

function isEvent(v: unknown): v is CanonicalEvent {
  return v !== null && typeof v === 'object' && EVENT_TYPES.has(String((v as Record<string, unknown>).type))
}

function asEvents(v: unknown): CanonicalEvent[] | null {
  return Array.isArray(v) && v.every(isEvent) ? v : null
}

function field(o: unknown, key: string): unknown {
  return o !== null && typeof o === 'object' && !Array.isArray(o) ? (o as Record<string, unknown>)[key] : undefined
}

/**
 * Streaming view of a test result, or null for non-streaming results. Accepts
 * `events` / `chatChunks` / `calls` at the top level, inside `output`
 * (`output.events`, `output.chatChunks` or `output.chunks`, `output.calls`),
 * or `output` itself being the event array.
 */
export function normalizeStreamResult(res: PluginTestResult | null | undefined, opts: { streamCase?: boolean } = {}): StreamResultView | null {
  if (!res)
    return null
  const out = res.output
  const callsRaw = (res.calls ?? field(out, 'calls')) as PluginStreamCall[] | null | undefined
  const calls: StreamCallRow[] = Array.isArray(callsRaw)
    ? callsRaw.map((c, i) => ({
        index: i,
        hook: typeof c?.hook === 'string' ? c.hook : 'parseStream',
        chunk: typeof c?.chunk === 'number' ? c.chunk : null,
        durationMs: typeof c?.durationMs === 'number' ? c.durationMs : 0,
        overLimit: typeof c?.durationMs === 'number' && c.durationMs > STREAM_CALL_LIMIT_MS,
        events: asEvents(c?.events) ?? [],
        error: typeof c?.error === 'string' && c.error ? c.error : null,
      }))
    : []
  const events = asEvents(res.events) ?? asEvents(field(out, 'events')) ?? asEvents(out) ?? calls.flatMap(c => c.events)
  const chunksRaw = res.chatChunks ?? field(out, 'chatChunks') ?? field(out, 'chunks')
  const serverChunks = Array.isArray(chunksRaw) ? chunksRaw : null
  if (!calls.length && !events.length && !serverChunks?.length && !isStreamOutput(res, opts.streamCase))
    return null
  const local = serverChunks ? null : eventsToChatChunks(events)
  const totalMs = calls.length ? calls.reduce((s, c) => s + c.durationMs, 0) : res.durationMs
  return {
    calls,
    events,
    chatChunks: serverChunks ?? local?.chunks ?? [],
    chatChunksLocal: !serverChunks,
    chatChunksError: local?.error ?? null,
    totalMs,
    totalOverLimit: totalMs > STREAM_TOTAL_LIMIT_MS,
  }
}

/** An empty event list from a streaming test is still a streaming result. */
function isStreamOutput(res: PluginTestResult, streamCase?: boolean): boolean {
  return Array.isArray(res.events) || Array.isArray(res.calls) || Array.isArray(field(res.output, 'events')) || (!!streamCase && (Array.isArray(res.output) || res.output === null))
}

/**
 * Canonical events → Chat Completions chunks, mirroring the host's conversion
 * (server/internal/protocol/canonical.go CanonicalStream): the first chunk
 * carries `role`, reasoning becomes `reasoning_content`, content after
 * `finish` is ignored, a missing finish becomes "stop" at the end of the
 * stream, the usage chunk (with `total_tokens`) comes last, then [DONE]
 * (added by chatChunksText). An error event stops the conversion.
 */
export function eventsToChatChunks(events: CanonicalEvent[], model = 'model'): { chunks: Record<string, unknown>[], error: string | null } {
  const chunks: Record<string, unknown>[] = []
  const base = { id: 'chatcmpl-preview', object: 'chat.completion.chunk', created: 0, model }
  const chunk = (choices: unknown[]) => ({ ...base, choices })
  let roleSent = false
  let finished = false
  let usage: Record<string, unknown> | null = null
  const toolIdx = new Map<string, number>()
  let nextTool = 0
  let lastTool = -1
  const finish = (reason: string) => {
    finished = true
    if (!roleSent) {
      roleSent = true
      chunks.push(chunk([{ index: 0, delta: { role: 'assistant', content: '' }, finish_reason: null }]))
    }
    chunks.push(chunk([{ index: 0, delta: {}, finish_reason: reason || 'stop' }]))
  }
  for (const e of events) {
    switch (e.type) {
      case 'delta': {
        if (finished)
          continue
        const delta: Record<string, unknown> = {}
        if (!roleSent)
          delta.role = 'assistant'
        if (e.reasoning)
          delta.reasoning_content = e.reasoning
        if (typeof e.content === 'string' && (e.content !== '' || !roleSent))
          delta.content = e.content
        if (e.toolCalls?.length) {
          delta.tool_calls = e.toolCalls.map((raw) => {
            const tc = (raw ?? {}) as { index?: number, id?: string, function?: { name?: string, arguments?: string } }
            let idx: number
            if (typeof tc.index === 'number')
              idx = tc.index
            else if (tc.id)
              idx = toolIdx.get(tc.id) ?? nextTool
            else
              idx = lastTool >= 0 ? lastTool : 0
            if (tc.id)
              toolIdx.set(tc.id, idx)
            nextTool = Math.max(nextTool, idx + 1)
            lastTool = idx
            const call: Record<string, unknown> = { index: idx, function: { arguments: tc.function?.arguments ?? '' } }
            if (tc.id) {
              call.id = tc.id
              call.type = 'function'
            }
            if (tc.function?.name) {
              (call.function as Record<string, unknown>).name = tc.function.name
              call.type = 'function'
            }
            return call
          })
        }
        const keys = Object.keys(delta)
        if (keys.length === 0 || (keys.length === 1 && keys[0] === 'role'))
          continue
        roleSent = true
        chunks.push(chunk([{ index: 0, delta, finish_reason: null }]))
        break
      }
      case 'finish':
        if (!finished)
          finish(e.reason)
        break
      case 'usage': {
        const u = (e.usage ?? {}) as Record<string, unknown>
        const p = Number(u.prompt_tokens)
        const c = Number(u.completion_tokens)
        if (!Number.isFinite(p) || !Number.isFinite(c))
          return { chunks, error: 'usage 事件需要 Chat Completions 格式的 usage（prompt_tokens、completion_tokens）' }
        usage = { ...u, total_tokens: p + c }
        break
      }
      case 'error':
        return { chunks, error: `error 事件：${e.message || 'upstream stream error'}（客户端收到错误，流结束）` }
      default:
        return { chunks, error: `未知的事件类型 ${JSON.stringify((e as { type?: unknown }).type)}` }
    }
  }
  if (!finished)
    finish('stop')
  if (usage)
    chunks.push({ ...chunk([]), usage })
  return { chunks, error: null }
}

function truncate(s: string, n: number): string {
  return [...s].length > n ? `${[...s].slice(0, n).join('')}…` : s
}

/** One-line description of an event for the timeline. */
export function eventSummary(e: CanonicalEvent): string {
  switch (e.type) {
    case 'delta': {
      const parts: string[] = []
      if (e.content)
        parts.push(JSON.stringify(truncate(e.content, 40)))
      if (e.reasoning)
        parts.push(`推理 ${JSON.stringify(truncate(e.reasoning, 24))}`)
      if (e.toolCalls?.length)
        parts.push(`${e.toolCalls.length} 个工具调用`)
      return parts.join(' · ') || '（空）'
    }
    case 'finish':
      return e.reason || '—'
    case 'usage': {
      const u = e.usage ?? {}
      const pick = (...keys: string[]) => {
        for (const k of keys) {
          const v = u[k]
          if (typeof v === 'number')
            return v
        }
        return null
      }
      const input = pick('prompt_tokens', 'input_tokens', 'input', 'promptTokens', 'inputTokens')
      const output = pick('completion_tokens', 'output_tokens', 'output', 'completionTokens', 'outputTokens')
      if (input === null && output === null)
        return truncate(JSON.stringify(u), 60)
      return `输入 ${input ?? '—'} · 输出 ${output ?? '—'}`
    }
    case 'error':
      return e.message || '—'
    default:
      return truncate(JSON.stringify(e), 60)
  }
}

export const EVENT_TYPE_LABELS: Record<string, string> = {
  delta: '增量',
  finish: '结束',
  usage: '用量',
  error: '错误',
}

/** Converted Chat chunks as SSE lines ("data: {...}"), ending with "data: [DONE]". */
export function chatChunksText(chunks: unknown[]): string {
  if (!chunks.length)
    return ''
  return `${chunks.map(c => `data: ${typeof c === 'string' ? c : JSON.stringify(c)}`).join('\n\n')}\n\ndata: [DONE]`
}

/** Width (0–100) of a duration bar relative to a limit, capped at 100. */
export function limitPercent(ms: number, limit: number): number {
  if (!Number.isFinite(ms) || ms <= 0 || limit <= 0)
    return 0
  return Math.min(100, (ms / limit) * 100)
}

/** Readable preview of an input chunk ("\n" shown as ⏎). */
export function chunkPreview(s: string, n = 48): string {
  return truncate(s.replace(/\r?\n/g, '⏎'), n)
}
