// Human-readable plugin permissions. Keys use the server's flattened form
// (server/internal/plugin/manifest.go Permissions.flatten), which is also what
// `permissionDiff.added/removed` contain: network:<host>, secret:<name>,
// schedule:<capability>, storage:<n>keys/<n>B, dangerous:<name>.
import type { PermissionDiff, PluginPermissions } from './types'
import { CAPABILITY_LABELS } from './labels'

export type PermissionCategory = 'network' | 'secret' | 'schedule' | 'storage' | 'dangerous' | 'other'

export interface PermissionItem {
  /** Flattened key, e.g. `network:$baseUrl`. */
  key: string
  category: PermissionCategory
  /** Short human description. */
  label: string
  /** Extra explanation (optional). */
  detail?: string
}

export const PERMISSION_CATEGORY_LABELS: Record<PermissionCategory, string> = {
  network: '网络访问',
  secret: '密钥',
  schedule: '定时执行',
  storage: '存储',
  dangerous: '危险权限',
  other: '其他',
}

/** Flattens permissions exactly like the server (sorted). */
export function flattenPermissions(p: PluginPermissions | null | undefined): string[] {
  if (!p)
    return []
  const out: string[] = []
  for (const h of p.network ?? []) out.push(`network:${h}`)
  for (const s of p.secrets ?? []) out.push(`secret:${s}`)
  for (const s of p.schedule ?? []) out.push(`schedule:${s}`)
  if (p.storage)
    out.push(`storage:${p.storage.maxKeys}keys/${p.storage.maxBytes}B`)
  for (const d of p.dangerous ?? []) out.push(`dangerous:${d}`)
  return out.sort()
}

export function formatBytes(n: number): string {
  if (n < 1024)
    return `${n} B`
  if (n < 1024 * 1024)
    return `${Number.isInteger(n / 1024) ? n / 1024 : (n / 1024).toFixed(1)} KiB`
  return `${Number.isInteger(n / 1048576) ? n / 1048576 : (n / 1048576).toFixed(1)} MiB`
}

function capabilityName(name: string): string {
  const label = CAPABILITY_LABELS[name]
  return label ? `${label}（${name}）` : name
}

/** Describes one flattened permission key. */
export function describePermission(key: string): PermissionItem {
  const i = key.indexOf(':')
  const kind = i < 0 ? key : key.slice(0, i)
  const value = i < 0 ? '' : key.slice(i + 1)
  switch (kind) {
    case 'network':
      if (value === '$baseUrl')
        return { key, category: 'network', label: '渠道 Base URL 所在主机', detail: '随渠道配置变化：插件只能访问该渠道 Base URL 的主机。' }
      if (value.startsWith('*.'))
        return { key, category: 'network', label: `${value.slice(2)} 的所有子域名`, detail: `匹配 ${value}` }
      return { key, category: 'network', label: value }
    case 'secret':
      if (value === 'apiKey')
        return { key, category: 'secret', label: '渠道主 API Key', detail: '仅以不透明句柄形式提供，由宿主在发送请求时替换，插件代码无法读取明文。' }
      return { key, category: 'secret', label: `插件密钥「${value}」`, detail: '仅以不透明句柄形式提供，插件代码无法读取明文。' }
    case 'schedule':
      return { key, category: 'schedule', label: capabilityName(value), detail: '宿主按 manifest 声明的最小间隔在后台定时执行。' }
    case 'storage': {
      const m = /^(\d+)keys\/(\d+)B$/.exec(value)
      if (m)
        return { key, category: 'storage', label: `最多 ${m[1]} 个键，共 ${formatBytes(Number(m[2]))}`, detail: '按（插件，渠道）隔离的键值存储。' }
      return { key, category: 'storage', label: value }
    }
    case 'dangerous':
      return { key, category: 'dangerous', label: value }
    default:
      return { key, category: 'other', label: key }
  }
}

export function describePermissions(p: PluginPermissions | null | undefined): PermissionItem[] {
  return flattenPermissions(p).map(describePermission)
}

export type DiffStatus = 'added' | 'removed' | 'unchanged'

export interface PermissionDiffItem extends PermissionItem {
  status: DiffStatus
}

/**
 * Merges the current permission list with the server's diff (relative to the
 * last approved version): every current permission is `added` or `unchanged`,
 * plus `removed` entries for permissions that were dropped.
 */
export function permissionDiffItems(current: PluginPermissions | null | undefined, diff: PermissionDiff | null | undefined): PermissionDiffItem[] {
  const added = new Set(diff?.added ?? [])
  const items: PermissionDiffItem[] = flattenPermissions(current).map(k => ({ ...describePermission(k), status: added.has(k) ? 'added' : 'unchanged' }))
  for (const k of diff?.removed ?? [])
    items.push({ ...describePermission(k), status: 'removed' })
  return items
}

/** Groups items by category in a fixed order. */
export function groupPermissions<T extends PermissionItem>(items: T[]): Array<{ category: PermissionCategory, label: string, items: T[] }> {
  const order: PermissionCategory[] = ['network', 'secret', 'schedule', 'storage', 'dangerous', 'other']
  return order
    .map(category => ({ category, label: PERMISSION_CATEGORY_LABELS[category], items: items.filter(x => x.category === category) }))
    .filter(g => g.items.length > 0)
}
