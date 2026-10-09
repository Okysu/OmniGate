import type { Plan, QuotaRule, RuleUsage, Subscription } from './types'
import { describe, expect, it } from 'vitest'
import {
  affectedText,
  ALL_PLANS,
  bodyKey,
  buildDuration,
  buildExtendInput,
  buildResetInput,
  buildTarget,
  extendDurationError,
  extendedEnd,
  lifetimeWarning,
  noteError,
  ruleOptions,
  ruleOptionsForTarget,
  targetIsEmpty,
  targetLabel,
} from './subscriptionBulk'

function rule(p: Partial<QuotaRule>): QuotaRule {
  return { id: 'r', label: '', meter: 'requests', window: { kind: 'calendar', unit: 'day' }, limit: '1', models: [], modelWeights: {}, ...p }
}
function usage(p: Partial<QuotaRule>): RuleUsage {
  return { ...rule(p), used: '1', windowStart: null, resetsAt: null, remaining: '0', exceeded: false }
}
const r5h = rule({ id: '5h', label: '5 小时会话', window: { kind: 'session', duration: '5h' } })
const weekly = rule({ id: 'weekly', label: '每周', window: { kind: 'calendar', unit: 'week' } })
const total = rule({ id: 'total', label: '总次数', window: { kind: 'lifetime' } })

function plan(id: string, rules: QuotaRule[]): Plan {
  return { id, name: id, description: '', listPrice: null, duration: '30d', models: [], rules, stackable: false, status: 'active', subscribers: 1, version: 1, createdAt: '', updatedAt: '' }
}
function sub(id: string, rules: RuleUsage[]): Subscription {
  return { id, user: { id: 'u', displayName: 'U' }, plan: { id: 'p', name: 'P' }, status: 'active', startsAt: '', endsAt: '2026-10-10T00:00:00Z', source: 'admin', models: [], rules, createdAt: '' }
}

describe('targets', () => {
  it('builds id and plan targets (全部套餐 → null)', () => {
    expect(buildTarget('selected', ['a', 'b', 'a'], ALL_PLANS)).toEqual({ ids: ['a', 'b'] })
    expect(buildTarget('plan', ['a'], ALL_PLANS)).toEqual({ planId: null, status: 'active' })
    expect(buildTarget('plan', [], 'p1')).toEqual({ planId: 'p1', status: 'active' })
    expect(targetIsEmpty({ ids: [] })).toBe(true)
    expect(targetIsEmpty({ planId: null, status: 'active' })).toBe(false)
    expect(targetLabel({ ids: ['a', 'b'] }, x => x)).toBe('所选 2 份订阅')
    expect(targetLabel({ planId: 'p1', status: 'active' }, () => 'Pro')).toBe('「Pro」的全部有效订阅')
    expect(targetLabel({ planId: null, status: 'active' }, x => x)).toBe('全部套餐的有效订阅')
  })
})

describe('rule options', () => {
  it('unions rule ids across sources, merges labels and flags lifetime rules', () => {
    const opts = ruleOptions([[r5h, total], [rule({ id: '5h', label: '会话' }), weekly, rule({ id: 'total', label: '总次数', window: { kind: 'calendar', unit: 'month' } })]])
    expect(opts).toEqual([
      { id: '5h', label: '5 小时会话 / 会话', lifetime: false, lifetimeOnly: false },
      { id: 'total', label: '总次数', lifetime: true, lifetimeOnly: false },
      { id: 'weekly', label: '每周', lifetime: false, lifetimeOnly: false },
    ])
  })

  it('uses the selected subscriptions or the chosen plan(s)', () => {
    const plans = [plan('p1', [r5h]), plan('p2', [weekly, total])]
    expect(ruleOptionsForTarget('plan', [], plans, ALL_PLANS).map(o => o.id)).toEqual(['5h', 'weekly', 'total'])
    expect(ruleOptionsForTarget('plan', [], plans, 'p2').map(o => o.id)).toEqual(['weekly', 'total'])
    expect(ruleOptionsForTarget('selected', [sub('s', [usage({ id: 'x', label: 'X' })])], plans, 'p2').map(o => o.id)).toEqual(['x'])
  })

  it('warns when only lifetime rules would be reset without includeLifetime', () => {
    const opts = ruleOptions([[r5h, total]])
    expect(lifetimeWarning(opts, ['total'], false)).toMatch(/总量/)
    expect(lifetimeWarning(opts, ['total'], true)).toBeNull()
    expect(lifetimeWarning(opts, null, false)).toBeNull()
    expect(lifetimeWarning(ruleOptions([[total]]), null, false)).toMatch(/总量/)
  })
})

describe('bodies', () => {
  it('builds reset and extend bodies (rules null = all, trimmed note)', () => {
    expect(buildResetInput({ ids: ['a'] }, null, false, ' 补偿 ')).toEqual({ target: { ids: ['a'] }, rules: null, includeLifetime: false, note: '补偿' })
    expect(buildResetInput({ planId: null, status: 'active' }, ['5h', '5h'], true, '')).toEqual({ target: { planId: null, status: 'active' }, rules: ['5h'], includeLifetime: true, note: '' })
    expect(buildExtendInput({ ids: ['a'] }, '7d', ' x ')).toEqual({ target: { ids: ['a'] }, duration: '7d', note: 'x' })
    expect(bodyKey({ a: 1 })).toBe('{"a":1}')
  })

  it('validates the note length', () => {
    expect(noteError('x'.repeat(200))).toBeNull()
    expect(noteError('x'.repeat(201))).toBe('最多 200 个字符')
  })
})

describe('durations', () => {
  it('builds and validates 1h–366d', () => {
    expect(buildDuration('7', 'd')).toBe('7d')
    expect(buildDuration(' 12 ', 'h')).toBe('12h')
    expect(buildDuration('0', 'd')).toBeNull()
    expect(buildDuration('1.5', 'd')).toBeNull()
    expect(extendDurationError('1', 'h')).toBeNull()
    expect(extendDurationError('366', 'd')).toBeNull()
    expect(extendDurationError('367', 'd')).toMatch(/366/)
    expect(extendDurationError('', 'd')).toBe('请输入正整数')
  })

  it('computes the new end time', () => {
    expect(extendedEnd('2026-10-10T00:00:00Z', '2d')).toBe('2026-10-12T00:00:00.000Z')
    expect(extendedEnd('2026-10-10T00:00:00Z', '5h')).toBe('2026-10-10T05:00:00.000Z')
    expect(extendedEnd('bad', '5h')).toBeNull()
  })

  it('formats the dry-run count', () => {
    expect(affectedText(12)).toBe('将影响 12 份订阅')
    expect(affectedText(0)).toBe('没有符合条件的有效订阅')
  })
})
