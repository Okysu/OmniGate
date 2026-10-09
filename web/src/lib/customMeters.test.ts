import type { Plugin, PluginManifest } from './types'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { clearCustomMeters, collectMeterOptions, optionsFromMeterList, customMeterId, customMeterInfo, declaredMeters, isBillingPlugin, isChannelPlugin, parseCustomMeter, pluginKinds } from './customMeters'

function plugin(p: Partial<Plugin>): Plugin {
  return {
    id: 'p1',
    key: 'community.billing-examples',
    name: '计费示例',
    description: '',
    author: '',
    homepage: '',
    source: 'bundled',
    status: 'enabled',
    version: 1,
    createdAt: '',
    updatedAt: '',
    latest: { id: 'v1', version: '1.0.0', approval: 'approved', publishedAt: '', publishedBy: null, contentHash: '', riskCount: 0 },
    pending: null,
    channels: 0,
    hasDraft: false,
    extends: '',
    ...p,
  }
}

const manifest = {
  id: 'community.billing-examples',
  kind: ['billing'],
  billing: { meters: { weighted_tokens: { label: '加权 Token', unit: 'tokens' }, per_image: { label: '图片张数' } } },
} as unknown as PluginManifest

describe('custom meter ids', () => {
  it('parses and builds ids', () => {
    expect(customMeterId('community.billing-examples', 'per_image')).toBe('custom:community.billing-examples.per_image')
    expect(parseCustomMeter('custom:community.billing-examples.per_image')).toEqual({ pluginKey: 'community.billing-examples', meter: 'per_image' })
    expect(parseCustomMeter('custom:nodot')).toBeNull()
    expect(parseCustomMeter('custom:a.b.')).toBeNull()
    expect(parseCustomMeter('requests')).toBeNull()
  })
})

describe('plugin kinds', () => {
  it('defaults to channel', () => {
    expect(pluginKinds({})).toEqual(['channel'])
    expect(pluginKinds({ kind: 'billing' })).toEqual(['billing'])
    expect(isBillingPlugin({ kind: ['channel', 'billing'] })).toBe(true)
    expect(isChannelPlugin({ kind: ['billing'] })).toBe(false)
    expect(isChannelPlugin(null)).toBe(true)
  })
})

describe('declaredMeters', () => {
  it('reads manifest.billing.meters sorted by name', () => {
    expect(declaredMeters(manifest)).toEqual([
      { name: 'per_image', label: '图片张数', unit: '' },
      { name: 'weighted_tokens', label: '加权 Token', unit: 'tokens' },
    ])
    expect(declaredMeters({ meters: { x: { label: ' ' } } })).toEqual([{ name: 'x', label: 'x', unit: '' }])
    expect(declaredMeters(null)).toEqual([])
  })
})

describe('collectMeterOptions', () => {
  afterEach(() => clearCustomMeters())

  it('uses the list meters when present, else fetches billing manifests', async () => {
    const fetchManifest = vi.fn(async () => manifest)
    const list = [
      plugin({ id: 'a', kind: ['billing'] }),
      plugin({ id: 'b', key: 'acme.points', name: '积分', kind: ['channel', 'billing'], meters: [{ name: 'points', label: '积分', unit: '点' }] }),
      plugin({ id: 'c', key: 'acme.chat', kind: ['channel'] }),
      plugin({ id: 'd', key: 'acme.off', kind: ['billing'], status: 'disabled' }),
      plugin({ id: 'e', key: 'acme.new', kind: ['billing'], latest: null }),
    ]
    const out = await collectMeterOptions(list, fetchManifest)
    expect(fetchManifest).toHaveBeenCalledTimes(1)
    expect(fetchManifest).toHaveBeenCalledWith('a', 'v1')
    expect(out.map(m => m.id)).toEqual([
      'custom:community.billing-examples.per_image',
      'custom:community.billing-examples.weighted_tokens',
      'custom:acme.points.points',
    ])
    expect(customMeterInfo('custom:acme.points.points')).toEqual({ label: '积分', unit: '点', known: true })
  })

  it('inspects every enabled non-builtin plugin when the list has no kind', async () => {
    const fetchManifest = vi.fn(async (id: string) => (id === 'a' ? manifest : ({ kind: undefined } as unknown as PluginManifest)))
    const out = await collectMeterOptions([plugin({ id: 'a' }), plugin({ id: 'b', key: 'x.y' }), plugin({ id: 'z', source: 'builtin' })], fetchManifest)
    expect(fetchManifest).toHaveBeenCalledTimes(2)
    expect(out).toHaveLength(2)
  })

  it('skips plugins whose manifest cannot be read', async () => {
    const out = await collectMeterOptions([plugin({ kind: ['billing'] })], async () => {
      throw new Error('403')
    })
    expect(out).toEqual([])
  })
})

describe('optionsFromMeterList (GET /api/admin/billing/meters)', () => {
  afterEach(() => clearCustomMeters())
  it('keeps plugin meters and registers them', () => {
    const out = optionsFromMeterList([
      { meter: 'requests', label: '请求数', unit: '次', builtin: true, plugin: null, integer: true },
      { meter: 'custom:community.billing-examples.weighted_tokens', label: '加权 Token', unit: 'tokens', builtin: false, integer: false, plugin: { id: 'p', key: 'community.billing-examples', name: '计费示例', version: '1.0.0', versionId: 'v' } },
      { meter: 'custom:bad', label: 'x', builtin: false, integer: false, plugin: null },
    ])
    expect(out).toEqual([{ id: 'custom:community.billing-examples.weighted_tokens', pluginKey: 'community.billing-examples', pluginName: '计费示例', meter: 'weighted_tokens', label: '加权 Token', unit: 'tokens' }])
    expect(customMeterInfo('custom:community.billing-examples.weighted_tokens')?.label).toBe('加权 Token')
  })
})
