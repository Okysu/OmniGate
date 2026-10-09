// Model plaza helpers (phase5-api.md §3): response normalisation, search / filter /
// sort, token-count formatting and the price calculator. Pure functions; prices are
// decimal strings and are only ever combined with BigInt arithmetic (lib/money.ts).
import type { CurrencyInfo } from './money'
import type { ChannelTier, ModelCapabilities, ModelCapability, MyPlazaModel, PlazaCurrency, PlazaModel, PlazaPrice, Price, PriceSchedulePeriod } from './types'
import { inferBillingMode } from './billingMode'
import { amountSign, fromNano, isValidAmount, toNano } from './money'
import { normalizePlazaTiers, resolveTiers } from './priceTiers'

// ---------------------------------------------------------------------------
// Normalisation (defensive against nulls from the Go backend)
// ---------------------------------------------------------------------------

export const NO_CAPABILITIES: ModelCapabilities = { vision: false, tools: false, reasoning: false, embedding: false, imageGeneration: false, audioInput: false, audioOutput: false }

type Loose<T> = { [K in keyof T]?: T[K] | null }

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}
function intOrNull(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? Math.floor(v) : null
}
function strList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x !== '') : []
}

export function normalizeCapabilities(c: Loose<ModelCapabilities> | null | undefined): ModelCapabilities {
  return {
    vision: c?.vision === true,
    tools: c?.tools === true,
    reasoning: c?.reasoning === true,
    embedding: c?.embedding === true,
    imageGeneration: c?.imageGeneration === true,
    // phase9 §1: absent on older backends.
    audioInput: c?.audioInput === true,
    audioOutput: c?.audioOutput === true,
  }
}

/** phase9 §1.1 audio prices (optional; only copied when sent as strings). */
export const AUDIO_PRICE_KEYS = ['audioInputPerM', 'audioOutputPerM', 'perMinute', 'perMCharacters'] as const
export type AudioPriceKey = typeof AUDIO_PRICE_KEYS[number]

function copyAudioPrices(from: { [K in AudioPriceKey]?: string | null }, to: PlazaPrice): void {
  for (const k of AUDIO_PRICE_KEYS) {
    const v = from[k]
    if (typeof v === 'string')
      to[k] = v
  }
}

/** A set, non-zero amount (`perMinute` / `perMCharacters` of 0 mean "not charged"). */
export function isChargedAmount(v: string | null | undefined): v is string {
  return typeof v === 'string' && isValidAmount(v) && amountSign(v) > 0
}

/** True when the price bills audio by duration or characters (per-minute / per-1M-characters set and non-zero). */
export function hasAudioUnitPrice(p: { perMinute?: string | null, perMCharacters?: string | null } | null | undefined): boolean {
  return !!p && (isChargedAmount(p.perMinute) || isChargedAmount(p.perMCharacters))
}

/** True when any phase9 audio price is set (audio token prices, or a non-zero per-minute / per-character price). */
export function hasAudioPrice(p: { [K in AudioPriceKey]?: string | null } | null | undefined): boolean {
  return !!p && (typeof p.audioInputPerM === 'string' || typeof p.audioOutputPerM === 'string' || hasAudioUnitPrice(p))
}

function normalizePrice(p: Loose<PlazaPrice> | null | undefined): PlazaPrice | null {
  if (!p || typeof p.inputPerM !== 'string' || typeof p.outputPerM !== 'string')
    return null
  const out: PlazaPrice = {
    inputPerM: p.inputPerM,
    outputPerM: p.outputPerM,
    cacheReadPerM: typeof p.cacheReadPerM === 'string' ? p.cacheReadPerM : null,
    cacheWritePerM: typeof p.cacheWritePerM === 'string' ? p.cacheWritePerM : null,
  }
  if (typeof p.perRequest === 'string')
    out.perRequest = p.perRequest
  // Image prices are not pinned by the contract for the plaza: kept only when sent.
  if (typeof p.perImage === 'string')
    out.perImage = p.perImage
  if (typeof p.imageInputPerM === 'string')
    out.imageInputPerM = p.imageInputPerM
  copyAudioPrices(p, out)
  // phase8 §3: time-of-day schedule and the multiplier in effect now.
  const schedule = normalizeSchedule(p.schedule)
  if (schedule.length) {
    out.schedule = schedule
    if (typeof p.scheduleTimezone === 'string' && p.scheduleTimezone)
      out.scheduleTimezone = p.scheduleTimezone
  }
  if (typeof p.currentMultiplier === 'string' && isValidAmount(p.currentMultiplier))
    out.currentMultiplier = p.currentMultiplier
  // phase10 §1: resolved context-length tiers (absent on older backends).
  const tiers = normalizePlazaTiers(p.tiers)
  if (tiers.length)
    out.tiers = tiers
  return out
}

