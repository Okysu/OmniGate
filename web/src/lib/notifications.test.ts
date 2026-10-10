import type { NotificationPreferences } from './types'
import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import {
  applyLocks,
  buildPreferencesBody,
  CATEGORIES,
  CATEGORY_LABELS,
  categoryOf,
  cooldownLeft,
  defaultEvents,
  dirtyParts,
  eligibleEvents,
  EVENT_CATALOG,
  eventLabel,
  eventMeta,
  formFromPreferences,
  isLocked,
  isSmtpUnavailableError,
  isValidEmail,
  normalizeCode,
  notificationLink,
  timezoneOptions,
  typesParam,
  unreadBadgeText,
  validatePreferences,
} from './notifications'

const USER = { permissions: ['channels.read', 'channels.write', 'keys.own', 'plugins.read', 'billing.own', 'stats.own'], managesChannels: false }
const OWNER = { ...USER, managesChannels: true }
const ADMIN = { permissions: [...USER.permissions, 'channels.manage', 'plugins.trust', 'plugins.manage'], managesChannels: false }
const AUDITOR = { permissions: ['users.read', 'audit.read', 'settings.read', 'channels.read', 'plugins.read', 'stats.own', 'stats.all'], managesChannels: false }

function prefs(over: Partial<NotificationPreferences> = {}): NotificationPreferences {
  return {
    email: { enabled: true, address: null, verified: true },
    webhook: { enabled: false, url: null, secretSet: false, format: 'json' },
    events: {},
    thresholds: { walletBalanceLow: '1' },
    digest: 'off',
    timezone: 'Asia/Shanghai',
    version: 0,
    ...over,
  }
}

describe('event catalog', () => {
  it('covers every contract event once with categories', () => {
    expect(EVENT_CATALOG).toHaveLength(23)
    expect(new Set(EVENT_CATALOG.map(e => e.type)).size).toBe(23)
    expect(categoryOf('upstream.balance_low')).toBe('channel')
    expect(categoryOf('quota.exhausted')).toBe('plan')
    expect(categoryOf('subscription.future_thing')).toBe('plan')
    expect(categoryOf('nope.x')).toBeNull()
    expect(typesParam('wallet')).toBe('wallet.balance_low,wallet.credited')
    expect(typesParam('channel')).toBe('channel.unhealthy,channel.recovered,channel.auth_failed,upstream.balance_low')
    expect(typesParam('')).toBeUndefined()
    // phase8
    expect(categoryOf('account.group_changed')).toBe('account')
    expect(typesParam('limit')).toBe('limit.spend_near,limit.spend_reached')
    expect(categoryOf('limit.future')).toBe('limit')
    // phase5 §5.5: share invitations have their own category (not the channel alerts).
    expect(categoryOf('channel.share_invited')).toBe('share')
    expect(categoryOf('channel.future')).toBe('channel')
    expect(typesParam('share')).toBe('channel.share_invited')
    expect(CATEGORY_LABELS.share).toBe('渠道共享')
  })

  it('defaults share invitations to in-app only (not an alert)', () => {
    expect(defaultEvents()['channel.share_invited']).toEqual({ email: false, webhook: false, inApp: true })
    expect(EVENT_CATALOG.find(e => e.type === 'channel.share_invited')?.alert).toBe(false)
  })

  it('has the phase8 defaults (group change in-app; spend reached email + in-app)', () => {
    const defs = defaultEvents()
    expect(defs['account.group_changed']).toEqual({ email: false, webhook: false, inApp: true })
    expect(defs['limit.spend_near']).toEqual({ email: false, webhook: false, inApp: true })
    expect(defs['limit.spend_reached']).toEqual({ email: true, webhook: false, inApp: true })
    // limit.* follows the server's billing category (billing.own).
    expect(eligibleEvents({ permissions: ['keys.own'], managesChannels: false }).map(e => e.type)).not.toContain('limit.spend_near')
  })

  it('marks alert-class events (never in the digest)', () => {
    expect(EVENT_CATALOG.filter(e => e.alert).map(e => e.type).sort()).toEqual([
      'account.status_changed',
      'channel.auth_failed',
      'channel.recovered',
      'channel.unhealthy',
      'limit.spend_reached',
      'quota.exhausted',
      'upstream.balance_low',
      'wallet.balance_low',
    ])
  })
})

