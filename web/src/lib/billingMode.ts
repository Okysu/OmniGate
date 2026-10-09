// Billing modes of a price version (按 Token / 按次 / 按张 / 按时长 / 自定义组合): the
// mode inferred from a version's amounts, the fields each mode edits in the price
// dialog, the request body for a mode, and the compact price summary shown by the
// price tables and the model plaza. Pure functions; amounts stay decimal strings.
import type { PriceInput } from './types'
import { amountSign, isValidAmount } from './money'

export type BillingMode = 'token' | 'request' | 'image' | 'audio' | 'custom'

export const BILLING_MODES: readonly BillingMode[] = ['token', 'request', 'image', 'audio', 'custom']

/** Every amount of a price version, in display order. */
export const PRICE_FIELD_KEYS = [
  'inputPerM',
  'outputPerM',
  'cacheReadPerM',
  'cacheWritePerM',
  'perRequest',
  'perImage',
  'imageInputPerM',
  'audioInputPerM',
  'audioOutputPerM',
  'perMinute',
  'perMCharacters',
] as const
export type PriceFieldKey = typeof PRICE_FIELD_KEYS[number]

/**
 * Prices whose absence falls back to a text price (image input → inputPerM, audio
 * in / out → inputPerM / outputPerM): an explicit "0" is a real price ("free"), so
 * they count as set whenever present. Every other amount counts only when > 0.
 */
export const FALLBACK_PRICE_KEYS: readonly PriceFieldKey[] = ['imageInputPerM', 'audioInputPerM', 'audioOutputPerM']

/** Always sent (blank → "0"); the other amounts are omitted from the body when blank. */
const REQUIRED_KEYS = ['inputPerM', 'outputPerM', 'cacheReadPerM', 'cacheWritePerM', 'perRequest'] as const

const TOKEN_KEYS: readonly PriceFieldKey[] = ['inputPerM', 'outputPerM', 'cacheReadPerM', 'cacheWritePerM']

/** Loose amounts: admin `Price`, plaza `PlazaPrice` (null = not set) or form values. */
export type PriceAmounts = { [K in PriceFieldKey]?: string | null }

/** The fields each mode edits and submits (`custom` = every field). */
export const BILLING_MODE_FIELDS: Record<BillingMode, readonly PriceFieldKey[]> = {
  token: TOKEN_KEYS,
  request: ['perRequest'],
  image: ['perImage', 'perRequest'],
  audio: ['perMinute', 'perMCharacters', 'audioInputPerM', 'audioOutputPerM', 'perRequest'],
  custom: PRICE_FIELD_KEYS,
}

export interface BillingModeMeta {
  /** Selector label (price dialog). */
  label: string
  /** Badge text (price tables). */
  short: string
  /** One-line explanation under the selector. */
  description: string
}

export const BILLING_MODE_META: Record<BillingMode, BillingModeMeta> = {
  token: { label: '按 Token', short: 'Token', description: '按输入 / 输出 token 用量计费，单价为每 100 万 token。适合对话、嵌入等文本模型。' },
  request: { label: '按次', short: '按次', description: '每次请求收取固定费用，与 token 用量无关。适合图片生成、搜索、重排等按调用计费的模型。' },
  image: { label: '按张', short: '按张', description: '按响应中的输出图片张数计费，可另加每次请求的固定费用。' },
  audio: { label: '按时长 / 字符', short: '按时长', description: '转写 / 翻译按音频时长计费，语音合成按输入字符数计费；上游返回 token 用量时按音频 token 单价计。' },
  custom: { label: '自定义组合', short: '组合', description: '同时设置 token、每次请求、图片与音频价格，费用为各部分之和。' },
}

/** Tailwind classes for the 计费方式 badge per mode (light + dark). */
export const BILLING_MODE_BADGE_CLASSES: Record<BillingMode, string> = {
  token: 'border-border text-muted-foreground',
  request: 'border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-300',
  image: 'border-fuchsia-500/40 bg-fuchsia-500/10 text-fuchsia-800 dark:text-fuchsia-300',
  audio: 'border-sky-500/40 bg-sky-500/10 text-sky-800 dark:text-sky-300',
  custom: 'border-violet-500/40 bg-violet-500/10 text-violet-800 dark:text-violet-300',
}

/** True when `key` carries a price: fallback prices whenever present, the rest when > 0. */
export function isSetPrice(key: PriceFieldKey, v: string | null | undefined): v is string {
  if (typeof v !== 'string' || !isValidAmount(v))
    return false
  return FALLBACK_PRICE_KEYS.includes(key) ? true : amountSign(v) > 0
}

/** The amounts of `p` that carry a price, in `PRICE_FIELD_KEYS` order. */
export function setPriceKeys(p: PriceAmounts | null | undefined): PriceFieldKey[] {
  return p ? PRICE_FIELD_KEYS.filter(k => isSetPrice(k, p[k])) : []
}

