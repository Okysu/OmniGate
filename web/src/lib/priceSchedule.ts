// Time-of-day price schedules (phase8-api.md §3): form rows ↔ request body, validation
// (including the overlap check the server answers with 422), the 24h timeline, period
// descriptions and the "current period" note of the model plaza.
//
// Semantics (see the contract questions in the README): a period that crosses midnight
// (start > end) belongs to the day it STARTS on — "周五 22:00–02:00" covers Friday 22:00
// to Saturday 02:00. `days: []` means every day. `end` is exclusive.
import type { PlazaPrice, PriceSchedulePeriod } from './types'
import { fromNano, isValidAmount, toNano } from './money'
import { DEFAULT_TIMEZONE, isValidTimezone } from './notifications'

export const DAY_MINUTES = 24 * 60
export const WEEK_MINUTES = 7 * DAY_MINUTES
/** Multiplier range of a period (inclusive). */
export const SCHEDULE_MULTIPLIER_MAX = '10'
/** Server limit (pricing maxSlots). */
export const MAX_SCHEDULE_ROWS = 48

/** Chip order: Sunday first, matching the contract's 0 = Sunday. */
export const WEEKDAYS = [0, 1, 2, 3, 4, 5, 6] as const
export const DAY_CHARS = ['日', '一', '二', '三', '四', '五', '六'] as const
export const DAY_NAMES = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'] as const

const TIME_RE = /^([01]\d|2[0-3]):([0-5]\d)$/

/** "08:30" → 510; invalid → null. */
export function parseTime(v: string): number | null {
  const m = TIME_RE.exec(v.trim())
  return m ? Number(m[1]) * 60 + Number(m[2]) : null
}

/** 510 → "08:30" (1440 → "24:00"). */
export function formatTime(min: number): string {
  const m = ((min % (DAY_MINUTES + 1)) + DAY_MINUTES + 1) % (DAY_MINUTES + 1)
  return `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`
}

// ---------------------------------------------------------------------------
// Form rows
// ---------------------------------------------------------------------------

export interface ScheduleRow {
  /** Explicit weekdays; all seven = every day (sent as `[]`). */
  days: number[]
  start: string
  end: string
  multiplier: string
}

export interface ScheduleForm {
  rows: ScheduleRow[]
  timezone: string
}

export function everyDay(): number[] {
  return [...WEEKDAYS]
}

export function emptyRow(): ScheduleRow {
  return { days: everyDay(), start: '00:00', end: '08:00', multiplier: '0.5' }
}

export function emptyScheduleForm(): ScheduleForm {
  return { rows: [], timezone: DEFAULT_TIMEZONE }
}

function normDays(days: readonly number[] | null | undefined): number[] {
  return [...new Set((days ?? []).filter(d => Number.isInteger(d) && d >= 0 && d <= 6))].sort((a, b) => a - b)
}

/** Form state from a price's `schedule` / `scheduleTimezone`. */
export function scheduleFormFrom(p: { schedule?: PriceSchedulePeriod[] | null, scheduleTimezone?: string } | null | undefined): ScheduleForm {
  return {
    rows: (p?.schedule ?? []).map((s) => {
      const days = normDays(s.days)
      // The server accepts end "24:00" (not crossing midnight); a time input cannot show it,
      // and "00:00" covers the same minutes (crossing into the next day's 00:00).
      return { days: days.length ? days : everyDay(), start: s.start, end: s.end === '24:00' ? '00:00' : s.end, multiplier: s.multiplier }
    }),
    timezone: p?.scheduleTimezone || DEFAULT_TIMEZONE,
  }
}

function canonicalMultiplier(v: string): string {
  const s = v.trim()
  return isValidAmount(s) ? fromNano(toNano(s)) : s
}

/** Request periods (`days` all seven → `[]`). */
export function buildSchedule(rows: readonly ScheduleRow[]): PriceSchedulePeriod[] {
  return rows.map((r) => {
    const days = normDays(r.days)
    return { days: days.length === 7 ? [] : days, start: r.start.trim(), end: r.end.trim(), multiplier: canonicalMultiplier(r.multiplier) }
  })
}

export function isEveryDay(days: readonly number[]): boolean {
  return days.length === 0 || normDays(days).length === 7
}

