import { afterEach, describe, expect, it, vi } from 'vitest'
import { setUnauthorizedHandler } from './api'
import { alertsApi, notificationsApi, settingsApi } from './endpoints'

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
  return { url, method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : undefined, headers: (init?.headers ?? {}) as Record<string, string> }
}

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(() => {})
})

describe('notificationsApi', () => {
  it('lists with unread / type filters and normalises a null body', async () => {
    const m = mockFetch(null)
    const res = await notificationsApi.list({ page: 2, pageSize: 20, unread: true, type: 'wallet.balance_low,wallet.credited' })
    expect(res).toEqual({ items: [], total: 0, page: 2, pageSize: 20 })
    expect(call(m).url).toBe('/api/notifications?page=2&pageSize=20&unread=true&type=wallet.balance_low%2Cwallet.credited')
    await notificationsApi.list({ page: 1, pageSize: 20, unread: false })
    expect(call(m, 1).url).toBe('/api/notifications?page=1&pageSize=20')
  })

  it('marks read by ids or all, with the CSRF header', async () => {
    const m = mockFetch(undefined, 204)
    await notificationsApi.markRead(['a', 'b'])
    await notificationsApi.markAllRead()
    expect(call(m)).toMatchObject({ url: '/api/notifications/read', method: 'POST', body: { ids: ['a', 'b'] } })
    expect(call(m).headers['X-Requested-With']).toBe('XMLHttpRequest')
    expect(call(m, 1).body).toEqual({ all: true })
  })

  it('sends preferences, email verification, webhook test and secret to the contract paths', async () => {
    const m = mockFetch({})
    await notificationsApi.unreadCount()
    await notificationsApi.verifyEmail('me@example.com')
    await notificationsApi.confirmEmail('me@example.com', '123456')
    await notificationsApi.testWebhook()
    await notificationsApi.setWebhookSecret('s')
    expect(m.mock.calls.map((_, i) => `${call(m, i).method} ${call(m, i).url}`)).toEqual([
      'GET /api/notifications/unread-count',
      'POST /api/notifications/email/verify',
      'POST /api/notifications/email/confirm',
      'POST /api/notifications/webhook/test',
      'PUT /api/notifications/webhook/secret',
    ])
    expect(call(m, 2).body).toEqual({ address: 'me@example.com', code: '123456' })
    expect(call(m, 4).body).toEqual({ secret: 's' })
  })
})

describe('alerts & SMTP test', () => {
  it('calls the summary and smtp-test endpoints', async () => {
    const m = mockFetch({ ok: false, error: 'dial tcp: timeout' })
    expect(await settingsApi.smtpTest('a@b.co')).toEqual({ ok: false, error: 'dial tcp: timeout' })
    await alertsApi.summary()
    expect(call(m)).toMatchObject({ url: '/api/admin/settings/smtp-test', method: 'POST', body: { to: 'a@b.co' } })
    expect(call(m, 1)).toMatchObject({ url: '/api/alerts/summary', method: 'GET' })
  })
})
