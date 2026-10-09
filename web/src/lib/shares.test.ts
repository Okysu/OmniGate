import type { ChannelShare, IncomingShare } from './types'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setUnauthorizedHandler } from './api'
import { channelSharesApi } from './endpoints'
import {
  addedGroups,
  declinedShares,
  isShareStatus,
  pendingCount,
  reinvite,
  SELECTION_STATUS_LABELS,
  selectionStatus,
  SHARE_STATUS_LABELS,
  shareIndex,
  splitIncoming,
  updateIncoming,
} from './shares'

const share = (userId: string, status: ChannelShare['status']): ChannelShare =>
  ({ userId, displayName: userId.toUpperCase(), status, createdAt: '2026-10-01T00:00:00Z', respondedAt: status === 'pending' ? null : '2026-10-02T00:00:00Z' })

const incoming = (channelId: string, status: IncomingShare['status'], createdAt: string): IncomingShare => ({
  channelId,
  name: channelId,
  type: 'openai',
  channelStatus: 'enabled',
  owner: { id: 'o', displayName: 'alice' },
  models: ['m'],
  status,
  createdAt,
  respondedAt: null,
})

describe('share status (phase5 §5)', () => {
  it('labels the three statuses', () => {
    expect(SHARE_STATUS_LABELS).toEqual({ pending: '待接受', accepted: '已接受', declined: '已拒绝' })
    expect(isShareStatus('declined')).toBe(true)
    expect(isShareStatus('left')).toBe(false)
  })

  it('derives the status of each selected user in the form', () => {
    const idx = shareIndex([share('a', 'pending'), share('b', 'accepted'), share('c', 'declined'), { ...share('d', 'pending'), status: 'weird' as never }])
    expect(selectionStatus('a', idx)).toBe('pending')
    expect(selectionStatus('b', idx)).toBe('accepted')
    // A declined user selected again is re-invited on save.
    expect(selectionStatus('c', idx)).toBe('reinvite')
    // Unknown status entries are ignored: treated as a new invitation.
    expect(selectionStatus('d', idx)).toBe('new')
    expect(selectionStatus('z', idx)).toBe('new')
    expect(SELECTION_STATUS_LABELS.reinvite).toBe('保存后重新邀请')
    expect(shareIndex(undefined).size).toBe(0)
  })

  it('lists declined recipients not selected again and re-invites them', () => {
    const shares = [share('a', 'accepted'), share('c', 'declined'), share('e', 'declined')]
    const sel = { users: ['a'], groups: ['g'] }
    expect(declinedShares(shares, sel).map(s => s.userId)).toEqual(['c', 'e'])
    const next = reinvite(sel, 'c')
    expect(next).toEqual({ users: ['a', 'c'], groups: ['g'] })
    expect(declinedShares(shares, next).map(s => s.userId)).toEqual(['e'])
    expect(reinvite(next, 'c')).toBe(next)
    expect(declinedShares(null, sel)).toEqual([])
  })

  it('finds groups a non-admin would add (keeping or removing existing ones is allowed)', () => {
    expect(addedGroups(['g1', 'g2'], ['g1'])).toEqual([])
    expect(addedGroups(['g1'], ['g1', 'g1'])).toEqual([])
    expect(addedGroups(['g1'], ['g1', 'g3', 'g3'])).toEqual(['g3'])
  })

  it('splits incoming shares into pending and accepted, newest first', () => {
    const items = [
      incoming('x', 'accepted', '2026-10-01T00:00:00Z'),
      incoming('y', 'pending', '2026-10-02T00:00:00Z'),
      incoming('z', 'pending', '2026-10-03T00:00:00Z'),
    ]
    const { pending, accepted } = splitIncoming(items)
    expect(pending.map(i => i.channelId)).toEqual(['z', 'y'])
    expect(accepted.map(i => i.channelId)).toEqual(['x'])
    expect(pendingCount(items)).toBe(2)
    expect(pendingCount(null)).toBe(0)
    const accepted2 = updateIncoming(items, 'y', { ...items[1]!, status: 'accepted' })
    expect(pendingCount(accepted2)).toBe(1)
    expect(updateIncoming(items, 'x', null).map(i => i.channelId)).toEqual(['y', 'z'])
  })
})

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

describe('channelSharesApi', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    setUnauthorizedHandler(() => {})
  })

  it('lists incoming shares (null items → empty)', async () => {
    const fetchMock = vi.fn(async () => json(200, { items: null }))
    vi.stubGlobal('fetch', fetchMock)
    expect(await channelSharesApi.list()).toEqual({ items: [] })
    expect((fetchMock.mock.calls[0] as unknown as [string])[0]).toBe('/api/channel-shares')
  })

  it('posts accept / decline / leave with the CSRF header', async () => {
    const fetchMock = vi.fn(async (url: string) => (url.endsWith('/accept') ? json(200, incoming('c 1', 'accepted', 't')) : new Response(null, { status: 204 })))
    vi.stubGlobal('fetch', fetchMock)
    expect((await channelSharesApi.accept('c 1')).status).toBe('accepted')
    await channelSharesApi.decline('c 1')
    await channelSharesApi.leave('c 1')
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][]
    expect(calls.map(([u]) => u)).toEqual([
      '/api/channel-shares/c%201/accept',
      '/api/channel-shares/c%201/decline',
      '/api/channel-shares/c%201/leave',
    ])
    for (const [, init] of calls) {
      expect(init.method).toBe('POST')
      expect((init.headers as Record<string, string>)['X-Requested-With']).toBe('XMLHttpRequest')
    }
  })

  it('surfaces 409 share_state_conflict as an ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(409, { error: { code: 'share_state_conflict', message: '请改用 leave', requestId: 'r' } })))
    await expect(channelSharesApi.decline('c')).rejects.toMatchObject({ status: 409, code: 'share_state_conflict' })
  })
})
