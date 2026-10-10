// Types mirroring the backend JSON contract (camelCase).

export type Role = 'system_admin' | 'channel_admin' | 'user' | 'auditor'
export type UserStatus = 'active' | 'disabled'
export type RegistrationMode = 'open' | 'restricted' | 'closed'

export interface SystemInfo {
  name: string
  version: string
  currency: { code: string, symbol: string, decimals: number }
  registrationMode: RegistrationMode
  /** Round 5 (system settings); absent on older backends — see `lib/site.ts` for fallbacks. */
  siteName?: string
  announcement?: string
  landingEnabled?: boolean
  docsUrl?: string
  /** `site.publicModelPlaza` (anonymous visitors may open `/models`); absent on older backends → true. */
  publicModelPlaza?: boolean
}

export interface AuthProvider {
  id: string
  type: 'github' | 'oidc'
  displayName: string
}

export interface AuthProvidersResponse {
  providers: AuthProvider[]
}

export interface UserIdentity {
  provider: string
  login: string | null
  email: string | null
}

export interface User {
  id: string
  displayName: string
  email: string | null
  avatarUrl: string | null
  role: Role
  status: UserStatus
  /** ISO 8601, UTC */
  createdAt: string
  lastLoginAt: string | null
  version: number
  identities: UserIdentity[]
  /**
   * phase7-api.md §2.1: why / until when the user is disabled. Pinned by the contract for
   * `GET /api/admin/users/{id}`; the list may or may not include them (older backends: absent).
   */
  disabledReason?: string | null
  /** null = permanent; the backend re-enables the user automatically once this passes. */
  disabledUntil?: string | null
  /** phase8 §1.3: the user's group (list and detail); absent on older backends. */
  group?: GroupRef | null
}

export interface MeResponse {
  user: User
  permissions: string[]
  /**
   * phase8 §1.3: own group with multiplier and limits. The contract says "/api/me 返回自己的
   * group" without pinning the nesting; `user.group` with the same shape is accepted too.
   */
  group?: MyGroup | null
}

// ---------------------------------------------------------------------------
// Round 6: user groups, price multipliers, limits (phase8-api.md §1–§2)
// ---------------------------------------------------------------------------

export interface GroupRef {
  id: string
  name: string
}

/** Per-user limits of a group; null = unlimited. Money is a decimal string. */
export interface GroupLimits {
  /** Requests per minute per user (all keys together). */
  rpm: number | null
  /** Requests per day per user (group time zone). */
  rpd: number | null
  /** Platform-channel spend per day (settlement currency). */
  dailySpend: string | null
  monthlySpend: string | null
}

export interface UserGroup {
  id: string
  /** 1–50, unique. */
  name: string
  /** ≤200. */
  description: string
  /** Decimal 0–100; "1" = list price, "0.8" = 20% off, "0" = platform channels free. */
  priceMultiplier: string
  limits: GroupLimits
  /** IANA; day / month boundaries. */
  timezone: string
  /** Group new users join; exactly one. */
  isDefault: boolean
  /** Read-only member count. */
  members: number
  version: number
  createdAt: string
  updatedAt: string
}

export interface UserGroupInput {
  name: string
  description: string
  priceMultiplier: string
  limits: GroupLimits
  timezone: string
  isDefault: boolean
}

export type UserGroupPatch = Partial<UserGroupInput> & { version: number }

/** `/api/me` `group`. */
export interface MyGroup extends GroupRef {
  priceMultiplier: string
  limits: GroupLimits
  timezone?: string
}

export type SpendWindow = 'day' | 'week' | 'month' | 'total'

/** phase8 §2.1: key spend limit (platform-channel charges only). */
export interface SpendLimit {
  amount: string
  window: SpendWindow
}

/** `GET /api/billing/limits` key row. */
export interface KeySpendUsage {
  id: string
  name: string
  spendLimit: SpendLimit | null
  spent: string
  resetsAt: string | null
}

/**
 * `GET /api/billing/limits` (§2.3). The contract writes `group: {...limits}` and
 * `resetsAt: {...}` without pinning the inner fields; see `lib/limits.ts` for the
 * normalisation (both a nested `limits` object and flattened limit fields are accepted).
 */
export interface BillingLimits {
  group: { id?: string, name?: string, priceMultiplier?: string, timezone?: string, limits?: Partial<GroupLimits> } & Partial<GroupLimits>
  usage: {
    rpdUsed: number
    dailySpent: string
    monthlySpent: string
    resetsAt: Record<string, string | null | undefined>
  }
  keys: KeySpendUsage[]
}

export interface Session {
  id: string
  createdAt: string
  lastSeenAt: string
  expiresAt: string
  userAgent: string
  ipPrefix: string
  current: boolean
}

export interface SessionsResponse {
  items: Session[]
}

export interface RevokeOthersResponse {
  revoked: number
}

export interface Paginated<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

export interface UserPatch {
  role?: Role
  status?: UserStatus
  /** phase7 §2.1: required (≤200) when disabling. */
  disabledReason?: string
  /** phase7 §2.1: ISO time to re-enable automatically; null = permanent. */
  disabledUntil?: string | null
  version: number
}

// ---------------------------------------------------------------------------
// Round 6 (continued): user management (phase7-api.md §2)
// ---------------------------------------------------------------------------

export interface AdminUserIdentity {
  provider: string
  subject: string
  email: string | null
  createdAt: string
}

export interface AdminUserKey {
  id: string
  name: string
  prefix: string
  status: EnabledStatus | string
  lastUsedAt: string | null
  createdAt: string
}

/** `GET /api/admin/users/{id}` (users.read). */
export interface AdminUserDetail {
  user: User & { disabledReason: string | null, disabledUntil: string | null, lastLoginAt: string | null }
  identities: AdminUserIdentity[]
  wallet: { balance: string, reserved: string } | null
  /** Newest 20. */
  subscriptions: Subscription[]
  keys: AdminUserKey[]
  sessions: { active: number }
  usage30d: { requests: number, charge: string, tokens: number }
  channels: { own: number }
}

export type UserBatchAction = 'disable' | 'enable' | 'logout' | 'set_group'

/** `POST /api/admin/users/batch` body. */
export interface UserBatchInput {
  /** 1–200. */
  ids: string[]
  action: UserBatchAction
  /** Required for `disable` (≤200). */
  reason?: string
  until?: string | null
  /** Required for `set_group` (phase8 §1.3). */
  groupId?: string
}

export interface UserBatchFailure {
  id: string
  code: string
  message: string
}

export interface UserBatchResult {
  succeeded: string[]
  failed: UserBatchFailure[]
}

export interface AuditLog {
  id: string
  actorId: string | null
  actorName: string | null
  action: string
  resourceType: string
  resourceId: string | null
  ipPrefix: string
  metadata: Record<string, unknown>
  createdAt: string
}

// ---------------------------------------------------------------------------
// Phase 1: channels, models & prices, gateway keys, logs & stats, billing.
// Mirrors server/internal/{channel,keys,pricing,requestlog,billing} JSON tags.
// Money is always a decimal string (never a JS number).
// ---------------------------------------------------------------------------

/** `custom`: channel backed by a custom-protocol plugin (phase9 §2). */
export type ChannelType = 'openai' | 'anthropic' | 'custom'
export type ChannelScope = 'private' | 'shared' | 'global'
export type EnabledStatus = 'enabled' | 'disabled'
export type HealthState = 'healthy' | 'degraded' | 'open'
export type MaxTokensField = 'max_tokens' | 'max_completion_tokens'

