// RFC 6901 JSON Pointer evaluation and plugin UI bindings
// (`<capability>:<JSON Pointer>`, phase2-api.md §5).

/**
 * Splits a JSON Pointer into unescaped reference tokens.
 * `""` is the whole document (no tokens); any other pointer must start with "/".
 * Returns null for malformed pointers.
 */
export function parsePointer(pointer: string): string[] | null {
  if (pointer === '')
    return []
  if (!pointer.startsWith('/'))
    return null
  const tokens = pointer.slice(1).split('/')
  const out: string[] = []
  for (const t of tokens) {
    // "~" must be followed by 0 or 1.
    if (/~(?![01])/.test(t))
      return null
    // Order matters: ~1 first, then ~0 (so "~01" → "~1", not "/").
    out.push(t.replace(/~1/g, '/').replace(/~0/g, '~'))
  }
  return out
}

const ARRAY_INDEX = /^(?:0|[1-9]\d*)$/

export interface PointerResult {
  found: boolean
  value: unknown
}

/** Evaluates `pointer` against `doc`. Missing members, bad indices and "-" are "not found". */
export function resolvePointer(doc: unknown, pointer: string): PointerResult {
  const tokens = parsePointer(pointer)
  if (tokens === null)
    return { found: false, value: undefined }
  let cur: unknown = doc
  for (const token of tokens) {
    if (Array.isArray(cur)) {
      if (!ARRAY_INDEX.test(token))
        return { found: false, value: undefined }
      const i = Number(token)
      if (i >= cur.length)
        return { found: false, value: undefined }
      cur = cur[i]
    }
    else if (cur !== null && typeof cur === 'object') {
      if (!Object.prototype.hasOwnProperty.call(cur, token))
        return { found: false, value: undefined }
      cur = (cur as Record<string, unknown>)[token]
    }
    else {
      return { found: false, value: undefined }
    }
  }
  return { found: true, value: cur }
}

export interface ParsedBind {
  capability: string
  pointer: string
}

/** `"balance.get:/total"` → `{capability: "balance.get", pointer: "/total"}`; null when malformed. */
export function parseBind(bind: string | null | undefined): ParsedBind | null {
  if (!bind)
    return null
  const i = bind.indexOf(':')
  if (i <= 0)
    return null
  const capability = bind.slice(0, i)
  const pointer = bind.slice(i + 1)
  if (pointer !== '' && !pointer.startsWith('/'))
    return null
  return { capability, pointer }
}

/** The subset of a capability result the renderer needs. */
export interface BindableResult {
  ok: boolean
  unsupported?: boolean
  output: unknown
  error?: string | null
}

export type BindState = 'ok' | 'missing' | 'failed' | 'unsupported' | 'invalid'

export interface BindResult {
  state: BindState
  value: unknown
  capability: string | null
}

/**
 * Resolves a binding against `results[capability].output`.
 * - `missing`: no result yet, or the pointer does not exist / is null
 * - `failed` / `unsupported`: the latest run failed or reported unsupported
 * - `invalid`: the bind string itself is malformed
 */
export function resolveBind(bind: string | null | undefined, results: Record<string, BindableResult | undefined> | null | undefined): BindResult {
  const parsed = parseBind(bind)
  if (!parsed)
    return { state: 'invalid', value: undefined, capability: null }
  const r = results?.[parsed.capability]
  if (!r)
    return { state: 'missing', value: undefined, capability: parsed.capability }
  if (r.unsupported)
    return { state: 'unsupported', value: undefined, capability: parsed.capability }
  if (!r.ok)
    return { state: 'failed', value: undefined, capability: parsed.capability }
  const { found, value } = resolvePointer(r.output, parsed.pointer)
  if (!found || value === null || value === undefined)
    return { state: 'missing', value: undefined, capability: parsed.capability }
  return { state: 'ok', value, capability: parsed.capability }
}