/** Valid periods of a schedule (anything malformed is dropped). */
export function normalizeSchedule(v: unknown): PriceSchedulePeriod[] {
  if (!Array.isArray(v))
    return []
  return v
    .filter((x): x is PriceSchedulePeriod => !!x && typeof x === 'object' && typeof x.start === 'string' && typeof x.end === 'string' && typeof x.multiplier === 'string')
    .map(x => ({ days: Array.isArray(x.days) ? x.days.filter(d => Number.isInteger(d) && d >= 0 && d <= 6) : [], start: x.start, end: x.end, multiplier: x.multiplier }))
}

export function normalizePlazaModel(raw: Loose<PlazaModel>): PlazaModel {
  return {
    model: str(raw.model),
    displayName: str(raw.displayName),
    description: str(raw.description),
    vendor: str(raw.vendor),
    tags: strList(raw.tags),
    contextWindow: intOrNull(raw.contextWindow),
    maxOutput: intOrNull(raw.maxOutput),
    capabilities: normalizeCapabilities(raw.capabilities),
    protocols: strList(raw.protocols),
    price: normalizePrice(raw.price),
    plans: Array.isArray(raw.plans) ? raw.plans.filter(p => p && typeof p.id === 'string').map(p => ({ id: p.id, name: str(p.name) || p.id })) : [],
  }
}

function count(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) && v > 0 ? Math.floor(v) : 0
}

export function normalizeMyPlazaModel(raw: Loose<MyPlazaModel>): MyPlazaModel {
  const base = normalizePlazaModel(raw)
  const sources = { own: count(raw.sources?.own), shared: count(raw.sources?.shared), platform: count(raw.sources?.platform) }
  const billing = raw.billing === 'free' || raw.billing === 'platform'
    ? raw.billing
    : (sources.own + sources.shared > 0 ? 'free' : 'platform')
  const sub = raw.subscription
  const out: MyPlazaModel = {
    ...base,
    sources,
    billing,
    subscription: sub && typeof sub.id === 'string' ? { id: sub.id, planName: str(sub.planName) || '套餐' } : null,
  }
  // phase8 §1.1: `price` already includes the group multiplier; `basePrice` is the list price.
  if (raw.basePrice !== undefined)
    out.basePrice = normalizePrice(raw.basePrice)
  if (typeof raw.priceMultiplier === 'string' && isValidAmount(raw.priceMultiplier))
    out.priceMultiplier = raw.priceMultiplier
  return out
}

export function isMyPlazaModel(m: PlazaModel): m is MyPlazaModel {
  return 'sources' in m && 'billing' in m
}

/**
 * Currency for plaza prices. The contract's `currency` field may be the system-info
 * object or a bare ISO code; the system currency (same settlement currency) supplies
 * whatever is missing.
 */
export function plazaCurrency(value: PlazaCurrency | null | undefined, fallback: CurrencyInfo | null | undefined): CurrencyInfo | null {
  const code = typeof value === 'string' ? value : value?.code
  if (!code)
    return fallback ?? null
  if (fallback && fallback.code === code)
    return { ...fallback, ...(typeof value === 'object' && value ? pickCurrency(value) : {}) }
  const obj = typeof value === 'object' && value ? value : null
  return {
    code,
    symbol: obj?.symbol ?? `${code} `,
    decimals: typeof obj?.decimals === 'number' ? obj.decimals : 2,
  }
}

function pickCurrency(v: { symbol?: string, decimals?: number }): Partial<CurrencyInfo> {
  const out: Partial<CurrencyInfo> = {}
  if (typeof v.symbol === 'string')
    out.symbol = v.symbol
  if (typeof v.decimals === 'number')
    out.decimals = v.decimals
  return out
}

// ---------------------------------------------------------------------------
// Labels
// ---------------------------------------------------------------------------

export interface CapabilityMeta {
  key: ModelCapability
  label: string
  description: string
}

export const CAPABILITIES: CapabilityMeta[] = [
  { key: 'vision', label: '视觉', description: '支持图片输入（多模态）' },
  { key: 'tools', label: '工具调用', description: '支持 function calling / tool use' },
  { key: 'reasoning', label: '推理', description: '支持深度思考（推理过程）' },
  { key: 'embedding', label: '嵌入', description: '向量嵌入模型（Embeddings 接口）' },
  { key: 'imageGeneration', label: '图片生成', description: '支持图片生成 / 编辑（Images 接口）' },
  { key: 'audioInput', label: '语音识别', description: '音频输入：语音转写 / 翻译（/v1/audio/transcriptions、/v1/audio/translations）' },
  { key: 'audioOutput', label: '语音合成', description: '音频输出：文字转语音（/v1/audio/speech）' },
]

