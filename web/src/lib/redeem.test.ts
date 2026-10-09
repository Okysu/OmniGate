import { describe, expect, it } from 'vitest'
import { normalizeRedeemCode } from './redeem'

describe('normalizeRedeemCode', () => {
  const canonical = 'OG-ABCDE-FGH12-34567-89XYZ'

  it('accepts the canonical form', () => {
    expect(normalizeRedeemCode(canonical)).toBe(canonical)
  })

  it('ignores case, spaces and hyphens and the optional prefix', () => {
    expect(normalizeRedeemCode('og abcde fgh12 34567 89xyz')).toBe(canonical)
    expect(normalizeRedeemCode('abcdefgh123456789xyz')).toBe(canonical)
    expect(normalizeRedeemCode('  ABCDE-FGH12-34567-89XYZ  ')).toBe(canonical)
  })

  it('maps Crockford ambiguous letters', () => {
    expect(normalizeRedeemCode('OG-ABCDE-FGHIL-O4567-89XYZ')).toBe('OG-ABCDE-FGH11-04567-89XYZ')
  })

  it('does not strip a leading 0G from a bare 20-symbol code', () => {
    expect(normalizeRedeemCode('0GCDEFGH123456789XYZ')).toBe('OG-0GCDE-FGH12-34567-89XYZ')
  })

  it('rejects invalid input', () => {
    expect(normalizeRedeemCode('')).toBeNull()
    expect(normalizeRedeemCode('OG-ABCDE')).toBeNull()
    expect(normalizeRedeemCode('OG-ABCDE-FGH12-34567-89XYU')).toBeNull() // U is not Crockford
    expect(normalizeRedeemCode('兑换码')).toBeNull()
  })
})
