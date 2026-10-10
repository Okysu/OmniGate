// Display helpers for plans, quota rules and subscription usage (contract
// phase3-api.md §1–§2). Pure functions: money formatting is injected so the
// caller can pass `useCurrency().money` (3-decimal display amounts).
import type { KeyQuotaOverflow, QuotaMeter, QuotaOverflow, QuotaRule, QuotaWindow, RuleUsage, Subscription } from './types'
import { customMeterInfo, isCustomMeter } from './customMeters'
import { formatDateTime } from './format'

export type MoneyFormatter = (value: string) => string

export type DurationUnit = 'm' | 'h' | 'd'

const DURATION_RE = /^([1-9]\d{0,6})([mhd])$/
const UNIT_TEXT: Record<DurationUnit, string> = { m: '分钟', h: '小时', d: '天' }
const UNIT_MINUTES: Record<DurationUnit, number> = { m: 1, h: 60, d: 1440 }

/** "5h" → { n: 5, unit: 'h' }; null when not `<positive int><m|h|d>`. */
export function parseDuration(s: string | null | undefined): { n: number, unit: DurationUnit } | null {
  const m = DURATION_RE.exec((s ?? '').trim())
  if (!m)
    return null
  return { n: Number(m[1]), unit: m[2] as DurationUnit }
}

/** Duration in minutes, or null when invalid. */
export function durationMinutes(s: string | null | undefined): number | null {
  const d = parseDuration(s)
  return d ? d.n * UNIT_MINUTES[d.unit] : null
}

/** "5h" → "5 小时", "30d" → "30 天"; unparsable input is returned as is. */
export function formatDuration(s: string | null | undefined): string {
  const d = parseDuration(s)
  if (!d)
    return s ?? '—'
  return `${d.n} ${UNIT_TEXT[d.unit]}`
}

/** Bounds from server/internal/subscription/duration.go. */
export const WINDOW_DURATION_RANGE = { min: 5, max: 31 * 1440, text: '5m 到 31d（5 分钟到 31 天）' }
export const PERIOD_RANGE = { min: 60, max: 366 * 1440, text: '1h 到 366d（1 小时到 366 天）' }

/** Error message for a duration string checked against a range, or null when valid. */
export function durationError(s: string, range: { min: number, max: number, text: string }): string | null {
  const mins = durationMinutes(s)
  if (mins === null)
    return '请输入正整数'
  if (mins < range.min || mins > range.max)
    return `必须在 ${range.text} 之间`
  return null
}

// ---------------------------------------------------------------------------
// Numbers
// ---------------------------------------------------------------------------

const smallFormatter = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 })

function trimFixed(n: number): string {
  return n.toFixed(2).replace(/\.?0+$/, '')
}

/**
 * Counts and token amounts with Chinese large-number units:
 * 1234 → "1,234", 5000000 → "500 万", 250000000 → "2.5 亿". Accepts decimal strings
 * (weighted usage may be fractional). Never use for money.
 */
export function formatQuotaNumber(value: string | number | null | undefined): string {
  if (value === null || value === undefined || value === '')
    return '—'
  const n = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(n))
    return String(value)
  const abs = Math.abs(n)
  if (abs >= 1e8)
    return `${trimFixed(n / 1e8)} 亿`
  if (abs >= 1e4)
    return `${trimFixed(n / 1e4)} 万`
  return smallFormatter.format(n)
}

export const METER_LABELS: Record<QuotaMeter, string> = {
  'requests': '请求次数',
  'tokens.input': '输入 Token',
  'tokens.output': '输出 Token',
  'tokens.total': '总 Token',
  'charge': '按售价折算的金额',
  'images': '图片张数',
  'audio_seconds': '音频秒数',
}

