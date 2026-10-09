import type { QuotaRule, RuleUsage, Subscription } from './types'
import { afterEach, describe, expect, it } from 'vitest'
import { clearCustomMeters, registerCustomMeters } from './customMeters'
import {
  audioMinutesNote,
  durationError,
  effectiveQuotaOverflow,
  exceededNote,
  formatDuration,
  formatMeterAmount,
  formatMeterValue,
  formatQuotaNumber,
  formatRemaining,
  formatUntil,
  isIntegerMeter,
  isQuotaOverflow,
  KEY_QUOTA_OVERFLOW_LABELS,
  METER_LABELS,
  meterHint,
  meterLabel,
  meterUnit,
  METERS,
  modelsSummary,
  mostUsedRule,
  PERIOD_RANGE,
  QUOTA_OVERFLOW_OPTIONS,
  resetInfo,
  ruleSummary,
  rulesSummary,
  ruleTitle,
  sortSubscriptions,
  usageLevel,
  usageRatio,
  WINDOW_DURATION_RANGE,
  windowDescription,
  windowShortLabel,
} from './quota'

const money = (v: string) => `$${Number(v).toFixed(2)}`

function rule(p: Partial<QuotaRule>): QuotaRule {
  return { id: 'r', label: '', meter: 'requests', window: { kind: 'lifetime' }, limit: '1', models: [], modelWeights: {}, ...p }
}

function usage(p: Partial<RuleUsage>): RuleUsage {
  return { ...rule({}), used: '0', windowStart: null, resetsAt: null, remaining: '1', exceeded: false, ...p }
}

describe('formatQuotaNumber', () => {
  it('uses 万 / 亿 for large counts', () => {
    expect(formatQuotaNumber('200')).toBe('200')
    expect(formatQuotaNumber('1234')).toBe('1,234')
    expect(formatQuotaNumber('5000000')).toBe('500 万')
    expect(formatQuotaNumber('12345')).toBe('1.23 万')
    expect(formatQuotaNumber('250000000')).toBe('2.5 亿')
    expect(formatQuotaNumber('1.5')).toBe('1.5')
    expect(formatQuotaNumber(null)).toBe('—')
  })
})

describe('durations', () => {
  it('formats and validates', () => {
    expect(formatDuration('5h')).toBe('5 小时')
    expect(formatDuration('30d')).toBe('30 天')
    expect(formatDuration('90m')).toBe('90 分钟')
    expect(formatDuration('bad')).toBe('bad')
    expect(durationError('5m', WINDOW_DURATION_RANGE)).toBeNull()
    expect(durationError('4m', WINDOW_DURATION_RANGE)).toMatch('5m 到 31d')
    expect(durationError('32d', WINDOW_DURATION_RANGE)).toMatch('5m 到 31d')
    expect(durationError('59m', PERIOD_RANGE)).toMatch('1h 到 366d')
    expect(durationError('366d', PERIOD_RANGE)).toBeNull()
    expect(durationError('05h', PERIOD_RANGE)).toBe('请输入正整数')
  })
})

describe('window descriptions', () => {
  it('describes every window kind', () => {
    expect(windowDescription({ kind: 'calendar', unit: 'week', timezone: 'Asia/Shanghai' })).toBe('每周（周一 00:00 Asia/Shanghai 重置）')
    expect(windowDescription({ kind: 'calendar', unit: 'day', timezone: 'UTC' })).toBe('每日（00:00 UTC 重置）')
    expect(windowDescription({ kind: 'calendar', unit: 'month' })).toBe('每月（1 日 00:00 UTC 重置）')
    expect(windowDescription({ kind: 'session', duration: '5h' })).toBe('5 小时会话窗口（首次请求开始计时）')
    expect(windowDescription({ kind: 'rolling', duration: '30m' })).toBe('滚动 30 分钟窗口（统计最近 30 分钟）')
    expect(windowDescription({ kind: 'period', every: '30d' })).toBe('每 30 天周期（从订阅开始时间起算）')
    expect(windowDescription({ kind: 'lifetime' })).toBe('订阅期内总量（不重置）')
  })

  it('has short labels', () => {
    expect(windowShortLabel({ kind: 'session', duration: '5h' })).toBe('5 小时会话')
    expect(windowShortLabel({ kind: 'calendar', unit: 'week' })).toBe('每周')
    expect(windowShortLabel({ kind: 'period', every: '7d' })).toBe('每 7 天')
    expect(windowShortLabel({ kind: 'rolling', duration: '1h' })).toBe('滚动 1 小时')
    expect(windowShortLabel({ kind: 'lifetime' })).toBe('总量')
  })
})

