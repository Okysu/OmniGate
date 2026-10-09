// Context-length tiered prices (phase10-api.md §1): a price version may carry up to 5
// tiers; a request whose prompt tokens (input + cache read + cache write) exceed a tier's
// `aboveInputTokens` is priced — every token component, for the whole request — at the
// highest such tier. A prompt of exactly the threshold stays on the lower tier.
// perRequest / perImage / perMinute / perMCharacters are never tiered.
//
// This module holds the price dialog's tier rows (form ↔ request body, validation, the
// long-context preset and the preview line), threshold parsing / formatting ("272K"),
// inheritance resolution of stored tiers and the tier pick of the plaza calculator.
import type { BillingMode } from './billingMode'
import type { PlazaPrice, PlazaPriceTier, PriceTier, PriceTierInput } from './types'
import { amountSign, fromNano, isValidAmount, toNano } from './money'

/** Server limit (phase10 §1). */
export const MAX_TIERS = 5
export const MAX_TIER_THRESHOLD = 1_000_000_000
/** OpenAI's long-context boundary: > 272K input tokens. */
export const LONG_CONTEXT_THRESHOLD = 272_000

/** Token prices a tier may override, in display order. */
export const TIER_PRICE_KEYS = ['inputPerM', 'outputPerM', 'cacheReadPerM', 'cacheWritePerM', 'imageInputPerM', 'audioInputPerM', 'audioOutputPerM'] as const
export type TierPriceKey = typeof TIER_PRICE_KEYS[number]
/** Optional tier prices (blank = inherit the base version's field). */
export const TIER_OPTIONAL_KEYS = ['cacheReadPerM', 'cacheWritePerM', 'imageInputPerM', 'audioInputPerM', 'audioOutputPerM'] as const
export type TierOptionalKey = typeof TIER_OPTIONAL_KEYS[number]

/** The tier prices each billing mode edits (tiers are offered for Token and 自定义组合 only). */
export const TIER_MODE_KEYS: Partial<Record<BillingMode, readonly TierPriceKey[]>> = {
  token: ['inputPerM', 'outputPerM', 'cacheReadPerM', 'cacheWritePerM'],
  custom: TIER_PRICE_KEYS,
}

export function tiersAllowed(mode: BillingMode): boolean {
  return mode in TIER_MODE_KEYS
}

// ---------------------------------------------------------------------------
// Thresholds
// ---------------------------------------------------------------------------

const THRESHOLD_RE = /^(\d+)(?:\.(\d+))?\s*([km])?$/i

/**
 * "272K" → 272000, "1M" / "1.5m" → 1000000 / 1500000, "272000" / "272,000" → 272000.
 * K = 1,000 and M = 1,000,000 (decimal, like providers' context tiers). `null` for
 * anything that is not a whole token count in 1 … 1,000,000,000.
 */
export function parseTokenThreshold(v: string | number | null | undefined): number | null {
  if (v === null || v === undefined)
    return null
  const s = String(v).trim().replace(/[,，_\s]/g, '')
  const m = THRESHOLD_RE.exec(s)
  if (!m)
    return null
  const int = m[1]!
  const frac = m[2] ?? ''
  const zeros = m[3] ? (m[3].toLowerCase() === 'k' ? 3 : 6) : 0
  // Whole tokens only: the fraction must fit in the suffix's zeros ("1.2345K" is not).
  if (frac.replace(/0+$/, '').length > zeros)
    return null
  const digits = int + frac.padEnd(zeros, '0').slice(0, zeros)
  if (digits.length > 12)
    return null
  const n = Number(digits)
  return Number.isSafeInteger(n) && n >= 1 && n <= MAX_TIER_THRESHOLD ? n : null
}

function trimDecimal(n: number, unit: number): string {
  const whole = Math.floor(n / unit)
  const rest = n % unit
  if (!rest)
    return String(whole)
  const width = String(unit).length - 1
  return `${whole}.${String(rest).padStart(width, '0').replace(/0+$/, '')}`
}

/**
 * Compact threshold: 272000 → "272K", 1000000 → "1M", 1500000 → "1.5M", 272500 →
 * "272.5K"; other counts with thousands separators (1234 → "1,234").
 */
export function formatTokenThreshold(n: number): string {
  if (!Number.isFinite(n))
    return '—'
  if (n >= 1_000_000 && n % 1000 === 0)
    return `${trimDecimal(n, 1_000_000)}M`
  if (n >= 1000 && n % 100 === 0)
    return `${trimDecimal(n, 1000)}K`
  return n.toLocaleString('en-US')
}

