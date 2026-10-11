import type { Plan } from './types'
import { describe, expect, it } from 'vitest'
import {
  buildPlanPayload,
  emptyPlanForm,
  emptyRuleForm,
  formFromPlan,
  isValidTimezone,
  presetRules,
  ruleErrorKey,
  ruleFromForm,
  suggestRuleId,
  validatePlanForm,
} from './planForm'

const plan: Plan = {
  id: 'p1',
  name: 'Pro',
  description: 'desc',
  listPrice: '20',
  duration: '30d',
  models: ['gpt-x'],
  rules: [
    { id: '5h', label: '5 小时', meter: 'requests', window: { kind: 'session', duration: '5h' }, limit: '200', models: [], modelWeights: { 'gpt-x': '2.5' } },
    { id: 'weekly', label: '', meter: 'tokens.total', window: { kind: 'calendar', unit: 'week', timezone: 'Asia/Shanghai' }, limit: '5000000', models: ['gpt-x'], modelWeights: {} },
  ],
  stackable: true,
  status: 'active',
  subscribers: 0,
  version: 4,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
}

describe('plan form ↔ payload', () => {
  it('round-trips a plan', () => {
    const body = buildPlanPayload(formFromPlan(plan))
    expect(body).toEqual({
      name: 'Pro',
      description: 'desc',
      listPrice: '20',
      duration: '30d',
      models: ['gpt-x'],
      rules: plan.rules,
      stackable: true,
      group: '',
    })
  })

  it('drops the legacy onExceed field (the server rejects unknown rule fields)', () => {
    const legacy = { ...plan, rules: plan.rules.map(r => ({ ...r, onExceed: 'overflow_to_wallet' })) }
    const body = buildPlanPayload(formFromPlan(legacy))
    expect(body.rules).toEqual(plan.rules)
    for (const r of body.rules)
      expect(r).not.toHaveProperty('onExceed')
  })

  it('maps blank list price to null and trims fields', () => {
    const f = emptyPlanForm()
    f.name = '  Basic '
    f.durationN = '7'
    f.durationUnit = 'd'
    f.rules = [emptyRuleForm([], 'UTC')]
    f.rules[0]!.limit = ' 100 '
    f.rules[0]!.weights = [{ model: ' m1 ', weight: '0.5' }, { model: '', weight: '' }]
    const body = buildPlanPayload(f)
    expect(body.name).toBe('Basic')
    expect(body.listPrice).toBeNull()
    expect(body.duration).toBe('7d')
    expect(body.rules[0]).toEqual({
      id: 'daily',
      label: '',
      meter: 'requests',
      window: { kind: 'calendar', unit: 'day', timezone: 'UTC' },
      limit: '100',
      models: [],
      modelWeights: { m1: '0.5' },
    })
  })

  it('only sends the fields of the selected window kind', () => {
    const r = emptyRuleForm([], 'UTC')
    r.windowKind = 'period'
    expect(ruleFromForm(r).window).toEqual({ kind: 'period', every: '30d' })
    r.windowKind = 'rolling'
    expect(ruleFromForm(r).window).toEqual({ kind: 'rolling', duration: '5h' })
    r.windowKind = 'lifetime'
    expect(ruleFromForm(r).window).toEqual({ kind: 'lifetime' })
  })
})

describe('suggestRuleId', () => {
  it('derives ids from the window and keeps them unique', () => {
    const r = emptyRuleForm([], 'UTC')
    r.windowKind = 'session'
    expect(suggestRuleId(r)).toBe('5h')
    r.windowKind = 'calendar'
    r.unit = 'week'
    expect(suggestRuleId(r, ['weekly'])).toBe('weekly-2')
    expect(suggestRuleId(r, ['weekly', 'weekly-2'])).toBe('weekly-3')
    r.windowKind = 'period'
    expect(suggestRuleId(r)).toBe('every-30d')
    r.windowKind = 'lifetime'
    expect(suggestRuleId(r)).toBe('total')
  })
})