export type UpstreamProtocol = 'chat' | 'responses'

export interface ModelMapping {
  model: string
  upstreamModel: string
  /** openai channels only: which OpenAI API the upstream model speaks (default chat). */
  upstreamProtocol?: UpstreamProtocol
}

export interface ChannelConfig {
  headers?: Record<string, string>
  supportsResponses?: boolean
  timeoutSeconds?: number
  maxTokensField?: MaxTokensField
}

export interface ChannelHealth {
  state: HealthState
  consecutiveFailures: number
  lastError: string | null
  lastCheckedAt: string | null
}

/** The plugin version a channel is pinned to (Phase 2). */
export interface ChannelPluginRef {
  /** Plugin row id (uuid). */
  id: string
  /** Manifest id, e.g. `community.deepseek`. */
  key: string
  name: string
  version: string
  versionId: string
}

/** Resolved `channel.list.badge` contribution. */
export interface ChannelBadge {
  value: unknown
  format?: UiFormat
  currency?: string
}

/** Reduced view for channels the caller can use but not manage. */
export interface ChannelSummary {
  id: string
  name: string
  type: ChannelType
  scope: ChannelScope
  status: EnabledStatus
  models: ModelMapping[] | null
  health: ChannelHealth
  plugin?: ChannelPluginRef | null
}

/** Full view (owner or `channels.manage`). */
/** phase8 §1.2: share targets (users and / or user groups). */
export interface SharedWith {
  users: string[]
  groups: string[]
}

export interface Channel extends ChannelSummary {
  baseUrl: string
  /** Older backends send a plain user-id list; normalise with `normalizeSharedWith()`. */
  sharedWith: SharedWith | string[] | null
  priority: number
  weight: number
  config: ChannelConfig
  secret: { set: boolean, hint: string | null }
  owner: { id: string, displayName: string }
  version: number
  createdAt: string
  updatedAt: string
  /** Non-secret plugin configuration. */
  pluginConfig?: Record<string, unknown> | null
  /** Write-only plugin secrets (`x-secret` fields): whether set + masked hint. */
  secretFields?: Record<string, SecretState> | null
  badges?: ChannelBadge[] | null
  /** Round 6 (phase6-api.md §5); absent on older backends. */
  alerts?: ChannelAlerts | null
  /**
   * phase5-api.md §5.2: per-recipient invitation status of user shares (owner /
   * `channels.manage` view); absent on older backends.
   */
  shares?: ChannelShare[] | null
}

/** phase5-api.md §5: status of a user-to-user share (groups need no acceptance). */
export type ShareStatus = 'pending' | 'accepted' | 'declined'

/** Owner view of one invited user (phase5-api.md §5.2). */
export interface ChannelShare {
  userId: string
  displayName: string
  status: ShareStatus
  /** Invitation time (updated on re-invite). */
  createdAt: string
  respondedAt: string | null
}

/** A channel shared with the current user (`GET /api/channel-shares`, phase5-api.md §5.3). */
export interface IncomingShare {
  channelId: string
  name: string
  type: ChannelType
  channelStatus: EnabledStatus
  owner: { id: string, displayName: string }
  /** Logical model names only. */
  models: string[]
  /** Declined shares are not listed. */
  status: Exclude<ShareStatus, 'declined'>
  createdAt: string
  respondedAt: string | null
}

/** Per-channel alert settings (phase6-api.md §5). */
export interface ChannelAlerts {
  /** Upstream balance threshold (decimal string, in the plugin's balance currency); null = no alert. */
  balanceBelow: string | null
}

export interface SecretState {
  set: boolean
  hint: string | null
}

export type ChannelView = Channel | ChannelSummary

export function isFullChannel(c: ChannelView): c is Channel {
  return 'baseUrl' in c
}

/** Writable fields; the backend rejects unknown keys. */
export interface ChannelInput {
  name?: string
  type?: ChannelType
  baseUrl?: string
  scope?: ChannelScope
  sharedWith?: SharedWith
  status?: EnabledStatus
  priority?: number
  weight?: number
  models?: ModelMapping[]
  config?: ChannelConfig
  apiKey?: string
  version?: number
  /** Approved version of an enabled plugin; `type` follows its `extends`. */
  pluginVersionId?: string
  /** Replaces the whole non-secret plugin config. */
  pluginConfig?: Record<string, unknown>
  /** Write-only `{name: value}`; "" deletes the secret. */
  secrets?: Record<string, string>
  /** Round 6: only sent when it changes (older backends reject unknown keys). */
  alerts?: ChannelAlerts
}

export interface ChannelTestResult {
  ok: boolean
  latencyMs: number
  statusCode: number
  error: string | null
}

export type PriceKind = 'sell' | 'cost'

/** phase8 §3: one time-of-day period of a price version. */
export interface PriceSchedulePeriod {
  /** 0 = Sunday … 6 = Saturday; empty = every day. */
  days: number[]
  /** "HH:MM", inclusive. */
  start: string
  /** "HH:MM", exclusive; may cross midnight (start > end). */
  end: string
  /** Decimal 0–10, applied to every unit price. */
  multiplier: string
}

/**
 * phase10 §1: one context-length tier of a price version, as stored. Applies when the
 * request's prompt tokens (input + cache read + cache write) exceed `aboveInputTokens`;
 * the highest matching tier prices every token component of the whole request. `null`
 * = inherit the base version's field verbatim.
 */
export interface PriceTier {
  aboveInputTokens: number
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string | null
  cacheWritePerM: string | null
  imageInputPerM: string | null
  audioInputPerM: string | null
  audioOutputPerM: string | null
}

/** phase10 §1: a tier in `POST /api/admin/prices` (optional fields omitted = inherit). */
export interface PriceTierInput {
  aboveInputTokens: number
  inputPerM: string
  outputPerM: string
  cacheReadPerM?: string
  cacheWritePerM?: string
  imageInputPerM?: string
  audioInputPerM?: string
  audioOutputPerM?: string
}

export interface Price {
  id: string
  kind: PriceKind
  model: string
  channelId: string | null
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string
  cacheWritePerM: string
  perRequest: string
  /** phase7 §1.1: price per output image; absent / null on older backends or when unset. */
  perImage?: string | null
  /** phase7 §1.1: per 1M image input tokens; null = billed at `inputPerM`. */
  imageInputPerM?: string | null
  /** phase9 §1.1: per 1M audio input tokens; null = billed at `inputPerM`. */
  audioInputPerM?: string | null
  /** phase9 §1.1: per 1M audio output tokens; null = billed at `outputPerM`. */
  audioOutputPerM?: string | null
  /**
   * phase9 §1.1: per minute of input audio (transcriptions / translations; seconds rounded up).
   * The backend always sends a string: "0" = 未设置 (absent on older backends).
   */
  perMinute?: string | null
  /** phase9 §1.1: per 1M input characters (speech, Unicode code points of `input`); "0" = 未设置. */
  perMCharacters?: string | null
  /** phase8 §3: time-of-day multipliers; absent / null = none. */
  schedule?: PriceSchedulePeriod[] | null
  scheduleTimezone?: string
  /** phase10 §1: context-length tiers (ascending); absent / null = none. */
  tiers?: PriceTier[] | null
  effectiveAt: string
  createdAt: string
  createdBy: string | null
}

