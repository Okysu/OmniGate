import type { RouteRule } from './types'
import { describe, expect, it } from 'vitest'
import {
  buildRoutePatch,
  buildRoutePayload,
  channelServedModels,
  duplicateForm,
  emptyRouteForm,
  emptyTarget,
  formFromRule,
  isInlineRouteErrorKey,
  listError,
  matchesAny,
  matchesPattern,
  modelsMatching,
  moveItem,
  PREVIEW_INBOUNDS,
  sortRules,
  targetErrorKey,
  targetErrors,
  targetsSummary,
  validateRouteForm,
} from './routeForm'

const rule: RouteRule = {
  id: 'r1',
  name: 'GPT 走便宜渠道',
  description: '说明',
  enabled: true,
  position: 0,
  match: { models: ['gpt-*', 'o3'], roles: ['user'] },
  targets: [
    { channelId: 'c1', priority: 10, weight: null },
    { channelId: 'c2', priority: null, weight: 5 },
  ],
  strategy: 'lowest_cost',
  protocolPreference: 'ignore',
  retry: { maxAttempts: 2, retryOn: ['rate_limit', 'timeout'] },
  fallbackModels: ['gpt-4o-mini'],
  version: 7,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
}

describe('route form ↔ payload', () => {
  it('round-trips a rule', () => {
    const body = buildRoutePayload(formFromRule(rule))
    expect(body).toEqual({
      name: 'GPT 走便宜渠道',
      description: '说明',
      enabled: true,
      match: { models: ['gpt-*', 'o3'], roles: ['user'] },
      targets: [
        { channelId: 'c1', priority: 10, weight: null },
        { channelId: 'c2', priority: null, weight: 5 },
      ],
      strategy: 'lowest_cost',
      protocolPreference: 'ignore',
      retry: { maxAttempts: 2, retryOn: ['rate_limit', 'timeout'] },
      fallbackModels: ['gpt-4o-mini'],
    })
    expect(buildRoutePatch(formFromRule(rule), 7).version).toBe(7)
  })

  it('maps defaults of a new rule', () => {
    const f = emptyRouteForm()
    f.name = ' 新规则 '
    f.models = [' claude-* ', 'claude-*', '']
    f.maxAttempts = '4'
    const body = buildRoutePayload(f)
    expect(body.name).toBe('新规则')
    expect(body.match).toEqual({ models: ['claude-*'], roles: [] })
    expect(body.strategy).toBe('priority')
    expect(body.protocolPreference).toBe('native_first')
    // New rules retry on everything except client_error (other 4xx).
    expect(body.retry).toEqual({ maxAttempts: 4, retryOn: ['rate_limit', 'server_error', 'timeout', 'network', 'auth_error', 'not_found'] })
    expect(body.targets).toEqual([])
  })

  it('keeps retryOn in canonical order and trims target overrides', () => {
    const f = emptyRouteForm()
    f.retryOn = ['client_error', 'network', 'rate_limit', 'network']
    f.targets = [{ ...emptyTarget('c9'), priority: ' 3 ', weight: '  ' }]
    const body = buildRoutePayload(f)
    expect(body.retry.retryOn).toEqual(['rate_limit', 'network', 'client_error'])
    expect(body.targets).toEqual([{ channelId: 'c9', priority: 3, weight: null }])
  })

  it('duplicates as a disabled copy', () => {
    const f = duplicateForm(rule)
    expect(f.name).toBe('GPT 走便宜渠道（副本）')
    expect(f.enabled).toBe(false)
    expect(f.targets[0]!.uid).not.toBe(formFromRule(rule).targets[0]!.uid)
    expect(duplicateForm({ ...rule, name: 'x'.repeat(100) }).name.length).toBe(100)
  })
})

describe('validation', () => {
  it('accepts a valid rule', () => {
    expect(validateRouteForm(formFromRule(rule))).toEqual({})
  })

  it('reports errors with server keys', () => {
    const f = emptyRouteForm()
    f.maxAttempts = 9
    f.targets = [{ ...emptyTarget('c1'), priority: '1.5' }, { ...emptyTarget('c1'), weight: '0' }, emptyTarget()]
    f.fallbackModels = ['gpt-*']
    const e = validateRouteForm(f)
    expect(Object.keys(e).sort()).toEqual([
      'fallbackModels',
      'match.models',
      'name',
      'retry.maxAttempts',
      'targets[0].priority',
      'targets[1].channelId',
      'targets[1].weight',
      'targets[2].channelId',
    ])
  })

  it('limits fallback models to five', () => {
    const f = formFromRule(rule)
    f.fallbackModels = ['a', 'b', 'c', 'd', 'e', 'f']
    expect(validateRouteForm(f).fallbackModels).toContain('5')
  })
})

