// Quota reset cards (phase11-api.md §2): labels, grouping of the user's cards, the
// before → after preview of the rules a card resets, and the admin issue form.
// Pure functions: money formatting is injected (`useCurrency().money`).
import type { MoneyFormatter } from './quota'
import type { QuotaWindow, ResetCard, ResetCardIssueInput, ResetCardKind, ResetCardTarget, RuleUsage } from './types'
import { durationMinutes, formatDuration, formatMeterAmount, formatRemaining, ruleTitle } from './quota'
import { fromLocalInput } from './timeRange'

export const RESET_CARD_KINDS: ResetCardKind[] = ['5h', 'weekly', 'both']

export const RESET_CARD_KIND_LABELS: Record<ResetCardKind, string> = {
  '5h': '5小时重置卡',
  'weekly': '周重置卡',
  'both': '双重置卡',
}

/** What a card resets (window length decides, not the rule id). */
export const RESET_CARD_KIND_HINTS: Record<ResetCardKind, string> = {
  '5h': '清零 5 小时额度，从使用时起重新计时 5 小时',
  'weekly': '清零每周（7 天）额度，从使用时起重新计时 7 天',
  'both': '同时清零 5 小时与每周额度',
}

export const RESET_CARD_STATUS_LABELS: Record<string, string> = {
  available: '可使用',
  used: '已使用',
  expired: '已过期',
  revoked: '已作废',
}

/** Friendly messages for the use endpoint's error codes (fallback: server message). */
export const RESET_CARD_ERROR_MESSAGES: Record<string, string> = {
  card_used: '这张重置卡已经用过了。',
  card_expired: '这张重置卡已过期。',
  card_revoked: '这张重置卡已被管理员作废。',
  card_not_applicable: '所选订阅没有这张卡能重置的额度，卡未被消耗。',
  card_plan_not_allowed: '这张重置卡不能用于所选套餐，卡未被消耗。',
  subscription_not_active: '订阅已不是有效状态（可能已过期或已被取消）。',
}

export const CARD_QUANTITY_MAX = 100
export const CARD_USERS_MAX = 1000
export const CARD_NOTE_MAX = 200

const FIVE_HOURS = 300
const ONE_WEEK = 7 * 1440

/** Whether a card of `kind` resets a rule with window `w` (mirrors the server's cardMatches). */
export function cardMatchesWindow(kind: ResetCardKind, w: QuotaWindow): boolean {
  if (w.kind !== 'session' && w.kind !== 'rolling')
    return false
  const m = durationMinutes(w.duration)
  if (kind === '5h')
    return m === FIVE_HOURS
  if (kind === 'weekly')
    return m === ONE_WEEK
  return m === FIVE_HOURS || m === ONE_WEEK
}

// ---------------------------------------------------------------------------
// The user's cards
// ---------------------------------------------------------------------------

/** Usable cards of one batch: same kind, expiry, plan restriction and note. */
export interface CardLot {
  batchId: string
  /** Soonest to expire first (all equal within a batch). */
  cards: ResetCard[]
  expiresAt: string | null
  plans: { id: string, name: string }[]
  note: string
}

export interface CardGroup {
  kind: ResetCardKind
  /** Number of usable cards of the kind. */
  count: number
  /** Lots soonest to expire first (then newest batch first). */
  lots: CardLot[]
  /** Earliest expiry among them (null: none expires). */
  soonestExpiry: string | null
}

function expiryTime(expiresAt: string | null): number {
  return expiresAt ? new Date(expiresAt).getTime() : Number.POSITIVE_INFINITY
}

