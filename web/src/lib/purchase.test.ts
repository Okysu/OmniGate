import type { CatalogPlan, PurchaseOption, PurchaseOptions, PurchaseResult, QuotaRule, QuotaWindow } from './types'
import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import {
  balanceAfter,
  buildPurchaseCards,
  canAfford,
  changedPrice,
  directIntent,
  findIntent,
  intentButtonLabel,
  intentTitle,
  isLongDescription,
  modelsOverlap,
  parallelSubscriptions,
  parallelWarning,
  planValue,
  purchaseBody,
  purchaseErrorMessage,
  purchaseSuccessText,
  shortfall,
  upgradeIntents,
} from './purchase'

const money = (v: string) => `$${v}`

function option(over: Partial<PurchaseOption> = {}, plan: Partial<PurchaseOption['plan']> = {}): PurchaseOption {
  return {
    plan: { id: 'pro', name: 'Pro', description: '', listPrice: '100', duration: '30d', models: [], rules: [], stackable: false, ...plan },
    purchasable: true,
    action: 'new',
    price: '100',
    renewSubscriptionId: null,
    currentEndsAt: null,
    newEndsAt: '2026-11-09T00:00:00Z',
    upgrades: [],
    ...over,
  }
}

const upgrade = (id: string, name: string, price: string) => ({
  fromSubscriptionId: id,
  fromPlanId: `p-${id}`,
  fromPlanName: name,
  fromPrice: '20',
  price,
  credit: '0.66',
  remainingSeconds: 86400,
  endsAt: '2026-10-20T00:00:00Z',
})

describe('purchase intents', () => {
  it('builds new / renew intents and none for plans without a price', () => {
    expect(directIntent(option())).toMatchObject({ action: 'new', planId: 'pro', price: '100', fromSubscriptionId: null, currentEndsAt: null, endsAt: '2026-11-09T00:00:00Z' })
    const renew = directIntent(option({ action: 'renew', renewSubscriptionId: 's1', currentEndsAt: '2026-10-20T00:00:00Z', newEndsAt: '2026-11-19T00:00:00Z' }))
    expect(renew).toMatchObject({ action: 'renew', currentEndsAt: '2026-10-20T00:00:00Z', endsAt: '2026-11-19T00:00:00Z' })
    expect(directIntent(option({ purchasable: false, price: null }))).toBeNull()
    expect(directIntent(option({ price: '0' }))).toBeNull()
  })

  it('builds upgrade intents cheapest first, skipping non-positive prices', () => {
    const o = option({ upgrades: [upgrade('b', 'Go+', '30.5'), upgrade('a', 'Go', '12'), upgrade('c', 'X', '0')] })
    const ups = upgradeIntents(o)
    expect(ups.map(u => u.fromSubscriptionId)).toEqual(['a', 'b'])
    expect(ups[0]).toMatchObject({ action: 'upgrade', planName: 'Pro', fromPlanName: 'Go', price: '12', endsAt: '2026-10-20T00:00:00Z', credit: '0.66', currentEndsAt: null })
    const cards = buildPurchaseCards({ available: '50', currency: 'USD', plans: [o, option({ purchasable: false, price: null }, { id: 'free' })] })
    expect(cards).toHaveLength(2)
    expect(cards[1]!.direct).toBeNull()
    // An upgradable plan offers only the upgrade; plans without a price offer nothing.
    expect(cards[0]!.direct).not.toBeNull()
    expect(cards[0]!.showDirect).toBe(false)
    expect(cards[1]!.showDirect).toBe(false)
    expect(buildPurchaseCards({ available: '50', currency: 'USD', plans: [option({})] })[0]!.showDirect).toBe(true)
    expect(buildPurchaseCards(null)).toEqual([])
  })

  it('finds the same intent in refreshed options', () => {
    const opts: PurchaseOptions = { available: '1', currency: 'USD', plans: [option({ price: '120', upgrades: [upgrade('a', 'Go', '11')] })] }
    expect(findIntent(opts, 'pro', null)?.price).toBe('120')
    expect(findIntent(opts, 'pro', 'a')?.price).toBe('11')
    expect(findIntent(opts, 'pro', 'zz')).toBeNull()
    expect(findIntent(opts, 'nope', null)).toBeNull()
  })

  it('labels buttons and titles', () => {
    const o = option({ upgrades: [upgrade('a', 'Go', '12')] })
    expect(intentButtonLabel(directIntent(o)!, money)).toBe('购买')
    expect(intentButtonLabel(directIntent(option({ action: 'renew' }))!, money)).toBe('续费')
    expect(intentButtonLabel(upgradeIntents(o)[0]!, money)).toBe('从 Go 升级 · 补差价 $12')
    expect(intentTitle(directIntent(o)!)).toBe('购买套餐「Pro」')
    expect(intentTitle(upgradeIntents(o)[0]!)).toBe('升级套餐：Go → Pro')
  })

  it('sends expectedPrice and the upgrade source', () => {
    const o = option({ upgrades: [upgrade('a', 'Go', '12')] })
    expect(purchaseBody(directIntent(o)!)).toEqual({ planId: 'pro', expectedPrice: '100' })
    expect(purchaseBody(upgradeIntents(o)[0]!)).toEqual({ planId: 'pro', fromSubscriptionId: 'a', expectedPrice: '12' })
  })
})