describe('error keys', () => {
  it('parses target keys', () => {
    expect(targetErrorKey('targets[3].weight')).toEqual({ index: 3, field: 'weight' })
    expect(targetErrorKey('targets[0]')).toEqual({ index: 0, field: '' })
    expect(targetErrorKey('match.models')).toBeNull()
    expect(targetErrors({ 'targets[1].channelId': 'x', 'targets[0].weight': 'y', 'name': 'z' }, 1)).toEqual({ channelId: 'x' })
  })

  it('classifies inline keys', () => {
    expect(isInlineRouteErrorKey('match.models', 0)).toBe(true)
    expect(isInlineRouteErrorKey('match.models[2]', 0)).toBe(true)
    expect(isInlineRouteErrorKey('targets[0].channelId', 1)).toBe(true)
    expect(isInlineRouteErrorKey('targets[4].channelId', 1)).toBe(false)
    expect(isInlineRouteErrorKey('something.else', 1)).toBe(false)
    expect(listError({ 'match.models[1]': 'bad' }, 'match.models')).toBe('bad')
    expect(listError({ 'match.models': 'a', 'match.models[1]': 'b' }, 'match.models')).toBe('a')
  })
})

describe('globs', () => {
  it('matches exact names and wildcards', () => {
    expect(matchesPattern('gpt-4o', 'gpt-4o')).toBe(true)
    expect(matchesPattern('gpt-4o', 'gpt-4o-mini')).toBe(false)
    expect(matchesPattern('gpt-*', 'gpt-4o-mini')).toBe(true)
    expect(matchesPattern('gpt-*', 'chatgpt-4o')).toBe(false)
    expect(matchesPattern('*', 'anything')).toBe(true)
    expect(matchesPattern('*-mini', 'gpt-4o-mini')).toBe(true)
    expect(matchesPattern('claude-*-sonnet*', 'claude-3-5-sonnet-latest')).toBe(true)
    // Regex metacharacters are literal.
    expect(matchesPattern('a.b*', 'axb')).toBe(false)
    expect(matchesPattern('a.b*', 'a.bc')).toBe(true)
    expect(matchesAny(['o3', 'gpt-*'], 'gpt-5')).toBe(true)
    expect(modelsMatching('gpt-*', ['gpt-4o', 'o3', 'gpt-5'])).toEqual(['gpt-4o', 'gpt-5'])
  })

  it('finds the models a channel serves', () => {
    const ch = { models: [{ model: 'gpt-4o', upstreamModel: 'x' }, { model: 'claude-x', upstreamModel: 'y' }, { model: 'gpt-4o', upstreamModel: 'z' }] }
    expect(channelServedModels(ch, ['gpt-*'])).toEqual(['gpt-4o'])
    expect(channelServedModels({ models: null }, ['*'])).toEqual([])
  })
})

describe('ordering', () => {
  it('moves items', () => {
    expect(moveItem(['a', 'b', 'c'], 0, 2)).toEqual(['b', 'c', 'a'])
    expect(moveItem(['a', 'b', 'c'], 2, 0)).toEqual(['c', 'a', 'b'])
    expect(moveItem(['a', 'b', 'c'], 1, 9)).toEqual(['a', 'c', 'b'])
    expect(moveItem(['a', 'b'], 5, 0)).toEqual(['a', 'b'])
  })

  it('sorts by position', () => {
    const r = (id: string, position: number) => ({ ...rule, id, position })
    expect(sortRules([r('b', 1), r('a', 0), r('c', 1)]).map(x => x.id)).toEqual(['a', 'b', 'c'])
  })

  it('summarises targets', () => {
    const names: Record<string, string> = { c1: '主渠道', c2: '备用' }
    expect(targetsSummary({ targets: [] }, id => names[id])).toBe('全部可用渠道')
    expect(targetsSummary(rule, id => names[id])).toBe('主渠道、备用')
    expect(targetsSummary({ targets: [...rule.targets, { channelId: 'zz', priority: null, weight: null }] }, id => names[id])).toBe('主渠道、备用 等 3 个')
  })
})

describe('PREVIEW_INBOUNDS (phase9 §1)', () => {
  it('offers the audio inbounds accepted by the preview endpoint', () => {
    expect(PREVIEW_INBOUNDS).toContain('openai.audio.transcriptions')
    expect(PREVIEW_INBOUNDS).toContain('openai.audio.speech')
    expect(PREVIEW_INBOUNDS).not.toContain('openai.audio.translations')
  })
  it('offers the completions inbound (phase14)', () => {
    expect(PREVIEW_INBOUNDS).toContain('openai.completions')
  })
})
