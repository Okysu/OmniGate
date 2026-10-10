// Referral (phase15-api.md §4): invite-code capture from `?invite=` on any page
// (stored 30 days in localStorage `og_invite`, sent with the login redirect) and
// display helpers for the 邀请返利 page.
import type { MoneyFormatter } from './quota'
import { amountSign } from './money'

export const INVITE_STORAGE_KEY = 'og_invite'
export const INVITE_TTL_MS = 30 * 24 * 60 * 60 * 1000

/** Codes are 8 uppercase letters / digits; accept a little slack (case, length) but nothing odd. */
const INVITE_RE = /^[A-Z0-9]{4,32}$/

type StorageLike = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

function defaultStorage(): StorageLike | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  }
  catch {
    return null
  }
}

/** Canonical invite code from a query value (`?invite=ab12cd34` → `AB12CD34`), or null. */
export function normalizeInviteCode(value: unknown): string | null {
  const v = Array.isArray(value) ? value[0] : value
  if (typeof v !== 'string')
    return null
  const code = v.trim().toUpperCase()
  return INVITE_RE.test(code) ? code : null
}

/** Remembers an invite code for 30 days (a newer link replaces an older one). Storage errors are ignored. */
export function saveInviteCode(value: unknown, now: number = Date.now(), storage: StorageLike | null = defaultStorage()): string | null {
  const code = normalizeInviteCode(value)
  if (!code || !storage)
    return code
  try {
    storage.setItem(INVITE_STORAGE_KEY, JSON.stringify({ code, expiresAt: now + INVITE_TTL_MS }))
  }
  catch {
    // Private mode / blocked storage: the code still works if it is in the login URL.
  }
  return code
}

/** The stored invite code, or null when missing, malformed or expired (expired entries are removed). */
export function loadInviteCode(now: number = Date.now(), storage: StorageLike | null = defaultStorage()): string | null {
  if (!storage)
    return null
  try {
    const raw = storage.getItem(INVITE_STORAGE_KEY)
    if (!raw)
      return null
    const parsed = JSON.parse(raw) as { code?: unknown, expiresAt?: unknown }
    const code = normalizeInviteCode(parsed?.code)
    if (!code || typeof parsed.expiresAt !== 'number' || parsed.expiresAt <= now) {
      storage.removeItem(INVITE_STORAGE_KEY)
      return null
    }
    return code
  }
  catch {
    return null
  }
}

/**
 * Captures `?invite=` from the current URL (any page, before route guards drop the
 * query). Returns the effective code: the URL's, else the stored one.
 */
export function captureInviteFromUrl(search: string, now: number = Date.now(), storage: StorageLike | null = defaultStorage()): string | null {
  let value: string | null
  try {
    value = new URLSearchParams(search).get('invite')
  }
  catch {
    value = null
  }
  return saveInviteCode(value, now, storage) ?? loadInviteCode(now, storage)
}

/** "10%" / "12.5%" (rate is a decimal-string percentage). */
export function formatRate(rate: string | null | undefined): string {
  if (!rate)
    return '—'
  const n = Number(rate)
  return Number.isFinite(n) ? `${Number(n.toFixed(2))}%` : `${rate}%`
}

/** The rebate rule in one sentence for the 邀请返利 page. */
export function referralRuleText(info: { rate: string, minRecharge: string }, money: MoneyFormatter): string {
  const min = amountSign(info.minRecharge) > 0 ? ` ≥ ${money(info.minRecharge)}` : ''
  return `好友通过你的链接注册后，每次使用兑换码充值${min}，你获得充值金额 ${formatRate(info.rate)} 的余额返利。`
}
