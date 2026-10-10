// Notifications (phase6-api.md §2–§4): event catalog, categories, eligibility,
// preference form state / dirty tracking / PUT body, list filters and small helpers.
import type {
  AppNotification,
  DigestMode,
  EventChannels,
  NotificationEventType,
  NotificationPreferences,
  NotificationSeverity,
  WebhookFormat,
} from './types'
import { isApiError } from './api'
import { fromNano, isValidAmount, toNano } from './money'
import { safeRedirect } from './paths'

export type NotificationCategory = 'account' | 'wallet' | 'plan' | 'limit' | 'model' | 'key' | 'channel' | 'share' | 'plugin'

export interface EventMeta {
  type: NotificationEventType
  category: NotificationCategory
  label: string
  /** Trigger description from the contract (§2). */
  description: string
  /** Contract defaults (email / in-app); webhook defaults to off (not specified by the contract). */
  defaults: EventChannels
  /** Alert-class event: never merged into the daily digest (§3 `digest`). */
  alert: boolean
  /**
   * Channels that are always on and cannot be switched off (phase7 §2.3:
   * `account.status_changed` is always delivered in-app).
   */
  locked?: Partial<Record<keyof EventChannels, true>>
}

const d = (email: boolean, inApp: boolean): EventChannels => ({ email, webhook: false, inApp })

/** Every event of §2, in display order. */
export const EVENT_CATALOG: EventMeta[] = [
  { type: 'account.status_changed', category: 'account', label: '账号状态变更', description: '管理员停用 / 启用你的账号，或强制下线全部会话（含原因与到期时间）。站内通知不可关闭。', defaults: d(true, true), alert: true, locked: { inApp: true } },
  { type: 'account.group_changed', category: 'account', label: '用户分组变更', description: '管理员把你移到了其他用户组（价格倍率与用量限额随之变化）。', defaults: d(false, true), alert: false },
  { type: 'wallet.balance_low', category: 'wallet', label: '钱包余额不足', description: '可用余额从阈值以上降到阈值以下时通知；恢复到阈值以上后才会再次触发。', defaults: d(true, true), alert: true },
  { type: 'wallet.credited', category: 'wallet', label: '钱包入账', description: '兑换码充值、管理员调整、新用户赠送。', defaults: d(false, true), alert: false },
  { type: 'subscription.expiring', category: 'plan', label: '订阅即将到期', description: '订阅到期前 3 天（每份订阅一次）。', defaults: d(true, true), alert: false },
  { type: 'subscription.expired', category: 'plan', label: '订阅已结束', description: '订阅到期或被取消。', defaults: d(true, true), alert: false },
  { type: 'subscription.quota_reset', category: 'plan', label: '套餐额度已重置', description: '管理员重置了你订阅的套餐额度（含备注）。', defaults: d(true, true), alert: false },
  { type: 'subscription.extended', category: 'plan', label: '订阅已延期', description: '管理员延长了你订阅的有效期（含备注）。', defaults: d(true, true), alert: false },
  { type: 'reset_card.issued', category: 'plan', label: '获得额度重置卡', description: '管理员向你发放了 5 小时 / 周 / 双重置卡（含数量、有效期与备注），可在「钱包与订阅」中使用。', defaults: d(true, true), alert: false },
  { type: 'quota.near_limit', category: 'plan', label: '套餐额度即将用完', description: '某条配额规则在当前窗口的用量达到 80%（每个窗口一次）。', defaults: d(false, true), alert: false },
  { type: 'quota.exhausted', category: 'plan', label: '套餐额度已用完', description: '某条配额规则在当前窗口用完（每个窗口一次）。', defaults: d(true, true), alert: true },
  { type: 'limit.spend_near', category: 'limit', label: '消费限额即将用完', description: '用户组的每日 / 每月消费限额或 API Key 的消费上限用到 80%（每个窗口一次）。', defaults: d(false, true), alert: false },
  { type: 'limit.spend_reached', category: 'limit', label: '消费限额已用完', description: '达到消费限额，平台渠道的请求将被拒绝（429 spend_limit_exceeded），直到窗口重置。', defaults: d(true, true), alert: true },
  { type: 'model.price_changed', category: 'model', label: '模型价格变化', description: '近 30 天调用过的平台模型售价变化（含未来生效的价格，会提前通知生效时间）。', defaults: d(true, true), alert: false },
  { type: 'model.removed', category: 'model', label: '模型不再可用', description: '近 30 天调用过的模型不再可用（平台下线，或共享被收回）。', defaults: d(true, true), alert: false },
  { type: 'model.added', category: 'model', label: '新模型上架', description: '平台模型广场新增模型。', defaults: d(false, false), alert: false },
  { type: 'key.expiring', category: 'key', label: 'API Key 即将到期', description: 'API Key 到期前 7 天。', defaults: d(true, true), alert: false },
  { type: 'channel.unhealthy', category: 'channel', label: '渠道异常', description: '熔断打开，或健康探测连续失败。', defaults: d(true, true), alert: true },
  { type: 'channel.recovered', category: 'channel', label: '渠道恢复', description: '渠道从异常中恢复。', defaults: d(false, true), alert: true },
  { type: 'channel.auth_failed', category: 'channel', label: '渠道凭据失效', description: '上游返回 401/403（凭据失效）；每个渠道 6 小时内最多通知一次。', defaults: d(true, true), alert: true },
  { type: 'upstream.balance_low', category: 'channel', label: '上游余额不足', description: '渠道插件 balance 能力返回的余额低于该渠道设置的告警阈值。', defaults: d(true, true), alert: true },
  { type: 'channel.share_invited', category: 'share', label: '渠道共享邀请', description: '有用户邀请你使用他的渠道（需要你接受后才会生效；接受前请确认信任对方）。', defaults: d(false, true), alert: false },
  { type: 'plugin.pending_approval', category: 'plugin', label: '插件待审批', description: '有插件版本等待审批。', defaults: d(true, true), alert: false },
]