export const METER_HINTS: Record<QuotaMeter, string> = {
  'requests': '每个成功的请求计 1 次。',
  'tokens.input': '输入 token（含缓存读写）。',
  'tokens.output': '输出 token。',
  'tokens.total': '输入（含缓存读写）与输出 token 之和。',
  'charge': '按模型售价计算的金额（即使不扣钱包也照常计算），适合“按额度”的套餐。',
  'images': '按图片接口输出的图片张数计，可配合模型倍率。',
  'audio_seconds': '按语音转写 / 翻译的输入音频时长计，每次请求向上取整到整秒（3600 = 1 小时）；语音合成不计入。可配合模型倍率。',
}

export const METERS: QuotaMeter[] = ['requests', 'tokens.input', 'tokens.output', 'tokens.total', 'images', 'audio_seconds', 'charge']

/** Hint under the meter select for `custom:` meters (phase9 §3). */
export const CUSTOM_METER_HINT = '由计费插件计算：数值与单位由插件定义；插件超时、出错或不可用时该次计量按 0 记（并写告警日志）。'

/** Rule meter fields used to resolve plugin meter labels (server snapshot first). */
type MeterRule = Pick<QuotaRule, 'meterLabel' | 'meterUnit'> | null | undefined

/** Display name of a meter: "请求次数", or the plugin meter's label. */
export function meterLabel(meter: string, rule?: MeterRule): string {
  const custom = customMeterInfo(meter, rule)
  if (custom)
    return custom.label
  return (METER_LABELS as Record<string, string>)[meter] ?? meter
}

/** Hint for a meter (built-in description, or the plugin-meter warning). */
export function meterHint(meter: string): string | undefined {
  if (isCustomMeter(meter))
    return CUSTOM_METER_HINT
  return (METER_HINTS as Record<string, string>)[meter]
}

/** True for meters whose limit must be a positive integer (plugin meters may be fractional). */
export function isIntegerMeter(meter: string): boolean {
  return meter !== 'charge' && !isCustomMeter(meter)
}

/** Short unit appended to counts ("次" / "张" / "秒" / "tokens" / plugin unit); "" for charge (money carries its symbol). */
export function meterUnit(meter: string, rule?: MeterRule): string {
  if (meter === 'requests')
    return '次'
  if (meter === 'images')
    return '张'
  if (meter === 'audio_seconds')
    return '秒'
  if (meter.startsWith('tokens.'))
    return 'tokens'
  return customMeterInfo(meter, rule)?.unit ?? ''
}

/** "（60 分钟）" for whole minutes, "（约 1.5 分钟）" otherwise; '' below one minute or when not numeric. */
export function audioMinutesNote(seconds: string | number): string {
  const n = typeof seconds === 'number' ? seconds : Number(seconds)
  if (!Number.isFinite(n) || n < 60)
    return ''
  const minutes = n / 60
  if (Number.isInteger(minutes))
    return `（${formatQuotaNumber(minutes)} 分钟）`
  return `（约 ${formatQuotaNumber(Math.round(minutes * 10) / 10)} 分钟）`
}

/** One meter value with its unit: "200 次请求", "500 万 tokens", "3,600 秒音频（60 分钟）", "$20.00". */
export function formatMeterValue(meter: string, value: string, money: MoneyFormatter, rule?: MeterRule): string {
  const custom = customMeterInfo(meter, rule)
  if (custom)
    return `${custom.label} ${formatQuotaNumber(value)}${custom.unit ? ` ${custom.unit}` : ''}`
  switch (meter) {
    case 'requests':
      return `${formatQuotaNumber(value)} 次请求`
    case 'tokens.input':
      return `${formatQuotaNumber(value)} 输入 tokens`
    case 'tokens.output':
      return `${formatQuotaNumber(value)} 输出 tokens`
    case 'tokens.total':
      return `${formatQuotaNumber(value)} tokens`
    case 'images':
      return `${formatQuotaNumber(value)} 张图片`
    case 'audio_seconds':
      return `${formatQuotaNumber(value)} 秒音频${audioMinutesNote(value)}`
    case 'charge':
      return `${money(value)} 额度`
    default:
      return value
  }
}

