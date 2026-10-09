import type { RouteLocationNormalized, RouteRecordRedirect } from 'vue-router'
import { describe, expect, it } from 'vitest'
import { CONSOLE_BASE, consolePath, isConsolePath, legacyRedirects, legacyTarget, loginLocation, PUBLIC_PATHS, safeRedirect } from './paths'

function loc(path: string, query: Record<string, string> = {}, hash = ''): RouteLocationNormalized {
  return { path, query, hash } as unknown as RouteLocationNormalized
}

describe('console paths', () => {
  it('prefixes console pages', () => {
    expect(CONSOLE_BASE).toBe('/console')
    expect(consolePath()).toBe('/console')
    expect(consolePath('/')).toBe('/console')
    expect(consolePath('/channels')).toBe('/console/channels')
    expect(consolePath('billing/plans')).toBe('/console/billing/plans')
  })

  it('recognises console paths', () => {
    expect(isConsolePath('/console')).toBe(true)
    expect(isConsolePath('/console/keys')).toBe(true)
    expect(isConsolePath('/console?x=1')).toBe(true)
    expect(isConsolePath('/consoles')).toBe(false)
    expect(isConsolePath('/')).toBe(false)
    expect(isConsolePath('/channels')).toBe(false)
  })

  it('builds the login location', () => {
    expect(loginLocation()).toEqual({ name: 'login', query: { redirect: '/console' } })
    expect(loginLocation('/console/keys?x=1')).toEqual({ name: 'login', query: { redirect: '/console/keys?x=1' } })
  })
})

describe('safeRedirect', () => {
  it('defaults to the console', () => {
    expect(safeRedirect(undefined)).toBe('/console')
    expect(safeRedirect('')).toBe('/console')
    expect(safeRedirect(null, '/')).toBe('/')
  })

  it('keeps same-origin paths', () => {
    expect(safeRedirect('/console/billing?page=2')).toBe('/console/billing?page=2')
    expect(safeRedirect('/')).toBe('/')
    expect(safeRedirect(['/console/keys', '/x'])).toBe('/console/keys')
  })

  it('rejects open redirects, the login page and the API', () => {
    for (const bad of ['//evil.com', '/\\evil.com', 'https://evil.com', 'console', '/login', '/login?redirect=/x', '/api/me', '/x\nSet-Cookie: a'])
      expect(safeRedirect(bad)).toBe('/console')
  })

  it('rejects the normalization bypasses found by the security audit', () => {
    for (const bad of ['/./\\evil.com', '/console/\\evil', '/x/../api/auth/logout', '/ /evil.com', '/console\t'])
      expect(safeRedirect(bad)).toBe('/console')
  })

  it('returns the normalized path', () => {
    expect(safeRedirect('/console/a/../b?x=1#h')).toBe('/console/b?x=1#h')
  })
})

describe('legacy redirects', () => {
  const children = [
    { path: '' },
    { path: 'channels' },
    { path: 'channels/:id' },
    { path: 'plugins/:id/edit' },
    { path: 'billing/plans' },
    { path: 'routes' },
    { path: ':pathMatch(.*)*' },
  ]

  it('creates one top-level redirect per console page (not the overview or the catch-all)', () => {
    expect(legacyRedirects(children).map(r => r.path)).toEqual(['/channels', '/channels/:id', '/plugins/:id/edit', '/billing/plans', '/routes'])
  })

  it('skips public top-level pages that shadow a console route', () => {
    const paths = legacyRedirects([{ path: 'models' }, { path: 'plaza' }, { path: 'my-models' }], PUBLIC_PATHS).map(r => r.path)
    expect(paths).toEqual(['/plaza', '/my-models'])
    expect(PUBLIC_PATHS).toContain('/models')
  })

  it('preserves params, query and hash', () => {
    expect(legacyTarget(loc('/channels/abc', { tab: 'models' }, '#x'))).toEqual({ path: '/console/channels/abc', query: { tab: 'models' }, hash: '#x' })
    const edit = legacyRedirects(children).find(r => r.path === '/plugins/:id/edit') as RouteRecordRedirect
    const redirect = edit.redirect as (to: RouteLocationNormalized) => unknown
    expect(redirect(loc('/plugins/p1/edit', { version: 'v2' }))).toEqual({ path: '/console/plugins/p1/edit', query: { version: 'v2' }, hash: '' })
  })
})
