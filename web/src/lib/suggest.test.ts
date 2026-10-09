import { describe, expect, it } from 'vitest'
import { filterSuggestions, highlightParts, isKnownSuggestion, matchRank, normalizeSuggestOptions } from './suggest'

const models = normalizeSuggestOptions(['gpt-4o-mini', 'gpt-4o', 'claude-sonnet-4', 'o1', 'text-embedding-3-small', 'deepseek-chat'])
const values = (r: { items: { value: string }[] }) => r.items.map(i => i.value)

describe('normalizeSuggestOptions', () => {
  it('accepts strings and objects, drops blanks and duplicates', () => {
    expect(normalizeSuggestOptions(['a', '', 'a', { value: 'b', label: 'Bee', description: 'd' }, { value: 'c' }, { value: 'b' }])).toEqual([
      { value: 'a', label: 'a' },
      { value: 'b', label: 'Bee', description: 'd' },
      { value: 'c', label: 'c' },
    ])
  })
})

describe('matchRank', () => {
  it('ranks exact < prefix < word start < anywhere', () => {
    expect(matchRank('GPT-4o', 'gpt-4o')).toBe(0)
    expect(matchRank('gpt-4o-mini', 'gpt')).toBe(1)
    expect(matchRank('text-embedding-3', 'emb')).toBe(2)
    expect(matchRank('org/model', 'model')).toBe(2)
    expect(matchRank('deepseek', 'seek')).toBe(3)
    expect(matchRank('deepseek', 'xyz')).toBe(-1)
  })

  it('finds a later word-start occurrence after an inner one', () => {
    // "mini" first appears inside "gemini", then as its own word.
    expect(matchRank('gemini-mini', 'mini')).toBe(2)
  })
})

describe('filterSuggestions', () => {
  it('lists everything for an empty query, without a create entry', () => {
    const r = filterSuggestions(models, '  ')
    expect(values(r)).toEqual(models.map(m => m.value))
    expect(r.create).toBeNull()
    expect(r.hidden).toBe(0)
  })

  it('filters case-insensitively and orders by match quality, stable within a rank', () => {
    const r = filterSuggestions(models, 'GPT-4O')
    expect(values(r)).toEqual(['gpt-4o', 'gpt-4o-mini'])
    expect(values(filterSuggestions(models, 's'))).toEqual(['claude-sonnet-4', 'text-embedding-3-small', 'deepseek-chat'])
  })

  it('offers the typed text when no option has exactly that value', () => {
    expect(filterSuggestions(models, ' gpt-5 ').create).toBe('gpt-5')
    expect(filterSuggestions(models, 'gpt-4o').create).toBeNull()
    // Case matters for values: "GPT-4O" is a different model name.
    expect(filterSuggestions(models, 'GPT-4O').create).toBe('GPT-4O')
    expect(filterSuggestions(models, 'gpt-5', { creatable: false }).create).toBeNull()
  })

  it('matches labels and descriptions, descriptions last', () => {
    const opts = normalizeSuggestOptions([
      { value: 'x1', label: 'Alpha', description: 'beta release' },
      { value: 'beta-2', label: 'Beta 2' },
    ])
    expect(values(filterSuggestions(opts, 'beta'))).toEqual(['beta-2', 'x1'])
    expect(values(filterSuggestions(opts, 'alp'))).toEqual(['x1'])
  })

  it('leaves out excluded values and never offers them for creation', () => {
    const r = filterSuggestions(models, 'gpt', { exclude: ['gpt-4o'] })
    expect(values(r)).toEqual(['gpt-4o-mini'])
    expect(filterSuggestions(models, 'gpt-4o', { exclude: ['gpt-4o'] }).create).toBeNull()
    expect(filterSuggestions(models, 'custom', { exclude: ['custom'] }).create).toBeNull()
  })

  it('caps the list and reports how many were cut', () => {
    const many = normalizeSuggestOptions(Array.from({ length: 30 }, (_, i) => `m-${i}`))
    const r = filterSuggestions(many, 'm-', { limit: 10 })
    expect(r.items).toHaveLength(10)
    expect(r.hidden).toBe(20)
  })
})

describe('isKnownSuggestion', () => {
  it('compares trimmed text with option values exactly', () => {
    expect(isKnownSuggestion(models, ' o1 ')).toBe(true)
    expect(isKnownSuggestion(models, 'O1')).toBe(false)
    expect(isKnownSuggestion(models, '')).toBe(false)
  })
})

describe('highlightParts', () => {
  it('splits around the first case-insensitive match', () => {
    expect(highlightParts('gpt-4o-mini', '4O')).toEqual([
      { text: 'gpt-', match: false },
      { text: '4o', match: true },
      { text: '-mini', match: false },
    ])
    expect(highlightParts('OpenAI', 'open')).toEqual([{ text: 'Open', match: true }, { text: 'AI', match: false }])
    expect(highlightParts('OpenAI', '')).toEqual([{ text: 'OpenAI', match: false }])
    expect(highlightParts('OpenAI', 'xyz')).toEqual([{ text: 'OpenAI', match: false }])
  })
})