describe('eligibility', () => {
  const types = (ctx: typeof USER) => eligibleEvents(ctx).map(e => e.type)

  it('hides channel events from users without channels and plugin approval from non-trusters', () => {
    expect(eligibleEvents(USER).some(e => e.category === 'channel')).toBe(false)
    // Share invitations reach every user, channel owner or not.
    expect(types(USER)).toContain('channel.share_invited')
    expect(types(USER)).not.toContain('plugin.pending_approval')
    expect(types(USER)).toContain('wallet.balance_low')
    expect(types(OWNER)).toContain('upstream.balance_low')
    expect(types(ADMIN)).toContain('channel.unhealthy')
    expect(types(ADMIN)).toContain('plugin.pending_approval')
  })

  it('gives auditors only account, model and share events (no wallet / keys)', () => {
    expect(new Set(eligibleEvents(AUDITOR).map(e => e.category))).toEqual(new Set(['account', 'model', 'share']))
  })
})

describe('preferences form', () => {
  it('fills contract defaults for missing events and starts clean', () => {
    const p = prefs()
    const f = formFromPreferences(p)
    expect(f.events['wallet.balance_low']).toEqual({ email: true, webhook: false, inApp: true })
    expect(f.events['model.added']).toEqual({ email: false, webhook: false, inApp: false })
    expect(dirtyParts(f, p, USER)).toEqual([])
  })

  it('sends only eligible events plus unknown server types, with version and echoed flags', () => {
    const p = prefs({
      version: 4,
      webhook: { enabled: true, url: 'https://hooks.example.com/x', secretSet: true, format: 'feishu' },
      events: { 'plugin.pending_approval': { email: true, webhook: false, inApp: true }, 'future.event': { email: true, webhook: true, inApp: false } },
    })
    const f = formFromPreferences(p)
    f.events['key.expiring'] = { email: false, webhook: true, inApp: true }
    f.walletBalanceLow = '2.50'
    const body = buildPreferencesBody(f, p, USER)
    expect(body.version).toBe(4)
    expect(body.events['plugin.pending_approval']).toBeUndefined()
    expect(body.events['channel.unhealthy']).toBeUndefined()
    expect(body.events['future.event']).toEqual({ email: true, webhook: true, inApp: false })
    expect(body.events['key.expiring']).toEqual({ email: false, webhook: true, inApp: true })
    expect(body.thresholds.walletBalanceLow).toBe('2.5')
    expect(body.webhook).toEqual({ enabled: true, url: 'https://hooks.example.com/x', secretSet: true, format: 'feishu' })
    expect(body.email).toEqual({ enabled: true, address: null, verified: true })
    expect(dirtyParts(f, p, USER)).toEqual(['事件开关', '余额阈值'])
    // Admins also send plugin approval.
    expect(buildPreferencesBody(f, p, ADMIN).events['plugin.pending_approval']).toEqual({ email: true, webhook: false, inApp: true })
  })

  it('ignores canonical-equal edits and empty URLs', () => {
    const p = prefs({ thresholds: { walletBalanceLow: '1' } })
    const f = formFromPreferences(p)
    f.walletBalanceLow = '1.000'
    f.webhookUrl = '   '
    expect(dirtyParts(f, p, USER)).toEqual([])
    f.digest = 'daily'
    f.emailAddress = 'me@example.com'
    expect(dirtyParts(f, p, USER)).toEqual(['邮件', '摘要与时区'])
  })

  it('validates threshold, webhook URL, time zone and email target', () => {
    const p = prefs()
    const f = formFromPreferences(p)
    expect(validatePreferences(f, { ...USER, accountEmail: 'a@b.co' })).toEqual({})
    f.walletBalanceLow = '-1'
    f.webhookEnabled = true
    f.timezone = 'Mars/Olympus'
    expect(Object.keys(validatePreferences(f, { ...USER, accountEmail: null })).sort()).toEqual(['email.address', 'thresholds.walletBalanceLow', 'timezone', 'webhook.url'])
    f.webhookUrl = 'ftp://x'
    expect(validatePreferences(f, { ...AUDITOR, accountEmail: 'a@b.co' })['webhook.url']).toMatch(/http/)
    // Auditors do not see the wallet threshold.
    expect(validatePreferences(f, { ...AUDITOR, accountEmail: 'a@b.co' })['thresholds.walletBalanceLow']).toBeUndefined()
  })

  it('has defaults for every event', () => {
    expect(Object.keys(defaultEvents())).toHaveLength(23)
  })
})

