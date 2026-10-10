import type { ResetCard, RuleUsage } from './types'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { adminResetCardsApi, resetCardsApi } from './endpoints'
import {
  buildCardTarget,
  buildIssueInput,
  cardExpiryText,
  cardHistory,
  cardMatchesWindow,
  emptyIssueForm,
  formatClock,
  groupUsableCards,
  issueFormErrors,
  issueKey,
  planRestrictionText,
  recipientsText,
  ruleChange,
  targetSummary,
} from './resetCards'
import { toLocalInput } from './timeRange'

const money = (v: string) => `$${Number(v).toFixed(3)}`

function card(p: Partial<ResetCard>): ResetCard {
  return {
    id: 'c',
    batchId: 'b1',
    user: { id: 'u', displayName: 'U' },
    kind: '5h',
    status: 'available',
    expiresAt: null,
    plans: [],
    note: '',
    createdAt: '2026-10-01T00:00:00Z',
    usedAt: null,
    subscription: null,
    revokedAt: null,
    ...p,
  }
}

function rule(p: Partial<RuleUsage>): RuleUsage {
  return {
    id: '5h',
    label: '5 小时',
    meter: 'charge',
    window: { kind: 'session', duration: '5h' },
    limit: '30',
    models: [],
    modelWeights: {},
    used: '18.2',
    windowStart: null,
    resetsAt: null,
    remaining: '11.8',
    exceeded: false,
    ...p,
  }
}

describe('cardMatchesWindow', () => {
  it('matches session / rolling windows by length only', () => {
    expect(cardMatchesWindow('5h', { kind: 'session', duration: '5h' })).toBe(true)
    expect(cardMatchesWindow('5h', { kind: 'rolling', duration: '300m' })).toBe(true)
    expect(cardMatchesWindow('5h', { kind: 'session', duration: '7d' })).toBe(false)
    expect(cardMatchesWindow('weekly', { kind: 'rolling', duration: '168h' })).toBe(true)
    expect(cardMatchesWindow('weekly', { kind: 'calendar', unit: 'week' })).toBe(false)
    expect(cardMatchesWindow('weekly', { kind: 'period', every: '7d' })).toBe(false)
    expect(cardMatchesWindow('both', { kind: 'session', duration: '7d' })).toBe(true)
    expect(cardMatchesWindow('both', { kind: 'session', duration: '6h' })).toBe(false)
  })
})

describe('the user\'s cards', () => {
  it('groups usable cards by kind and batch, soonest to expire first', () => {
    const items = [
      card({ id: '1', kind: 'weekly', batchId: 'b1' }),
      card({ id: '2', kind: '5h', batchId: 'b2', expiresAt: '2026-10-20T00:00:00Z', note: '福利', plans: [{ id: 'p', name: 'Pro' }] }),
      card({ id: '3', kind: '5h', batchId: 'b3', expiresAt: '2026-10-12T00:00:00Z' }),
      card({ id: '4', kind: '5h', batchId: 'b2', expiresAt: '2026-10-20T00:00:00Z' }),
      card({ id: '5', kind: '5h', status: 'used', batchId: 'b3' }),
      card({ id: '6', kind: 'both', status: 'expired' }),
    ]
    const groups = groupUsableCards(items)
    expect(groups.map(g => [g.kind, g.count])).toEqual([['5h', 3], ['weekly', 1]])
    const five = groups[0]!
    expect(five.lots.map(l => [l.batchId, l.cards.map(c => c.id)])).toEqual([['b3', ['3']], ['b2', ['2', '4']]])
    expect(five.soonestExpiry).toBe('2026-10-12T00:00:00Z')
    expect(five.lots[1]!.note).toBe('福利')
    expect(groups[1]!.soonestExpiry).toBeNull()
    expect(cardHistory(items).map(c => c.id)).toEqual(['5', '6'])
  })

  it('describes expiry and plan restrictions', () => {
    const now = new Date('2026-10-10T00:00:00Z').getTime()
    expect(cardExpiryText(null, now)).toBe('长期有效')
    expect(cardExpiryText('2026-10-13T00:00:00Z', now)).toBe('剩余 3 天')
    expect(planRestrictionText([])).toBe('')
    expect(planRestrictionText([{ name: 'Pro' }, { name: 'Max' }])).toBe('仅限 Pro、Max')
  })
})