const EVENT_BY_TYPE = new Map<string, EventMeta>(EVENT_CATALOG.map(e => [e.type, e]))

export function eventMeta(type: string): EventMeta | null {
  return EVENT_BY_TYPE.get(type) ?? null
}

export const CATEGORIES: NotificationCategory[] = ['account', 'wallet', 'plan', 'limit', 'model', 'key', 'channel', 'share', 'plugin']

export const CATEGORY_LABELS: Record<NotificationCategory, string> = {
  account: '账户',
  wallet: '钱包',
  plan: '套餐',
  limit: '用量限额',
  model: '模型',
  key: 'API Key',
  channel: '渠道',
  share: '渠道共享',
  plugin: '插件',
}

/** Category of an event type (unknown types: by prefix, else null). */
export function categoryOf(type: string): NotificationCategory | null {
  const meta = eventMeta(type)
  if (meta)
    return meta.category
  const prefix = type.split('.')[0]
  switch (prefix) {
    case 'account': return 'account'
    case 'wallet': return 'wallet'
    case 'subscription':
    case 'quota':
    case 'reset_card': return 'plan'
    case 'limit': return 'limit'
    case 'model': return 'model'
    case 'key': return 'key'
    case 'channel':
    case 'upstream': return 'channel'
    case 'plugin': return 'plugin'
    default: return null
  }
}

export function eventLabel(type: string): string {
  return eventMeta(type)?.label ?? type
}

/** Value of the `type` query parameter for a category filter: every type of it, comma-separated. */
export function typesParam(category: NotificationCategory | '' | null | undefined): string | undefined {
  if (!category)
    return undefined
  return EVENT_CATALOG.filter(e => e.category === category).map(e => e.type).join(',')
}

export function isCategory(v: unknown): v is NotificationCategory {
  return typeof v === 'string' && (CATEGORIES as string[]).includes(v)
}

// ---------------------------------------------------------------------------
// Eligibility (who may receive which events, §2 "接收者")
// ---------------------------------------------------------------------------

export interface EligibilityContext {
  /** `/api/me` permissions. */
  permissions: readonly string[]
  /** The user owns at least one channel (or can manage channels). */
  managesChannels: boolean
}

