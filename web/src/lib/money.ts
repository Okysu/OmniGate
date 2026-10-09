// Decimal-string money helpers. The backend sends every amount as a decimal
// string with at most 9 fractional digits (ADR-0007). We never convert amounts
// to JS numbers: arithmetic uses BigInt nano units, formatting is string-based.

export interface CurrencyInfo {
  code: string
  symbol: string
  decimals: number
}

/** Scale used by the backend (nano units). */
export const MONEY_SCALE = 9

const DECIMAL_RE = /^([+-])?(\d+)(?:\.(\d+))?$/
const NANO = 10n ** BigInt(MONEY_SCALE)

interface ParsedDecimal {
  negative: boolean
  int: string
  frac: string
}

function parse(value: string): ParsedDecimal | null {
  const m = DECIMAL_RE.exec(value.trim())
  if (!m)
    return null
  const int = m[2]!.replace(/^0+(?=\d)/, '')
  const frac = m[3] ?? ''
  const zero = /^0*$/.test(int) && /^0*$/.test(frac)
  return { negative: m[1] === '-' && !zero, int, frac }
}

/**
 * Validates user input for an amount: plain decimal, at most `maxFrac`
 * fractional digits (default 9), optional leading minus when `allowNegative`.
 */
export function isValidAmount(value: string, opts: { allowNegative?: boolean, maxFrac?: number } = {}): boolean {
  const maxFrac = opts.maxFrac ?? MONEY_SCALE
  const re = opts.allowNegative
    ? new RegExp(`^-?\\d+(\\.\\d{1,${maxFrac}})?$`)
    : new RegExp(`^\\d+(\\.\\d{1,${maxFrac}})?$`)
  return re.test(value.trim())
}

/** Decimal string → BigInt nano units. Throws on invalid input or > 9 decimals. */
export function toNano(value: string): bigint {
  const p = parse(value)
  if (!p || p.frac.length > MONEY_SCALE)
    throw new Error(`invalid amount: ${value}`)
  const n = BigInt(p.int + p.frac.padEnd(MONEY_SCALE, '0'))
  return p.negative ? -n : n
}

/** BigInt nano units → canonical decimal string (trailing zeros trimmed). */
export function fromNano(nano: bigint): string {
  const negative = nano < 0n
  const abs = negative ? -nano : nano
  const int = (abs / NANO).toString()
  const frac = (abs % NANO).toString().padStart(MONEY_SCALE, '0').replace(/0+$/, '')
  const s = frac ? `${int}.${frac}` : int
  return negative ? `-${s}` : s
}

/** Exact sum of two decimal strings. */
export function addAmounts(a: string, b: string): string {
  return fromNano(toNano(a) + toNano(b))
}

/** -1, 0 or 1. Invalid input counts as 0. */
export function amountSign(value: string | null | undefined): -1 | 0 | 1 {
  const p = value == null ? null : parse(value)
  if (!p || (/^0*$/.test(p.int) && /^0*$/.test(p.frac)))
    return 0
  return p.negative ? -1 : 1
}

/**
 * Rounds a decimal string to `decimals` fractional digits, half away from zero,
 * and pads with zeros to exactly `decimals` digits. Invalid input is returned as is.
 */
export function roundDecimal(value: string, decimals: number): string {
  const p = parse(value)
  if (!p)
    return value
  const d = Math.max(0, Math.floor(decimals))
  let digits = p.int + p.frac.padEnd(d, '0').slice(0, d)
  const next = p.frac.charAt(d)
  if (next !== '' && next >= '5')
    digits = (BigInt(digits) + 1n).toString().padStart(digits.length, '0')
  const intPart = d > 0 ? digits.slice(0, digits.length - d) || '0' : digits
  const fracPart = d > 0 ? digits.slice(digits.length - d) : ''
  const isZero = /^0*$/.test(intPart) && /^0*$/.test(fracPart)
  const body = fracPart ? `${intPart}.${fracPart}` : intPart
  return p.negative && !isZero ? `-${body}` : body
}

function groupThousands(int: string): string {
  return int.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

/**
 * Fraction digits of every displayed amount, whatever the currency's own
 * `decimals`: half away from zero, padded (`$0.0025986` → `$0.003`, `$24` →
 * `$24.000`). The full stored precision is never displayed — editable inputs
 * use the raw decimal string instead.
 */
export const DISPLAY_DECIMALS = 3

export interface FormatMoneyOptions {
  /** Prefix positive values with `+`. */
  signed?: boolean
  /** Omit the currency symbol. */
  plain?: boolean
}

export const FALLBACK_CURRENCY: CurrencyInfo = { code: 'USD', symbol: '$', decimals: 2 }

/**
 * Formats a decimal string for display with `DISPLAY_DECIMALS` fraction digits,
 * e.g. `formatMoney('-1234.5', USD)` → `-$1,234.500`. A non-zero amount that
 * rounds to zero shows as a lower bound (`<$0.001`, `-<$0.001`) so tiny charges
 * never look free. `null`/`undefined` render as an em dash; unparsable strings
 * are returned unchanged.
 */
export function formatMoney(
  value: string | null | undefined,
  currency: CurrencyInfo | null | undefined,
  opts: FormatMoneyOptions = {},
): string {
  if (value == null || value === '')
    return '—'
  const cur = currency ?? FALLBACK_CURRENCY
  const p = parse(value)
  if (!p)
    return value
  const symbol = opts.plain ? '' : cur.symbol
  const num = roundDecimal(value, DISPLAY_DECIMALS).replace(/^-/, '')
  const nonZero = /[1-9]/.test(p.int + p.frac)
  if (nonZero && !/[1-9]/.test(num)) {
    const body = `<${symbol}0.${'0'.repeat(DISPLAY_DECIMALS - 1)}1`
    return p.negative ? `-${body}` : opts.signed ? `+${body}` : body
  }
  const [int = '0', frac] = num.split('.')
  const body = `${symbol}${groupThousands(int)}${frac !== undefined ? `.${frac}` : ''}`
  if (p.negative && nonZero)
    return `-${body}`
  if (opts.signed && nonZero)
    return `+${body}`
  return body
}
