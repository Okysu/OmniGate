import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, setUnauthorizedHandler } from './api'
import { modelInfoApi, plazaApi } from './endpoints'

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(() => {})
})

describe('plazaApi', () => {
  it('normalises items and passes the currency through', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(200, { items: [{ model: 'm', tags: null, plans: null, protocols: ['openai.chat'], price: null }], currency: 'USD' })))
    const res = await plazaApi.models()
    expect(res.currency).toBe('USD')
    expect(res.items[0]).toMatchObject({ model: 'm', tags: [], plans: [], protocols: ['openai.chat'], price: null })
  })

  it('does not trigger the login redirect for the public page on 401', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    vi.stubGlobal('fetch', vi.fn(async () => json(401, { error: { code: 'unauthorized', message: '需要登录', requestId: 'r' } })))
    await expect(plazaApi.models({ publicPage: true })).rejects.toBeInstanceOf(ApiError)
    expect(handler).not.toHaveBeenCalled()
    await expect(plazaApi.models()).rejects.toMatchObject({ status: 401 })
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('normalises "mine" items (null list → empty)', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(200, { items: null, currency: null })))
    expect(await plazaApi.mine()).toEqual({ items: [], currency: null })
  })
})

describe('modelInfoApi', () => {
  it('URL-encodes the model name and sends the CSRF header on writes', async () => {
    const fetchMock = vi.fn(async () => json(200, {}))
    vi.stubGlobal('fetch', fetchMock)
    await modelInfoApi.save('org/model:v1', { displayName: '', description: '', vendor: '', tags: [], contextWindow: null, maxOutput: null, capabilities: { vision: false, tools: false, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false, completions: false }, hidden: false, sortOrder: 0, version: 2 })
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/admin/model-info/org%2Fmodel%3Av1')
    expect(init.method).toBe('PUT')
    expect((init.headers as Record<string, string>)['X-Requested-With']).toBe('XMLHttpRequest')
    expect(JSON.parse(String(init.body))).toMatchObject({ version: 2 })
  })

  it('returns an empty list for a null body', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(200, { items: null })))
    expect(await modelInfoApi.list()).toEqual({ items: [] })
  })
})
