import type { ClientInfo, ClientKind, LogClient } from './types'

/**
 * Client detection (phase13-api.md). The known client list lives in one place: the backend's
 * `clientdetect.Known`, served by `GET /api/clients` and cached by `useClients()`. This module
 * only holds display helpers; nothing here hardcodes client ids except `UNKNOWN_CLIENT`.
 */

/** Requests no detection rule recognised (and rows logged before detection existed). */
export const UNKNOWN_CLIENT = 'unknown'

export const CLIENT_KINDS: ClientKind[] = ['agent', 'chat', 'sdk', 'tool', 'unknown']
export const KIND_LABELS: Record<ClientKind, string> = {
  agent: '编码代理',
  chat: '聊天客户端',
  sdk: 'SDK',
  tool: 'HTTP 工具',
  unknown: '未识别',
}

function kindOf(v: unknown): ClientKind {
  return typeof v === 'string' && (CLIENT_KINDS as string[]).includes(v) ? v as ClientKind : 'unknown'
}

/** "Claude Code 2.0.14" (name only without a version). */
export function clientLabel(c: Pick<LogClient, 'name' | 'version'>): string {
  return c.version ? `${c.name} ${c.version}` : c.name
}

/** Tooltip of a client badge: name, version, kind and id. */
export function clientTitle(c: LogClient, kind?: ClientKind): string {
  const parts = [c.name, c.version ? `版本 ${c.version}` : '版本未知']
  if (kind)
    parts.push(KIND_LABELS[kind])
  return `${parts.join(' · ')}（${c.id}）`
}

/** The kind of a client id in the known list ('unknown' when not listed). */
export function clientKind(list: readonly ClientInfo[], id: string | null | undefined): ClientKind {
  return kindOf(list.find(c => c.id === id)?.kind)
}

/** Display name of a client id: from the known list, else the id itself. */
export function clientName(list: readonly ClientInfo[], id: string): string {
  return list.find(c => c.id === id)?.name ?? id
}

/** The known clients grouped by kind, in kind order (filter select groups). */
export function groupClients(list: readonly ClientInfo[]): { kind: ClientKind, label: string, items: ClientInfo[] }[] {
  return CLIENT_KINDS
    .map(kind => ({ kind, label: KIND_LABELS[kind], items: list.filter(c => kindOf(c.kind) === kind) }))
    .filter(g => g.items.length > 0)
}

/** MultiSelect options (affinity client_include): name, with kind and id as the hint. */
export function clientOptions(list: readonly ClientInfo[]): { value: string, label: string, hint: string }[] {
  return list.map(c => ({ value: c.id, label: c.name, hint: `${KIND_LABELS[kindOf(c.kind)]} · ${c.id}` }))
}

/** "Claude Code、Codex" for a rule's client_include; '' when the rule applies to any client. */
export function clientIncludeLabel(ids: readonly string[], list: readonly ClientInfo[]): string {
  return ids.map(id => clientName(list, id)).join('、')
}

// ---------- cache hit rate highlighting (stats 「客户端」 table) ----------

/** Below this cache hit rate a client is flagged red, below `CACHE_RATE_WARN` amber. */
export const CACHE_RATE_LOW = 0.3
export const CACHE_RATE_WARN = 0.6
/** Clients with fewer prompt tokens are not flagged (too little traffic to judge). */
export const CACHE_RATE_MIN_PROMPT = 10_000

export type CacheTone = 'none' | 'low' | 'warn' | 'ok'

export function cacheRateTone(rate: number | null | undefined, promptTokens: number): CacheTone {
  if (rate == null || promptTokens < CACHE_RATE_MIN_PROMPT)
    return 'none'
  if (rate < CACHE_RATE_LOW)
    return 'low'
  return rate < CACHE_RATE_WARN ? 'warn' : 'ok'
}

export const CACHE_TONE_CLASSES: Record<CacheTone, string> = {
  none: '',
  low: 'text-destructive font-medium',
  warn: 'text-amber-700 dark:text-amber-400',
  ok: 'text-emerald-700 dark:text-emerald-400',
}
