// Helpers for reading typed values out of vue-router's `route.query`.

/** First value of a query param as a string ('' when absent). */
export function queryStr(v: unknown): string {
  const s = Array.isArray(v) ? v[0] : v
  return typeof s === 'string' ? s : ''
}

/** Positive integer query param, or `fallback`. */
export function queryInt(v: unknown, fallback: number): number {
  const s = queryStr(v)
  const n = s === '' ? Number.NaN : Number(s)
  return Number.isInteger(n) && n > 0 ? n : fallback
}

export function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError'
}
