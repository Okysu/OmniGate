import { describe, expect, it } from 'vitest'
import { ANNOUNCEMENT_STORAGE_KEY, announcementOf, dismissAnnouncement, docsUrlOf, isAnnouncementDismissed, landingEnabledOf, pageTitle, publicModelPlazaOf, siteNameOf, textHash } from './site'

const base = { name: 'OmniGate', version: 'dev', currency: { code: 'USD', symbol: '$', decimals: 2 }, registrationMode: 'open' as const }

function memoryStorage() {
  const m = new Map<string, string>()
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), map: m }
}

describe('site info fallbacks', () => {
  it('handles an old backend without the new fields', () => {
    expect(siteNameOf(base)).toBe('OmniGate')
    expect(announcementOf(base)).toBe('')
    expect(landingEnabledOf(base)).toBe(true)
    expect(docsUrlOf(base, null)).toBeNull()
    expect(docsUrlOf(base, 'https://env.example/docs')).toBe('https://env.example/docs')
    expect(siteNameOf(null)).toBe('OmniGate')
    expect(landingEnabledOf(undefined)).toBe(true)
    expect(publicModelPlazaOf(base)).toBe(true)
    expect(publicModelPlazaOf(null)).toBe(true)
    expect(publicModelPlazaOf({ ...base, publicModelPlaza: false })).toBe(false)
  })

  it('uses the runtime settings when present', () => {
    const info = { ...base, siteName: '  Acme AI ', announcement: ' 维护通知 ', landingEnabled: false, docsUrl: 'https://docs.acme.dev' }
    expect(siteNameOf(info)).toBe('Acme AI')
    expect(announcementOf(info)).toBe('维护通知')
    expect(landingEnabledOf(info)).toBe(false)
    expect(docsUrlOf(info, 'https://env.example')).toBe('https://docs.acme.dev')
  })

  it('ignores blank names and non-http docs links', () => {
    expect(siteNameOf({ ...base, siteName: '   ' })).toBe('OmniGate')
    expect(docsUrlOf({ ...base, docsUrl: 'javascript:alert(1)' }, null)).toBeNull()
    expect(docsUrlOf({ ...base, docsUrl: '' }, 'https://fallback')).toBe('https://fallback')
  })

  it('builds page titles', () => {
    expect(pageTitle('渠道', 'Acme')).toBe('渠道 · Acme')
    expect(pageTitle(undefined, 'Acme')).toBe('Acme')
  })
})

describe('announcement dismissal', () => {
  it('is remembered per text', () => {
    const s = memoryStorage()
    expect(isAnnouncementDismissed('a', s)).toBe(false)
    dismissAnnouncement('a', s)
    expect(s.map.get(ANNOUNCEMENT_STORAGE_KEY)).toBe(textHash('a'))
    expect(isAnnouncementDismissed('a', s)).toBe(true)
    expect(isAnnouncementDismissed('b', s)).toBe(false)
  })

  it('survives throwing storage', () => {
    const bad = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
    expect(isAnnouncementDismissed('a', bad)).toBe(false)
    expect(() => dismissAnnouncement('a', bad)).not.toThrow()
    expect(isAnnouncementDismissed('a', null)).toBe(false)
  })

  it('hashes deterministically', () => {
    expect(textHash('维护通知')).toBe(textHash('维护通知'))
    expect(textHash('a')).not.toBe(textHash('b'))
    expect(textHash('')).toMatch(/^[0-9a-f]{8}$/)
  })
})