/** Compact amount without the meter noun (for "used / limit"). */
export function formatMeterAmount(meter: string, value: string, money: MoneyFormatter): string {
  return meter === 'charge' ? money(value) : formatQuotaNumber(value)
}

// ---------------------------------------------------------------------------
// Windows
// ---------------------------------------------------------------------------

export const WINDOW_KIND_LABELS: Record<string, string> = {
  calendar: '日历周期（日 / 周 / 月）',
  rolling: '滚动窗口',
  session: '会话窗口',
  period: '订阅周期',
  lifetime: '订阅期内总量',
}

export const WINDOW_KIND_HINTS: Record<string, string> = {
  calendar: '按自然日 / 周（周一开始）/ 月在指定时区重置。',
  rolling: '统计最近一段时间内的用量（按 5 分钟分桶），旧用量逐步滚出窗口。',
  session: '窗口结束后的首次请求开启新窗口，例如“5 小时会话”。',
  period: '从订阅开始时间起，每隔固定时长重置一次。',
  lifetime: '整个订阅有效期内累计，不会重置（适合按次包）。',
}

export const CALENDAR_UNIT_LABELS: Record<string, string> = { day: '日', week: '周', month: '月' }

/** Short window name used in rule summaries: "5 小时会话", "每周", "每 30 天", "总量". */
export function windowShortLabel(w: QuotaWindow): string {
  switch (w.kind) {
    case 'calendar':
      return w.unit === 'week' ? '每周' : w.unit === 'month' ? '每月' : '每日'
    case 'rolling':
      return `滚动 ${formatDuration(w.duration)}`
    case 'session':
      return `${formatDuration(w.duration)}会话`
    case 'period':
      return `每 ${formatDuration(w.every)}`
    case 'lifetime':
      return '总量'
    default:
      return w.kind
  }
}

/**
 * Full window description: "每周（周一 00:00 Asia/Shanghai 重置）",
 * "5 小时会话窗口（首次请求开始计时）" …
 */
export function windowDescription(w: QuotaWindow): string {
  const tz = w.timezone || 'UTC'
  switch (w.kind) {
    case 'calendar':
      if (w.unit === 'week')
        return `每周（周一 00:00 ${tz} 重置）`
      if (w.unit === 'month')
        return `每月（1 日 00:00 ${tz} 重置）`
      return `每日（00:00 ${tz} 重置）`
    case 'rolling':
      return `滚动 ${formatDuration(w.duration)}窗口（统计最近 ${formatDuration(w.duration)}）`
    case 'session':
      return `${formatDuration(w.duration)}会话窗口（首次请求开始计时）`
    case 'period':
      return `每 ${formatDuration(w.every)}周期（从订阅开始时间起算）`
    case 'lifetime':
      return '订阅期内总量（不重置）'
    default:
      return w.kind
  }
}

// ---------------------------------------------------------------------------
// Rules & plans
// ---------------------------------------------------------------------------

/** Rule display name: its label, else the window's short label. */
export function ruleTitle(rule: Pick<QuotaRule, 'label' | 'window'>): string {
  return rule.label.trim() || windowShortLabel(rule.window)
}

/** "5 小时会话：200 次请求". */
export function ruleSummary(rule: QuotaRule, money: MoneyFormatter): string {
  return `${ruleTitle(rule)}：${formatMeterValue(rule.meter, rule.limit, money, rule)}`
}

/** "5 小时会话：200 次请求 · 每周：500 万 tokens". */
export function rulesSummary(rules: QuotaRule[], money: MoneyFormatter): string {
  return rules.length ? rules.map(r => ruleSummary(r, money)).join(' · ') : '—'
}

// ---------------------------------------------------------------------------
// Quota overflow (account preference, overridable per API key)
// ---------------------------------------------------------------------------