/** Usable cards grouped by kind (in {@link RESET_CARD_KINDS} order, empty kinds omitted) and batch. */
export function groupUsableCards(items: readonly ResetCard[]): CardGroup[] {
  const out: CardGroup[] = []
  for (const kind of RESET_CARD_KINDS) {
    const lots = new Map<string, CardLot>()
    let count = 0
    for (const c of items) {
      if (c.kind !== kind || c.status !== 'available')
        continue
      count++
      const lot = lots.get(c.batchId)
      if (lot)
        lot.cards.push(c)
      else
        lots.set(c.batchId, { batchId: c.batchId, cards: [c], expiresAt: c.expiresAt, plans: c.plans, note: c.note })
    }
    if (!count)
      continue
    const sorted = [...lots.values()].sort((a, b) => expiryTime(a.expiresAt) - expiryTime(b.expiresAt) || (a.batchId < b.batchId ? 1 : -1))
    out.push({ kind, count, lots: sorted, soonestExpiry: sorted[0]!.expiresAt })
  }
  return out
}

/** Used, expired and revoked cards (history), in the server's order. */
export function cardHistory(items: readonly ResetCard[]): ResetCard[] {
  return items.filter(c => c.status !== 'available')
}

/** "长期有效" / "剩余 3 天" for a usable card. */
export function cardExpiryText(expiresAt: string | null, now: number = Date.now()): string {
  return expiresAt ? formatRemaining(expiresAt, now) : '长期有效'
}

/** "仅限 Pro、Max" / "" (every plan). */
export function planRestrictionText(plans: readonly { name: string }[]): string {
  return plans.length ? `仅限 ${plans.map(p => p.name).join('、')}` : ''
}

// ---------------------------------------------------------------------------
// Before → after preview
// ---------------------------------------------------------------------------

const pad = (n: number) => String(n).padStart(2, '0')

