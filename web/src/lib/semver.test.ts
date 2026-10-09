import { describe, expect, it } from 'vitest'
import { compareSemver } from './semver'

describe('compareSemver', () => {
  it('compares numerically', () => {
    expect(compareSemver('1.10.0', '1.9.0')).toBe(1)
    expect(compareSemver('1.0.0', '1.0.1')).toBe(-1)
    expect(compareSemver('2.0.0', '2.0.0')).toBe(0)
  })
  it('sorts pre-releases before releases', () => {
    expect(compareSemver('1.0.0-beta', '1.0.0')).toBe(-1)
    expect(compareSemver('1.0.0', '1.0.0-rc.1')).toBe(1)
  })
})
