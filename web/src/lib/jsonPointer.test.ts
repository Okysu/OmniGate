import { describe, expect, it } from 'vitest'
import { parseBind, parsePointer, resolveBind, resolvePointer } from './jsonPointer'

// Example document from RFC 6901 §5.
const doc = {
  'foo': ['bar', 'baz'],
  '': 0,
  'a/b': 1,
  'c%d': 2,
  'e^f': 3,
  'g|h': 4,
  'i\\j': 5,
  'k"l': 6,
  ' ': 7,
  'm~n': 8,
}

describe('parsePointer', () => {
  it('splits and unescapes tokens', () => {
    expect(parsePointer('')).toEqual([])
    expect(parsePointer('/')).toEqual([''])
    expect(parsePointer('/a~1b/m~0n')).toEqual(['a/b', 'm~n'])
    // ~01 must decode to "~1", not "/".
    expect(parsePointer('/~01')).toEqual(['~1'])
  })

  it('rejects malformed pointers', () => {
    expect(parsePointer('foo')).toBeNull()
    expect(parsePointer('/a~2')).toBeNull()
    expect(parsePointer('/a~')).toBeNull()
  })
})

describe('resolvePointer (RFC 6901 §5 examples)', () => {
  const cases: Array<[string, unknown]> = [
    ['', doc],
    ['/foo', ['bar', 'baz']],
    ['/foo/0', 'bar'],
    ['/', 0],
    ['/a~1b', 1],
    ['/c%d', 2],
    ['/e^f', 3],
    ['/g|h', 4],
    ['/i\\j', 5],
    ['/k"l', 6],
    ['/ ', 7],
    ['/m~0n', 8],
  ]
  for (const [ptr, want] of cases) {
    it(`resolves ${JSON.stringify(ptr)}`, () => {
      expect(resolvePointer(doc, ptr)).toEqual({ found: true, value: want })
    })
  }

  it('reports missing members and bad array indices', () => {
    expect(resolvePointer(doc, '/nope').found).toBe(false)
    expect(resolvePointer(doc, '/foo/2').found).toBe(false)
    expect(resolvePointer(doc, '/foo/-').found).toBe(false)
    expect(resolvePointer(doc, '/foo/01').found).toBe(false)
    expect(resolvePointer(doc, '/foo/0/x').found).toBe(false)
    expect(resolvePointer(null, '/a').found).toBe(false)
  })

  it('does not walk the prototype chain', () => {
    expect(resolvePointer({}, '/constructor').found).toBe(false)
    expect(resolvePointer({}, '/__proto__').found).toBe(false)
  })

  it('finds explicit nulls', () => {
    expect(resolvePointer({ a: null }, '/a')).toEqual({ found: true, value: null })
  })
})

describe('parseBind / resolveBind', () => {
  it('parses capability and pointer', () => {
    expect(parseBind('balance.get:/total')).toEqual({ capability: 'balance.get', pointer: '/total' })
    expect(parseBind('custom.x:')).toEqual({ capability: 'custom.x', pointer: '' })
    expect(parseBind('balance.get')).toBeNull()
    expect(parseBind(':/x')).toBeNull()
    expect(parseBind('a:b')).toBeNull()
    expect(parseBind(undefined)).toBeNull()
  })

  const results = {
    'balance.get': { ok: true, unsupported: false, output: { total: '12.50', currency: 'CNY', nested: { 'a/b': [10, 20] } } },
    'usage.query': { ok: true, unsupported: true, output: { unsupported: true, reason: 'no api' } },
    'models.list': { ok: false, unsupported: false, output: null, error: 'boom' },
  }

  it('resolves values from the latest output', () => {
    expect(resolveBind('balance.get:/total', results)).toEqual({ state: 'ok', value: '12.50', capability: 'balance.get' })
    expect(resolveBind('balance.get:/nested/a~1b/1', results).value).toBe(20)
  })

  it('classifies missing, failed, unsupported and invalid binds', () => {
    expect(resolveBind('balance.get:/granted', results).state).toBe('missing')
    expect(resolveBind('quota.get:/windows', results).state).toBe('missing')
    expect(resolveBind('usage.query:/periods', results).state).toBe('unsupported')
    expect(resolveBind('models.list:/models', results).state).toBe('failed')
    expect(resolveBind('garbage', results).state).toBe('invalid')
  })
})