export function isQuotaOverflow(v: unknown): v is QuotaOverflow {
  return v === 'block' || v === 'wallet'
}

/** Options of the account setting 「套餐额度用完后」. */
export const QUOTA_OVERFLOW_OPTIONS: { value: QuotaOverflow, title: string, description: string }[] = [
  { value: 'block', title: '阻断请求（默认，返回 429）', description: '额度用完后拒绝套餐覆盖模型的请求（HTTP 429），直到额度窗口重置；不会动用钱包余额。' },
  { value: 'wallet', title: '使用钱包余额按量计费', description: '额度用完后继续服务，超出部分与未订阅时一样按模型价格从钱包余额扣费。' },
]

/** Short form for cards and summaries: "阻断请求" / "改用钱包按量计费". */
export const QUOTA_OVERFLOW_SHORT: Record<QuotaOverflow, string> = {
  block: '阻断请求',
  wallet: '改用钱包按量计费',
}

/** API key policy select 「额度用完后」 ('' = follow the account). */
export const KEY_QUOTA_OVERFLOW_LABELS: Record<KeyQuotaOverflow, string> = {
  '': '跟随账户设置',
  'block': '阻断',
  'wallet': '使用钱包',
}

/** Behaviour that applies to a key: its override, else the account setting (null = unknown). */
export function effectiveQuotaOverflow(key: string | null | undefined, account: QuotaOverflow | null | undefined): QuotaOverflow | null {
  if (isQuotaOverflow(key))
    return key
  return isQuotaOverflow(account) ? account : null
}

/** Message under a rule that is out of quota; `null` overflow = not known (e.g. admin view). */
export function exceededNote(overflow: QuotaOverflow | null | undefined): string {
  if (overflow === 'wallet')
    return '已达上限：超出部分改用钱包余额按量计费。'
  if (overflow === 'block')
    return '已达上限：覆盖的请求将被拒绝（429），直到窗口重置。'
  return '已达上限，直到窗口重置。'
}

/** "全部模型" / "a、b" / "a、b 等 5 个模型". */
export function modelsSummary(models: string[], max = 2): string {
  if (models.length === 0)
    return '全部模型'
  if (models.length <= max)
    return models.join('、')
  return `${models.slice(0, max).join('、')} 等 ${models.length} 个模型`
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

/** used / limit as a number (0 when the limit is not positive). */
export function usageRatio(used: string, limit: string): number {
  const u = Number(used)
  const l = Number(limit)
  if (!Number.isFinite(u) || !Number.isFinite(l) || l <= 0)
    return 0
  return Math.max(0, u / l)
}

export type UsageLevel = 'ok' | 'warn' | 'exceeded'

export function usageLevel(rule: Pick<RuleUsage, 'used' | 'limit' | 'exceeded'>): UsageLevel {
  if (rule.exceeded)
    return 'exceeded'
  return usageRatio(rule.used, rule.limit) >= 0.8 ? 'warn' : 'ok'
}

/** "3 小时 20 分钟后", "2 天 4 小时后"; far dates fall back to an absolute time. */
export function formatUntil(iso: string, now: number = Date.now()): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t))
    return iso
  const diff = t - now
  if (diff <= 0)
    return '即将'
  const mins = Math.floor(diff / 60_000)
  if (mins < 1)
    return '不到 1 分钟后'
  if (mins < 60)
    return `${mins} 分钟后`
  const hours = Math.floor(mins / 60)
  if (hours < 24)
    return mins % 60 ? `${hours} 小时 ${mins % 60} 分钟后` : `${hours} 小时后`
  const days = Math.floor(hours / 24)
  if (days < 30)
    return hours % 24 ? `${days} 天 ${hours % 24} 小时后` : `${days} 天后`
  return formatDateTime(iso)
}

