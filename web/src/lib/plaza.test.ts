import type { MyPlazaModel, PlazaModel } from './types'
import { describe, expect, it } from 'vitest'
import {
  audioModes,
  CAPABILITIES,
  EMPTY_FILTERS,
  estimateCallCost,
  estimateCost,
  filterPlaza,
  formatTokenCount,
  freeBillingLabel,
  hasActiveFilters,
  hasAudioPrice,
  hasAudioUnitPrice,
  isCovered,
  isImageModel,
  matchesQuery,
  NO_CAPABILITIES,
  normalizeMyPlazaModel,
  normalizePlazaModel,
  parseTokenInput,
  plazaCurrency,
  plazaPriceFromSell,
  PROTOCOL_LABELS,
  protocolsForCapabilities,
  protocolsOf,
  sortPlaza,
  sortProtocols,
  sourceEntries,
  vendorsOf,
} from './plaza'

function model(over: Partial<PlazaModel> & { model: string }): PlazaModel {
  return normalizePlazaModel({ displayName: '', description: '', vendor: '', tags: [], protocols: ['openai.chat'], price: null, plans: [], ...over })
}

const ds = model({ model: 'deepseek-chat', displayName: 'DeepSeek V3', vendor: 'DeepSeek', tags: ['性价比'], contextWindow: 65536, capabilities: { vision: false, tools: true, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false, completions: false }, price: { inputPerM: '0.27', outputPerM: '1.1', cacheReadPerM: '0.07', cacheWritePerM: null } })
const gpt = model({ model: 'gpt-4o', vendor: 'OpenAI', contextWindow: 128000, capabilities: { vision: true, tools: true, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false, completions: false }, protocols: ['anthropic.messages', 'openai.chat', 'openai.responses'], price: { inputPerM: '2.5', outputPerM: '10', cacheReadPerM: '1.25', cacheWritePerM: null }, plans: [{ id: 'p1', name: 'Pro' }] })
const free = model({ model: 'qwen-free', vendor: 'Alibaba', contextWindow: null })
const emb = model({ model: 'text-embedding-3-small', vendor: 'OpenAI', capabilities: { vision: false, tools: false, reasoning: false, embedding: true, imageGeneration: false, audioInput: false, audioOutput: false, completions: false }, protocols: ['openai.embeddings'], price: { inputPerM: '0.02', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null } })
const all = [ds, gpt, free, emb]

describe('normalizePlazaModel', () => {
  it('fills nulls from the backend with safe defaults', () => {
    const m = normalizePlazaModel({ model: 'x', displayName: null, tags: null, protocols: null, plans: null, capabilities: null, contextWindow: 0, price: null } as never)
    expect(m).toEqual({ model: 'x', displayName: '', description: '', vendor: '', tags: [], contextWindow: null, maxOutput: null, capabilities: { vision: false, tools: false, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false, completions: false }, protocols: [], price: null, plans: [] })
  })

  it('keeps cache prices nullable and drops malformed prices', () => {
    expect(normalizePlazaModel({ model: 'a', price: { inputPerM: '1', outputPerM: '2', cacheReadPerM: null } } as never).price).toEqual({ inputPerM: '1', outputPerM: '2', cacheReadPerM: null, cacheWritePerM: null })
    expect(normalizePlazaModel({ model: 'a', price: { inputPerM: 1 } } as never).price).toBeNull()
  })

  it('derives billing for "mine" when missing and keeps the subscription', () => {
    const m = normalizeMyPlazaModel({ model: 'a', sources: { own: 1, shared: 0, platform: 2 }, subscription: { id: 's1', planName: 'Pro' } } as never)
    expect(m.billing).toBe('free')
    expect(m.subscription).toEqual({ id: 's1', planName: 'Pro' })
    expect(normalizeMyPlazaModel({ model: 'b', sources: null } as never)).toMatchObject({ sources: { own: 0, shared: 0, platform: 0 }, billing: 'platform', subscription: null })
    expect(normalizeMyPlazaModel({ model: 'c', billing: 'platform', sources: { own: 0, shared: 0, platform: 1 } } as never).billing).toBe('platform')
  })
})