export interface PriceInput {
  kind: PriceKind
  model: string
  channelId?: string
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string
  cacheWritePerM: string
  perRequest: string
  /** Omitted when blank (phase7 §1.1, optional). */
  perImage?: string
  /** Omitted when blank = billed at `inputPerM`. */
  imageInputPerM?: string
  /** phase9 §1.1, all omitted when blank (audio in / out fall back to inputPerM / outputPerM). */
  audioInputPerM?: string
  audioOutputPerM?: string
  perMinute?: string
  perMCharacters?: string
  /** Omitted when no periods are set (older backends reject unknown fields). */
  schedule?: PriceSchedulePeriod[]
  scheduleTimezone?: string
  /** phase10 §1: omitted when no tiers are set (older backends reject unknown fields). */
  tiers?: PriceTierInput[]
  effectiveAt?: string
}

export interface ModelEntry {
  model: string
  channels: number
  price: Price | null
}

export type CompatMode = 'strict' | 'lenient'

/**
 * What happens when every covering subscription is out of quota: `block` (HTTP 429)
 * or `wallet` (pay as you go from the wallet). Account-wide billing preference.
 */
export type QuotaOverflow = 'block' | 'wallet'

/** Per-key override of {@link QuotaOverflow}; `''` follows the account setting. */
export type KeyQuotaOverflow = '' | QuotaOverflow

export interface KeyPolicy {
  allowedModels: string[]
  allowedChannels: string[]
  ipAllowlist: string[]
  rpm: number | null
  compatMode: CompatMode
  quotaOverflow: KeyQuotaOverflow
  /** phase8 §2.1; absent on older backends. */
  spendLimit?: SpendLimit | null
}

export interface GatewayKey {
  id: string
  name: string
  prefix: string
  status: EnabledStatus
  policy: KeyPolicy
  expiresAt: string | null
  lastUsedAt: string | null
  createdAt: string
  version: number
}

export interface KeyCreateInput {
  name: string
  policy?: KeyPolicy
  expiresAt?: string
}

export interface KeyPatch {
  name?: string
  status?: EnabledStatus
  policy?: KeyPolicy
  expiresAt?: string
  clearExpiry?: boolean
  version: number
}

export interface KeyCreated {
  key: GatewayKey
  secret: string
}

export type InboundProtocol
  = | 'openai.chat'
    | 'openai.responses'
    | 'anthropic.messages'
    | 'openai.models'
    | 'openai.embeddings'
    | 'openai.images.generations'
    | 'openai.images.edits'
    | 'openai.images.variations'
    | 'openai.audio.transcriptions'
    | 'openai.audio.translations'
    | 'openai.audio.speech'

export interface FallbackAttempt {
  channelId: string
  channelName: string
  statusCode: number
  errorClass: string | null
  durationMs: number
}

export interface RequestLog {
  id: string
  requestId: string
  startedAt: string
  user: { id: string, displayName: string } | null
  keyId: string | null
  keyName: string | null
  inbound: InboundProtocol | string
  model: string
  /** Logical model that actually served the request (differs from `model` after a fallback; Round 5). */
  servedModel?: string | null
  channelId: string | null
  channelName: string | null
  upstreamModel: string | null
  stream: boolean
  statusCode: number
  errorClass: string | null
  errorMessage: string | null
  attempts: number
  fallbackPath: FallbackAttempt[] | null
  ttftMs: number | null
  durationMs: number
  usage: {
    input: number
    output: number
    cacheRead: number
    cacheWrite: number
    reasoning: number
    estimated: boolean
    /** phase7 §1.1: image input tokens (image endpoints); absent on older backends. */
    imageInputTokens?: number
    /** phase9 §1.1: audio input / output tokens (audio endpoints); absent on older backends. */
    audioInputTokens?: number
    audioOutputTokens?: number
    /** phase9 §4 #2: Unicode code points of a speech request's `input` (character-billed requests; 0 otherwise). */
    inputCharacters?: number
  }
  /** phase7 §1.1: number of output images (image endpoints); absent / 0 otherwise. */
  imageCount?: number
  /** phase9 §1.1 / §4 #1: billed seconds of input audio, an integer rounded up (8.47 s → 9); absent / 0 otherwise. */
  audioSeconds?: number
  /** phase9 §4 #3: same as `usage.estimated`; for audio requests: no upstream usage, only `perRequest` was charged. */
  usageEstimated?: boolean
  /** null unless the viewer has `stats.all`. */
  cost: string | null
  /** Wallet charge; 0 when a plan subscription covered the request. */
  charge: string
  /** Set when a plan subscription covered the request (Phase 3). */
  subscriptionId: string | null
  /**
   * Tier of the channel that served the request (or of the last attempt when it failed),
   * relative to the requesting user (phase5-api.md §1). `own` / `shared` requests are free:
   * `charge` and `quotaCharge` are 0, `cost` is null. Absent on older backends.
   */
  channelTier?: ChannelTier | null
  /** Sell-price value counted against the plan quota (not charged to the wallet). */
  quotaCharge: string
  /** phase8 §1.1: effective group × time-of-day multiplier (decimal string); absent on older backends. */
  priceMultiplier?: string | null
  /** phase10 §1: `aboveInputTokens` of the sell-price tier applied; null = base prices / not priced / older rows. */
  priceTier?: number | null
  /** phase12 §4: session affinity outcome and rule; null when no rule applied / older backends. */
  affinity?: AffinityOutcome | null
  affinityRule?: string | null
  /** phase13 §3: detected client (rows logged before detection read as unknown); absent on older backends. */
  client?: LogClient | null
  /** NOT in the contract: shown in the tooltip when the backend splits the multiplier. */
  groupMultiplier?: string | null
  scheduleMultiplier?: string | null
}

export interface StatsTotals {
  requests: number
  success: number
  errors: number
  successRate: number
  inputTokens: number
  outputTokens: number
  cost: string | null
  charge: string
  latencyP50Ms: number | null
  latencyP95Ms: number | null
  latencyP99Ms: number | null
  ttftP50Ms: number | null
  /** phase12 §5: prompt cache reads / writes; `cacheHitRate` = cacheRead / inputTokens (null without input). Absent on older backends. */
  cacheReadTokens?: number
  cacheWriteTokens?: number
  cacheHitRate?: number | null
}

export interface StatsDaily {
  date: string
  requests: number
  errors: number
  inputTokens: number
  outputTokens: number
  charge: string
}

export interface StatsByModel {
  model: string
  requests: number
  errors: number
  inputTokens: number
  outputTokens: number
  charge: string
}

export interface StatsByChannel {
  channelId: string | null
  channelName: string | null
  requests: number
  errors: number
  latencyP95Ms: number | null
  /** phase12 §5: prompt tokens (input + cache), cache reads and their ratio; absent on older backends. */
  inputTokens?: number
  cacheReadTokens?: number
  cacheHitRate?: number | null
}

/** phase13 §4: per-client breakdown row (prompt tokens = input + cache read + cache write). */
export interface StatsByClient {
  client: string
  name: string
  requests: number
  errors: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  cacheHitRate: number | null
  /** Affinity hits and bound-session requests (hit + rebound + failover + broken + strict_failed). */
  affinityHits: number
  affinityBound: number
  affinityHitRate: number | null
  charge: string
}

export interface StatsSummary {
  from: string
  to: string
  totals: StatsTotals
  daily: StatsDaily[]
  byModel: StatsByModel[]
  byChannel: StatsByChannel[]
  /** phase12 §4: requests per session affinity outcome; absent on older backends. */
  affinity?: Partial<Record<AffinityOutcome, number>>
  /** phase13 §4: per detected client, by requests; absent on older backends. */
  byClient?: StatsByClient[]
}

