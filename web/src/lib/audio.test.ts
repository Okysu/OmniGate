import { describe, expect, it } from 'vitest'
import { audioPriceParts, formatAudioSeconds, isAudioInbound } from './audio'

describe('formatAudioSeconds', () => {
  it('formats minutes and seconds', () => {
    expect(formatAudioSeconds(83)).toBe('1分23秒')
    expect(formatAudioSeconds(120)).toBe('2分')
    expect(formatAudioSeconds(45)).toBe('45秒')
    expect(formatAudioSeconds(0)).toBe('0秒')
    expect(formatAudioSeconds(3725)).toBe('1小时2分5秒')
    expect(formatAudioSeconds(3600)).toBe('1小时')
    expect(formatAudioSeconds(3605)).toBe('1小时5秒')
  })

  it('keeps one decimal under a minute and rounds above', () => {
    expect(formatAudioSeconds(2.54)).toBe('2.5秒')
    expect(formatAudioSeconds(59.97)).toBe('1分')
    expect(formatAudioSeconds(83.6)).toBe('1分24秒')
  })

  it('returns a dash for missing or invalid values', () => {
    expect(formatAudioSeconds(undefined)).toBe('—')
    expect(formatAudioSeconds(null)).toBe('—')
    expect(formatAudioSeconds(-1)).toBe('—')
    expect(formatAudioSeconds(Number.NaN)).toBe('—')
  })
})

describe('audioPriceParts', () => {
  it('lists the set audio prices in a fixed order', () => {
    expect(audioPriceParts({ perMCharacters: '15', audioOutputPerM: '12', audioInputPerM: null, perMinute: '0' }).map(p => [p.key, p.amount]))
      .toEqual([['audioOutputPerM', '12'], ['perMCharacters', '15']])
    expect(audioPriceParts({ audioInputPerM: '0', perMinute: '0.006' }).map(p => p.label)).toEqual(['音频入', '每分钟'])
  })

  it('is empty for prices without audio fields', () => {
    expect(audioPriceParts(null)).toEqual([])
    expect(audioPriceParts({})).toEqual([])
    expect(audioPriceParts({ perMinute: '0', perMCharacters: '0' })).toEqual([])
  })
})

describe('isAudioInbound', () => {
  it('matches the openai.audio.* inbounds', () => {
    expect(isAudioInbound('openai.audio.transcriptions')).toBe(true)
    expect(isAudioInbound('openai.audio.speech')).toBe(true)
    expect(isAudioInbound('openai.images.generations')).toBe(false)
  })
})
