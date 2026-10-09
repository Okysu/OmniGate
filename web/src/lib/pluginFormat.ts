// Value formatting for declarative plugin UI (phase2-api.md §5 `format`) and
// channel list badges. Money stays a decimal string end to end (lib/money.ts).
import type { CurrencyInfo } from './money'
import { formatDateTime, formatRelative } from './format'
import { formatMoney } from './money'

export const MISSING = '—'

/** Well-known symbols; anything else is shown as "<CODE> 1,234.00". */
const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  CNY: '¥',
  RMB: '¥',
  EUR: '€',
  GBP: '£',
  JPY: '¥',
  HKD: 'HK$',
  KRW: '₩',
  INR: '₹',
}

/** Fraction digits per ISO 4217 (from Intl; never touches the amount itself). */
function currencyDecimals(code: string): number {
  try {
    return new Intl.NumberFormat('en-US', { style: 'currency', currency: code }).resolvedOptions().maximumFractionDigits ?? 2
  }
  catch {
    return 2
  }
}

/** Currency display info for an upstream currency code (e.g. "CNY"). */
export function currencyFromCode(code: string | null | undefined): CurrencyInfo | null {
  const c = (code ?? '').trim().toUpperCase()
  if (!/^[A-Z]{3}$/.test(c))
    return null
  return { code: c, symbol: CURRENCY_SYMBOLS[c] ?? `${c} `, decimals: currencyDecimals(c) }
}

const DECIMAL_RE = /^-?\d+(?:\.\d+)?$/

function groupDecimalString(s: string): string {
  const negative = s.startsWith('-')
  const [int = '0', frac] = (negative ? s.slice(1) : s).split('.')
  const grouped = int.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return `${negative ? '-' : ''}${grouped}${frac !== undefined ? `.${frac}` : ''}`
}

const numberFormatter = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 6 })

function toText(value: unknown): string {
  if (typeof value === 'string')
    return value
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint')
    return String(value)
  try {
    return JSON.stringify(value)
  }
  catch {
    return String(value)
  }
}

/** Timestamps: ISO strings, or epoch numbers (seconds below 1e11, else milliseconds). */
function toIso(value: unknown): string | null {
  if (typeof value === 'number' && Number.isFinite(value)) {
    const ms = Math.abs(value) < 1e11 ? value * 1000 : value
    const d = new Date(ms)
    return Number.isNaN(d.getTime()) ? null : d.toISOString()
  }
  if (typeof value === 'string' && value.trim() !== '') {
    const d = new Date(value)
    return Number.isNaN(d.getTime()) ? null : d.toISOString()
  }
  return null
}

export interface FormattedValue {
  /** Display text ("—" when missing). */
  text: string
  /** Set for `boolean` format: render as a 是/否 badge. */
  bool?: boolean
  /** Raw value for a tooltip (never set for `money`: amounts are only shown rounded). */
  title?: string
  missing: boolean
}

export interface FormatOptions {
  /** Currency code for `money` (from `currencyBind`, a table row's `currency`, or a badge). */
  currency?: string | null
  now?: number
}

/**
 * Formats a bound value.
 * - `number`: thousands separators; numeric strings are grouped without float conversion
 * - `money`: decimal string (numbers are stringified first) with the currency's symbol,
 *   3 decimals like every displayed amount (`formatMoney`); no full-precision `title`
 * - `percent`: the value is a ratio (0.42 → 42%)
 * - `boolean`: 是 / 否
 * - `datetime` / `relativeTime`: ISO strings or epoch seconds/milliseconds
 * - `text` / unknown formats: as is (objects as JSON)
 * `null` / `undefined` / "" render as "—".
 */
export function formatUiValue(value: unknown, format: string | null | undefined, opts: FormatOptions = {}): FormattedValue {
  if (value === null || value === undefined || value === '')
    return { text: MISSING, missing: true }
  switch (format) {
    case 'number': {
      if (typeof value === 'number' && Number.isFinite(value))
        return { text: numberFormatter.format(value), title: String(value), missing: false }
      if (typeof value === 'string' && DECIMAL_RE.test(value.trim()))
        return { text: groupDecimalString(value.trim()), title: value, missing: false }
      return { text: toText(value), missing: false }
    }
    case 'money': {
      const amount = typeof value === 'number' && Number.isFinite(value) ? String(value) : typeof value === 'string' ? value.trim() : null
      if (amount === null || !DECIMAL_RE.test(amount))
        return { text: toText(value), missing: false }
      const cur = currencyFromCode(opts.currency)
      const text = cur
        ? formatMoney(amount, cur)
        : formatMoney(amount, { code: '', symbol: '', decimals: 2 }, { plain: true })
      return { text, title: cur?.code, missing: false }
    }
    case 'percent': {
      const n = typeof value === 'number' ? value : typeof value === 'string' && DECIMAL_RE.test(value.trim()) ? Number(value) : Number.NaN
      if (!Number.isFinite(n))
        return { text: toText(value), missing: false }
      const pct = n * 100
      const digits = Number.isInteger(pct) ? 0 : Math.abs(pct) < 10 ? 2 : 1
      return { text: `${pct.toFixed(digits)}%`, title: String(value), missing: false }
    }
    case 'boolean': {
      const b = value === true || value === 'true' || value === 1 ? true : value === false || value === 'false' || value === 0 ? false : null
      if (b === null)
        return { text: toText(value), missing: false }
      return { text: b ? '是' : '否', bool: b, missing: false }
    }
    case 'datetime': {
      const iso = toIso(value)
      return iso ? { text: formatDateTime(iso), title: iso, missing: false } : { text: toText(value), missing: false }
    }
    case 'relativeTime': {
      const iso = toIso(value)
      return iso ? { text: formatRelative(iso, opts.now), title: formatDateTime(iso), missing: false } : { text: toText(value), missing: false }
    }
    default:
      return { text: toText(value), missing: false }
  }
}