export interface Wallet {
  userId: string
  balance: string
  reserved: string
  available: string
  currency: string
  version: number
}

export type LedgerKind = 'grant' | 'charge' | 'refund' | 'adjust'

export interface LedgerEntry {
  id: string
  kind: LedgerKind | string
  amount: string
  balanceAfter: string
  refType: 'request' | 'redeem' | 'admin' | string
  refId: string
  note: string | null
  createdAt: string
}

export type RedeemResult
  = | { kind: 'wallet_credit', amount: string, wallet: Wallet }
    | { kind: 'plan', subscription: Subscription }

export interface BillingSettings {
  enforce: boolean
  version: number
}

export type BatchStatus = 'active' | 'disabled'

export type RedeemBatchKind = 'wallet_credit' | 'plan'

export interface RedeemBatch {
  id: string
  kind: RedeemBatchKind | string
  /** wallet_credit only (null for plan batches). */
  amount: string | null
  /** plan batches only. */
  planId: string | null
  planName: string | null
  periods: number | null
  count: number
  redeemed: number
  maxRedemptionsPerCode: number
  perUserLimit: number
  validFrom: string | null
  expiresAt: string | null
  note: string | null
  status: BatchStatus
  createdAt: string
  createdBy: string
  version: number
}

export interface RedeemBatchInput {
  /** Omitted → wallet_credit. */
  kind?: RedeemBatchKind
  /** wallet_credit only. */
  amount?: string
  /** plan only. */
  planId?: string
  /** plan only, 1–120. */
  periods?: number
  count: number
  maxRedemptionsPerCode?: number
  perUserLimit?: number
  validFrom?: string
  expiresAt?: string
  note?: string
}

export interface RedeemBatchCreated {
  batch: RedeemBatch
  codes: string[]
}

export interface WalletAdjustInput {
  amount: string
  note: string
  version: number
}

export interface WalletAdjusted {
  wallet: Wallet
  entry: LedgerEntry
}

// ---------------------------------------------------------------------------
// Phase 3: plans & periodic quotas. Mirrors server/internal/subscription JSON.
// ---------------------------------------------------------------------------

/** Built-in meters; phase9 §3 adds `custom:<pluginKey>.<meter>` (billing plugins), typed as plain strings. */
export type QuotaMeter = 'requests' | 'tokens.input' | 'tokens.output' | 'tokens.total' | 'charge' | 'images' | 'audio_seconds'
export type WindowKind = 'calendar' | 'rolling' | 'session' | 'period' | 'lifetime'
export type CalendarUnit = 'day' | 'week' | 'month'

/** Only the fields of `kind` are present (the server omits the others). */
export interface QuotaWindow {
  kind: WindowKind | string
  /** calendar */
  unit?: CalendarUnit | string
  /** calendar: IANA name (stored as "UTC" when omitted). */
  timezone?: string
  /** rolling / session: 5m–31d */
  duration?: string
  /** period: 1h–366d */
  every?: string
}

export interface QuotaRule {
  id: string
  label: string
  meter: QuotaMeter | string
  window: QuotaWindow
  /** Decimal; integer for requests / tokens, money for charge. */
  limit: string
  /** Empty = every model the plan covers. */
  models: string[]
  /** model → decimal multiplier (0–1000); unlisted models count ×1. */
  modelWeights: Record<string, string>
  /**
   * phase9 §3, NOT pinned by the contract: label / unit of a `custom:` meter as
   * snapshotted by the server (read-only; never sent back).
   */
  meterLabel?: string
  meterUnit?: string
  /** phase9 §3: billing plugin version pinned when the plan is saved (read-only; ignored on input). */
  pluginVersionId?: string | null
}

/** phase9 §3: `GET /api/admin/billing/meters` item (built-in and billing-plugin meters). */
export interface MeterOption {
  meter: string
  label: string
  unit?: string
  builtin: boolean
  plugin: { id: string, key: string, name: string, version: string, versionId: string } | null
  /** The rule limit must be an integer. */
  integer: boolean
}

export type PlanStatus = 'active' | 'archived'

export interface Plan {
  id: string
  name: string
  description: string
  listPrice: string | null
  /** Length of one period, e.g. "30d" (1h–366d). */
  duration: string
  /** Empty = all models. */
  models: string[]
  rules: QuotaRule[]
  stackable: boolean
  status: PlanStatus
  /** Live (active, unexpired) subscriptions. */
  subscribers: number
  version: number
  createdAt: string
  updatedAt: string
}

/** User-facing catalog entry (`GET /api/plans`): no admin-only data (subscribers, status, version). */
export interface CatalogPlan {
  id: string
  name: string
  description: string
  listPrice: string | null
  duration: string
  models: string[]
  rules: QuotaRule[]
  stackable: boolean
}

export interface PlanInput {
  name: string
  description: string
  listPrice: string | null
  duration: string
  models: string[]
  rules: QuotaRule[]
  stackable: boolean
  status?: PlanStatus
}

export type PlanPatch = Partial<PlanInput> & { version: number }

export interface RuleUsage extends QuotaRule {
  /** Weighted usage in the current window. */
  used: string
  /** null: session window not started yet. */
  windowStart: string | null
  /** null: lifetime, or rolling / session with nothing to reset. */
  resetsAt: string | null
  remaining: string
  exceeded: boolean
}

export type SubscriptionStatus = 'active' | 'expired' | 'cancelled'
export type SubscriptionSource = 'admin' | 'redeem'

export interface Subscription {
  id: string
  user: { id: string, displayName: string }
  /** Name from the grant-time snapshot. */
  plan: { id: string, name: string }
  status: SubscriptionStatus | string
  startsAt: string
  endsAt: string
  source: SubscriptionSource | string
  models: string[]
  rules: RuleUsage[]
  createdAt: string
}

/** `GET / PUT /api/billing/preferences` (billing.own). */
export interface BillingPreferences {
  quotaOverflow: QuotaOverflow
}

export interface GrantInput {
  userId: string
  planId: string
  periods: number
}

/**
 * Bulk subscription target (phase7-api.md §3): explicit subscription ids, or every active
 * subscription of a plan (`planId: null` = every plan).
 */
export type SubscriptionTarget = { ids: string[] } | { planId: string | null, status: 'active' }

/** `POST /api/admin/billing/subscriptions/reset-quota` body. */
export interface ResetQuotaInput {
  target: SubscriptionTarget
  /** Rule ids; null = every rule. */
  rules: string[] | null
  includeLifetime: boolean
  /** ≤200. */
  note: string
}

/** `POST /api/admin/billing/subscriptions/extend` body. */
export interface ExtendSubscriptionsInput {
  target: SubscriptionTarget
  /** 1h–366d, e.g. "7d". */
  duration: string
  note: string
}

export interface BulkSubscriptionResult {
  affected: number
  /** At most the first 500 ids. */
  subscriptions: string[]
}

// ---------------------------------------------------------------------------
// Round 11: quota reset cards (phase11-api.md §2)
// ---------------------------------------------------------------------------

/** `5h`: 5-hour windows; `weekly`: 7-day windows; `both`: both at once. */
export type ResetCardKind = '5h' | 'weekly' | 'both'
/** `expired` is derived (available and `expiresAt` passed). */
export type ResetCardStatus = 'available' | 'used' | 'expired' | 'revoked'

