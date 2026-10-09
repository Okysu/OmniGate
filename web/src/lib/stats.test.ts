import { describe, expect, it } from 'vitest'
import { errorRate, fillDaily, shortDate } from './stats'

describe('fillDaily', () => {
  it('fills missing UTC days with zeros', () => {
    const out = fillDaily(
      [{ date: '2026-10-02', requests: 3, errors: 1, inputTokens: 10, outputTokens: 5, charge: '0.1' }],
      '2026-10-01T08:00:00Z',
      '2026-10-03T08:00:00Z',
    )
    expect(out.map(d => d.date)).toEqual(['2026-10-01', '2026-10-02', '2026-10-03'])
    expect(out[0]).toMatchObject({ requests: 0, charge: '0' })
    expect(out[1]?.requests).toBe(3)
  })

  it('treats `to` as exclusive at midnight', () => {
    const out = fillDaily([], '2026-10-01T00:00:00Z', '2026-10-03T00:00:00Z')
    expect(out.map(d => d.date)).toEqual(['2026-10-01', '2026-10-02'])
  })

  it('returns the input for invalid ranges', () => {
    const daily = [{ date: '2026-10-01', requests: 1, errors: 0, inputTokens: 0, outputTokens: 0, charge: '0' }]
    expect(fillDaily(daily, 'x', 'y')).toBe(daily)
  })
})

describe('helpers', () => {
  it('shortens dates and computes error rates', () => {
    expect(shortDate('2026-10-08')).toBe('10-08')
    expect(shortDate('n/a')).toBe('n/a')
    expect(errorRate(0, 0)).toBeNull()
    expect(errorRate(4, 1)).toBe(0.25)
  })
})
