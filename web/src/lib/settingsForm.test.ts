import type { SystemSettings } from './types'
import { describe, expect, it } from 'vitest'
import {
  buildResetPatch,
  buildSectionPatch,
  changedFields,
  formatReadonlyValue,
  formFieldOfKey,
  formFromSettings,
  isSectionDirty,
  isValidFromAddress,
  normalizeErrorKeys,
  parseDomains,
  readonlyEntries,
  sourceOf,
  validateSection,
} from './settingsForm'

const saved: SystemSettings = {
  site: { name: 'OmniGate', announcement: '', landingEnabled: true, docsUrl: '', publicModelPlaza: true },
  auth: { registrationMode: 'restricted', allowedEmailDomains: ['example.com'] },
  billing: { enforce: false, signupCredit: '0' },
  gateway: { maxAttempts: 3, logRetentionDays: 90, retryOn: ['rate_limit', 'server_error', 'timeout', 'network', 'auth_error', 'not_found'] },
}

describe('settings form', () => {
  it('starts clean', () => {
    const f = formFromSettings(saved)
    for (const s of ['site', 'auth', 'billing', 'gateway'] as const)
      expect(isSectionDirty(s, f[s], saved)).toBe(false)
  })

  it('treats a missing publicModelPlaza (older backend) as on and patches only real changes', () => {
    const old = { ...saved, site: { name: 'OmniGate', announcement: '', landingEnabled: true, docsUrl: '' } } as unknown as SystemSettings
    const f = formFromSettings(old)
    expect(f.site.publicModelPlaza).toBe(true)
    expect(isSectionDirty('site', f.site, old)).toBe(false)
    f.site.publicModelPlaza = false
    expect(buildSectionPatch('site', f.site, old, 4)).toEqual({ version: 4, settings: { site: { publicModelPlaza: false } } })
  })

  it('defaults gateway.retryOn for older backends and patches it in canonical order', () => {
    const old = { ...saved, gateway: { maxAttempts: 3, logRetentionDays: 90 } } as unknown as SystemSettings
    const f = formFromSettings(old)
    expect(f.gateway.retryOn).toEqual(['rate_limit', 'server_error', 'timeout', 'network', 'auth_error', 'not_found'])
    expect(isSectionDirty('gateway', f.gateway, old)).toBe(false)
    f.gateway.retryOn = ['client_error', 'rate_limit', 'rate_limit']
    expect(buildSectionPatch('gateway', f.gateway, old, 9)).toEqual({ version: 9, settings: { gateway: { retryOn: ['rate_limit', 'client_error'] } } })
    f.gateway.retryOn = ['not_found', 'auth_error', 'network', 'timeout', 'server_error', 'rate_limit']
    expect(isSectionDirty('gateway', f.gateway, old)).toBe(false)
    expect(buildResetPatch('gateway.retryOn', 2)).toEqual({ version: 2, settings: { gateway: { retryOn: null } } })
  })

  it('ignores whitespace-only and canonical-equal edits', () => {
    const f = formFromSettings(saved)
    f.site.name = ' OmniGate '
    f.billing.signupCredit = '0.000'
    f.gateway.maxAttempts = '3'
    f.auth.allowedEmailDomains = ['@Example.com']
    expect(isSectionDirty('site', f.site, saved)).toBe(false)
    expect(isSectionDirty('billing', f.billing, saved)).toBe(false)
    expect(isSectionDirty('gateway', f.gateway, saved)).toBe(false)
    expect(isSectionDirty('auth', f.auth, saved)).toBe(false)
  })

  it('builds a patch with only the changed fields of one section', () => {
    const f = formFromSettings(saved)
    f.site.name = '  Acme AI  '
    f.site.announcement = '周六 2:00 维护'
    f.gateway.maxAttempts = 5 // other section: not included
    expect(changedFields('site', f.site, saved)).toEqual(['name', 'announcement'])
    expect(buildSectionPatch('site', f.site, saved, 12)).toEqual({
      version: 12,
      settings: { site: { name: 'Acme AI', announcement: '周六 2:00 维护' } },
    })
    f.gateway.logRetentionDays = '0'
    expect(buildSectionPatch('gateway', f.gateway, saved, 12)).toEqual({
      version: 12,
      settings: { gateway: { maxAttempts: 5, logRetentionDays: 0 } },
    })
  })

  it('normalises domains and amounts in the patch', () => {
    const f = formFromSettings(saved)
    f.auth.registrationMode = 'restricted'
    f.auth.allowedEmailDomains = ['example.com', '@Corp.Example.org', 'corp.example.org']
    f.billing.signupCredit = '1.50'
    expect(buildSectionPatch('auth', f.auth, saved, 1).settings).toEqual({ auth: { allowedEmailDomains: ['example.com', 'corp.example.org'] } })
    expect(buildSectionPatch('billing', f.billing, saved, 1).settings).toEqual({ billing: { signupCredit: '1.5' } })
  })

  it('defaults referral settings for older backends and patches them canonically', () => {
    const f = formFromSettings(saved)
    expect(f.billing).toMatchObject({ referralEnabled: false, referralRate: '10', referralMinRecharge: '0' })
    expect(isSectionDirty('billing', f.billing, saved)).toBe(false)
    f.billing.referralRate = '10.00'
    expect(isSectionDirty('billing', f.billing, saved)).toBe(false)
    f.billing.referralEnabled = true
    f.billing.referralRate = '12.50'
    f.billing.referralMinRecharge = '20.0'
    expect(changedFields('billing', f.billing, saved)).toEqual(['referralEnabled', 'referralRate', 'referralMinRecharge'])
    expect(buildSectionPatch('billing', f.billing, saved, 7)).toEqual({
      version: 7,
      settings: { billing: { referralEnabled: true, referralRate: '12.5', referralMinRecharge: '20' } },
    })
    const withReferral = { ...saved, billing: { ...saved.billing, referralEnabled: true, referralRate: '5', referralMinRecharge: '1' } }
    expect(formFromSettings(withReferral).billing).toMatchObject({ referralEnabled: true, referralRate: '5', referralMinRecharge: '1' })
    expect(buildResetPatch('billing.referralRate', 2)).toEqual({ version: 2, settings: { billing: { referralRate: null } } })
  })

  it('builds reset patches with null', () => {
    expect(buildResetPatch('site.name', 3)).toEqual({ version: 3, settings: { site: { name: null } } })
    expect(buildResetPatch('gateway.logRetentionDays', 4)).toEqual({ version: 4, settings: { gateway: { logRetentionDays: null } } })
  })
})