/** Short badge text per client protocol. */
export const PROTOCOL_LABELS: Record<string, string> = {
  'openai.chat': 'OpenAI Chat',
  'openai.responses': 'Responses',
  'anthropic.messages': 'Anthropic',
  'openai.embeddings': 'Embeddings',
  'openai.images': 'Images',
  'openai.audio': 'Audio',
}

/** Full protocol description (tooltips, filter options). */
export const PROTOCOL_DESCRIPTIONS: Record<string, string> = {
  'openai.chat': 'OpenAI Chat Completions（/v1/chat/completions）',
  'openai.responses': 'OpenAI Responses（/v1/responses）',
  'anthropic.messages': 'Anthropic Messages（/v1/messages）',
  'openai.embeddings': 'OpenAI Embeddings（/v1/embeddings）',
  'openai.images': 'OpenAI Images（/v1/images/generations、/v1/images/edits）',
  'openai.audio': 'OpenAI Audio（/v1/audio/transcriptions、/v1/audio/translations、/v1/audio/speech）',
}

const PROTOCOL_ORDER = ['openai.chat', 'openai.responses', 'anthropic.messages', 'openai.embeddings', 'openai.images', 'openai.audio']

export function sortProtocols(list: readonly string[]): string[] {
  const rank = (p: string) => {
    const i = PROTOCOL_ORDER.indexOf(p)
    return i < 0 ? PROTOCOL_ORDER.length : i
  }
  return [...new Set(list)].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))
}

export const TIER_LABELS: Record<ChannelTier, string> = {
  own: '自有',
  shared: '共享',
  platform: '平台',
}

/** Display name, falling back to the model name. */
export function displayNameOf(m: Pick<PlazaModel, 'model' | 'displayName'>): string {
  return m.displayName.trim() || m.model
}

/**
 * Token counts in K / M: 128000 → "128K", 131072 → "128K", 1048576 → "1M",
 * 1500000 → "1.5M", 8192 → "8K", 500 → "500". Multiples of 1024 that are not round
 * thousands (131072, 65536) use 1024 steps, everything else 1000. `null` → "—".
 */
export function formatTokenCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n) || n <= 0)
    return '—'
  const binary = n % 1000 !== 0 && n % 1024 === 0
  const k = binary ? 1024 : 1000
  const trim = (x: number) => (Math.round(x * 10) / 10).toString()
  if (n >= k * k)
    return `${trim(n / (k * k))}M`
  if (n >= k)
    return `${trim(n / k)}K`
  return String(n)
}

// ---------------------------------------------------------------------------
// Search, filters, sorting
// ---------------------------------------------------------------------------

export type PlazaSort = 'default' | 'price' | 'context'
export type SourceFilter = 'all' | ChannelTier

export const SORT_LABELS: Record<PlazaSort, string> = {
  default: '默认排序',
  price: '价格从低到高',
  context: '上下文长度',
}

export const SOURCE_FILTER_LABELS: Record<SourceFilter, string> = {
  all: '全部来源',
  own: '自有',
  shared: '共享',
  platform: '平台',
}

export interface PlazaFilters {
  /** Free text over model, displayName, vendor and tags. */
  q: string
  /** '' = any vendor. */
  vendor: string
  /** Every listed capability is required. */
  capabilities: ModelCapability[]
  /** '' = any protocol. */
  protocol: string
  /** Only models covered by at least one plan (platform plaza) / a subscription (mine). */
  coveredOnly: boolean
  /** "我的模型" only. */
  source: SourceFilter
}

export const EMPTY_FILTERS: PlazaFilters = { q: '', vendor: '', capabilities: [], protocol: '', coveredOnly: false, source: 'all' }

export function hasActiveFilters(f: PlazaFilters): boolean {
  return f.q.trim() !== '' || f.vendor !== '' || f.capabilities.length > 0 || f.protocol !== '' || f.coveredOnly || f.source !== 'all'
}

/** Case-insensitive; every whitespace-separated term must match somewhere. */
export function matchesQuery(m: PlazaModel, q: string): boolean {
  const terms = q.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (!terms.length)
    return true
  const hay = [m.model, m.displayName, m.vendor, ...m.tags].join('\n').toLowerCase()
  return terms.every(t => hay.includes(t))
}

