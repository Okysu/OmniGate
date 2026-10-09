import type { UserGroup } from './types'
import { describe, expect, it } from 'vitest'
import {
  buildGroupInput,
  buildGroupPatch,
  emptyGroupForm,
  formFromGroup,
  isNonUnitMultiplier,
  isPatchEmpty,
  limitParts,
  logMultiplier,
  multiplierExample,
  multiplierLabel,
  multiplierTone,
  multiplierWords,
  multiplyAmount,
  myGroupOf,
  normalizeLimits,
  sortGroups,
  validateGroupForm,
} from './groups'

const money = (v: string) => `$${Number(v).toFixed(2)}`

const group: UserGroup = {
  id: 'g1',
  name: 'VIP',
  description: '年付客户',
  priceMultiplier: '0.8',
  limits: { rpm: 60, rpd: null, dailySpend: '5', monthlySpend: null },
  timezone: 'Asia/Shanghai',
  isDefault: false,
  members: 12,
  version: 3,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
}

describe('multipliers', () => {
  it('words the multiplier in Chinese price terms', () => {
    expect(multiplierWords('1')).toBe('原价')
    expect(multiplierWords('1.000')).toBe('原价')
    expect(multiplierWords('0')).toBe('免费')
    expect(multiplierWords('0.8')).toBe('八折')
    expect(multiplierWords('0.80')).toBe('八折')
    expect(multiplierWords('0.85')).toBe('八五折')
    expect(multiplierWords('0.5')).toBe('五折')
    expect(multiplierWords('0.1')).toBe('一折')
    expect(multiplierWords('0.05')).toBe('0.5 折')
    expect(multiplierWords('0.825')).toBe('8.25 折')
    expect(multiplierWords('1.5')).toBe('加价 50%')
    expect(multiplierWords('2')).toBe('加价 100%')
    expect(multiplierWords('abc')).toBe('')
    expect(multiplierLabel('0.80')).toBe('×0.8 / 八折')
  })

  it('multiplies amounts exactly (half-up at 9 decimals)', () => {
    expect(multiplyAmount('1', '0.8')).toBe('0.8')
    expect(multiplyAmount('2.5', '0.333333333')).toBe('0.833333333')
    expect(multiplyAmount('0.000000001', '0.5')).toBe('0.000000001')
    expect(multiplyAmount('x', '1')).toBeNull()
    expect(multiplierExample('0.8', money)).toBe('售价 $1.00 → $0.80')
    expect(multiplierExample('-1', money)).toBeNull()
  })

  it('classifies tones and non-unit multipliers', () => {
    expect(multiplierTone('0.8')).toBe('discount')
    expect(multiplierTone('1.2')).toBe('markup')
    expect(multiplierTone('1')).toBe('list')
    expect(multiplierTone(undefined)).toBe('list')
    expect(isNonUnitMultiplier('1.0')).toBe(false)
    expect(isNonUnitMultiplier('0')).toBe(true)
    expect(isNonUnitMultiplier(undefined)).toBe(false)
  })
})

describe('limits', () => {
  it('normalises and summarises limits', () => {
    expect(normalizeLimits({ rpm: 0, rpd: 1.5, dailySpend: 'x', monthlySpend: '100' })).toEqual({ rpm: null, rpd: null, dailySpend: null, monthlySpend: '100' })
    expect(limitParts(normalizeLimits(group.limits), money).map(p => p.text)).toEqual(['60 次/分钟', '$5.00/天'])
    expect(limitParts(normalizeLimits(null), money)).toEqual([])
  })
})

describe('group form', () => {
  it('validates names, multiplier range, limits and time zone', () => {
    const f = emptyGroupForm()
    expect(validateGroupForm(f)).toEqual({ name: '请填写名称' })
    f.name = 'x'.repeat(51)
    f.priceMultiplier = '100.5'
    f.rpm = '0'
    f.rpd = '1.5'
    f.dailySpend = '-1'
    f.monthlySpend = 'abc'
    f.timezone = 'Mars/Base'
    expect(validateGroupForm({ ...f, rpm: '1000001' })['limits.rpm']).toMatch(/1–1,000,000/)
    expect(Object.keys(validateGroupForm(f)).sort()).toEqual(['limits.dailySpend', 'limits.monthlySpend', 'limits.rpd', 'limits.rpm', 'name', 'priceMultiplier', 'timezone'])
    f.name = 'ok'
    f.priceMultiplier = '100'
    f.rpm = '60'
    f.rpd = ''
    f.dailySpend = '0'
    f.monthlySpend = ''
    f.timezone = 'UTC'
    expect(validateGroupForm(f)).toEqual({})
  })

  it('builds the create body with canonical amounts and null = unlimited', () => {
    const f = emptyGroupForm()
    f.name = ' VIP '
    f.priceMultiplier = '0.80'
    f.dailySpend = '5.50'
    f.rpm = '60'
    expect(buildGroupInput(f)).toEqual({
      name: 'VIP',
      description: '',
      priceMultiplier: '0.8',
      limits: { rpm: 60, rpd: null, dailySpend: '5.5', monthlySpend: null },
      timezone: 'Asia/Shanghai',
      isDefault: false,
    })
  })

  it('patches only changed fields with the version', () => {
    const f = formFromGroup(group)
    expect(isPatchEmpty(buildGroupPatch(f, group))).toBe(true)
    f.priceMultiplier = '0.80' // canonical-equal
    expect(isPatchEmpty(buildGroupPatch(f, group))).toBe(true)
    f.priceMultiplier = '0.7'
    f.rpd = '1000'
    f.isDefault = true
    expect(buildGroupPatch(f, group)).toEqual({
      version: 3,
      priceMultiplier: '0.7',
      limits: { rpm: 60, rpd: 1000, dailySpend: '5', monthlySpend: null },
      isDefault: true,
    })
  })

  it('sorts the default group first', () => {
    expect(sortGroups([{ name: 'b', isDefault: false }, { name: '默认', isDefault: true }, { name: 'a', isDefault: false }]).map(g => g.name)).toEqual(['默认', 'a', 'b'])
  })
})

describe('/api/me group', () => {
  it('accepts top-level or user.group and fills defaults', () => {
    const user = { id: 'u', displayName: 'u', email: null, avatarUrl: null, role: 'user' as const, status: 'active' as const, createdAt: '', lastLoginAt: null, version: 1, identities: [] }
    expect(myGroupOf({ user, group: { id: 'g', name: 'VIP', priceMultiplier: '0.8', limits: { rpm: 10, rpd: null, dailySpend: null, monthlySpend: null } } })).toEqual({ id: 'g', name: 'VIP', priceMultiplier: '0.8', limits: { rpm: 10, rpd: null, dailySpend: null, monthlySpend: null } })
    expect(myGroupOf({ user: { ...user, group: { id: 'g2', name: '默认' } } })?.priceMultiplier).toBe('1')
    expect(myGroupOf({ user })).toBeNull()
  })
})

describe('logMultiplier', () => {
  it('shows nothing for 1 / absent and splits when the backend does', () => {
    expect(logMultiplier({})).toBeNull()
    expect(logMultiplier({ priceMultiplier: '1' })).toBeNull()
    expect(logMultiplier({ priceMultiplier: '0.4' })?.short).toBe('×0.4')
    expect(logMultiplier({ priceMultiplier: '0.4' })?.detail).toContain('用户组倍率 × 分时价格倍率')
    expect(logMultiplier({ priceMultiplier: '0.4', groupMultiplier: '0.8', scheduleMultiplier: '0.5' })?.detail).toBe('分组 ×0.8 · 分时 ×0.5（售价 ×0.4）')
  })
})
