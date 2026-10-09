import type { PlazaPrice, PriceTier } from './types'
import { describe, expect, it } from 'vitest'
import { formatMoney } from './money'
import { normalizePlazaModel, plazaPriceFromSell } from './plaza'
import {
  buildTiers,
  emptyTierRow,
  formatTokenThreshold,
  longContextPreset,
  normalizePlazaTiers,
  normalizeTierErrorKeys,
  parseTokenThreshold,
  pickTier,
  priceForPrompt,
  resolveTiers,
  tierLabel,
  tierPreview,
  tierRowErrors,
  tierRowsFrom,
  tiersAllowed,
  tierTable,
  validateTiers,
} from './priceTiers'

const usd = (v: string) => formatMoney(v, { code: 'USD', symbol: '$', decimals: 2 })
const row = (threshold: string, inputPerM: string, outputPerM: string, extra: Partial<ReturnType<typeof emptyTierRow>> = {}) => ({ ...emptyTierRow(threshold), inputPerM, outputPerM, ...extra })

describe('parseTokenThreshold / formatTokenThreshold', () => {
  it('parses K / M suffixes and plain counts', () => {
    expect(parseTokenThreshold('272K')).toBe(272_000)
    expect(parseTokenThreshold('272k')).toBe(272_000)
    expect(parseTokenThreshold(' 272 K ')).toBe(272_000)
    expect(parseTokenThreshold('1M')).toBe(1_000_000)
    expect(parseTokenThreshold('1.5M')).toBe(1_500_000)
    expect(parseTokenThreshold('1.5m')).toBe(1_500_000)
    expect(parseTokenThreshold('272.5K')).toBe(272_500)
    expect(parseTokenThreshold('272000')).toBe(272_000)
    expect(parseTokenThreshold('272,000')).toBe(272_000)
    expect(parseTokenThreshold(128000)).toBe(128_000)
    expect(parseTokenThreshold('1000M')).toBe(1_000_000_000)
  })
  it('rejects fractions of a token, zero, negatives and out-of-range values', () => {
    for (const v of ['', '0', '0K', '-1', '1.2345K', '1.5', 'abc', '272KB', '1001M', '1e6', null, undefined])
      expect(parseTokenThreshold(v as string), String(v)).toBeNull()
  })
  it('formats compactly', () => {
    expect(formatTokenThreshold(272_000)).toBe('272K')
    expect(formatTokenThreshold(1_000_000)).toBe('1M')
    expect(formatTokenThreshold(1_500_000)).toBe('1.5M')
    expect(formatTokenThreshold(272_500)).toBe('272.5K')
    expect(formatTokenThreshold(128_000)).toBe('128K')
    expect(formatTokenThreshold(1234)).toBe('1,234')
    expect(formatTokenThreshold(999)).toBe('999')
    expect(tierLabel(272_000)).toBe('长上下文档 >272K')
  })
  it('round-trips', () => {
    for (const n of [1, 999, 1000, 1234, 32_768, 128_000, 200_000, 272_000, 1_000_000, 1_048_576, 2_000_000])
      expect(parseTokenThreshold(formatTokenThreshold(n))).toBe(n)
  })
})

describe('tier rows', () => {
  const stored: PriceTier[] = [
    { aboveInputTokens: 272_000, inputPerM: '20', outputPerM: '75', cacheReadPerM: '2', cacheWritePerM: null, imageInputPerM: null, audioInputPerM: null, audioOutputPerM: null },
  ]
  it('prefills from a stored version (null = blank = inherit)', () => {
    expect(tierRowsFrom({ tiers: stored })).toEqual([row('272K', '20', '75', { cacheReadPerM: '2' })])
    expect(tierRowsFrom({ tiers: null })).toEqual([])
    expect(tierRowsFrom(null)).toEqual([])
  })
  it('builds the body per mode (blank optional prices omitted)', () => {
    const rows = [row('272K', '20', '75', { cacheReadPerM: ' 2 ', imageInputPerM: '30' })]
    expect(buildTiers(rows, 'token')).toEqual([{ aboveInputTokens: 272_000, inputPerM: '20', outputPerM: '75', cacheReadPerM: '2' }])
    expect(buildTiers(rows, 'custom')).toEqual([{ aboveInputTokens: 272_000, inputPerM: '20', outputPerM: '75', cacheReadPerM: '2', imageInputPerM: '30' }])
    expect(buildTiers(rows, 'request')).toEqual([])
    expect(tiersAllowed('token')).toBe(true)
    expect(tiersAllowed('custom')).toBe(true)
    expect(tiersAllowed('image')).toBe(false)
  })
})

