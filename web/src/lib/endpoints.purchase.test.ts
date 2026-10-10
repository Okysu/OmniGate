import { afterEach, describe, expect, it, vi } from 'vitest'
import { setUnauthorizedHandler } from './api'
import { authApi, billingApi } from './endpoints'

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

type FetchMock = ReturnType<typeof vi.fn<(url: string, init?: RequestInit) => Promise<Response>>>
function mockFetch(body: unknown, status = 200): FetchMock {
  const m = vi.fn(async (_url: string, _init?: RequestInit) => json(status, body))
  vi.stubGlobal('fetch', m)
  return m
}
function call(m: FetchMock, i = 0) {
  const [url, init] = m.mock.calls[i]!
  return { url, method: init?.method ?? 'GET', body: init?.body ? JSON.parse(String(init.body)) : undefined }
}

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(() => {})
})

describe('billingApi purchase & referral (phase15)', () => {
  it('uses the contract paths', async () => {
    const m = mockFetch({})
    await billingApi.purchaseOptions()
    await billingApi.purchase({ planId: 'p1', fromSubscriptionId: 's1', expectedPrice: '12.5' })
    await billingApi.purchases({ page: 2, pageSize: 10 })
    await billingApi.referral()
    await billingApi.referralRebates({ page: 1, pageSize: 20 })
    expect(call(m, 0)).toMatchObject({ url: '/api/billing/purchase/options', method: 'GET' })
    expect(call(m, 1)).toMatchObject({ url: '/api/billing/purchase', method: 'POST', body: { planId: 'p1', fromSubscriptionId: 's1', expectedPrice: '12.5' } })
    expect(call(m, 2).url).toBe('/api/billing/purchases?page=2&pageSize=10')
    expect(call(m, 3).url).toBe('/api/billing/referral')
    expect(call(m, 4).url).toBe('/api/billing/referral/rebates?page=1&pageSize=20')
  })

  it('surfaces price_changed details', async () => {
    mockFetch({ error: { code: 'price_changed', message: 'price changed', requestId: 'r', details: { price: '15' } } }, 409)
    await expect(billingApi.purchase({ planId: 'p1', expectedPrice: '12' })).rejects.toMatchObject({ code: 'price_changed', details: { price: '15' } })
  })
})

describe('authApi.loginUrl', () => {
  it('appends the invite code only when present', () => {
    expect(authApi.loginUrl('github', '/console')).toBe('/api/auth/github/login?redirect=%2Fconsole')
    expect(authApi.loginUrl('github', '/console', null)).toBe('/api/auth/github/login?redirect=%2Fconsole')
    expect(authApi.loginUrl('github', '/console', 'AB12CD34')).toBe('/api/auth/github/login?redirect=%2Fconsole&invite=AB12CD34')
  })
})
