import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api, buildRequestInit, buildUrl, errorMessage, filenameFromDisposition, needsCsrfHeader, parseApiError, setUnauthorizedHandler } from './api'

function jsonResponse(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('parseApiError', () => {
  it('parses the backend error envelope', () => {
    const err = parseApiError(409, { error: { code: 'version_conflict', message: 'stale', requestId: 'req_1' } })
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(409)
    expect(err.code).toBe('version_conflict')
    expect(err.message).toBe('stale')
    expect(err.requestId).toBe('req_1')
    expect(err.isConflict).toBe(true)
  })

  it('falls back to a status-based message when the body is not the envelope', () => {
    const err = parseApiError(502, '<html>bad gateway</html>')
    expect(err.code).toBe('http_502')
    expect(err.message).toBe('网关错误，后端服务不可用')
    expect(err.requestId).toBeNull()
  })

  it('uses a fallback message when the envelope message is empty', () => {
    const err = parseApiError(403, { error: { code: 'forbidden', message: '', requestId: '' } })
    expect(err.code).toBe('forbidden')
    expect(err.message).toBe('没有权限执行此操作')
    expect(err.requestId).toBeNull()
    expect(err.isForbidden).toBe(true)
  })

  it('handles unknown statuses and null bodies', () => {
    const err = parseApiError(418, null)
    expect(err.code).toBe('http_418')
    expect(err.message).toContain('418')
  })
})

describe('helpers', () => {
  it('buildUrl drops empty params', () => {
    expect(buildUrl('/api/admin/users', { page: 1, pageSize: 20, q: '', sort: undefined })).toBe('/api/admin/users?page=1&pageSize=20')
    expect(buildUrl('/api/x')).toBe('/api/x')
  })

  it('needsCsrfHeader only for mutating /api requests', () => {
    expect(needsCsrfHeader('GET', '/api/me')).toBe(false)
    expect(needsCsrfHeader('head', '/api/me')).toBe(false)
    expect(needsCsrfHeader('POST', '/api/auth/logout')).toBe(true)
    expect(needsCsrfHeader('delete', '/api/me/sessions/1')).toBe(true)
    expect(needsCsrfHeader('POST', '/v1/chat/completions')).toBe(false)
  })

  it('errorMessage includes the request id', () => {
    expect(errorMessage(new ApiError(500, 'internal', '出错了', 'req_9'))).toBe('出错了（请求 ID：req_9）')
    expect(errorMessage(new Error('boom'))).toBe('boom')
    expect(errorMessage(42)).toBe('未知错误')
  })
})

describe('request', () => {
  const fetchMock = vi.fn<typeof fetch>()
  const onUnauthorized = vi.fn()

  beforeEach(() => {
    vi.stubGlobal('fetch', fetchMock)
    setUnauthorizedHandler(onUnauthorized)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    fetchMock.mockReset()
    onUnauthorized.mockReset()
  })

  it('sends X-Requested-With and same-origin credentials on mutations', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(api.post('/api/auth/logout')).resolves.toBeUndefined()
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/auth/logout')
    expect(init?.credentials).toBe('same-origin')
    expect((init?.headers as Record<string, string>)['X-Requested-With']).toBe('XMLHttpRequest')
  })

  it('does not send X-Requested-With on GET', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(200, { ok: true }))
    await expect(api.get('/api/system/info')).resolves.toEqual({ ok: true })
    const init = fetchMock.mock.calls[0]![1]
    expect((init?.headers as Record<string, string>)['X-Requested-With']).toBeUndefined()
  })

  it('throws a typed ApiError and triggers the 401 handler', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(401, { error: { code: 'unauthenticated', message: 'login required', requestId: 'r1' } }))
    const err = await api.get('/api/me/sessions').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe('unauthenticated')
    expect(onUnauthorized).toHaveBeenCalledOnce()
  })

  it('does not redirect on 401 from the /api/me probe', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(401, { error: { code: 'unauthenticated', message: 'x', requestId: 'r' } }))
    await expect(api.get('/api/me')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('maps network failures to ApiError(status 0)', async () => {
    fetchMock.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const err = await api.get('/api/system/info').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(0)
    expect((err as ApiError).code).toBe('network_error')
  })
})

describe('buildRequestInit', () => {
  it('JSON-encodes plain bodies and adds the CSRF header to mutating /api calls', () => {
    const { headers, body } = buildRequestInit('POST', '/api/plugins', { id: 'a.b' })
    expect(headers['Content-Type']).toBe('application/json')
    expect(headers['X-Requested-With']).toBe('XMLHttpRequest')
    expect(body).toBe('{"id":"a.b"}')
  })

  it('sends FormData untouched without a JSON content type (multipart boundary is set by the browser)', () => {
    const form = new FormData()
    form.append('file', new Blob(['zip']), 'p.zip')
    const { headers, body } = buildRequestInit('POST', '/api/plugins/import', form)
    expect(body).toBe(form)
    expect(headers['Content-Type']).toBeUndefined()
    expect(headers['X-Requested-With']).toBe('XMLHttpRequest')
  })

  it('omits the CSRF header for GET', () => {
    expect(buildRequestInit('GET', '/api/plugins', undefined).headers['X-Requested-With']).toBeUndefined()
  })
})

describe('filenameFromDisposition', () => {
  it('prefers the RFC 5987 filename*', () => {
    expect(filenameFromDisposition(`attachment; filename*=UTF-8''community.deepseek-1.0.0.zip`)).toBe('community.deepseek-1.0.0.zip')
    expect(filenameFromDisposition(`attachment; filename*=UTF-8''%E6%8F%92%E4%BB%B6.zip`)).toBe('插件.zip')
  })
  it('falls back to filename=', () => {
    expect(filenameFromDisposition('attachment; filename="a.zip"')).toBe('a.zip')
    expect(filenameFromDisposition(null)).toBeNull()
  })
})

describe('parseApiError rawDetails', () => {
  it('keeps structured details (e.g. build diagnostics)', () => {
    const err = parseApiError(422, { error: { code: 'plugin_build_failed', message: 'x', requestId: 'r', details: { diagnostics: [{ file: 'a', line: 1 }] } } })
    expect(err.details).toEqual({})
    expect(err.rawDetails.diagnostics).toEqual([{ file: 'a', line: 1 }])
  })
})
