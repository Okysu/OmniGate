// JSON helpers. Everything mutates in place: request bodies can be several
// MiB (long contexts, base64 images), so nothing here clones the body.

export type JsonObject = Record<string, any>

/** Top-level fields no rewrite may remove or replace (they decide routing, streaming and the conversation). */
export const PROTECTED = ["model", "stream", "messages", "input"]

export function isObject(v: unknown): v is JsonObject {
  return typeof v === "object" && v !== null && !Array.isArray(v)
}

function own(o: JsonObject, k: string): boolean {
  return Object.prototype.hasOwnProperty.call(o, k)
}

/** Keys like "__x__" are never real API parameters; skipping them keeps prototype-special keys out of the body. */
function unsafeKey(k: string): boolean {
  return k.length >= 4 && k.slice(0, 2) === "__" && k.slice(-2) === "__"
}

/** "a.b.c" or the JSON pointer "/a/b/c" → ["a", "b", "c"] (null when empty or unsafe). */
export function splitPath(path: string): string[] | null {
  const p = path.trim()
  if (!p) return null
  const segs = p.charAt(0) === "/"
    ? p.slice(1).split("/").map((s) => s.replace(/~1/g, "/").replace(/~0/g, "~"))
    : p.split(".")
  if (segs.some((s) => s === "" || unsafeKey(s))) return null
  return segs
}

export function isProtected(segs: string[]): boolean {
  return PROTECTED.indexOf(segs[0]) >= 0
}

/** Deletes the field at segs (objects only); missing intermediate objects mean nothing to do. */
export function deletePath(body: JsonObject, segs: string[]): void {
  let cur: JsonObject = body
  for (let i = 0; i < segs.length - 1; i++) {
    const next = own(cur, segs[i]) ? cur[segs[i]] : undefined
    if (!isObject(next)) return
    cur = next
  }
  const last = segs[segs.length - 1]
  if (own(cur, last)) delete cur[last]
}

/** Sets the field at segs, creating (or replacing non-object) intermediate objects. */
export function setPath(body: JsonObject, segs: string[], value: unknown): void {
  let cur: JsonObject = body
  for (let i = 0; i < segs.length - 1; i++) {
    let next = own(cur, segs[i]) ? cur[segs[i]] : undefined
    if (!isObject(next)) {
      next = {}
      cur[segs[i]] = next
    }
    cur = next
  }
  cur[segs[segs.length - 1]] = value
}

/**
 * Deep-merges patch into target: objects merge recursively, arrays and
 * scalars (including null) replace. At the top level the PROTECTED fields
 * are ignored.
 */
export function deepMerge(target: JsonObject, patch: JsonObject, top: boolean): void {
  for (const k of Object.keys(patch)) {
    if (unsafeKey(k) || (top && PROTECTED.indexOf(k) >= 0)) continue
    const pv = patch[k]
    const tv = own(target, k) ? target[k] : undefined
    if (isObject(pv) && isObject(tv)) deepMerge(tv, pv, false)
    else target[k] = pv
  }
}

/** JSON.parse that reports failure instead of throwing. */
export function parseJSON(text: string): { ok: true; value: any } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(text) }
  } catch {
    return { ok: false }
  }
}
