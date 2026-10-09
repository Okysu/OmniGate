// Bulk subscription actions (phase7-api.md §3): quota reset and extension. Target and
// body construction, rule options for the reset dialog, duration input and validation.
import type { ExtendSubscriptionsInput, Plan, QuotaRule, ResetQuotaInput, Subscription, SubscriptionTarget } from './types'
import { durationError, PERIOD_RANGE, ruleTitle } from './quota'

export const BULK_NOTE_MAX = 200

/** `selected`: the checked rows; `plan`: every active subscription of a plan (or all plans). */
export type TargetMode = 'selected' | 'plan'

/** Plan select value for "全部套餐" (sent as `planId: null`). */
export const ALL_PLANS = '__all__'

export function buildTarget(mode: TargetMode, ids: readonly string[], planId: string): SubscriptionTarget {
  if (mode === 'selected')
    return { ids: [...new Set(ids)] }
  return { planId: planId === ALL_PLANS || !planId ? null : planId, status: 'active' }
}

export function targetIsEmpty(t: SubscriptionTarget): boolean {
  return 'ids' in t && t.ids.length === 0
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

export interface RuleOption {
  id: string
  /** Display names (several when plans use the same id with different labels). */
  label: string
  /** Lifetime (总量) rule in at least one source: only reset with `includeLifetime`. */
  lifetime: boolean
  /** Lifetime in every source. */
  lifetimeOnly: boolean
}

type RuleLike = Pick<QuotaRule, 'id' | 'label' | 'window'>

/** Distinct rule ids (first-seen order) across the given rule lists. */
export function ruleOptions(lists: readonly (readonly RuleLike[])[]): RuleOption[] {
  const byId = new Map<string, { labels: string[], lifetime: number, total: number }>()
  for (const rules of lists) {
    for (const r of rules) {
      const cur = byId.get(r.id) ?? { labels: [], lifetime: 0, total: 0 }
      const title = ruleTitle(r)
      if (!cur.labels.includes(title))
        cur.labels.push(title)
      cur.total++
      if (r.window.kind === 'lifetime')
        cur.lifetime++
      byId.set(r.id, cur)
    }
  }
  return [...byId.entries()].map(([id, v]) => ({
    id,
    label: v.labels.join(' / '),
    lifetime: v.lifetime > 0,
    lifetimeOnly: v.lifetime === v.total,
  }))
}

/** Rule options for the target: the selected subscriptions' snapshots, or the plan(s)' rules. */
export function ruleOptionsForTarget(mode: TargetMode, selected: readonly Subscription[], plans: readonly Plan[], planId: string): RuleOption[] {
  if (mode === 'selected')
    return ruleOptions(selected.map(s => s.rules))
  const ps = planId === ALL_PLANS || !planId ? plans : plans.filter(p => p.id === planId)
  return ruleOptions(ps.map(p => p.rules))
}

/**
 * Warning shown above the confirm button, or null: every chosen rule is a lifetime rule
 * while `includeLifetime` is off (the reset would not change anything).
 */
export function lifetimeWarning(options: readonly RuleOption[], chosen: readonly string[] | null, includeLifetime: boolean): string | null {
  if (includeLifetime)
    return null
  const picked = chosen === null ? options : options.filter(o => chosen.includes(o.id))
  if (picked.length > 0 && picked.every(o => o.lifetimeOnly))
    return '所选规则都是「订阅期内总量」规则：未开启「包含总量规则」时不会重置任何额度。'
  return null
}

export function buildResetInput(target: SubscriptionTarget, rules: readonly string[] | null, includeLifetime: boolean, note: string): ResetQuotaInput {
  return { target, rules: rules === null ? null : [...new Set(rules)], includeLifetime, note: note.trim() }
}

// ---------------------------------------------------------------------------
// Extension
// ---------------------------------------------------------------------------

export type ExtendUnit = 'h' | 'd'

export const EXTEND_UNIT_LABELS: Record<ExtendUnit, string> = { h: '小时', d: '天' }

/** "7" + "d" → "7d"; null when the value is not a positive integer. */
export function buildDuration(value: string | number, unit: ExtendUnit): string | null {
  const s = String(value).trim()
  if (!/^[1-9]\d{0,6}$/.test(s))
    return null
  return `${Number(s)}${unit}`
}

/** Error for the extension duration (1h–366d), or null. */
export function extendDurationError(value: string | number, unit: ExtendUnit): string | null {
  const d = buildDuration(value, unit)
  if (!d)
    return '请输入正整数'
  return durationError(d, PERIOD_RANGE)
}

export function buildExtendInput(target: SubscriptionTarget, duration: string, note: string): ExtendSubscriptionsInput {
  return { target, duration, note: note.trim() }
}

/** `endsAt` moved by the duration (for the single-subscription preview). */
export function extendedEnd(endsAt: string, duration: string): string | null {
  const m = /^(\d+)([hd])$/.exec(duration)
  const t = new Date(endsAt).getTime()
  if (!m || Number.isNaN(t))
    return null
  const ms = Number(m[1]) * (m[2] === 'd' ? 86_400_000 : 3_600_000)
  return new Date(t + ms).toISOString()
}

export function noteError(note: string): string | null {
  return [...note.trim()].length > BULK_NOTE_MAX ? `最多 ${BULK_NOTE_MAX} 个字符` : null
}

/** Stable key of a request body: the dry-run result is only valid for the same body. */
export function bodyKey(body: unknown): string {
  return JSON.stringify(body)
}

/** "将影响 12 份订阅" / "没有符合条件的有效订阅". */
export function affectedText(n: number): string {
  return n > 0 ? `将影响 ${n} 份订阅` : '没有符合条件的有效订阅'
}

/** Short description of the target for dialog titles / toasts. */
export function targetLabel(t: SubscriptionTarget, planName: (id: string) => string): string {
  if ('ids' in t)
    return t.ids.length === 1 ? '所选订阅' : `所选 ${t.ids.length} 份订阅`
  return t.planId === null ? '全部套餐的有效订阅' : `「${planName(t.planId)}」的全部有效订阅`
}