/** "长上下文档 >272K". */
export function tierLabel(above: number): string {
  return `长上下文档 >${formatTokenThreshold(above)}`
}

// ---------------------------------------------------------------------------
// Form rows
// ---------------------------------------------------------------------------

export interface TierRow {
  /** Free text: "272K", "1M", "272000". */
  threshold: string
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string
  cacheWritePerM: string
  imageInputPerM: string
  audioInputPerM: string
  audioOutputPerM: string
}

export function emptyTierRow(threshold = ''): TierRow {
  return { threshold, inputPerM: '', outputPerM: '', cacheReadPerM: '', cacheWritePerM: '', imageInputPerM: '', audioInputPerM: '', audioOutputPerM: '' }
}

/** Form rows from a stored version (`null` fields = inherit → blank). */
export function tierRowsFrom(p: { tiers?: PriceTier[] | null } | null | undefined): TierRow[] {
  return (p?.tiers ?? [])
    .filter(t => t && typeof t.aboveInputTokens === 'number')
    .map(t => ({
      threshold: formatTokenThreshold(t.aboveInputTokens),
      inputPerM: t.inputPerM ?? '',
      outputPerM: t.outputPerM ?? '',
      cacheReadPerM: t.cacheReadPerM ?? '',
      cacheWritePerM: t.cacheWritePerM ?? '',
      imageInputPerM: t.imageInputPerM ?? '',
      audioInputPerM: t.audioInputPerM ?? '',
      audioOutputPerM: t.audioOutputPerM ?? '',
    }))
}

/**
 * Request tiers for `mode`: the mode's tier prices as typed (blank optional prices are
 * omitted = inherit), rows ordered as entered (validation enforces ascending order).
 * Modes without tiers send none.
 */
export function buildTiers(rows: readonly TierRow[], mode: BillingMode): PriceTierInput[] {
  const keys = TIER_MODE_KEYS[mode]
  if (!keys)
    return []
  return rows.map((r) => {
    const t: PriceTierInput = {
      aboveInputTokens: parseTokenThreshold(r.threshold) ?? 0,
      inputPerM: r.inputPerM.trim(),
      outputPerM: r.outputPerM.trim(),
    }
    for (const k of TIER_OPTIONAL_KEYS) {
      const v = r[k].trim()
      if (keys.includes(k) && v !== '')
        t[k] = v
    }
    return t
  })
}

const AMOUNT_HINT = '非负十进制数，最多 9 位小数'

/**
 * Client checks mirroring the server (422). Keys: `tiers` (list), `tiers.<i>.<field>`.
 * Thresholds must be whole token counts in 1 … 1,000,000,000, strictly ascending;
 * input / output prices are required.
 */
export function validateTiers(rows: readonly TierRow[], mode: BillingMode): Record<string, string> {
  const e: Record<string, string> = {}
  const keys = TIER_MODE_KEYS[mode]
  if (!keys || !rows.length)
    return e
  if (rows.length > MAX_TIERS)
    e.tiers = `最多 ${MAX_TIERS} 档`
  let prev: number | null = null
  rows.forEach((r, i) => {
    const k = `tiers.${i}`
    const n = parseTokenThreshold(r.threshold)
    if (!r.threshold.trim())
      e[`${k}.aboveInputTokens`] = '请填写阈值，如 272K'
    else if (n === null)
      e[`${k}.aboveInputTokens`] = '1 – 1,000M 之间的整数 token 数，如 272K'
    else if (prev !== null && n <= prev)
      e[`${k}.aboveInputTokens`] = `须大于上一档（>${formatTokenThreshold(prev)}）`
    if (n !== null)
      prev = prev === null ? n : Math.max(prev, n)
    for (const f of keys) {
      const v = r[f].trim()
      if (v === '') {
        if (f === 'inputPerM' || f === 'outputPerM')
          e[`${k}.${f}`] = '必填'
        continue
      }
      if (!isValidAmount(v))
        e[`${k}.${f}`] = AMOUNT_HINT
    }
  })
  return e
}

/** Server 422 details → form keys (`tiers[1].inputPerM` → `tiers.1.inputPerM`). */
export function normalizeTierErrorKeys(details: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(details))
    out[k.replace(/\[(\d+)\]/g, '.$1')] = v
  return out
}

