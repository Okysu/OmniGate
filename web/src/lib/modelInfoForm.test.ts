import type { ModelEntry, ModelInfo } from './types'
import { describe, expect, it } from 'vitest'
import {
  buildModelInfoInput,
  emptyModelInfoForm,
  formFromModelInfo,
  isModelInfoDirty,
  mergeModelInfo,
  normalizeTags,
  parseTags,
  parseTokenLimit,
  previewPlazaModel,
  validateModelInfo,
} from './modelInfoForm'

const info: ModelInfo = {
  model: 'deepseek-chat',
  displayName: 'DeepSeek V3',
  description: '通用对话',
  vendor: 'DeepSeek',
  tags: ['性价比'],
  contextWindow: 65536,
  maxOutput: 8192,
  capabilities: { vision: false, tools: true, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false },
  hidden: false,
  sortOrder: 10,
  version: 3,
  updatedAt: '2026-10-01T00:00:00Z',
}

describe('model info form', () => {
  it('round-trips an info row without becoming dirty', () => {
    const f = formFromModelInfo(info)
    expect(f.contextWindow).toBe('65536')
    expect(isModelInfoDirty(f, info)).toBe(false)
    f.tags = ['性价比', ' 性价比 ']
    expect(isModelInfoDirty(f, info)).toBe(false)
    f.hidden = true
    expect(isModelInfoDirty(f, info)).toBe(true)
  })

  it('builds the PUT body (version only when updating)', () => {
    const f = emptyModelInfoForm()
    f.displayName = '  GPT-4o  '
    f.tags = ['旗舰', 'Vision', 'vision']
    f.contextWindow = '128K'
    f.maxOutput = '16,384'
    f.capabilities.vision = true
    f.sortOrder = '-5'
    expect(buildModelInfoInput(f)).toEqual({
      displayName: 'GPT-4o',
      description: '',
      vendor: '',
      tags: ['旗舰', 'Vision'],
      contextWindow: 128000,
      maxOutput: 16384,
      capabilities: { vision: true, tools: false, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false },
      hidden: false,
      sortOrder: -5,
    })
    expect(buildModelInfoInput(formFromModelInfo(info), info.version).version).toBe(3)
  })

  it('parses token limits with K / M suffixes', () => {
    expect(parseTokenLimit('')).toBeNull()
    expect(parseTokenLimit('128k')).toBe(128000)
    expect(parseTokenLimit('1M')).toBe(1000000)
    expect(parseTokenLimit('1.5M')).toBe(1500000)
    expect(parseTokenLimit('131072')).toBe(131072)
    expect(parseTokenLimit('abc')).toBeNaN()
    expect(parseTokenLimit('1.5')).toBeNaN()
  })

  it('validates the contract limits', () => {
    const f = emptyModelInfoForm()
    expect(validateModelInfo(f)).toEqual({})
    f.displayName = 'x'.repeat(101)
    f.description = 'x'.repeat(1001)
    f.vendor = 'x'.repeat(51)
    f.contextWindow = '0'
    f.maxOutput = 'many'
    f.sortOrder = '1.5'
    expect(Object.keys(validateModelInfo(f)).sort()).toEqual(['contextWindow', 'description', 'displayName', 'maxOutput', 'sortOrder', 'vendor'])
    const g = emptyModelInfoForm()
    g.tags = Array.from({ length: 11 }, (_, i) => `t${i}`)
    expect(validateModelInfo(g).tags).toContain('10')
    g.tags = ['x'.repeat(21)]
    expect(validateModelInfo(g).tags).toContain('20')
    const h = emptyModelInfoForm()
    h.contextWindow = '8K'
    h.maxOutput = '16K'
    expect(validateModelInfo(h).maxOutput).toBeDefined()
  })

  it('normalises tags', () => {
    expect(normalizeTags([' a ', 'A', '', 'b'])).toEqual(['a', 'b'])
    expect(parseTags('长上下文，旗舰, 推理;多模态')).toEqual(['长上下文', '旗舰', '推理', '多模态'])
  })
})

