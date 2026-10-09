import { describe, expect, it } from 'vitest'
import { addAmounts, amountSign, DISPLAY_DECIMALS, formatMoney, fromNano, isValidAmount, roundDecimal, toNano } from './money'

const USD = { code: 'USD', symbol: '$', decimals: 2 }
const CNY = { code: 'CNY', symbol: '¥', decimals: 2 }
const JPY = { code: 'JPY', symbol: '¥', decimals: 0 }

describe('roundDecimal', () => {
  it('pads to the requested decimals', () => {
    expect(roundDecimal('1', 2)).toBe('1.00')
    expect(roundDecimal('1.5', 2)).toBe('1.50')
    expect(roundDecimal('0', 3)).toBe('0.000')
  })

  it('rounds half away from zero', () => {
    expect(roundDecimal('1.005', 2)).toBe('1.01')
    expect(roundDecimal('1.004999999', 2)).toBe('1.00')
    expect(roundDecimal('-1.005', 2)).toBe('-1.01')
    expect(roundDecimal('-1.004', 2)).toBe('-1.00')
    expect(roundDecimal('2.5', 0)).toBe('3')
    expect(roundDecimal('-2.5', 0)).toBe('-3')
  })

  it('carries across the integer part', () => {
    expect(roundDecimal('9.999', 2)).toBe('10.00')
    expect(roundDecimal('99.995', 2)).toBe('100.00')
    expect(roundDecimal('0.999999999', 0)).toBe('1')
  })

  it('handles values that do not fit in a double', () => {
    expect(roundDecimal('123456789012345678901.125', 2)).toBe('123456789012345678901.13')
  })

  it('does not produce negative zero', () => {
    expect(roundDecimal('-0.001', 2)).toBe('0.00')
    expect(roundDecimal('-0', 2)).toBe('0.00')
  })

  it('returns invalid input unchanged', () => {
    expect(roundDecimal('abc', 2)).toBe('abc')
    expect(roundDecimal('1e5', 2)).toBe('1e5')
  })
})

describe('formatMoney', () => {
  it('always shows DISPLAY_DECIMALS (3) fraction digits, with symbol and grouping', () => {
    expect(DISPLAY_DECIMALS).toBe(3)
    expect(formatMoney('0.0025986', USD)).toBe('$0.003')
    expect(formatMoney('1.5', USD)).toBe('$1.500')
    expect(formatMoney('24', USD)).toBe('$24.000')
    expect(formatMoney('1234.5', USD)).toBe('$1,234.500')
    expect(formatMoney('1234567.891', CNY)).toBe('¥1,234,567.891')
    expect(formatMoney('0', USD)).toBe('$0.000')
    expect(formatMoney('15.000000000', USD)).toBe('$15.000')
  })

  it('ignores the currency decimals for display', () => {
    expect(formatMoney('1234.5', JPY)).toBe('¥1,234.500')
    expect(formatMoney('0.12345', { code: 'BHD', symbol: 'BD ', decimals: 3 })).toBe('BD 0.123')
    expect(formatMoney('0.12345', { code: 'XYZ', symbol: 'X', decimals: 6 })).toBe('X0.123')
  })

  it('rounds half away from zero', () => {
    expect(formatMoney('0.0005', USD)).toBe('$0.001')
    expect(formatMoney('1.2345', USD)).toBe('$1.235')
    expect(formatMoney('1.234499999', USD)).toBe('$1.234')
    expect(formatMoney('-1.2345', USD)).toBe('-$1.235')
    expect(formatMoney('9.9995', USD)).toBe('$10.000')
    expect(formatMoney('999.9995', USD)).toBe('$1,000.000')
  })

  it('places the minus sign before the symbol', () => {
    expect(formatMoney('-12.3456', USD)).toBe('-$12.346')
    expect(formatMoney('-0', USD)).toBe('$0.000')
  })

  it('shows a lower bound when rounding would hide a non-zero amount', () => {
    expect(formatMoney('0.000123', USD)).toBe('<$0.001')
    expect(formatMoney('0.000499999', USD)).toBe('<$0.001')
    expect(formatMoney('-0.000123', USD)).toBe('-<$0.001')
    expect(formatMoney('0.0001', USD, { signed: true })).toBe('+<$0.001')
    expect(formatMoney('-0.0001', USD, { signed: true })).toBe('-<$0.001')
    expect(formatMoney('0.0001', USD, { plain: true })).toBe('<0.001')
    expect(formatMoney('0.000000001', JPY)).toBe('<¥0.001')
  })

  it('supports signed and plain output', () => {
    expect(formatMoney('5', USD, { signed: true })).toBe('+$5.000')
    expect(formatMoney('-5', USD, { signed: true })).toBe('-$5.000')
    expect(formatMoney('0', USD, { signed: true })).toBe('$0.000')
    expect(formatMoney('1000', USD, { plain: true })).toBe('1,000.000')
  })

  it('renders missing values as a dash and garbage unchanged', () => {
    expect(formatMoney(null, USD)).toBe('—')
    expect(formatMoney(undefined, USD)).toBe('—')
    expect(formatMoney('', USD)).toBe('—')
    expect(formatMoney('NaN', USD)).toBe('NaN')
  })

  it('falls back to a default currency', () => {
    expect(formatMoney('1', null)).toBe('$1.000')
  })
})

describe('nano arithmetic', () => {
  it('round-trips decimal strings', () => {
    expect(toNano('1.5')).toBe(1_500_000_000n)
    expect(toNano('-0.000000001')).toBe(-1n)
    expect(fromNano(1_500_000_000n)).toBe('1.5')
    expect(fromNano(-1n)).toBe('-0.000000001')
    expect(fromNano(0n)).toBe('0')
  })

  it('rejects more than 9 decimals', () => {
    expect(() => toNano('0.0000000001')).toThrow()
    expect(() => toNano('x')).toThrow()
  })

  it('adds exactly', () => {
    expect(addAmounts('0.1', '0.2')).toBe('0.3')
    expect(addAmounts('10', '-12.5')).toBe('-2.5')
    expect(addAmounts('99999999999.999999999', '0.000000001')).toBe('100000000000')
  })
})

describe('validation & sign', () => {
  it('validates amount input', () => {
    expect(isValidAmount('1')).toBe(true)
    expect(isValidAmount('0.123456789')).toBe(true)
    expect(isValidAmount('0.1234567891')).toBe(false)
    expect(isValidAmount('-1')).toBe(false)
    expect(isValidAmount('-1', { allowNegative: true })).toBe(true)
    expect(isValidAmount('1.')).toBe(false)
    expect(isValidAmount('.5')).toBe(false)
    expect(isValidAmount('1e3')).toBe(false)
    expect(isValidAmount('12.345', { maxFrac: 2 })).toBe(false)
  })

  it('reports the sign', () => {
    expect(amountSign('5')).toBe(1)
    expect(amountSign('-0.01')).toBe(-1)
    expect(amountSign('0.000')).toBe(0)
    expect(amountSign('-0')).toBe(0)
    expect(amountSign(null)).toBe(0)
  })
})
