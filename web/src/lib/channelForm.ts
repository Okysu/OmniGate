// Pure helpers for the channel create / edit form.
import type { Channel, ChannelInput, ChannelScope, ChannelType, EnabledStatus, MaxTokensField, ModelMapping, SharedWith } from './types'
import { fromNano, isValidAmount, toNano } from './money'

export interface HeaderRow {
  key: string
  value: string
}

export interface ChannelFormState {
  name: string
  type: ChannelType
  baseUrl: string
  /** Only sent on create, or on edit when `replaceKey` is on. */
  apiKey: string
  replaceKey: boolean
  status: EnabledStatus
  priority: number
  weight: number
  models: ModelMapping[]
  headers: HeaderRow[]
  supportsResponses: boolean
  /** phase14: the upstream serves legacy /completions (FIM via `suffix`). */
  supportsCompletions: boolean
  /** 0 = backend default (60 s). */
  timeoutSeconds: number
  maxTokensField: MaxTokensField
  scope: ChannelScope
  /** phase8 §1.2: user ids and group ids. */
  sharedWith: SharedWith
  /** `alerts.balanceBelow` as typed ('' = no alert). */
  balanceBelow: string
}

export function emptyChannelForm(): ChannelFormState {
  return {
    name: '',
    type: 'openai',
    baseUrl: '',
    apiKey: '',
    replaceKey: true,
    status: 'enabled',
    priority: 0,
    weight: 1,
    models: [{ model: '', upstreamModel: '' }],
    headers: [],
    supportsResponses: false,
    supportsCompletions: false,
    timeoutSeconds: 0,
    maxTokensField: 'max_tokens',
    scope: 'private',
    sharedWith: { users: [], groups: [] },
    balanceBelow: '',
  }
}

/**
 * phase8 §1.2: `sharedWith` is `{users, groups}`; older backends (and inputs) use a plain
 * user-id list, which counts as users.
 */
export function normalizeSharedWith(v: SharedWith | string[] | null | undefined): SharedWith {
  const ids = (x: unknown) => (Array.isArray(x) ? x.filter((i): i is string => typeof i === 'string' && i !== '') : [])
  if (Array.isArray(v))
    return { users: ids(v), groups: [] }
  return { users: ids(v?.users), groups: ids(v?.groups) }
}

/** Total number of share targets. */
export function shareCount(v: SharedWith | string[] | null | undefined): number {
  const s = normalizeSharedWith(v)
  return s.users.length + s.groups.length
}

export function headersToRows(headers: Record<string, string> | undefined | null): HeaderRow[] {
  return Object.entries(headers ?? {}).map(([key, value]) => ({ key, value }))
}

/** Drops rows without a key; later duplicates win. */
export function rowsToHeaders(rows: HeaderRow[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const r of rows) {
    const k = r.key.trim()
    if (k)
      out[k] = r.value
  }
  return out
}

export function formFromChannel(c: Channel): ChannelFormState {
  return {
    name: c.name,
    type: c.type,
    baseUrl: c.baseUrl,
    apiKey: '',
    replaceKey: !c.secret.set,
    status: c.status,
    priority: c.priority,
    weight: c.weight,
    models: (c.models ?? []).map(m => ({ ...m })),
    headers: headersToRows(c.config.headers),
    supportsResponses: c.config.supportsResponses ?? false,
    supportsCompletions: c.config.supportsCompletions ?? false,
    timeoutSeconds: c.config.timeoutSeconds ?? 0,
    maxTokensField: c.config.maxTokensField ?? 'max_tokens',
    scope: c.scope,
    sharedWith: normalizeSharedWith(c.sharedWith),
    balanceBelow: c.alerts?.balanceBelow ?? '',
  }
}

/** Canonical threshold: null when empty, trailing zeros trimmed; invalid input is returned trimmed. */
export function normalizeBalanceThreshold(v: string | null | undefined): string | null {
  const s = (v ?? '').trim()
  if (!s)
    return null
  return isValidAmount(s) ? fromNano(toNano(s)) : s
}

/** Validation message for the upstream balance threshold, or null when valid / empty. */
export function balanceThresholdError(v: string): string | null {
  const s = v.trim()
  if (!s)
    return null
  return isValidAmount(s) ? null : '请输入不小于 0 的数字（最多 9 位小数），留空表示不告警'
}

/**
 * Builds the request body. Only writable fields are included (the backend
 * rejects unknown fields); `type` is omitted on update (immutable) and `apiKey`
 * only when it is being set.
 */