/** Recipients of a batch; name snapshots are filled by the server. */
export type ResetCardTarget =
  | { type: 'users', userIds: string[] }
  | { type: 'group', groupId: string, groupName?: string }
  | { type: 'plan', planId: string, planName?: string }
  | { type: 'all' }

export interface ResetCardCounts {
  issued: number
  used: number
  available: number
  expired: number
  revoked: number
}

export interface ResetCardBatch {
  id: string
  kind: ResetCardKind
  /** Cards per recipient. */
  quantity: number
  recipients: number
  target: ResetCardTarget
  /** Plan restriction; [] = every plan. */
  plans: { id: string, name: string }[]
  expiresAt: string | null
  note: string
  status: 'active' | 'revoked'
  counts: ResetCardCounts
  createdBy: { id: string, displayName: string }
  createdAt: string
  revokedAt: string | null
}

export interface ResetCard {
  id: string
  batchId: string
  user: { id: string, displayName: string }
  kind: ResetCardKind
  status: ResetCardStatus | string
  expiresAt: string | null
  /** Plan restriction; [] = every plan. */
  plans: { id: string, name: string }[]
  note: string
  createdAt: string
  usedAt: string | null
  /** The subscription the card was used on. */
  subscription: { id: string, planName: string } | null
  revokedAt: string | null
}

/** `GET /api/billing/reset-cards`. */
export interface MyResetCards {
  /** Usable cards first (soonest to expire first), then the rest; at most 500. */
  items: ResetCard[]
  /** Usable cards by kind. */
  available: Record<ResetCardKind, number>
}

/** `POST /api/admin/billing/reset-cards/batches` body. */
export interface ResetCardIssueInput {
  kind: ResetCardKind
  /** 1–100 per recipient. */
  quantity: number
  expiresAt: string | null
  /** Plan restriction; [] = every plan. */
  planIds: string[]
  /** ≤200. */
  note: string
  target: ResetCardTarget
}

/** Issue result (dry run: `batch` null). */
export interface ResetCardIssueResult {
  recipients: number
  cards: number
  batch: ResetCardBatch | null
}

/** A subscription a card can be used on, with the rules it would reset. */
export interface ResetCardTargetSubscription {
  id: string
  plan: { id: string, name: string }
  endsAt: string
  rules: RuleUsage[]
  /** Some affected window has usage. */
  hasUsage: boolean
}

/** `GET /api/billing/reset-cards/{id}/preview`. */
export interface ResetCardPreview {
  card: ResetCard
  /** Server time of the preview. */
  now: string
  /** Applicable live subscriptions only (empty for unusable cards). */
  subscriptions: ResetCardTargetSubscription[]
}

/** `POST /api/billing/reset-cards/{id}/use`. */
export interface ResetCardUseResult {
  card: ResetCard
  subscription: Subscription
  /** Ids of the rules that were reset. */
  rules: string[]
}

// ---------------------------------------------------------------------------
// Phase 2: plugins. Mirrors server/internal/plugin JSON tags.
// ---------------------------------------------------------------------------

export type PluginSource = 'builtin' | 'bundled' | 'upload' | 'editor'
export type PluginApproval = 'pending' | 'approved' | 'rejected'
export type PluginExtends = 'openai.chat' | 'anthropic.messages'
export type PluginTemplate = 'openai-compatible' | 'anthropic-compatible' | 'custom-protocol' | 'blank'
/** phase9 §2: `custom` = the plugin implements the whole upstream protocol (buildRequest / parseResponse / parseStream). */
export type PluginProtocol = 'custom'
/** phase9 §3: manifest `kind` (a plugin may be both). Absent on older backends = channel. */
export type PluginKind = 'channel' | 'billing'

/** phase9 §3: a meter declared by a billing plugin (`computeUnits` lives in code). */
export interface BillingMeterDecl {
  label: string
  unit?: string
}

/** phase9 §3: a meter as listed on `Plugin.meters`. */
export interface BillingMeterInfo extends BillingMeterDecl {
  name: string
}
export type CapabilityOutput = 'balance' | 'quota' | 'models' | 'usage' | 'health' | 'json'

export interface PluginVersionSummary {
  id: string
  version: string
  approval: PluginApproval
  publishedAt: string
  publishedBy: string | null
  contentHash: string
  riskCount: number
}

export interface Plugin {
  id: string
  key: string
  name: string
  description: string
  author: string
  homepage: string
  source: PluginSource
  status: EnabledStatus
  version: number
  createdAt: string
  updatedAt: string
  /** Newest approved version. */
  latest: PluginVersionSummary | null
  /** Newest version awaiting approval. */
  pending: PluginVersionSummary | null
  /** Channels pinned to any version of this plugin. */
  channels: number
  hasDraft: boolean
  /** `extends` of the latest approved version ("" when none, and for custom-protocol / billing-only plugins). */
  extends: PluginExtends | ''
  /** phase9 §2: `protocol` of the latest approved version; "" = inherits `extends`. Absent on older backends. */
  protocol?: PluginProtocol | '' | string
  /** phase9 §3: `kind` of the latest approved version (server defaults to ["channel"]). Absent on older backends. */
  kind?: (PluginKind | string)[] | PluginKind | string | null
  /** phase9 §3: billing meters of the latest approved version (`[]` unless kind has billing). */
  meters?: BillingMeterInfo[] | Record<string, BillingMeterDecl> | null
  /** Only on `GET /api/plugins/{id}`, newest first. */
  versions?: PluginVersionSummary[]
}

export interface ModelDefault {
  model: string
  upstreamModel: string
}

export interface StorageQuota {
  maxKeys: number
  maxBytes: number
}

export interface PluginPermissions {
  network: string[] | null
  secrets: string[] | null
  storage?: StorageQuota | null
  schedule: string[] | null
  dangerous: string[] | null
}

export interface CapabilityDecl {
  output: CapabilityOutput | string
  userTriggerable: boolean
  schedule?: { minInterval: string } | null
  cacheTtl?: string
  timeout?: string
  label?: string
}

export type SchemaPropertyType = 'string' | 'number' | 'integer' | 'boolean'

export interface SchemaProperty {
  type: SchemaPropertyType
  title?: string
  description?: string
  default?: unknown
  enum?: unknown[]
  minimum?: number
  maximum?: number
  minLength?: number
  maxLength?: number
  pattern?: string
  'x-secret'?: boolean
  'x-group'?: string
  'x-help'?: string
}

export interface ConfigSchema {
  type: 'object'
  properties: Record<string, SchemaProperty> | null
  required?: string[] | null
}

export type UiFormat = '' | 'text' | 'number' | 'money' | 'percent' | 'boolean' | 'datetime' | 'relativeTime'
export type UiSlot = 'channel.detail.capabilities' | 'channel.detail.overview' | 'channel.list.badge'
export type UiNodeType = 'statGroup' | 'stat' | 'keyValue' | 'table' | 'progress' | 'badge' | 'alert' | 'markdown' | 'link'

export interface UiColumn {
  key: string
  label: string
  format?: UiFormat
}

export interface UiNode {
  type: UiNodeType | string
  label?: string
  text?: string
  level?: 'info' | 'warning' | 'error' | 'success' | string
  href?: string
  bind?: string
  currencyBind?: string
  valueBind?: string
  maxBind?: string
  rowsBind?: string
  format?: UiFormat | string
  columns?: UiColumn[]
  items?: UiNode[]
}

export interface UiAction {
  label: string
  capability: string
  confirm?: string
}