describe('ruleChange', () => {
  const now = new Date(2026, 9, 10, 9, 35).getTime() // local time

  it('restarts a session window at now', () => {
    const c = ruleChange(rule({ windowStart: new Date(2026, 9, 10, 9, 35 - 300).toISOString(), resetsAt: new Date(2026, 9, 10, 14, 35).toISOString() }), now, money)
    expect(c.title).toBe('5 小时')
    expect(c.before).toBe('$18.200 / $30.000，14:35 刷新')
    expect(c.after).toBe('$0.000 / $30.000，下次刷新 14:35（5 小时后）')
    expect(c.idle).toBe(false)
  })

  it('shows dates for other days and unstarted sessions', () => {
    const weekly = rule({ id: 'weekly', label: '', meter: 'requests', window: { kind: 'session', duration: '7d' }, used: '0', limit: '100', windowStart: null })
    const c = ruleChange(weekly, now, money)
    expect(c.title).toBe('7 天会话')
    expect(c.before).toBe('0 / 100，尚未开始')
    expect(c.after).toBe('0 / 100，下次刷新 10-17 09:35（7 天后）')
    expect(c.idle).toBe(true)
  })

  it('clears a rolling window (no single refresh time)', () => {
    const roll = rule({ window: { kind: 'rolling', duration: '5h' }, resetsAt: new Date(2026, 9, 10, 11, 0).toISOString() })
    expect(ruleChange(roll, now, money)).toMatchObject({ before: '$18.200 / $30.000，11:00 起逐步释放', after: '$0.000 / $30.000，窗口清空' })
    expect(ruleChange({ ...roll, used: '0', resetsAt: null }, now, money).before).toBe('$0.000 / $30.000，窗口内暂无用量')
  })

  it('formats clock times relative to the day of now', () => {
    expect(formatClock(new Date(2026, 9, 10, 23, 5).toISOString(), now)).toBe('23:05')
    expect(formatClock(new Date(2026, 9, 11, 0, 5).getTime(), now)).toBe('10-11 00:05')
    expect(formatClock('garbage', now)).toBe('garbage')
  })
})