describe('settings validation', () => {
  it('validates each section', () => {
    const f = formFromSettings(saved)
    expect(validateSection('site', f.site)).toEqual({})
    f.site.name = ''
    f.site.announcement = 'x'.repeat(501)
    f.site.docsUrl = 'ftp://x'
    expect(Object.keys(validateSection('site', f.site)).sort()).toEqual(['site.announcement', 'site.docsUrl', 'site.name'])
    f.site.name = 'x'.repeat(51)
    expect(validateSection('site', f.site)['site.name']).toBeDefined()

    f.auth.allowedEmailDomains = ['ok.com', 'not a domain']
    expect(validateSection('auth', f.auth)['auth.allowedEmailDomains']).toContain('not a domain')

    f.billing.signupCredit = '-1'
    expect(validateSection('billing', f.billing)['billing.signupCredit']).toBeDefined()
    f.billing.signupCredit = '0.123456789'
    expect(validateSection('billing', f.billing)).toEqual({})
    for (const bad of ['100.01', '-1', '1.234', 'abc', ''])
      expect(validateSection('billing', { ...f.billing, referralRate: bad })['billing.referralRate']).toBeDefined()
    for (const ok of ['0', '100', '12.5', '99.99'])
      expect(validateSection('billing', { ...f.billing, referralRate: ok })).toEqual({})
    expect(validateSection('billing', { ...f.billing, referralMinRecharge: '-5' })['billing.referralMinRecharge']).toBeDefined()

    f.gateway.maxAttempts = 0
    f.gateway.logRetentionDays = ''
    expect(Object.keys(validateSection('gateway', f.gateway)).sort()).toEqual(['gateway.logRetentionDays', 'gateway.maxAttempts'])
    f.gateway.maxAttempts = 5
    f.gateway.logRetentionDays = 0
    expect(validateSection('gateway', f.gateway)).toEqual({})
  })
})

describe('helpers', () => {
  it('parses pasted domains', () => {
    expect(parseDomains('A.com, b.org\n@c.net；a.com')).toEqual(['a.com', 'b.org', 'c.net'])
  })

  it('strips the settings. prefix of error keys', () => {
    expect(normalizeErrorKeys({ 'settings.site.name': 'x', 'gateway.maxAttempts': 'y' })).toEqual({ 'site.name': 'x', 'gateway.maxAttempts': 'y' })
  })

  it('reads sources', () => {
    expect(sourceOf({ 'site.name': 'db' }, 'site.name')).toBe('db')
    expect(sourceOf({ 'site.name': 'weird' }, 'site.name')).toBeNull()
    expect(sourceOf(undefined, 'site.name')).toBeNull()
  })

  it('formats read-only values', () => {
    expect(formatReadonlyValue({ code: 'USD', symbol: '$', decimals: 2 })).toBe('USD（$，2 位小数）')
    expect(formatReadonlyValue(true)).toBe('是')
    expect(formatReadonlyValue(null)).toBe('未设置')
    expect(formatReadonlyValue([])).toBe('无')
    expect(formatReadonlyValue([{ id: 'github', type: 'github', displayName: 'GitHub' }, 'dev'])).toBe('GitHub（github）、dev')
    const entries = readonlyEntries({ env: 'development', currency: 'USD', extra: 1 })
    expect(entries.map(e => e.key)).toEqual(['currency', 'env', 'extra'])
    expect(entries[0]!.env).toBe('OMNIGATE_CURRENCY')
  })
})

