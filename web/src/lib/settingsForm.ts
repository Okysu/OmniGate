// System settings page: per-section form state, normalisation, dirty tracking,
// client-side validation (contract §3 limits) and PATCH bodies.
import type { NotificationSettings, RegistrationMode, RetryOn, SettingSource, SettingsPatch, SettingsPatchBody, SettingsSection, SmtpSecurity, SystemSettings } from './types'
import { fromNano, isValidAmount, toNano } from './money'
import { DEFAULT_RETRY_ON, normalizeRetryOn } from './retry'
import { isHttpUrl } from './site'

export const SECTIONS: SettingsSection[] = ['site', 'auth', 'billing', 'gateway', 'notifications']

/**
 * Field paths per section, relative to the section. `notifications` nests its SMTP
 * fields (`smtp.host` → setting `notifications.smtp.host`, phase6-api.md §1).
 */
export const SECTION_FIELDS: Record<SettingsSection, string[]> = {
  site: ['name', 'announcement', 'landingEnabled', 'docsUrl', 'publicModelPlaza'],
  auth: ['registrationMode', 'allowedEmailDomains'],
  billing: ['enforce', 'signupCredit'],
  gateway: ['maxAttempts', 'retryOn', 'logRetentionDays'],
  notifications: ['smtp.host', 'smtp.port', 'smtp.security', 'smtp.username', 'smtp.password', 'smtp.from', 'enabled', 'emailRateLimitPerHour'],
}

export const SECTION_TITLES: Record<SettingsSection, string> = {
  site: '站点',
  auth: '登录与注册',
  billing: '计费',
  gateway: '网关',
  notifications: '邮件（SMTP）',
}

export const FIELD_LABELS: Record<string, string> = {
  'site.name': '站点名称',
  'site.announcement': '控制台公告',
  'site.landingEnabled': '公开首页',
  'site.docsUrl': '文档链接',
  'site.publicModelPlaza': '公开模型广场',
  'auth.registrationMode': '注册模式',
  'auth.allowedEmailDomains': '允许的邮箱域名',
  'billing.enforce': '余额强制',
  'billing.signupCredit': '新用户赠送余额',
  'gateway.maxAttempts': '默认最多尝试次数',
  'gateway.logRetentionDays': '请求日志保留天数',
  'gateway.retryOn': '默认重试条件（未命中路由规则时）',
  'notifications.smtp.host': 'SMTP 服务器',
  'notifications.smtp.port': '端口',
  'notifications.smtp.security': '加密方式',
  'notifications.smtp.username': '用户名',
  'notifications.smtp.password': '密码',
  'notifications.smtp.from': '发件人',
  'notifications.enabled': '通知总开关',
  'notifications.emailRateLimitPerHour': '每用户每小时邮件上限',
}

export const SMTP_SECURITY: SmtpSecurity[] = ['starttls', 'tls', 'none']

export const SMTP_SECURITY_LABELS: Record<SmtpSecurity, string> = {
  starttls: 'STARTTLS',
  tls: 'TLS',
  none: '无（仅开发）',
}

export const SMTP_SECURITY_HINTS: Record<SmtpSecurity, string> = {
  starttls: '先以明文连接再升级为 TLS，通常使用 587 端口（默认）。',
  tls: '隐式 TLS，连接建立即加密，通常使用 465 端口。',
  none: '不加密，仅限开发环境；生产环境会被服务端拒绝。',
}

/** Default port per security mode (applied when the port still holds the other default). */
export const SMTP_DEFAULT_PORTS: Record<SmtpSecurity, number> = { starttls: 587, tls: 465, none: 25 }

export const SOURCE_LABELS: Record<SettingSource, string> = {
  db: '数据库',
  env: '环境变量',
  default: '默认值',
}

export const SOURCE_DESCRIPTIONS: Record<SettingSource, string> = {
  db: '在设置页中修改过，保存在数据库中（优先于环境变量）。',
  env: '来自服务端环境变量。',
  default: '未设置，使用内置默认值。',
}

export const REGISTRATION_MODES: RegistrationMode[] = ['open', 'restricted', 'closed']

export const REGISTRATION_LABELS: Record<RegistrationMode, string> = {
  open: '开放注册',
  restricted: '受限注册',
  closed: '关闭注册',
}