describe('mergeModelInfo', () => {
  const entries: ModelEntry[] = [
    { model: 'gpt-4o', channels: 2, price: null },
    { model: 'deepseek-chat', channels: 1, price: null },
    { model: 'aaa', channels: 1, price: null },
  ]
  it('joins models with info rows, keeps orphans, orders like the plaza', () => {
    const orphan = { ...info, model: 'retired-model', sortOrder: -1 }
    const rows = mergeModelInfo(entries, [info, orphan])
    expect(rows.map(r => r.model)).toEqual(['retired-model', 'aaa', 'gpt-4o', 'deepseek-chat'])
    expect(rows[0]).toMatchObject({ channels: 0, entry: null })
    expect(rows.find(r => r.model === 'deepseek-chat')?.info).toBe(info)
    expect(rows.find(r => r.model === 'gpt-4o')?.info).toBeNull()
  })

  it('builds a plaza preview from the form and the sell price', () => {
    const entry: ModelEntry = { model: 'deepseek-chat', channels: 1, price: { id: 'p', kind: 'sell', model: 'deepseek-chat', channelId: null, inputPerM: '0.27', outputPerM: '1.1', cacheReadPerM: '0.07', cacheWritePerM: '0', perRequest: '0', effectiveAt: '', createdAt: '', createdBy: null } }
    const p = previewPlazaModel('deepseek-chat', formFromModelInfo(info), entry)
    expect(p).toMatchObject({ model: 'deepseek-chat', displayName: 'DeepSeek V3', contextWindow: 65536, plans: [], price: { inputPerM: '0.27', outputPerM: '1.1', cacheReadPerM: '0.07', cacheWritePerM: '0' } })
    const bad = formFromModelInfo(info)
    bad.contextWindow = 'oops'
    expect(previewPlazaModel('x', bad, null)).toMatchObject({ contextWindow: null, price: null })
  })
})

describe('audio capabilities (phase9 §1)', () => {
  it('default to false for older rows and are sent in the PUT body', () => {
    const old = { ...info, capabilities: { vision: false, tools: true, reasoning: false, embedding: false, imageGeneration: false } } as unknown as ModelInfo
    const f = formFromModelInfo(old)
    expect(f.capabilities).toMatchObject({ audioInput: false, audioOutput: false })
    f.capabilities.audioInput = true
    f.capabilities.audioOutput = true
    expect(isModelInfoDirty(f, old)).toBe(true)
    expect(buildModelInfoInput(f).capabilities).toMatchObject({ audioInput: true, audioOutput: true })
    expect(previewPlazaModel('whisper-1', f, null).capabilities).toMatchObject({ audioInput: true, audioOutput: true })
  })

  it('carries audio prices into the plaza preview', () => {
    const entry: ModelEntry = { model: 'tts', channels: 1, price: { id: 'p', kind: 'sell', model: 'tts', channelId: null, inputPerM: '0.6', outputPerM: '12', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0', perMCharacters: '15', audioOutputPerM: null, effectiveAt: '2026-10-01T00:00:00Z', createdAt: '2026-10-01T00:00:00Z', createdBy: null } }
    expect(previewPlazaModel('tts', emptyModelInfoForm(), entry).price).toEqual({ inputPerM: '0.6', outputPerM: '12', cacheReadPerM: '0', cacheWritePerM: '0', perMCharacters: '15' })
  })
})

describe('imageGeneration capability (phase7 §1)', () => {
  it('defaults to false for older rows and is sent in the PUT body', () => {
    const old = { ...info, capabilities: { vision: true, tools: false, reasoning: false, embedding: false } } as unknown as ModelInfo
    const f = formFromModelInfo(old)
    expect(f.capabilities.imageGeneration).toBe(false)
    f.capabilities.imageGeneration = true
    expect(isModelInfoDirty(f, old)).toBe(true)
    expect(buildModelInfoInput(f).capabilities).toEqual({ vision: true, tools: false, reasoning: false, embedding: false, imageGeneration: true, audioInput: false, audioOutput: false })
    expect(previewPlazaModel('gpt-image-1', f, null).capabilities.imageGeneration).toBe(true)
  })

  it('carries image prices into the plaza preview', () => {
    const entry: ModelEntry = { model: 'gpt-image-1', channels: 1, price: { id: 'p', kind: 'sell', model: 'gpt-image-1', channelId: null, inputPerM: '5', outputPerM: '40', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0', perImage: '0.04', imageInputPerM: null, effectiveAt: '2026-10-01T00:00:00Z', createdAt: '2026-10-01T00:00:00Z', createdBy: null } }
    expect(previewPlazaModel('gpt-image-1', emptyModelInfoForm(), entry).price).toEqual({ inputPerM: '5', outputPerM: '40', cacheReadPerM: '0', cacheWritePerM: '0', perImage: '0.04' })
  })
})