describe('balance', () => {
  it('computes the balance after purchase exactly', () => {
    expect(balanceAfter('100.1', '0.2')).toBe('99.9')
    expect(balanceAfter('10', '12.5')).toBe('-2.5')
    expect(balanceAfter('-1', '1')).toBe('-2')
    expect(balanceAfter(null, '1')).toBeNull()
    expect(balanceAfter('x', '1')).toBeNull()
  })

  it('checks affordability (available ≥ price)', () => {
    expect(canAfford('12', '12')).toBe(true)
    expect(canAfford('11.999999999', '12')).toBe(false)
    expect(canAfford(null, '12')).toBe(false)
    expect(shortfall('10', '12.5')).toBe('2.5')
    expect(shortfall('20', '12.5')).toBeNull()
  })
})

describe('errors', () => {
  it('maps purchase error codes', () => {
    expect(purchaseErrorMessage(new ApiError(403, 'insufficient_balance', 'x', null, { price: '12', available: '3' }), money))
      .toBe('钱包余额不足：需要 $12，当前可用 $3。请先使用兑换码充值。')
    expect(purchaseErrorMessage(new ApiError(403, 'insufficient_balance', 'x'), money)).toContain('余额不足')
    const changed = new ApiError(409, 'price_changed', 'x', null, { price: '15' })
    expect(changedPrice(changed)).toBe('15')
    expect(purchaseErrorMessage(changed, money)).toBe('套餐价格已变为 $15，请确认新价格后重试。')
    expect(changedPrice(new ApiError(409, 'plan_archived', 'x'))).toBeNull()
    expect(purchaseErrorMessage(new ApiError(409, 'plan_archived', 'x'), money)).toContain('下架')
    expect(purchaseErrorMessage(new ApiError(409, 'plan_not_for_sale', 'x'), money)).toContain('不支持余额购买')
    expect(purchaseErrorMessage(new ApiError(409, 'not_an_upgrade', 'x'), money)).toContain('无法升级')
    expect(purchaseErrorMessage(new ApiError(409, 'subscription_not_active', 'x'), money)).toContain('不是有效状态')
    expect(purchaseErrorMessage(new ApiError(404, 'not_found', 'x'), money)).toContain('不存在')
    expect(purchaseErrorMessage(new ApiError(500, 'internal', '服务器炸了'), money)).toBe('服务器炸了')
  })
})

describe('success text', () => {
  const base = {
    id: 'r1',
    price: '12',
    wallet: { userId: 'u', balance: '88', reserved: '0', available: '88', currency: 'USD', version: 2 },
    subscription: { id: 's', user: { id: 'u', displayName: 'U' }, plan: { id: 'pro', name: 'Pro' }, status: 'active', startsAt: '2026-10-01T00:00:00Z', endsAt: '2026-10-31T00:00:00Z', source: 'purchase', models: [], rules: [], createdAt: '2026-10-01T00:00:00Z' },
  } satisfies Omit<PurchaseResult, 'action'>

  it('describes each action', () => {
    expect(purchaseSuccessText({ ...base, action: 'new' }, money).title).toBe('购买成功')
    expect(purchaseSuccessText({ ...base, action: 'renew' }, money).title).toBe('续费成功')
    const up = purchaseSuccessText({ ...base, action: 'upgrade' }, money)
    expect(up.title).toBe('升级成功')
    expect(up.description).toContain('已扣款 $12，可用余额 $88')
  })
})