export interface UiContribution {
  slot: UiSlot | string
  title?: string
  component: UiNode
  actions?: UiAction[]
}

export interface PluginManifest {
  id: string
  name: string
  version: string
  sdk: number
  description?: string
  author?: string
  homepage?: string
  /** Empty / absent for custom-protocol and billing-only plugins. */
  extends: PluginExtends | string
  /** phase9 §2 (`inherits` in the contract prose is the same thing as `extends`). */
  inherits?: string
  /** phase9 §2: "custom" = full upstream protocol implemented by the plugin. */
  protocol?: PluginProtocol | string
  /** phase9 §3: plugin kinds; absent = channel. */
  kind?: (PluginKind | string)[] | PluginKind | string | null
  /** phase9 §3: billing meters declared by a billing plugin. */
  billing?: { meters?: Record<string, BillingMeterDecl> | null } | null
  entry?: string
  defaults: { baseUrl?: string, models?: ModelDefault[] | null }
  configSchema?: ConfigSchema | null
  permissions: PluginPermissions
  capabilities: Record<string, CapabilityDecl> | null
  hooks: string[] | null
  uiContributions: UiContribution[] | null
}

export interface RiskFinding {
  rule: string
  file: string
  line: number
  message: string
}

export interface PermissionDiff {
  added: string[] | null
  removed: string[] | null
}

export interface PluginVersion extends PluginVersionSummary {
  pluginId: string
  manifest: PluginManifest | null
  files: Record<string, string> | null
  risk: RiskFinding[] | null
  approvedBy: string | null
  approvedAt: string | null
  approvalNote: string | null
  permissionDiff: PermissionDiff
}

export interface PluginDraft {
  files: Record<string, string>
  baseVersionId: string | null
  updatedAt: string
  /** 0 when no draft row exists yet (the first PUT creates it). */
  version: number
}

export interface BuildDiagnostic {
  file: string
  line: number
  column: number
  severity: 'error' | 'warning' | string
  message: string
}

export interface BuildResult {
  ok: boolean
  manifest: PluginManifest | null
  diagnostics: BuildDiagnostic[] | null
  risk: RiskFinding[] | null
  bundleBytes: number
}

export interface ImportResult {
  plugin: Plugin
  draft: PluginDraft | null
  build: BuildResult
}

export interface MockFetch {
  match: { method?: string, url: string }
  response: { status: number, headers?: Record<string, string>, json?: unknown, body?: string }
}

/**
 * phase9 §2: canonical stream events produced by parseStream / endStream
 * (a subset of Chat Completions stream chunks).
 */
export type CanonicalEvent
  = | { type: 'delta', content?: string, reasoning?: string, toolCalls?: unknown[] }
    | { type: 'finish', reason: string }
    | { type: 'usage', usage: Record<string, unknown> }
    | { type: 'error', message: string, status?: number }

/** A §6 mock test case (or `{case: "tests/x.json"}`); the server rejects unknown fields. */
export interface PluginTestCase {
  name?: string
  case?: string
  capability?: string
  hook?: string
  input?: unknown
  request?: unknown
  /** phase9 §2 `hook: "parseStream"`: upstream byte chunks (UTF-8 text) fed to parseStream one call each, then endStream. */
  chunks?: string[]
  /** Same as `chunks` for binary data (appended after `chunks`). */
  chunksBase64?: string[]
  /** phase9 §2 parseResponse / normalizeError: mocked upstream response; `body` is a string or any JSON value. */
  response?: { status: number, headers?: Record<string, string>, body?: unknown }
  /** phase9 §3 billing meter test: `usage` + `billingCtx` → units. */
  meter?: string
  usage?: Record<string, unknown>
  billingCtx?: Record<string, unknown>
  config?: Record<string, unknown>
  secrets?: Record<string, string>
  baseUrl?: string
  fetch?: MockFetch[]
  /** Subset match; streaming cases compare `output` with the event array. */
  expect?: { output?: unknown, error?: string }
}

/** phase9 §2: one parseStream / endStream invocation of a streaming test. NOT pinned by the contract. */
export interface PluginStreamCall {
  hook: 'parseStream' | 'endStream' | string
  /** Index of the input chunk (absent for endStream). */
  chunk?: number
  durationMs: number
  events?: CanonicalEvent[] | null
  error?: string | null
}

export interface PluginLogLine {
  level: string
  message: string
}

export interface FetchRecord {
  method: string
  url: string
  status: number
  error?: string
  durationMs: number
}

export interface PluginTestResult {
  ok: boolean
  output: unknown
  error: string | null
  logs: PluginLogLine[] | null
  fetches: FetchRecord[] | null
  durationMs: number
  /** Why the expectation failed, if it did. */
  expectation: string | null
  /**
   * phase9 §2 streaming tests: the backend returns the event array as `output`
   * and the session's total JS time as `durationMs`. The fields below are NOT
   * sent by the backend today; they are rendered when present.
   */
  events?: CanonicalEvent[] | null
  /** Events converted to Chat Completions chunks (else converted in the browser). */
  chatChunks?: unknown[] | null
  /** Per-call timing. */
  calls?: PluginStreamCall[] | null
  /** Present when the draft did not build. */
  build?: BuildResult
}

export interface CapabilityResult {
  ok: boolean
  unsupported: boolean
  output: unknown
  error: string | null
  durationMs: number
  fetchedAt: string
  pluginVersion: string
}

export interface CapabilityInfo {
  name: string
  label: string
  output: CapabilityOutput | string
  userTriggerable: boolean
  /** Minimum schedule interval ("" when not scheduled). */
  schedule: string
}

export interface ChannelCapabilities {
  plugin: ChannelPluginRef | null
  capabilities: CapabilityInfo[]
  results: Record<string, CapabilityResult>
  ui: UiContribution[]
}

// ---------------------------------------------------------------------------
// Round 5: route rules & system settings (docs/contracts/phase4-api.md §2, §3).
// ---------------------------------------------------------------------------

export type RouteStrategy = 'priority' | 'weighted' | 'round_robin' | 'least_latency' | 'lowest_cost'
export type ProtocolPreference = 'native_first' | 'ignore'
/** Status-code based retry classes; see lib/retry.ts for labels and defaults. */
export type RetryOn = 'rate_limit' | 'server_error' | 'timeout' | 'network' | 'auth_error' | 'not_found' | 'client_error'

export interface RouteTarget {
  channelId: string
  /** null = keep the channel's own priority. */
  priority: number | null
  /** null = keep the channel's own weight. */
  weight: number | null
}

export interface RouteMatch {
  /** Exact logical model names or `*` globs ("gpt-*", "*"); 1–100. */
  models: string[]
  /** Empty = every role. */
  roles: Role[]
}

export interface RouteRetry {
  /** 1–5, including the first attempt. */
  maxAttempts: number
  retryOn: RetryOn[]
}

export interface RouteRule {
  id: string
  name: string
  description: string
  enabled: boolean
  position: number
  match: RouteMatch
  targets: RouteTarget[]
  strategy: RouteStrategy
  protocolPreference: ProtocolPreference
  retry: RouteRetry
  fallbackModels: string[]
  version: number
  createdAt: string
  updatedAt: string
}

/** Writable fields of a rule (create body; PATCH adds `version`). */
export interface RouteRuleInput {
  name: string
  description: string
  enabled: boolean
  match: RouteMatch
  targets: RouteTarget[]
  strategy: RouteStrategy
  protocolPreference: ProtocolPreference
  retry: RouteRetry
  fallbackModels: string[]
}

