import { describe, expect, it } from 'vitest'
import { fromLocalInput, presetRange, toLocalInput, validateRange } from './timeRange'

describe('timeRange', () => {
  const now = Date.UTC(2026, 9, 8, 12, 0, 0)

  it('computes presets relative to now', () => {
    expect(presetRange('1h', now)).toEqual({ from: '2026-10-08T11:00:00.000Z', to: '2026-10-08T12:00:00.000Z' })
    expect(presetRange('7d', now).from).toBe('2026-10-01T12:00:00.000Z')
  })

  it('round-trips datetime-local values', () => {
    const iso = new Date(2026, 0, 2, 3, 4).toISOString()
    expect(toLocalInput(iso)).toBe('2026-01-02T03:04')
    expect(fromLocalInput('2026-01-02T03:04')).toBe(iso)
    expect(fromLocalInput('')).toBeNull()
    expect(toLocalInput('garbage')).toBe('')
  })

  it('validates ranges', () => {
    expect(validateRange(null, '2026-01-01T00:00:00Z')).not.toBeNull()
    expect(validateRange('2026-01-02T00:00:00Z', '2026-01-01T00:00:00Z')).toContain('早于')
    expect(validateRange('2026-01-01T00:00:00Z', '2026-06-01T00:00:00Z')).toContain('92')
    expect(validateRange('2026-01-01T00:00:00Z', '2026-01-08T00:00:00Z')).toBeNull()
  })
})
