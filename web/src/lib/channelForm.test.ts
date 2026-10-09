import type { Channel } from './types'
import { describe, expect, it } from 'vitest'
import { appendModels, balanceThresholdError, buildChannelPayload, diffDiscoveredModels, emptyChannelForm, formFromChannel, hasBalanceOutput, normalizeBalanceThreshold, normalizeSharedWith, rowsToHeaders, sectionOfField, shareCount } from './channelForm'

const channel: Channel = {
  id: 'c1',
  name: 'Main',
  type: 'openai',
  baseUrl: 'https://api.example.com/v1',
  scope: 'shared',
  sharedWith: ['u1'],
  status: 'enabled',
  priority: 5,
  weight: 2,
  models: [{ model: 'gpt-4o', upstreamModel: 'gpt-4o-2024' }],
  config: { headers: { 'X-Org': 'acme' }, supportsResponses: true, timeoutSeconds: 120 },
  secret: { set: true, hint: '…a1b2' },
  owner: { id: 'u0', displayName: 'Owner' },
  health: { state: 'healthy', consecutiveFailures: 0, lastError: null, lastCheckedAt: null },
  version: 3,
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
}

describe('buildChannelPayload', () => {
  it('builds a create body with type and apiKey, dropping blank model rows', () => {
    const f = emptyChannelForm()
    f.name = ' New '
    f.baseUrl = 'https://api.anthropic.com '
    f.type = 'anthropic'
    f.apiKey = 'sk-test'
    f.supportsResponses = true // ignored for anthropic
    f.models = [{ model: 'claude', upstreamModel: '' }, { model: '', upstreamModel: '' }]
    const body = buildChannelPayload(f)
    expect(body).toEqual({
      name: 'New',
      baseUrl: 'https://api.anthropic.com',
      scope: 'private',
      status: 'enabled',
      priority: 0,
      weight: 1,
      models: [{ model: 'claude', upstreamModel: 'claude' }],
      config: {},
      type: 'anthropic',
      apiKey: 'sk-test',
    })
  })

  it('builds a patch body with version, without type, and apiKey only when replacing', () => {
    const f = formFromChannel(channel)
    expect(f.replaceKey).toBe(false)
    const body = buildChannelPayload(f, channel)
    expect(body.type).toBeUndefined()
    expect(body.apiKey).toBeUndefined()
    expect(body.version).toBe(3)
    expect(body.sharedWith).toEqual({ users: ['u1'], groups: [] })
    expect(body.config).toEqual({ headers: { 'X-Org': 'acme' }, supportsResponses: true, timeoutSeconds: 120 })

    f.replaceKey = true
    f.apiKey = 'sk-new'
    f.scope = 'private'
    const body2 = buildChannelPayload(f, channel)
    expect(body2.apiKey).toBe('sk-new')
    expect(body2.sharedWith).toBeUndefined()
  })
})

describe('upstream balance alert threshold (phase6 §5)', () => {
  it('is omitted when unchanged, so older backends keep accepting edits', () => {
    expect(buildChannelPayload(formFromChannel(channel), channel).alerts).toBeUndefined()
    const withAlert = { ...channel, alerts: { balanceBelow: '10.50' } }
    const f = formFromChannel(withAlert)
    expect(f.balanceBelow).toBe('10.50')
    f.balanceBelow = ' 10.5 '
    expect(buildChannelPayload(f, withAlert).alerts).toBeUndefined()
  })

  it('sends the canonical value when set or changed, and null when cleared', () => {
    const f = formFromChannel(channel)
    f.balanceBelow = '5.000'
    expect(buildChannelPayload(f, channel).alerts).toEqual({ balanceBelow: '5' })
    const withAlert = { ...channel, alerts: { balanceBelow: '5' } }
    const g = formFromChannel(withAlert)
    g.balanceBelow = ''
    expect(buildChannelPayload(g, withAlert).alerts).toEqual({ balanceBelow: null })
  })

  it('only sends alerts on create when a threshold is entered', () => {
    const f = emptyChannelForm()
    f.name = 'x'
    f.baseUrl = 'https://x'
    f.apiKey = 'k'
    expect(buildChannelPayload(f).alerts).toBeUndefined()
    f.balanceBelow = '0.5'
    expect(buildChannelPayload(f).alerts).toEqual({ balanceBelow: '0.5' })
  })

  it('validates the threshold', () => {
    expect(balanceThresholdError('')).toBeNull()
    expect(balanceThresholdError('12.345')).toBeNull()
    expect(balanceThresholdError('-1')).not.toBeNull()
    expect(balanceThresholdError('abc')).not.toBeNull()
    expect(balanceThresholdError('1.0000000001')).not.toBeNull()
    expect(normalizeBalanceThreshold(null)).toBeNull()
    expect(normalizeBalanceThreshold('  ')).toBeNull()
    expect(normalizeBalanceThreshold('007.10')).toBe('7.1')
    expect(sectionOfField('alerts.balanceBelow')).toBe('alerts')
  })
})

