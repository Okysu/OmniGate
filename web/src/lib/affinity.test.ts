import type { AffinityConfig } from './types'
import { describe, expect, it } from 'vitest'
import {
  affinityHitRate,
  configEquals,
  defaultConfig,
  effectiveMode,
  emptyRule,
  formToRule,
  isForbiddenHeader,
  keySourceLabel,
  mergeRules,
  modeIsOwn,
  moveRule,
  normalizeConfig,
  omnigatePresets,
  parseConfigJson,
  passHeaders,
  ruleErrors,
  ruleToForm,
  scopeBadges,
  stringifyConfig,
  ttlLabel,
  uniqueName,
  validateConfig,
  validateRule,
} from './affinity'
import { cacheHitRate } from './stats'

/** new-api's setting as an administrator would paste it (null arrays, no session_mode). */
const NEW_API = `{
  "enabled": true, "switch_on_success": true, "max_entries": 100000, "default_ttl_seconds": 3600,
  "rules": [{
    "name": "codex cli trace", "model_regex": ["^gpt-.*$"], "path_regex": ["/v1/responses"],
    "key_sources": [{"type": "gjson", "path": "prompt_cache_key"}], "value_regex": "", "ttl_seconds": 0,
    "param_override_template": {"operations": [{"mode": "pass_headers", "value": ["Originator", "Session_id", "User-Agent"], "keep_origin": true}]},
    "skip_retry_on_failure": true, "include_using_group": true, "include_rule_name": true, "user_agent_include": null
  }]
}`

describe('affinity setting (de)serialisation', () => {
  it('normalises pasted new-api JSON like the server', () => {
    const { config, error } = parseConfigJson(NEW_API)
    expect(error).toBeNull()
    expect(config).not.toBeNull()
    const c = config!
    expect(c.session_mode).toBe('prefer')
    expect(c.keep_on_channel_disabled).toBe(false)
    expect(c.rules).toHaveLength(1)
    const r = c.rules[0]!
    expect(r.user_agent_include).toEqual([])
    expect(r.include_model_name).toBe(false)
    expect(r.session_mode).toBe('')
    expect(effectiveMode(r, c.session_mode)).toBe('strict')
    expect(modeIsOwn(r)).toBe(true)
    expect(validateConfig(c)).toEqual({})
  })

  it('reports syntax errors and non-objects', () => {
    expect(parseConfigJson('{').error).toMatch(/^JSON 格式错误/)
    expect(parseConfigJson('[]').error).toBe('必须是 JSON 对象（会话亲和设置）')
    expect(parseConfigJson('{}').config).toEqual({ ...defaultConfig(), rules: [] })
  })

  it('accepts a comma-separated pass_headers value', () => {
    const c = normalizeConfig({ rules: [{ name: 'r', key_sources: [{ type: 'request_header', key: 'Session_id' }], param_override_template: { operations: [{ mode: 'pass_headers', value: 'X-A, Session_id' }] } }] })
    expect(passHeaders(c.rules[0]!)).toEqual({ names: ['X-A', 'Session_id'], keepOrigin: false })
  })

  it('round-trips between the table and the JSON editor', () => {
    const c = defaultConfig()
    const text = stringifyConfig(c)
    const back = parseConfigJson(text).config!
    expect(back).toEqual(c)
    expect(stringifyConfig(back)).toBe(text)
    expect(configEquals(c, back)).toBe(true)
    back.rules[0]!.ttl_seconds = 60
    expect(configEquals(c, back)).toBe(false)
  })

  it('rule form round-trips the presets', () => {
    for (const r of omnigatePresets())
      expect(formToRule(ruleToForm(r))).toEqual(r)
    const f = ruleToForm(emptyRule())
    f.name = ' x '
    f.modelRegex = '^a\n\n ^b '
    f.keySources = [{ type: 'request_header', value: ' Session_id ' }]
    f.headers = ['Session_id']
    f.ttl = ''
    f.mode = 'strict'
    f.skipRetry = true
    const r = formToRule(f)
    expect(r).toMatchObject({ name: 'x', model_regex: ['^a', '^b'], key_sources: [{ type: 'request_header', key: 'Session_id' }], ttl_seconds: 0,
      session_mode: 'strict', skip_retry_on_failure: false })
    expect(r.param_override_template).toEqual({ operations: [{ mode: 'pass_headers', value: ['Session_id'], keep_origin: true }] })
  })
})