export type RouteRulePatch = Partial<RouteRuleInput> & { version: number }

export type PreviewInbound = 'openai.chat' | 'openai.responses' | 'anthropic.messages' | 'openai.embeddings' | 'openai.images.generations' | 'openai.audio.transcriptions' | 'openai.audio.speech'
export type BreakerState = 'closed' | 'open' | 'half_open'

export interface RoutePreviewInput {
  model: string
  /** Omitted = the current user. */
  userId?: string
  inbound: PreviewInbound
}

export interface RouteCandidate {
  channelId: string
  channelName: string
  channelType: ChannelType | string
  priority: number
  weight: number
  upstreamDialect: string
  conversionHops: number
  breaker: BreakerState | string
  latencyMs: number | null
  costPerM: string | null
  /** Why the channel is skipped (breaker open, protocol unsupported …); null when it is tried. */
  skipped: string | null
  /** Channel tier relative to the previewed user (absent on older backends). */
  tier?: ChannelTier | string
}

export interface RoutePreview {
  rule: { id: string, name: string } | null
  strategy: RouteStrategy | string
  maxAttempts: number
  candidates: RouteCandidate[]
  fallbackModels: string[]
  /** Tier order the gateway tries (own → shared → platform); absent on older backends. */
  tierOrder?: (ChannelTier | string)[]
}

export interface SystemSettings {
  site: {
    name: string
    announcement: string
    landingEnabled: boolean
    docsUrl: string
    /** phase5-api.md §3.1: anonymous visitors may open `/models` (default true; absent on older backends). */
    publicModelPlaza: boolean
  }
  auth: {
    registrationMode: RegistrationMode
    allowedEmailDomains: string[]
  }
  billing: {
    enforce: boolean
    signupCredit: string
  }
  gateway: {
    maxAttempts: number
    logRetentionDays: number
    /** Retry classes for requests no route rule matches (absent on older backends → default). */
    retryOn: RetryOn[]
    /** Session affinity (phase12-api.md §1); absent on older backends. */
    affinity?: AffinityConfig
  }
  /** Round 6 (phase6-api.md §1); absent on older backends. */
  notifications?: NotificationSettings
}

export type SmtpSecurity = 'starttls' | 'tls' | 'none'

/** `settings.notifications` (phase6-api.md §1). The SMTP password is write-only. */
export interface NotificationSettings {
  smtp: {
    host: string
    port: number
    security: SmtpSecurity
    username: string
    /** Read side only: whether a password is stored (database or env). */
    passwordSet: boolean
    from: string
  }
  enabled: boolean
  emailRateLimitPerHour: number
}

// ---------------------------------------------------------------------------
// Round 12: session affinity (phase12-api.md). Field names follow new-api.
// ---------------------------------------------------------------------------

export type AffinityMode = 'off' | 'prefer' | 'strict'
/** '' = new-api's legacy form (skip_retry_on_failure decides: true = strict, else the global mode). */
export type AffinityRuleMode = '' | 'inherit' | AffinityMode

export interface AffinityKeySource {
  /** 'gjson' (path into the JSON body) | 'request_header' (key) | 'anchor' (OmniGate: conversation fingerprint, no key/path); other values are rejected. */
  type: 'gjson' | 'request_header' | 'anchor' | string
  key?: string
  path?: string
}

export interface AffinityOperation {
  mode: 'pass_headers'
  value: string[]
  keep_origin: boolean
}

export interface AffinityRule {
  name: string
  model_regex: string[]
  path_regex: string[]
  user_agent_include: string[]
  key_sources: AffinityKeySource[]
  value_regex: string
  /** 0 = default_ttl_seconds. */
  ttl_seconds: number
  param_override_template: { operations: AffinityOperation[] } | null
  skip_retry_on_failure: boolean
  session_mode: AffinityRuleMode
  include_using_group: boolean
  include_model_name: boolean
  include_rule_name: boolean
  /** OmniGate extension: add a per-conversation prompt_cache_key to OpenAI-format upstream bodies without one. */
  inject_prompt_cache_key: boolean
  /** OmniGate extension: header (e.g. Session_id) set to a per-conversation UUID on OpenAI-format upstream requests without it; '' = off. */
  inject_session_header: string
  /** OmniGate extension (phase13 §5): detected client ids the rule applies to; [] = any client. Absent in new-api documents. */
  client_include: string[]
}

export interface AffinityConfig {
  enabled: boolean
  session_mode: AffinityMode
  switch_on_success: boolean
  keep_on_channel_disabled: boolean
  max_entries: number
  default_ttl_seconds: number
  rules: AffinityRule[]
}

/** GET /api/admin/affinity/stats. */
export interface AffinityStats {
  entries: number
  maxEntries: number
  /** Live bindings per rule name. */
  rules: Record<string, number>
}

// Round 13: client detection (phase13-api.md).

export type ClientKind = 'agent' | 'chat' | 'sdk' | 'tool' | 'unknown'

/** GET /api/clients item: a client the backend recognises (clientdetect.Known). */
export interface ClientInfo {
  id: string
  name: string
  kind: ClientKind
}

/** request_logs.client / client_version (phase13-api.md §3). */
export interface LogClient {
  id: string
  name: string
  version: string | null
}

/** request_logs.affinity (phase12-api.md §4). */
export type AffinityOutcome = 'hit' | 'new' | 'miss' | 'rebound' | 'failover' | 'broken' | 'strict_failed' | 'off'

export type SettingsSection = keyof SystemSettings
export type SettingSource = 'db' | 'env' | 'default'

/** Env-only configuration shown read-only on the settings page. */
export interface ReadonlySettings {
  currency?: unknown
  publicUrl?: unknown
  channelsAllowPrivateNetwork?: unknown
  loginProviders?: unknown
  env?: unknown
  [key: string]: unknown
}

export interface SettingsResponse {
  settings: SystemSettings
  /** Per field (`"site.name"`) where the value comes from. */
  sources: Record<string, SettingSource | string>
  readonly: ReadonlySettings
  version: number
}

/** Partial update: per section, each field a value or `null` (= back to env / default). */
export type SettingsPatchBody = {
  [S in Exclude<SettingsSection, 'notifications'>]?: { [F in keyof SystemSettings[S]]?: SystemSettings[S][F] | null }
} & {
  /** Nested like the read side; `smtp.password` is write-only (no `passwordSet`). */
  notifications?: {
    smtp?: { [F in keyof Omit<NotificationSettings['smtp'], 'passwordSet'>]?: NotificationSettings['smtp'][F] | null } & { password?: string | null }
    enabled?: boolean | null
    emailRateLimitPerHour?: number | null
  }
}

export interface SettingsPatch {
  version: number
  settings: SettingsPatchBody
}

// ---------------------------------------------------------------------------
// Phase 5 (continued): channel tiers, model info, model plaza (phase5-api.md).
// ---------------------------------------------------------------------------

/** A channel relative to the requesting user: own → shared → platform (routing order). */
export type ChannelTier = 'own' | 'shared' | 'platform'

export interface ModelCapabilities {
  vision: boolean
  tools: boolean
  reasoning: boolean
  embedding: boolean
  /** phase7 §1: image generation model (`/v1/images/*`). */
  imageGeneration: boolean
  /** phase9 §1: speech recognition (`/v1/audio/transcriptions`, `/v1/audio/translations`). */
  audioInput: boolean
  /** phase9 §1: speech synthesis (`/v1/audio/speech`). */
  audioOutput: boolean
}