describe('formatTokenCount', () => {
  it.each([
    [128000, '128K'],
    [131072, '128K'],
    [200000, '200K'],
    [65536, '64K'],
    [8192, '8K'],
    [1000000, '1M'],
    [1048576, '1M'],
    [1500000, '1.5M'],
    [32000, '32K'],
    [500, '500'],
    [null, '—'],
    [0, '—'],
  ])('%s → %s', (n, s) => {
    expect(formatTokenCount(n)).toBe(s)
  })
})

describe('search & filters', () => {
  it('matches name, display name, vendor and tags, every term', () => {
    expect(matchesQuery(ds, 'deepseek')).toBe(true)
    expect(matchesQuery(ds, 'V3')).toBe(true)
    expect(matchesQuery(ds, '性价比')).toBe(true)
    expect(matchesQuery(ds, 'deepseek 性价比')).toBe(true)
    expect(matchesQuery(ds, 'deepseek vision')).toBe(false)
    expect(matchesQuery(ds, '   ')).toBe(true)
  })

  it('filters by vendor, capabilities (all required), protocol and plan coverage', () => {
    const f = { ...EMPTY_FILTERS, capabilities: [] }
    expect(filterPlaza(all, { ...f, vendor: 'OpenAI' }).map(m => m.model)).toEqual(['gpt-4o', 'text-embedding-3-small'])
    expect(filterPlaza(all, { ...f, capabilities: ['tools'] }).map(m => m.model)).toEqual(['deepseek-chat', 'gpt-4o'])
    expect(filterPlaza(all, { ...f, capabilities: ['tools', 'vision'] }).map(m => m.model)).toEqual(['gpt-4o'])
    expect(filterPlaza(all, { ...f, protocol: 'openai.embeddings' }).map(m => m.model)).toEqual(['text-embedding-3-small'])
    expect(filterPlaza(all, { ...f, coveredOnly: true }).map(m => m.model)).toEqual(['gpt-4o'])
    expect(hasActiveFilters(f)).toBe(false)
    expect(hasActiveFilters({ ...f, coveredOnly: true })).toBe(true)
  })

  it('filters "mine" by source tier and counts a subscription as coverage', () => {
    const own = normalizeMyPlazaModel({ ...ds, sources: { own: 2, shared: 0, platform: 1 }, billing: 'free', subscription: null })
    const plat = normalizeMyPlazaModel({ ...free, sources: { own: 0, shared: 0, platform: 1 }, billing: 'platform', subscription: { id: 's', planName: 'Max' } })
    const f = { ...EMPTY_FILTERS, capabilities: [] }
    expect(filterPlaza([own, plat], { ...f, source: 'own' })).toEqual([own])
    expect(filterPlaza([own, plat], { ...f, source: 'platform' })).toEqual([own, plat])
    expect(filterPlaza([own, plat], { ...f, source: 'shared' })).toEqual([])
    expect(isCovered(plat)).toBe(true)
    expect(isCovered(own)).toBe(false)
  })

  it('lists vendors and protocols', () => {
    expect(vendorsOf(all)).toEqual(['Alibaba', 'DeepSeek', 'OpenAI'])
    expect(protocolsOf(all)).toEqual(['openai.chat', 'openai.responses', 'anthropic.messages', 'openai.embeddings'])
    expect(sortProtocols(['x.custom', 'anthropic.messages', 'openai.chat'])).toEqual(['openai.chat', 'anthropic.messages', 'x.custom'])
  })
})

describe('sortPlaza', () => {
  it('keeps the server order by default', () => {
    expect(sortPlaza(all, 'default')).toEqual(all)
  })

  it('sorts by input + output price ascending (exact decimals), unpriced last', () => {
    expect(sortPlaza(all, 'price').map(m => m.model)).toEqual(['text-embedding-3-small', 'deepseek-chat', 'gpt-4o', 'qwen-free'])
    const a = model({ model: 'a', price: { inputPerM: '0.000000001', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null } })
    const b = model({ model: 'b', price: { inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null } })
    expect(sortPlaza([a, b], 'price').map(m => m.model)).toEqual(['b', 'a'])
  })

  it('sorts by context window descending, unknown last, stable', () => {
    expect(sortPlaza(all, 'context').map(m => m.model)).toEqual(['gpt-4o', 'deepseek-chat', 'qwen-free', 'text-embedding-3-small'])
  })
})