export const REGISTRATION_DESCRIPTIONS: Record<RegistrationMode, string> = {
  open: '任何能通过已配置登录方式认证的人，首次登录时自动创建账号（普通用户）。',
  restricted: '只有邮箱域名在下方允许列表中（或在环境变量 OMNIGATE_AUTH_ALLOWED_IDENTITIES 中列出）的用户，首次登录时才会自动创建账号；其他人会被拒绝。',
  closed: '不再自动创建新账号；只有已存在的用户（以及 OMNIGATE_BOOTSTRAP_ADMINS 中的管理员）可以登录。',
}

export const LIMITS = {
  nameMax: 50,
  announcementMax: 500,
  maxAttempts: [1, 5],
  logRetentionDays: [0, 3650],
  domainsMax: 100,
  smtpPort: [1, 65535],
  emailRateLimitPerHour: [1, 10000],
} as const

export const DEFAULT_NOTIFICATION_SETTINGS: NotificationSettings = {
  smtp: { host: '', port: 587, security: 'starttls', username: '', passwordSet: false, from: '' },
  enabled: true,
  emailRateLimitPerHour: 20,
}

/** Contract defaults (also the placeholder form before the first load). */
export const DEFAULT_SETTINGS: SystemSettings = {
  site: { name: 'OmniGate', announcement: '', landingEnabled: true, docsUrl: '', publicModelPlaza: true },
  auth: { registrationMode: 'restricted', allowedEmailDomains: [] },
  billing: { enforce: false, signupCredit: '0' },
  gateway: { maxAttempts: 3, logRetentionDays: 90, retryOn: [...DEFAULT_RETRY_ON] },
  notifications: DEFAULT_NOTIFICATION_SETTINGS,
}

/** Write-only SMTP password: keep the stored one, replace it, or clear it. */
export type SecretAction = 'keep' | 'replace' | 'clear'

export interface NotificationsForm {
  host: string
  port: number | string
  security: SmtpSecurity
  username: string
  from: string
  passwordAction: SecretAction
  /** New password (only with `passwordAction: 'replace'`). */
  password: string
  enabled: boolean
  emailRateLimitPerHour: number | string
}

/** Values bound to the inputs (numbers may be strings while typing). */
export interface SettingsForm {
  site: { name: string, announcement: string, landingEnabled: boolean, docsUrl: string, publicModelPlaza: boolean }
  auth: { registrationMode: RegistrationMode, allowedEmailDomains: string[] }
  billing: { enforce: boolean, signupCredit: string }
  gateway: { maxAttempts: number | string, logRetentionDays: number | string, retryOn: RetryOn[] }
  notifications: NotificationsForm
}

function isSmtpSecurity(v: unknown): v is SmtpSecurity {
  return v === 'starttls' || v === 'tls' || v === 'none'
}

function notificationsFormFrom(n: NotificationSettings | undefined | null): NotificationsForm {
  const d = DEFAULT_NOTIFICATION_SETTINGS
  const smtp = n?.smtp ?? d.smtp
  return {
    host: smtp.host ?? '',
    port: typeof smtp.port === 'number' ? smtp.port : d.smtp.port,
    security: isSmtpSecurity(smtp.security) ? smtp.security : 'starttls',
    username: smtp.username ?? '',
    from: smtp.from ?? '',
    passwordAction: 'keep',
    password: '',
    enabled: n ? n.enabled !== false : d.enabled,
    emailRateLimitPerHour: typeof n?.emailRateLimitPerHour === 'number' ? n.emailRateLimitPerHour : d.emailRateLimitPerHour,
  }
}

export function formFromSettings(s: SystemSettings): SettingsForm {
  return {
    // publicModelPlaza: default true, also when an older backend does not send it.
    site: { name: s.site.name ?? '', announcement: s.site.announcement ?? '', landingEnabled: s.site.landingEnabled !== false, docsUrl: s.site.docsUrl ?? '', publicModelPlaza: s.site.publicModelPlaza !== false },
    auth: { registrationMode: s.auth.registrationMode, allowedEmailDomains: [...(s.auth.allowedEmailDomains ?? [])] },
    billing: { enforce: !!s.billing.enforce, signupCredit: s.billing.signupCredit ?? '0' },
    // retryOn: absent on older backends → the contract default (so the form is not dirty).
    gateway: { maxAttempts: s.gateway.maxAttempts, logRetentionDays: s.gateway.logRetentionDays, retryOn: Array.isArray(s.gateway.retryOn) ? normalizeRetryOn(s.gateway.retryOn) : [...DEFAULT_RETRY_ON] },
    // notifications: absent on older backends → contract defaults (the card explains it is unsupported).
    notifications: notificationsFormFrom(s.notifications),
  }
}