/** "14:35" when `iso` is on the same local day as `now`, else "10-12 14:35". */
export function formatClock(iso: string | number, now: number = Date.now()): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()))
    return String(iso)
  const n = new Date(now)
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const sameDay = d.getFullYear() === n.getFullYear() && d.getMonth() === n.getMonth() && d.getDate() === n.getDate()
  return sameDay ? time : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time}`
}

export interface RuleChange {
  id: string
  title: string
  /** "18.2 / 30，14:35 刷新". */
  before: string
  /** "0 / 30，下次刷新 19:35（5 小时后）". */
  after: string
  /** The window has no usage (resetting it changes nothing now). */
  idle: boolean
}

/**
 * Before → after of one rule a card resets at `now`: usage drops to 0; a session
 * window restarts at `now` (refreshing one duration later), a rolling window loses
 * every bucket (it has no single refresh time).
 */
export function ruleChange(rule: RuleUsage, now: number, money: MoneyFormatter): RuleChange {
  const limit = formatMeterAmount(rule.meter, rule.limit, money)
  const used = `${formatMeterAmount(rule.meter, rule.used, money)} / ${limit}`
  let before = used
  let after = `${formatMeterAmount(rule.meter, '0', money)} / ${limit}`
  if (rule.window.kind === 'session') {
    before += rule.windowStart === null || !rule.resetsAt ? '，尚未开始' : `，${formatClock(rule.resetsAt, now)} 刷新`
    const mins = durationMinutes(rule.window.duration)
    if (mins !== null)
      after += `，下次刷新 ${formatClock(now + mins * 60_000, now)}（${formatDuration(rule.window.duration)}后）`
  }
  else {
    before += rule.resetsAt ? `，${formatClock(rule.resetsAt, now)} 起逐步释放` : '，窗口内暂无用量'
    after += '，窗口清空'
  }
  return { id: rule.id, title: ruleTitle(rule), before, after, idle: !(Number(rule.used) > 0) }
}

// ---------------------------------------------------------------------------
// Admin: issuing
// ---------------------------------------------------------------------------

export type CardTargetType = ResetCardTarget['type']

export const CARD_TARGET_LABELS: Record<CardTargetType, string> = {
  users: '指定用户',
  group: '用户组',
  plan: '套餐有效订阅用户',
  all: '全部用户',
}

export interface IssueForm {
  kind: ResetCardKind
  quantity: number | string
  targetType: CardTargetType
  userIds: string[]
  groupId: string
  planId: string
  /** `datetime-local` value ('' = never expires). */
  expiresAt: string
  /** Plan restriction ([] = every plan). */
  planIds: string[]
  note: string
}

export function emptyIssueForm(): IssueForm {
  return { kind: '5h', quantity: 1, targetType: 'users', userIds: [], groupId: '', planId: '', expiresAt: '', planIds: [], note: '' }
}

function quantityOk(v: number | string): boolean {
  const n = Number(v)
  return v !== '' && Number.isInteger(n) && n >= 1 && n <= CARD_QUANTITY_MAX
}

/** The target, or null while it is incomplete. */
export function buildCardTarget(f: Pick<IssueForm, 'targetType' | 'userIds' | 'groupId' | 'planId'>): ResetCardTarget | null {
  switch (f.targetType) {
    case 'users': {
      const ids = [...new Set(f.userIds)]
      return ids.length ? { type: 'users', userIds: ids } : null
    }
    case 'group':
      return f.groupId ? { type: 'group', groupId: f.groupId } : null
    case 'plan':
      return f.planId ? { type: 'plan', planId: f.planId } : null
    case 'all':
      return { type: 'all' }
  }
}

/** Request body, or null while the form cannot be previewed (incomplete target, bad quantity). */
export function buildIssueInput(f: IssueForm): ResetCardIssueInput | null {
  const target = buildCardTarget(f)
  if (!target || !quantityOk(f.quantity))
    return null
  return {
    kind: f.kind,
    quantity: Number(f.quantity),
    expiresAt: fromLocalInput(f.expiresAt),
    planIds: [...new Set(f.planIds)],
    note: f.note.trim(),
    target,
  }
}

/** Field errors of the issue form (keys match the server's `details`). */
export function issueFormErrors(f: IssueForm, now: number = Date.now()): Record<string, string> {
  const e: Record<string, string> = {}
  if (!quantityOk(f.quantity))
    e.quantity = `每人 1–${CARD_QUANTITY_MAX} 张`
  if (f.targetType === 'users' && f.userIds.length === 0)
    e.target = '请选择至少一位用户'
  else if (f.targetType === 'users' && f.userIds.length > CARD_USERS_MAX)
    e.target = `最多 ${CARD_USERS_MAX} 位用户`
  else if (f.targetType === 'group' && !f.groupId)
    e.target = '请选择用户组'
  else if (f.targetType === 'plan' && !f.planId)
    e.target = '请选择套餐'
  const ex = fromLocalInput(f.expiresAt)
  if (ex && new Date(ex).getTime() <= now)
    e.expiresAt = '必须晚于当前时间'
  if ([...f.note.trim()].length > CARD_NOTE_MAX)
    e.note = `最多 ${CARD_NOTE_MAX} 个字符`
  return e
}

/** Key of the parts of the body that change the recipients (dry-run cache). */
export function issueKey(b: ResetCardIssueInput): string {
  return JSON.stringify({ ...b, note: '' })
}

/** "将发放给 12 位用户（共 24 张）" / "没有符合条件的用户". */
export function recipientsText(recipients: number, cards?: number): string {
  if (recipients <= 0)
    return '没有符合条件的用户'
  return cards !== undefined && cards !== recipients ? `将发放给 ${recipients} 位用户（共 ${cards} 张）` : `将发放给 ${recipients} 位用户`
}

/** Short description of a batch's recipients. */
export function targetSummary(t: ResetCardTarget): string {
  switch (t.type) {
    case 'users':
      return `指定 ${t.userIds.length} 位用户`
    case 'group':
      return `用户组「${t.groupName || t.groupId.slice(0, 8)}」`
    case 'plan':
      return `「${t.planName || t.planId.slice(0, 8)}」的有效订阅用户`
    case 'all':
      return '全部用户'
  }
}
