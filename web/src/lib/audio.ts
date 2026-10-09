// Audio endpoints (phase9-api.md §1): price parts and duration formatting. Pure functions.
import type { Price } from './types'
import { isChargedAmount } from './plaza'

export type AudioPriceFields = Pick<Price, 'audioInputPerM' | 'audioOutputPerM' | 'perMinute' | 'perMCharacters'>

export interface AudioPricePart {
  key: 'audioInputPerM' | 'audioOutputPerM' | 'perMinute' | 'perMCharacters'
  /** Short label for compact table cells. */
  label: string
  /** Long description for tooltips. */
  description: string
  amount: string
}

/**
 * The audio prices that are set: audio token prices whenever present (null = falls back
 * to the text price, not listed), per-minute / per-1M-characters only when non-zero.
 */
export function audioPriceParts(p: AudioPriceFields | null | undefined): AudioPricePart[] {
  if (!p)
    return []
  const out: AudioPricePart[] = []
  if (typeof p.audioInputPerM === 'string')
    out.push({ key: 'audioInputPerM', label: '音频入', description: '音频输入 / 1M token', amount: p.audioInputPerM })
  if (typeof p.audioOutputPerM === 'string')
    out.push({ key: 'audioOutputPerM', label: '音频出', description: '音频输出 / 1M token', amount: p.audioOutputPerM })
  if (isChargedAmount(p.perMinute))
    out.push({ key: 'perMinute', label: '每分钟', description: '每分钟输入音频（转写 / 翻译，按秒计）', amount: p.perMinute })
  if (isChargedAmount(p.perMCharacters))
    out.push({ key: 'perMCharacters', label: '每 1M 字符', description: '每 1M 输入字符（语音合成）', amount: p.perMCharacters })
  return out
}

/**
 * Seconds of audio as "1分23秒" / "45秒" / "1小时2分5秒"; fractions are kept to one
 * decimal under a minute ("2.5秒") and rounded otherwise. Invalid / negative → "—".
 */
export function formatAudioSeconds(seconds: number | null | undefined): string {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0)
    return '—'
  if (seconds < 60) {
    const r = Math.round(seconds * 10) / 10
    if (r < 60)
      return `${Number.isInteger(r) ? r : r.toFixed(1)}秒`
  }
  const total = Math.round(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  if (h > 0)
    return `${h}小时${m ? `${m}分` : ''}${s ? `${s}秒` : ''}`
  return `${m}分${s ? `${s}秒` : ''}`
}

/** Audio inbound protocols (`openai.audio.*`). */
export function isAudioInbound(inbound: string): boolean {
  return inbound.startsWith('openai.audio.')
}
