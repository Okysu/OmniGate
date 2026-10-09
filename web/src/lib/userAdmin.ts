// User management (phase7-api.md §2): disable form (reason / until), PATCH and batch
// bodies, batch result summaries, status tooltips and the login page's disabled notice.
import type { User, UserBatchAction, UserBatchInput, UserBatchResult, UserPatch } from './types'
import { formatDateTime } from './format'
import { fromLocalInput } from './timeRange'

export const DISABLE_REASON_MAX = 200
/** `POST /api/admin/users/batch` accepts 1–200 ids. */
export const USER_BATCH_LIMIT = 200

export interface DisableForm {
  reason: string
  /** `datetime-local` value; '' = permanent. */
  until: string
}

export function emptyDisableForm(): DisableForm {
  return { reason: '', until: '' }
}

/** Errors keyed like the server's 422 details (`disabledReason`, `disabledUntil`). */
export function validateDisableForm(f: DisableForm, now: number = Date.now()): Record<string, string> {
  const e: Record<string, string> = {}
  const reason = f.reason.trim()
  if (!reason)
    e.disabledReason = '请填写停用原因（用户登录时可以看到）'
  else if ([...reason].length > DISABLE_REASON_MAX)
    e.disabledReason = `最多 ${DISABLE_REASON_MAX} 个字符`
  if (f.until) {
    const iso = fromLocalInput(f.until)
    if (!iso)
      e.disabledUntil = '无效的时间'
    else if (new Date(iso).getTime() <= now)
      e.disabledUntil = '到期时间必须晚于现在'
  }
  return e
}

/** ISO time of the form's `until`, or null (= permanent). */
export function disableUntilIso(f: DisableForm): string | null {
  return f.until ? fromLocalInput(f.until) : null
}

export function disablePatch(user: Pick<User, 'version'>, f: DisableForm): UserPatch {
  return { status: 'disabled', disabledReason: f.reason.trim(), disabledUntil: disableUntilIso(f), version: user.version }
}

export function enablePatch(user: Pick<User, 'version'>): UserPatch {
  return { status: 'active', version: user.version }
}

export const BATCH_ACTION_LABELS: Record<UserBatchAction, string> = {
  disable: '停用',
  enable: '启用',
  logout: '强制下线',
  set_group: '设置分组',
}

/** Batch body; `reason` / `until` only for `disable`, `groupId` only for `set_group`. */
export function buildBatchInput(ids: readonly string[], action: UserBatchAction, f?: DisableForm, groupId?: string): UserBatchInput {
  const body: UserBatchInput = { ids: [...new Set(ids)], action }
  if (action === 'disable' && f) {
    body.reason = f.reason.trim()
    body.until = disableUntilIso(f)
  }
  if (action === 'set_group' && groupId)
    body.groupId = groupId
  return body
}

export const USER_ERROR_MESSAGES: Record<string, string> = {
  cannot_disable_self: '不能停用自己',
  last_admin: '不能停用最后一位系统管理员',
  last_system_admin: '不能停用最后一位系统管理员',
  not_found: '用户不存在',
  version_conflict: '数据已被他人修改',
  group_not_found: '分组不存在',
}

export interface BatchFailureRow {
  id: string
  name: string
  message: string
}

export interface BatchSummary {
  succeeded: number
  failed: BatchFailureRow[]
}

/** Failures with display names (falls back to a shortened id) and friendly messages. */
export function summarizeBatch(res: UserBatchResult, names: ReadonlyMap<string, string>): BatchSummary {
  return {
    succeeded: res.succeeded.length,
    failed: res.failed.map(f => ({
      id: f.id,
      name: names.get(f.id) ?? `${f.id.slice(0, 8)}…`,
      message: f.message || USER_ERROR_MESSAGES[f.code] || f.code,
    })),
  }
}

/** "已停用 3 位用户" / "已停用 2 位用户，1 位失败". */
export function batchResultText(action: UserBatchAction, s: BatchSummary): string {
  const verb = action === 'logout' ? '已强制下线' : action === 'set_group' ? '已移动' : `已${BATCH_ACTION_LABELS[action]}`
  const head = action === 'set_group' ? `${verb} ${s.succeeded} 位用户的分组` : `${verb} ${s.succeeded} 位用户`
  return s.failed.length ? `${head}，${s.failed.length} 位失败` : head
}

/** Status hover text: reason and until (or "永久停用"); null for active users. */
export function disabledDetails(u: Pick<User, 'status' | 'disabledReason' | 'disabledUntil'>): string | null {
  if (u.status !== 'disabled')
    return null
  const lines: string[] = []
  if (u.disabledReason)
    lines.push(`原因：${u.disabledReason}`)
  lines.push(u.disabledUntil ? `到期自动启用：${formatDateTime(u.disabledUntil)}` : '永久停用（需管理员手动启用）')
  return lines.join('\n')
}

/** Short badge text for the status column: "停用至 10/15 12:00" / "停用". */
export function disabledBadge(u: Pick<User, 'status' | 'disabledUntil'>): string | null {
  if (u.status !== 'disabled')
    return null
  return u.disabledUntil ? `停用至 ${formatDateTime(u.disabledUntil)}` : '已停用'
}

/** Quick picks for "到期时间" (relative to now). */
export const UNTIL_PRESETS: { label: string, hours: number }[] = [
  { label: '1 天', hours: 24 },
  { label: '7 天', hours: 24 * 7 },
  { label: '30 天', hours: 24 * 30 },
]

export interface DisabledNotice {
  reason: string | null
  /** Formatted local time, or null (permanent / unknown). */
  until: string | null
}

function first(v: unknown): string {
  const x = Array.isArray(v) ? v[0] : v
  return typeof x === 'string' ? x : ''
}

/**
 * Login page: `?error=account_disabled&reason=…&until=…` (reason / until are optional and
 * shown as plain text; an invalid or past `until` is ignored).
 */
export function disabledNotice(query: Record<string, unknown>, now: number = Date.now()): DisabledNotice | null {
  if (first(query.error) !== 'account_disabled')
    return null
  const reason = first(query.reason).trim().slice(0, DISABLE_REASON_MAX) || null
  const raw = first(query.until).trim()
  const t = raw ? new Date(raw).getTime() : Number.NaN
  const until = Number.isFinite(t) && t > now ? formatDateTime(new Date(t).toISOString()) : null
  return { reason, until }
}