/** Fresh copy of one section of the form for a reset after saving / reloading. */
export function sectionFromSettings<S extends SettingsSection>(s: SystemSettings, section: S): SettingsForm[S] {
  return formFromSettings(s)[section]
}

/** `@Example.COM ` → `example.com`. */
export function normalizeDomain(raw: string): string {
  return raw.trim().toLowerCase().replace(/^@+/, '').replace(/\.+$/, '')
}

const DOMAIN_RE = /^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9-]{2,63}$/

export function isValidDomain(d: string): boolean {
  return DOMAIN_RE.test(d)
}

/** Splits pasted text ("a.com, b.com\nc.com") into normalised, de-duplicated domains. */
export function parseDomains(text: string): string[] {
  return dedupe(text.split(/[\s,，;；]+/).map(normalizeDomain).filter(Boolean))
}

function dedupe(list: string[]): string[] {
  return [...new Set(list)]
}

function canonicalAmount(v: string): string {
  const s = v.trim()
  if (!isValidAmount(s))
    return s
  return fromNano(toNano(s))
}

function toInt(v: number | string): number {
  return typeof v === 'number' ? v : Number(String(v).trim())
}

/** Canonical (wire) values of a section — what would be sent. */
export function normalizeSection<S extends SettingsSection>(section: S, f: SettingsForm[S]): SystemSettings[S] {
  switch (section) {
    case 'site': {
      const v = f as SettingsForm['site']
      return { name: v.name.trim(), announcement: v.announcement.trim(), landingEnabled: v.landingEnabled, docsUrl: v.docsUrl.trim(), publicModelPlaza: v.publicModelPlaza } as SystemSettings[S]
    }
    case 'auth': {
      const v = f as SettingsForm['auth']
      return { registrationMode: v.registrationMode, allowedEmailDomains: dedupe(v.allowedEmailDomains.map(normalizeDomain).filter(Boolean)) } as SystemSettings[S]
    }
    case 'billing': {
      const v = f as SettingsForm['billing']
      return { enforce: v.enforce, signupCredit: canonicalAmount(v.signupCredit) } as SystemSettings[S]
    }
    case 'notifications': {
      const v = f as SettingsForm['notifications']
      return {
        smtp: { host: v.host.trim(), port: toInt(v.port), security: v.security, username: v.username.trim(), passwordSet: v.passwordAction === 'replace', from: v.from.trim() },
        enabled: v.enabled,
        emailRateLimitPerHour: toInt(v.emailRateLimitPerHour),
      } as SystemSettings[S]
    }
    default: {
      const v = f as SettingsForm['gateway']
      return { maxAttempts: toInt(v.maxAttempts), logRetentionDays: toInt(v.logRetentionDays), retryOn: normalizeRetryOn(v.retryOn) } as SystemSettings[S]
    }
  }
}

/** Marker for "password unchanged" in the flat value map (never sent). */
const KEEP = Symbol('keep')

/**
 * Wire values of a section keyed by the paths of {@link SECTION_FIELDS}; the SMTP
 * password is the new value (`""` = clear) or {@link KEEP}.
 */
function flatValues<S extends SettingsSection>(section: S, f: SettingsForm[S]): Record<string, unknown> {
  if (section !== 'notifications')
    return normalizeSection(section, f) as Record<string, unknown>
  const v = f as SettingsForm['notifications']
  const n = normalizeSection('notifications', v) as NotificationSettings
  return {
    'smtp.host': n.smtp.host,
    'smtp.port': n.smtp.port,
    'smtp.security': n.smtp.security,
    'smtp.username': n.smtp.username,
    'smtp.password': v.passwordAction === 'keep' ? KEEP : v.passwordAction === 'replace' ? v.password : '',
    'smtp.from': n.smtp.from,
    'enabled': n.enabled,
    'emailRateLimitPerHour': n.emailRateLimitPerHour,
  }
}

/** `{ 'smtp.host': 'x', enabled: true }` → `{ smtp: { host: 'x' }, enabled: true }`. */
function unflatten(flat: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [path, value] of Object.entries(flat)) {
    const parts = path.split('.')
    let node = out
    for (const p of parts.slice(0, -1)) {
      if (typeof node[p] !== 'object' || node[p] === null)
        node[p] = {}
      node = node[p] as Record<string, unknown>
    }
    node[parts[parts.length - 1]!] = value
  }
  return out
}

