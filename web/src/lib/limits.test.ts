import { describe, expect, it } from 'vitest'
import {
  amountRatio,
  countRatio,
  levelOf,
  normalizeBillingLimits,
  normalizeSpendLimit,
  percentText,
  spendFormFrom,
  spendLimitFromForm,
  spendLimitText,
  validateSpendForm,
  withSpendLimit,
} from './limits'
import type { KeyPolicy } from './types'

const money = (v: string) => `$${Number(v).toFixed(2)}`
const policy: KeyPolicy = { allowedModels: [], allowedChannels: [], ipAllowlist: [], rpm: null, compatMode: 'strict', quotaOverflow: '' }

describe('key spend limit', () => {
  it('round-trips the form and validates amounts', () => {
    expect(spendFormFrom(null)).toEqual({ amount: '', window: 'month' })
    expect(spendFormFrom({ amount: '10', window: 'week' })).toEqual({ amount: '10', window: 'week' })
    expect(spendLimitFromForm({ amount: ' 10.50 ', window: 'day' })).toEqual({ amount: '10.5', window: 'day' })
    expect(spendLimitFromForm({ amount: '', window: 'day' })).toBeNull()
    expect(validateSpendForm({ amount: '-1', window: 'day' })).toMatch(/金额/)
    expect(validateSpendForm({ amount: '', window: 'day' })).toBeNull()
    expect(normalizeSpendLimit({ amount: '1', window: 'year' })).toBeNull()
  })

  it('sends spendLimit only when set or when clearing a stored one (older backends reject unknown fields)', () => {
    expect('spendLimit' in withSpendLimit(policy, null)).toBe(false)
    expect(withSpendLimit(policy, { amount: '5', window: 'month' }).spendLimit).toEqual({ amount: '5', window: 'month' })
    expect(withSpendLimit(policy, null, { spendLimit: { amount: '5', window: 'month' } }).spendLimit).toBeNull()
    expect('spendLimit' in withSpendLimit(policy, null, { spendLimit: undefined })).toBe(false)
  })

  it('formats limits', () => {
    expect(spendLimitText({ amount: '10', window: 'day' }, money)).toBe('$10.00/天')
    expect(spendLimitText({ amount: '10', window: 'total' }, money)).toBe('总计 $10.00')
  })
})

describe('ratios', () => {
  it('computes ratios, levels and percentages', () => {
    expect(amountRatio('4', '5')).toBe(0.8)
    expect(amountRatio('0', '0')).toBe(1)
    expect(amountRatio('1', null)).toBe(0)
    expect(countRatio(50, 100)).toBe(0.5)
    expect(countRatio(5, null)).toBe(0)
    expect(levelOf(0.5)).toBe('ok')
    expect(levelOf(0.8)).toBe('warn')
    expect(levelOf(1.2)).toBe('exceeded')
    expect(percentText(0.374)).toBe('37%')
    expect(percentText(0.0123)).toBe('1.2%')
  })
})

describe('normalizeBillingLimits', () => {
  it('accepts flattened group limits and alternative resetsAt keys', () => {
    const n = normalizeBillingLimits({
      group: { name: 'VIP', rpd: 1000, dailySpend: '5', monthlySpend: null, rpm: null },
      usage: { rpdUsed: 12, dailySpent: '1.5', monthlySpent: '20', resetsAt: { daily: '2026-10-10T00:00:00+08:00', monthly: '2026-11-01T00:00:00+08:00' } },
      keys: [{ id: 'k1', name: 'prod', spendLimit: { amount: '10', window: 'month' }, spent: '2', resetsAt: '2026-11-01T00:00:00+08:00' }],
    })
    expect(n.group).toEqual({ id: null, name: 'VIP', priceMultiplier: null, timezone: null, limits: { rpm: null, rpd: 1000, dailySpend: '5', monthlySpend: null } })
    expect(n.usage).toEqual({ rpdUsed: 12, dailySpent: '1.5', monthlySpent: '20', dayResetsAt: '2026-10-10T00:00:00+08:00', monthResetsAt: '2026-11-01T00:00:00+08:00' })
    expect(n.keys[0]?.spendLimit).toEqual({ amount: '10', window: 'month' })
  })

  it('accepts nested limits and survives nulls', () => {
    const n = normalizeBillingLimits({ group: { id: 'g', name: '默认', limits: { rpm: 60 } }, usage: { rpdUsed: Number.NaN, dailySpent: '', monthlySpent: '0', resetsAt: { day: 'a', month: 'b' } }, keys: null as never })
    expect(n.group.limits.rpm).toBe(60)
    expect(n.usage.rpdUsed).toBe(0)
    expect(n.usage.dailySpent).toBe('0')
    expect(n.usage.dayResetsAt).toBe('a')
    expect(n.keys).toEqual([])
    expect(normalizeBillingLimits(null).group.name).toBe('')
  })
})
