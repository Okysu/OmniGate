// "模型资料" (model info, phase5-api.md §2): form state, client-side validation with
// the contract limits, PUT body, and the merge of logical models with their info rows.
import type { ModelCapabilities, ModelEntry, ModelInfo, ModelInfoInput, PlazaModel } from './types'
import { NO_CAPABILITIES, normalizeCapabilities, plazaPriceFromSell, protocolsForCapabilities } from './plaza'

export const MODEL_INFO_LIMITS = {
  displayName: 100,
  description: 1000,
  vendor: 50,
  tags: 10,
  tag: 20,
} as const

/** Vendor suggestions (the input accepts anything ≤ 50 characters). */
export const VENDOR_SUGGESTIONS = [
  'OpenAI',
  'Anthropic',
  'Google',
  'DeepSeek',
  '阿里云通义',
  'Moonshot',
  '智谱',
  'MiniMax',
  '字节跳动',
  '腾讯混元',
  '百度文心',
  'xAI',
  'Meta',
  'Mistral',
]

export interface ModelInfoForm {
  displayName: string
  description: string
  vendor: string
  tags: string[]
  /** Text inputs ('' = unknown). */
  contextWindow: string
  maxOutput: string
  capabilities: ModelCapabilities
  hidden: boolean
  sortOrder: string
}

export function emptyModelInfoForm(): ModelInfoForm {
  return {
    displayName: '',
    description: '',
    vendor: '',
    tags: [],
    contextWindow: '',
    maxOutput: '',
    capabilities: { ...NO_CAPABILITIES },
    hidden: false,
    sortOrder: '0',
  }
}

export function formFromModelInfo(info: ModelInfo | null | undefined): ModelInfoForm {
  if (!info)
    return emptyModelInfoForm()
  return {
    displayName: info.displayName ?? '',
    description: info.description ?? '',
    vendor: info.vendor ?? '',
    tags: [...(info.tags ?? [])],
    contextWindow: info.contextWindow ? String(info.contextWindow) : '',
    maxOutput: info.maxOutput ? String(info.maxOutput) : '',
    capabilities: normalizeCapabilities(info.capabilities),
    hidden: !!info.hidden,
    sortOrder: String(info.sortOrder ?? 0),
  }
}

/** Trims, drops empties and case-insensitive duplicates (keeps the first spelling). */
export function normalizeTags(tags: readonly string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of tags) {
    const t = raw.trim()
    const k = t.toLowerCase()
    if (t && !seen.has(k)) {
      seen.add(k)
      out.push(t)
    }
  }
  return out
}

/** Splits pasted / typed text ("a, b，c") into tags. */
export function parseTags(text: string): string[] {
  return normalizeTags(text.split(/[,，;；\n]+/))
}

/** "128k" / "128K" → 128000, "1m" → 1000000, "131072" → 131072; '' → null; NaN when invalid. */
export function parseTokenLimit(v: string): number | null {
  const s = v.replace(/[,，_\s]/g, '')
  if (s === '')
    return null
  const m = /^(\d+(?:\.\d+)?)([kKmM]?)$/.exec(s)
  if (!m)
    return Number.NaN
  const mult = m[2] ? (m[2].toLowerCase() === 'k' ? 1e3 : 1e6) : 1
  const n = Number(m[1]) * mult
  return Number.isInteger(n) ? n : Number.NaN
}

function isSafePositiveInt(n: number | null): boolean {
  return n !== null && Number.isSafeInteger(n) && n > 0
}

