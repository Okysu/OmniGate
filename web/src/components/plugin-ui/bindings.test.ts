import { describe, expect, it } from 'vitest'
import { contributionCapabilities, plainParagraphs, progressRatio, referencedCapabilities, safeHttpsUrl } from './bindings'

describe('plugin-ui bindings helpers', () => {
  it('collects referenced capabilities', () => {
    expect(referencedCapabilities({
      type: 'statGroup',
      items: [
        { type: 'stat', bind: 'balance.get:/total', currencyBind: 'balance.get:/currency' },
        { type: 'progress', valueBind: 'quota.get:/windows/0/used', maxBind: 'quota.get:/windows/0/limit' },
      ],
    })).toEqual(['balance.get', 'quota.get'])
    expect(contributionCapabilities({ slot: 'channel.detail.overview', component: { type: 'markdown', text: 'x' }, actions: [{ label: 'r', capability: 'health.check' }] })).toEqual(['health.check'])
  })

  it('only accepts https links', () => {
    expect(safeHttpsUrl('https://example.com/a')).toBe('https://example.com/a')
    expect(safeHttpsUrl('http://example.com')).toBeNull()
    expect(safeHttpsUrl('javascript:alert(1)')).toBeNull()
    expect(safeHttpsUrl('')).toBeNull()
  })

  it('splits plain-text paragraphs', () => {
    expect(plainParagraphs('a\nb\n\n  c  \n\n\n')).toEqual(['a\nb', 'c'])
    expect(plainParagraphs('<b>x</b>')).toEqual(['<b>x</b>'])
  })

  it('computes progress ratios', () => {
    expect(progressRatio('25', '100')).toBe(0.25)
    expect(progressRatio(150, 100)).toBe(1)
    expect(progressRatio('x', 100)).toBeNull()
    expect(progressRatio(1, 0)).toBeNull()
  })
})
