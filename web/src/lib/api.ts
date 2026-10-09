/**
 * Minimal typed fetch wrapper for the OmniGate admin API.
 *
 * - Same-origin cookie session (`credentials: 'same-origin'`).
 * - Every non-GET/HEAD request to `/api/*` carries `X-Requested-With: XMLHttpRequest`
 *   (the backend's CSRF guard rejects mutating requests without it).
 * - Non-2xx responses are parsed into a typed {@link ApiError}.
 * - A 401 from any endpoint except `GET /api/me` triggers the unauthorized handler
 *   (by default: navigate to `/login?redirect=<current>`; the app replaces this with a
 *   router-aware handler in `main.ts`).
 */

export interface ApiErrorBody {
  error: {
    code: string
    message: string
    requestId: string
    /** Field-level validation messages (422 `validation_failed`), keyed by field path. */
    details?: Record<string, unknown>
  }
}

export class ApiError extends Error {
  /** HTTP status; 0 for network failures. */
  readonly status: number
  readonly code: string
  readonly requestId: string | null
  /** Field-level messages from `error.details` (only string values are kept). */
  readonly details: Record<string, string>
  /** `error.details` as sent by the server (may hold structured values, e.g. build diagnostics). */
  readonly rawDetails: Record<string, unknown>

  constructor(status: number, code: string, message: string, requestId: string | null = null, details: Record<string, string> = {}, rawDetails: Record<string, unknown> = {}) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
    this.details = details
    this.rawDetails = rawDetails
  }

  get isUnauthorized(): boolean {
    return this.status === 401
  }

  get isForbidden(): boolean {
    return this.status === 403
  }

  get isConflict(): boolean {
    return this.status === 409
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isApiErrorBody(body: unknown): body is ApiErrorBody {
  if (!isRecord(body) || !isRecord(body.error))
    return false
  return typeof body.error.code === 'string'
}

const STATUS_MESSAGES: Record<number, string> = {
  400: '请求参数有误',
  401: '登录已失效，请重新登录',
  403: '没有权限执行此操作',
  404: '请求的资源不存在',
  409: '数据已被他人修改',
  422: '提交的数据未通过校验',
  429: '请求过于频繁，请稍后再试',
  500: '服务器内部错误',
  502: '网关错误，后端服务不可用',
  503: '服务暂不可用',
  504: '后端服务响应超时',
}

/** Build an {@link ApiError} from an HTTP status and an (already JSON-parsed, possibly null) body. */
export function parseApiError(status: number, body: unknown): ApiError {
  const fallback = STATUS_MESSAGES[status] ?? `请求失败（HTTP ${status}）`
  if (isApiErrorBody(body)) {
    const { code, message, requestId, details } = body.error
    const fields: Record<string, string> = {}
    if (isRecord(details)) {
      for (const [k, v] of Object.entries(details)) {
        if (typeof v === 'string')
          fields[k] = v
      }
    }
    return new ApiError(
      status,
      code,
      typeof message === 'string' && message !== '' ? message : fallback,
      typeof requestId === 'string' && requestId !== '' ? requestId : null,
      fields,
      isRecord(details) ? { ...details } : {},
    )
  }
  return new ApiError(status, `http_${status}`, fallback)
}

type UnauthorizedHandler = () => void

function defaultUnauthorizedHandler(): void {
  const { pathname, search, hash } = window.location
  // Public pages: the landing page (`/`) and the login page itself.
  if (pathname === '/login' || pathname === '/')
    return
  const redirect = encodeURIComponent(pathname + search + hash)
  window.location.assign(`/login?redirect=${redirect}`)
}

let unauthorizedHandler: UnauthorizedHandler = defaultUnauthorizedHandler

/** Override what happens on a 401 (the app wires this to the router + auth store). */
export function setUnauthorizedHandler(handler: UnauthorizedHandler): void {
  unauthorizedHandler = handler
}

export type QueryValue = string | number | boolean | null | undefined
export type Query = Record<string, QueryValue>

export interface RequestOptions {
  query?: Query
  body?: unknown
  signal?: AbortSignal
  /** Do not trigger the global 401 redirect for this call. */
  skipAuthRedirect?: boolean
}

/** Path that is allowed to 401 silently (used to probe the session). */
const SESSION_PROBE_PATH = '/api/me'

export function buildUrl(path: string, query?: Query): string {
  if (!query)
    return path
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '')
      continue
    params.set(key, String(value))
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

export function needsCsrfHeader(method: string, path: string): boolean {
  const m = method.toUpperCase()
  return m !== 'GET' && m !== 'HEAD' && (path === '/api' || path.startsWith('/api/'))
}

async function readJson(res: Response): Promise<unknown> {
  const text = await res.text()
  if (text === '')
    return null
  try {
    return JSON.parse(text) as unknown
  }
  catch {
    return null
  }
}

/**
 * Request headers and body for `options.body`: FormData is sent as is (the browser sets the
 * multipart boundary, so no Content-Type here); anything else is JSON-encoded.
 */
export function buildRequestInit(method: string, path: string, payload: unknown, accept = 'application/json'): { headers: Record<string, string>, body: BodyInit | undefined } {
  const headers: Record<string, string> = { Accept: accept }
  let body: BodyInit | undefined
  if (typeof FormData !== 'undefined' && payload instanceof FormData) {
    body = payload
  }
  else if (payload !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(payload)
  }
  if (needsCsrfHeader(method, path))
    headers['X-Requested-With'] = 'XMLHttpRequest'
  return { headers, body }
}

async function send(method: string, path: string, options: RequestOptions, accept?: string): Promise<Response> {
  const { headers, body } = buildRequestInit(method, path, options.body, accept)
  let res: Response
  try {
    res = await fetch(buildUrl(path, options.query), {
      method,
      headers,
      body,
      credentials: 'same-origin',
      signal: options.signal,
    })
  }
  catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError')
      throw err
    throw new ApiError(0, 'network_error', '无法连接到服务器，请检查网络或后端服务')
  }

  if (!res.ok) {
    const error = parseApiError(res.status, await readJson(res))
    if (res.status === 401 && path !== SESSION_PROBE_PATH && !options.skipAuthRedirect)
      unauthorizedHandler()
    throw error
  }
  return res
}

