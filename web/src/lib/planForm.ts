// Plan create / edit form state, client-side validation (mirrors
// server/internal/subscription/{rule.go,service.go}) and payload mapping.
import type { DurationUnit } from './quota'
import type { CalendarUnit, Plan, PlanInput, QuotaMeter, QuotaRule, QuotaWindow, WindowKind } from './types'
import { isCustomMeter, parseCustomMeter } from './customMeters'
import { amountSign, isValidAmount } from './money'
import { durationError, parseDuration, PERIOD_RANGE, WINDOW_DURATION_RANGE } from './quota'

export const RULE_ID_RE = /^[a-z0-9_-]{1,32}$/
export const MAX_RULES = 10
const MAX_NAME = 100
const MAX_DESC = 2000
const MAX_LABEL = 64
const MAX_MODELS = 200

export interface WeightRow {
  model: string
  weight: string
}

export interface RuleFormState {
  /** Stable key for v-for (not sent). */
  uid: number
  id: string
  /** True once the admin typed an id; stops auto-suggestion. */
  idEdited: boolean
  label: string
  /** Built-in meter or `custom:<pluginKey>.<meter>` (phase9 §3). */
  meter: QuotaMeter | string
  windowKind: WindowKind
  unit: CalendarUnit
  timezone: string
  /** rolling / session */
  durationN: number | string
  durationUnit: DurationUnit
  /** period */
  everyN: number | string
  everyUnit: DurationUnit
  limit: string
  models: string[]
  weights: WeightRow[]
}

export interface PlanFormState {
  name: string
  description: string
  /** '' = no list price. */
  listPrice: string
  durationN: number | string
  durationUnit: DurationUnit
  stackable: boolean
  models: string[]
  rules: RuleFormState[]
}

let uidSeq = 0
function nextUid(): number {
  uidSeq += 1
  return uidSeq
}

/** Timezone of the browser (IANA), falling back to UTC. */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  }
  catch {
    return 'UTC'
  }
}

/** True when `tz` is an IANA zone the browser knows (or UTC). */
export function isValidTimezone(tz: string): boolean {
  if (tz === 'UTC')
    return true
  if (!tz || tz === 'Local')
    return false
  try {
    void new Intl.DateTimeFormat('en-US', { timeZone: tz })
    return true
  }
  catch {
    return false
  }
}

export const COMMON_TIMEZONES = [
  'UTC',
  'Asia/Shanghai',
  'Asia/Hong_Kong',
  'Asia/Taipei',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
]

function splitDuration(s: string | undefined, fallback: { n: number, unit: DurationUnit }): { n: number, unit: DurationUnit } {
  return parseDuration(s) ?? fallback
}

function joinDuration(n: number | string, unit: DurationUnit): string {
  return `${String(n).trim()}${unit}`
}

export function windowFromRuleForm(r: RuleFormState): QuotaWindow {
  switch (r.windowKind) {
    case 'calendar':
      return { kind: 'calendar', unit: r.unit, timezone: r.timezone.trim() || 'UTC' }
    case 'rolling':
    case 'session':
      return { kind: r.windowKind, duration: joinDuration(r.durationN, r.durationUnit) }
    case 'period':
      return { kind: 'period', every: joinDuration(r.everyN, r.everyUnit) }
    default:
      return { kind: 'lifetime' }
  }
}

/** Rule id suggested from the window, unique among `taken`: "5h", "weekly", "every-30d", "total" … */
export function suggestRuleId(r: Pick<RuleFormState, 'windowKind' | 'unit' | 'durationN' | 'durationUnit' | 'everyN' | 'everyUnit'>, taken: Iterable<string> = []): string {
  let base: string
  switch (r.windowKind) {
    case 'calendar':
      base = r.unit === 'week' ? 'weekly' : r.unit === 'month' ? 'monthly' : 'daily'
      break
    case 'session':
      base = joinDuration(r.durationN, r.durationUnit)
      break
    case 'rolling':
      base = `rolling-${joinDuration(r.durationN, r.durationUnit)}`
      break
    case 'period':
      base = `every-${joinDuration(r.everyN, r.everyUnit)}`
      break
    default:
      base = 'total'
  }
  base = base.toLowerCase().replace(/[^a-z0-9_-]/g, '').slice(0, 28) || 'rule'
  const used = new Set(taken)
  if (!used.has(base))
    return base
  for (let i = 2; ; i++) {
    const id = `${base}-${i}`
    if (!used.has(id))
      return id
  }
}

