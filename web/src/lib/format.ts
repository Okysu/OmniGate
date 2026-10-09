import type { Role, UserStatus } from './types'

export const ROLE_LABELS: Record<Role, string> = {
  system_admin: '系统管理员',
  channel_admin: '渠道管理员',
  user: '普通用户',
  auditor: '审计员',
}

export const ROLE_DESCRIPTIONS: Record<Role, string> = {
  system_admin: '拥有全部权限，可管理用户、系统设置与所有资源。',
  channel_admin: '可管理渠道、模型与路由等网关资源。',
  user: '可使用网关、管理自己的 API Key 与查看自己的用量。',
  auditor: '只读访问审计日志与统计数据，不能修改配置。',
}

export const ROLES: Role[] = ['system_admin', 'channel_admin', 'user', 'auditor']

export const STATUS_LABELS: Record<UserStatus, string> = {
  active: '正常',
  disabled: '已停用',
}

export const PROVIDER_LABELS: Record<string, string> = {
  github: 'GitHub',
  oidc: 'OIDC',
}

const dateTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
})

/** Format an ISO (UTC) timestamp in the viewer's local time zone. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso)
    return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()))
    return iso
  return dateTimeFormatter.format(d)
}

const relativeFormatter = new Intl.RelativeTimeFormat('zh-CN', { numeric: 'auto' })

export function formatRelative(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso)
    return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t))
    return iso
  const diffSec = Math.round((t - now) / 1000)
  const abs = Math.abs(diffSec)
  if (abs < 45)
    return '刚刚'
  if (abs < 3600)
    return relativeFormatter.format(Math.round(diffSec / 60), 'minute')
  if (abs < 86400)
    return relativeFormatter.format(Math.round(diffSec / 3600), 'hour')
  if (abs < 86400 * 30)
    return relativeFormatter.format(Math.round(diffSec / 86400), 'day')
  return formatDateTime(iso)
}

/** Initials for avatar fallbacks (handles CJK names: first character). */
export function initials(name: string | null | undefined): string {
  const n = (name ?? '').trim()
  if (!n)
    return '?'
  const parts = n.split(/\s+/).filter(Boolean)
  if (parts.length >= 2)
    return (parts[0]!.charAt(0) + parts[1]!.charAt(0)).toUpperCase()
  return Array.from(n).slice(0, 2).join('').toUpperCase()
}

/** Very small user-agent summary, e.g. "Chrome · macOS". */
export function describeUserAgent(ua: string): string {
  if (!ua)
    return '未知设备'
  const browser
    = /Edg\//.test(ua)
      ? 'Edge'
      : /Firefox\//.test(ua)
        ? 'Firefox'
        : /Chrome\//.test(ua)
          ? 'Chrome'
          : /Safari\//.test(ua)
            ? 'Safari'
            : /curl\//i.test(ua)
              ? 'curl'
              : null
  const os
    = /Windows/.test(ua)
      ? 'Windows'
      : /iPhone|iPad|iOS/.test(ua)
        ? 'iOS'
        : /Mac OS X|Macintosh/.test(ua)
          ? 'macOS'
          : /Android/.test(ua)
            ? 'Android'
            : /Linux/.test(ua)
              ? 'Linux'
              : null
  const parts = [browser, os].filter((p): p is string => p !== null)
  return parts.length ? parts.join(' · ') : ua.slice(0, 40)
}

const integerFormatter = new Intl.NumberFormat('zh-CN')
const compactFormatter = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 })

/** 1234567 → "1,234,567". */
export function formatNumber(n: number | null | undefined): string {
  return n == null || Number.isNaN(n) ? '—' : integerFormatter.format(n)
}

/** 1234567 → "1.2M" (counts / tokens, never money). */
export function formatCompact(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n))
    return '—'
  return Math.abs(n) < 10000 ? integerFormatter.format(n) : compactFormatter.format(n)
}

/** Milliseconds → "850 ms" / "1.24 s" / "2 分 5 秒". */
export function formatMs(ms: number | null | undefined): string {
  if (ms == null || Number.isNaN(ms))
    return '—'
  if (ms < 1000)
    return `${Math.round(ms)} ms`
  if (ms < 60_000)
    return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
  const s = Math.round(ms / 1000)
  return `${Math.floor(s / 60)} 分 ${s % 60} 秒`
}

/** 0.9876 → "98.8%". */
export function formatPercent(ratio: number | null | undefined, digits = 1): string {
  if (ratio == null || Number.isNaN(ratio))
    return '—'
  return `${(ratio * 100).toFixed(digits)}%`
}