/** Errors of row `i` (field → message). */
export function tierRowErrors(errors: Record<string, string>, i: number): Record<string, string> {
  const prefix = `tiers.${i}.`
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(errors)) {
    if (k.startsWith(prefix))
      out[k.slice(prefix.length)] = v
  }
  return out
}

// ---------------------------------------------------------------------------
// Preset
// ---------------------------------------------------------------------------

/** `amount × num / den`, rounded half up at 9 decimals; '' for blank / invalid / 0. */
function scaled(amount: string | null | undefined, num: bigint, den: bigint): string {
  const v = (amount ?? '').trim()
  if (!v || !isValidAmount(v) || amountSign(v) <= 0)
    return ''
  return fromNano((toNano(v) * num + den / 2n) / den)
}

export type TierBase = { [K in TierPriceKey]?: string | null }

/**
 * "长上下文：>272K 输入 token 全部单价翻倍": a tier at 272K with 2× the base input,
 * cache read / write, image and audio input prices and 1.5× the output prices (the
 * pattern of OpenAI's long-context table). Blank base prices stay blank (inherit).
 */
export function longContextPreset(base: TierBase, mode: BillingMode = 'token'): TierRow {
  const keys = TIER_MODE_KEYS[mode] ?? TIER_MODE_KEYS.token!
  const row = emptyTierRow(formatTokenThreshold(LONG_CONTEXT_THRESHOLD))
  for (const k of keys) {
    const output = k === 'outputPerM' || k === 'audioOutputPerM'
    row[k] = output ? scaled(base[k], 3n, 2n) : scaled(base[k], 2n, 1n)
  }
  // Input / output are required: an unset base price starts at 0.
  row.inputPerM ||= '0'
  row.outputPerM ||= '0'
  return row
}

// ---------------------------------------------------------------------------
// Preview / display
// ---------------------------------------------------------------------------

/**
 * "≤272K：输入 $10.000 · 输出 $50.000；>272K：输入 $20.000 · 输出 $75.000" for valid
 * rows (`null` when nothing to show). `fmt` formats amounts (3 decimals in the UI).
 */
export function tierPreview(base: { inputPerM: string, outputPerM: string }, rows: readonly TierRow[], fmt: (amount: string) => string): string | null {
  const parsed = rows
    .map(r => ({ n: parseTokenThreshold(r.threshold), r }))
    .filter((x): x is { n: number, r: TierRow } => x.n !== null && isValidAmount(x.r.inputPerM.trim()) && isValidAmount(x.r.outputPerM.trim()))
  if (!parsed.length)
    return null
  const price = (i: string, o: string) => `输入 ${fmt(i || '0')} · 输出 ${fmt(o || '0')}`
  const parts = [`≤${formatTokenThreshold(parsed[0]!.n)}：${price(base.inputPerM.trim(), base.outputPerM.trim())}`]
  for (const { n, r } of parsed)
    parts.push(`>${formatTokenThreshold(n)}：${price(r.inputPerM.trim(), r.outputPerM.trim())}`)
  return parts.join('；')
}

/**
 * Stored tiers with inheritance applied, in the plaza's resolved shape: a null field takes
 * the base version's value (`imageInputPerM` / audio prices stay null when the base's are,
 * i.e. billed at the tier's input / output); cache prices of 0 become null.
 */
export function resolveTiers(base: { cacheReadPerM?: string | null, cacheWritePerM?: string | null, imageInputPerM?: string | null, audioInputPerM?: string | null, audioOutputPerM?: string | null }, tiers: readonly PriceTier[] | null | undefined): PlazaPriceTier[] {
  const zeroNull = (v: string | null | undefined) => (typeof v === 'string' && amountSign(v) > 0 ? v : null)
  return [...(tiers ?? [])]
    .filter(t => t && typeof t.aboveInputTokens === 'number')
    .sort((a, b) => a.aboveInputTokens - b.aboveInputTokens)
    .map(t => ({
      aboveInputTokens: t.aboveInputTokens,
      inputPerM: t.inputPerM,
      outputPerM: t.outputPerM,
      cacheReadPerM: zeroNull(t.cacheReadPerM ?? base.cacheReadPerM),
      cacheWritePerM: zeroNull(t.cacheWritePerM ?? base.cacheWritePerM),
      imageInputPerM: t.imageInputPerM ?? base.imageInputPerM ?? null,
      audioInputPerM: t.audioInputPerM ?? base.audioInputPerM ?? null,
      audioOutputPerM: t.audioOutputPerM ?? base.audioOutputPerM ?? null,
    }))
}

