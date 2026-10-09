// Redeem-code helpers mirroring server/internal/billing/code.go (NormalizeCode).

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
const SYMBOLS = 20
const GROUP = 5
const PREFIX = 'OG'

/**
 * Canonical display form (`OG-XXXXX-XXXXX-XXXXX-XXXXX`) of user input, or null
 * when the input cannot be a valid code. Case, spaces and hyphens are ignored,
 * the `OG` prefix is optional, and Crockford ambiguities (I/L → 1, O → 0) are mapped.
 */
export function normalizeRedeemCode(input: string): string | null {
  let s = input.toUpperCase().replace(/[\s\-\u3000]/g, '')
  if (/[^\x20-\x7E]/.test(s))
    return null
  if (s.length === SYMBOLS + PREFIX.length && (s.startsWith('OG') || s.startsWith('0G')))
    s = s.slice(PREFIX.length)
  if (s.length !== SYMBOLS)
    return null
  let out = ''
  for (const ch of s) {
    const c = ch === 'I' || ch === 'L' ? '1' : ch === 'O' ? '0' : ch
    if (!CROCKFORD.includes(c))
      return null
    out += c
  }
  const groups: string[] = []
  for (let i = 0; i < out.length; i += GROUP)
    groups.push(out.slice(i, i + GROUP))
  return [PREFIX, ...groups].join('-')
}

/** Friendly Chinese messages for redeem error codes (fallback: server message). */
export const REDEEM_ERROR_MESSAGES: Record<string, string> = {
  redeem_invalid: '兑换码无效，请检查是否输入正确。',
  redeem_expired: '该兑换码已过期。',
  redeem_not_started: '该兑换码尚未到生效时间，请稍后再试。',
  redeem_used_up: '该兑换码已被使用完。',
  redeem_user_limit: '你已达到该批次兑换码的兑换次数上限。',
  redeem_batch_disabled: '该兑换码所属批次已被停用。',
  rate_limited: '尝试过于频繁（每分钟最多 5 次），请稍后再试。',
}
