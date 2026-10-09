import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useNotificationsStore } from './notifications'

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

beforeEach(() => setActivePinia(createPinia()))
afterEach(() => vi.unstubAllGlobals())

describe('notifications store', () => {
  it('loads the unread count and shares concurrent requests', async () => {
    const m = vi.fn(async () => json(200, { count: 12 }))
    vi.stubGlobal('fetch', m)
    const s = useNotificationsStore()
    await Promise.all([s.refresh(), s.refresh()])
    expect(m).toHaveBeenCalledTimes(1)
    expect(s.unread).toBe(12)
    s.decrement(3)
    expect(s.unread).toBe(9)
    s.decrement(20)
    expect(s.unread).toBe(0)
  })

  it('stops (available = false) when the backend has no notifications endpoint', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(404, { error: { code: 'not_found', message: 'x', requestId: 'r' } })))
    const s = useNotificationsStore()
    s.set(4)
    await s.refresh()
    expect(s.available).toBe(false)
    expect(s.unread).toBe(4)
  })
})
