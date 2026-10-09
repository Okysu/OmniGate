// phase9-api.md §3: plan rule meters provided by billing plugins
// (`custom:<pluginKey>.<meterName>`). Labels and units come from the plugin's
// manifest; they are cached in a small reactive registry so the pure quota
// formatters (rule summaries, usage bars) can render them without every caller
// threading plugin data through.
import type { BillingMeterDecl, BillingMeterInfo, MeterOption, Plugin, PluginManifest, QuotaRule } from './types'
import { shallowReactive } from 'vue'

export const CUSTOM_METER_PREFIX = 'custom:'

/** Meter names (server manifest.go meterNameRe, relaxed: any identifier-like name is displayed). */
const METER_NAME_RE = /^[\w-]{1,64}$/

export interface CustomMeterRef {
  pluginKey: string
  meter: string
}

export interface CustomMeterInfo extends CustomMeterRef {
  /** `custom:<pluginKey>.<meter>` */
  id: string
  label: string
  /** '' when the plugin declares no unit. */
  unit: string
  pluginName?: string
}

export function isCustomMeter(meter: string | null | undefined): boolean {
  return typeof meter === 'string' && meter.startsWith(CUSTOM_METER_PREFIX)
}

export function customMeterId(pluginKey: string, meter: string): string {
  return `${CUSTOM_METER_PREFIX}${pluginKey}.${meter}`
}

/** "custom:community.billing-examples.weighted_tokens" → { pluginKey: "community.billing-examples", meter: "weighted_tokens" }. */
export function parseCustomMeter(meter: string | null | undefined): CustomMeterRef | null {
  if (!isCustomMeter(meter))
    return null
  const rest = (meter as string).slice(CUSTOM_METER_PREFIX.length)
  const dot = rest.lastIndexOf('.')
  if (dot <= 0 || dot === rest.length - 1)
    return null
  const ref = { pluginKey: rest.slice(0, dot), meter: rest.slice(dot + 1) }
  return METER_NAME_RE.test(ref.meter) ? ref : null
}

function normalizeKinds(kind: unknown): string[] {
  if (Array.isArray(kind))
    return kind.filter((k): k is string => typeof k === 'string')
  return typeof kind === 'string' && kind ? [kind] : []
}

/** Plugin kinds; absent = a channel plugin (every plugin before phase9). */
export function pluginKinds(p: { kind?: unknown } | null | undefined): string[] {
  const k = normalizeKinds(p?.kind)
  return k.length ? k : ['channel']
}

export function isBillingPlugin(p: { kind?: unknown } | null | undefined): boolean {
  return pluginKinds(p).includes('billing')
}

/** Whether the plugin can back a channel (billing-only plugins cannot). */
export function isChannelPlugin(p: { kind?: unknown } | null | undefined): boolean {
  return pluginKinds(p).includes('channel')
}

/** Declared meters, sorted by name: from `manifest.billing.meters` or a list item's `meters`. */
export function declaredMeters(src: Pick<PluginManifest, 'billing'> | Pick<Plugin, 'meters'> | null | undefined): { name: string, label: string, unit: string }[] {
  if (!src)
    return []
  const raw: BillingMeterInfo[] | Record<string, BillingMeterDecl> | null | undefined
    = 'billing' in src ? src.billing?.meters : 'meters' in src ? src.meters : null
  if (!raw || typeof raw !== 'object')
    return []
  // `Plugin.meters` is a list of {name, label, unit}; manifests use a name → decl map.
  const entries: [string, BillingMeterDecl | undefined][] = Array.isArray(raw)
    ? raw.filter(m => m && typeof m.name === 'string').map(m => [m.name, m])
    : Object.entries(raw)
  return entries
    .filter(([name]) => METER_NAME_RE.test(name))
    .map(([name, d]) => ({
      name,
      label: typeof d?.label === 'string' && d.label.trim() ? d.label.trim() : name,
      unit: typeof d?.unit === 'string' ? d.unit.trim() : '',
    }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

/** Meter options of one billing plugin. */
export function meterOptions(plugin: { key: string, name?: string }, meters: { name: string, label: string, unit: string }[]): CustomMeterInfo[] {
  return meters.map(m => ({
    id: customMeterId(plugin.key, m.name),
    pluginKey: plugin.key,
    pluginName: plugin.name,
    meter: m.name,
    label: m.label,
    unit: m.unit,
  }))
}

// ---------------------------------------------------------------------------
// Registry (labels for summaries)
// ---------------------------------------------------------------------------

const registry = shallowReactive(new Map<string, CustomMeterInfo>())

export function registerCustomMeters(list: CustomMeterInfo[]): void {
  for (const m of list)
    registry.set(m.id, m)
}

/** Test helper. */
export function clearCustomMeters(): void {
  registry.clear()
}

/**
 * Label and unit of a custom meter: the server's rule snapshot (`meterLabel` /
 * `meterUnit`) first, then the registry, then the bare meter name.
 */
export function customMeterInfo(meter: string, rule?: Pick<QuotaRule, 'meterLabel' | 'meterUnit'> | null): { label: string, unit: string, known: boolean } | null {
  const ref = parseCustomMeter(meter)
  if (!ref && !isCustomMeter(meter))
    return null
  const reg = registry.get(meter)
  const label = rule?.meterLabel?.trim() || reg?.label || ref?.meter || meter.slice(CUSTOM_METER_PREFIX.length)
  const unit = rule?.meterUnit?.trim() ?? reg?.unit ?? ''
  return { label, unit, known: !!(rule?.meterLabel?.trim() || reg) }
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

/**
 * Meter options of every enabled billing plugin with an approved version
 * (phase9 §3: the server validates the same on save). The contract has no
 * meters endpoint, so the meters come from the plugin list (`meters`, when the
 * backend includes it) or the latest approved version's manifest
 * (`billing.meters`), fetched through `fetchManifest`. When the list carries no
 * `kind` at all (older backend shape) every enabled non-builtin plugin's
 * manifest is inspected.
 */
export async function collectMeterOptions(
  plugins: Plugin[],
  fetchManifest: (pluginId: string, versionId: string) => Promise<PluginManifest | null>,
): Promise<CustomMeterInfo[]> {
  const listHasKind = plugins.some(p => p.kind !== undefined && p.kind !== null)
  const candidates = plugins.filter(p =>
    p.status === 'enabled'
    && p.latest !== null
    && p.source !== 'builtin'
    && (listHasKind ? isBillingPlugin(p) : true),
  )
  const results = await Promise.all(candidates.map(async (p) => {
    if (p.meters && typeof p.meters === 'object')
      return meterOptions(p, declaredMeters(p))
    try {
      const m = await fetchManifest(p.id, p.latest!.id)
      if (!m || !isBillingPlugin(m))
        return []
      return meterOptions(p, declaredMeters(m))
    }
    catch {
      return []
    }
  }))
  const out = results.flat()
  registerCustomMeters(out)
  return out
}

/** Custom meters from `GET /api/admin/billing/meters` (built-in entries are skipped). */
export function optionsFromMeterList(list: MeterOption[]): CustomMeterInfo[] {
  const out: CustomMeterInfo[] = []
  for (const m of list) {
    if (m.builtin)
      continue
    const ref = parseCustomMeter(m.meter)
    if (!ref)
      continue
    out.push({ id: m.meter, pluginKey: m.plugin?.key ?? ref.pluginKey, pluginName: m.plugin?.name, meter: ref.meter, label: m.label?.trim() || ref.meter, unit: m.unit?.trim() ?? '' })
  }
  registerCustomMeters(out)
  return out
}