describe('validateTiers', () => {
  it('accepts valid ascending tiers', () => {
    expect(validateTiers([row('128K', '3', '15'), row('272K', '6', '22.5', { cacheReadPerM: '0.6' })], 'token')).toEqual({})
    expect(validateTiers([], 'token')).toEqual({})
  })
  it('requires a threshold and input / output prices', () => {
    const e = validateTiers([row('', '', '')], 'token')
    expect(e['tiers.0.aboveInputTokens']).toMatch(/请填写阈值/)
    expect(e['tiers.0.inputPerM']).toBe('必填')
    expect(e['tiers.0.outputPerM']).toBe('必填')
  })
  it('rejects malformed thresholds and amounts', () => {
    const e = validateTiers([row('1.2345K', '-1', '1.0000000001', { cacheWritePerM: 'x' })], 'token')
    expect(Object.keys(e).sort()).toEqual(['tiers.0.aboveInputTokens', 'tiers.0.cacheWritePerM', 'tiers.0.inputPerM', 'tiers.0.outputPerM'])
  })
  it('requires strictly ascending thresholds', () => {
    const e = validateTiers([row('272K', '1', '1'), row('272000', '2', '2'), row('128K', '3', '3')], 'token')
    expect(e['tiers.1.aboveInputTokens']).toBe('须大于上一档（>272K）')
    expect(e['tiers.2.aboveInputTokens']).toBe('须大于上一档（>272K）')
    expect(e['tiers.0.aboveInputTokens']).toBeUndefined()
  })
  it('limits the count to 5 and ignores media prices in token mode', () => {
    const rows = ['1K', '2K', '3K', '4K', '5K', '6K'].map(t => row(t, '1', '1', { imageInputPerM: 'bad' }))
    expect(validateTiers(rows, 'token')).toEqual({ tiers: '最多 5 档' })
    expect(validateTiers(rows.slice(0, 1), 'custom')['tiers.0.imageInputPerM']).toBeDefined()
    expect(validateTiers(rows, 'audio')).toEqual({})
  })
  it('normalizes server keys and splits them per row', () => {
    const e = normalizeTierErrorKeys({ 'tiers[1].inputPerM': 'bad', 'tiers': 'too many', 'model': 'x' })
    expect(e).toEqual({ 'tiers.1.inputPerM': 'bad', 'tiers': 'too many', 'model': 'x' })
    expect(tierRowErrors(e, 1)).toEqual({ inputPerM: 'bad' })
    expect(tierRowErrors(e, 0)).toEqual({})
  })
})

