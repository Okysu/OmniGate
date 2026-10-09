import { describe, expect, it } from 'vitest'
import { currencyFromCode, formatUiValue } from './pluginFormat'

describe('currencyFromCode', () => {
  it('maps known codes to symbols and ISO decimals', () => {
    expect(currencyFromCode('cny')).toEqual({ code: 'CNY', symbol: '¥', decimals: 2 })
    expect(currencyFromCode('JPY')?.decimals).toBe(0)
    expect(currencyFromCode('CHF')?.symbol).toBe('CHF ')
  })

  it('rejects non-codes', () => {
    expect(currencyFromCode('')).toBeNull()
    expect(currencyFromCode('¥')).toBeNull()
    expect(currencyFromCode(undefined)).toBeNull()
  })
})

describe('formatUiValue', () => {
  it('renders missing values as an em dash', () => {
    for (const v of [null, undefined, ''])
      expect(formatUiValue(v, 'money')).toEqual({ text: '—', missing: true })
  })

  it('formats money from decimal strings without floats, always with 3 decimals', () => {
    expect(formatUiValue('110.00', 'money', { currency: 'CNY' }).text).toBe('¥110.000')
    expect(formatUiValue('1234567.5', 'money', { currency: 'USD' }).text).toBe('$1,234,567.500')
    // 0.1 + 0.2 style precision issues cannot happen: the string is never parsed as a float.
    expect(formatUiValue('9007199254740993.01', 'money', { currency: 'USD' }).text).toBe('$9,007,199,254,740,993.010')
    expect(formatUiValue('1.23456', 'money', { currency: 'USD' }).text).toBe('$1.235')
    expect(formatUiValue('0.0025986', 'money', { currency: 'USD' }).text).toBe('$0.003')
    // Sub-0.001 amounts never look free.
    expect(formatUiValue('0.0004', 'money', { currency: 'USD' }).text).toBe('<$0.001')
    expect(formatUiValue('-0.0004', 'money').text).toBe('-<0.001')
    expect(formatUiValue('-3.5', 'money', { currency: 'EUR' }).text).toBe('-€3.500')
    expect(formatUiValue('12', 'money', { currency: 'CHF' }).text).toBe('CHF 12.000')
    expect(formatUiValue('1234.5', 'money', { currency: 'JPY' }).text).toBe('¥1,234.500')
    expect(formatUiValue('12.5', 'money').text).toBe('12.500')
    expect(formatUiValue(3.5, 'money', { currency: 'USD' }).text).toBe('$3.500')
    expect(formatUiValue('n/a', 'money').text).toBe('n/a')
  })

  it('never exposes the full-precision amount in the tooltip', () => {
    expect(formatUiValue('0.0004', 'money', { currency: 'USD' }).title).toBe('USD')
    expect(formatUiValue('1.23456', 'money').title).toBeUndefined()
  })

  it('formats numbers and numeric strings', () => {
    expect(formatUiValue(1234567, 'number').text).toBe('1,234,567')
    expect(formatUiValue('1234567.125', 'number').text).toBe('1,234,567.125')
    expect(formatUiValue('-1000', 'number').text).toBe('-1,000')
  })

  it('treats percent values as ratios', () => {
    expect(formatUiValue(0.42, 'percent').text).toBe('42%')
    expect(formatUiValue('0.125', 'percent').text).toBe('12.5%')
    expect(formatUiValue(0.0005, 'percent').text).toBe('0.05%')
    expect(formatUiValue(1, 'percent').text).toBe('100%')
  })

  it('formats booleans as 是 / 否', () => {
    expect(formatUiValue(true, 'boolean')).toMatchObject({ text: '是', bool: true })
    expect(formatUiValue(false, 'boolean')).toMatchObject({ text: '否', bool: false })
    expect(formatUiValue('maybe', 'boolean').bool).toBeUndefined()
  })

  it('formats timestamps', () => {
    const now = Date.parse('2026-10-08T12:00:00Z')
    expect(formatUiValue('2026-10-08T11:58:00Z', 'relativeTime', { now }).text).toBe('2分钟前')
    expect(formatUiValue(Date.parse('2026-10-08T11:00:00Z') / 1000, 'relativeTime', { now }).text).toBe('1小时前')
    expect(formatUiValue('2026-10-08T11:58:00Z', 'datetime').title).toBe('2026-10-08T11:58:00.000Z')
    expect(formatUiValue('not a date', 'datetime').text).toBe('not a date')
  })

  it('stringifies objects for text', () => {
    expect(formatUiValue({ a: 1 }, 'text').text).toBe('{"a":1}')
    expect(formatUiValue(42, undefined).text).toBe('42')
  })
})