export type ModelCapability = keyof ModelCapabilities

/** Display information for a logical model (`/api/admin/model-info`, models.manage). */
export interface ModelInfo {
  /** Logical model name (primary key). */
  model: string
  /** ≤100; '' = show the model name. */
  displayName: string
  /** ≤1000, plain text. */
  description: string
  /** ≤50, e.g. "DeepSeek". */
  vendor: string
  /** ≤10 tags, each ≤20. */
  tags: string[]
  contextWindow: number | null
  maxOutput: number | null
  capabilities: ModelCapabilities
  /** Hidden from every plaza (still callable). */
  hidden: boolean
  /** Ascending; default 0. */
  sortOrder: number
  version: number
  updatedAt: string
}

/** `PUT /api/admin/model-info/{model}` body: create (no version) or update (with version). */
export interface ModelInfoInput {
  displayName: string
  description: string
  vendor: string
  tags: string[]
  contextWindow: number | null
  maxOutput: number | null
  capabilities: ModelCapabilities
  hidden: boolean
  sortOrder: number
  version?: number
}

/** Client protocols a plaza model can be called with. */
export type PlazaProtocol = 'openai.chat' | 'openai.responses' | 'anthropic.messages' | 'openai.embeddings' | 'openai.images' | 'openai.audio'

/** Current sell price (settlement currency, per 1M tokens); `null` on the model = not priced (free). */
export interface PlazaPrice {
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string | null
  cacheWritePerM: string | null
  /** Fixed fee per request (per-call pricing); null / absent when 0 (or on older backends). */
  perRequest?: string | null
  /**
   * NOT pinned by the contract for the plaza: shown when the backend includes the
   * phase7 image prices (per output image / per 1M image input tokens).
   */
  perImage?: string | null
  imageInputPerM?: string | null
  /** phase9: the backend always includes the four audio prices (null when unset; absent on older backends). */
  audioInputPerM?: string | null
  audioOutputPerM?: string | null
  perMinute?: string | null
  perMCharacters?: string | null
  /** phase8 §3: time-of-day periods of the current price version. */
  schedule?: PriceSchedulePeriod[] | null
  scheduleTimezone?: string
  /** Multiplier in effect right now ("1" outside every period). */
  currentMultiplier?: string
  /** phase10 §1: context-length tiers, resolved (inheritance applied) and × the group multiplier. */
  tiers?: PlazaPriceTier[] | null
}

/**
 * phase10 §1: a resolved plaza tier. `cacheReadPerM` / `cacheWritePerM` null = 0;
 * `imageInputPerM` / `audioInputPerM` null = this tier's `inputPerM`, `audioOutputPerM`
 * null = this tier's `outputPerM`.
 */
export interface PlazaPriceTier {
  aboveInputTokens: number
  inputPerM: string
  outputPerM: string
  cacheReadPerM: string | null
  cacheWritePerM: string | null
  imageInputPerM: string | null
  audioInputPerM: string | null
  audioOutputPerM: string | null
}

export interface PlazaModel {
  model: string
  displayName: string
  description: string
  vendor: string
  tags: string[]
  contextWindow: number | null
  maxOutput: number | null
  capabilities: ModelCapabilities
  protocols: (PlazaProtocol | string)[]
  price: PlazaPrice | null
  /** Active plans covering the model (a plan with no models covers every model). */
  plans: { id: string, name: string }[]
}

/** `GET /api/plaza/mine` item: every model the user can call, with channel tiers. */
export interface MyPlazaModel extends PlazaModel {
  /** Usable channels per tier. */
  sources: { own: number, shared: number, platform: number }
  /** `free`: own or shared channels exist (used first, not billed; falling back to platform bills `price`). */
  billing: 'free' | 'platform'
  /** Valid subscription covering the model (the one expiring first). */
  subscription: { id: string, planName: string } | null
  /** phase8 §1.1: list price before the group multiplier (`price` already includes it). */
  basePrice?: PlazaPrice | null
  /** The user's group multiplier (decimal string). */
  priceMultiplier?: string
}

/**
 * `currency` of the plaza responses. The contract does not pin its shape; the
 * system-info object form and a bare ISO code are both accepted (see lib/plaza.ts).
 */
export type PlazaCurrency = { code: string, symbol?: string, decimals?: number } | string

export interface PlazaResponse<T extends PlazaModel = PlazaModel> {
  items: T[]
  currency: PlazaCurrency | null
}

// ---------------------------------------------------------------------------
// Round 6: notifications, alerts & upstream balances (phase6-api.md).
// ---------------------------------------------------------------------------

export type NotificationEventType
  = | 'wallet.balance_low'
    | 'wallet.credited'
    | 'subscription.expiring'
    | 'subscription.expired'
    | 'quota.near_limit'
    | 'quota.exhausted'
    | 'model.price_changed'
    | 'model.removed'
    | 'model.added'
    | 'key.expiring'
    | 'channel.unhealthy'
    | 'channel.recovered'
    | 'channel.auth_failed'
    | 'upstream.balance_low'
    | 'plugin.pending_approval'
    | 'account.status_changed'
    | 'subscription.quota_reset'
    | 'subscription.extended'
    | 'account.group_changed'
    | 'limit.spend_near'
    | 'limit.spend_reached'
    | 'channel.share_invited'
    | 'reset_card.issued'

export type NotificationSeverity = 'info' | 'warning' | 'critical'

/** In-app notification (§4). */
export interface AppNotification {
  id: string
  type: NotificationEventType | string
  severity: NotificationSeverity | string
  title: string
  body: string
  /** Console path (`/console/...`) or absolute URL; null = none. */
  link: string | null
  data: Record<string, unknown> | null
  readAt: string | null
  createdAt: string
}

export type WebhookFormat = 'json' | 'feishu' | 'dingtalk' | 'wecom' | 'slack'
export type DigestMode = 'off' | 'daily'

export interface EventChannels {
  email: boolean
  webhook: boolean
  inApp: boolean
}

/** `GET/PUT /api/notifications/preferences` (§3). */
export interface NotificationPreferences {
  /** `address: null` = the account email from the identity provider. */
  email: { enabled: boolean, address: string | null, verified: boolean }
  webhook: { enabled: boolean, url: string | null, secretSet: boolean, format: WebhookFormat }
  events: Partial<Record<NotificationEventType | string, EventChannels>>
  thresholds: { walletBalanceLow: string }
  digest: DigestMode
  /** IANA time zone. */
  timezone: string
  version: number
  /**
   * NOT in the contract: if the backend reports whether SMTP is configured, the email
   * section is disabled up front (otherwise inferred from a 409/422 of the verify call).
   */
  smtpConfigured?: boolean
}

/** Result of `POST /api/notifications/webhook/test` (shape not pinned by the contract). */
export interface WebhookTestResult {
  ok: boolean
  error?: string | null
  statusCode?: number | null
  latencyMs?: number | null
}

export interface SmtpTestResult {
  ok: boolean
  error?: string | null
}

/** `GET /api/alerts/summary` (§5). */
export interface UpstreamBalance {
  channelId: string
  channelName: string
  currency: string
  total: string
  available: boolean
  threshold: string | null
  low: boolean
  checkedAt: string
}

export interface AlertsSummary {
  channels: { total: number, healthy: number, degraded: number, down: number }
  balances: UpstreamBalance[]
  recent: AppNotification[]
}