/** Toggles a weekday chip. */
export function toggleDay(days: readonly number[], d: number): number[] {
  const set = new Set(days)
  if (set.has(d))
    set.delete(d)
  else
    set.add(d)
  return normDays([...set])
}

// ---------------------------------------------------------------------------
// Week intervals & overlap
// ---------------------------------------------------------------------------

export interface WeekInterval {
  /** Minutes since Sunday 00:00, [from, to). */
  from: number
  to: number
}

/**
 * Week-minute intervals covered by a period (empty for invalid times). Periods that cross
 * midnight spill into the next day; Saturday night wraps around to Sunday morning.
 */
export function weekIntervals(p: Pick<PriceSchedulePeriod, 'days' | 'start' | 'end'>): WeekInterval[] {
  const s = parseTime(p.start)
  const e = parseTime(p.end)
  if (s === null || e === null || s === e)
    return []
  const days = normDays(p.days)
  const out: WeekInterval[] = []
  for (const d of days.length ? days : WEEKDAYS) {
    const from = d * DAY_MINUTES + s
    const to = d * DAY_MINUTES + (s < e ? e : DAY_MINUTES + e)
    if (to <= WEEK_MINUTES) {
      out.push({ from, to })
    }
    else {
      out.push({ from, to: WEEK_MINUTES })
      out.push({ from: 0, to: to - WEEK_MINUTES })
    }
  }
  return out
}

function firstOverlap(a: WeekInterval[], b: WeekInterval[]): WeekInterval | null {
  for (const x of a) {
    for (const y of b) {
      const from = Math.max(x.from, y.from)
      const to = Math.min(x.to, y.to)
      if (from < to)
        return { from, to }
    }
  }
  return null
}

/** "周一 23:00–24:00". */
export function describeWeekInterval(iv: WeekInterval): string {
  const d = Math.floor(iv.from / DAY_MINUTES) % 7
  const from = iv.from - d * DAY_MINUTES
  const to = Math.min(iv.to - d * DAY_MINUTES, DAY_MINUTES)
  return `${DAY_NAMES[d]} ${formatTime(from)}–${formatTime(to)}`
}

export interface ScheduleOverlap {
  a: number
  b: number
  at: WeekInterval
}

/** Every pair of overlapping periods (indexes), with the first overlapping slot. */
export function findOverlaps(periods: readonly Pick<PriceSchedulePeriod, 'days' | 'start' | 'end'>[]): ScheduleOverlap[] {
  const ivs = periods.map(weekIntervals)
  const out: ScheduleOverlap[] = []
  for (let i = 0; i < ivs.length; i++) {
    for (let j = i + 1; j < ivs.length; j++) {
      const at = firstOverlap(ivs[i]!, ivs[j]!)
      if (at)
        out.push({ a: i, b: j, at })
    }
  }
  return out
}

function isValidScheduleMultiplier(v: string): boolean {
  const s = v.trim()
  return isValidAmount(s) && toNano(s) <= toNano(SCHEDULE_MULTIPLIER_MAX)
}

/**
 * Client-side checks mirroring the server (422). Keys follow the request paths:
 * `schedule.<i>.days|start|end|multiplier`, `schedule.<i>` (overlap), `scheduleTimezone`.
 */
export function validateSchedule(form: ScheduleForm): Record<string, string> {
  const e: Record<string, string> = {}
  if (form.rows.length > MAX_SCHEDULE_ROWS)
    e.schedule = `最多 ${MAX_SCHEDULE_ROWS} 个时段`
  form.rows.forEach((r, i) => {
    const k = `schedule.${i}`
    if (normDays(r.days).length === 0)
      e[`${k}.days`] = '至少选择一天'
    const s = parseTime(r.start)
    const en = parseTime(r.end)
    if (s === null)
      e[`${k}.start`] = '格式为 HH:MM'
    if (en === null)
      e[`${k}.end`] = '格式为 HH:MM'
    if (s !== null && en !== null && s === en)
      e[`${k}.end`] = '结束时间不能与开始时间相同'
    if (!isValidScheduleMultiplier(r.multiplier))
      e[`${k}.multiplier`] = '0–10 之间的数字'
  })
  for (const o of findOverlaps(form.rows.map(r => ({ days: r.days, start: r.start, end: r.end })))) {
    const msg = `与时段 ${o.a + 1} 重叠（${describeWeekInterval(o.at)}）`
    e[`schedule.${o.b}`] ??= msg
  }
  if (form.rows.length && !isValidTimezone(form.timezone))
    e.scheduleTimezone = '无效的时区'
  return e
}

