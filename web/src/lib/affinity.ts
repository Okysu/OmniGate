import type { AffinityConfig, AffinityKeySource, AffinityMode, AffinityOutcome, AffinityRule, AffinityRuleMode } from './types'

/**
 * Session affinity (`settings.gateway.affinity`, phase12-api.md): rules in new-api's
 * snake_case JSON shape, so new-api templates paste as-is. The backend validates
 * again (Go RE2 regexes); these helpers mirror its rules for inline errors.
 */

export const LIMITS = {
  rules: 50,
  keySources: 16,
  passHeaders: 64,
  patterns: 32,
  name: 64,
  path: 256,
  pattern: 512,
  ttl: 30 * 24 * 3600,
  maxEntries: 1_000_000,
} as const

export const MODES: AffinityMode[] = ['off', 'prefer', 'strict']
export const MODE_LABELS: Record<AffinityMode, string> = { off: '不保持', prefer: '优先保持', strict: '严格保持' }
export const MODE_DESCRIPTIONS: Record<AffinityMode, string> = {
  off: '只透传请求头，不把会话固定到渠道。',
  prefer: '优先使用会话上次的渠道；该渠道失败时按正常重试规则换渠道（推荐）。',
  strict: '只用会话上次的渠道；该渠道失败时直接返回错误，不换渠道（缓存优先，可用性较低）。',
}
/** Rule-level choices; '' is new-api's legacy form (decided by skip_retry_on_failure). */
export const RULE_MODES: AffinityRuleMode[] = ['inherit', 'off', 'prefer', 'strict', '']
export const RULE_MODE_LABELS: Record<AffinityRuleMode, string> = {
  'inherit': '继承全局',
  'off': MODE_LABELS.off,
  'prefer': MODE_LABELS.prefer,
  'strict': MODE_LABELS.strict,
  '': '兼容 new-api（按「失败不重试」）',
}

/** Forbidden pass_headers (same list as channel headers on the server). */
const FORBIDDEN = new Set(['authorization', 'x-api-key', 'host', 'content-length', 'connection', 'transfer-encoding', 'cookie', 'te', 'upgrade',
  'keep-alive', 'proxy-authorization', 'proxy-connection', 'content-type', 'accept-encoding'])
const HEADER_RE = /^[\w-]{1,64}$/

export function isForbiddenHeader(name: string): boolean {
  return FORBIDDEN.has(name.trim().toLowerCase())
}
export function isValidHeaderName(name: string): boolean {
  return HEADER_RE.test(name)
}

const CODEX_HEADERS = ['Originator', 'Session_id', 'Thread_id', 'Session-Id', 'Thread-Id', 'X-Client-Request-Id', 'User-Agent', 'X-Codex-Beta-Features',
  'X-Codex-Turn-State', 'X-Codex-Turn-Metadata', 'X-Codex-Window-Id', 'X-Codex-Parent-Thread-Id', 'X-OpenAI-Subagent', 'X-OpenAI-Memgen-Request',
  'X-ResponsesAPI-Include-Timing-Metrics', 'X-OpenAI-Internal-Codex-Responses-Lite']
const CLAUDE_HEADERS = ['X-Stainless-Arch', 'X-Stainless-Lang', 'X-Stainless-Os', 'X-Stainless-Package-Version', 'X-Stainless-Retry-Count',
  'X-Stainless-Runtime', 'X-Stainless-Runtime-Version', 'X-Stainless-Timeout', 'User-Agent', 'X-App', 'Anthropic-Beta',
  'Anthropic-Dangerous-Direct-Browser-Access', 'Anthropic-Version', 'X-Claude-Code-Session-Id']

function passTemplate(headers: string[]) {
  return { operations: [{ mode: 'pass_headers' as const, value: [...headers], keep_origin: true }] }
}

