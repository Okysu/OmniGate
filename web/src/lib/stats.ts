import type { StatsDaily } from './types'

/**
 * Returns one entry per UTC day between `from` and `to` (inclusive), filling
 * days without traffic with zeros. The backend aggregates by UTC date and
 * omits empty days. Capped at 93 days.
 */
export function fillDaily(daily: StatsDaily[], from: string, to: string): StatsDaily[] {
  const byDate = new Map(daily.map(d => [d.date, d]))
  const start = new Date(from)
  const end = new Date(to)
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || start > end)
    return daily
  const out: StatsDaily[] = []
  const cur = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth(), start.getUTCDate()))
  // `to` is exclusive on the backend: a range ending exactly at 00:00Z does not include that day.
  const last = new Date(end.getTime() - 1)
  while (cur <= last && out.length < 93) {
    const date = cur.toISOString().slice(0, 10)
    out.push(byDate.get(date) ?? { date, requests: 0, errors: 0, inputTokens: 0, outputTokens: 0, charge: '0' })
    cur.setUTCDate(cur.getUTCDate() + 1)
  }
  // Keep any server days outside the computed window (should not happen).
  for (const d of daily) {
    if (!out.some(o => o.date === d.date))
      out.push(d)
  }
  return out.sort((a, b) => a.date.localeCompare(b.date))
}

/** "2026-10-08" → "10-08". */
export function shortDate(date: string): string {
  return /^\d{4}-\d{2}-\d{2}$/.test(date) ? date.slice(5) : date
}

/** errors / requests, or null when there are no requests. */
export function errorRate(requests: number, errors: number): number | null {
  return requests > 0 ? errors / requests : null
}

/**
 * Prompt cache hit ratio: cache reads / prompt tokens (input + cache read + cache write,
 * `inputTokens` in the summary). Uses the server's `cacheHitRate` when present.
 */
export function cacheHitRate(row: { inputTokens?: number, cacheReadTokens?: number, cacheHitRate?: number | null }): number | null {
  if (typeof row.cacheHitRate === 'number')
    return row.cacheHitRate
  if (row.cacheHitRate === null)
    return null
  const prompt = row.inputTokens ?? 0
  return prompt > 0 && typeof row.cacheReadTokens === 'number' ? row.cacheReadTokens / prompt : null
}