/**
 * Server 422 details → form keys. Indexed paths (`schedule[1].start` → `schedule.1.start`)
 * are kept; the current server reports one `schedule` message prefixed with the 1-based
 * period ("第 2 个时段：与第 1 个时段重叠"), which is moved to that period's row.
 */
export function normalizeScheduleErrorKeys(details: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(details)) {
    const key = k.replace(/\[(\d+)\]/g, '.$1')
    const m = key === 'schedule' ? /^第 ?(\d+) ?个时段[：:]\s*(.+)$/.exec(v) : null
    if (m)
      out[`schedule.${Number(m[1]) - 1}`] = m[2]!
    else
      out[key] = v
  }
  return out
}

/** Errors of row `i` (field → message; `''` = row-level). */
export function rowErrors(errors: Record<string, string>, i: number): Record<string, string> {
  const prefix = `schedule.${i}`
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(errors)) {
    if (k === prefix)
      out[''] = v
    else if (k.startsWith(`${prefix}.`))
      out[k.slice(prefix.length + 1)] = v
  }
  return out
}

// ---------------------------------------------------------------------------
// Timeline
// ---------------------------------------------------------------------------

export interface DaySegment {
  /** Minutes within the day, [from, to). */
  from: number
  to: number
  multiplier: string
  /** Index of the period. */
  index: number
}

/** Segments per weekday (index 0 = Sunday), for the 24h preview bars. */
export function timelineByDay(periods: readonly Pick<PriceSchedulePeriod, 'days' | 'start' | 'end' | 'multiplier'>[]): DaySegment[][] {
  const out: DaySegment[][] = WEEKDAYS.map(() => [])
  periods.forEach((p, index) => {
    for (const iv of weekIntervals(p)) {
      // Split at day boundaries (cross-midnight periods continue on the next row).
      let from = iv.from
      while (from < iv.to) {
        const d = Math.floor(from / DAY_MINUTES)
        const to = Math.min(iv.to, (d + 1) * DAY_MINUTES)
        out[d]!.push({ from: from - d * DAY_MINUTES, to: to - d * DAY_MINUTES, multiplier: p.multiplier, index })
        from = to
      }
    }
  })
  for (const segs of out)
    segs.sort((a, b) => a.from - b.from)
  return out
}

// ---------------------------------------------------------------------------
// Descriptions
// ---------------------------------------------------------------------------

/** "每天" / "工作日" / "周末" / "周一、三、五". */
export function describeDays(days: readonly number[]): string {
  const d = normDays(days)
  if (d.length === 0 || d.length === 7)
    return '每天'
  if (d.join() === '1,2,3,4,5')
    return '工作日'
  if (d.join() === '0,6')
    return '周末'
  // Monday-first display order.
  const order = [...d].sort((a, b) => ((a + 6) % 7) - ((b + 6) % 7))
  return `周${order.map(x => DAY_CHARS[x]).join('、')}`
}

/** "每天 00:30–08:30 ×0.5" / "周五 22:00–次日 02:00 ×0.8". */
export function describePeriod(p: PriceSchedulePeriod): string {
  const s = parseTime(p.start)
  const e = parseTime(p.end)
  const cross = s !== null && e !== null && s > e
  return `${describeDays(p.days)} ${p.start}–${cross ? '次日 ' : ''}${p.end} ×${canonicalMultiplier(p.multiplier)}`
}

export function hasSchedule<T extends { schedule?: PriceSchedulePeriod[] | null }>(p: T | null | undefined): p is T & { schedule: PriceSchedulePeriod[] } {
  return !!p?.schedule && p.schedule.length > 0
}

// ---------------------------------------------------------------------------
// Current period (plaza note)
// ---------------------------------------------------------------------------

/** Weekday (0 = Sunday) and minute of day of `at` in `tz`; null for an unknown zone. */
export function zonedWeekMinute(at: Date, tz: string): { weekday: number, minute: number } | null {
  try {
    const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(at)
    const get = (t: string) => parts.find(p => p.type === t)?.value ?? ''
    const weekday = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].indexOf(get('weekday'))
    const hour = Number(get('hour')) % 24
    const minute = Number(get('minute'))
    if (weekday < 0 || !Number.isFinite(hour) || !Number.isFinite(minute))
      return null
    return { weekday, minute: hour * 60 + minute }
  }
  catch {
    return null
  }
}