/** Covered by a plan: any active plan for the platform plaza; also a live subscription for "mine". */
export function isCovered(m: PlazaModel): boolean {
  return m.plans.length > 0 || (isMyPlazaModel(m) && m.subscription !== null)
}

export function filterPlaza<T extends PlazaModel>(items: readonly T[], f: PlazaFilters): T[] {
  return items.filter((m) => {
    if (!matchesQuery(m, f.q))
      return false
    if (f.vendor && m.vendor !== f.vendor)
      return false
    if (f.capabilities.some(c => !m.capabilities[c]))
      return false
    if (f.protocol && !m.protocols.includes(f.protocol))
      return false
    if (f.coveredOnly && !isCovered(m))
      return false
    if (f.source !== 'all' && !(isMyPlazaModel(m) && m.sources[f.source] > 0))
      return false
    return true
  })
}

function safeNano(v: string | null | undefined): bigint | null {
  if (v == null)
    return null
  try {
    return toNano(v)
  }
  catch {
    return null
  }
}

/**
 * Sort key for "价格从低到高": input + output price per 1M; unpriced models and
 * prices not billed by tokens (按次 / 按张 / 按时长, not comparable) sort last.
 */
export function priceKey(p: PlazaPrice | null): bigint | null {
  if (!p || !['token', 'custom'].includes(inferBillingMode(p)))
    return null
  const i = safeNano(p.inputPerM)
  const o = safeNano(p.outputPerM)
  return i === null || o === null ? null : i + o
}

/**
 * `default` keeps the server order (sortOrder, then model name); `price` ascends by
 * input + output price (unpriced last); `context` descends by context window (unknown last).
 * Stable: ties keep the server order.
 */
export function sortPlaza<T extends PlazaModel>(items: readonly T[], sort: PlazaSort): T[] {
  const indexed = items.map((m, i) => ({ m, i }))
  if (sort === 'price') {
    indexed.sort((a, b) => {
      const pa = priceKey(a.m.price)
      const pb = priceKey(b.m.price)
      if (pa === null || pb === null)
        return pa === pb ? a.i - b.i : (pa === null ? 1 : -1)
      return pa < pb ? -1 : pa > pb ? 1 : a.i - b.i
    })
  }
  else if (sort === 'context') {
    indexed.sort((a, b) => {
      const ca = a.m.contextWindow
      const cb = b.m.contextWindow
      if (ca === null || cb === null)
        return ca === cb ? a.i - b.i : (ca === null ? 1 : -1)
      return cb - ca || a.i - b.i
    })
  }
  return indexed.map(x => x.m)
}

/** Distinct non-empty vendors, alphabetically. */
export function vendorsOf(items: readonly PlazaModel[]): string[] {
  return [...new Set(items.map(m => m.vendor.trim()).filter(Boolean))].sort((a, b) => a.localeCompare(b, 'zh-CN'))
}

export function protocolsOf(items: readonly PlazaModel[]): string[] {
  return sortProtocols(items.flatMap(m => m.protocols))
}

// ---------------------------------------------------------------------------
// Price calculator
// ---------------------------------------------------------------------------

const MILLION = 1_000_000n

/** Parses a token-count input ("12,000", " 5000 ") into a non-negative integer; null when invalid. */
export function parseTokenInput(v: string | number | null | undefined): number | null {
  if (v === null || v === undefined)
    return null
  const s = String(v).replace(/[,，_\s]/g, '')
  if (s === '')
    return 0
  if (!/^\d{1,15}$/.test(s))
    return null
  return Number(s)
}

/**
 * Estimated cost of `input` + `output` tokens at a per-1M price (exact decimal string,
 * rounded half-up at 9 decimals). `null` when the model is unpriced or a count is invalid.
 */
export function estimateCost(price: PlazaPrice | null, input: number | null, output: number | null): string | null {
  if (!price || input === null || output === null)
    return null
  const i = safeNano(price.inputPerM)
  const o = safeNano(price.outputPerM)
  if (i === null || o === null)
    return null
  const total = i * BigInt(input) + o * BigInt(output)
  return fromNano((total + MILLION / 2n) / MILLION)
}

/**
 * Estimated cost of `requests` calls at a per-call / per-image price: requests ×
 * (perRequest + imagesPerRequest × perImage), exact decimal string. `null` when
 * the model is unpriced or a count is invalid.
 */