describe('longContextPreset', () => {
  it('doubles input / cache prices and multiplies output by 1.5 (gpt-6-astra)', () => {
    expect(longContextPreset({ inputPerM: '10', outputPerM: '50', cacheReadPerM: '1', cacheWritePerM: '12.5' }))
      .toEqual(row('272K', '20', '75', { cacheReadPerM: '2', cacheWritePerM: '25' }))
  })
  it('matches the gpt-6.1-sol / gpt-5.5 rows and keeps unset prices blank', () => {
    expect(longContextPreset({ inputPerM: '2', outputPerM: '10', cacheReadPerM: '0.10', cacheWritePerM: '2.5' }))
      .toEqual(row('272K', '4', '15', { cacheReadPerM: '0.2', cacheWritePerM: '5' }))
    expect(longContextPreset({ inputPerM: '5', outputPerM: '30', cacheReadPerM: '0.50', cacheWritePerM: '' }))
      .toEqual(row('272K', '10', '45', { cacheReadPerM: '1' }))
    expect(longContextPreset({ inputPerM: '30', outputPerM: '180', cacheReadPerM: '0', cacheWritePerM: '' }))
      .toEqual(row('272K', '60', '270'))
  })
  it('rounds exactly at 9 decimals and scales media prices in 自定义组合', () => {
    const r = longContextPreset({ inputPerM: '0.000000001', outputPerM: '0.000000001', imageInputPerM: '3', audioInputPerM: '4', audioOutputPerM: '8' }, 'custom')
    expect(r.inputPerM).toBe('0.000000002')
    expect(r.outputPerM).toBe('0.000000002') // 1.5 nano rounds half up
    expect([r.imageInputPerM, r.audioInputPerM, r.audioOutputPerM]).toEqual(['6', '8', '12'])
    expect(longContextPreset({ inputPerM: '3', outputPerM: '3', imageInputPerM: '3' }, 'token').imageInputPerM).toBe('')
  })
  it('starts input / output at 0 when the base has none', () => {
    const r = longContextPreset({ inputPerM: '', outputPerM: '' })
    expect([r.inputPerM, r.outputPerM]).toEqual(['0', '0'])
  })
})

describe('tierPreview', () => {
  it('lists the base and each valid tier', () => {
    expect(tierPreview({ inputPerM: '10', outputPerM: '50' }, [row('272K', '20', '75')], usd))
      .toBe('≤272K：输入 $10.000 · 输出 $50.000；>272K：输入 $20.000 · 输出 $75.000')
    expect(tierPreview({ inputPerM: '', outputPerM: '1' }, [row('128K', '1', '2'), row('1M', '3', '4')], usd))
      .toBe('≤128K：输入 $0.000 · 输出 $1.000；>128K：输入 $1.000 · 输出 $2.000；>1M：输入 $3.000 · 输出 $4.000')
  })
  it('is null without valid tiers', () => {
    expect(tierPreview({ inputPerM: '1', outputPerM: '1' }, [], usd)).toBeNull()
    expect(tierPreview({ inputPerM: '1', outputPerM: '1' }, [row('x', '1', '1'), row('1K', '', '1')], usd)).toBeNull()
  })
})