export async function request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
  const res = await send(method, path, options)
  if (res.status === 204)
    return undefined as T
  return (await readJson(res)) as T
}

/** Like {@link request}, but also returns the HTTP status (e.g. 201 created vs 200 renewed). */
export async function requestWithStatus<T>(method: string, path: string, options: RequestOptions = {}): Promise<{ status: number, data: T }> {
  const res = await send(method, path, options)
  return { status: res.status, data: (res.status === 204 ? undefined : await readJson(res)) as T }
}

/** Parses `filename*=UTF-8''…` / `filename="…"` from a Content-Disposition header. */
export function filenameFromDisposition(header: string | null): string | null {
  if (!header)
    return null
  const star = /filename\*=UTF-8''([^;]+)/i.exec(header)
  if (star?.[1]) {
    try {
      return decodeURIComponent(star[1].trim())
    }
    catch {
      return star[1].trim()
    }
  }
  const plain = /filename="?([^";]+)"?/i.exec(header)
  return plain?.[1]?.trim() ?? null
}

/** GET a binary resource (e.g. a ZIP export) with the same error handling as {@link request}. */
export async function requestBlob(path: string, options: Omit<RequestOptions, 'body'> = {}): Promise<{ blob: Blob, filename: string | null }> {
  const res = await send('GET', path, options, '*/*')
  return { blob: await res.blob(), filename: filenameFromDisposition(res.headers.get('Content-Disposition')) }
}

/** GET a text resource (e.g. TypeScript declarations). */
export async function requestText(path: string, options: Omit<RequestOptions, 'body'> = {}): Promise<string> {
  const res = await send('GET', path, options, 'text/plain, */*')
  return res.text()
}

export const api = {
  get: <T>(path: string, options?: Omit<RequestOptions, 'body'>) => request<T>('GET', path, options),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) => request<T>('POST', path, { ...options, body }),
  patch: <T>(path: string, body?: unknown, options?: RequestOptions) => request<T>('PATCH', path, { ...options, body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) => request<T>('PUT', path, { ...options, body }),
  delete: <T = void>(path: string, options?: RequestOptions) => request<T>('DELETE', path, options),
}

/** Field errors of a 422 response (empty object for anything else). */
export function fieldErrors(err: unknown): Record<string, string> {
  return isApiError(err) ? { ...err.details } : {}
}

/** True for 409 `version_conflict` (optimistic-lock failure). */
export function isVersionConflict(err: unknown): boolean {
  return isApiError(err) && err.code === 'version_conflict'
}

/** Human-readable message for any thrown value (for toasts / inline errors). */
export function errorMessage(err: unknown): string {
  if (isApiError(err))
    return err.requestId ? `${err.message}（请求 ID：${err.requestId}）` : err.message
  if (err instanceof Error)
    return err.message
  return '未知错误'
}