/** OmniGate's presets (the server default): any model, prefer, also matching the session headers. */
export function omnigatePresets(): AffinityRule[] {
  return [
    {
      ...emptyRule(),
      name: 'codex cli trace',
      model_regex: ['.*'],
      path_regex: ['^/v1/responses'],
      key_sources: [{ type: 'gjson', path: 'prompt_cache_key' }, { type: 'request_header', key: 'Session_id' }, { type: 'request_header', key: 'Session-Id' }],
      param_override_template: passTemplate(CODEX_HEADERS),
      session_mode: 'prefer',
    },
    {
      ...emptyRule(),
      name: 'claude cli trace',
      model_regex: ['.*'],
      path_regex: ['^/v1/messages'],
      key_sources: [{ type: 'gjson', path: 'metadata.user_id' }, { type: 'request_header', key: 'X-Claude-Code-Session-Id' }],
      param_override_template: passTemplate(CLAUDE_HEADERS),
      session_mode: 'prefer',
    },
  ]
}

export function emptyRule(): AffinityRule {
  return {
    name: '',
    model_regex: [],
    path_regex: [],
    user_agent_include: [],
    key_sources: [{ type: 'gjson', path: '' }],
    value_regex: '',
    ttl_seconds: 0,
    param_override_template: null,
    skip_retry_on_failure: false,
    session_mode: 'inherit',
    include_using_group: true,
    include_model_name: false,
    include_rule_name: true,
  }
}

export function defaultConfig(): AffinityConfig {
  return { enabled: true, session_mode: 'prefer', switch_on_success: true, keep_on_channel_disabled: false, max_entries: 100_000, default_ttl_seconds: 3600, rules: omnigatePresets() }
}

/** The effective mode of a rule (server semantics, `Rule.Mode`). */
export function effectiveMode(rule: AffinityRule, global: AffinityMode): AffinityMode {
  switch (rule.session_mode) {
    case 'off':
    case 'prefer':
    case 'strict':
      return rule.session_mode
    case '':
      return rule.skip_retry_on_failure ? 'strict' : global
    default:
      return global
  }
}

/** "规则单独设置" when the rule decides its mode itself (explicit, or legacy strict). */
export function modeIsOwn(rule: AffinityRule): boolean {
  return rule.session_mode === 'off' || rule.session_mode === 'prefer' || rule.session_mode === 'strict' || (rule.session_mode === '' && rule.skip_retry_on_failure)
}

/** pass_headers of a rule (merged over operations, case-insensitively deduped). */
export function passHeaders(rule: AffinityRule): { names: string[], keepOrigin: boolean } {
  const names: string[] = []
  let keepOrigin = false
  for (const op of rule.param_override_template?.operations ?? []) {
    for (const h of op.value) {
      if (!names.some(n => n.toLowerCase() === h.toLowerCase()))
        names.push(h)
    }
    keepOrigin ||= op.keep_origin
  }
  return { names, keepOrigin }
}

/** Sets a rule's pass_headers (one operation; none = no template). */
export function withPassHeaders(rule: AffinityRule, names: string[], keepOrigin: boolean): AffinityRule {
  return { ...rule, param_override_template: names.length ? { operations: [{ mode: 'pass_headers', value: [...names], keep_origin: keepOrigin }] } : null }
}

export function ttlLabel(rule: AffinityRule): string {
  return rule.ttl_seconds > 0 ? `${rule.ttl_seconds}s` : '全局默认'
}

/** 作用域 badges from the include_* flags (the user is always part of the key). */
export function scopeBadges(rule: AffinityRule): string[] {
  const out: string[] = []
  if (rule.include_using_group)
    out.push('分组')
  if (rule.include_model_name)
    out.push('模型')
  if (rule.include_rule_name)
    out.push('规则')
  return out
}

export function keySourceLabel(ks: AffinityKeySource): { type: string, value: string } {
  return ks.type === 'gjson' ? { type: 'gjson', value: ks.path ?? '' } : ks.type === 'request_header' ? { type: 'header', value: ks.key ?? '' } : { type: ks.type, value: ks.key ?? ks.path ?? '' }
}