export function buildChannelPayload(form: ChannelFormState, original?: Channel): ChannelInput {
  const models = form.models
    .map(m => ({ model: m.model.trim(), upstreamModel: m.upstreamModel.trim(), upstreamProtocol: m.upstreamProtocol }))
    .filter(m => m.model !== '' || m.upstreamModel !== '')
    .map(m => ({
      model: m.model,
      upstreamModel: m.upstreamModel || m.model,
      ...(form.type === 'openai' && m.upstreamProtocol === 'responses' ? { upstreamProtocol: 'responses' as const } : {}),
    }))
  const headers = rowsToHeaders(form.headers)
  const config: ChannelInput['config'] = {}
  if (Object.keys(headers).length > 0)
    config.headers = headers
  if (form.type === 'openai') {
    if (form.supportsResponses)
      config.supportsResponses = true
    if (form.supportsCompletions)
      config.supportsCompletions = true
    if (form.maxTokensField !== 'max_tokens')
      config.maxTokensField = form.maxTokensField
  }
  if (form.timeoutSeconds > 0)
    config.timeoutSeconds = Math.floor(form.timeoutSeconds)

  const body: ChannelInput = {
    name: form.name.trim(),
    baseUrl: form.baseUrl.trim(),
    scope: form.scope,
    status: form.status,
    priority: Math.trunc(form.priority),
    weight: Math.trunc(form.weight),
    models,
    config,
  }
  if (form.scope === 'shared')
    body.sharedWith = { users: [...new Set(form.sharedWith.users)], groups: [...new Set(form.sharedWith.groups)] }
  // Round 6: `alerts` only when it changes, so older backends (which reject unknown
  // fields) keep accepting edits that do not touch the threshold.
  const threshold = normalizeBalanceThreshold(form.balanceBelow)
  if (threshold !== normalizeBalanceThreshold(original?.alerts?.balanceBelow))
    body.alerts = { balanceBelow: threshold }
  if (!original) {
    body.type = form.type
    body.apiKey = form.apiKey
  }
  else {
    body.version = original.version
    if (form.replaceKey)
      body.apiKey = form.apiKey
  }
  return body
}

export interface DiscoverDiff {
  /** Upstream IDs not yet mapped by any row (candidates to add). */
  added: string[]
  /** Upstream IDs already mapped. */
  existing: string[]
  /** Mapped upstream models that the upstream no longer lists. */
  missing: string[]
}

export function diffDiscoveredModels(current: ModelMapping[], discovered: string[]): DiscoverDiff {
  const mapped = new Set(current.map(m => (m.upstreamModel || m.model).trim()).filter(Boolean))
  const upstream = new Set(discovered)
  const added: string[] = []
  const existing: string[] = []
  for (const id of [...upstream].sort()) {
    if (mapped.has(id))
      existing.push(id)
    else
      added.push(id)
  }
  const missing = [...mapped].filter(id => !upstream.has(id)).sort()
  return { added, existing, missing }
}

/** Appends selected upstream IDs as identity mappings, skipping logical names already used. */
export function appendModels(current: ModelMapping[], ids: string[]): ModelMapping[] {
  const rows = current.filter(m => m.model.trim() !== '' || m.upstreamModel.trim() !== '')
  const used = new Set(rows.map(m => m.model.trim()))
  for (const id of ids) {
    if (!used.has(id)) {
      rows.push({ model: id, upstreamModel: id })
      used.add(id)
    }
  }
  return rows
}

/**
 * Maps backend `details` keys to form sections so the sheet can flag the
 * section containing an error.
 */
export function sectionOfField(field: string): 'basic' | 'credentials' | 'models' | 'routing' | 'alerts' | 'advanced' | 'sharing' | 'other' {
  if (field === 'name' || field === 'type' || field === 'baseUrl' || field === 'status')
    return 'basic'
  if (field === 'apiKey')
    return 'credentials'
  if (field === 'models')
    return 'models'
  if (field === 'priority' || field === 'weight')
    return 'routing'
  if (field.startsWith('config'))
    return 'advanced'
  if (field.startsWith('alerts'))
    return 'alerts'
  if (field === 'scope' || field === 'sharedWith' || field.startsWith('sharedWith.'))
    return 'sharing'
  return 'other'
}

/**
 * Whether a plugin version declares a capability with output kind `balance`
 * (the name is free-form, e.g. `balance.get`); upstream balance alerts are
 * raised from those results.
 */
export function hasBalanceOutput(capabilities: Record<string, { output: string }> | null | undefined): boolean {
  return !!capabilities && Object.values(capabilities).some(c => c?.output === 'balance')
}