describe('helpers', () => {
  it('keeps only same-origin paths and https links', () => {
    expect(notificationLink('/console/billing')).toEqual({ kind: 'internal', to: '/console/billing' })
    expect(notificationLink('//evil.com')).toBeNull()
    expect(notificationLink('/api/x')).toBeNull()
    expect(notificationLink('javascript:alert(1)')).toBeNull()
    expect(notificationLink('http://example.com')).toBeNull()
    expect(notificationLink('https://status.example.com/x')).toEqual({ kind: 'external', href: 'https://status.example.com/x' })
    expect(notificationLink(null)).toBeNull()
  })

  it('formats the unread badge', () => {
    expect(unreadBadgeText(0)).toBe('')
    expect(unreadBadgeText(7)).toBe('7')
    expect(unreadBadgeText(100)).toBe('99+')
  })

  it('computes the resend cooldown and normalises codes', () => {
    expect(cooldownLeft(null)).toBe(0)
    expect(cooldownLeft(10_500, 1_000)).toBe(10)
    expect(cooldownLeft(1_000, 5_000)).toBe(0)
    expect(normalizeCode(' 12a3-45 678')).toBe('123456')
    expect(isValidEmail('a@b.co')).toBe(true)
    expect(isValidEmail('a@b')).toBe(false)
  })

  it('detects "SMTP not configured" errors', () => {
    expect(isSmtpUnavailableError(new ApiError(409, 'smtp_not_configured', 'x'))).toBe(true)
    expect(isSmtpUnavailableError(new ApiError(422, 'validation_failed', '邮件服务未配置'))).toBe(true)
    expect(isSmtpUnavailableError(new ApiError(429, 'rate_limited', 'SMTP'))).toBe(false)
    expect(isSmtpUnavailableError(new ApiError(422, 'validation_failed', '地址无效'))).toBe(false)
  })

  it('lists common time zones first and keeps an unknown current zone', () => {
    const { common, others } = timezoneOptions('Etc/GMT-3', ['Asia/Shanghai', 'Europe/Paris'])
    expect(common[0]).toBe('Asia/Shanghai')
    expect(others).toEqual(['Etc/GMT-3', 'Europe/Paris'])
  })
})

describe('phase7 events', () => {
  it('registers the account and subscription admin events', () => {
    expect(categoryOf('account.status_changed')).toBe('account')
    expect(categoryOf('account.future')).toBe('account')
    expect(eventLabel('subscription.quota_reset')).toBe('套餐额度已重置')
    expect(eventLabel('subscription.extended')).toBe('订阅已延期')
    expect(typesParam('account')).toBe('account.status_changed,account.group_changed')
    expect(typesParam('plan')!.split(',')).toEqual(expect.arrayContaining(['subscription.quota_reset', 'subscription.extended']))
    expect(CATEGORIES[0]).toBe('account')
    expect(CATEGORY_LABELS.account).toBe('账户')
    for (const t of ['account.status_changed', 'subscription.quota_reset', 'subscription.extended'])
      expect(eventMeta(t)!.defaults).toEqual({ email: true, webhook: false, inApp: true })
  })

  it('registers reset_card.issued in the plan category (phase11 §2.1)', () => {
    expect(categoryOf('reset_card.issued')).toBe('plan')
    expect(categoryOf('reset_card.future')).toBe('plan')
    expect(eventLabel('reset_card.issued')).toBe('获得额度重置卡')
    expect(eventMeta('reset_card.issued')!.defaults).toEqual({ email: true, webhook: false, inApp: true })
    expect(typesParam('plan')!.split(',')).toContain('reset_card.issued')
    expect(eligibleEvents(USER).map(e => e.type)).toContain('reset_card.issued')
    expect(eligibleEvents(AUDITOR).map(e => e.type)).not.toContain('reset_card.issued')
  })

  it('is eligible for every signed-in user (account) and billing users (plan)', () => {
    const types = eligibleEvents(USER).map(e => e.type)
    expect(types).toEqual(expect.arrayContaining(['account.status_changed', 'subscription.quota_reset', 'subscription.extended']))
    expect(eligibleEvents(AUDITOR).map(e => e.type)).toContain('account.status_changed')
  })

  it('keeps the in-app channel of account.status_changed locked on', () => {
    expect(isLocked('account.status_changed', 'inApp')).toBe(true)
    expect(isLocked('account.status_changed', 'email')).toBe(false)
    expect(isLocked('wallet.credited', 'inApp')).toBe(false)
    const p = prefs({ events: { 'account.status_changed': { email: false, webhook: false, inApp: false } } })
    const f = formFromPreferences(p)
    expect(f.events['account.status_changed']).toEqual({ email: false, webhook: false, inApp: true })
    f.events['account.status_changed'] = { email: true, webhook: true, inApp: false }
    expect(buildPreferencesBody(f, p, USER).events['account.status_changed']).toEqual({ email: true, webhook: true, inApp: true })
    expect(applyLocks('model.added', { email: false, webhook: false, inApp: false })).toEqual({ email: false, webhook: false, inApp: false })
  })
})