/** Valid tiers of a plaza payload (anything malformed is dropped), ascending. */
export function normalizePlazaTiers(v: unknown): PlazaPriceTier[] {
  if (!Array.isArray(v))
    return []
  const str = (x: unknown) => (typeof x === 'string' && isValidAmount(x) ? x : null)
  return v
    .filter((t): t is Record<string, unknown> => !!t && typeof t === 'object' && typeof (t as PlazaPriceTier).aboveInputTokens === 'number'
      && (t as PlazaPriceTier).aboveInputTokens > 0 && str((t as PlazaPriceTier).inputPerM) !== null && str((t as PlazaPriceTier).outputPerM) !== null)
    .map(t => ({
      aboveInputTokens: t.aboveInputTokens as number,
      inputPerM: t.inputPerM as string,
      outputPerM: t.outputPerM as string,
      cacheReadPerM: str(t.cacheReadPerM),
      cacheWritePerM: str(t.cacheWritePerM),
      imageInputPerM: str(t.imageInputPerM),
      audioInputPerM: str(t.audioInputPerM),
      audioOutputPerM: str(t.audioOutputPerM),
    }))
    .sort((a, b) => a.aboveInputTokens - b.aboveInputTokens)
}

export function hasTiers<T extends { tiers?: readonly unknown[] | null }>(p: T | null | undefined): p is T & { tiers: NonNullable<T['tiers']> } {
  return !!p?.tiers && p.tiers.length > 0
}

/** The tier applying to `promptTokens` (highest with aboveInputTokens < prompt), or null = base. */
export function pickTier<T extends { aboveInputTokens: number }>(tiers: readonly T[] | null | undefined, promptTokens: number): T | null {
  let hit: T | null = null
  for (const t of tiers ?? []) {
    if (promptTokens > t.aboveInputTokens && (!hit || t.aboveInputTokens > hit.aboveInputTokens))
      hit = t
  }
  return hit
}

/**
 * The plaza price a request with `promptTokens` is billed at: the base price, or the
 * picked tier's token prices over it (non-token prices unchanged).
 */
export function priceForPrompt(price: PlazaPrice, promptTokens: number): { price: PlazaPrice, tier: PlazaPriceTier | null } {
  const tier = pickTier(price.tiers, promptTokens)
  if (!tier)
    return { price, tier: null }
  return {
    tier,
    price: {
      ...price,
      inputPerM: tier.inputPerM,
      outputPerM: tier.outputPerM,
      cacheReadPerM: tier.cacheReadPerM,
      cacheWritePerM: tier.cacheWritePerM,
      imageInputPerM: tier.imageInputPerM,
      audioInputPerM: tier.audioInputPerM,
      audioOutputPerM: tier.audioOutputPerM,
      tiers: null,
    },
  }
}

/** One row of a tier table: "≤272K" (base) or ">272K". */
export interface TierTableRow {
  /** null = the base row. */
  above: number | null
  label: string
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string | null
  cacheWritePerM: string | null
}

/**
 * Rows for the tier popovers: the base prices ("≤272K"), then one row per tier.
 * `tiers` must be resolved (plaza shape, see `resolveTiers`).
 */
export function tierTable(base: { inputPerM: string, outputPerM: string, cacheReadPerM?: string | null, cacheWritePerM?: string | null }, tiers: readonly PlazaPriceTier[]): TierTableRow[] {
  if (!tiers.length)
    return []
  const nz = (v: string | null | undefined) => (typeof v === 'string' && amountSign(v) > 0 ? v : null)
  const rows: TierTableRow[] = [{ above: null, label: `≤${formatTokenThreshold(tiers[0]!.aboveInputTokens)}`, inputPerM: base.inputPerM, outputPerM: base.outputPerM, cacheReadPerM: nz(base.cacheReadPerM), cacheWritePerM: nz(base.cacheWritePerM) }]
  for (const t of tiers)
    rows.push({ above: t.aboveInputTokens, label: `>${formatTokenThreshold(t.aboveInputTokens)}`, inputPerM: t.inputPerM, outputPerM: t.outputPerM, cacheReadPerM: nz(t.cacheReadPerM), cacheWritePerM: nz(t.cacheWritePerM) })
  return rows
}
