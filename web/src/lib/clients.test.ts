import type { ClientInfo } from './types'
import { describe, expect, it } from 'vitest'
import {
  CACHE_RATE_MIN_PROMPT,
  cacheRateTone,
  clientIncludeLabel,
  clientKind,
  clientLabel,
  clientName,
  clientOptions,
  clientTitle,
  groupClients,
} from './clients'

/** A slice of GET /api/clients (the backend's clientdetect.Known order). */
const LIST: ClientInfo[] = [
  { id: 'claude-code', name: 'Claude Code', kind: 'agent' },
  { id: 'codex', name: 'Codex', kind: 'agent' },
  { id: 'cherry-studio', name: 'Cherry Studio', kind: 'chat' },
  { id: 'openai-sdk-python', name: 'OpenAI SDK (Python)', kind: 'sdk' },
  { id: 'curl', name: 'curl', kind: 'tool' },
  { id: 'unknown', name: '未知', kind: 'unknown' },
]

describe('client display helpers', () => {
  it('labels clients with their version', () => {
    expect(clientLabel({ name: 'Claude Code', version: '2.0.14' })).toBe('Claude Code 2.0.14')
    expect(clientLabel({ name: 'curl', version: null })).toBe('curl')
    expect(clientTitle({ id: 'codex', name: 'Codex', version: '0.46.0' }, 'agent')).toBe('Codex · 版本 0.46.0 · 编码代理（codex）')
    expect(clientTitle({ id: 'unknown', name: '未知', version: null })).toBe('未知 · 版本未知（unknown）')
  })

  it('looks up kinds and names in the known list', () => {
    expect(clientKind(LIST, 'cherry-studio')).toBe('chat')
    expect(clientKind(LIST, 'gone-client')).toBe('unknown')
    expect(clientKind(LIST, null)).toBe('unknown')
    expect(clientKind([{ id: 'x', name: 'X', kind: 'robot' as never }], 'x')).toBe('unknown')
    expect(clientName(LIST, 'codex')).toBe('Codex')
    expect(clientName(LIST, 'gone-client')).toBe('gone-client')
    expect(clientIncludeLabel(['claude-code', 'gone-client'], LIST)).toBe('Claude Code、gone-client')
    expect(clientIncludeLabel([], LIST)).toBe('')
  })

  it('groups clients by kind in kind order and builds select options', () => {
    expect(groupClients(LIST).map(g => [g.label, g.items.map(c => c.id)])).toEqual([
      ['编码代理', ['claude-code', 'codex']],
      ['聊天客户端', ['cherry-studio']],
      ['SDK', ['openai-sdk-python']],
      ['HTTP 工具', ['curl']],
      ['未识别', ['unknown']],
    ])
    expect(groupClients([])).toEqual([])
    expect(clientOptions(LIST)[0]).toEqual({ value: 'claude-code', label: 'Claude Code', hint: '编码代理 · claude-code' })
  })

  it('flags low cache hit rates only with enough traffic', () => {
    const big = CACHE_RATE_MIN_PROMPT
    expect(cacheRateTone(0.1, big)).toBe('low')
    expect(cacheRateTone(0.29, big)).toBe('low')
    expect(cacheRateTone(0.3, big)).toBe('warn')
    expect(cacheRateTone(0.59, big)).toBe('warn')
    expect(cacheRateTone(0.6, big)).toBe('ok')
    expect(cacheRateTone(0.05, big - 1)).toBe('none')
    expect(cacheRateTone(null, big)).toBe('none')
    expect(cacheRateTone(undefined, big)).toBe('none')
  })
})