function sameValue(a: unknown, b: unknown): boolean {
  if (Array.isArray(a) && Array.isArray(b))
    return a.length === b.length && a.every((x, i) => x === b[i])
  return a === b
}

/** Fields of `section` whose (normalised) value differs from the saved settings. */
export function changedFields<S extends SettingsSection>(section: S, f: SettingsForm[S], saved: SystemSettings): string[] {
  const now = flatValues(section, f)
  const before = flatValues(section, sectionFromSettings(saved, section))
  return SECTION_FIELDS[section].filter(k => !sameValue(now[k], before[k]))
}

export function isSectionDirty<S extends SettingsSection>(section: S, f: SettingsForm[S], saved: SystemSettings): boolean {
  return changedFields(section, f, saved).length > 0
}

/** PATCH body with only the changed fields of one section. */
export function buildSectionPatch<S extends SettingsSection>(section: S, f: SettingsForm[S], saved: SystemSettings, version: number): SettingsPatch {
  const now = flatValues(section, f)
  const changed: Record<string, unknown> = {}
  for (const k of changedFields(section, f, saved))
    changed[k] = now[k]
  return { version, settings: { [section]: unflatten(changed) } as SettingsPatchBody }
}

/**
 * PATCH body that resets one field to its env / default value
 * (`notifications.smtp.host` → `{notifications: {smtp: {host: null}}}`).
 */
export function buildResetPatch(fieldKey: string, version: number): SettingsPatch {
  return { version, settings: unflatten({ [fieldKey]: null }) as SettingsPatchBody }
}

/** Setting key → [section, form field]: `notifications.smtp.host` → `['notifications', 'host']`. */
export function formFieldOfKey(fieldKey: string): [SettingsSection, string] {
  const [section, ...rest] = fieldKey.split('.') as [SettingsSection, ...string[]]
  if (section === 'notifications' && rest[0] === 'smtp')
    return [section, rest[1] === 'password' ? 'passwordAction' : rest.slice(1).join('.')]
  return [section, rest.join('.')]
}

function intRangeError(v: number | string, [min, max]: readonly [number, number]): string | null {
  const s = String(v).trim()
  const n = Number(s)
  if (s === '' || !Number.isInteger(n) || n < min || n > max)
    return `范围为 ${min} 到 ${max} 的整数`
  return null
}

/** Client-side checks; keys are full field paths (`site.name`) like the server's 422 details. */
export function validateSection<S extends SettingsSection>(section: S, f: SettingsForm[S]): Record<string, string> {
  const e: Record<string, string> = {}
  if (section === 'site') {
    const v = f as SettingsForm['site']
    const name = v.name.trim()
    if (!name || name.length > LIMITS.nameMax)
      e['site.name'] = `长度应为 1–${LIMITS.nameMax} 个字符`
    if (v.announcement.trim().length > LIMITS.announcementMax)
      e['site.announcement'] = `最多 ${LIMITS.announcementMax} 个字符`
    const docs = v.docsUrl.trim()
    if (docs && !isHttpUrl(docs))
      e['site.docsUrl'] = '必须是 http(s) 开头的完整地址，或留空'
  }
  else if (section === 'auth') {
    const v = f as SettingsForm['auth']
    if (!REGISTRATION_MODES.includes(v.registrationMode))
      e['auth.registrationMode'] = '请选择注册模式'
    const domains = v.allowedEmailDomains.map(normalizeDomain).filter(Boolean)
    const bad = domains.filter(d => !isValidDomain(d))
    if (bad.length)
      e['auth.allowedEmailDomains'] = `域名格式无效：${bad.join('、')}`
    else if (domains.length > LIMITS.domainsMax)
      e['auth.allowedEmailDomains'] = `最多 ${LIMITS.domainsMax} 个域名`
  }
  else if (section === 'billing') {
    const v = f as SettingsForm['billing']
    if (!isValidAmount(v.signupCredit.trim()))
      e['billing.signupCredit'] = '请输入不小于 0 的金额（最多 9 位小数）'
  }
  else if (section === 'notifications') {
    const v = f as SettingsForm['notifications']
    const host = v.host.trim()
    if (host && !/^[\w.-]+$/.test(host))
      e['notifications.smtp.host'] = '只填写主机名或 IP，不含协议与端口'
    const p = intRangeError(v.port, LIMITS.smtpPort)
    if (p)
      e['notifications.smtp.port'] = p
    if (!isSmtpSecurity(v.security))
      e['notifications.smtp.security'] = '请选择加密方式'
    const from = v.from.trim()
    if (host && !from)
      e['notifications.smtp.from'] = '配置 SMTP 服务器时必须填写发件人'
    else if (from && !isValidFromAddress(from))
      e['notifications.smtp.from'] = '格式为 noreply@example.com 或 OmniGate <noreply@example.com>'
    if (v.passwordAction === 'replace' && !v.password)
      e['notifications.smtp.password'] = '请输入新密码，或取消更换'
    const r = intRangeError(v.emailRateLimitPerHour, LIMITS.emailRateLimitPerHour)
    if (r)
      e['notifications.emailRateLimitPerHour'] = r
  }
  else {
    const v = f as SettingsForm['gateway']
    const a = intRangeError(v.maxAttempts, LIMITS.maxAttempts)
    if (a)
      e['gateway.maxAttempts'] = a
    const d = intRangeError(v.logRetentionDays, LIMITS.logRetentionDays)
    if (d)
      e['gateway.logRetentionDays'] = d
  }
  return e
}

