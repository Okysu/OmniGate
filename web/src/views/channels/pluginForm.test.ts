import type { PluginManifest } from '@/lib/types'
import { describe, expect, it } from 'vitest'
import { emptyPluginForm, pluginPayload, validatePluginForm } from './pluginForm'

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
})

describe('validatePluginForm', () => {
  it('skips config checks during an untouched version switch', () => {
    const s = { ...emptyPluginForm(), versionId: 'v2', originalVersionId: 'v1', manifest, config: { region: '' }, secrets: { token: { value: '', replace: true } } }
    expect(validatePluginForm(s)).toEqual({})
    expect(validatePluginForm({ ...s, configTouched: true })).toEqual({ 'pluginConfig.region': '必填' })
  })
})