/** Index of the first period containing week minute `wm` (the server takes the first hit), or -1. */
export function periodAt(periods: readonly PriceSchedulePeriod[], wm: number): number {
  return periods.findIndex(p => weekIntervals(p).some(iv => wm >= iv.from && wm < iv.to))
}

function isOne(m: string | null | undefined): boolean {
  return m != null && isValidAmount(m) && toNano(m) === 1_000_000_000n
}

function clockAfter(nowWm: number, targetWm: number): string {
  const delta = (targetWm - nowWm + WEEK_MINUTES) % WEEK_MINUTES
  const nowDay = Math.floor(nowWm / DAY_MINUTES)
  const target = (nowWm + delta) % WEEK_MINUTES
  const targetDay = Math.floor(target / DAY_MINUTES)
  const time = formatTime(target - targetDay * DAY_MINUTES)
  if (delta < DAY_MINUTES && targetDay === nowDay)
    return time
  if (delta < 2 * DAY_MINUTES && targetDay === (nowDay + 1) % 7)
    return `明天 ${time}`
  return `${DAY_NAMES[targetDay]} ${time}`
}

export interface ScheduleNote {
  /** Multiplier in effect now (server `currentMultiplier` when sent). */
  current: string
  /** A period applies right now (current ≠ 1). */
  active: boolean
  /** "当前时段 ×0.5，08:30 恢复原价" / "分时价格：每天 00:30–08:30 ×0.5". */
  text: string
  /** One line per period, for tooltips. */
  periods: string[]
}

/**
 * Plaza note for a price with a schedule (null without one). The current multiplier comes
 * from the server when present; the end of the current period / the next change is
 * computed client-side in the schedule's time zone.
 */
export function scheduleNote(price: Pick<PlazaPrice, 'schedule' | 'scheduleTimezone' | 'currentMultiplier'> | null | undefined, now: Date = new Date(), viewerTz?: string | null): ScheduleNote | null {
  if (!hasSchedule(price))
    return null
  const periods = price.schedule
  const tz = price.scheduleTimezone || DEFAULT_TIMEZONE
  const lines = periods.map(describePeriod)
  const zoned = zonedWeekMinute(now, tz)
  const wm = zoned ? zoned.weekday * DAY_MINUTES + zoned.minute : null
  const idx = wm === null ? -1 : periodAt(periods, wm)
  const computed = idx >= 0 ? canonicalMultiplier(periods[idx]!.multiplier) : '1'
  const current = typeof price.currentMultiplier === 'string' && isValidAmount(price.currentMultiplier) ? canonicalMultiplier(price.currentMultiplier) : computed
  const tzSuffix = viewerTz && viewerTz !== tz ? `（${tz}）` : ''
  if (isOne(current) || wm === null) {
    return { current, active: false, text: `分时价格：${lines.join('；')}${tzSuffix}`, periods: lines }
  }
  let text = `当前时段 ×${current}`
  if (idx >= 0) {
    const iv = weekIntervals(periods[idx]!).find(x => wm >= x.from && wm < x.to)
    if (iv) {
      // A period ending at Saturday 24:00 continues on Sunday 00:00 when it wraps.
      const endWm = iv.to % WEEK_MINUTES
      const next = periodAt(periods, endWm)
      const nextM = next >= 0 ? canonicalMultiplier(periods[next]!.multiplier) : '1'
      const at = clockAfter(wm, endWm)
      text += isOne(nextM) ? `，${at} 恢复原价` : `，${at} 起 ×${nextM}`
    }
  }
  return { current, active: true, text: text + tzSuffix, periods: lines }
}

// ---------------------------------------------------------------------------
// Presets
// ---------------------------------------------------------------------------

export interface SchedulePreset {
  id: string
  label: string
  description: string
  rows: ScheduleRow[]
  timezone: string
}

export const SCHEDULE_PRESETS: SchedulePreset[] = [
  {
    id: 'deepseek-offpeak',
    label: 'DeepSeek 低谷价（00:30–08:30 半价）',
    description: '北京时间每天 00:30–08:30 按 ×0.5 计价。',
    rows: [{ days: everyDay(), start: '00:30', end: '08:30', multiplier: '0.5' }],
    timezone: 'Asia/Shanghai',
  },
]
