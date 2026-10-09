import { describe, expect, it } from 'vitest'
import { toCsv } from './csv'

describe('toCsv', () => {
  it('joins rows with CRLF and quotes special cells', () => {
    expect(toCsv([['code', 'amount'], ['OG-AAAAA', '10.5'], ['a,b', 'say "hi"']]))
      .toBe('code,amount\r\nOG-AAAAA,10.5\r\n"a,b","say ""hi"""')
  })

  it('neutralises formula injection and handles nulls', () => {
    expect(toCsv([['=SUM(A1)', null, -1, '-x']])).toBe('\'=SUM(A1),,-1,\'-x')
  })
})
