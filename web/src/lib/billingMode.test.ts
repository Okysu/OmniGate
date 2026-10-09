import type { PriceFormAmounts } from './billingMode'
import { describe, expect, it } from 'vitest'
import {
  BILLING_MODE_FIELDS,
  formatPricePart,
  inferBillingMode,
  isSetPrice,
  priceBodyForMode,
  priceCaption,
  priceSummary,
  priceSummaryText,
  priceSummaryTitle,
  PRICE_FIELD_KEYS,
  showsTokenPrices,
  validateModeAmounts,
} from './billingMode'

/** An admin price as the backend sends it: every zero-default amount "0", fallbacks null. */
const admin = (over: Record<string, string | null> = {}) => ({
  inputPerM: '0',
  outputPerM: '0',
  cacheReadPerM: '0',
  cacheWritePerM: '0',
  perRequest: '0',
  perImage: '0',
  imageInputPerM: null,
  audioInputPerM: null,
  audioOutputPerM: null,
  perMinute: '0',
  perMCharacters: '0',
  ...over,
})

function form(over: Partial<PriceFormAmounts> = {}): PriceFormAmounts {
  const out = Object.fromEntries(PRICE_FIELD_KEYS.map(k => [k, ''])) as PriceFormAmounts
  return { ...out, ...over }
}

const usd = (v: string) => `$${v}`

describe('isSetPrice', () => {
  it('counts zero-default prices only when above zero', () => {
    expect(isSetPrice('perRequest', '0.04')).toBe(true)
    expect(isSetPrice('perRequest', '0')).toBe(false)
    expect(isSetPrice('perRequest', '0.000')).toBe(false)
    expect(isSetPrice('perImage', null)).toBe(false)
    expect(isSetPrice('perMinute', 'abc')).toBe(false)
  })

  it('counts fallback prices whenever present (an explicit 0 is a price)', () => {
    expect(isSetPrice('audioInputPerM', '0')).toBe(true)
    expect(isSetPrice('imageInputPerM', null)).toBe(false)
    expect(isSetPrice('audioOutputPerM', undefined)).toBe(false)
  })
})

describe('inferBillingMode', () => {
  it('detects per-call prices', () => {
    expect(inferBillingMode(admin({ perRequest: '0.04' }))).toBe('request')
    expect(inferBillingMode({ inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perRequest: '0.04' })).toBe('request')
  })

  it('detects per-image prices with an optional per-request fee', () => {
    expect(inferBillingMode(admin({ perImage: '0.02' }))).toBe('image')
    expect(inferBillingMode(admin({ perImage: '0.02', perRequest: '0.01' }))).toBe('image')
  })

  it('detects audio prices with an optional per-request fee', () => {
    expect(inferBillingMode(admin({ perMinute: '0.006' }))).toBe('audio')
    expect(inferBillingMode(admin({ perMCharacters: '15', perRequest: '0.001' }))).toBe('audio')
    expect(inferBillingMode(admin({ audioInputPerM: '0', audioOutputPerM: '12' }))).toBe('audio')
  })

  it('detects token prices (default for empty prices)', () => {
    expect(inferBillingMode(admin({ inputPerM: '2.5', outputPerM: '10', cacheReadPerM: '1.25' }))).toBe('token')
    expect(inferBillingMode(admin({ outputPerM: '10' }))).toBe('token')
    expect(inferBillingMode(admin())).toBe('token')
    expect(inferBillingMode(null)).toBe('token')
  })

  it('falls back to custom for mixed prices', () => {
    expect(inferBillingMode(admin({ inputPerM: '2.5', perRequest: '0.01' }))).toBe('custom')
    expect(inferBillingMode(admin({ inputPerM: '5', outputPerM: '40', perImage: '0.04', imageInputPerM: '10' }))).toBe('custom')
    expect(inferBillingMode(admin({ perImage: '0.02', perMinute: '0.006' }))).toBe('custom')
    expect(inferBillingMode(admin({ inputPerM: '0.6', audioOutputPerM: '12' }))).toBe('custom')
    expect(inferBillingMode(admin({ perImage: '0.02', imageInputPerM: '0' }))).toBe('custom')
  })
})