describe('validatePlanForm', () => {
  it('accepts a valid form', () => {
    const f = formFromPlan(plan)
    expect(validatePlanForm(f)).toEqual({})
  })

  it('reports errors under server detail keys', () => {
    const f = formFromPlan(plan)
    f.name = ''
    f.listPrice = '-1'
    f.durationN = 30
    f.durationUnit = 'm'
    f.rules[0]!.id = 'Bad Id'
    f.rules[0]!.durationN = 2
    f.rules[0]!.durationUnit = 'm'
    f.rules[0]!.limit = '1.5'
    f.rules[0]!.weights = [{ model: 'gpt-x', weight: '1001' }]
    f.rules[1]!.timezone = 'Mars/Base'
    f.rules[1]!.meter = 'charge'
    f.rules[1]!.limit = '0'
    const e = validatePlanForm(f)
    expect(Object.keys(e).sort()).toEqual([
      'duration',
      'listPrice',
      'name',
      'rules[0].id',
      'rules[0].limit',
      'rules[0].modelWeights.gpt-x',
      'rules[0].window.duration',
      'rules[1].limit',
      'rules[1].window.timezone',
    ])
  })

  it('rejects duplicate ids and empty rule lists', () => {
    const f = formFromPlan(plan)
    f.rules[1]!.id = '5h'
    expect(validatePlanForm(f)['rules[1].id']).toMatch('唯一')
    f.rules = []
    expect(validatePlanForm(f).rules).toMatch('1 到 10')
  })

  it('requires a positive integer limit for the images meter', () => {
    const f = formFromPlan(plan)
    f.rules[1]!.meter = 'images'
    f.rules[1]!.limit = '2.5'
    expect(validatePlanForm(f)['rules[1].limit']).toMatch('正整数')
    f.rules[1]!.limit = '100'
    expect(validatePlanForm(f)).toEqual({})
    expect(ruleFromForm(f.rules[1]!)).toMatchObject({ meter: 'images', limit: '100' })
  })

  it('requires a positive integer limit for audio_seconds (phase9 §1)', () => {
    const f = formFromPlan(plan)
    f.rules[1]!.meter = 'audio_seconds'
    f.rules[1]!.limit = '60.5'
    expect(validatePlanForm(f)['rules[1].limit']).toMatch('音频秒数')
    f.rules[1]!.limit = '3600'
    expect(validatePlanForm(f)).toEqual({})
  })

  it('accepts decimal limits for plugin meters and checks the meter id (phase9 §3)', () => {
    const f = formFromPlan(plan)
    f.rules[1]!.meter = 'custom:community.billing-examples.weighted_tokens'
    f.rules[1]!.limit = '12.5'
    expect(validatePlanForm(f)).toEqual({})
    expect(ruleFromForm(f.rules[1]!)).toMatchObject({ meter: 'custom:community.billing-examples.weighted_tokens', limit: '12.5' })
    f.rules[1]!.limit = '0'
    expect(validatePlanForm(f)['rules[1].limit']).toMatch('大于 0')
    f.rules[1]!.limit = '1'
    f.rules[1]!.meter = 'custom:nodot'
    expect(validatePlanForm(f)['rules[1].meter']).toMatch('custom:')
  })

  it('round-trips plugin meters without sending snapshot fields', () => {
    const p = { ...plan, rules: [{ ...plan.rules[0]!, meter: 'custom:acme.billing.points', meterLabel: '积分', meterUnit: '点' }] }
    const out = ruleFromForm(formFromPlan(p).rules[0]!)
    expect(out.meter).toBe('custom:acme.billing.points')
    expect(out).not.toHaveProperty('meterLabel')
    expect(out).not.toHaveProperty('meterUnit')
  })

  it('accepts money limits for charge rules', () => {
    const f = formFromPlan(plan)
    f.rules[1]!.meter = 'charge'
    f.rules[1]!.limit = '12.345'
    expect(validatePlanForm(f)).toEqual({})
  })
})

describe('presets', () => {
  it('builds a Claude-Pro-like rule set', () => {
    const rules = presetRules('claude-pro', 'Asia/Shanghai').map(ruleFromForm)
    expect(rules).toEqual([
      { id: '5h', label: '5 小时会话', meter: 'requests', window: { kind: 'session', duration: '5h' }, limit: '200', models: [], modelWeights: {} },
      { id: 'weekly', label: '每周', meter: 'tokens.total', window: { kind: 'calendar', unit: 'week', timezone: 'Asia/Shanghai' }, limit: '5000000', models: [], modelWeights: {} },
    ])
  })

  it('builds the request pack and monthly credit presets', () => {
    expect(presetRules('request-pack').map(ruleFromForm)[0]).toMatchObject({ id: 'total', meter: 'requests', window: { kind: 'lifetime' }, limit: '1000' })
    expect(presetRules('monthly-credit').map(ruleFromForm)[0]).toMatchObject({ id: 'monthly', meter: 'charge', window: { kind: 'period', every: '30d' }, limit: '20' })
  })

  it('produces valid forms with unique uids', () => {
    for (const key of ['claude-pro', 'request-pack', 'monthly-credit'] as const) {
      const f = emptyPlanForm()
      f.name = 'x'
      f.rules = presetRules(key, 'UTC')
      expect(validatePlanForm(f)).toEqual({})
      expect(new Set(f.rules.map(r => r.uid)).size).toBe(f.rules.length)
    }
  })
})

describe('helpers', () => {
  it('parses rule error keys and timezones', () => {
    expect(ruleErrorKey('rules[3].window.duration')).toEqual({ index: 3, field: 'window.duration' })
    expect(ruleErrorKey('duration')).toBeNull()
    expect(isValidTimezone('UTC')).toBe(true)
    expect(isValidTimezone('Asia/Shanghai')).toBe(true)
    expect(isValidTimezone('Nope/Nowhere')).toBe(false)
    expect(isValidTimezone('Local')).toBe(false)
  })
})
