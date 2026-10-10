import { afterEach, describe, expect, it, vi } from 'vitest'
import { adminApi, affinityApi, billingApi, groupsApi, logsApi, plansApi } from './endpoints'

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function lastCall(fetchMock: ReturnType<typeof vi.fn>): { url: string, init: RequestInit } {
  const call = fetchMock.mock.calls.at(-1) as [string, RequestInit]
  return { url: call[0], init: call[1] }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('adminApi (phase7 §2)', () => {
  it('fetches the user detail and posts logout / keys disable with the CSRF header', async () => {
    const fetchMock = vi.fn(async () => json(200, { revoked: 2 }))
    vi.stubGlobal('fetch', fetchMock)
    await adminApi.getUser('u/1')
    expect(lastCall(fetchMock).url).toBe('/api/admin/users/u%2F1')
    expect(await adminApi.logoutUser('u1')).toEqual({ revoked: 2 })
    expect(lastCall(fetchMock).url).toBe('/api/admin/users/u1/logout')
    expect((lastCall(fetchMock).init.headers as Record<string, string>)['X-Requested-With']).toBe('XMLHttpRequest')
    await adminApi.disableUserKeys('u1')
    expect(lastCall(fetchMock).url).toBe('/api/admin/users/u1/keys/disable')
  })

  it('sends batch bodies and normalises null lists', async () => {
    const fetchMock = vi.fn(async () => json(200, { succeeded: null, failed: [{ id: 'a', code: 'x', message: 'm' }] }))
    vi.stubGlobal('fetch', fetchMock)
    const res = await adminApi.batchUsers({ ids: ['a'], action: 'disable', reason: 'r', until: null })
    expect(res).toEqual({ succeeded: [], failed: [{ id: 'a', code: 'x', message: 'm' }] })
    const { url, init } = lastCall(fetchMock)
    expect(url).toBe('/api/admin/users/batch')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ ids: ['a'], action: 'disable', reason: 'r', until: null })
  })

  it('patches status with reason and until', async () => {
    const fetchMock = vi.fn(async () => json(200, {}))
    vi.stubGlobal('fetch', fetchMock)
    await adminApi.patchUser('u1', { status: 'disabled', disabledReason: 'r', disabledUntil: '2026-10-09T00:00:00.000Z', version: 2 })
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({ status: 'disabled', disabledReason: 'r', disabledUntil: '2026-10-09T00:00:00.000Z', version: 2 })
  })
})

describe('plansApi bulk actions (phase7 §3)', () => {
  it('adds ?dryRun=true only for previews', async () => {
    const fetchMock = vi.fn(async () => json(200, { affected: 3, subscriptions: ['a', 'b', 'c'] }))
    vi.stubGlobal('fetch', fetchMock)
    const body = { target: { planId: null, status: 'active' as const }, rules: null, includeLifetime: false, note: '' }
    expect(await plansApi.resetQuota(body, { dryRun: true })).toEqual({ affected: 3, subscriptions: ['a', 'b', 'c'] })
    expect(lastCall(fetchMock).url).toBe('/api/admin/billing/subscriptions/reset-quota?dryRun=true')
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual(body)
    await plansApi.resetQuota(body)
    expect(lastCall(fetchMock).url).toBe('/api/admin/billing/subscriptions/reset-quota')
    await plansApi.extend({ target: { ids: ['a'] }, duration: '7d', note: 'x' }, { dryRun: true })
    expect(lastCall(fetchMock).url).toBe('/api/admin/billing/subscriptions/extend?dryRun=true')
  })

  it('tolerates missing fields', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(200, {})))
    expect(await plansApi.extend({ target: { ids: ['a'] }, duration: '1h', note: '' })).toEqual({ affected: 0, subscriptions: [] })
  })
})

describe('groups & limits (phase8 §1–§2)', () => {
  it('lists, creates, patches and deletes groups', async () => {
    const fetchMock = vi.fn(async () => json(200, { items: null }))
    vi.stubGlobal('fetch', fetchMock)
    expect(await groupsApi.list()).toEqual({ items: [] })
    expect(lastCall(fetchMock).url).toBe('/api/admin/groups')
    await groupsApi.update('g/1', { isDefault: true, version: 2 })
    expect(lastCall(fetchMock).url).toBe('/api/admin/groups/g%2F1')
    expect(lastCall(fetchMock).init.method).toBe('PATCH')
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({ isDefault: true, version: 2 })
    await groupsApi.remove('g1')
    expect(lastCall(fetchMock).init.method).toBe('DELETE')
  })

  it('moves a user, filters users by group and reads my limits', async () => {
    const fetchMock = vi.fn(async () => json(200, {}))
    vi.stubGlobal('fetch', fetchMock)
    await adminApi.setUserGroup('u1', 'g2')
    expect(lastCall(fetchMock).url).toBe('/api/admin/users/u1/group')
    expect(lastCall(fetchMock).init.method).toBe('PUT')
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({ groupId: 'g2' })
    await adminApi.listUsers({ page: 1, pageSize: 20, groupId: 'g2' })
    expect(lastCall(fetchMock).url).toBe('/api/admin/users?page=1&pageSize=20&groupId=g2')
    await billingApi.limits()
    expect(lastCall(fetchMock).url).toBe('/api/billing/limits')
  })
})

describe('affinityApi (phase12 §3)', () => {
  it('reads stats and clears all or one rule', async () => {
    const fetchMock = vi.fn(async () => json(200, { cleared: 2, stats: { entries: 0, maxEntries: 100, rules: {} } }))
    vi.stubGlobal('fetch', fetchMock)
    await affinityApi.stats()
    expect(lastCall(fetchMock).url).toBe('/api/admin/affinity/stats')
    expect((await affinityApi.clear('codex cli trace')).cleared).toBe(2)
    expect(lastCall(fetchMock).url).toBe('/api/admin/affinity/clear')
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({ rule: 'codex cli trace' })
    await affinityApi.clear()
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({})
  })

  it('filters request logs by affinity outcome', async () => {
    const fetchMock = vi.fn(async () => json(200, { items: [], total: 0, page: 1, pageSize: 50 }))
    vi.stubGlobal('fetch', fetchMock)
    await logsApi.list({ page: 1, pageSize: 50, affinity: 'strict_failed' })
    expect(lastCall(fetchMock).url).toContain('affinity=strict_failed')
  })
})