describe('priceBodyForMode', () => {
  const values = form({ inputPerM: '2.5', outputPerM: '10', cacheReadPerM: '', perRequest: '0.04', perImage: '0.02', imageInputPerM: '3', audioInputPerM: '4', perMinute: '0.006', perMCharacters: '' })

  it('submits only the per-request fee in 按次 mode', () => {
    expect(priceBodyForMode('request', values)).toEqual({ inputPerM: '0', outputPerM: '0', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0.04' })
  })

  it('submits per-image + per-request in 按张 mode', () => {
    expect(priceBodyForMode('image', values)).toEqual({ inputPerM: '0', outputPerM: '0', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0.04', perImage: '0.02' })
    expect(priceBodyForMode('image', form({ perImage: '0.02' }))).toEqual({ inputPerM: '0', outputPerM: '0', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0', perImage: '0.02' })
  })

  it('submits token prices only in 按 Token mode (blank → "0")', () => {
    expect(priceBodyForMode('token', values)).toEqual({ inputPerM: '2.5', outputPerM: '10', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0' })
  })

  it('submits audio prices (blank optional ones omitted) in 按时长 mode', () => {
    expect(priceBodyForMode('audio', values)).toEqual({ inputPerM: '0', outputPerM: '0', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0.04', audioInputPerM: '4', perMinute: '0.006' })
  })

  it('submits every typed field in 自定义组合 mode', () => {
    expect(priceBodyForMode('custom', values)).toEqual({ inputPerM: '2.5', outputPerM: '10', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0.04', perImage: '0.02', imageInputPerM: '3', audioInputPerM: '4', perMinute: '0.006' })
  })

  it('trims values', () => {
    expect(priceBodyForMode('request', form({ perRequest: ' 0.5 ' })).perRequest).toBe('0.5')
  })

  it('covers every field in custom mode', () => {
    expect([...BILLING_MODE_FIELDS.custom].sort()).toEqual([...PRICE_FIELD_KEYS].sort())
  })
})

describe('validateModeAmounts', () => {
  it('requires a non-zero amount in the active mode', () => {
    expect(validateModeAmounts('request', form())).toEqual({ perRequest: '请填写大于 0 的每次请求价格' })
    expect(validateModeAmounts('request', form({ perRequest: '0' }))).toHaveProperty('perRequest')
    expect(validateModeAmounts('request', form({ inputPerM: '5' }))).toHaveProperty('perRequest')
    expect(validateModeAmounts('request', form({ perRequest: '0.04' }))).toEqual({})
    expect(validateModeAmounts('token', form({ cacheReadPerM: '1' }))).toEqual({})
    expect(validateModeAmounts('token', form({ perRequest: '1' }))).toHaveProperty('inputPerM')
    expect(validateModeAmounts('image', form({ perRequest: '0.01' }))).toEqual({})
    expect(validateModeAmounts('image', form())).toHaveProperty('perImage')
    expect(validateModeAmounts('audio', form({ audioInputPerM: '0' }))).toHaveProperty('perMinute')
    expect(validateModeAmounts('audio', form({ perMCharacters: '15' }))).toEqual({})
    expect(validateModeAmounts('custom', form({ perImage: '0.02' }))).toEqual({})
  })

  it('flags malformed amounts of the active mode only', () => {
    expect(validateModeAmounts('request', form({ perRequest: '1.2.3' }))).toEqual({ perRequest: '非负十进制数，最多 9 位小数' })
    expect(validateModeAmounts('request', form({ perRequest: '0.04', inputPerM: 'x' }))).toEqual({})
    expect(validateModeAmounts('token', form({ inputPerM: '1', outputPerM: '-1' }))).toEqual({ outputPerM: '非负十进制数，最多 9 位小数' })
  })
})

describe('priceSummary', () => {
  it('is null for unpriced models', () => {
    expect(priceSummary(null)).toBeNull()
    expect(priceSummaryText(null, usd)).toBe('未定价')
  })

  it('summarises a per-call price without $0 token parts', () => {
    const s = priceSummary(admin({ perRequest: '0.04' }))!
    expect(s.mode).toBe('request')
    expect(s.main.map(p => p.key)).toEqual(['perRequest'])
    expect(s.extra).toEqual([])
    expect(priceSummaryText(s, usd)).toBe('$0.04 / 次')
  })

  it('summarises per-image prices', () => {
    expect(priceSummaryText(priceSummary(admin({ perImage: '0.02' })), usd)).toBe('$0.02 / 张')
    expect(priceSummaryText(priceSummary(admin({ perImage: '0.02', perRequest: '0.01' })), usd)).toBe('$0.02 / 张 + $0.01 / 次')
  })

  it('summarises audio prices', () => {
    expect(priceSummaryText(priceSummary(admin({ perMinute: '0.006' })), usd)).toBe('$0.006 / 分钟')
    expect(priceSummaryText(priceSummary(admin({ perMCharacters: '15' })), usd)).toBe('$15 / 1M 字符')
    const tts = priceSummary(admin({ perMCharacters: '15', audioOutputPerM: '12' }))!
    expect(tts.main.map(p => p.key)).toEqual(['perMCharacters'])
    expect(tts.extra.map(p => p.key)).toEqual(['audioOutputPerM'])
    expect(priceSummaryText(priceSummary(admin({ audioInputPerM: '4', audioOutputPerM: '12' })), usd)).toBe('音频输入 $4 · 音频输出 $12 / 1M')
  })

  it('keeps the input / output display for token prices', () => {
    const s = priceSummary(admin({ inputPerM: '2.5', outputPerM: '10', cacheReadPerM: '1.25' }))!
    expect(s.mode).toBe('token')
    expect(s.main.map(p => p.key)).toEqual(['inputPerM', 'outputPerM'])
    expect(s.extra.map(p => p.key)).toEqual(['cacheReadPerM'])
    expect(priceSummaryText(s, usd)).toBe('输入 $2.5 · 输出 $10 / 1M')
    expect(priceSummaryText(priceSummary(admin()), usd)).toBe('输入 $0 · 输出 $0 / 1M')
  })

  it('shows the main parts of combinations compactly', () => {
    expect(priceSummaryText(priceSummary(admin({ inputPerM: '1', outputPerM: '2', perRequest: '0.01' })), usd)).toBe('输入 $1 · 输出 $2 / 1M + $0.01 / 次')
    const img = priceSummary(admin({ inputPerM: '5', outputPerM: '40', perImage: '0.04', imageInputPerM: '10' }))!
    expect(img.mode).toBe('custom')
    expect(priceSummaryText(img, usd)).toBe('输入 $5 · 输出 $40 / 1M + $0.04 / 张')
    expect(img.extra.map(p => p.key)).toEqual(['imageInputPerM'])
    expect(priceSummaryText(priceSummary(admin({ perImage: '0.02', perMinute: '0.006' })), usd)).toBe('$0.02 / 张 + $0.006 / 分钟')
    // Only a fallback price set besides per-call fees: still shown, never empty.
    expect(priceSummary(admin({ imageInputPerM: '3', perMinute: '0.006', perImage: '0.01' }))!.main.length).toBeGreaterThan(0)
  })

  it('reads plaza prices (null = not set)', () => {
    const s = priceSummary({ inputPerM: '0', outputPerM: '0', cacheReadPerM: null, cacheWritePerM: null, perRequest: '0.05', perImage: null })!
    expect(s.mode).toBe('request')
    expect(priceSummaryText(s, usd)).toBe('$0.05 / 次')
  })
})

describe('formatPricePart', () => {
  it('labels token parts by default', () => {
    const s = priceSummary(admin({ inputPerM: '2.5', outputPerM: '10' }))!
    expect(formatPricePart(s.main[0]!, usd)).toBe('输入 $2.5 / 1M')
    expect(formatPricePart(s.main[0]!, usd, { label: false })).toBe('$2.5 / 1M')
    const r = priceSummary(admin({ perRequest: '0.04' }))!
    expect(formatPricePart(r.main[0]!, usd)).toBe('$0.04 / 次')
    expect(formatPricePart(r.main[0]!, usd, { label: true })).toBe('每次请求 $0.04 / 次')
  })
})

describe('priceSummaryTitle', () => {
  it('lists the mode and every set price', () => {
    expect(priceSummaryTitle(priceSummary(admin({ perImage: '0.02', perRequest: '0.01' })), usd)).toBe('计费方式：按张\n每张图片：$0.02 / 张\n每次请求：$0.01 / 次')
    expect(priceSummaryTitle(priceSummary(admin({ inputPerM: '1', outputPerM: '2', cacheReadPerM: '0.5' })), usd)).toBe('计费方式：按 Token\n输入：$1 / 1M\n输出：$2 / 1M\n缓存读：$0.5 / 1M')
    expect(priceSummaryTitle(null, usd)).toBe('未定价')
  })
})

describe('priceCaption / showsTokenPrices', () => {
  it('describes the unit of the price block', () => {
    expect(priceCaption(priceSummary(admin({ inputPerM: '1' })))).toBe('每 1M tokens')
    expect(priceCaption(priceSummary(admin({ perRequest: '0.04' })))).toBe('按次计费')
    expect(priceCaption(priceSummary(admin({ perImage: '0.04' })))).toBe('按张计费')
    expect(priceCaption(priceSummary(admin({ perMinute: '0.006' })))).toBe('按时长 / 字符计费')
    expect(priceCaption(priceSummary(admin({ inputPerM: '1', perRequest: '0.01' })))).toBe('token 价格每 1M · 组合计费')
    expect(priceCaption(priceSummary(admin({ perImage: '0.02', perMinute: '0.006' })))).toBe('组合计费')
    expect(priceCaption(null)).toBe('每 1M tokens')
  })

  it('tells whether input / output prices are shown', () => {
    expect(showsTokenPrices(priceSummary(admin()))).toBe(true)
    expect(showsTokenPrices(priceSummary(admin({ perRequest: '0.04' })))).toBe(false)
    expect(showsTokenPrices(priceSummary(admin({ audioInputPerM: '4' })))).toBe(false)
    expect(showsTokenPrices(null)).toBe(false)
  })
})