/** A name not used by any rule: "x (副本)", "x (副本 2)", … */
export function uniqueName(base: string, rules: readonly AffinityRule[]): string {
  const taken = new Set(rules.map(r => r.name))
  let name = `${base} (副本)`
  for (let i = 2; taken.has(name); i++)
    name = `${base} (副本 ${i})`
  return name.slice(0, LIMITS.name)
}

/** Adds template rules: a rule with the same name is replaced in place, others are appended. */
export function mergeRules(current: readonly AffinityRule[], add: readonly AffinityRule[]): { rules: AffinityRule[], replaced: number, added: number } {
  const rules = current.map(r => structuredClone(r))
  let replaced = 0
  let added = 0
  for (const r of add) {
    const i = rules.findIndex(x => x.name === r.name)
    if (i >= 0) {
      rules[i] = structuredClone(r)
      replaced++
    }
    else {
      rules.push(structuredClone(r))
      added++
    }
  }
  return { rules, replaced, added }
}

export function moveRule(rules: readonly AffinityRule[], from: number, to: number): AffinityRule[] {
  const out = [...rules]
  if (from < 0 || from >= out.length || to < 0 || to >= out.length)
    return out
  const [r] = out.splice(from, 1)
  out.splice(to, 0, r!)
  return out
}

// ---------- normalisation (paste new-api JSON) ----------

type Obj = Record<string, unknown>
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v)
const str = (v: unknown) => (typeof v === 'string' ? v : '')
const strList = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').map(s => s.trim()).filter(Boolean) : [])

function normalizeRule(raw: unknown): AffinityRule {
  const r = isObj(raw) ? raw : {}
  const tpl = isObj(r.param_override_template) ? r.param_override_template : null
  const ops = tpl && Array.isArray(tpl.operations)
    ? tpl.operations.filter(isObj).map(op => ({
        mode: str(op.mode) as 'pass_headers',
        value: typeof op.value === 'string' ? op.value.split(',').map(s => s.trim()).filter(Boolean) : strList(op.value),
        keep_origin: op.keep_origin === true,
      }))
    : []
  return {
    name: str(r.name).trim(),
    model_regex: strList(r.model_regex),
    path_regex: strList(r.path_regex),
    user_agent_include: strList(r.user_agent_include),
    key_sources: Array.isArray(r.key_sources)
      ? r.key_sources.filter(isObj).map(ks => ({ type: str(ks.type), ...(ks.key !== undefined ? { key: str(ks.key).trim() } : {}), ...(ks.path !== undefined ? { path: str(ks.path).trim() } : {}) }))
      : [],
    value_regex: str(r.value_regex).trim(),
    ttl_seconds: typeof r.ttl_seconds === 'number' ? r.ttl_seconds : 0,
    param_override_template: ops.length ? { operations: ops } : null,
    skip_retry_on_failure: r.skip_retry_on_failure === true,
    session_mode: (typeof r.session_mode === 'string' ? r.session_mode : '') as AffinityRuleMode,
    include_using_group: r.include_using_group === true,
    include_model_name: r.include_model_name === true,
    include_rule_name: r.include_rule_name === true,
  }
}

/**
 * Normalises a setting document like the server: missing globals take their defaults,
 * null arrays become [], a comma-separated pass_headers value becomes a list.
 * Unknown fields are dropped (new-api documents paste as-is).
 */
export function normalizeConfig(raw: unknown): AffinityConfig {
  const c = isObj(raw) ? raw : {}
  const d = defaultConfig()
  const bool = (k: string, def: boolean) => (typeof c[k] === 'boolean' ? c[k] as boolean : def)
  const num = (k: string, def: number) => (typeof c[k] === 'number' ? c[k] as number : def)
  return {
    enabled: bool('enabled', d.enabled),
    session_mode: (typeof c.session_mode === 'string' && c.session_mode !== '' ? c.session_mode : d.session_mode) as AffinityMode,
    switch_on_success: bool('switch_on_success', d.switch_on_success),
    keep_on_channel_disabled: bool('keep_on_channel_disabled', d.keep_on_channel_disabled),
    max_entries: num('max_entries', d.max_entries),
    default_ttl_seconds: num('default_ttl_seconds', d.default_ttl_seconds),
    rules: Array.isArray(c.rules) ? c.rules.map(normalizeRule) : [],
  }
}

