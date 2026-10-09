// User groups (phase8-api.md §1): price multiplier labels and arithmetic, the group
// form (validation mirrors the server's ranges), request bodies and limit summaries.
// Multipliers and amounts are decimal strings; arithmetic uses BigInt nano units.
import type { GroupLimits, MeResponse, MyGroup, UserGroup, UserGroupInput, UserGroupPatch } from './types'
import { fromNano, isValidAmount, toNano } from './money'
import { DEFAULT_TIMEZONE, isValidTimezone } from './notifications'

export const GROUP_NAME_MAX = 50
export const GROUP_DESCRIPTION_MAX = 200
/** `priceMultiplier` range (inclusive). */
export const GROUP_MULTIPLIER_MAX = '100'

const NANO = 1_000_000_000n

export const UNLIMITED: GroupLimits = { rpm: null, rpd: null, dailySpend: null, monthlySpend: null }

function nanoOrNull(v: string | null | undefined): bigint | null {
  if (v == null)
    return null
  const s = String(v).trim()
  if (!isValidAmount(s))
    return null
  return toNano(s)
}

/** Canonical decimal ("0.80" → "0.8"); invalid input is returned trimmed. */
export function canonicalDecimal(v: string): string {
  const n = nanoOrNull(v)
  return n === null ? v.trim() : fromNano(n)
}

/** True when the multiplier is a valid decimal other than 1 (i.e. it changes prices). */
export function isNonUnitMultiplier(m: string | null | undefined): boolean {
  const n = nanoOrNull(m)
  return n !== null && n !== NANO
}

/** `amount × multiplier`, rounded half-up at 9 decimals. Invalid input → null. */
export function multiplyAmount(amount: string | null | undefined, multiplier: string | null | undefined): string | null {
  const a = nanoOrNull(amount)
  const m = nanoOrNull(multiplier)
  if (a === null || m === null)
    return null
  return fromNano((a * m + NANO / 2n) / NANO)
}

/** Product of two multipliers ("0.8" × "0.5" → "0.4"). */
export function multiplyMultipliers(a: string, b: string): string | null {
  return multiplyAmount(a, b)
}

export type MultiplierTone = 'discount' | 'markup' | 'list'

/** discount (< 1), markup (> 1) or list price (= 1 / invalid). */
export function multiplierTone(m: string | null | undefined): MultiplierTone {
  const n = nanoOrNull(m)
  if (n === null || n === NANO)
    return 'list'
  return n < NANO ? 'discount' : 'markup'
}

/** Badge classes per tone (light / dark). */
export const MULTIPLIER_TONE_CLASSES: Record<MultiplierTone, string> = {
  discount: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  markup: 'border-amber-500/50 text-amber-700 dark:text-amber-400',
  list: 'text-muted-foreground',
}

const CN_DIGITS = ['零', '一', '二', '三', '四', '五', '六', '七', '八', '九']

/** "×0.8". */
export function multiplierShort(m: string): string {
  return `×${canonicalDecimal(m)}`
}

/**
 * Chinese price wording of a multiplier: "1" → "原价", "0" → "免费", "0.8" → "八折",
 * "0.85" → "八五折", "0.05" → "0.5 折", "0.825" → "8.25 折", "1.5" → "加价 50%".
 * Invalid input → ''.
 */
export function multiplierWords(m: string): string {
  const n = nanoOrNull(m)
  if (n === null)
    return ''
  if (n === 0n)
    return '免费'
  if (n === NANO)
    return '原价'
  if (n > NANO)
    return `加价 ${fromNano((n - NANO) * 100n)}%`
  // 0 < m < 1
  if (n % 10_000_000n === 0n) {
    const pct = Number(n / 10_000_000n) // 1..99
    if (pct % 10 === 0)
      return `${CN_DIGITS[pct / 10]}折`
    if (pct > 10)
      return `${CN_DIGITS[Math.floor(pct / 10)]}${CN_DIGITS[pct % 10]}折`
  }
  return `${fromNano(n * 10n)} 折`
}

/** "×0.8 / 八折" (just "×1 / 原价" for 1). */
export function multiplierLabel(m: string): string {
  const words = multiplierWords(m)
  return words ? `${multiplierShort(m)} / ${words}` : m
}

// ---------------------------------------------------------------------------
// Limits
// ---------------------------------------------------------------------------

function posInt(v: unknown): number | null {
  return typeof v === 'number' && Number.isInteger(v) && v > 0 ? v : null
}
function amountOrNull(v: unknown): string | null {
  return typeof v === 'string' && isValidAmount(v) ? v : null
}