const MAILBOX_RE = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/

/** `noreply@example.com` or `Display Name <noreply@example.com>`. */
export function isValidFromAddress(v: string): boolean {
  const s = v.trim()
  const m = /^(.*)<([^<>]+)>$/.exec(s)
  return m ? MAILBOX_RE.test(m[2]!.trim()) : MAILBOX_RE.test(s)
}

/** Server 422 detail keys may be prefixed with `settings.`; strip it. */
export function normalizeErrorKeys(details: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(details))
    out[k.replace(/^settings\./, '')] = v
  return out
}

export function sourceOf(sources: Record<string, string> | null | undefined, key: string): SettingSource | null {
  const s = sources?.[key]
  return s === 'db' || s === 'env' || s === 'default' ? s : null
}

// ---------------------------------------------------------------------------
// Read-only (env) configuration
// ---------------------------------------------------------------------------

export interface ReadonlyEntry {
  key: string
  label: string
  env: string
  value: string
}

const READONLY_META: Record<string, { label: string, env: string }> = {
  currency: { label: '结算币种', env: 'OMNIGATE_CURRENCY' },
  publicUrl: { label: '公开访问地址', env: 'OMNIGATE_PUBLIC_URL' },
  channelsAllowPrivateNetwork: { label: '渠道允许访问私网地址', env: 'OMNIGATE_CHANNELS_ALLOW_PRIVATE_NETWORK' },
  loginProviders: { label: '登录方式', env: 'OMNIGATE_AUTH_GITHUB_* / OMNIGATE_AUTH_OIDC' },
  env: { label: '运行环境', env: 'OMNIGATE_ENV' },
}

const READONLY_ORDER = ['currency', 'publicUrl', 'channelsAllowPrivateNetwork', 'loginProviders', 'env']

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/** Readable text for an env-config value of unknown shape. */
export function formatReadonlyValue(v: unknown): string {
  if (v === null || v === undefined || v === '')
    return '未设置'
  if (typeof v === 'boolean')
    return v ? '是' : '否'
  if (typeof v === 'string' || typeof v === 'number')
    return String(v)
  if (Array.isArray(v))
    return v.length ? v.map(formatReadonlyValue).join('、') : '无'
  if (isRecord(v)) {
    if (typeof v.code === 'string') {
      const extra = [typeof v.symbol === 'string' ? v.symbol : '', typeof v.decimals === 'number' ? `${v.decimals} 位小数` : ''].filter(Boolean).join('，')
      return extra ? `${v.code}（${extra}）` : v.code
    }
    const name = v.displayName ?? v.name ?? v.id
    if (typeof name === 'string')
      return typeof v.type === 'string' && v.type !== name ? `${name}（${v.type}）` : name
    return JSON.stringify(v)
  }
  return String(v)
}

export function readonlyEntries(ro: Record<string, unknown> | null | undefined): ReadonlyEntry[] {
  if (!ro)
    return []
  const keys = [...READONLY_ORDER.filter(k => k in ro), ...Object.keys(ro).filter(k => !READONLY_ORDER.includes(k)).sort()]
  return keys.map(k => ({
    key: k,
    label: READONLY_META[k]?.label ?? k,
    env: READONLY_META[k]?.env ?? '',
    value: formatReadonlyValue(ro[k]),
  }))
}