describe('resolution, pick and calculator price', () => {
  const stored: PriceTier[] = [
    { aboveInputTokens: 1_000_000, inputPerM: '40', outputPerM: '100', cacheReadPerM: null, cacheWritePerM: null, imageInputPerM: null, audioInputPerM: null, audioOutputPerM: '9' },
    { aboveInputTokens: 272_000, inputPerM: '20', outputPerM: '75', cacheReadPerM: '2', cacheWritePerM: null, imageInputPerM: null, audioInputPerM: null, audioOutputPerM: null },
  ]
  it('resolves inheritance like the server (cache 0 → null; fallback prices stay null)', () => {
    const t = resolveTiers({ cacheReadPerM: '1', cacheWritePerM: '0', imageInputPerM: '5', audioInputPerM: null, audioOutputPerM: null }, stored)
    expect(t.map(x => x.aboveInputTokens)).toEqual([272_000, 1_000_000])
    expect(t[0]).toEqual({ aboveInputTokens: 272_000, inputPerM: '20', outputPerM: '75', cacheReadPerM: '2', cacheWritePerM: null, imageInputPerM: '5', audioInputPerM: null, audioOutputPerM: null })
    expect(t[1]!.cacheReadPerM).toBe('1')
    expect(t[1]!.audioOutputPerM).toBe('9')
  })
  it('picks the highest tier strictly below the prompt (= threshold stays on the lower tier)', () => {
    const t = resolveTiers({}, stored)
    expect(pickTier(t, 0)).toBeNull()
    expect(pickTier(t, 272_000)).toBeNull()
    expect(pickTier(t, 272_001)!.aboveInputTokens).toBe(272_000)
    expect(pickTier(t, 1_000_000)!.aboveInputTokens).toBe(272_000)
    expect(pickTier(t, 1_000_001)!.aboveInputTokens).toBe(1_000_000)
    expect(pickTier(null, 5)).toBeNull()
  })
  it('swaps token prices for the calculator and keeps the rest', () => {
    const price: PlazaPrice = { inputPerM: '10', outputPerM: '50', cacheReadPerM: '1', cacheWritePerM: null, perRequest: '0.01', tiers: resolveTiers({ cacheReadPerM: '1' }, stored) }
    expect(priceForPrompt(price, 100_000)).toEqual({ price, tier: null })
    const hi = priceForPrompt(price, 300_000)
    expect(hi.tier!.aboveInputTokens).toBe(272_000)
    expect(hi.price).toMatchObject({ inputPerM: '20', outputPerM: '75', cacheReadPerM: '2', perRequest: '0.01', tiers: null })
  })
  it('builds the popover table', () => {
    const rows = tierTable({ inputPerM: '10', outputPerM: '50', cacheReadPerM: '1', cacheWritePerM: '0' }, resolveTiers({ cacheReadPerM: '1' }, stored))
    expect(rows.map(r => r.label)).toEqual(['≤272K', '>272K', '>1M'])
    expect(rows[0]).toEqual({ above: null, label: '≤272K', inputPerM: '10', outputPerM: '50', cacheReadPerM: '1', cacheWritePerM: null })
    expect(tierTable({ inputPerM: '1', outputPerM: '1' }, [])).toEqual([])
  })
})

describe('plaza normalisation', () => {
  it('keeps valid tiers sorted and drops malformed ones', () => {
    expect(normalizePlazaTiers([
      { aboveInputTokens: 1_000_000, inputPerM: '4', outputPerM: '8' },
      { aboveInputTokens: 272_000, inputPerM: '2', outputPerM: '6', cacheReadPerM: '0.2', imageInputPerM: 'bad' },
      { aboveInputTokens: 0, inputPerM: '1', outputPerM: '1' },
      { inputPerM: '1', outputPerM: '1' },
      null,
    ])).toEqual([
      { aboveInputTokens: 272_000, inputPerM: '2', outputPerM: '6', cacheReadPerM: '0.2', cacheWritePerM: null, imageInputPerM: null, audioInputPerM: null, audioOutputPerM: null },
      { aboveInputTokens: 1_000_000, inputPerM: '4', outputPerM: '8', cacheReadPerM: null, cacheWritePerM: null, imageInputPerM: null, audioInputPerM: null, audioOutputPerM: null },
    ])
    expect(normalizePlazaTiers(null)).toEqual([])
  })
  it('normalizePlazaModel copies tiers; plazaPriceFromSell resolves admin tiers', () => {
    const m = normalizePlazaModel({ model: 'x', price: { inputPerM: '1', outputPerM: '2', cacheReadPerM: null, cacheWritePerM: null, tiers: [{ aboveInputTokens: 272_000, inputPerM: '2', outputPerM: '3' }] } } as never)
    expect(m.price!.tiers).toHaveLength(1)
    const noTiers = normalizePlazaModel({ model: 'y', price: { inputPerM: '1', outputPerM: '2', tiers: null } } as never)
    expect(noTiers.price!.tiers).toBeUndefined()
    const p = plazaPriceFromSell({ inputPerM: '10', outputPerM: '50', cacheReadPerM: '1', cacheWritePerM: '0', tiers: [{ aboveInputTokens: 272_000, inputPerM: '20', outputPerM: '75', cacheReadPerM: null, cacheWritePerM: null, imageInputPerM: null, audioInputPerM: null, audioOutputPerM: null }] })
    expect(p!.tiers![0]).toMatchObject({ inputPerM: '20', cacheReadPerM: '1', cacheWritePerM: null })
  })
})
