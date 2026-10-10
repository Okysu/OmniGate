import { describe, expect, it } from 'vitest'
import {
  captureInviteFromUrl,
  formatRate,
  INVITE_STORAGE_KEY,
  INVITE_TTL_MS,
  loadInviteCode,
  normalizeInviteCode,
  referralRuleText,
  saveInviteCode,
} from './referral'

function memoryStorage() {
  const data = new Map<string, string>()
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  }
}

const throwing = {
  getItem: () => { throw new Error('blocked') },
  setItem: () => { throw new Error('blocked') },
  removeItem: () => { throw new Error('blocked') },
}

describe('invite codes', () => {
  it('normalises codes from query values', () => {
    expect(normalizeInviteCode(' ab12cd34 ')).toBe('AB12CD34')
    expect(normalizeInviteCode(['XY98ZZ11', 'other'])).toBe('XY98ZZ11')
    expect(normalizeInviteCode('')).toBeNull()
    expect(normalizeInviteCode('<script>')).toBeNull()
    expect(normalizeInviteCode(undefined)).toBeNull()
  })

  it('stores for 30 days and expires', () => {
    const s = memoryStorage()
    expect(saveInviteCode('ab12cd34', 1000, s)).toBe('AB12CD34')
    expect(JSON.parse(s.data.get(INVITE_STORAGE_KEY)!)).toEqual({ code: 'AB12CD34', expiresAt: 1000 + INVITE_TTL_MS })
    expect(loadInviteCode(1000 + INVITE_TTL_MS - 1, s)).toBe('AB12CD34')
    expect(loadInviteCode(1000 + INVITE_TTL_MS, s)).toBeNull()
    expect(s.data.has(INVITE_STORAGE_KEY)).toBe(false)
  })

  it('drops malformed entries and survives throwing storage', () => {
    const s = memoryStorage()
    s.data.set(INVITE_STORAGE_KEY, 'not json')
    expect(loadInviteCode(0, s)).toBeNull()
    expect(saveInviteCode('AB12CD34', 0, throwing)).toBe('AB12CD34')
    expect(loadInviteCode(0, throwing)).toBeNull()
    expect(loadInviteCode(0, null)).toBeNull()
  })

  it('captures from the URL, else keeps the stored code', () => {
    const s = memoryStorage()
    expect(captureInviteFromUrl('?redirect=%2Fconsole&invite=ab12cd34', 0, s)).toBe('AB12CD34')
    expect(captureInviteFromUrl('', 10, s)).toBe('AB12CD34')
    expect(captureInviteFromUrl('?invite=ZZ99ZZ99', 20, s)).toBe('ZZ99ZZ99')
    expect(captureInviteFromUrl('?invite=bad!', 30, s)).toBe('ZZ99ZZ99')
  })
})

describe('referral display', () => {
  const money = (v: string) => `¥${v}`
  it('formats rates and the rule sentence', () => {
    expect(formatRate('10')).toBe('10%')
    expect(formatRate('12.50')).toBe('12.5%')
    expect(formatRate(null)).toBe('—')
    expect(referralRuleText({ rate: '10', minRecharge: '50' }, money)).toBe('好友通过你的链接注册后，每次使用兑换码充值 ≥ ¥50，你获得充值金额 10% 的余额返利。')
    expect(referralRuleText({ rate: '5', minRecharge: '0' }, money)).toBe('好友通过你的链接注册后，每次使用兑换码充值，你获得充值金额 5% 的余额返利。')
  })
})