describe('model discovery helpers', () => {
  it('diffs discovered upstream models against current mappings', () => {
    const d = diffDiscoveredModels(
      [{ model: 'gpt-4o', upstreamModel: 'gpt-4o-2024' }, { model: 'old', upstreamModel: '' }],
      ['gpt-4o-2024', 'gpt-4.1', 'o3'],
    )
    expect(d).toEqual({ added: ['gpt-4.1', 'o3'], existing: ['gpt-4o-2024'], missing: ['old'] })
  })

  it('appends identity mappings without duplicating logical names', () => {
    const rows = appendModels([{ model: 'a', upstreamModel: 'a' }, { model: '', upstreamModel: '' }], ['a', 'b'])
    expect(rows).toEqual([{ model: 'a', upstreamModel: 'a' }, { model: 'b', upstreamModel: 'b' }])
  })

  it('converts header rows', () => {
    expect(rowsToHeaders([{ key: ' X-A ', value: '1' }, { key: '', value: 'x' }])).toEqual({ 'X-A': '1' })
  })
})

describe('sharedWith (phase8 §1.2)', () => {
  it('normalises legacy user lists and the {users, groups} object', () => {
    expect(normalizeSharedWith(['u1', 'u2'])).toEqual({ users: ['u1', 'u2'], groups: [] })
    expect(normalizeSharedWith({ users: ['u1'], groups: ['g1'] })).toEqual({ users: ['u1'], groups: ['g1'] })
    expect(normalizeSharedWith(null)).toEqual({ users: [], groups: [] })
    expect(normalizeSharedWith({ users: null, groups: ['g1', ''] } as never)).toEqual({ users: [], groups: ['g1'] })
    expect(shareCount({ users: ['a'], groups: ['b', 'c'] })).toBe(3)
  })

  it('sends users and groups (deduplicated) only for shared channels', () => {
    const f = formFromChannel({ ...channel, sharedWith: { users: ['u1'], groups: ['g1'] } })
    f.sharedWith.groups.push('g1', 'g2')
    expect(buildChannelPayload(f, channel).sharedWith).toEqual({ users: ['u1'], groups: ['g1', 'g2'] })
    f.scope = 'private'
    expect(buildChannelPayload(f, channel).sharedWith).toBeUndefined()
    expect(sectionOfField('sharedWith.groups')).toBe('sharing')
  })
})

describe('hasBalanceOutput', () => {
  it('detects a balance capability by output kind, not by name', () => {
    expect(hasBalanceOutput({ 'balance.get': { output: 'balance' }, 'models.list': { output: 'models' } })).toBe(true)
    expect(hasBalanceOutput({ 'custom.wallet': { output: 'balance' } })).toBe(true)
    expect(hasBalanceOutput({ 'models.list': { output: 'models' } })).toBe(false)
    expect(hasBalanceOutput(null)).toBe(false)
  })
})
