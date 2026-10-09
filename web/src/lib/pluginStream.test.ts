import type { PluginTestResult } from './types'
import { describe, expect, it } from 'vitest'
import { buildStreamCase, chatChunksText, eventsToChatChunks, chunkPayload, chunkPreview, chunkRow, emptyStreamForm, eventSummary, isStreamCase, limitPercent, normalizeStreamResult, streamFormFromCase } from './pluginStream'

function result(p: Partial<PluginTestResult>): PluginTestResult {
  return { ok: true, output: null, error: null, logs: null, fetches: null, durationMs: 7, expectation: null, ...p }
}

describe('chunkPayload', () => {
  it('serializes JSON compactly with an optional newline', () => {
    expect(chunkPayload({ kind: 'json', text: '{ "a": 1 }' })).toEqual({ ok: true, value: '{"a":1}\n' })
    expect(chunkPayload({ kind: 'json', text: '{"a":1}' }, false)).toEqual({ ok: true, value: '{"a":1}' })
    expect(chunkPayload({ kind: 'json', text: '{' }).ok).toBe(false)
    expect(chunkPayload({ kind: 'json', text: ' ' })).toEqual({ ok: false, error: '内容为空' })
  })
  it('sends text as is', () => {
    expect(chunkPayload({ kind: 'text', text: '{"a":\n' })).toEqual({ ok: true, value: '{"a":\n' })
    expect(chunkPayload({ kind: 'text', text: '' }).ok).toBe(false)
  })
})

describe('buildStreamCase', () => {
  it('builds a parseStream case', () => {
    const f = emptyStreamForm()
    f.config = '{"region":"cn"}'
    f.expectEvents = '[{"type":"finish","reason":"stop"}]'
    const { body, errors } = buildStreamCase(f)
    expect(errors).toEqual({})
    expect(body).toMatchObject({ name: '流式用例', hook: 'parseStream', config: { region: 'cn' }, expect: { output: [{ type: 'finish', reason: 'stop' }] } })
    expect(body?.chunks).toHaveLength(3)
    expect(body?.chunks?.[0]).toBe('{"event":"text","text":"你好"}\n')
  })
  it('omits empty optional fields', () => {
    const { body } = buildStreamCase({ ...emptyStreamForm(), name: ' ' })
    expect(body).not.toHaveProperty('config')
    expect(body).not.toHaveProperty('expect')
    expect(body?.name).toBe('流式用例')
  })
  it('reports errors per chunk and field', () => {
    const f = emptyStreamForm()
    f.chunks = [chunkRow('json', 'nope'), chunkRow('text', 'ok')]
    f.config = '[]'
    f.expectEvents = '{}'
    const { body, errors } = buildStreamCase(f)
    expect(body).toBeNull()
    expect(Object.keys(errors).sort()).toEqual(['chunks.0', 'config', 'expectEvents'])
    expect(buildStreamCase({ ...f, chunks: [], config: '', expectEvents: '' }).errors.chunks).toMatch('至少')
  })
})

describe('stream case files', () => {
  it('detects streaming cases', () => {
    expect(isStreamCase({ hook: 'parseStream' })).toBe(true)
    expect(isStreamCase({ chunks: [] })).toBe(true)
    expect(isStreamCase({ capability: 'models.list' })).toBe(false)
    expect(isStreamCase([])).toBe(false)
  })
  it('converts JSON Lines chunks back to JSON rows, others to text', () => {
    const f = streamFormFromCase({ name: 'x', hook: 'parseStream', chunks: ['{"a":1}\n', '{"b":2}\n'], config: { k: 1 }, expect: { output: [{ type: 'finish', reason: 'stop' }] } })
    expect(f.expectEvents).toContain('"finish"')
    expect(f.chunks.map(c => [c.kind, c.text])).toEqual([['json', '{"a":1}'], ['json', '{"b":2}']])
    expect(f.config).toContain('"k": 1')
    const t = streamFormFromCase({ chunks: ['{"a":', '1}\n'] })
    expect(t.chunks.map(c => c.kind)).toEqual(['text', 'text'])
    expect(t.name).toBe('流式用例')
  })
})

describe('normalizeStreamResult', () => {
  it('returns null for non-streaming results', () => {
    expect(normalizeStreamResult(result({ output: { models: [] } }))).toBeNull()
    expect(normalizeStreamResult(null)).toBeNull()
  })
  it('reads top-level events, chatChunks and calls', () => {
    const v = normalizeStreamResult(result({
      events: [{ type: 'delta', content: 'hi' }, { type: 'finish', reason: 'stop' }],
      chatChunks: [{ id: 'c1' }],
      calls: [
        { hook: 'parseStream', chunk: 0, durationMs: 3, events: [{ type: 'delta', content: 'hi' }] },
        { hook: 'parseStream', chunk: 1, durationMs: 61, events: [] },
        { hook: 'endStream', durationMs: 1, events: [{ type: 'finish', reason: 'stop' }] },
      ],
    }))!
    expect(v.calls.map(c => [c.hook, c.chunk, c.overLimit])).toEqual([['parseStream', 0, false], ['parseStream', 1, true], ['endStream', null, false]])
    expect(v.totalMs).toBe(65)
    expect(v.totalOverLimit).toBe(false)
    expect(v.events).toHaveLength(2)
    expect(v.chatChunks).toEqual([{ id: 'c1' }])
  })
  it('reads events nested in output or as the output itself', () => {
    expect(normalizeStreamResult(result({ output: { events: [{ type: 'usage', usage: { input: 1 } }], chunks: ['x'] } }))!.chatChunks).toEqual(['x'])
    const v = normalizeStreamResult(result({ output: [{ type: 'error', message: 'boom' }], durationMs: 6000 }))!
    expect(v.events).toEqual([{ type: 'error', message: 'boom' }])
    expect(v.totalMs).toBe(6000)
    expect(v.totalOverLimit).toBe(true)
  })
  it('derives events from calls and keeps empty streaming results', () => {
    expect(normalizeStreamResult(result({ calls: [{ hook: 'parseStream', chunk: 0, durationMs: 1, events: [{ type: 'finish', reason: 'length' }] }] }))!.events).toEqual([{ type: 'finish', reason: 'length' }])
    expect(normalizeStreamResult(result({ events: [] }))).not.toBeNull()
  })
})

