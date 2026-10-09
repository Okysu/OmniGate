/**
 * Pure logic behind `SuggestInput` (free text with suggestions): option normalisation,
 * ranking/filtering as the user types, the creatable "使用 “xxx”" entry and match
 * highlighting.
 */

export interface SuggestOption {
  value: string
  /** Display text; defaults to `value`. */
  label?: string
  /** Secondary line under the label. */
  description?: string
}

export type SuggestOptionsInput = readonly (string | SuggestOption)[]

export interface NormalizedSuggestOption {
  value: string
  label: string
  description?: string
}

/** Turns strings/objects into options, dropping blanks and duplicate values (first wins). */
export function normalizeSuggestOptions(input: SuggestOptionsInput): NormalizedSuggestOption[] {
  const seen = new Set<string>()
  const out: NormalizedSuggestOption[] = []
  for (const raw of input) {
    const o = typeof raw === 'string' ? { value: raw } : raw
    if (!o.value || seen.has(o.value))
      continue
    seen.add(o.value)
    out.push({ value: o.value, label: o.label || o.value, ...(o.description ? { description: o.description } : {}) })
  }
  return out
}

/** Separators that start a "word" inside model and vendor names (gpt-4o, org/model, a.b). */
const WORD_START = /[\s\-_/.:@]/

/**
 * Match quality of `text` for a lower-cased query; lower is better, -1 means no match.
 * 0 exact · 1 prefix · 2 word start · 3 anywhere.
 */
export function matchRank(text: string, q: string): number {
  const t = text.toLowerCase()
  if (t === q)
    return 0
  if (t.startsWith(q))
    return 1
  let i = t.indexOf(q)
  if (i < 0)
    return -1
  while (i >= 0) {
    if (WORD_START.test(t[i - 1] ?? ''))
      return 2
    i = t.indexOf(q, i + 1)
  }
  return 3
}

function optionRank(o: NormalizedSuggestOption, q: string): number {
  const ranks = [matchRank(o.value, q), matchRank(o.label, q)].filter(r => r >= 0)
  if (ranks.length)
    return Math.min(...ranks)
  // Description matches rank last.
  return o.description && o.description.toLowerCase().includes(q) ? 4 : -1
}

export interface SuggestFilterOptions {
  /** Most items to return (the rest are counted in `hidden`). Default 100. */
  limit?: number
  /** Offer the typed text as a value when no option has exactly that value. Default true. */
  creatable?: boolean
  /** Values to leave out (e.g. already added to a list). */
  exclude?: readonly string[]
}

export interface SuggestFilterResult {
  items: NormalizedSuggestOption[]
  /** Typed text to offer as "使用 “xxx”", or null. */
  create: string | null
  /** Matching options cut by `limit`. */
  hidden: number
}

/**
 * Filters options for the typed text, case-insensitively. Exact matches come first, then
 * prefix, word-start and substring matches; the original order is kept within each rank.
 * An empty query lists every option.
 */
export function filterSuggestions(
  options: readonly NormalizedSuggestOption[],
  query: string,
  opts: SuggestFilterOptions = {},
): SuggestFilterResult {
  const { limit = 100, creatable = true, exclude = [] } = opts
  const skip = new Set(exclude)
  const typed = query.trim()
  const q = typed.toLowerCase()
  const pool = skip.size ? options.filter(o => !skip.has(o.value)) : options

  let matched: NormalizedSuggestOption[]
  if (!q) {
    matched = [...pool]
  }
  else {
    const buckets: NormalizedSuggestOption[][] = [[], [], [], [], []]
    for (const o of pool) {
      const r = optionRank(o, q)
      if (r >= 0)
        buckets[r]!.push(o)
    }
    matched = buckets.flat()
  }

  const create = creatable && typed !== '' && !skip.has(typed) && !options.some(o => o.value === typed)
    ? typed
    : null
  const max = Math.max(0, limit)
  return { items: matched.slice(0, max), create, hidden: Math.max(0, matched.length - max) }
}

/** True when `text` is exactly one of the option values. */
export function isKnownSuggestion(options: readonly NormalizedSuggestOption[], text: string): boolean {
  const t = text.trim()
  return t !== '' && options.some(o => o.value === t)
}

export interface HighlightPart {
  text: string
  match: boolean
}

/** Splits `text` around the first case-insensitive occurrence of `query` for highlighting. */
export function highlightParts(text: string, query: string): HighlightPart[] {
  const q = query.trim().toLowerCase()
  const i = q ? text.toLowerCase().indexOf(q) : -1
  if (i < 0)
    return [{ text, match: false }]
  const parts: HighlightPart[] = []
  if (i > 0)
    parts.push({ text: text.slice(0, i), match: false })
  parts.push({ text: text.slice(i, i + q.length), match: true })
  if (i + q.length < text.length)
    parts.push({ text: text.slice(i + q.length), match: false })
  return parts
}