describe('affinity validation', () => {
  const base = (): AffinityConfig => ({ ...defaultConfig(), rules: [{ ...emptyRule(), name: 'r', key_sources: [{ type: 'gjson', path: 'k' }] }] })

  it('accepts the presets and pasted new-api rules', () => {
    expect(validateConfig(defaultConfig())).toEqual({})
    expect(validateConfig(parseConfigJson(NEW_API).config!)).toEqual({})
  })

  it('rejects new-api internal key sources and other operation modes', () => {
    const c = base()
    c.rules[0]!.key_sources = [{ type: 'context_int', key: 'user_id' }, { type: 'gjson', path: '' }]
    c.rules[0]!.param_override_template = { operations: [{ mode: 'set' as 'pass_headers', value: ['X'], keep_origin: false }] }
    const e = validateConfig(c)
    expect(e['rules[0].key_sources[0].type']).toMatch(/context_int 是 new-api 内部上下文类型/)
    expect(e['rules[0].key_sources[1].path']).toBeTruthy()
    expect(e['rules[0].param_override_template.operations[0].mode']).toMatch(/只支持 pass_headers/)
    expect(Object.keys(ruleErrors(e, 0))).toContain('key_sources[0].type')
  })

  it('rejects forbidden and malformed pass headers', () => {
    for (const h of ['Authorization', 'cookie', 'X-Api-Key', 'Host', 'Content-Length'])
      expect(isForbiddenHeader(h)).toBe(true)
    expect(isForbiddenHeader('Session_id')).toBe(false)
    const r = { ...emptyRule(), name: 'r', key_sources: [{ type: 'gjson', path: 'k' }], param_override_template: { operations: [{ mode: 'pass_headers' as const, value: ['Session_id', 'Cookie'], keep_origin: true }] } }
    expect(validateRule(r)['param_override_template.operations[0].value']).toMatch(/"Cookie" 不允许透传/)
    r.param_override_template.operations[0]!.value = ['bad header']
    expect(validateRule(r)['param_override_template.operations[0].value']).toMatch(/格式无效/)
  })

  it('checks regexes, names, limits and globals', () => {
    const c = base()
    c.rules.push({ ...c.rules[0]!, model_regex: ['(('], value_regex: '*', ttl_seconds: -1 })
    c.max_entries = 0
    c.default_ttl_seconds = Number.NaN
    const e = validateConfig(c)
    expect(e['rules[1].name']).toMatch(/重复/)
    expect(e['rules[1].model_regex']).toMatch(/无效/)
    expect(e['rules[1].value_regex']).toMatch(/无效/)
    expect(e['rules[1].ttl_seconds']).toBeTruthy()
    expect(e.max_entries).toBeTruthy()
    expect(e.default_ttl_seconds).toBeTruthy()
    expect(validateRule({ ...emptyRule(), key_sources: [] })).toMatchObject({ name: expect.any(String), key_sources: '至少需要一个 Key 来源' })
  })
})

describe('affinity table helpers', () => {
  it('derives modes, TTL, scope and key source labels', () => {
    const r = { ...emptyRule(), name: 'r' }
    expect(effectiveMode(r, 'strict')).toBe('strict') // inherit
    expect(modeIsOwn(r)).toBe(false)
    expect(effectiveMode({ ...r, session_mode: '' }, 'off')).toBe('off')
    expect(modeIsOwn({ ...r, session_mode: 'prefer' })).toBe(true)
    expect(ttlLabel(r)).toBe('全局默认')
    expect(ttlLabel({ ...r, ttl_seconds: 90 })).toBe('90s')
    expect(scopeBadges(r)).toEqual(['分组', '规则'])
    expect(scopeBadges({ ...r, include_using_group: false, include_rule_name: false, include_model_name: true })).toEqual(['模型'])
    expect(keySourceLabel({ type: 'gjson', path: 'prompt_cache_key' })).toEqual({ type: 'gjson', value: 'prompt_cache_key' })
    expect(keySourceLabel({ type: 'request_header', key: 'Session_id' })).toEqual({ type: 'header', value: 'Session_id' })
  })

  it('merges templates by name, duplicates and moves rules', () => {
    const current = [{ ...emptyRule(), name: 'codex cli trace', ttl_seconds: 5, model_regex: ['^gpt-.*$'] }, { ...emptyRule(), name: 'mine' }]
    const { rules, replaced, added } = mergeRules(current, omnigatePresets())
    expect([replaced, added]).toEqual([1, 1])
    expect(rules.map(r => r.name)).toEqual(['codex cli trace', 'mine', 'claude cli trace'])
    expect(rules[0]!.model_regex).toEqual(['.*'])
    expect(current[0]!.ttl_seconds).toBe(5) // inputs untouched
    expect(uniqueName('mine', rules)).toBe('mine (副本)')
    expect(uniqueName('mine', [...rules, { ...emptyRule(), name: 'mine (副本)' }])).toBe('mine (副本 2)')
    expect(moveRule(rules, 2, 0).map(r => r.name)).toEqual(['claude cli trace', 'codex cli trace', 'mine'])
    expect(moveRule(rules, 0, 5)).toEqual(rules)
  })

  it('computes affinity and cache hit rates', () => {
    expect(affinityHitRate(undefined)).toBeNull()
    expect(affinityHitRate({ new: 3, off: 2 })).toBeNull()
    expect(affinityHitRate({ hit: 6, rebound: 1, failover: 1, new: 9 })).toBe(0.75)
    expect(cacheHitRate({ inputTokens: 1000, cacheReadTokens: 300, cacheHitRate: 0.3 })).toBe(0.3)
    expect(cacheHitRate({ inputTokens: 1000, cacheReadTokens: 250 })).toBe(0.25)
    expect(cacheHitRate({ inputTokens: 0, cacheReadTokens: 0, cacheHitRate: null })).toBeNull()
    expect(cacheHitRate({ inputTokens: 10 })).toBeNull()
  })
})
