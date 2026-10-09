// Retry classes (status-code based) shared by route rules (`retry.retryOn`) and the
// system setting `gateway.retryOn` (default for requests no rule matches).
import type { RetryOn } from './types'

/** Every class, in canonical (display / wire) order. */
export const RETRY_ON: RetryOn[] = ['rate_limit', 'server_error', 'timeout', 'network', 'auth_error', 'not_found', 'client_error']

/** Default for new rules and the system setting: everything except `client_error`. */
export const DEFAULT_RETRY_ON: RetryOn[] = RETRY_ON.filter(r => r !== 'client_error')

export const RETRY_ON_LABELS: Record<RetryOn, string> = {
  rate_limit: '限流（429）',
  server_error: '上游 5xx / 响应异常',
  timeout: '超时（408 / 超时）',
  network: '网络错误',
  auth_error: '渠道凭据被拒（401 / 403）',
  not_found: '模型不存在（404）',
  client_error: '其他 4xx（400、402、409、413、422 …）',
}

/** Shown under the `client_error` checkbox. */
export const CLIENT_ERROR_HINT = '上游返回的其他 4xx 通常表示请求本身有误，换渠道也会失败；如果你的上游会用 400 表示模型不存在或余额不足，可以勾选。失败的尝试不计费。'

export function isRetryOn(v: unknown): v is RetryOn {
  return typeof v === 'string' && (RETRY_ON as string[]).includes(v)
}

/** Known classes only, de-duplicated, in canonical order. */
export function normalizeRetryOn(list: readonly unknown[] | null | undefined): RetryOn[] {
  const set = new Set((list ?? []).filter(isRetryOn))
  return RETRY_ON.filter(r => set.has(r))
}

/** Same set as the default (order-insensitive). */
export function isDefaultRetryOn(list: readonly unknown[]): boolean {
  const n = normalizeRetryOn(list)
  return n.length === DEFAULT_RETRY_ON.length && n.every((r, i) => r === DEFAULT_RETRY_ON[i])
}

/** Short summary: "不重试" / "全部失败类型" / "默认条件" / labels. */
export function retrySummary(list: readonly unknown[]): string {
  const n = normalizeRetryOn(list)
  if (n.length === 0)
    return '不重试'
  if (n.length === RETRY_ON.length)
    return '全部失败类型'
  if (isDefaultRetryOn(n))
    return '默认条件（除其他 4xx）'
  return n.map(r => RETRY_ON_LABELS[r]).join('、')
}