export function stringifyConfig(c: AffinityConfig): string {
  return JSON.stringify(c, null, 2)
}

/** Parses the JSON editor's text: the normalised config, or a syntax error. */
export function parseConfigJson(text: string): { config: AffinityConfig | null, error: string | null } {
  let raw: unknown
  try {
    raw = JSON.parse(text)
  }
  catch (e) {
    return { config: null, error: `JSON 格式错误：${e instanceof Error ? e.message : String(e)}` }
  }
  if (!isObj(raw))
    return { config: null, error: '必须是 JSON 对象（会话亲和设置）' }
  return { config: normalizeConfig(raw), error: null }
}

export function configEquals(a: AffinityConfig, b: AffinityConfig): boolean {
  return stringifyConfig(normalizeConfig(a)) === stringifyConfig(normalizeConfig(b))
}

// ---------- validation (mirrors the server; error keys are field paths) ----------

function regexError(p: string): string | null {
  if (p.length > LIMITS.pattern)
    return `正则最多 ${LIMITS.pattern} 个字符`
  try {
    new RegExp(p)
    return null
  }
  catch {
    return `正则 ${JSON.stringify(p)} 无效`
  }
}

export function validateRule(r: AffinityRule, p = ''): Record<string, string> {
  const e: Record<string, string> = {}
  const name = r.name.trim()
  if (!name || name.length > LIMITS.name)
    e[`${p}name`] = `必填，最多 ${LIMITS.name} 个字符`
  for (const f of ['model_regex', 'path_regex'] as const) {
    if (r[f].length > LIMITS.patterns)
      e[`${p}${f}`] = `最多 ${LIMITS.patterns} 个正则`
    for (const pat of r[f]) {
      const msg = regexError(pat)
      if (msg) {
        e[`${p}${f}`] = msg
        break
      }
    }
  }
  if (r.user_agent_include.length > LIMITS.patterns)
    e[`${p}user_agent_include`] = `最多 ${LIMITS.patterns} 项`
  if (!r.key_sources.length)
    e[`${p}key_sources`] = '至少需要一个 Key 来源'
  else if (r.key_sources.length > LIMITS.keySources)
    e[`${p}key_sources`] = `最多 ${LIMITS.keySources} 个 Key 来源`
  r.key_sources.forEach((ks, i) => {
    const f = `${p}key_sources[${i}]`
    if (ks.type === 'gjson') {
      if (!ks.path?.trim() || ks.path.length > LIMITS.path)
        e[`${f}.path`] = 'gjson 来源需要 path（请求体 JSON 路径）'
    }
    else if (ks.type === 'request_header') {
      if (!isValidHeaderName(ks.key?.trim() ?? ''))
        e[`${f}.key`] = 'request_header 来源需要合法的请求头名称'
    }
    else if (ks.type === 'context_int' || ks.type === 'context_string') {
      e[`${f}.type`] = `${ks.type} 是 new-api 内部上下文类型，OmniGate 不支持；请改用 gjson 或 request_header`
    }
    else {
      e[`${f}.type`] = '只能是 gjson 或 request_header'
    }
  })
  if (r.value_regex) {
    const msg = regexError(r.value_regex)
    if (msg)
      e[`${p}value_regex`] = msg
  }
  if (!Number.isInteger(r.ttl_seconds) || r.ttl_seconds < 0 || r.ttl_seconds > LIMITS.ttl)
    e[`${p}ttl_seconds`] = `范围为 0–${LIMITS.ttl} 秒（0 = 使用全局默认）`
  if (!RULE_MODES.includes(r.session_mode))
    e[`${p}session_mode`] = '只能是空、inherit、off、prefer、strict'
  r.param_override_template?.operations.forEach((op, i) => {
    const f = `${p}param_override_template.operations[${i}]`
    if (op.mode !== 'pass_headers') {
      e[`${f}.mode`] = `不支持 ${JSON.stringify(op.mode)}（OmniGate 只支持 pass_headers）`
      return
    }
    if (!op.value.length)
      e[`${f}.value`] = '至少需要一个请求头'
    else if (op.value.length > LIMITS.passHeaders)
      e[`${f}.value`] = `最多 ${LIMITS.passHeaders} 个请求头`
    const bad = op.value.find(h => !isValidHeaderName(h))
    const forbidden = op.value.find(isForbiddenHeader)
    if (bad)
      e[`${f}.value`] = `请求头 ${JSON.stringify(bad)} 格式无效`
    else if (forbidden)
      e[`${f}.value`] = `请求头 ${JSON.stringify(forbidden)} 不允许透传（凭据、Cookie、Host 等由网关管理）`
  })
  return e
}