describe('price calculator', () => {
  it('parses token inputs', () => {
    expect(parseTokenInput('12,000')).toBe(12000)
    expect(parseTokenInput(' 5000 ')).toBe(5000)
    expect(parseTokenInput('')).toBe(0)
    expect(parseTokenInput('-1')).toBeNull()
    expect(parseTokenInput('1.5')).toBeNull()
  })

  it('computes exact costs per 1M tokens', () => {
    expect(estimateCost(ds.price, 1_000_000, 1_000_000)).toBe('1.37')
    expect(estimateCost(ds.price, 100_000, 20_000)).toBe('0.049')
    expect(estimateCost(gpt.price, 1, 0)).toBe('0.0000025')
    // 0.27 * 1 / 1e6 = 0.00000027 (exact within 9 decimals)
    expect(estimateCost(ds.price, 1, 0)).toBe('0.00000027')
    expect(estimateCost(null, 1, 1)).toBeNull()
    expect(estimateCost(ds.price, null, 1)).toBeNull()
  })

  it('rounds sub-nano results half up', () => {
    const p = { inputPerM: '0.000000001', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null }
    expect(estimateCost(p, 400_000, 0)).toBe('0')
    expect(estimateCost(p, 500_000, 0)).toBe('0.000000001')
  })
})

describe('currency & mine helpers', () => {
  const usd = { code: 'USD', symbol: '$', decimals: 2 }
  it('accepts an object or a bare code and falls back to the system currency', () => {
    expect(plazaCurrency(null, usd)).toEqual(usd)
    expect(plazaCurrency('USD', usd)).toEqual(usd)
    expect(plazaCurrency({ code: 'USD', decimals: 4 }, usd)).toEqual({ code: 'USD', symbol: '$', decimals: 4 })
    expect(plazaCurrency('CNY', usd)).toEqual({ code: 'CNY', symbol: 'CNY ', decimals: 2 })
    expect(plazaCurrency({ code: 'CNY', symbol: '¥', decimals: 2 }, null)).toEqual({ code: 'CNY', symbol: '¥', decimals: 2 })
    expect(plazaCurrency(undefined, null)).toBeNull()
  })

  it('lists non-zero sources in routing order and labels free billing', () => {
    const m: MyPlazaModel = normalizeMyPlazaModel({ model: 'x', sources: { own: 0, shared: 2, platform: 3 }, billing: 'free' } as never)
    expect(sourceEntries(m)).toEqual([{ tier: 'shared', label: '共享', count: 2 }, { tier: 'platform', label: '平台', count: 3 }])
    expect(freeBillingLabel(m)).toBe('优先共享渠道 · 不计费')
    expect(freeBillingLabel({ ...m, sources: { own: 1, shared: 2, platform: 3 } })).toBe('优先自有渠道 · 不计费')
  })
})