/** Defensive copy of a server `limits` object (missing / invalid fields = unlimited). */
export function normalizeLimits(l: Partial<Record<keyof GroupLimits, unknown>> | null | undefined): GroupLimits {
  return {
    rpm: posInt(l?.rpm),
    rpd: posInt(l?.rpd),
    dailySpend: amountOrNull(l?.dailySpend),
    monthlySpend: amountOrNull(l?.monthlySpend),
  }
}

export function hasLimits(l: GroupLimits): boolean {
  return l.rpm !== null || l.rpd !== null || l.dailySpend !== null || l.monthlySpend !== null
}

export interface LimitPart {
  key: keyof GroupLimits
  label: string
  text: string
}

/** One entry per set limit, e.g. "60 次/分钟", "$5.00/天". Empty = unlimited. */
export function limitParts(l: GroupLimits, money: (v: string) => string): LimitPart[] {
  const out: LimitPart[] = []
  if (l.rpm !== null)
    out.push({ key: 'rpm', label: '每分钟请求', text: `${l.rpm.toLocaleString('zh-CN')} 次/分钟` })
  if (l.rpd !== null)
    out.push({ key: 'rpd', label: '每天请求', text: `${l.rpd.toLocaleString('zh-CN')} 次/天` })
  if (l.dailySpend !== null)
    out.push({ key: 'dailySpend', label: '每天消费', text: `${money(l.dailySpend)}/天` })
  if (l.monthlySpend !== null)
    out.push({ key: 'monthlySpend', label: '每月消费', text: `${money(l.monthlySpend)}/月` })
  return out
}

// ---------------------------------------------------------------------------
// Form
// ---------------------------------------------------------------------------

export interface GroupForm {
  name: string
  description: string
  priceMultiplier: string
  /** '' = unlimited. */
  rpm: string
  rpd: string
  dailySpend: string
  monthlySpend: string
  timezone: string
  isDefault: boolean
}

export function emptyGroupForm(): GroupForm {
  return { name: '', description: '', priceMultiplier: '1', rpm: '', rpd: '', dailySpend: '', monthlySpend: '', timezone: DEFAULT_TIMEZONE, isDefault: false }
}

export function formFromGroup(g: UserGroup): GroupForm {
  const l = normalizeLimits(g.limits)
  return {
    name: g.name,
    description: g.description ?? '',
    priceMultiplier: g.priceMultiplier || '1',
    rpm: l.rpm === null ? '' : String(l.rpm),
    rpd: l.rpd === null ? '' : String(l.rpd),
    dailySpend: l.dailySpend ?? '',
    monthlySpend: l.monthlySpend ?? '',
    timezone: g.timezone || DEFAULT_TIMEZONE,
    isDefault: g.isDefault,
  }
}

const INT_RE = /^\d+$/
/** Server ranges (usergroup maxRPM / maxRPD). */
export const MAX_RPM = 1_000_000
export const MAX_RPD = 100_000_000

/** Valid multiplier: decimal 0–100 (at most 9 decimals). */
export function isValidMultiplier(v: string, max: string = GROUP_MULTIPLIER_MAX): boolean {
  const n = nanoOrNull(v)
  return n !== null && n <= toNano(max)
}

/** Errors keyed like the server's 422 details (`limits.rpm` …). */
export function validateGroupForm(f: GroupForm): Record<string, string> {
  const e: Record<string, string> = {}
  const name = f.name.trim()
  if (!name)
    e.name = '请填写名称'
  else if ([...name].length > GROUP_NAME_MAX)
    e.name = `最多 ${GROUP_NAME_MAX} 个字符`
  if ([...f.description.trim()].length > GROUP_DESCRIPTION_MAX)
    e.description = `最多 ${GROUP_DESCRIPTION_MAX} 个字符`
  if (!isValidMultiplier(f.priceMultiplier.trim()))
    e.priceMultiplier = '请输入 0–100 之间的数字（最多 9 位小数），1 表示原价'
  for (const [k, max] of [['rpm', MAX_RPM], ['rpd', MAX_RPD]] as const) {
    const v = f[k].trim()
    if (v !== '' && (!INT_RE.test(v) || Number(v) < 1 || Number(v) > max))
      e[`limits.${k}`] = `请输入 1–${max.toLocaleString('zh-CN')} 的整数，留空表示不限`
  }
  for (const k of ['dailySpend', 'monthlySpend'] as const) {
    const v = f[k].trim()
    if (v !== '' && !isValidAmount(v))
      e[`limits.${k}`] = '请输入不小于 0 的金额（最多 9 位小数），留空表示不限'
  }
  if (!isValidTimezone(f.timezone))
    e.timezone = '无效的时区'
  return e
}