export function validateConfig(c: AffinityConfig): Record<string, string> {
  const e: Record<string, string> = {}
  if (!MODES.includes(c.session_mode))
    e.session_mode = '只能是 off、prefer、strict'
  if (!Number.isInteger(c.max_entries) || c.max_entries < 1 || c.max_entries > LIMITS.maxEntries)
    e.max_entries = `范围为 1–${LIMITS.maxEntries}`
  if (!Number.isInteger(c.default_ttl_seconds) || c.default_ttl_seconds < 1 || c.default_ttl_seconds > LIMITS.ttl)
    e.default_ttl_seconds = `范围为 1–${LIMITS.ttl} 秒（30 天）`
  if (c.rules.length > LIMITS.rules)
    e.rules = `最多 ${LIMITS.rules} 条规则`
  const seen = new Set<string>()
  c.rules.forEach((r, i) => {
    Object.assign(e, validateRule(r, `rules[${i}].`))
    if (r.name && seen.has(r.name))
      e[`rules[${i}].name`] = `规则名称 ${JSON.stringify(r.name)} 重复`
    seen.add(r.name)
  })
  return e
}

/** "rules[2].key_sources[0].type：…" lines for the JSON editor. */
export function errorLines(errors: Record<string, string>): string[] {
  return Object.entries(errors).map(([k, v]) => `${k}：${v}`)
}

/** Errors of rule index i, with the "rules[i]." prefix removed. */
export function ruleErrors(errors: Record<string, string>, i: number): Record<string, string> {
  const prefix = `rules[${i}].`
  return Object.fromEntries(Object.entries(errors).filter(([k]) => k.startsWith(prefix)).map(([k, v]) => [k.slice(prefix.length), v]))
}

// ---------- rule form (edit sheet) ----------

export interface RuleForm {
  name: string
  /** One regex per line. */
  modelRegex: string
  pathRegex: string
  userAgent: string[]
  keySources: { type: 'gjson' | 'request_header', value: string }[]
  valueRegex: string
  ttl: string
  headers: string[]
  keepOrigin: boolean
  mode: AffinityRuleMode
  skipRetry: boolean
  includeGroup: boolean
  includeModel: boolean
  includeRule: boolean
}

const lines = (s: string) => s.split('\n').map(x => x.trim()).filter(Boolean)

export function ruleToForm(r: AffinityRule): RuleForm {
  const pass = passHeaders(r)
  return {
    name: r.name,
    modelRegex: r.model_regex.join('\n'),
    pathRegex: r.path_regex.join('\n'),
    userAgent: [...r.user_agent_include],
    keySources: r.key_sources.map(ks => ks.type === 'request_header' ? { type: 'request_header' as const, value: ks.key ?? '' } : { type: 'gjson' as const, value: ks.path ?? ks.key ?? '' }),
    valueRegex: r.value_regex,
    ttl: String(r.ttl_seconds || ''),
    headers: pass.names,
    keepOrigin: r.param_override_template ? pass.keepOrigin : true,
    mode: r.session_mode,
    skipRetry: r.skip_retry_on_failure,
    includeGroup: r.include_using_group,
    includeModel: r.include_model_name,
    includeRule: r.include_rule_name,
  }
}

