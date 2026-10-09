// URL layout of the SPA: the public landing page lives at `/`, the login page at
// `/login`, and every authenticated console page under `/console`. Pages used to
// live at the top level (`/channels`, `/billing/plans`, …); `legacyRedirects`
// keeps those bookmarks working.
import type { RouteLocationNormalized, RouteRecordRaw } from 'vue-router'

/** Prefix of every authenticated console page. */
export const CONSOLE_BASE = '/console'

/** Landing page (public). */
export const LANDING_PATH = '/'

/** Public model plaza (landing site). */
export const PUBLIC_PLAZA_PATH = '/models'

/**
 * Top-level public pages that shadow a console child of the same name (so no
 * legacy redirect is generated for them): `/models` used to redirect to
 * `/console/models` and is the public model plaza now.
 */
export const PUBLIC_PATHS: readonly string[] = [PUBLIC_PLAZA_PATH]

/** `consolePath('/channels')` → `/console/channels`; `consolePath()` → `/console`. */
export function consolePath(sub = ''): string {
  const s = sub.replace(/^\/+/, '')
  return s ? `${CONSOLE_BASE}/${s}` : CONSOLE_BASE
}

/** True for `/console` and anything below it. */
export function isConsolePath(path: string): boolean {
  return path === CONSOLE_BASE || path.startsWith(`${CONSOLE_BASE}/`) || path.startsWith(`${CONSOLE_BASE}?`) || path.startsWith(`${CONSOLE_BASE}#`)
}

/**
 * Only allow same-origin, path-absolute redirects (no `//evil.com`, no schemes, not
 * the login page itself, not the API). Mirrors the backend's `auth.SafeRedirect`.
 */
export function safeRedirect(value: unknown, fallback = CONSOLE_BASE): string {
  const v = Array.isArray(value) ? value[0] : value
  // Same-origin absolute paths only. Backslashes, whitespace and control
  // characters are rejected anywhere: browsers treat "\\" as "/", so values
  // like "/./\\evil.com" would normalize into a protocol-relative URL.
  // eslint-disable-next-line no-control-regex
  if (typeof v !== 'string' || v.length > 2048 || !v.startsWith('/') || v.startsWith('//') || /[\\\s\u0000-\u001f\u007f]/.test(v))
    return fallback
  // Validate the normalized form, i.e. what the browser would actually open.
  let u: URL
  try {
    u = new URL(v, 'http://omnigate.invalid')
  }
  catch {
    return fallback
  }
  if (u.origin !== 'http://omnigate.invalid' || u.pathname.startsWith('//'))
    return fallback
  const p = u.pathname
  if (p === '/login' || p === '/api' || p.startsWith('/api/'))
    return fallback
  return p + u.search + u.hash
}

/** Login page URL that comes back to `redirect` afterwards. */
export function loginLocation(redirect: string = CONSOLE_BASE): { name: 'login', query: { redirect: string } } {
  return { name: 'login', query: { redirect } }
}

/** Where an old top-level URL now lives: `/channels/x?a=1#h` → `/console/channels/x?a=1#h`. */
export function legacyTarget(to: Pick<RouteLocationNormalized, 'path' | 'query' | 'hash'>): { path: string, query: RouteLocationNormalized['query'], hash: string } {
  return { path: consolePath(to.path), query: to.query, hash: to.hash }
}

/**
 * Top-level redirects for every console child route (`channels`, `channels/:id`,
 * `plugins/:id/edit`, …) so URLs from before the `/console` prefix keep working.
 * Params, query and hash are preserved. The overview (empty path) and catch-all
 * routes are skipped: `/` is the landing page now. `exclude` lists top-level public
 * paths (e.g. `/models`) that must not become redirects.
 */
export function legacyRedirects(children: readonly Pick<RouteRecordRaw, 'path'>[], exclude: readonly string[] = []): RouteRecordRaw[] {
  const seen = new Set<string>(exclude.map(p => p.replace(/^\/+/, '')))
  const out: RouteRecordRaw[] = []
  for (const child of children) {
    const path = child.path.replace(/^\/+/, '')
    if (!path || path.startsWith(':pathMatch') || seen.has(path))
      continue
    seen.add(path)
    out.push({ path: `/${path}`, redirect: to => legacyTarget(to) })
  }
  return out
}