describe('rule summaries', () => {
  it('summarizes rules with the window or label', () => {
    const rules = [
      rule({ meter: 'requests', window: { kind: 'session', duration: '5h' }, limit: '200' }),
      rule({ meter: 'tokens.total', window: { kind: 'calendar', unit: 'week', timezone: 'UTC' }, limit: '5000000' }),
    ]
    expect(rulesSummary(rules, money)).toBe('5 小时会话：200 次请求 · 每周：500 万 tokens')
    expect(ruleSummary(rule({ label: '月度额度', meter: 'charge', limit: '20' }), money)).toBe('月度额度：$20.00 额度')
    expect(ruleTitle({ label: '  ', window: { kind: 'lifetime' } })).toBe('总量')
    expect(rulesSummary([], money)).toBe('—')
    expect(formatMeterValue('tokens.input', '10000', money)).toBe('1 万 输入 tokens')
  })

  it('formats the images meter (phase7 §1.1)', () => {
    expect(formatMeterValue('images', '500', money)).toBe('500 张图片')
    expect(ruleSummary(rule({ label: '每日出图', meter: 'images', limit: '20000' }), money)).toBe('每日出图：2 万 张图片')
    expect(meterUnit('images')).toBe('张')
    expect(formatMeterAmount('images', '12', money)).toBe('12')
    expect(isIntegerMeter('images')).toBe(true)
    expect(METERS).toContain('images')
    expect(METER_LABELS.images).toBe('图片张数')
  })

  it('formats the audio_seconds meter with minutes (phase9 §1)', () => {
    expect(METERS).toContain('audio_seconds')
    expect(METER_LABELS.audio_seconds).toBe('音频秒数')
    expect(meterUnit('audio_seconds')).toBe('秒')
    expect(isIntegerMeter('audio_seconds')).toBe(true)
    expect(formatMeterValue('audio_seconds', '3600', money)).toBe('3,600 秒音频（60 分钟）')
    expect(formatMeterValue('audio_seconds', '30', money)).toBe('30 秒音频')
    expect(formatMeterValue('audio_seconds', '90', money)).toBe('90 秒音频（约 1.5 分钟）')
    expect(ruleSummary(rule({ meter: 'audio_seconds', window: { kind: 'calendar', unit: 'day', timezone: 'UTC' }, limit: '3600' }), money)).toBe('每日：3,600 秒音频（60 分钟）')
    expect(formatMeterValue('audio_seconds', '600000', money)).toBe('60 万 秒音频（1 万 分钟）')
    expect(audioMinutesNote('abc')).toBe('')
    expect(audioMinutesNote(125)).toBe('（约 2.1 分钟）')
  })

  describe('plugin meters (phase9 §3)', () => {
    afterEach(() => clearCustomMeters())
    const id = 'custom:community.billing-examples.weighted_tokens'

    it('falls back to the meter name when nothing is known', () => {
      expect(meterLabel(id)).toBe('weighted_tokens')
      expect(meterUnit(id)).toBe('')
      expect(formatMeterValue(id, '1000', money)).toBe('weighted_tokens 1,000')
      expect(isIntegerMeter(id)).toBe(false)
      expect(meterHint(id)).toContain('按 0 记')
    })

    it('uses the registry, and the server snapshot first', () => {
      registerCustomMeters([{ id, pluginKey: 'community.billing-examples', meter: 'weighted_tokens', label: '加权 Token', unit: 'tokens' }])
      expect(meterLabel(id)).toBe('加权 Token')
      expect(meterUnit(id)).toBe('tokens')
      expect(ruleSummary(rule({ label: '每日', meter: id, limit: '5000000' }), money)).toBe('每日：加权 Token 500 万 tokens')
      const snap = rule({ label: '每日', meter: id, limit: '20', meterLabel: '快照名', meterUnit: '点' })
      expect(ruleSummary(snap, money)).toBe('每日：快照名 20 点')
      expect(meterUnit(id, snap)).toBe('点')
    })

    it('keeps built-in labels', () => {
      expect(meterLabel('requests')).toBe('请求次数')
      expect(meterHint('images')).toContain('图片')
      expect(meterLabel('unknown')).toBe('unknown')
    })
  })

  it('summarizes model lists', () => {
    expect(modelsSummary([])).toBe('全部模型')
    expect(modelsSummary(['a', 'b'])).toBe('a、b')
    expect(modelsSummary(['a', 'b', 'c'])).toBe('a、b 等 3 个模型')
  })
})