describe('image models (phase7 §1)', () => {
  const img = model({ model: 'gpt-image-1', vendor: 'OpenAI', capabilities: { vision: false, tools: false, reasoning: false, embedding: false, imageGeneration: true, audioInput: false, audioOutput: false, completions: false }, protocols: ['openai.images'], price: { inputPerM: '5', outputPerM: '40', cacheReadPerM: null, cacheWritePerM: null, perImage: '0.04', imageInputPerM: '10' } })

  it('normalises the image capability and keeps image prices only when sent', () => {
    expect(img.capabilities.imageGeneration).toBe(true)
    expect(img.price).toEqual({ inputPerM: '5', outputPerM: '40', cacheReadPerM: null, cacheWritePerM: null, perImage: '0.04', imageInputPerM: '10' })
    const p = normalizePlazaModel({ model: 'a', price: { inputPerM: '1', outputPerM: '2', perImage: null, imageInputPerM: 3 } } as never).price
    expect(p).toEqual({ inputPerM: '1', outputPerM: '2', cacheReadPerM: null, cacheWritePerM: null })
  })

  it('filters by the imageGeneration capability and the openai.images protocol', () => {
    const f = { ...EMPTY_FILTERS, capabilities: [] }
    expect(filterPlaza([...all, img], { ...f, capabilities: ['imageGeneration'] }).map(m => m.model)).toEqual(['gpt-image-1'])
    expect(filterPlaza([...all, img], { ...f, protocol: 'openai.images' }).map(m => m.model)).toEqual(['gpt-image-1'])
    expect(protocolsOf([img, ...all]).at(-1)).toBe('openai.images')
  })

  it('detects image models by protocol or capability', () => {
    expect(isImageModel(img)).toBe(true)
    expect(isImageModel({ protocols: ['openai.images'], capabilities: { ...NO_CAPABILITIES } })).toBe(true)
    expect(isImageModel(gpt)).toBe(false)
  })

  it('builds the preview price from a sell price with image fields', () => {
    expect(plazaPriceFromSell({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0', perImage: '0.02', imageInputPerM: null }))
      .toEqual({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0', perImage: '0.02' })
    expect(plazaPriceFromSell({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0' }))
      .toEqual({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0' })
  })
})

describe('audio models (phase9 §1)', () => {
  const caps = (over: Partial<PlazaModel['capabilities']>) => ({ ...NO_CAPABILITIES, ...over })
  const stt = model({ model: 'whisper-1', vendor: 'OpenAI', capabilities: caps({ audioInput: true }), protocols: ['openai.chat', 'openai.audio'], price: { inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perMinute: '0.006' } })
  const tts = model({ model: 'gpt-4o-mini-tts', vendor: 'OpenAI', capabilities: caps({ audioOutput: true, completions: false }), protocols: ['openai.audio', 'openai.chat'], price: { inputPerM: '0.6', outputPerM: '12', cacheReadPerM: null, cacheWritePerM: null, audioOutputPerM: '12', perMCharacters: '15' } })

  it('defaults the audio capabilities to false for older backends', () => {
    const m = normalizePlazaModel({ model: 'a', capabilities: { vision: true } } as never)
    expect(m.capabilities).toMatchObject({ vision: true, audioInput: false, audioOutput: false, completions: false })
    expect(normalizePlazaModel({ model: 'b', capabilities: { audioInput: true, audioOutput: 'yes' } } as never).capabilities).toMatchObject({ audioInput: true, audioOutput: false, completions: false })
  })

  it('keeps audio prices only when sent as strings', () => {
    expect(stt.price).toEqual({ inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perMinute: '0.006' })
    expect(tts.price?.perMCharacters).toBe('15')
    const p = normalizePlazaModel({ model: 'a', price: { inputPerM: '1', outputPerM: '2', audioInputPerM: null, perMinute: 3 } } as never).price
    expect(p).toEqual({ inputPerM: '1', outputPerM: '2', cacheReadPerM: null, cacheWritePerM: null })
    expect(hasAudioPrice(stt.price)).toBe(true)
    expect(hasAudioPrice(gpt.price)).toBe(false)
    expect(hasAudioPrice(null)).toBe(false)
    // per-minute / per-character prices of 0 mean "not charged"
    expect(hasAudioPrice({ perMinute: '0', perMCharacters: '0.000' })).toBe(false)
    expect(hasAudioUnitPrice({ perMCharacters: '15' })).toBe(true)
    expect(hasAudioUnitPrice({ perMinute: '0' })).toBe(false)
    expect(hasAudioPrice({ audioInputPerM: '0' })).toBe(true)
  })

  it('offers 语音识别 / 语音合成 filters and the openai.audio protocol last', () => {
    expect(CAPABILITIES.find(c => c.key === 'audioInput')?.label).toBe('语音识别')
    expect(CAPABILITIES.find(c => c.key === 'audioOutput')?.label).toBe('语音合成')
    const f = { ...EMPTY_FILTERS, capabilities: [] }
    expect(filterPlaza([...all, stt, tts], { ...f, capabilities: ['audioInput'] }).map(m => m.model)).toEqual(['whisper-1'])
    expect(filterPlaza([...all, stt, tts], { ...f, capabilities: ['audioOutput'] }).map(m => m.model)).toEqual(['gpt-4o-mini-tts'])
    expect(filterPlaza([...all, stt, tts], { ...f, protocol: 'openai.audio' }).map(m => m.model)).toEqual(['whisper-1', 'gpt-4o-mini-tts'])
    expect(sortProtocols(['openai.audio', 'openai.images', 'openai.chat'])).toEqual(['openai.chat', 'openai.images', 'openai.audio'])
    expect(PROTOCOL_LABELS['openai.audio']).toBe('Audio')
  })

  it('picks the audio endpoints from the capabilities', () => {
    expect(audioModes(stt)).toEqual({ transcription: true, speech: false })
    expect(audioModes(tts)).toEqual({ transcription: false, speech: true })
    // protocol without either capability: both; reported protocols without openai.audio: none
    expect(audioModes({ protocols: ['openai.audio'], capabilities: caps({}) })).toEqual({ transcription: true, speech: true })
    expect(audioModes({ protocols: ['anthropic.messages'], capabilities: caps({ audioInput: true }) })).toEqual({ transcription: false, speech: false })
    expect(audioModes({ protocols: [], capabilities: caps({ audioOutput: true, completions: false }) })).toEqual({ transcription: false, speech: true })
    expect(audioModes(gpt)).toEqual({ transcription: false, speech: false })
  })

  it('builds the preview price from a sell price with audio fields', () => {
    expect(plazaPriceFromSell({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0', audioInputPerM: '4', audioOutputPerM: null, perMinute: '0.006', perMCharacters: null }))
      .toEqual({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0', audioInputPerM: '4', perMinute: '0.006' })
  })
})

describe('phase8 plaza fields', () => {
  it('keeps the schedule, its time zone and the current multiplier (dropping malformed periods)', () => {
    const m = normalizePlazaModel({
      model: 'deepseek-chat',
      price: {
        inputPerM: '2',
        outputPerM: '8',
        cacheReadPerM: null,
        cacheWritePerM: null,
        schedule: [{ days: [], start: '00:30', end: '08:30', multiplier: '0.5' }, { start: 1 } as never, { days: [9, 1], start: '10:00', end: '11:00', multiplier: '2' }],
        scheduleTimezone: 'Asia/Shanghai',
        currentMultiplier: '0.5',
      },
    } as never)
    expect(m.price?.schedule).toEqual([{ days: [], start: '00:30', end: '08:30', multiplier: '0.5' }, { days: [1], start: '10:00', end: '11:00', multiplier: '2' }])
    expect(m.price?.scheduleTimezone).toBe('Asia/Shanghai')
    expect(m.price?.currentMultiplier).toBe('0.5')
    expect(normalizePlazaModel({ model: 'x', price: { inputPerM: '1', outputPerM: '1', schedule: null } } as never).price).not.toHaveProperty('schedule')
  })

  it('keeps basePrice and the group multiplier of "我的模型"', () => {
    const m = normalizeMyPlazaModel({
      model: 'gpt-4o',
      sources: { own: 0, shared: 0, platform: 1 },
      price: { inputPerM: '0.8', outputPerM: '3.2', cacheReadPerM: null, cacheWritePerM: null },
      basePrice: { inputPerM: '1', outputPerM: '4', cacheReadPerM: null, cacheWritePerM: null },
      priceMultiplier: '0.8',
    } as never)
    expect(m.basePrice?.inputPerM).toBe('1')
    expect(m.priceMultiplier).toBe('0.8')
    expect(normalizeMyPlazaModel({ model: 'y', sources: null } as never)).not.toHaveProperty('basePrice')
  })
})

describe('per-call prices', () => {
  const call = model({ model: 'flux-schnell', protocols: ['openai.images'], price: { inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perRequest: '0.04' } })

  it('keeps perRequest from the plaza response', () => {
    expect(call.price?.perRequest).toBe('0.04')
    expect(normalizePlazaModel({ model: 'x', price: { inputPerM: '1', outputPerM: '2', perRequest: null } } as never).price).not.toHaveProperty('perRequest')
  })

  it('copies a charged per-request fee into the admin preview price', () => {
    expect(plazaPriceFromSell({ inputPerM: '0', outputPerM: '0', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0.04' })?.perRequest).toBe('0.04')
    expect(plazaPriceFromSell({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0' })).not.toHaveProperty('perRequest')
  })

  it('sorts prices not billed by tokens after token prices (never as $0)', () => {
    expect(sortPlaza([call, ds, free], 'price').map(m => m.model)).toEqual(['deepseek-chat', 'flux-schnell', 'qwen-free'])
  })

  it('estimates per-call and per-image costs exactly', () => {
    expect(estimateCallCost(call.price, 100)).toBe('4')
    expect(estimateCallCost({ inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perImage: '0.02', perRequest: '0.001' }, 10, 4)).toBe('0.81')
    expect(estimateCallCost({ inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perImage: '0.02' }, 3, 1)).toBe('0.06')
    expect(estimateCallCost(null, 1)).toBeNull()
    expect(estimateCallCost(call.price, null)).toBeNull()
    expect(estimateCallCost(call.price, 1, -1)).toBeNull()
  })
})

describe('protocolsForCapabilities', () => {
  const none = { vision: false, tools: false, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false, completions: false }
  it('lists only the image endpoint for a pure image model', () => {
    expect(protocolsForCapabilities({ ...none, imageGeneration: true })).toEqual(['openai.images'])
  })
  it('lists only embeddings for an embedding model', () => {
    expect(protocolsForCapabilities({ ...none, embedding: true })).toEqual(['openai.embeddings'])
  })
  it('gives chat models the three chat protocols and no embeddings by default', () => {
    expect(protocolsForCapabilities(none)).toEqual(['openai.chat', 'openai.responses', 'anthropic.messages'])
    expect(protocolsForCapabilities({ ...none, tools: true, vision: true })).toEqual(['openai.chat', 'openai.responses', 'anthropic.messages'])
  })
  it('needs an OpenAI-compatible channel for the special endpoints', () => {
    expect(protocolsForCapabilities({ ...none, imageGeneration: true }, false)).toEqual([])
  })
  it('adds Completions to marked models served by a completions channel (phase14), keeping Chat', () => {
    expect(protocolsForCapabilities({ ...none, completions: true })).toEqual(['openai.chat', 'openai.responses', 'anthropic.messages', 'openai.completions'])
    expect(protocolsForCapabilities({ ...none, completions: true, tools: true }, true, false)).toEqual(['openai.chat', 'openai.responses', 'anthropic.messages'])
    expect(protocolsForCapabilities({ ...none, completions: true }, false)).toEqual(['openai.chat', 'openai.responses', 'anthropic.messages'])
    expect(protocolsForCapabilities({ ...none, embedding: true, completions: true })).toEqual(['openai.embeddings', 'openai.completions'])
  })
  it('labels and orders the Completions protocol and capability', () => {
    expect(PROTOCOL_LABELS['openai.completions']).toBe('Completions')
    expect(sortProtocols(['openai.embeddings', 'openai.completions', 'openai.chat'])).toEqual(['openai.chat', 'openai.completions', 'openai.embeddings'])
    expect(CAPABILITIES.find(c => c.key === 'completions')?.label).toBe('文本补全 / FIM')
    expect(normalizePlazaModel({ model: 'a', capabilities: { completions: true } } as never).capabilities.completions).toBe(true)
    expect(normalizePlazaModel({ model: 'b', capabilities: { vision: true } } as never).capabilities.completions).toBe(false)
  })
})
