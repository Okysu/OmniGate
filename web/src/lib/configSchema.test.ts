import type { ConfigSchema } from './types'
import { describe, expect, it } from 'vitest'
import { buildPluginConfig, buildSecrets, checkValue, groupFields, initConfigValues, initSecretValues, schemaFields, validatePluginConfig } from './configSchema'

const schema: ConfigSchema = {
  type: 'object',
  properties: {
    currency: { 'type': 'string', 'title': '币种', 'enum': ['CNY', 'USD'], 'default': 'CNY' },
    lowBalance: { type: 'number', minimum: 0, default: 10 },
    retries: { 'type': 'integer', 'minimum': 1, 'maximum': 5, 'x-group': '高级' },
    region: { type: 'string', pattern: '^[a-z]+-\\d$', minLength: 3, maxLength: 12 },
    tier: { type: 'integer', enum: [1, 2, 3] },
    verbose: { type: 'boolean', default: true },
    orgId: { 'type': 'string', 'x-secret': true },
    token: { 'type': 'string', 'x-secret': true },
  },
  required: ['region', 'token'],
}

describe('schemaFields / groupFields', () => {
  it('lists fields with required / secret flags', () => {
    const f = schemaFields(schema)
    expect(f.map(x => x.name)).toEqual(['currency', 'lowBalance', 'retries', 'region', 'tier', 'verbose', 'orgId', 'token'])
    expect(f.find(x => x.name === 'region')?.required).toBe(true)
    expect(f.filter(x => x.secret).map(x => x.name)).toEqual(['orgId', 'token'])
    expect(schemaFields(null)).toEqual([])
  })

  it('groups by x-group with ungrouped first', () => {
    const groups = groupFields(schemaFields(schema).filter(x => !x.secret))
    expect(groups.map(g => g.group)).toEqual(['', '高级'])
    expect(groups[1]?.fields.map(x => x.name)).toEqual(['retries'])
  })
})

describe('initConfigValues', () => {
  it('uses defaults for new channels', () => {
    expect(initConfigValues(schema)).toEqual({ currency: 'CNY', lowBalance: '10', retries: '', region: '', tier: '', verbose: true })
  })

  it('prefers existing config', () => {
    const v = initConfigValues(schema, { currency: 'USD', verbose: false, tier: 2 })
    expect(v).toMatchObject({ currency: 'USD', verbose: false, tier: '2', lowBalance: '10' })
  })

  it('keeps set secrets unless replaced', () => {
    expect(initSecretValues(schema, { token: { set: true } })).toEqual({ orgId: { value: '', replace: true }, token: { value: '', replace: false } })
  })
})

describe('checkValue', () => {
  it('checks numbers, integers and ranges', () => {
    expect(checkValue({ type: 'number' }, 'abc')).toBe('必须是数字')
    expect(checkValue({ type: 'integer' }, '1.5')).toBe('必须是整数')
    expect(checkValue({ type: 'integer', minimum: 1, maximum: 5 }, '6')).toBe('范围为 1–5')
    expect(checkValue({ type: 'number', minimum: 0 }, '-1')).toBe('不能小于 0')
    expect(checkValue({ type: 'number', minimum: 0 }, '0.5')).toBe('')
  })

  it('checks string length (in characters) and pattern', () => {
    expect(checkValue({ type: 'string', maxLength: 2 }, '你好')).toBe('')
    expect(checkValue({ type: 'string', maxLength: 2 }, '你好吗')).toBe('最多 2 个字符')
    expect(checkValue({ type: 'string', pattern: '^[a-z]+-\\d$' }, 'cn-1')).toBe('')
    expect(checkValue({ type: 'string', pattern: '^[a-z]+-\\d$' }, 'CN-1')).toBe('格式不符合要求')
  })

  it('checks enums by string value', () => {
    expect(checkValue({ type: 'integer', enum: [1, 2] }, '2')).toBe('')
    expect(checkValue({ type: 'string', enum: ['a'] }, 'b')).toBe('不在允许的取值范围内')
  })
})

describe('validatePluginConfig', () => {
  it('reports required fields and invalid values with server keys', () => {
    const values = initConfigValues(schema)
    values.retries = '9'
    const errs = validatePluginConfig(schema, values, initSecretValues(schema))
    expect(errs).toEqual({
      'pluginConfig.region': '必填',
      'pluginConfig.retries': '范围为 1–5',
      'secrets.token': '必填',
    })
  })

  it('accepts a stored secret as satisfying required', () => {
    const values = { ...initConfigValues(schema), region: 'cn-1' }
    const secrets = initSecretValues(schema, { token: { set: true } })
    expect(validatePluginConfig(schema, values, secrets, { secretsSet: { token: { set: true } } })).toEqual({})
  })

  it('does not require fields that have a default', () => {
    const s: ConfigSchema = { type: 'object', properties: { a: { type: 'string', default: 'x' } }, required: ['a'] }
    expect(validatePluginConfig(s, { a: '' }, {})).toEqual({})
  })
})

describe('buildPluginConfig / buildSecrets', () => {
  it('builds typed config, omitting empty optionals', () => {
    const values = { ...initConfigValues(schema), region: ' cn-1 ', tier: '2', lowBalance: '' }
    expect(buildPluginConfig(schema, values)).toEqual({ currency: 'CNY', region: 'cn-1', tier: 2, verbose: true })
  })

  it('sends only newly typed secrets', () => {
    expect(buildSecrets({ a: { value: 'x', replace: true }, b: { value: '', replace: true }, c: { value: 'y', replace: false } })).toEqual({ a: 'x' })
    expect(buildSecrets({ a: { value: '', replace: true } })).toBeUndefined()
  })
})
