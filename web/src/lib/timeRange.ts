// Time-range presets for logs / stats filters.

export type RangePreset = '1h' | '24h' | '7d' | '30d' | 'custom'

export const RANGE_PRESET_LABELS: Record<RangePreset, string> = {
  '1h': '近 1 小时',
  '24h': '近 24 小时',
  '7d': '近 7 天',
  '30d': '近 30 天',
  'custom': '自定义',
}

const PRESET_MS: Record<Exclude<RangePreset, 'custom'>, number> = {
  '1h': 3600_000,
  '24h': 86400_000,
  '7d': 7 * 86400_000,
  '30d': 30 * 86400_000,
}

/** Maximum span accepted by the backend (days). */
export const MAX_RANGE_DAYS = 92

export interface TimeRange {
  from: string
  to: string
}

/** ISO `from`/`to` for a preset ending at `now`. */
export function presetRange(preset: Exclude<RangePreset, 'custom'>, now: number = Date.now()): TimeRange {
  return { from: new Date(now - PRESET_MS[preset]).toISOString(), to: new Date(now).toISOString() }
}

/** `<input type="datetime-local">` value (local time, minute precision) for an ISO time. */
export function toLocalInput(iso: string | Date): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso
  if (Number.isNaN(d.getTime()))
    return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Parses a `datetime-local` value (interpreted in local time) to ISO, or null. */
export function fromLocalInput(value: string): string | null {
  if (!value)
    return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d.toISOString()
}

/** Validation message for a custom range, or null when valid. */
export function validateRange(from: string | null, to: string | null): string | null {
  if (!from || !to)
    return '请选择开始和结束时间'
  const f = new Date(from).getTime()
  const t = new Date(to).getTime()
  if (!(f < t))
    return '开始时间必须早于结束时间'
  if (t - f > MAX_RANGE_DAYS * 86400_000)
    return `时间跨度不能超过 ${MAX_RANGE_DAYS} 天`
  return null
}
