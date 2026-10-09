// Site branding from `/api/system/info` (Round 5 system settings) with fallbacks for
// older backends that do not send the new fields yet.
import type { SystemInfo } from './types'

export const DEFAULT_SITE_NAME = 'OmniGate'

type InfoLike = Pick<SystemInfo, 'name' | 'siteName' | 'announcement' | 'landingEnabled' | 'docsUrl' | 'publicModelPlaza'> | null | undefined

/** Display name of this deployment: `siteName`, else "OmniGate". */
export function siteNameOf(info: InfoLike): string {
  const s = typeof info?.siteName === 'string' ? info.siteName.trim() : ''
  return s || DEFAULT_SITE_NAME
}

/** Console announcement ('' = none). */
export function announcementOf(info: InfoLike): string {
  return typeof info?.announcement === 'string' ? info.announcement.trim() : ''
}

/** False only when the backend explicitly disables the landing page. */
export function landingEnabledOf(info: InfoLike): boolean {
  return info?.landingEnabled !== false
}

/** Anonymous visitors may browse `/models` (default true, also for older backends). */
export function publicModelPlazaOf(info: InfoLike): boolean {
  return info?.publicModelPlaza !== false
}

/** True for absolute http(s) URLs. */
export function isHttpUrl(value: string): boolean {
  try {
    const u = new URL(value)
    return u.protocol === 'http:' || u.protocol === 'https:'
  }
  catch {
    return false
  }
}

/** Documentation link: the runtime `docsUrl` setting, else the build-time fallback. */
export function docsUrlOf(info: InfoLike, fallback: string | null): string | null {
  const s = typeof info?.docsUrl === 'string' ? info.docsUrl.trim() : ''
  if (s && isHttpUrl(s))
    return s
  return fallback || null
}

/** `document.title` for a page: "渠道 · 站点名" / "站点名". */
export function pageTitle(title: string | undefined, site: string): string {
  return title ? `${title} · ${site}` : site
}

// ---------------------------------------------------------------------------
// Announcement dismissal (remembered per announcement text).
// ---------------------------------------------------------------------------

export const ANNOUNCEMENT_STORAGE_KEY = 'omnigate-announcement-dismissed'

/** Short stable hash (FNV-1a, 32 bit, hex) so the stored value stays small. */
export function textHash(text: string): string {
  let h = 0x811C9DC5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0).toString(16).padStart(8, '0')
}

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>

function defaultStorage(): StorageLike | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  }
  catch {
    return null
  }
}

/** Whether this exact announcement was dismissed before (storage errors → not dismissed). */
export function isAnnouncementDismissed(text: string, storage: StorageLike | null = defaultStorage()): boolean {
  if (!text || !storage)
    return false
  try {
    return storage.getItem(ANNOUNCEMENT_STORAGE_KEY) === textHash(text)
  }
  catch {
    return false
  }
}

/** Remembers the dismissal; a changed announcement shows again. */
export function dismissAnnouncement(text: string, storage: StorageLike | null = defaultStorage()): void {
  if (!text || !storage)
    return
  try {
    storage.setItem(ANNOUNCEMENT_STORAGE_KEY, textHash(text))
  }
  catch {
    // Private mode / blocked storage: dismissed for this page view only.
  }
}
