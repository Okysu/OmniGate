import { describe, expect, it } from 'vitest'
import {
  buildSchedule,
  describeDays,
  describePeriod,
  emptyRow,
  everyDay,
  findOverlaps,
  formatTime,
  normalizeScheduleErrorKeys,
  parseTime,
  periodAt,
  rowErrors,
  SCHEDULE_PRESETS,
  scheduleFormFrom,
  scheduleNote,
  timelineByDay,
  toggleDay,
  validateSchedule,
  weekIntervals,
  zonedWeekMinute,
} from './priceSchedule'

const DAY = 1440
const deepseek = { days: [], start: '00:30', end: '08:30', multiplier: '0.5' }

describe('times', () => {
  it('parses and formats HH:MM', () => {
    expect(parseTime('08:30')).toBe(510)
    expect(parseTime('24:00')).toBeNull()
    expect(parseTime('8:30')).toBeNull()
    expect(formatTime(510)).toBe('08:30')
    expect(formatTime(1440)).toBe('24:00')
  })
})

describe('form ↔ body', () => {
  it('maps every day to [] and keeps explicit days sorted', () => {
    const form = scheduleFormFrom({ schedule: [deepseek, { days: [5, 1], start: '22:00', end: '02:00', multiplier: '0.80' }], scheduleTimezone: 'UTC' })
    expect(form.timezone).toBe('UTC')
    expect(form.rows[0]!.days).toEqual(everyDay())
    expect(buildSchedule(form.rows)).toEqual([deepseek, { days: [1, 5], start: '22:00', end: '02:00', multiplier: '0.8' }])
    expect(scheduleFormFrom(null)).toEqual({ rows: [], timezone: 'Asia/Shanghai' })
    expect(toggleDay([1, 2], 2)).toEqual([1])
    expect(toggleDay([3], 0)).toEqual([0, 3])
  })

  it('has the DeepSeek off-peak preset', () => {
    const p = SCHEDULE_PRESETS.find(x => x.id === 'deepseek-offpeak')!
    expect(buildSchedule(p.rows)).toEqual([deepseek])
    expect(p.timezone).toBe('Asia/Shanghai')
  })
})

describe('week intervals & overlap (mirrors the server 422)', () => {
  it('spills cross-midnight periods into the next day (start-day anchored) and wraps Saturday', () => {
    expect(weekIntervals({ days: [5], start: '22:00', end: '02:00' })).toEqual([{ from: 5 * DAY + 1320, to: 6 * DAY + 120 }])
    expect(weekIntervals({ days: [6], start: '23:00', end: '01:00' })).toEqual([{ from: 6 * DAY + 1380, to: 7 * DAY }, { from: 0, to: 60 }])
    expect(weekIntervals({ days: [], start: '00:30', end: '08:30' })).toHaveLength(7)
    expect(weekIntervals({ days: [1], start: '10:00', end: '10:00' })).toEqual([])
  })

  it('finds overlapping periods, including across midnight and the week boundary', () => {
    expect(findOverlaps([deepseek, { days: [1], start: '08:30', end: '12:00' }])).toEqual([]) // end is exclusive
    expect(findOverlaps([deepseek, { days: [1], start: '08:00', end: '12:00' }])).toHaveLength(1)
    // Friday 22:00–02:00 overlaps Saturday 01:00–03:00.
    const o = findOverlaps([{ days: [5], start: '22:00', end: '02:00' }, { days: [6], start: '01:00', end: '03:00' }])
    expect(o).toEqual([{ a: 0, b: 1, at: { from: 6 * DAY + 60, to: 6 * DAY + 120 } }])
    // Saturday night wraps into Sunday morning.
    expect(findOverlaps([{ days: [6], start: '23:00', end: '01:00' }, { days: [0], start: '00:00', end: '00:30' }])).toHaveLength(1)
    // Different days do not overlap.
    expect(findOverlaps([{ days: [1], start: '00:00', end: '23:59' }, { days: [2], start: '00:00', end: '23:59' }])).toEqual([])
  })

  it('validates rows with server-style keys', () => {
    const e = validateSchedule({
      timezone: 'Mars/Base',
      rows: [
        { days: [], start: '25:00', end: '08:00', multiplier: '11' },
        { days: everyDay(), start: '01:00', end: '01:00', multiplier: '0.5' },
        { ...emptyRow(), start: '00:00', end: '08:00' },
        { ...emptyRow(), start: '07:00', end: '09:00' },
      ],
    })
    expect(e['schedule.0.days']).toBe('至少选择一天')
    expect(e['schedule.0.start']).toBeDefined()
    expect(e['schedule.0.multiplier']).toBeDefined()
    expect(e['schedule.1.end']).toMatch(/不能与开始时间相同/)
    expect(e['schedule.3']).toMatch(/与时段 3 重叠（周日 07:00–08:00）/)
    expect(e.scheduleTimezone).toBe('无效的时区')
    expect(validateSchedule({ timezone: 'Asia/Shanghai', rows: [{ ...emptyRow(), start: '00:30', end: '08:30' }] })).toEqual({})
  })

  it('normalises server detail keys and groups them per row', () => {
    const e = normalizeScheduleErrorKeys({ 'schedule[1].start': 'bad', 'schedule[2]': 'overlap', 'name': 'x' })
    expect(e).toEqual({ 'schedule.1.start': 'bad', 'schedule.2': 'overlap', 'name': 'x' })
    expect(rowErrors(e, 1)).toEqual({ start: 'bad' })
    expect(rowErrors(e, 2)).toEqual({ '': 'overlap' })
    // Current server format: one `schedule` message naming the period.
    expect(normalizeScheduleErrorKeys({ schedule: '第 2 个时段：与第 1 个时段重叠' })).toEqual({ 'schedule.1': '与第 1 个时段重叠' })
    expect(normalizeScheduleErrorKeys({ schedule: '最多 48 个时段' })).toEqual({ schedule: '最多 48 个时段' })
    expect(scheduleFormFrom({ schedule: [{ days: [], start: '20:00', end: '24:00', multiplier: '2' }] }).rows[0]!.end).toBe('00:00')
  })
})

