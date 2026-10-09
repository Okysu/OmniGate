// Limits (phase8-api.md §2): API key spend limits (form ↔ policy), `GET /api/billing/limits`
// normalisation, ratios and labels. Amounts are decimal strings (BigInt nano arithmetic).
import type { BillingLimits, GroupLimits, KeyPolicy, KeySpendUsage, SpendLimit, SpendWindow } from './types'
import type { UsageLevel } from './quota'
import { fromNano, isValidAmount, toNano } from './money'
import { normalizeLimits } from './groups'

export const SPEND_WINDOWS: SpendWindow[] = ['day', 'week', 'month', 'total']

export const SPEND_WINDOW_LABELS: Record<SpendWindow, string> = {
  day: '每天',
  week: '每周',
  month: '每月',
  total: '总计',
}

const SPEND_WINDOW_SUFFIX: Record<SpendWindow, string> = {
  day: '/天',
  week: '/周',
  month: '/月',
  total: '',
}

export function isSpendWindow(v: unknown): v is SpendWindow {
  return typeof v === 'string' && (SPEND_WINDOWS as string[]).includes(v)
}

export function normalizeSpendLimit(v: unknown): SpendLimit | null {
  if (!v || typeof v !== 'object')
    return null
  const o = v as Record<string, unknown>
  if (typeof o.amount !== 'string' || !isValidAmount(o.amount) || !isSpendWindow(o.window))
    return null
  return { amount: o.amount, window: o.window }
}

/** "$10.00/天" / "总计 $10.00". */
export function spendLimitText(l: SpendLimit, money: (v: string) => string): string {
  return l.window === 'total' ? `总计 ${money(l.amount)}` : `${money(l.amount)}${SPEND_WINDOW_SUFFIX[l.window]}`
}

// ---------------------------------------------------------------------------
// Key form
// ---------------------------------------------------------------------------

export interface SpendLimitForm {
  /** '' = unlimited. */
  amount: string
  window: SpendWindow
}

export function spendFormFrom(l: SpendLimit | null | undefined): SpendLimitForm {
  const n = normalizeSpendLimit(l)
  return { amount: n?.amount ?? '', window: n?.window ?? 'month' }
}

export function validateSpendForm(f: SpendLimitForm): string | null {
  const s = f.amount.trim()
  if (s === '')
    return null
  return isValidAmount(s) ? null : '请输入不小于 0 的金额（最多 9 位小数），留空表示不限'
}

export function spendLimitFromForm(f: SpendLimitForm): SpendLimit | null {
  const s = f.amount.trim()
  if (s === '' || !isValidAmount(s))
    return null
  return { amount: fromNano(toNano(s)), window: f.window }
}

/**
 * Adds `spendLimit` to a key policy body. Older backends reject unknown fields, so the
 * field is only sent when set, or when the stored policy already has it (to clear it).
 */
export function withSpendLimit(policy: KeyPolicy, limit: SpendLimit | null, original?: Pick<KeyPolicy, 'spendLimit'> | null): KeyPolicy {
  if (limit)
    return { ...policy, spendLimit: limit }
  if (original && 'spendLimit' in original && original.spendLimit !== undefined)
    return { ...policy, spendLimit: null }
  const rest = { ...policy }
  delete rest.spendLimit
  return rest
}

// ---------------------------------------------------------------------------
// Ratios
// ---------------------------------------------------------------------------

function nano(v: string | null | undefined): bigint | null {
  return v != null && isValidAmount(v) ? toNano(v) : null
}

/** used / limit (0 when the limit is 0 and nothing is used; ≥1 once reached). */
export function amountRatio(used: string | null | undefined, limit: string | null | undefined): number {
  const u = nano(used) ?? 0n
  const l = nano(limit)
  if (l === null)
    return 0
  // A zero limit blocks every platform request: treat it as reached.
  if (l === 0n)
    return 1
  return Number((u * 10_000n) / l) / 10_000
}

export function countRatio(used: number, limit: number | null): number {
  if (!limit || limit <= 0)
    return 0
  return Math.max(0, used) / limit
}

export function levelOf(ratio: number): UsageLevel {
  if (ratio >= 1)
    return 'exceeded'
  return ratio >= 0.8 ? 'warn' : 'ok'
}

/** "37%" (one decimal under 10%). */
export function percentText(ratio: number): string {
  const p = Math.max(0, ratio) * 100
  return p > 0 && p < 10 ? `${(Math.round(p * 10) / 10).toString()}%` : `${Math.round(p)}%`
}

// ---------------------------------------------------------------------------
// GET /api/billing/limits
// ---------------------------------------------------------------------------

export interface NormalizedLimits {
  group: { id: string | null, name: string, priceMultiplier: string | null, timezone: string | null, limits: GroupLimits }
  usage: { rpdUsed: number, dailySpent: string, monthlySpent: string, dayResetsAt: string | null, monthResetsAt: string | null }
  keys: KeySpendUsage[]
}

function str(v: unknown): string | null {
  return typeof v === 'string' && v !== '' ? v : null
}

function pick(o: Record<string, unknown> | null | undefined, keys: string[]): string | null {
  for (const k of keys) {
    const v = str(o?.[k])
    if (v)
      return v
  }
  return null
}

/**
 * Defensive normalisation: `group` may carry the limits flattened (`{name, rpm, …}`) or
 * nested (`{name, limits: {…}}`); `resetsAt` keys are not pinned (`day` / `daily` / `rpd` …).
 */
export function normalizeBillingLimits(raw: Partial<BillingLimits> | null | undefined): NormalizedLimits {
  const g = (raw?.group ?? {}) as Record<string, unknown>
  const limits = normalizeLimits((g.limits && typeof g.limits === 'object' ? g.limits : g) as Partial<Record<keyof GroupLimits, unknown>>)
  const u = (raw?.usage ?? {}) as Record<string, unknown>
  const resets = (u.resetsAt && typeof u.resetsAt === 'object' ? u.resetsAt : {}) as Record<string, unknown>
  const amount = (v: unknown) => (typeof v === 'string' && isValidAmount(v) ? v : '0')
  return {
    group: {
      id: str(g.id),
      name: str(g.name) ?? '',
      priceMultiplier: typeof g.priceMultiplier === 'string' && isValidAmount(g.priceMultiplier) ? g.priceMultiplier : null,
      timezone: str(g.timezone),
      limits,
    },
    usage: {
      rpdUsed: typeof u.rpdUsed === 'number' && Number.isFinite(u.rpdUsed) ? Math.max(0, Math.floor(u.rpdUsed)) : 0,
      dailySpent: amount(u.dailySpent),
      monthlySpent: amount(u.monthlySpent),
      dayResetsAt: pick(resets, ['day', 'daily', 'rpd', 'dailySpend', 'today']),
      monthResetsAt: pick(resets, ['month', 'monthly', 'monthlySpend']),
    },
    keys: (Array.isArray(raw?.keys) ? raw.keys : [])
      .filter(k => k && typeof k.id === 'string')
      .map(k => ({ id: k.id, name: str(k.name) ?? k.id, spendLimit: normalizeSpendLimit(k.spendLimit), spent: amount(k.spent), resetsAt: str(k.resetsAt) })),
  }
}