describe('isLongDescription', () => {
  it('collapses only very long descriptions', () => {
    expect(isLongDescription('short')).toBe(false)
    expect(isLongDescription('x'.repeat(401))).toBe(true)
    expect(isLongDescription(Array.from({ length: 11 }, () => 'a').join('\n'))).toBe(true)
  })
})

describe('parallel subscriptions', () => {
  const opts = (subs: PurchaseOptions['subscriptions']): PurchaseOptions => ({
    available: '100',
    currency: 'USD',
    plans: [option({}, { id: 'go', name: 'Go+', models: [] }), option({}, { id: 'aigo', name: 'Aigo', models: ['glm-5.3'] })],
    subscriptions: subs,
  })
  const pro = { id: 's1', planId: 'pro', planName: 'Pro', models: ['gpt-6-sol'], endsAt: '2026-11-09T00:00:00Z' }

  it('warns when a new purchase stacks next to a subscription covering the same models', () => {
    const newGo = directIntent(option({}, { id: 'go', name: 'Go+' }))!
    const subs = parallelSubscriptions(opts([pro]), newGo)
    expect(subs.map(s => s.id)).toEqual(['s1'])
    expect(parallelWarning(subs, 'Go+')).toContain('你已持有 「Pro」')
    expect(parallelWarning(subs, 'Go+')).toContain('额度叠加')
  })

  it('does not warn for other models, renewals, upgrades or older servers', () => {
    const newAigo = directIntent(option({}, { id: 'aigo', name: 'Aigo' }))!
    expect(parallelSubscriptions(opts([pro]), newAigo)).toEqual([])
    const renew = directIntent(option({ action: 'renew', renewSubscriptionId: 's9', currentEndsAt: '2026-10-20T00:00:00Z' }, { id: 'go' }))!
    expect(parallelSubscriptions(opts([pro]), renew)).toEqual([])
    const up = upgradeIntents(option({ upgrades: [upgrade('s1', 'Pro', '10')] }, { id: 'go' }))[0]!
    expect(parallelSubscriptions(opts([pro]), up)).toEqual([])
    expect(parallelSubscriptions(opts(undefined), directIntent(option({}, { id: 'go' }))!)).toEqual([])
    expect(parallelWarning([], 'Go+')).toBeNull()
  })

  it('treats an empty model list as all models', () => {
    expect(modelsOverlap([], ['a'])).toBe(true)
    expect(modelsOverlap(['a'], [])).toBe(true)
    expect(modelsOverlap(['a'], ['b'])).toBe(false)
    expect(modelsOverlap(['a', 'b'], ['b'])).toBe(true)
  })
})

describe('plan value', () => {
  const rule = (limit: string, window: QuotaWindow, extra = {}): QuotaRule =>
    ({ id: 'r', label: 'r', meter: 'charge', window, limit, models: [], modelWeights: {}, ...extra })
  const plan = (listPrice: string | null, rules: QuotaRule[]): CatalogPlan =>
    ({ id: 'p', name: 'P', description: '', listPrice, duration: '30d', models: [], rules, stackable: false })

  it('extrapolates the tightest spend rule to 30 days', () => {
    expect(planValue(plan('19', [rule('30', { kind: 'session', duration: '7d' })]))).toEqual({ monthly: 129, ratio: 6.8 })
    // A loose monthly cap does not raise the value of a tight weekly limit, and vice versa.
    expect(planValue(plan('10', [rule('40', { kind: 'session', duration: '7d' }), rule('80', { kind: 'period', every: '30d' })]))?.monthly).toBe(80)
    expect(planValue(plan('10', [rule('10', { kind: 'calendar', unit: 'day' })]))?.monthly).toBe(300)
  })

  it('needs a price and a spend rule over all models', () => {
    expect(planValue(plan(null, [rule('30', { kind: 'session', duration: '7d' })]))).toBeNull()
    expect(planValue(plan('19', [{ ...rule('30', { kind: 'session', duration: '7d' }), meter: 'requests' }]))).toBeNull()
    expect(planValue(plan('19', [rule('30', { kind: 'session', duration: '7d' }, { models: ['m'] })]))).toBeNull()
  })
})