describe('formatting', () => {
  it('summarizes events', () => {
    expect(eventSummary({ type: 'delta', content: '你好' })).toBe('"你好"')
    expect(eventSummary({ type: 'delta', reasoning: 'r', toolCalls: [{}] })).toBe('推理 "r" · 1 个工具调用')
    expect(eventSummary({ type: 'delta' })).toBe('（空）')
    expect(eventSummary({ type: 'finish', reason: 'stop' })).toBe('stop')
    expect(eventSummary({ type: 'usage', usage: { prompt_tokens: 12, completion_tokens: 4 } })).toBe('输入 12 · 输出 4')
    expect(eventSummary({ type: 'usage', usage: { foo: 'bar' } })).toBe('{"foo":"bar"}')
    expect(eventSummary({ type: 'error', message: 'bad' })).toBe('bad')
  })
  it('renders chat chunks as SSE', () => {
    expect(chatChunksText([])).toBe('')
    expect(chatChunksText([{ a: 1 }, 'raw'])).toBe('data: {"a":1}\n\ndata: raw\n\ndata: [DONE]')
  })
  it('computes bars and previews', () => {
    expect(limitPercent(25, 50)).toBe(50)
    expect(limitPercent(80, 50)).toBe(100)
    expect(limitPercent(0, 50)).toBe(0)
    expect(chunkPreview('{"a":1}\n')).toBe('{"a":1}⏎')
    expect(chunkPreview('x'.repeat(60), 10)).toBe(`${'x'.repeat(10)}…`)
  })
})

describe('backend result shape (output = event array)', () => {
  it('treats an output array of a streaming case as events and converts chunks locally', () => {
    const v = normalizeStreamResult(result({ output: [{ type: 'delta', content: 'a' }, { type: 'usage', usage: { prompt_tokens: 1, completion_tokens: 2 } }] }), { streamCase: true })!
    expect(v.events).toHaveLength(2)
    expect(v.chatChunksLocal).toBe(true)
    expect(v.chatChunks).toHaveLength(3)
    expect(normalizeStreamResult(result({ output: [] }), { streamCase: true })).not.toBeNull()
    expect(normalizeStreamResult(result({ output: [] }))).toBeNull()
  })
  it('prefers server chunks', () => {
    const v = normalizeStreamResult(result({ events: [{ type: 'finish', reason: 'stop' }], chatChunks: ['x'] }))!
    expect(v.chatChunksLocal).toBe(false)
    expect(v.chatChunks).toEqual(['x'])
  })
})

describe('eventsToChatChunks (mirrors protocol/canonical.go)', () => {
  it('adds the role, reasoning_content, tool calls, finish and usage', () => {
    const { chunks, error } = eventsToChatChunks([
      { type: 'delta', reasoning: '想' },
      { type: 'delta', content: '好的' },
      { type: 'delta', toolCalls: [{ id: 'call_1', function: { name: 'f', arguments: '{}' } }] },
      { type: 'finish', reason: 'tool_calls' },
      { type: 'delta', content: 'ignored' },
      { type: 'usage', usage: { prompt_tokens: 12, completion_tokens: 9 } },
    ], 'm')
    expect(error).toBeNull()
    const choices = chunks.map(c => (c.choices as { delta: Record<string, unknown>, finish_reason: unknown }[])[0])
    expect(choices[0]).toEqual({ index: 0, delta: { role: 'assistant', reasoning_content: '想' }, finish_reason: null })
    expect(choices[1]!.delta).toEqual({ content: '好的' })
    expect(choices[2]!.delta).toEqual({ tool_calls: [{ index: 0, id: 'call_1', type: 'function', function: { name: 'f', arguments: '{}' } }] })
    expect(choices[3]).toEqual({ index: 0, delta: {}, finish_reason: 'tool_calls' })
    expect(chunks).toHaveLength(5)
    expect(chunks[4]).toMatchObject({ choices: [], usage: { prompt_tokens: 12, completion_tokens: 9, total_tokens: 21 }, model: 'm' })
  })
  it('finishes with stop and sends an empty role chunk when nothing was said', () => {
    const { chunks } = eventsToChatChunks([])
    expect(chunks.map(c => (c.choices as { delta: unknown, finish_reason: unknown }[])[0])).toEqual([
      { index: 0, delta: { role: 'assistant', content: '' }, finish_reason: null },
      { index: 0, delta: {}, finish_reason: 'stop' },
    ])
  })
  it('stops at errors and bad usage', () => {
    expect(eventsToChatChunks([{ type: 'delta', content: 'a' }, { type: 'error', message: 'boom' }]).error).toContain('boom')
    expect(eventsToChatChunks([{ type: 'usage', usage: { input: 1 } }]).error).toContain('prompt_tokens')
  })
})