describe('timeline & descriptions', () => {
  it('splits segments per weekday', () => {
    const t = timelineByDay([{ days: [5], start: '22:00', end: '02:00', multiplier: '0.8' }])
    expect(t[5]).toEqual([{ from: 1320, to: 1440, multiplier: '0.8', index: 0 }])
    expect(t[6]).toEqual([{ from: 0, to: 120, multiplier: '0.8', index: 0 }])
    expect(t[0]).toEqual([])
  })

  it('describes days and periods', () => {
    expect(describeDays([])).toBe('每天')
    expect(describeDays(everyDay())).toBe('每天')
    expect(describeDays([1, 2, 3, 4, 5])).toBe('工作日')
    expect(describeDays([0, 6])).toBe('周末')
    expect(describeDays([0, 1, 3])).toBe('周一、三、日')
    expect(describePeriod(deepseek)).toBe('每天 00:30–08:30 ×0.5')
    expect(describePeriod({ days: [5], start: '22:00', end: '02:00', multiplier: '0.80' })).toBe('周五 22:00–次日 02:00 ×0.8')
  })
})

describe('current period note', () => {
  // 2026-10-09 is a Friday. 02:00 Asia/Shanghai = 2026-10-08T18:00Z.
  const at = (iso: string) => new Date(iso)

  it('reads the weekday / minute in a time zone', () => {
    expect(zonedWeekMinute(at('2026-10-08T18:00:00Z'), 'Asia/Shanghai')).toEqual({ weekday: 5, minute: 120 })
    expect(zonedWeekMinute(at('2026-10-08T18:00:00Z'), 'Nope/Zone')).toBeNull()
    expect(periodAt([deepseek], 5 * DAY + 120)).toBe(0)
    expect(periodAt([deepseek], 5 * DAY + 600)).toBe(-1)
  })

  it('says when the list price returns while a period applies', () => {
    const n = scheduleNote({ schedule: [deepseek], scheduleTimezone: 'Asia/Shanghai', currentMultiplier: '0.5' }, at('2026-10-08T18:00:00Z'), 'Asia/Shanghai')
    expect(n?.active).toBe(true)
    expect(n?.text).toBe('当前时段 ×0.5，08:30 恢复原价')
  })

  it('names the next multiplier for back-to-back periods and the next day for cross-midnight ends', () => {
    const sched = [{ days: [], start: '22:00', end: '02:00', multiplier: '0.5' }, { days: [], start: '02:00', end: '06:00', multiplier: '0.8' }]
    // 23:00 Friday Shanghai = 15:00Z.
    const n = scheduleNote({ schedule: sched, scheduleTimezone: 'Asia/Shanghai' }, at('2026-10-09T15:00:00Z'), 'Asia/Shanghai')
    expect(n?.text).toBe('当前时段 ×0.5，明天 02:00 起 ×0.8')
  })

  it('lists the periods outside them and mentions a foreign time zone', () => {
    const n = scheduleNote({ schedule: [deepseek], scheduleTimezone: 'Asia/Shanghai', currentMultiplier: '1' }, at('2026-10-09T04:00:00Z'), 'Europe/Berlin')
    expect(n?.active).toBe(false)
    expect(n?.text).toBe('分时价格：每天 00:30–08:30 ×0.5（Asia/Shanghai）')
    expect(scheduleNote({ schedule: [] })).toBeNull()
    expect(scheduleNote(null)).toBeNull()
  })
})
