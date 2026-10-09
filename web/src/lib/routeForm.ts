// Route rule form state, client-side validation (contract §2 limits) and
// payload mapping, plus model glob helpers used by the editor and the list.
import type {
  ChannelView,
  ProtocolPreference,
  RetryOn,
  Role,
  RouteRule,
  RouteRuleInput,
  RouteRulePatch,
  RouteStrategy,
} from './types'
import { DEFAULT_RETRY_ON, normalizeRetryOn } from './retry'

// Retry classes moved to lib/retry.ts (shared with the system settings); re-exported for callers.
export { DEFAULT_RETRY_ON, RETRY_ON, RETRY_ON_LABELS } from './retry'

export const MAX_NAME = 100
export const MAX_DESCRIPTION = 500
export const MAX_MATCH_MODELS = 100
export const MAX_MODEL_LEN = 128
export const MAX_TARGETS = 50
export const MAX_FALLBACKS = 5
export const MAX_ATTEMPTS = 5
/** Server accepts ±1e6 for overrides (channels themselves use -1000–1000). */
export const PRIORITY_RANGE = [-1000000, 1000000] as const
export const WEIGHT_RANGE = [1, 1000] as const

export const STRATEGIES: RouteStrategy[] = ['priority', 'weighted', 'round_robin', 'least_latency', 'lowest_cost']

export const STRATEGY_LABELS: Record<RouteStrategy, string> = {
  priority: '优先级',
  weighted: '权重',
  round_robin: '轮询',
  least_latency: '最低延迟',
  lowest_cost: '最低成本',
}

export const STRATEGY_DESCRIPTIONS: Record<RouteStrategy, string> = {
  priority: '先尝试优先级最高的一组渠道，组内按权重随机；失败后回退到下一优先级。与默认路由相同。',
  weighted: '忽略优先级，所有候选渠道按权重随机选择，适合按比例分流。',
  round_robin: '按顺序轮流使用候选渠道（每个网关实例单独计数），流量均匀分摊。',
  least_latency: '按近期首字节延迟从低到高尝试；还没有延迟数据的渠道排在最后。',
  lowest_cost: '按该渠道该模型的成本价（输入 + 输出单价之和）从低到高尝试；没有成本价的渠道排在最后。',
}

export const HOP_LABELS: Record<number, string> = {
  0: '直通',
  1: '一次转换',
  2: '两次转换',
}

export const BREAKER_LABELS: Record<string, string> = {
  closed: '正常',
  open: '熔断',
  half_open: '半开',
}

export const PREVIEW_INBOUNDS = ['openai.chat', 'openai.responses', 'anthropic.messages', 'openai.embeddings', 'openai.images.generations', 'openai.audio.transcriptions', 'openai.audio.speech'] as const

// ---------------------------------------------------------------------------
// Globs
// ---------------------------------------------------------------------------

/** True when the pattern contains a `*` wildcard. */
export function isGlob(pattern: string): boolean {
  return pattern.includes('*')
}

const globCache = new Map<string, RegExp>()