export function emptyRuleForm(taken: Iterable<string> = [], timezone = browserTimezone()): RuleFormState {
  const r: RuleFormState = {
    uid: nextUid(),
    id: '',
    idEdited: false,
    label: '',
    meter: 'requests',
    windowKind: 'calendar',
    unit: 'day',
    timezone,
    durationN: 5,
    durationUnit: 'h',
    everyN: 30,
    everyUnit: 'd',
    limit: '',
    models: [],
    weights: [],
  }
  r.id = suggestRuleId(r, taken)
  return r
}

export function ruleFormFromRule(rule: QuotaRule): RuleFormState {
  const w = rule.window
  const dur = splitDuration(w.duration, { n: 5, unit: 'h' })
  const every = splitDuration(w.every, { n: 30, unit: 'd' })
  return {
    uid: nextUid(),
    id: rule.id,
    idEdited: true,
    label: rule.label,
    meter: rule.meter,
    windowKind: w.kind as WindowKind,
    unit: (w.unit as CalendarUnit | undefined) ?? 'day',
    timezone: w.timezone || 'UTC',
    durationN: dur.n,
    durationUnit: dur.unit,
    everyN: every.n,
    everyUnit: every.unit,
    limit: rule.limit,
    models: [...rule.models],
    weights: Object.entries(rule.modelWeights).map(([model, weight]) => ({ model, weight })),
  }
}

export function ruleFromForm(r: RuleFormState): QuotaRule {
  const weights: Record<string, string> = {}
  for (const row of r.weights) {
    const m = row.model.trim()
    if (m)
      weights[m] = row.weight.trim()
  }
  return {
    id: r.id.trim(),
    label: r.label.trim(),
    meter: r.meter,
    window: windowFromRuleForm(r),
    limit: r.limit.trim(),
    models: [...new Set(r.models.map(m => m.trim()).filter(Boolean))],
    modelWeights: weights,
  }
}

export function emptyPlanForm(): PlanFormState {
  return {
    name: '',
    description: '',
    listPrice: '',
    durationN: 30,
    durationUnit: 'd',
    stackable: false,
    models: [],
    rules: [emptyRuleForm()],
  }
}

export function formFromPlan(p: Plan): PlanFormState {
  const d = splitDuration(p.duration, { n: 30, unit: 'd' })
  return {
    name: p.name,
    description: p.description,
    listPrice: p.listPrice ?? '',
    durationN: d.n,
    durationUnit: d.unit,
    stackable: p.stackable,
    models: [...p.models],
    rules: p.rules.map(ruleFormFromRule),
  }
}

/** Full plan document for POST (create) or PATCH (edit, plus `version`). */
export function buildPlanPayload(f: PlanFormState): PlanInput {
  const price = f.listPrice.trim()
  return {
    name: f.name.trim(),
    description: f.description.trim(),
    listPrice: price === '' ? null : price,
    duration: joinDuration(f.durationN, f.durationUnit),
    models: [...new Set(f.models.map(m => m.trim()).filter(Boolean))],
    rules: f.rules.map(ruleFromForm),
    stackable: f.stackable,
  }
}

const INT_RE = /^\d+$/
const WEIGHT_RE = /^\d+(\.\d{1,9})?$/

function validateRule(r: RuleFormState, i: number, e: Record<string, string>, seen: Set<string>) {
  const p = `rules[${i}].`
  const id = r.id.trim()
  if (!RULE_ID_RE.test(id))
    e[`${p}id`] = '只能包含小写字母、数字、- 和 _，长度 1–32'
  else if (seen.has(id))
    e[`${p}id`] = '规则 id 在套餐内必须唯一'
  seen.add(id)
  if ([...r.label.trim()].length > MAX_LABEL)
    e[`${p}label`] = `不能超过 ${MAX_LABEL} 个字符`
  if (isCustomMeter(r.meter) && !parseCustomMeter(r.meter))
    e[`${p}meter`] = '插件计量的格式应为 custom:<插件 ID>.<计量名>'
  const w = windowFromRuleForm(r)
  if (w.kind === 'calendar' && !isValidTimezone(w.timezone ?? ''))
    e[`${p}window.timezone`] = '不是合法的 IANA 时区（如 Asia/Shanghai）'
  if (w.kind === 'rolling' || w.kind === 'session') {
    const msg = durationError(w.duration ?? '', WINDOW_DURATION_RANGE)
    if (msg)
      e[`${p}window.duration`] = msg
  }
  if (w.kind === 'period') {
    const msg = durationError(w.every ?? '', PERIOD_RANGE)
    if (msg)
      e[`${p}window.every`] = msg
  }
  const limit = r.limit.trim()
  if (!limit)
    e[`${p}limit`] = '必填'
  else if (r.meter === 'charge') {
    if (!isValidAmount(limit) || amountSign(limit) <= 0)
      e[`${p}limit`] = '请输入大于 0 的金额（最多 9 位小数）'
  }
  else if (isCustomMeter(r.meter)) {
    if (!isValidAmount(limit) || amountSign(limit) <= 0)
      e[`${p}limit`] = '请输入大于 0 的数值（最多 9 位小数）'
  }
  else if (!INT_RE.test(limit) || /^0+$/.test(limit))
    e[`${p}limit`] = '请求次数、token、图片张数与音频秒数的上限必须为正整数'
  else if (limit.replace(/^0+/, '').length > 29)
    e[`${p}limit`] = '数值过大'
  if (r.models.length > MAX_MODELS)
    e[`${p}models`] = `最多 ${MAX_MODELS} 个模型`
  const wseen = new Set<string>()
  for (const row of r.weights) {
    const m = row.model.trim()
    if (!m && !row.weight.trim())
      continue
    if (!m) {
      e[`${p}modelWeights`] = '模型名不能为空'
      continue
    }
    if (wseen.has(m)) {
      e[`${p}modelWeights`] = `模型 ${m} 重复`
      continue
    }
    wseen.add(m)
    const v = row.weight.trim()
    if (!WEIGHT_RE.test(v) || Number(v) > 1000)
      e[`${p}modelWeights.${m}`] = '必须是 0 到 1000 之间的十进制数'
  }
}