export function limitsFromForm(f: GroupForm): GroupLimits {
  const int = (v: string) => (v.trim() === '' ? null : Number(v.trim()))
  const amt = (v: string) => (v.trim() === '' ? null : canonicalDecimal(v))
  return { rpm: int(f.rpm), rpd: int(f.rpd), dailySpend: amt(f.dailySpend), monthlySpend: amt(f.monthlySpend) }
}

export function buildGroupInput(f: GroupForm): UserGroupInput {
  return {
    name: f.name.trim(),
    description: f.description.trim(),
    priceMultiplier: canonicalDecimal(f.priceMultiplier),
    limits: limitsFromForm(f),
    timezone: f.timezone.trim(),
    isDefault: f.isDefault,
  }
}

/**
 * PATCH body: only changed fields plus `version`. `limits` is sent whole when any limit
 * changed; `isDefault` only when switching a group to default (the server unsets the old one).
 */
export function buildGroupPatch(f: GroupForm, original: UserGroup): UserGroupPatch {
  const now = buildGroupInput(f)
  const before = buildGroupInput(formFromGroup(original))
  const patch: UserGroupPatch = { version: original.version }
  if (now.name !== before.name)
    patch.name = now.name
  if (now.description !== before.description)
    patch.description = now.description
  if (now.priceMultiplier !== before.priceMultiplier)
    patch.priceMultiplier = now.priceMultiplier
  if (JSON.stringify(now.limits) !== JSON.stringify(before.limits))
    patch.limits = now.limits
  if (now.timezone !== before.timezone)
    patch.timezone = now.timezone
  if (now.isDefault && !original.isDefault)
    patch.isDefault = true
  return patch
}

export function isPatchEmpty(p: UserGroupPatch): boolean {
  return Object.keys(p).every(k => k === 'version')
}

/** Example for the form: "售价 $1.00 → $0.80" (null when the multiplier is invalid). */
export function multiplierExample(multiplier: string, money: (v: string) => string, base = '1'): string | null {
  const v = multiplyAmount(base, multiplier.trim())
  return v === null ? null : `售价 ${money(base)} → ${money(v)}`
}

export const GROUP_ERROR_MESSAGES: Record<string, string> = {
  group_is_default: '默认分组不能删除，请先把其他分组设为默认',
  group_name_exists: '已有同名分组',
  group_name_taken: '已有同名分组',
  name_taken: '已有同名分组',
  duplicate_name: '已有同名分组',
  version_conflict: '数据已被他人修改，请刷新后重试',
  not_found: '分组不存在',
}

/** Own group from `/api/me` (top-level `group`, or `user.group` with the full shape). */
export function myGroupOf(me: Pick<MeResponse, 'group' | 'user'> | null | undefined): MyGroup | null {
  const raw = (me?.group ?? (me?.user?.group as unknown)) as Partial<MyGroup> | null | undefined
  if (!raw || typeof raw.id !== 'string')
    return null
  return {
    id: raw.id,
    name: typeof raw.name === 'string' ? raw.name : '',
    priceMultiplier: typeof raw.priceMultiplier === 'string' && isValidAmount(raw.priceMultiplier) ? raw.priceMultiplier : '1',
    limits: normalizeLimits(raw.limits),
    ...(typeof raw.timezone === 'string' ? { timezone: raw.timezone } : {}),
  }
}

/** Default group first, then by name. */
export function sortGroups<T extends Pick<UserGroup, 'isDefault' | 'name'>>(list: readonly T[]): T[] {
  return [...list].sort((a, b) => Number(b.isDefault) - Number(a.isDefault) || a.name.localeCompare(b.name, 'zh-CN'))
}

/**
 * Request-log multiplier (phase8 §1.1 `priceMultiplier` = group × time-of-day); null when
 * absent or 1. The split is shown only when the backend sends it (not in the contract).
 */
export function logMultiplier(l: { priceMultiplier?: string | null, groupMultiplier?: string | null, scheduleMultiplier?: string | null }): { short: string, detail: string } | null {
  const m = l.priceMultiplier
  if (!m || !isNonUnitMultiplier(m))
    return null
  const parts: string[] = []
  if (l.groupMultiplier && isValidAmount(l.groupMultiplier))
    parts.push(`分组 ${multiplierShort(l.groupMultiplier)}`)
  if (l.scheduleMultiplier && isValidAmount(l.scheduleMultiplier))
    parts.push(`分时 ${multiplierShort(l.scheduleMultiplier)}`)
  const detail = parts.length
    ? `${parts.join(' · ')}（售价 ${multiplierShort(m)}）`
    : `本次售价 ${multiplierShort(m)}（${multiplierWords(m)}）：用户组倍率 × 分时价格倍率`
  return { short: multiplierShort(m), detail }
}