describe('settings form: notifications (SMTP, phase6 §1)', () => {
  const withSmtp: SystemSettings = {
    ...saved,
    notifications: {
      smtp: { host: 'smtp.example.com', port: 587, security: 'starttls', username: 'mailer', passwordSet: true, from: 'OmniGate <noreply@example.com>' },
      enabled: true,
      emailRateLimitPerHour: 20,
    },
  }

  it('defaults the section for older backends and stays clean', () => {
    const f = formFromSettings(saved)
    expect(f.notifications).toMatchObject({ host: '', port: 587, security: 'starttls', passwordAction: 'keep', enabled: true, emailRateLimitPerHour: 20 })
    expect(isSectionDirty('notifications', f.notifications, saved)).toBe(false)
    expect(isSectionDirty('notifications', formFromSettings(withSmtp).notifications, withSmtp)).toBe(false)
  })

  it('patches nested SMTP fields and keeps the password write-only', () => {
    const f = formFromSettings(withSmtp)
    f.notifications.host = ' mail.example.org '
    f.notifications.port = '465'
    f.notifications.security = 'tls'
    expect(changedFields('notifications', f.notifications, withSmtp)).toEqual(['smtp.host', 'smtp.port', 'smtp.security'])
    expect(buildSectionPatch('notifications', f.notifications, withSmtp, 7)).toEqual({
      version: 7,
      settings: { notifications: { smtp: { host: 'mail.example.org', port: 465, security: 'tls' } } },
    })
  })

  it('sends a new password on replace and "" on clear', () => {
    const f = formFromSettings(withSmtp)
    f.notifications.passwordAction = 'replace'
    f.notifications.password = 's3cret'
    f.notifications.enabled = false
    expect(buildSectionPatch('notifications', f.notifications, withSmtp, 1)).toEqual({
      version: 1,
      settings: { notifications: { smtp: { password: 's3cret' }, enabled: false } },
    })
    f.notifications.passwordAction = 'clear'
    f.notifications.enabled = true
    expect(buildSectionPatch('notifications', f.notifications, withSmtp, 1)).toEqual({ version: 1, settings: { notifications: { smtp: { password: '' } } } })
    f.notifications.passwordAction = 'keep'
    expect(isSectionDirty('notifications', f.notifications, withSmtp)).toBe(false)
  })

  it('builds nested reset patches and maps keys back to form fields', () => {
    expect(buildResetPatch('notifications.smtp.host', 3)).toEqual({ version: 3, settings: { notifications: { smtp: { host: null } } } })
    expect(buildResetPatch('notifications.enabled', 3)).toEqual({ version: 3, settings: { notifications: { enabled: null } } })
    expect(formFieldOfKey('notifications.smtp.host')).toEqual(['notifications', 'host'])
    expect(formFieldOfKey('notifications.smtp.password')).toEqual(['notifications', 'passwordAction'])
    expect(formFieldOfKey('notifications.emailRateLimitPerHour')).toEqual(['notifications', 'emailRateLimitPerHour'])
    expect(formFieldOfKey('site.name')).toEqual(['site', 'name'])
  })

  it('validates SMTP fields', () => {
    const f = formFromSettings(withSmtp).notifications
    expect(validateSection('notifications', f)).toEqual({})
    f.host = 'smtp://bad:25'
    f.port = 70000
    f.from = 'not an address'
    f.passwordAction = 'replace'
    f.emailRateLimitPerHour = 0
    expect(Object.keys(validateSection('notifications', f)).sort()).toEqual([
      'notifications.emailRateLimitPerHour',
      'notifications.smtp.from',
      'notifications.smtp.host',
      'notifications.smtp.password',
      'notifications.smtp.port',
    ])
    const g = formFromSettings(saved).notifications
    g.host = 'smtp.example.com'
    expect(validateSection('notifications', g)).toEqual({ 'notifications.smtp.from': '配置 SMTP 服务器时必须填写发件人' })
    expect(isValidFromAddress('noreply@example.com')).toBe(true)
    expect(isValidFromAddress('OmniGate <noreply@example.com>')).toBe(true)
    expect(isValidFromAddress('OmniGate <noreply>')).toBe(false)
  })
})