describe('issue form', () => {
  it('builds targets only when complete', () => {
    const f = emptyIssueForm()
    expect(buildCardTarget(f)).toBeNull()
    expect(buildCardTarget({ ...f, userIds: ['a', 'b', 'a'] })).toEqual({ type: 'users', userIds: ['a', 'b'] })
    expect(buildCardTarget({ ...f, targetType: 'group' })).toBeNull()
    expect(buildCardTarget({ ...f, targetType: 'group', groupId: 'g' })).toEqual({ type: 'group', groupId: 'g' })
    expect(buildCardTarget({ ...f, targetType: 'plan', planId: 'p' })).toEqual({ type: 'plan', planId: 'p' })
    expect(buildCardTarget({ ...f, targetType: 'all' })).toEqual({ type: 'all' })
  })

  it('builds the request body', () => {
    const expiresAt = new Date(Date.now() + 86_400_000)
    const f = { ...emptyIssueForm(), kind: 'both' as const, quantity: '3', targetType: 'all' as const, planIds: ['p', 'p'], note: ' 福利 ', expiresAt: toLocalInput(expiresAt) }
    const body = buildIssueInput(f)!
    expect(body).toMatchObject({ kind: 'both', quantity: 3, planIds: ['p'], note: '福利', target: { type: 'all' } })
    expect(Math.abs(new Date(body.expiresAt!).getTime() - expiresAt.getTime())).toBeLessThan(60_000)
    expect(buildIssueInput({ ...f, expiresAt: '' })!.expiresAt).toBeNull()
    expect(buildIssueInput({ ...f, quantity: 101 })).toBeNull()
    expect(buildIssueInput({ ...f, quantity: '1.5' })).toBeNull()
    // The note does not change the recipients: same dry-run key.
    expect(issueKey(body)).toBe(issueKey({ ...body, note: 'other' }))
    expect(issueKey(body)).not.toBe(issueKey({ ...body, quantity: 2 }))
  })

  it('validates fields', () => {
    const f = emptyIssueForm()
    expect(issueFormErrors(f)).toEqual({ target: '请选择至少一位用户' })
    expect(issueFormErrors({ ...f, targetType: 'plan', quantity: 0, note: 'x'.repeat(201), expiresAt: toLocalInput(new Date(Date.now() - 3_600_000)) }))
      .toEqual({ quantity: '每人 1–100 张', target: '请选择套餐', note: '最多 200 个字符', expiresAt: '必须晚于当前时间' })
    expect(issueFormErrors({ ...f, targetType: 'users', userIds: Array.from({ length: 1001 }, (_, i) => String(i)) }).target).toBe('最多 1000 位用户')
    expect(issueFormErrors({ ...f, targetType: 'all' })).toEqual({})
  })

  it('describes recipients and batch targets', () => {
    expect(recipientsText(0, 0)).toBe('没有符合条件的用户')
    expect(recipientsText(12, 12)).toBe('将发放给 12 位用户')
    expect(recipientsText(12, 24)).toBe('将发放给 12 位用户（共 24 张）')
    expect(targetSummary({ type: 'users', userIds: ['a', 'b'] })).toBe('指定 2 位用户')
    expect(targetSummary({ type: 'group', groupId: 'g', groupName: '默认' })).toBe('用户组「默认」')
    expect(targetSummary({ type: 'plan', planId: '0123456789', planName: '' })).toBe('「01234567」的有效订阅用户')
    expect(targetSummary({ type: 'all' })).toBe('全部用户')
  })
})

describe('reset card endpoints', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function stub(body: unknown) {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    return (): { url: string, init: RequestInit } => {
      const call = fetchMock.mock.calls.at(-1) as unknown as [string, RequestInit]
      return { url: call[0], init: call[1] }
    }
  }

  it('calls the user endpoints', async () => {
    const last = stub({})
    await resetCardsApi.mine()
    expect(last().url).toBe('/api/billing/reset-cards')
    await resetCardsApi.preview('c/1')
    expect(last().url).toBe('/api/billing/reset-cards/c%2F1/preview')
    await resetCardsApi.use('c1', 's1')
    expect(last().url).toBe('/api/billing/reset-cards/c1/use')
    expect(last().init.method).toBe('POST')
    expect(JSON.parse(String(last().init.body))).toEqual({ subscriptionId: 's1' })
  })

  it('adds ?dryRun=true only for issue previews', async () => {
    const last = stub({ recipients: 2, cards: 4, batch: null })
    const body = { kind: '5h' as const, quantity: 2, expiresAt: null, planIds: [], note: '', target: { type: 'all' as const } }
    expect(await adminResetCardsApi.issue(body, { dryRun: true })).toEqual({ recipients: 2, cards: 4, batch: null })
    expect(last().url).toBe('/api/admin/billing/reset-cards/batches?dryRun=true')
    expect(JSON.parse(String(last().init.body))).toEqual(body)
    await adminResetCardsApi.issue(body)
    expect(last().url).toBe('/api/admin/billing/reset-cards/batches')
    await adminResetCardsApi.revoke('b1')
    expect(last().url).toBe('/api/admin/billing/reset-cards/batches/b1/revoke')
    await adminResetCardsApi.listCards({ page: 1, pageSize: 20, userId: 'u1' })
    expect(last().url).toBe('/api/admin/billing/reset-cards?page=1&pageSize=20&userId=u1')
  })
})