/** `*` matches any run of characters (including none); everything else is literal. */
export function globToRegExp(pattern: string): RegExp {
  let re = globCache.get(pattern)
  if (!re) {
    const body = pattern.split('*').map(part => part.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*')
    re = new RegExp(`^${body}$`)
    globCache.set(pattern, re)
  }
  return re
}

export function matchesPattern(pattern: string, model: string): boolean {
  return isGlob(pattern) ? globToRegExp(pattern).test(model) : pattern === model
}

/** True when any of `patterns` matches `model`. */
export function matchesAny(patterns: readonly string[], model: string): boolean {
  return patterns.some(p => matchesPattern(p, model))
}

/** Models (from `models`) matched by `pattern`, in input order. */
export function modelsMatching(pattern: string, models: readonly string[]): string[] {
  return models.filter(m => matchesPattern(pattern, m))
}

/** Logical models a channel serves that the patterns match. */
export function channelServedModels(channel: Pick<ChannelView, 'models'>, patterns: readonly string[]): string[] {
  const names = (channel.models ?? []).map(m => m.model)
  return [...new Set(names.filter(n => matchesAny(patterns, n)))]
}

// ---------------------------------------------------------------------------
// Form state
// ---------------------------------------------------------------------------

export interface TargetRow {
  /** Stable key for v-for (not sent). */
  uid: number
  channelId: string
  /** '' = keep the channel's own value. */
  priority: string
  weight: string
}

export interface RouteFormState {
  name: string
  description: string
  enabled: boolean
  models: string[]
  roles: Role[]
  strategy: RouteStrategy
  nativeFirst: boolean
  targets: TargetRow[]
  maxAttempts: number | string
  retryOn: RetryOn[]
  fallbackModels: string[]
}

let uidSeq = 0
export function nextUid(): number {
  uidSeq += 1
  return uidSeq
}

export function emptyTarget(channelId = ''): TargetRow {
  return { uid: nextUid(), channelId, priority: '', weight: '' }
}

export function emptyRouteForm(): RouteFormState {
  return {
    name: '',
    description: '',
    enabled: true,
    models: [],
    roles: [],
    strategy: 'priority',
    nativeFirst: true,
    targets: [],
    maxAttempts: 3,
    retryOn: [...DEFAULT_RETRY_ON],
    fallbackModels: [],
  }
}

export function formFromRule(r: RouteRule): RouteFormState {
  return {
    name: r.name,
    description: r.description ?? '',
    enabled: r.enabled,
    models: [...(r.match?.models ?? [])],
    roles: [...(r.match?.roles ?? [])],
    strategy: r.strategy,
    nativeFirst: r.protocolPreference !== 'ignore',
    targets: (r.targets ?? []).map(t => ({
      uid: nextUid(),
      channelId: t.channelId,
      priority: t.priority === null || t.priority === undefined ? '' : String(t.priority),
      weight: t.weight === null || t.weight === undefined ? '' : String(t.weight),
    })),
    maxAttempts: r.retry?.maxAttempts ?? 3,
    retryOn: normalizeRetryOn(r.retry?.retryOn),
    fallbackModels: [...(r.fallbackModels ?? [])],
  }
}

/** Copy of a rule as a new, disabled draft ("名称（副本）"). */
export function duplicateForm(r: RouteRule): RouteFormState {
  const f = formFromRule(r)
  const suffix = '（副本）'
  f.name = `${r.name.slice(0, MAX_NAME - suffix.length)}${suffix}`
  f.enabled = false
  return f
}

function optionalInt(v: string): number | null {
  const s = v.trim()
  return s === '' ? null : Number(s)
}

function cleanList(list: readonly string[]): string[] {
  const out: string[] = []
  for (const raw of list) {
    const v = raw.trim()
    if (v && !out.includes(v))
      out.push(v)
  }
  return out
}

/** Body for POST (and, with `version`, PATCH). */
export function buildRoutePayload(f: RouteFormState): RouteRuleInput {
  const retryOn = normalizeRetryOn(f.retryOn)
  return {
    name: f.name.trim(),
    description: f.description.trim(),
    enabled: f.enabled,
    match: { models: cleanList(f.models), roles: [...new Set(f.roles)] },
    targets: f.targets
      .filter(t => t.channelId)
      .map(t => ({ channelId: t.channelId, priority: optionalInt(t.priority), weight: optionalInt(t.weight) })),
    strategy: f.strategy,
    protocolPreference: (f.nativeFirst ? 'native_first' : 'ignore') satisfies ProtocolPreference,
    retry: { maxAttempts: Number(f.maxAttempts), retryOn },
    fallbackModels: cleanList(f.fallbackModels),
  }
}

export function buildRoutePatch(f: RouteFormState, version: number): RouteRulePatch {
  return { ...buildRoutePayload(f), version }
}

function intError(raw: string, [min, max]: readonly [number, number]): string | null {
  const s = raw.trim()
  if (s === '')
    return null
  const n = Number(s)
  if (!Number.isInteger(n) || n < min || n > max)
    return `范围为 ${min} 到 ${max} 的整数`
  return null
}

/** Client-side checks; keys match the server's 422 `details` (e.g. `targets[0].weight`). */
export function validateRouteForm(f: RouteFormState): Record<string, string> {
  const e: Record<string, string> = {}
  const name = f.name.trim()
  if (!name || name.length > MAX_NAME)
    e.name = `长度应为 1–${MAX_NAME} 个字符`
  if (f.description.trim().length > MAX_DESCRIPTION)
    e.description = `最多 ${MAX_DESCRIPTION} 个字符`

  const models = cleanList(f.models)
  if (models.length === 0)
    e['match.models'] = '至少匹配一个模型（可以使用通配，如 gpt-* 或 *）'
  else if (models.length > MAX_MATCH_MODELS)
    e['match.models'] = `最多 ${MAX_MATCH_MODELS} 个`
  else if (models.some(m => m.length > MAX_MODEL_LEN || /\s/.test(m)))
    e['match.models'] = `模型名不能包含空白，且不超过 ${MAX_MODEL_LEN} 个字符`

  const targets = f.targets
  if (targets.length > MAX_TARGETS)
    e.targets = `最多 ${MAX_TARGETS} 个目标渠道`
  const seen = new Set<string>()
  targets.forEach((t, i) => {
    if (!t.channelId)
      e[`targets[${i}].channelId`] = '请选择渠道'
    else if (seen.has(t.channelId))
      e[`targets[${i}].channelId`] = '渠道重复'
    seen.add(t.channelId)
    const pe = intError(t.priority, PRIORITY_RANGE)
    if (pe)
      e[`targets[${i}].priority`] = pe
    const we = intError(t.weight, WEIGHT_RANGE)
    if (we)
      e[`targets[${i}].weight`] = we
  })

  const n = Number(f.maxAttempts)
  if (String(f.maxAttempts).trim() === '' || !Number.isInteger(n) || n < 1 || n > MAX_ATTEMPTS)
    e['retry.maxAttempts'] = `范围为 1 到 ${MAX_ATTEMPTS} 的整数（含首次请求）`

  const fallbacks = cleanList(f.fallbackModels)
  if (fallbacks.length > MAX_FALLBACKS)
    e.fallbackModels = `最多 ${MAX_FALLBACKS} 个回退模型`
  else if (fallbacks.some(isGlob))
    e.fallbackModels = '回退模型必须是具体的模型名，不能使用通配'
  else if (fallbacks.some(m => m.length > MAX_MODEL_LEN || /\s/.test(m)))
    e.fallbackModels = `模型名不能包含空白，且不超过 ${MAX_MODEL_LEN} 个字符`
  return e
}

/** `targets[3].weight` → `{ index: 3, field: 'weight' }`. */
export function targetErrorKey(key: string): { index: number, field: string } | null {
  const m = /^targets\[(\d+)\](?:\.(\w+))?$/.exec(key)
  if (!m)
    return null
  return { index: Number(m[1]), field: m[2] ?? '' }
}

/** Errors of one target row, keyed by field ('' = the row itself). */
export function targetErrors(errors: Record<string, string>, index: number): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(errors)) {
    const m = targetErrorKey(k)
    if (m && m.index === index)
      out[m.field] = v
  }
  return out
}