/**
 * The billing mode a price version was created with, from the amounts it sets:
 * only perRequest → `request`; perImage (+ perRequest) → `image`; audio prices
 * (+ perRequest) → `audio`; token prices only → `token`; anything else → `custom`.
 * A version that sets nothing (or no version) is `token`, the default.
 */
export function inferBillingMode(p: PriceAmounts | null | undefined): BillingMode {
  const set = setPriceKeys(p)
  if (!set.length)
    return 'token'
  const within = (m: BillingMode) => set.every(k => BILLING_MODE_FIELDS[m].includes(k))
  if (within('request'))
    return 'request'
  if (within('image') && set.includes('perImage'))
    return 'image'
  if (within('audio'))
    return 'audio'
  if (within('token'))
    return 'token'
  return 'custom'
}

/** Form values of the price dialog: one (possibly blank) string per amount. */
export type PriceFormAmounts = Record<PriceFieldKey, string>

export type PriceAmountBody = Pick<PriceInput, PriceFieldKey>

/**
 * The amount part of `POST /api/admin/prices` for `mode`: the mode's fields as
 * typed, everything else as the backend's "not set" (required amounts "0",
 * optional amounts omitted). Blank required amounts become "0", blank optional
 * amounts are omitted (null-able prices then fall back to the text price).
 */
export function priceBodyForMode(mode: BillingMode, values: PriceFormAmounts): PriceAmountBody {
  const fields = BILLING_MODE_FIELDS[mode]
  const body: PriceAmountBody = { inputPerM: '0', outputPerM: '0', cacheReadPerM: '0', cacheWritePerM: '0', perRequest: '0' }
  for (const k of PRICE_FIELD_KEYS) {
    if (!fields.includes(k))
      continue
    const v = values[k].trim()
    if ((REQUIRED_KEYS as readonly string[]).includes(k))
      body[k as typeof REQUIRED_KEYS[number]] = v || '0'
    else if (v !== '')
      body[k] = v
  }
  return body
}

const AMOUNT_HINT = '非负十进制数，最多 9 位小数'

const EMPTY_MODE_ERRORS: Record<BillingMode, { key: PriceFieldKey, message: string }> = {
  token: { key: 'inputPerM', message: '至少填写一项大于 0 的 token 单价' },
  request: { key: 'perRequest', message: '请填写大于 0 的每次请求价格' },
  image: { key: 'perImage', message: '请填写大于 0 的每张图片价格（或每次请求价格）' },
  audio: { key: 'perMinute', message: '至少填写一项大于 0 的音频价格' },
  custom: { key: 'inputPerM', message: '至少填写一项大于 0 的价格' },
}

/**
 * Field errors for the active mode's amounts: malformed values, and none of the
 * mode's fields above 0. Fields of other modes are ignored (they are not submitted).
 */
export function validateModeAmounts(mode: BillingMode, values: PriceFormAmounts): Partial<Record<PriceFieldKey, string>> {
  const errors: Partial<Record<PriceFieldKey, string>> = {}
  let charged = false
  for (const k of BILLING_MODE_FIELDS[mode]) {
    const v = values[k].trim()
    if (v === '')
      continue
    if (!isValidAmount(v))
      errors[k] = AMOUNT_HINT
    else if (amountSign(v) > 0)
      charged = true
  }
  if (!charged && !Object.keys(errors).length) {
    const e = EMPTY_MODE_ERRORS[mode]
    errors[e.key] = e.message
  }
  return errors
}

// ---------------------------------------------------------------------------
// Price summary (tables, plaza card / sheet, 我的模型)
// ---------------------------------------------------------------------------

export interface PricePart {
  key: PriceFieldKey
  /** Short label ("输入", "每张图片"). */
  label: string
  amount: string
  /** Billing unit after the slash: "1M" (tokens), "次", "张", "分钟", "1M 字符". */
  unit: string
  /** True for per-1M-token prices (shown with their label; the others read as "$x / 张"). */
  token: boolean
}

export interface PriceSummary {
  mode: BillingMode
  /** The parts that define the price (shown inline). */
  main: PricePart[]
  /** The other set prices (cache, image input, audio tokens …): tooltips / secondary lines. */
  extra: PricePart[]
}

const PART_META: Record<PriceFieldKey, { label: string, unit: string }> = {
  inputPerM: { label: '输入', unit: '1M' },
  outputPerM: { label: '输出', unit: '1M' },
  cacheReadPerM: { label: '缓存读', unit: '1M' },
  cacheWritePerM: { label: '缓存写', unit: '1M' },
  perRequest: { label: '每次请求', unit: '次' },
  perImage: { label: '每张图片', unit: '张' },
  imageInputPerM: { label: '图片输入', unit: '1M' },
  audioInputPerM: { label: '音频输入', unit: '1M' },
  audioOutputPerM: { label: '音频输出', unit: '1M' },
  perMinute: { label: '每分钟音频', unit: '分钟' },
  perMCharacters: { label: '每 1M 字符', unit: '1M 字符' },
}