export function isCategoryEligible(category: NotificationCategory, ctx: EligibilityContext): boolean {
  const has = (p: string) => ctx.permissions.includes(p)
  switch (category) {
    case 'wallet':
    case 'plan': return has('billing.own')
    // Same as the server (notify catBilling: wallet.*, subscription.*, quota.*, limit.*).
    case 'limit': return has('billing.own')
    case 'key': return has('keys.own')
    // phase5-api.md §5.5: share invitations reach every user.
    case 'account':
    case 'model':
    case 'share': return true
    case 'channel': return ctx.managesChannels || has('channels.manage')
    case 'plugin': return has('plugins.trust')
  }
}

export function eligibleEvents(ctx: EligibilityContext): EventMeta[] {
  return EVENT_CATALOG.filter(e => isCategoryEligible(e.category, ctx))
}

// ---------------------------------------------------------------------------
// Preferences form
// ---------------------------------------------------------------------------

export const WEBHOOK_FORMATS: WebhookFormat[] = ['json', 'feishu', 'dingtalk', 'wecom', 'slack']

export const WEBHOOK_FORMAT_LABELS: Record<WebhookFormat, string> = {
  json: '通用 JSON',
  feishu: '飞书',
  dingtalk: '钉钉',
  wecom: '企业微信',
  slack: 'Slack',
}

export const WEBHOOK_FORMAT_HINTS: Record<WebhookFormat, string> = {
  json: '请求体为 {id, type, title, body, url, data, createdAt}；设置密钥后带 X-OmniGate-Signature: sha256=<HMAC> 签名头。',
  feishu: '飞书群机器人的 Webhook 地址（open.feishu.cn/open-apis/bot/v2/hook/…），消息转换为飞书卡片格式。',
  dingtalk: '钉钉群机器人的 Webhook 地址（oapi.dingtalk.com/robot/send?access_token=…），消息转换为钉钉 Markdown 格式。',
  wecom: '企业微信群机器人的 Webhook 地址（qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…）。',
  slack: 'Slack Incoming Webhook 地址（hooks.slack.com/services/…）。',
}

export const DIGEST_LABELS: Record<DigestMode, string> = {
  off: '逐条发送',
  daily: '每日摘要',
}

export const DEFAULT_TIMEZONE = 'Asia/Shanghai'
export const DEFAULT_WALLET_THRESHOLD = '1'

export interface PreferencesForm {
  emailEnabled: boolean
  /** Verified custom address; null = account email. Changed only through verify / confirm or "使用账户邮箱". */
  emailAddress: string | null
  webhookEnabled: boolean
  webhookUrl: string
  webhookFormat: WebhookFormat
  events: Record<string, EventChannels>
  walletBalanceLow: string
  digest: DigestMode
  timezone: string
}

function isWebhookFormat(v: unknown): v is WebhookFormat {
  return typeof v === 'string' && (WEBHOOK_FORMATS as string[]).includes(v)
}

function channelsOf(v: EventChannels | undefined | null, fallback: EventChannels): EventChannels {
  if (!v)
    return { ...fallback }
  return { email: !!v.email, webhook: !!v.webhook, inApp: !!v.inApp }
}

/** Forces the channels an event locks on (e.g. in-app for `account.status_changed`). */
export function applyLocks(type: string, v: EventChannels): EventChannels {
  const locked = eventMeta(type)?.locked
  if (!locked)
    return v
  return { email: v.email || !!locked.email, webhook: v.webhook || !!locked.webhook, inApp: v.inApp || !!locked.inApp }
}

/** True when the event's `channel` switch is locked on. */
export function isLocked(type: string, channel: keyof EventChannels): boolean {
  return !!eventMeta(type)?.locked?.[channel]
}

/**
 * Form state from the server document. Missing events fall back to the contract defaults;
 * events the server sent but this UI does not know are kept as they are.
 */
export function formFromPreferences(p: NotificationPreferences): PreferencesForm {
  const events: Record<string, EventChannels> = {}
  for (const [k, v] of Object.entries(p.events ?? {})) {
    if (v)
      events[k] = channelsOf(v, d(false, false))
  }
  for (const e of EVENT_CATALOG)
    events[e.type] = applyLocks(e.type, channelsOf(p.events?.[e.type], e.defaults))
  return {
    emailEnabled: !!p.email?.enabled,
    emailAddress: p.email?.address || null,
    webhookEnabled: !!p.webhook?.enabled,
    webhookUrl: p.webhook?.url ?? '',
    webhookFormat: isWebhookFormat(p.webhook?.format) ? p.webhook.format : 'json',
    events,
    walletBalanceLow: p.thresholds?.walletBalanceLow ?? DEFAULT_WALLET_THRESHOLD,
    digest: p.digest === 'daily' ? 'daily' : 'off',
    timezone: p.timezone || DEFAULT_TIMEZONE,
  }
}

