import { describe, expect, it } from 'vitest'
import { toLocalInput } from './timeRange'
import {
  batchResultText,
  buildBatchInput,
  disabledBadge,
  disabledDetails,
  disabledNotice,
  disablePatch,
  enablePatch,
  summarizeBatch,
  validateDisableForm,
} from './userAdmin'

const NOW = new Date('2026-10-08T12:00:00Z').getTime()
const future = toLocalInput(new Date(NOW + 86_400_000))
const past = toLocalInput(new Date(NOW - 3_600_000))

describe('validateDisableForm', () => {
  it('requires a reason of at most 200 characters', () => {
    expect(validateDisableForm({ reason: '  ', until: '' }, NOW)).toEqual({ disabledReason: expect.stringContaining('停用原因') })
    expect(validateDisableForm({ reason: '违规'.repeat(100), until: '' }, NOW)).toEqual({})
    expect(validateDisableForm({ reason: `${'违规'.repeat(100)}x`, until: '' }, NOW).disabledReason).toBe('最多 200 个字符')
  })

  it('accepts an empty until (permanent) and rejects past / invalid times', () => {
    expect(validateDisableForm({ reason: 'x', until: future }, NOW)).toEqual({})
    expect(validateDisableForm({ reason: 'x', until: past }, NOW).disabledUntil).toBe('到期时间必须晚于现在')
    expect(validateDisableForm({ reason: 'x', until: 'nope' }, NOW).disabledUntil).toBe('无效的时间')
  })
})

describe('patches and batch bodies', () => {
  it('builds the disable PATCH with a trimmed reason and ISO until (null = permanent)', () => {
    expect(disablePatch({ version: 3 }, { reason: ' 违规 ', until: '' })).toEqual({ status: 'disabled', disabledReason: '违规', disabledUntil: null, version: 3 })
    const p = disablePatch({ version: 3 }, { reason: 'x', until: future })
    expect(p.disabledUntil).toBe(new Date(future).toISOString())
    expect(enablePatch({ version: 4 })).toEqual({ status: 'active', version: 4 })
  })

  it('sends reason / until only for disable and de-duplicates ids', () => {
    expect(buildBatchInput(['a', 'b', 'a'], 'logout', { reason: 'x', until: '' })).toEqual({ ids: ['a', 'b'], action: 'logout' })
    expect(buildBatchInput(['a'], 'enable')).toEqual({ ids: ['a'], action: 'enable' })
    expect(buildBatchInput(['a'], 'disable', { reason: ' r ', until: '' })).toEqual({ ids: ['a'], action: 'disable', reason: 'r', until: null })
  })
})

describe('batch results', () => {
  it('maps failures to names and friendly messages', () => {
    const s = summarizeBatch({
      succeeded: ['a', 'b'],
      failed: [
        { id: 'c', code: 'cannot_disable_self', message: '' },
        { id: 'd0000000-1111', code: 'last_admin', message: '最后一位系统管理员' },
      ],
    }, new Map([['c', '我自己']]))
    expect(s.succeeded).toBe(2)
    expect(s.failed).toEqual([
      { id: 'c', name: '我自己', message: '不能停用自己' },
      { id: 'd0000000-1111', name: 'd0000000…', message: '最后一位系统管理员' },
    ])
    expect(batchResultText('disable', s)).toBe('已停用 2 位用户，2 位失败')
    expect(batchResultText('logout', { succeeded: 3, failed: [] })).toBe('已强制下线 3 位用户')
  })
})

describe('status details', () => {
  it('describes reason and until for disabled users only', () => {
    expect(disabledDetails({ status: 'active', disabledReason: 'x', disabledUntil: null })).toBeNull()
    expect(disabledDetails({ status: 'disabled', disabledReason: '违规', disabledUntil: null })).toBe('原因：违规\n永久停用（需管理员手动启用）')
    expect(disabledDetails({ status: 'disabled', disabledReason: null, disabledUntil: '2026-10-09T00:00:00Z' })).toMatch(/^到期自动启用：/)
    expect(disabledBadge({ status: 'disabled', disabledUntil: null })).toBe('已停用')
    expect(disabledBadge({ status: 'active', disabledUntil: null })).toBeNull()
  })
})

describe('disabledNotice (login page)', () => {
  it('only applies to account_disabled and ignores past / invalid until', () => {
    expect(disabledNotice({ error: 'not_allowed' }, NOW)).toBeNull()
    expect(disabledNotice({ error: 'account_disabled' }, NOW)).toEqual({ reason: null, until: null })
    expect(disabledNotice({ error: ['account_disabled'], reason: ' 违规 ', until: 'bad' }, NOW)).toEqual({ reason: '违规', until: null })
    expect(disabledNotice({ error: 'account_disabled', until: '2026-10-01T00:00:00Z' }, NOW)?.until).toBeNull()
    expect(disabledNotice({ error: 'account_disabled', until: '2026-10-20T00:00:00Z' }, NOW)?.until).toBeTruthy()
  })
})

describe('set_group batch (phase8 §1.3)', () => {
  it('adds groupId only for set_group and words the result', () => {
    expect(buildBatchInput(['a', 'a', 'b'], 'set_group', undefined, 'g1')).toEqual({ ids: ['a', 'b'], action: 'set_group', groupId: 'g1' })
    expect(buildBatchInput(['a'], 'logout', undefined, 'g1')).toEqual({ ids: ['a'], action: 'logout' })
    expect(batchResultText('set_group', { succeeded: 3, failed: [] })).toBe('已移动 3 位用户的分组')
  })
})