/** Time left until `iso`: "剩余 29 天", "剩余 5 小时", "剩余 12 分钟", "已到期". */
export function formatRemaining(iso: string, now: number = Date.now()): string {
  const diff = new Date(iso).getTime() - now
  if (Number.isNaN(diff))
    return iso
  if (diff <= 0)
    return '已到期'
  const mins = Math.floor(diff / 60_000)
  if (mins < 60)
    return `剩余 ${Math.max(1, mins)} 分钟`
  const hours = Math.floor(mins / 60)
  if (hours < 24)
    return `剩余 ${hours} 小时`
  return `剩余 ${Math.floor(hours / 24)} 天`
}

/**
 * When the rule's window resets, relative to now:
 * - `text`: "3 小时 20 分钟后重置", "尚未开始", "不重置" …
 * - `absolute`: formatted reset time for a tooltip (null when there is none).
 */
export function resetInfo(rule: Pick<RuleUsage, 'window' | 'windowStart' | 'resetsAt'>, now: number = Date.now()): { text: string, absolute: string | null } {
  if (rule.window.kind === 'lifetime')
    return { text: '不重置', absolute: null }
  if (rule.window.kind === 'session' && rule.windowStart === null)
    return { text: '尚未开始', absolute: null }
  if (!rule.resetsAt)
    return { text: rule.window.kind === 'rolling' ? '窗口内暂无用量' : '—', absolute: null }
  const until = formatUntil(rule.resetsAt, now)
  const text = rule.window.kind === 'rolling' ? `${until}释放最早的用量` : `${until}重置`
  return { text, absolute: formatDateTime(rule.resetsAt) }
}

/** Active subscriptions first (soonest to end first), then the rest newest first. */
export function sortSubscriptions(subs: Subscription[]): Subscription[] {
  return [...subs].sort((a, b) => {
    const aa = a.status === 'active' ? 0 : 1
    const ba = b.status === 'active' ? 0 : 1
    if (aa !== ba)
      return aa - ba
    if (aa === 0)
      return new Date(a.endsAt).getTime() - new Date(b.endsAt).getTime()
    return new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()
  })
}

/** The rule with the highest used / limit ratio across active subscriptions. */
export function mostUsedRule(subs: Subscription[]): { subscription: Subscription, rule: RuleUsage, ratio: number } | null {
  let best: { subscription: Subscription, rule: RuleUsage, ratio: number } | null = null
  for (const s of subs) {
    if (s.status !== 'active')
      continue
    for (const r of s.rules) {
      const ratio = r.exceeded ? Math.max(1, usageRatio(r.used, r.limit)) : usageRatio(r.used, r.limit)
      if (!best || ratio > best.ratio)
        best = { subscription: s, rule: r, ratio }
    }
  }
  return best
}

export const SUBSCRIPTION_STATUS_LABELS: Record<string, string> = {
  active: '有效',
  expired: '已过期',
  cancelled: '已取消',
}

export const SUBSCRIPTION_SOURCE_LABELS: Record<string, string> = {
  admin: '管理员开通',
  redeem: '兑换码',
  purchase: '余额购买',
}

export const PLAN_STATUS_LABELS: Record<string, string> = {
  active: '上架中',
  archived: '已下架',
}

/** Friendly messages for plan / subscription error codes. */
export const PLAN_ERROR_MESSAGES: Record<string, string> = {
  plan_archived: '套餐已下架，不能再开通。',
  subscription_not_active: '订阅已不是有效状态（可能已过期或已被取消）。',
  // phase15 §3.2: wallet purchases.
  plan_not_for_sale: '该套餐暂不支持余额购买，可通过兑换码或联系管理员开通。',
  not_an_upgrade: '无法升级：目标套餐不比当前套餐更贵，或你已持有目标套餐（请改为续费）。',
  insufficient_balance: '钱包余额不足，请先使用兑换码充值。',
  price_changed: '套餐价格已变化，请确认新价格后重试。',
}