function canonicalAmount(v: string): string {
  const s = v.trim()
  return isValidAmount(s) ? fromNano(toNano(s)) : s
}

/**
 * `PUT /api/notifications/preferences` body: the whole document with `version`.
 * Only events the user is eligible for are sent (the server answers 422 when a switch
 * is turned on for an event the user may not receive); read-only flags are echoed.
 */
export function buildPreferencesBody(form: PreferencesForm, saved: NotificationPreferences, ctx: EligibilityContext) {
  const events: Record<string, EventChannels> = {}
  const eligible = new Set(eligibleEvents(ctx).map(e => e.type as string))
  for (const [k, v] of Object.entries(form.events)) {
    // Unknown (newer) types the server sent are round-tripped unchanged.
    if (eligible.has(k) || (!eventMeta(k) && saved.events?.[k]))
      events[k] = applyLocks(k, { email: v.email, webhook: v.webhook, inApp: v.inApp })
  }
  const url = form.webhookUrl.trim()
  return {
    email: { enabled: form.emailEnabled, address: form.emailAddress, verified: !!saved.email?.verified },
    webhook: { enabled: form.webhookEnabled, url: url || null, secretSet: !!saved.webhook?.secretSet, format: form.webhookFormat },
    events,
    thresholds: { walletBalanceLow: canonicalAmount(form.walletBalanceLow) },
    digest: form.digest,
    timezone: form.timezone.trim(),
    version: saved.version,
  }
}

/** Sections with unsaved edits (empty = clean). */
export function dirtyParts(form: PreferencesForm, saved: NotificationPreferences, ctx: EligibilityContext): string[] {
  const now = buildPreferencesBody(form, saved, ctx)
  const before = buildPreferencesBody(formFromPreferences(saved), saved, ctx)
  const out: string[] = []
  if (JSON.stringify(now.events) !== JSON.stringify(before.events))
    out.push('事件开关')
  if (now.email.enabled !== before.email.enabled || now.email.address !== before.email.address)
    out.push('邮件')
  if (JSON.stringify(now.webhook) !== JSON.stringify(before.webhook))
    out.push('Webhook')
  if (now.thresholds.walletBalanceLow !== before.thresholds.walletBalanceLow)
    out.push('余额阈值')
  if (now.digest !== before.digest || now.timezone !== before.timezone)
    out.push('摘要与时区')
  return out
}

export function isHttpUrlString(v: string): boolean {
  try {
    const u = new URL(v)
    return (u.protocol === 'https:' || u.protocol === 'http:') && !!u.hostname
  }
  catch {
    return false
  }
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function isValidEmail(v: string): boolean {
  const s = v.trim()
  return s.length <= 254 && EMAIL_RE.test(s)
}

export function isValidTimezone(tz: string): boolean {
  if (!tz.trim())
    return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz.trim() }).format(0)
    return true
  }
  catch {
    return false
  }
}

/** Client-side checks; keys follow the server's 422 detail paths. */
export function validatePreferences(form: PreferencesForm, ctx: EligibilityContext & { accountEmail: string | null }): Record<string, string> {
  const e: Record<string, string> = {}
  if (isCategoryEligible('wallet', ctx) && !isValidAmount(form.walletBalanceLow.trim()))
    e['thresholds.walletBalanceLow'] = '请输入不小于 0 的金额（最多 9 位小数）'
  const url = form.webhookUrl.trim()
  if (form.webhookEnabled && !url)
    e['webhook.url'] = '启用 Webhook 时必须填写地址'
  else if (url && !isHttpUrlString(url))
    e['webhook.url'] = '必须是 http(s) 开头的完整地址'
  else if (url.length > 2048)
    e['webhook.url'] = '地址过长（最多 2048 个字符）'
  if (!isValidTimezone(form.timezone))
    e.timezone = '无效的时区'
  if (form.emailEnabled && !form.emailAddress && !ctx.accountEmail)
    e['email.address'] = '账户没有邮箱，请先设置并验证通知邮箱'
  return e
}