export function estimateCallCost(price: PlazaPrice | null, requests: number | null, imagesPerRequest = 0): string | null {
  if (!price || requests === null || !Number.isInteger(imagesPerRequest) || imagesPerRequest < 0)
    return null
  const perRequest = safeNano(price.perRequest ?? '0') ?? 0n
  const perImage = safeNano(price.perImage ?? '0') ?? 0n
  return fromNano(BigInt(requests) * (perRequest + BigInt(imagesPerRequest) * perImage))
}

// ---------------------------------------------------------------------------
// "我的模型"
// ---------------------------------------------------------------------------

export interface SourceEntry {
  tier: ChannelTier
  label: string
  count: number
}

/** Non-zero tiers in routing order (own → shared → platform). */
export function sourceEntries(m: MyPlazaModel): SourceEntry[] {
  return (['own', 'shared', 'platform'] as const)
    .filter(t => m.sources[t] > 0)
    .map(t => ({ tier: t, label: TIER_LABELS[t], count: m.sources[t] }))
}

/** Billing badge text for a free model: which tier is tried first. */
export function freeBillingLabel(m: MyPlazaModel): string {
  return m.sources.own > 0 ? '优先自有渠道 · 不计费' : '优先共享渠道 · 不计费'
}

// ---------------------------------------------------------------------------
// Admin preview: a logical model + its model-info form → a plaza card.
// ---------------------------------------------------------------------------

/** Plaza price from the admin sell price of `/api/models` (cache prices of 0 still shown). */
export function plazaPriceFromSell(p: Pick<Price, 'inputPerM' | 'outputPerM' | 'cacheReadPerM' | 'cacheWritePerM' | 'perImage' | 'imageInputPerM' | AudioPriceKey> & { perRequest?: string | null, tiers?: Price['tiers'] } | null | undefined): PlazaPrice | null {
  if (!p)
    return null
  const out: PlazaPrice = { inputPerM: p.inputPerM, outputPerM: p.outputPerM, cacheReadPerM: p.cacheReadPerM ?? null, cacheWritePerM: p.cacheWritePerM ?? null }
  // Like the plaza: the per-request fee only when charged (null when 0).
  if (isChargedAmount(p.perRequest))
    out.perRequest = p.perRequest
  if (typeof p.perImage === 'string')
    out.perImage = p.perImage
  if (typeof p.imageInputPerM === 'string')
    out.imageInputPerM = p.imageInputPerM
  copyAudioPrices(p, out)
  // Stored tiers inherit unset fields from the base version (the plaza sends them resolved).
  const tiers = resolveTiers(p, p.tiers)
  if (tiers.length)
    out.tiers = tiers
  return out
}

/** True for models served by the Images API (protocol or capability). */
export function isImageModel(m: Pick<PlazaModel, 'protocols' | 'capabilities'>): boolean {
  return m.protocols.includes('openai.images') || m.capabilities.imageGeneration
}

/** phase9 §1: audio models (transcription / translation and / or speech). */
export interface AudioModes {
  /** `/v1/audio/transcriptions` + `/v1/audio/translations` (audioInput). */
  transcription: boolean
  /** `/v1/audio/speech` (audioOutput). */
  speech: boolean
}

/**
 * Which audio endpoints a model serves. Capabilities decide; reported protocols
 * without `openai.audio` (no OpenAI-compatible channel) mean none, and the
 * protocol with neither capability set means both.
 */
export function audioModes(m: Pick<PlazaModel, 'protocols' | 'capabilities'>): AudioModes {
  const hasProtocol = m.protocols.includes('openai.audio')
  if (m.protocols.length > 0 && !hasProtocol)
    return { transcription: false, speech: false }
  const transcription = m.capabilities.audioInput
  const speech = m.capabilities.audioOutput
  if (!transcription && !speech && hasProtocol)
    return { transcription: true, speech: true }
  return { transcription, speech }
}

/**
 * Client protocols of a model from its capabilities — mirrors the backend
 * (server/internal/plaza `clientProtocols`): chat models (tools / vision /
 * reasoning, or nothing special marked) get Chat, Responses and Messages;
 * embeddings / images / audio only when marked and an OpenAI-compatible
 * channel serves the model.
 */
export function protocolsForCapabilities(caps: ModelCapabilities, openaiChannel = true): string[] {
  const special = caps.embedding || caps.imageGeneration || caps.audioInput || caps.audioOutput
  const out: string[] = []
  if (caps.tools || caps.vision || caps.reasoning || !special)
    out.push('openai.chat', 'openai.responses', 'anthropic.messages')
  if (openaiChannel) {
    if (caps.embedding)
      out.push('openai.embeddings')
    if (caps.imageGeneration)
      out.push('openai.images')
    if (caps.audioInput || caps.audioOutput)
      out.push('openai.audio')
  }
  return out
}