/** One summary part for `key` (any amount, even unset ones → "0"). */
export function pricePart(key: PriceFieldKey, amount: string | null | undefined): PricePart {
  const meta = PART_META[key]
  return { key, label: meta.label, amount: amount ?? '0', unit: meta.unit, token: meta.unit === '1M' }
}

/**
 * The compact summary of a price: the billing mode, the parts that define it
 * and the remaining set prices. Token prices always show input + output (even
 * at 0, as before); per-call / per-image / audio prices only when charged, so a
 * per-call-only price never reads as "$0". `null` → `null` (未定价).
 */
export function priceSummary(p: PriceAmounts | null | undefined): PriceSummary | null {
  if (!p)
    return null
  const mode = inferBillingMode(p)
  const set = setPriceKeys(p)
  const has = (k: PriceFieldKey) => set.includes(k)
  const main: PriceFieldKey[] = []
  switch (mode) {
    case 'token':
      main.push('inputPerM', 'outputPerM')
      break
    case 'request':
      main.push('perRequest')
      break
    case 'image':
      main.push('perImage')
      break
    case 'audio': {
      const units = (['perMinute', 'perMCharacters'] as const).filter(has)
      main.push(...(units.length ? units : (['audioInputPerM', 'audioOutputPerM'] as const).filter(has)))
      break
    }
    case 'custom':
      if (has('inputPerM') || has('outputPerM'))
        main.push('inputPerM', 'outputPerM')
      main.push(...(['perImage', 'perMinute', 'perMCharacters'] as const).filter(has))
      break
  }
  if (mode !== 'token' && mode !== 'request' && has('perRequest'))
    main.push('perRequest')
  if (!main.length)
    main.push(...set)
  const extra = set.filter(k => !main.includes(k))
  return { mode, main: main.map(k => pricePart(k, p[k])), extra: extra.map(k => pricePart(k, p[k])) }
}

/** "$0.04 / 次", "$15 / 1M 字符"; token parts with their label: "输入 $2.5 / 1M". */
export function formatPricePart(part: PricePart, fmt: (amount: string) => string, opts: { label?: boolean } = {}): string {
  const label = opts.label ?? part.token
  return `${label ? `${part.label} ` : ''}${fmt(part.amount)} / ${part.unit}`
}

/**
 * One-line summary text: token prices as "输入 $2.5 · 输出 $10 / 1M", other parts
 * as "$0.04 / 次", joined with " + " ("$0.02 / 张 + $0.01 / 次"). `null` → "未定价".
 */
export function priceSummaryText(s: PriceSummary | null, fmt: (amount: string) => string): string {
  if (!s)
    return '未定价'
  const tokens = s.main.filter(p => p.token)
  const out: string[] = []
  if (tokens.length)
    out.push(`${tokens.map(p => `${p.label} ${fmt(p.amount)}`).join(' · ')} / 1M`)
  for (const p of s.main.filter(p => !p.token))
    out.push(formatPricePart(p, fmt))
  return out.join(' + ')
}

/** Caption for a plaza price block: "每 1M tokens" / "按次计费" / … */
export const BILLING_MODE_CAPTIONS: Record<BillingMode, string> = {
  token: '每 1M tokens',
  request: '按次计费',
  image: '按张计费',
  audio: '按时长 / 字符计费',
  custom: '组合计费',
}

/** Tooltip text: the billing mode, then every set price on its own line ("每次请求：$0.04 / 次"). */
export function priceSummaryTitle(s: PriceSummary | null, fmt: (amount: string) => string): string {
  if (!s)
    return '未定价'
  const lines = [...s.main, ...s.extra].map(p => `${p.label}：${fmt(p.amount)} / ${p.unit}`)
  return [`计费方式：${BILLING_MODE_META[s.mode].label}`, ...lines].join('\n')
}

/** True when the summary shows token prices (input / output) among its main parts. */
export function showsTokenPrices(s: PriceSummary | null): boolean {
  return !!s && s.main.some(p => p.token && (p.key === 'inputPerM' || p.key === 'outputPerM'))
}

/** Unit caption of a plaza price block ("每 1M tokens", "按次计费" …). */
export function priceCaption(s: PriceSummary | null): string {
  if (!s)
    return BILLING_MODE_CAPTIONS.token
  if (s.mode === 'custom')
    return showsTokenPrices(s) ? 'token 价格每 1M · 组合计费' : BILLING_MODE_CAPTIONS.custom
  return BILLING_MODE_CAPTIONS[s.mode]
}
