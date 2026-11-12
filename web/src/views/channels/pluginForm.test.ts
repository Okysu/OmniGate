import type { PluginManifest } from '@/lib/types'
import { describe, expect, it } from 'vitest'
import { emptyPluginForm, pluginIdentityChanged, pluginPayload, validatePluginForm } from './pluginForm'

const manifest = {
  id: 'acme.x',
  name: 'X',
  version: '1.0.0',
  sdk: 1,
  extends: 'openai.chat',
  defaults: {},
  permissions: { network: [], secrets: ['token'], schedule: [], dangerous: [] },
  capabilities: {},
  hooks: [],
  uiContributions: [],
  configSchema: {
    type: 'object',
    properties: { region: { type: 'string' }, token: { 'type': 'string', 'x-secret': true } },
    required: ['region'],
  },
} as PluginManifest

describe('pluginPayload', () => {
  it('always sends the version and config on create', () => {
    const s = { ...emptyPluginForm(), versionId: 'v1', manifest, config: { region: 'cn' }, secrets: { token: { value: 't', replace: true } } }
    expect(pluginPayload(s, true)).toEqual({ pluginVersionId: 'v1', pluginConfig: { region: 'cn' }, secrets: { token: 't' } })
  })

  it('on edit sends only what changed', () => {
    const base = { ...emptyPluginForm(), versionId: 'v1', originalVersionId: 'v1', manifest, config: { region: 'cn' }, secrets: { token: { value: '', replace: false } } }
    expect(pluginPayload(base, false)).toEqual({})
    expect(pluginPayload({ ...base, configTouched: true }, false)).toEqual({ pluginConfig: { region: 'cn' } })
    // Version switch without touching config: let the server run migrateConfig.
    expect(pluginPayload({ ...base, versionId: 'v2' }, false)).toEqual({ pluginVersionId: 'v2' })
  })

  it('replaces config when changing plugins and can bind a legacy channel', () => {
    const s = { ...emptyPluginForm(), pluginId: 'new', originalPluginId: 'old', originalVersionId: 'v1', versionId: 'v2', manifest, config: { region: 'us' } }
    expect(pluginIdentityChanged(s)).toBe(true)
    expect(pluginPayload(s, false)).toEqual({ pluginVersionId: 'v2', pluginConfig: { region: 'us' } })
    expect(pluginPayload({ ...s, originalPluginId: null, originalVersionId: null }, false)).toEqual({ pluginVersionId: 'v2', pluginConfig: { region: 'us' } })
  })
})

describe('validatePluginForm', () => {
  it('skips config checks during an untouched version switch', () => {
    const s = { ...emptyPluginForm(), versionId: 'v2', originalVersionId: 'v1', manifest, config: { region: '' }, secrets: { token: { value: '', replace: true } } }
    expect(validatePluginForm(s)).toEqual({})
    expect(validatePluginForm({ ...s, configTouched: true })).toEqual({ 'pluginConfig.region': '必填' })
  })

  it('validates a new plugin without reusing same-named secrets from the old one', () => {
    const m = { ...manifest, configSchema: { ...manifest.configSchema!, required: ['region', 'token'] } }
    const s = { ...emptyPluginForm(), pluginId: 'new', originalPluginId: 'old', versionId: 'v2', originalVersionId: 'v1', manifest: m, config: { region: '' }, secrets: { token: { value: '', replace: true } } }
    expect(validatePluginForm(s, { token: { set: true } })).toEqual({ 'pluginConfig.region': '必填', 'secrets.token': '必填' })
    expect(validatePluginForm({ ...s, config: { region: 'cn' }, secrets: { token: { value: 'fresh-token', replace: true } } })).toEqual({})
  })
})