/** Field keys rendered inline by the editor (others are listed at the bottom). */
export function isInlineRouteErrorKey(key: string, targetCount: number): boolean {
  const known = new Set(['name', 'description', 'enabled', 'match', 'match.models', 'match.roles', 'targets', 'strategy', 'protocolPreference', 'retry', 'retry.maxAttempts', 'retry.retryOn', 'fallbackModels'])
  if (known.has(key) || /^match\.models\[\d+\]$/.test(key) || /^fallbackModels\[\d+\]$/.test(key))
    return true
  const t = targetErrorKey(key)
  return t !== null && t.index < targetCount
}

/** First server error for a list field, including its indexed children (`match.models[2]`). */
export function listError(errors: Record<string, string>, key: string): string | undefined {
  if (errors[key])
    return errors[key]
  const prefix = `${key}[`
  for (const [k, v] of Object.entries(errors)) {
    if (k.startsWith(prefix))
      return v
  }
  return undefined
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

/** New array with the item at `from` moved to `to` (indices clamped). */
export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  const out = [...list]
  if (from < 0 || from >= out.length)
    return out
  const target = Math.max(0, Math.min(out.length - 1, to))
  const [item] = out.splice(from, 1)
  out.splice(target, 0, item as T)
  return out
}

/** Rules sorted by `position` (stable for equal positions). */
export function sortRules(rules: readonly RouteRule[]): RouteRule[] {
  return rules.map((r, i) => ({ r, i })).sort((a, b) => (a.r.position - b.r.position) || (a.i - b.i)).map(x => x.r)
}

/** Short human summary of a rule's targets. */
export function targetsSummary(rule: Pick<RouteRule, 'targets'>, channelName: (id: string) => string | undefined): string {
  const t = rule.targets ?? []
  if (t.length === 0)
    return '全部可用渠道'
  const names = t.map(x => channelName(x.channelId) ?? '未知渠道')
  return names.length <= 2 ? names.join('、') : `${names.slice(0, 2).join('、')} 等 ${names.length} 个`
}