describe('usage', () => {
  it('computes ratio and level', () => {
    expect(usageRatio('50', '200')).toBe(0.25)
    expect(usageRatio('5', '0')).toBe(0)
    expect(usageLevel({ used: '170', limit: '200', exceeded: false })).toBe('warn')
    expect(usageLevel({ used: '10', limit: '200', exceeded: false })).toBe('ok')
    expect(usageLevel({ used: '200', limit: '200', exceeded: true })).toBe('exceeded')
  })

  it('formats time until a reset', () => {
    const now = Date.parse('2026-10-08T00:00:00Z')
    expect(formatUntil('2026-10-08T00:30:00Z', now)).toBe('30 分钟后')
    expect(formatUntil('2026-10-08T03:20:00Z', now)).toBe('3 小时 20 分钟后')
    expect(formatUntil('2026-10-08T05:00:00Z', now)).toBe('5 小时后')
    expect(formatUntil('2026-10-10T04:00:00Z', now)).toBe('2 天 4 小时后')
    expect(formatUntil('2026-10-07T00:00:00Z', now)).toBe('即将')
  })

  it('formats the remaining validity', () => {
    const now = Date.parse('2026-10-08T00:00:00Z')
    expect(formatRemaining('2026-11-07T00:00:00Z', now)).toBe('剩余 30 天')
    expect(formatRemaining('2026-10-08T05:30:00Z', now)).toBe('剩余 5 小时')
    expect(formatRemaining('2026-10-08T00:10:00Z', now)).toBe('剩余 10 分钟')
    expect(formatRemaining('2026-10-07T00:00:00Z', now)).toBe('已到期')
  })

  it('describes the reset of each window kind', () => {
    const now = Date.parse('2026-10-08T00:00:00Z')
    expect(resetInfo(usage({ window: { kind: 'lifetime' } }), now).text).toBe('不重置')
    expect(resetInfo(usage({ window: { kind: 'session', duration: '5h' }, windowStart: null }), now)).toEqual({ text: '尚未开始', absolute: null })
    expect(resetInfo(usage({ window: { kind: 'rolling', duration: '5h' }, windowStart: '2026-10-07T19:00:00Z', resetsAt: null }), now).text).toBe('窗口内暂无用量')
    const r = resetInfo(usage({ window: { kind: 'calendar', unit: 'day' }, windowStart: '2026-10-07T00:00:00Z', resetsAt: '2026-10-08T02:00:00Z' }), now)
    expect(r.text).toBe('2 小时后重置')
    expect(r.absolute).not.toBeNull()
  })

  function sub(p: Partial<Subscription>): Subscription {
    return { id: 's', user: { id: 'u', displayName: 'U' }, plan: { id: 'p', name: 'P' }, status: 'active', startsAt: '2026-10-01T00:00:00Z', endsAt: '2026-11-01T00:00:00Z', source: 'admin', models: [], rules: [], createdAt: '2026-10-01T00:00:00Z', ...p }
  }

  it('sorts active subscriptions first and picks the most used rule', () => {
    const a = sub({ id: 'a', status: 'expired', createdAt: '2026-09-01T00:00:00Z' })
    const b = sub({ id: 'b', endsAt: '2026-12-01T00:00:00Z', rules: [usage({ id: 'x', used: '10', limit: '100' })] })
    const c = sub({ id: 'c', endsAt: '2026-11-15T00:00:00Z', rules: [usage({ id: 'y', used: '90', limit: '100' })] })
    const d = sub({ id: 'd', status: 'cancelled', createdAt: '2026-10-05T00:00:00Z', rules: [usage({ id: 'z', used: '100', limit: '100', exceeded: true })] })
    expect(sortSubscriptions([a, b, c, d]).map(s => s.id)).toEqual(['c', 'b', 'd', 'a'])
    const best = mostUsedRule([a, b, c, d])
    expect(best?.subscription.id).toBe('c')
    expect(best?.rule.id).toBe('y')
    expect(mostUsedRule([a])).toBeNull()
  })
})

describe('quota overflow', () => {
  it('validates values', () => {
    expect(isQuotaOverflow('block')).toBe(true)
    expect(isQuotaOverflow('wallet')).toBe(true)
    expect(isQuotaOverflow('')).toBe(false)
    expect(isQuotaOverflow('overflow_to_wallet')).toBe(false)
    expect(isQuotaOverflow(undefined)).toBe(false)
  })

  it('resolves a key override against the account setting', () => {
    expect(effectiveQuotaOverflow('wallet', 'block')).toBe('wallet')
    expect(effectiveQuotaOverflow('block', 'wallet')).toBe('block')
    expect(effectiveQuotaOverflow('', 'wallet')).toBe('wallet')
    expect(effectiveQuotaOverflow(undefined, 'block')).toBe('block')
    expect(effectiveQuotaOverflow('', null)).toBeNull()
  })

  it('describes what happens once a rule is exceeded', () => {
    expect(exceededNote('wallet')).toContain('钱包')
    expect(exceededNote('block')).toContain('429')
    expect(exceededNote(null)).not.toContain('429')
  })

  it('offers block (default) before wallet and labels the key options', () => {
    expect(QUOTA_OVERFLOW_OPTIONS.map(o => o.value)).toEqual(['block', 'wallet'])
    expect(QUOTA_OVERFLOW_OPTIONS[0]!.title).toContain('默认')
    expect(KEY_QUOTA_OVERFLOW_LABELS).toEqual({ '': '跟随账户设置', 'block': '阻断', 'wallet': '使用钱包' })
  })
})
