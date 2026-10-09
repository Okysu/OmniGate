import { describe, expect, it } from 'vitest'
import { DEFAULT_RETRY_ON, isDefaultRetryOn, normalizeRetryOn, RETRY_ON, RETRY_ON_LABELS, retrySummary } from './retry'

describe('retry classes', () => {
  it('lists the seven status-code based classes; default excludes client_error', () => {
    expect(RETRY_ON).toEqual(['rate_limit', 'server_error', 'timeout', 'network', 'auth_error', 'not_found', 'client_error'])
    expect(DEFAULT_RETRY_ON).toEqual(['rate_limit', 'server_error', 'timeout', 'network', 'auth_error', 'not_found'])
    expect(RETRY_ON_LABELS.auth_error).toBe('渠道凭据被拒（401 / 403）')
    expect(RETRY_ON_LABELS.not_found).toBe('模型不存在（404）')
  })

  it('normalises to known classes in canonical order', () => {
    expect(normalizeRetryOn(['client_error', 'bogus', 'timeout', 'timeout'])).toEqual(['timeout', 'client_error'])
    expect(normalizeRetryOn(null)).toEqual([])
    expect(isDefaultRetryOn([...DEFAULT_RETRY_ON].reverse())).toBe(true)
    expect(isDefaultRetryOn(RETRY_ON)).toBe(false)
  })

  it('summarises a selection', () => {
    expect(retrySummary([])).toBe('不重试')
    expect(retrySummary(RETRY_ON)).toBe('全部失败类型')
    expect(retrySummary(DEFAULT_RETRY_ON)).toBe('默认条件（除其他 4xx）')
    expect(retrySummary(['not_found', 'rate_limit'])).toBe('限流（429）、模型不存在（404）')
  })
})