/** Server 422 keys → form keys (`events.<type>.email` stays as is). */
export function normalizePrefsErrorKeys(details: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(details))
    out[k.replace(/^preferences\./, '')] = v
  return out
}

/** Contract defaults for every event (the "恢复默认" action of the matrix). */
export function defaultEvents(): Record<string, EventChannels> {
  return Object.fromEntries(EVENT_CATALOG.map(e => [e.type, { ...e.defaults }]))
}

// ---------------------------------------------------------------------------
// Time zones
// ---------------------------------------------------------------------------

export const COMMON_TIMEZONES = [
  'Asia/Shanghai',
  'Asia/Hong_Kong',
  'Asia/Taipei',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'UTC',
]

/** Common zones first, then every zone the browser knows (always containing `current`). */
export function timezoneOptions(current: string, all: readonly string[] = supportedTimezones()): { common: string[], others: string[] } {
  const common = [...COMMON_TIMEZONES]
  const set = new Set(common)
  const others = all.filter(z => !set.has(z))
  if (current && !set.has(current) && !others.includes(current))
    others.unshift(current)
  return { common, others }
}

function supportedTimezones(): string[] {
  try {
    return typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  }
  catch {
    return []
  }
}

export function browserTimezone(): string | null {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || null
  }
  catch {
    return null
  }
}

/** "UTC+08:00" for a zone at `at` (empty string when unknown). */
export function utcOffsetLabel(tz: string, at: Date = new Date()): string {
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'longOffset' }).formatToParts(at)
    const name = parts.find(p => p.type === 'timeZoneName')?.value ?? ''
    return name === 'GMT' ? 'UTC+00:00' : name.replace('GMT', 'UTC')
  }
  catch {
    return ''
  }
}

// ---------------------------------------------------------------------------
// List helpers
// ---------------------------------------------------------------------------

export const SEVERITY_LABELS: Record<NotificationSeverity, string> = {
  info: '提示',
  warning: '警告',
  critical: '严重',
}

export function severityOf(n: Pick<AppNotification, 'severity'>): NotificationSeverity {
  return n.severity === 'warning' || n.severity === 'critical' ? n.severity : 'info'
}

export type NotificationLink = { kind: 'internal', to: string } | { kind: 'external', href: string }

/** Same-origin path (router link) or https URL (new tab); anything else is dropped. */
export function notificationLink(link: string | null | undefined): NotificationLink | null {
  const v = (link ?? '').trim()
  if (!v)
    return null
  if (v.startsWith('/')) {
    const safe = safeRedirect(v, '')
    return safe ? { kind: 'internal', to: safe } : null
  }
  if (/^https:\/\//i.test(v) && isHttpUrlString(v))
    return { kind: 'external', href: v }
  return null
}

/** Sidebar badge text: "1"…"99", then "99+". */
export function unreadBadgeText(count: number): string {
  if (!Number.isFinite(count) || count <= 0)
    return ''
  return count > 99 ? '99+' : String(Math.floor(count))
}

/** Seconds left of a cooldown that ends at `until` (ms epoch). */
export function cooldownLeft(until: number | null, now: number = Date.now()): number {
  if (!until)
    return 0
  return Math.max(0, Math.ceil((until - now) / 1000))
}

/** Normalises the verification code input: digits only, at most 6. */
export function normalizeCode(v: string): string {
  return v.replace(/\D/g, '').slice(0, 6)
}

/**
 * True when an error of the email verify / confirm calls means "SMTP is not configured"
 * (the contract does not pin the code; we accept 409 / 422 / 503 whose code or message
 * mentions SMTP / the mail service).
 */
export function isSmtpUnavailableError(err: unknown): boolean {
  if (!isApiError(err) || ![409, 422, 503].includes(err.status))
    return false
  return /smtp|mail_(not_configured|disabled|unavailable)|email_(not_configured|disabled|unavailable)/i.test(err.code)
    || /SMTP|邮件服务|邮件发送未配置|未配置邮件/.test(err.message)
}