/** Client-side validation; keys match the server's 422 `details` (e.g. `rules[0].limit`). */
export function validatePlanForm(f: PlanFormState): Record<string, string> {
  const e: Record<string, string> = {}
  const name = f.name.trim()
  if (!name)
    e.name = '必填'
  else if ([...name].length > MAX_NAME)
    e.name = `不能超过 ${MAX_NAME} 个字符`
  if ([...f.description.trim()].length > MAX_DESC)
    e.description = `不能超过 ${MAX_DESC} 个字符`
  const price = f.listPrice.trim()
  if (price && !isValidAmount(price))
    e.listPrice = '请输入不小于 0 的金额（最多 9 位小数），或留空'
  const dmsg = durationError(joinDuration(f.durationN, f.durationUnit), PERIOD_RANGE)
  if (dmsg)
    e.duration = dmsg
  if (f.models.length > MAX_MODELS)
    e.models = `最多 ${MAX_MODELS} 个模型`
  if (f.rules.length < 1 || f.rules.length > MAX_RULES)
    e.rules = `必须有 1 到 ${MAX_RULES} 条规则`
  const seen = new Set<string>()
  f.rules.forEach((r, i) => validateRule(r, i, e, seen))
  return e
}

// ---------------------------------------------------------------------------
// Presets
// ---------------------------------------------------------------------------

export type PlanPresetKey = 'claude-pro' | 'request-pack' | 'monthly-credit'

export interface PlanPreset {
  key: PlanPresetKey
  label: string
  description: string
}

export const PLAN_PRESETS: PlanPreset[] = [
  { key: 'claude-pro', label: '类 Claude Pro（5 小时会话 + 每周上限）', description: '5 小时会话内 200 次请求，且每周最多 500 万 tokens。' },
  { key: 'request-pack', label: '按次包（总次数）', description: '订阅有效期内共 1000 次请求，用完即止。' },
  { key: 'monthly-credit', label: '月度额度（按售价折算金额/月）', description: '每 30 天按模型售价折算 20 的额度。' },
]

function rule(partial: Partial<RuleFormState>): RuleFormState {
  // Preset ids are deliberate: keep them when the window is edited later.
  return { ...emptyRuleForm([], partial.timezone ?? 'UTC'), ...partial, idEdited: true, uid: nextUid() }
}

/** Typical rule sets; they replace the current rules. */
export function presetRules(key: PlanPresetKey, timezone = browserTimezone()): RuleFormState[] {
  switch (key) {
    case 'claude-pro':
      return [
        rule({ id: '5h', label: '5 小时会话', meter: 'requests', windowKind: 'session', durationN: 5, durationUnit: 'h', limit: '200' }),
        rule({ id: 'weekly', label: '每周', meter: 'tokens.total', windowKind: 'calendar', unit: 'week', timezone, limit: '5000000' }),
      ]
    case 'request-pack':
      return [
        rule({ id: 'total', label: '总次数', meter: 'requests', windowKind: 'lifetime', limit: '1000' }),
      ]
    case 'monthly-credit':
      return [
        rule({ id: 'monthly', label: '每月额度', meter: 'charge', windowKind: 'period', everyN: 30, everyUnit: 'd', limit: '20' }),
      ]
  }
}

/** Splits a 422 detail key "rules[2].window.duration" → { index: 2, field: "window.duration" }. */
export function ruleErrorKey(key: string): { index: number, field: string } | null {
  const m = /^rules\[(\d+)\]\.(.+)$/.exec(key)
  return m ? { index: Number(m[1]), field: m[2]! } : null
}