/** Client-side checks; keys match the server's 422 `details` (`displayName`, `tags`, …). */
export function validateModelInfo(f: ModelInfoForm): Record<string, string> {
  const e: Record<string, string> = {}
  const L = MODEL_INFO_LIMITS
  if (f.displayName.trim().length > L.displayName)
    e.displayName = `最多 ${L.displayName} 个字符`
  if (f.description.trim().length > L.description)
    e.description = `最多 ${L.description} 个字符`
  if (f.vendor.trim().length > L.vendor)
    e.vendor = `最多 ${L.vendor} 个字符`
  const tags = normalizeTags(f.tags)
  if (tags.length > L.tags)
    e.tags = `最多 ${L.tags} 个标签`
  else if (tags.some(t => t.length > L.tag))
    e.tags = `每个标签最多 ${L.tag} 个字符`
  for (const k of ['contextWindow', 'maxOutput'] as const) {
    const n = parseTokenLimit(f[k])
    if (n !== null && !isSafePositiveInt(n))
      e[k] = '请输入正整数（可用 128K、1M 等写法），或留空'
  }
  const cw = parseTokenLimit(f.contextWindow)
  const mo = parseTokenLimit(f.maxOutput)
  if (!e.contextWindow && !e.maxOutput && cw !== null && mo !== null && mo > cw)
    e.maxOutput = '最大输出不能超过上下文长度'
  const so = f.sortOrder.trim()
  if (!/^-?\d{1,9}$/.test(so))
    e.sortOrder = '请输入整数'
  return e
}

/** PUT body; `version` only when updating an existing row. */
export function buildModelInfoInput(f: ModelInfoForm, version?: number): ModelInfoInput {
  const body: ModelInfoInput = {
    displayName: f.displayName.trim(),
    description: f.description.trim(),
    vendor: f.vendor.trim(),
    tags: normalizeTags(f.tags),
    contextWindow: parseTokenLimit(f.contextWindow),
    maxOutput: parseTokenLimit(f.maxOutput),
    capabilities: { ...f.capabilities },
    hidden: f.hidden,
    sortOrder: Number(f.sortOrder.trim() || '0'),
  }
  if (version !== undefined)
    body.version = version
  return body
}

/** Whether the form differs from the saved row (or from an empty form when there is none). */
export function isModelInfoDirty(f: ModelInfoForm, saved: ModelInfo | null | undefined): boolean {
  return JSON.stringify(buildModelInfoInput(f)) !== JSON.stringify(buildModelInfoInput(formFromModelInfo(saved)))
}

// ---------------------------------------------------------------------------
// Table rows: logical models ∪ info rows
// ---------------------------------------------------------------------------

export interface ModelInfoRow {
  model: string
  /** Channels serving the model (0 = info row for a model no channel provides). */
  channels: number
  entry: ModelEntry | null
  info: ModelInfo | null
}

/** Every logical model plus orphan info rows, ordered like the plaza (sortOrder, then name). */
export function mergeModelInfo(models: readonly ModelEntry[], infos: readonly ModelInfo[]): ModelInfoRow[] {
  const byModel = new Map<string, ModelInfoRow>()
  for (const e of models)
    byModel.set(e.model, { model: e.model, channels: e.channels, entry: e, info: null })
  for (const i of infos) {
    const row = byModel.get(i.model)
    if (row)
      row.info = i
    else
      byModel.set(i.model, { model: i.model, channels: 0, entry: null, info: i })
  }
  return [...byModel.values()].sort((a, b) =>
    (a.info?.sortOrder ?? 0) - (b.info?.sortOrder ?? 0) || a.model.localeCompare(b.model))
}

/** The plaza card the form would produce (admin "在广场中预览"; plans / protocols unknown here). */
export function previewPlazaModel(model: string, f: ModelInfoForm, entry: ModelEntry | null): PlazaModel {
  const cw = parseTokenLimit(f.contextWindow)
  const mo = parseTokenLimit(f.maxOutput)
  return {
    model,
    displayName: f.displayName.trim(),
    description: f.description.trim(),
    vendor: f.vendor.trim(),
    tags: normalizeTags(f.tags),
    contextWindow: isSafePositiveInt(cw) ? cw : null,
    maxOutput: isSafePositiveInt(mo) ? mo : null,
    capabilities: { ...f.capabilities },
    protocols: protocolsForCapabilities(f.capabilities),
    price: plazaPriceFromSell(entry?.price),
    plans: [],
  }
}
