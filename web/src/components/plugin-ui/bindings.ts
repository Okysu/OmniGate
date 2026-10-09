// Helpers shared by the declarative plugin UI renderer.
import type { BindableResult } from '@/lib/jsonPointer'
import type { UiContribution, UiNode } from '@/lib/types'
import { parseBind } from '@/lib/jsonPointer'

export type ResultMap = Record<string, BindableResult | undefined>

/** Capabilities referenced by any binding in a node tree (in first-seen order). */
export function referencedCapabilities(node: UiNode | null | undefined, out: string[] = []): string[] {
  if (!node)
    return out
  for (const b of [node.bind, node.currencyBind, node.valueBind, node.maxBind, node.rowsBind]) {
    const p = parseBind(b)
    if (p && !out.includes(p.capability))
      out.push(p.capability)
  }
  for (const child of node.items ?? [])
    referencedCapabilities(child, out)
  return out
}

/** Capabilities a contribution depends on: bindings plus action targets. */
export function contributionCapabilities(c: UiContribution): string[] {
  const out = referencedCapabilities(c.component)
  for (const a of c.actions ?? []) {
    if (!out.includes(a.capability))
      out.push(a.capability)
  }
  return out
}

/** Only https links with a host are rendered as links. */
export function safeHttpsUrl(href: string | null | undefined): string | null {
  if (!href)
    return null
  try {
    const u = new URL(href)
    return u.protocol === 'https:' && u.host !== '' ? u.toString() : null
  }
  catch {
    return null
  }
}

/** Splits plain text into paragraphs on blank lines (no HTML is ever produced). */
export function plainParagraphs(text: string | null | undefined): string[] {
  return (text ?? '').split(/\n\s*\n/).map(p => p.trim()).filter(Boolean)
}

/** Ratio for a progress bar from numbers or decimal strings (display only, not money math). */
export function progressRatio(value: unknown, max: unknown): number | null {
  const v = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : Number.NaN
  const m = typeof max === 'number' ? max : typeof max === 'string' ? Number(max) : Number.NaN
  if (!Number.isFinite(v) || !Number.isFinite(m) || m <= 0)
    return null
  return Math.min(1, Math.max(0, v / m))
}