export function formToRule(f: RuleForm): AffinityRule {
  const ttl = f.ttl.trim() === '' ? 0 : Number(f.ttl)
  return withPassHeaders({
    name: f.name.trim(),
    model_regex: lines(f.modelRegex),
    path_regex: lines(f.pathRegex),
    user_agent_include: f.userAgent.map(s => s.trim()).filter(Boolean),
    key_sources: f.keySources.map(ks => ks.type === 'gjson' ? { type: 'gjson', path: ks.value.trim() } : { type: 'request_header', key: ks.value.trim() }),
    value_regex: f.valueRegex.trim(),
    ttl_seconds: ttl,
    param_override_template: null,
    // Only the legacy mode reads skip_retry_on_failure.
    skip_retry_on_failure: f.mode === '' ? f.skipRetry : false,
    session_mode: f.mode,
    include_using_group: f.includeGroup,
    include_model_name: f.includeModel,
    include_rule_name: f.includeRule,
  }, f.headers, f.keepOrigin)
}

// ---------- request log outcomes (phase12-api.md §4) ----------

export const OUTCOMES: AffinityOutcome[] = ['hit', 'new', 'rebound', 'failover', 'broken', 'strict_failed', 'miss', 'off']
export const OUTCOME_LABELS: Record<AffinityOutcome, string> = {
  hit: '命中',
  new: '新绑定',
  rebound: '改绑',
  failover: '未命中',
  broken: '绑定失效',
  strict_failed: '严格失败',
  miss: '未绑定',
  off: '仅透传',
}
export const OUTCOME_HINTS: Record<AffinityOutcome, string> = {
  hit: '由会话绑定的渠道处理',
  new: '会话还没有绑定：处理该请求的渠道成为绑定渠道',
  rebound: '绑定渠道失败或未被选中，由其他渠道处理，绑定改到该渠道',
  failover: '未由绑定渠道处理（失败或回退），绑定保持不变',
  broken: '绑定的渠道不可用（停用、熔断或不再可用），按正常路由处理',
  strict_failed: '严格保持：绑定渠道失败，直接返回错误，没有换渠道',
  miss: '会话还没有绑定，且请求失败（没有建立绑定）',
  off: '规则生效但未开启会话保持：只透传请求头',
}
export const OUTCOME_CLASSES: Record<AffinityOutcome, string> = {
  hit: 'border-emerald-500/50 text-emerald-700 dark:text-emerald-400',
  new: 'border-sky-500/50 text-sky-700 dark:text-sky-400',
  rebound: 'border-violet-500/50 text-violet-700 dark:text-violet-400',
  failover: 'border-amber-500/50 text-amber-700 dark:text-amber-400',
  broken: 'border-amber-500/50 text-amber-700 dark:text-amber-400',
  strict_failed: 'border-destructive/50 text-destructive',
  miss: 'text-muted-foreground',
  off: 'text-muted-foreground',
}

export function isOutcome(v: unknown): v is AffinityOutcome {
  return typeof v === 'string' && (OUTCOMES as string[]).includes(v)
}

/** Share of affinity requests served by their bound channel: hit / (hit + rebound + failover + broken + strict_failed); null without bound sessions. */
export function affinityHitRate(counts: Partial<Record<AffinityOutcome, number>> | undefined): number | null {
  if (!counts)
    return null
  const hit = counts.hit ?? 0
  const bound = hit + (counts.rebound ?? 0) + (counts.failover ?? 0) + (counts.broken ?? 0) + (counts.strict_failed ?? 0)
  return bound > 0 ? hit / bound : null
}
