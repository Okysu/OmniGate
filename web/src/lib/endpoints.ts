import type {
  AffinityOutcome,
  AffinityStats,
  ClientInfo,
  AdminUserDetail,
  AlertsSummary,
  BulkSubscriptionResult,
  ExtendSubscriptionsInput,
  ResetQuotaInput,
  UserBatchInput,
  UserBatchResult,
  AppNotification,
  AuditLog,
  BuildResult,
  CapabilityResult,
  CatalogPlan,
  ChannelCapabilities,
  ImportResult,
  IncomingShare,
  MeterOption,
  Plugin,
  PluginDraft,
  PluginTemplate,
  PluginTestCase,
  PluginTestResult,
  PluginVersion,
  PurchaseInput,
  PurchaseOptions,
  PurchaseRecord,
  PurchaseResult,
  ReferralInfo,
  ReferralRebate,
  AuthProvidersResponse,
  BatchStatus,
  BillingLimits,
  BillingPreferences,
  BillingSettings,
  Channel,
  ChannelInput,
  ChannelScope,
  ChannelTestResult,
  ChannelType,
  ChannelView,
  EnabledStatus,
  GatewayKey,
  GrantInput,
  KeyCreated,
  KeyCreateInput,
  KeyPatch,
  LedgerEntry,
  MeResponse,
  ModelEntry,
  ModelInfo,
  ModelInfoInput,
  MyPlazaModel,
  NotificationPreferences,
  Paginated,
  Plan,
  PlanInput,
  PlazaModel,
  PlazaResponse,
  PlanPatch,
  PlanStatus,
  Price,
  PriceInput,
  PriceKind,
  RedeemBatch,
  RedeemBatchCreated,
  RedeemBatchInput,
  RedeemResult,
  RequestLog,
  ResetCard,
  ResetCardBatch,
  ResetCardIssueInput,
  ResetCardIssueResult,
  ResetCardPreview,
  ResetCardUseResult,
  MyResetCards,
  RevokeOthersResponse,
  RoutePreview,
  RoutePreviewInput,
  RouteRule,
  RouteRuleInput,
  RouteRulePatch,
  SettingsPatch,
  SettingsResponse,
  SmtpTestResult,
  SessionsResponse,
  StatsSummary,
  Subscription,
  SubscriptionStatus,
  SystemInfo,
  User,
  UserGroup,
  UserGroupInput,
  UserGroupPatch,
  UserPatch,
  Wallet,
  WalletAdjusted,
  WalletAdjustInput,
  WebhookTestResult,
} from './types'
import { api, requestBlob, requestText, requestWithStatus } from './api'
import { normalizeMyPlazaModel, normalizePlazaModel } from './plaza'

const enc = encodeURIComponent

export const systemApi = {
  info: () => api.get<SystemInfo>('/api/system/info'),
}

export const authApi = {
  providers: () => api.get<AuthProvidersResponse>('/api/auth/providers'),
  /** Login is a full-page navigation (not fetch): the backend redirects to the IdP. */
  /** phase15 §4.2: `invite` binds a newly created account to the inviter (ignored for existing users). */
  loginUrl: (providerId: string, redirect: string, invite?: string | null) =>
    `/api/auth/${enc(providerId)}/login?redirect=${enc(redirect)}${invite ? `&invite=${enc(invite)}` : ''}`,
  logout: () => api.post<void>('/api/auth/logout', undefined, { skipAuthRedirect: true }),
}

export const meApi = {
  get: () => api.get<MeResponse>('/api/me'),
  sessions: () => api.get<SessionsResponse>('/api/me/sessions'),
  revokeSession: (id: string) => api.delete(`/api/me/sessions/${enc(id)}`),
  revokeOtherSessions: () => api.post<RevokeOthersResponse>('/api/me/sessions/revoke-others'),
}

export interface ListUsersParams {
  page: number
  pageSize: number
  q?: string
  sort?: string
  /** phase8 §1.3: only members of this group. */
  groupId?: string
}

export interface ListAuditLogsParams {
  page: number
  pageSize: number
  action?: string
  actorId?: string
}

export const adminApi = {
  listUsers: (params: ListUsersParams, signal?: AbortSignal) =>
    api.get<Paginated<User>>('/api/admin/users', { query: { ...params }, signal }),
  patchUser: (id: string, patch: UserPatch) => api.patch<User>(`/api/admin/users/${enc(id)}`, patch),
  /** phase7 §2.2 (users.read). */
  getUser: (id: string, signal?: AbortSignal) => api.get<AdminUserDetail>(`/api/admin/users/${enc(id)}`, { signal }),
  /** Revokes every session of the user (users.write). */
  logoutUser: (id: string) => api.post<{ revoked: number }>(`/api/admin/users/${enc(id)}/logout`),
  /** Disables every API key of the user (users.write); they can be re-enabled one by one. */
  disableUserKeys: (id: string) => api.post<{ disabled: number }>(`/api/admin/users/${enc(id)}/keys/disable`),
  /** Applies the action to 1–200 users with the single-user protection rules (users.write). */
  batchUsers: async (input: UserBatchInput): Promise<UserBatchResult> => {
    const res = await api.post<Partial<UserBatchResult> | null>('/api/admin/users/batch', input)
    return { succeeded: res?.succeeded ?? [], failed: res?.failed ?? [] }
  },
  /** phase8 §1.3: moves the user to another group (users.write). */
  setUserGroup: (id: string, groupId: string) => api.put<User | null>(`/api/admin/users/${enc(id)}/group`, { groupId }),
  listAuditLogs: (params: ListAuditLogsParams, signal?: AbortSignal) =>
    api.get<Paginated<AuditLog>>('/api/admin/audit-logs', { query: { ...params }, signal }),
}

// ---------------------------------------------------------------------------
// Round 6: user groups (phase8-api.md §1)
// ---------------------------------------------------------------------------

export const groupsApi = {
  /** Every group (users.read), `{items}`. */
  list: async (signal?: AbortSignal): Promise<{ items: UserGroup[] }> => {
    const res = await api.get<{ items: UserGroup[] | null } | null>('/api/admin/groups', { signal })
    return { items: res?.items ?? [] }
  },
  create: (input: UserGroupInput) => api.post<UserGroup>('/api/admin/groups', input),
  /** `isDefault: true` unsets the previous default group. 409 `version_conflict` when stale. */
  update: (id: string, patch: UserGroupPatch) => api.patch<UserGroup>(`/api/admin/groups/${enc(id)}`, patch),
  /** Members move to the default group; 409 `group_is_default` for the default group. */
  remove: (id: string) => api.delete(`/api/admin/groups/${enc(id)}`),
}

// ---------------------------------------------------------------------------
// Phase 1 endpoints
// ---------------------------------------------------------------------------

export interface ListChannelsParams {
  page: number
  pageSize: number
  q?: string
  type?: ChannelType
  scope?: ChannelScope
  status?: EnabledStatus
}

export const channelsApi = {
  list: (params: ListChannelsParams, signal?: AbortSignal) =>
    api.get<Paginated<ChannelView>>('/api/channels', { query: { ...params }, signal }),
  get: (id: string) => api.get<ChannelView>(`/api/channels/${enc(id)}`),
  create: (input: ChannelInput) => api.post<Channel>('/api/channels', input),
  update: (id: string, input: ChannelInput) => api.patch<Channel>(`/api/channels/${enc(id)}`, input),
  remove: (id: string) => api.delete(`/api/channels/${enc(id)}`),
  test: (id: string) => api.post<ChannelTestResult>(`/api/channels/${enc(id)}/test`),
  discoverModels: (id: string) => api.post<{ models: string[] }>(`/api/channels/${enc(id)}/discover-models`),
  /** Owner or `channels.manage` only. */
  capabilities: (id: string, signal?: AbortSignal) =>
    api.get<ChannelCapabilities>(`/api/channels/${enc(id)}/capabilities`, { signal }),
  invokeCapability: (id: string, name: string) =>
    api.post<CapabilityResult>(`/api/channels/${enc(id)}/capabilities/${enc(name)}`),
}

/** Channels shared with the current user (phase5-api.md §5.3): invitations must be accepted. */
export const channelSharesApi = {
  /** Pending invitations first, then accepted shares; declined ones are not listed. */
  list: async (signal?: AbortSignal): Promise<{ items: IncomingShare[] }> => {
    const res = await api.get<{ items: IncomingShare[] | null } | null>('/api/channel-shares', { signal })
    return { items: res?.items ?? [] }
  },
  /** Idempotent for an already accepted share. */
  accept: (channelId: string) => api.post<IncomingShare>(`/api/channel-shares/${enc(channelId)}/accept`),
  /** Pending only (204); 409 `share_state_conflict` for an accepted share (use `leave`). */
  decline: (channelId: string) => api.post<void>(`/api/channel-shares/${enc(channelId)}/decline`),
  /** Accepted only (204); 409 `share_state_conflict` for a pending invitation (use `decline`). */
  leave: (channelId: string) => api.post<void>(`/api/channel-shares/${enc(channelId)}/leave`),
}

// ---------------------------------------------------------------------------
// Phase 2: plugins
// ---------------------------------------------------------------------------

export const pluginsApi = {
  list: (signal?: AbortSignal) => api.get<{ items: Plugin[] }>('/api/plugins', { signal }),
  get: (id: string, signal?: AbortSignal) => api.get<Plugin>(`/api/plugins/${enc(id)}`, { signal }),
  create: (body: { id: string, name: string, template: PluginTemplate }) => api.post<Plugin>('/api/plugins', body),
  /** multipart/form-data with a `file` field (ZIP, ≤ 4 MiB). */
  importZip: (file: File) => {
    const form = new FormData()
    form.append('file', file)
    return api.post<ImportResult>('/api/plugins/import', form)
  },
  setStatus: (id: string, status: 'enabled' | 'disabled', version: number) =>
    api.patch<Plugin>(`/api/plugins/${enc(id)}`, { status, version }),
  /**
   * Permanently deletes an `upload` / `editor` plugin (versions, draft, storage); 204.
   * 409 `plugin_builtin` for builtin/bundled, 409 `plugin_in_use` (`details.channels`) while channels use it.
   */
  remove: (id: string) => api.delete(`/api/plugins/${enc(id)}`),
  getDraft: (id: string, signal?: AbortSignal) => api.get<PluginDraft>(`/api/plugins/${enc(id)}/draft`, { signal }),
  saveDraft: (id: string, files: Record<string, string>, version: number) =>
    api.put<PluginDraft>(`/api/plugins/${enc(id)}/draft`, { files, version }),
  build: (id: string) => api.post<BuildResult>(`/api/plugins/${enc(id)}/draft/build`),
  test: (id: string, testCase: PluginTestCase) => api.post<PluginTestResult>(`/api/plugins/${enc(id)}/draft/test`, testCase),
  publish: (id: string) => api.post<PluginVersion>(`/api/plugins/${enc(id)}/draft/publish`),
  version: (id: string, vid: string, signal?: AbortSignal) =>
    api.get<PluginVersion>(`/api/plugins/${enc(id)}/versions/${enc(vid)}`, { signal }),
  decide: (id: string, vid: string, decision: 'approve' | 'reject', note?: string) =>
    api.post<PluginVersion>(`/api/plugins/${enc(id)}/versions/${enc(vid)}/approve`, note ? { decision, note } : { decision }),
  exportZip: (id: string, vid: string) => requestBlob(`/api/plugins/${enc(id)}/versions/${enc(vid)}/export`),
  sdkTypes: () => requestText('/api/plugins/sdk.d.ts'),
}

export interface ListPricesParams {
  page: number
  pageSize: number
  kind?: PriceKind
  model?: string
  channelId?: string
}

export const modelsApi = {
  list: (signal?: AbortSignal) => api.get<{ items: ModelEntry[] }>('/api/models', { signal }),
  /** Every model on any enabled channel (requires models.manage); used for pricing. */
  listAll: (signal?: AbortSignal) =>
    api.get<{ items: ModelEntry[] }>('/api/models', { query: { scope: 'all' }, signal }),
  listPrices: (params: ListPricesParams, signal?: AbortSignal) =>
    api.get<Paginated<Price>>('/api/admin/prices', { query: { ...params }, signal }),
  createPrice: (input: PriceInput) => api.post<Price>('/api/admin/prices', input),
}

export const keysApi = {
  list: (signal?: AbortSignal) => api.get<{ items: GatewayKey[] }>('/api/keys', { signal }),
  create: (input: KeyCreateInput) => api.post<KeyCreated>('/api/keys', input),
  update: (id: string, patch: KeyPatch) => api.patch<GatewayKey>(`/api/keys/${enc(id)}`, patch),
  revoke: (id: string) => api.delete(`/api/keys/${enc(id)}`),
  rotate: (id: string) => api.post<KeyCreated>(`/api/keys/${enc(id)}/rotate`),
}

export interface LogsFilter {
  from?: string
  to?: string
  model?: string
  channelId?: string
  keyId?: string
  userId?: string
  status?: 'success' | 'error'
  /** phase12 §4: one session affinity outcome, or 'any' (an affinity rule applied). */
  affinity?: AffinityOutcome | 'any'
  /** phase13 §3: one detected client id (GET /api/clients). */
  client?: string
}

export const logsApi = {
  list: (params: LogsFilter & { page: number, pageSize: number }, signal?: AbortSignal) =>
    api.get<Paginated<RequestLog>>('/api/logs', { query: { ...params }, signal }),
  summary: (params: { from?: string, to?: string, userId?: string }, signal?: AbortSignal) =>
    api.get<StatsSummary>('/api/stats/summary', { query: { ...params }, signal }),
  /** phase13 §1: the known client ids (log filter values, affinity client_include). */
  clients: (signal?: AbortSignal) => api.get<{ items: ClientInfo[] }>('/api/clients', { signal }),
}

export const billingApi = {
  wallet: (signal?: AbortSignal) => api.get<Wallet>('/api/billing/wallet', { signal }),
  ledger: (params: { page: number, pageSize: number }, signal?: AbortSignal) =>
    api.get<Paginated<LedgerEntry>>('/api/billing/ledger', { query: { ...params }, signal }),
  redeem: (code: string) => api.post<RedeemResult>('/api/billing/redeem', { code }),
  /** My subscriptions (newest 50, including expired / cancelled) with current usage. */
  subscriptions: (signal?: AbortSignal) => api.get<Paginated<Subscription>>('/api/billing/subscriptions', { signal }),
  /** Account billing preferences (what happens once subscription quota is used up). */
  preferences: (signal?: AbortSignal) => api.get<BillingPreferences>('/api/billing/preferences', { signal }),
  updatePreferences: (body: BillingPreferences) => api.put<BillingPreferences>('/api/billing/preferences', body),
  /** phase8 §2.3: own group limits, usage and per-key spend limits. */
  limits: (signal?: AbortSignal) => api.get<BillingLimits>('/api/billing/limits', { signal }),
  /** Active plans (catalog visible to every signed-in user; trimmed, no admin-only fields). */
  catalog: (signal?: AbortSignal) => api.get<Paginated<CatalogPlan>>('/api/plans', { signal }),
  /** phase15 §3.1: plans for sale with server-computed prices, renewals and upgrades. */
  purchaseOptions: (signal?: AbortSignal) => api.get<PurchaseOptions>('/api/billing/purchase/options', { signal }),
  /** phase15 §3.2: buy / renew, or upgrade with `fromSubscriptionId`, paid from the wallet. */
  purchase: (body: PurchaseInput) => api.post<PurchaseResult>('/api/billing/purchase', body),
  /** phase15 §3.3: my purchase records, newest first. */
  purchases: (params: { page: number, pageSize: number }, signal?: AbortSignal) =>
    api.get<Paginated<PurchaseRecord>>('/api/billing/purchases', { query: { ...params }, signal }),
  /** phase15 §4.4: my invite code / link, invitees and rebate total. */
  referral: (signal?: AbortSignal) => api.get<ReferralInfo>('/api/billing/referral', { signal }),
  /** phase15 §4.4: rebate records, newest first. */
  referralRebates: (params: { page: number, pageSize: number }, signal?: AbortSignal) =>
    api.get<Paginated<ReferralRebate>>('/api/billing/referral/rebates', { query: { ...params }, signal }),
}

export interface ListSubscriptionsParams {
  page: number
  pageSize: number
  userId?: string
  planId?: string
  status?: SubscriptionStatus
}

/** phase9 §3: meters a quota rule can use (`billing.manage`); older backends 404. */
export const billingMetersApi = {
  list: async (signal?: AbortSignal): Promise<MeterOption[]> => {
    const res = await api.get<{ items: MeterOption[] } | MeterOption[]>('/api/admin/billing/meters', { signal })
    return Array.isArray(res) ? res : (res.items ?? [])
  },
}

export const plansApi = {
  list: (params: { page: number, pageSize: number, status?: PlanStatus }, signal?: AbortSignal) =>
    api.get<Paginated<Plan>>('/api/admin/billing/plans', { query: { ...params }, signal }),
  create: (input: PlanInput) => api.post<Plan>('/api/admin/billing/plans', input),
  update: (id: string, patch: PlanPatch) => api.patch<Plan>(`/api/admin/billing/plans/${enc(id)}`, patch),
  listSubscriptions: (params: ListSubscriptionsParams, signal?: AbortSignal) =>
    api.get<Paginated<Subscription>>('/api/admin/billing/subscriptions', { query: { ...params }, signal }),
  /** 201: new subscription; 200: an existing one was renewed (non-stackable plan). */
  grant: async (input: GrantInput) => {
    const res = await requestWithStatus<Subscription>('POST', '/api/admin/billing/subscriptions', { body: input })
    return { subscription: res.data, renewed: res.status === 200 }
  },
  cancel: (id: string, note?: string) =>
    api.post<Subscription>(`/api/admin/billing/subscriptions/${enc(id)}/cancel`, note ? { note } : {}),
  /** phase7 §3.1; `dryRun` only counts (§3.3). */
  resetQuota: (input: ResetQuotaInput, opts: { dryRun?: boolean } = {}) =>
    bulkSubscriptions('/api/admin/billing/subscriptions/reset-quota', input, opts.dryRun),
  /** phase7 §3.2; `dryRun` only counts (§3.3). */
  extend: (input: ExtendSubscriptionsInput, opts: { dryRun?: boolean } = {}) =>
    bulkSubscriptions('/api/admin/billing/subscriptions/extend', input, opts.dryRun),
}

async function bulkSubscriptions(path: string, body: unknown, dryRun?: boolean): Promise<BulkSubscriptionResult> {
  const res = await api.post<Partial<BulkSubscriptionResult> | null>(path, body, { query: dryRun ? { dryRun: true } : undefined })
  return { affected: typeof res?.affected === 'number' ? res.affected : 0, subscriptions: res?.subscriptions ?? [] }
}

/** Every plan (pages of 100, capped at 1000), optionally filtered by status. */
export async function fetchAllPlans(status?: PlanStatus, signal?: AbortSignal): Promise<Plan[]> {
  const out: Plan[] = []
  for (let page = 1; page <= 10; page++) {
    const res = await plansApi.list({ page, pageSize: 100, status }, signal)
    out.push(...res.items)
    if (out.length >= res.total || res.items.length === 0)
      break
  }
  return out
}

export const adminBillingApi = {
  settings: () => api.get<BillingSettings>('/api/admin/billing/settings'),
  updateSettings: (body: BillingSettings) => api.put<BillingSettings>('/api/admin/billing/settings', body),
  listBatches: (params: { page: number, pageSize: number }, signal?: AbortSignal) =>
    api.get<Paginated<RedeemBatch>>('/api/admin/billing/redeem-batches', { query: { ...params }, signal }),
  createBatch: (input: RedeemBatchInput) => api.post<RedeemBatchCreated>('/api/admin/billing/redeem-batches', input),
  setBatchStatus: (id: string, status: BatchStatus, version?: number) =>
    api.patch<RedeemBatch>(`/api/admin/billing/redeem-batches/${enc(id)}`, { status, version }),
  wallet: (userId: string) => api.get<Wallet>(`/api/admin/billing/wallets/${enc(userId)}`),
  adjust: (userId: string, input: WalletAdjustInput) =>
    api.post<WalletAdjusted>(`/api/admin/billing/wallets/${enc(userId)}/adjust`, input),
}

// ---------------------------------------------------------------------------
// Round 11: quota reset cards (phase11-api.md §2)
// ---------------------------------------------------------------------------

export const resetCardsApi = {
  /** Own cards, usable first (billing.own). */
  mine: (signal?: AbortSignal) => api.get<MyResetCards>('/api/billing/reset-cards', { signal }),
  /** The live subscriptions the card applies to, with the rules it would reset. */
  preview: (id: string, signal?: AbortSignal) => api.get<ResetCardPreview>(`/api/billing/reset-cards/${enc(id)}/preview`, { signal }),
  /** Spends the card (409 card_used / card_expired / card_revoked; 422 card_not_applicable / card_plan_not_allowed). */
  use: (id: string, subscriptionId: string) => api.post<ResetCardUseResult>(`/api/billing/reset-cards/${enc(id)}/use`, { subscriptionId }),
}

export const adminResetCardsApi = {
  listBatches: (params: { page: number, pageSize: number }, signal?: AbortSignal) =>
    api.get<Paginated<ResetCardBatch>>('/api/admin/billing/reset-cards/batches', { query: { ...params }, signal }),
  /** `dryRun` validates and only counts the recipients (`batch: null`). */
  issue: (input: ResetCardIssueInput, opts: { dryRun?: boolean } = {}) =>
    api.post<ResetCardIssueResult>('/api/admin/billing/reset-cards/batches', input, { query: opts.dryRun ? { dryRun: true } : undefined }),
  /** Revokes the unused cards of a batch (409 card_batch_revoked when already revoked). */
  revoke: (id: string) => api.post<ResetCardBatch>(`/api/admin/billing/reset-cards/batches/${enc(id)}/revoke`),
  listCards: (params: { page: number, pageSize: number, userId?: string, batchId?: string }, signal?: AbortSignal) =>
    api.get<Paginated<ResetCard>>('/api/admin/billing/reset-cards', { query: { ...params }, signal }),
}

/** Fetches every visible channel (pages of 200, capped at 2000 entries). */
export async function fetchAllChannels(signal?: AbortSignal): Promise<ChannelView[]> {
  const out: ChannelView[] = []
  for (let page = 1; page <= 10; page++) {
    const res = await channelsApi.list({ page, pageSize: 200 }, signal)
    out.push(...res.items)
    if (out.length >= res.total || res.items.length === 0)
      break
  }
  return out
}

// ---------------------------------------------------------------------------
// Round 5: route rules & system settings
// ---------------------------------------------------------------------------

export const routesApi = {
  /** Every rule, ordered by `position` (not paginated). */
  list: async (signal?: AbortSignal): Promise<{ items: RouteRule[] }> => {
    const res = await api.get<{ items: RouteRule[] | null } | RouteRule[] | null>('/api/admin/routes', { signal })
    return { items: Array.isArray(res) ? res : (res?.items ?? []) }
  },
  /** Appended to the end. */
  create: (input: RouteRuleInput) => api.post<RouteRule>('/api/admin/routes', input),
  update: (id: string, patch: RouteRulePatch) => api.patch<RouteRule>(`/api/admin/routes/${enc(id)}`, patch),
  remove: (id: string) => api.delete(`/api/admin/routes/${enc(id)}`),
  /** `ids` must contain exactly every rule. */
  reorder: (ids: string[]) => api.put<unknown>('/api/admin/routes/order', { ids }),
  preview: (input: RoutePreviewInput) => api.post<RoutePreview>('/api/admin/routes/preview', input),
}

export const settingsApi = {
  get: (signal?: AbortSignal) => api.get<SettingsResponse>('/api/admin/settings', { signal }),
  update: (body: SettingsPatch) => api.patch<SettingsResponse>('/api/admin/settings', body),
  /** Sends a test email with the saved SMTP settings (phase6-api.md §1); errors never contain the password. */
  smtpTest: (to: string) => api.post<SmtpTestResult>('/api/admin/settings/smtp-test', { to }),
}

/** phase12-api.md §3: session affinity bindings (the rules are `settings.gateway.affinity`). */
export const affinityApi = {
  stats: (signal?: AbortSignal) => api.get<AffinityStats>('/api/admin/affinity/stats', { signal }),
  /** Clears every binding, or only those of `rule`. */
  clear: (rule?: string) => api.post<{ cleared: number, stats: AffinityStats }>('/api/admin/affinity/clear', rule ? { rule } : {}),
}

// ---------------------------------------------------------------------------
// Phase 5 (continued): model info & model plaza (phase5-api.md §2, §3)
// ---------------------------------------------------------------------------

export const modelInfoApi = {
  /** Every model-info row (models.manage). */
  list: async (signal?: AbortSignal): Promise<{ items: ModelInfo[] }> => {
    const res = await api.get<{ items: ModelInfo[] | null } | null>('/api/admin/model-info', { signal })
    return { items: res?.items ?? [] }
  },
  /** Create (omit `version`) or update (with `version`; 409 `version_conflict` when stale). */
  save: (model: string, input: ModelInfoInput) => api.put<ModelInfo>(`/api/admin/model-info/${enc(model)}`, input),
  /** 204; the model stays callable and shows up in the plaza with its bare name. */
  remove: (model: string) => api.delete(`/api/admin/model-info/${enc(model)}`),
}

export const plazaApi = {
  /**
   * Platform model plaza. Anonymous when `site.publicModelPlaza` is on; otherwise 401
   * for visitors. `publicPage` suppresses the global 401 → login redirect (the public
   * `/models` page shows its own "登录后查看" state instead).
   */
  models: async (opts: { signal?: AbortSignal, publicPage?: boolean } = {}): Promise<PlazaResponse> => {
    const res = await api.get<PlazaResponse | null>('/api/plaza/models', { signal: opts.signal, skipAuthRedirect: opts.publicPage })
    return { items: (res?.items ?? []).map(normalizePlazaModel) as PlazaModel[], currency: res?.currency ?? null }
  },
  /** Every model the signed-in user can call (platform + own + shared, hidden ones included). */
  mine: async (signal?: AbortSignal): Promise<PlazaResponse<MyPlazaModel>> => {
    const res = await api.get<PlazaResponse<MyPlazaModel> | null>('/api/plaza/mine', { signal })
    return { items: (res?.items ?? []).map(normalizeMyPlazaModel), currency: res?.currency ?? null }
  },
}

// ---------------------------------------------------------------------------
// Round 6: notifications & alerts (phase6-api.md §3–§5)
// ---------------------------------------------------------------------------

export interface ListNotificationsParams {
  page: number
  pageSize: number
  /** true = unread only; omitted = all. */
  unread?: boolean
  /** Comma-separated event types (a category filter sends every type of the category). */
  type?: string
}

/** Body of `PUT /api/notifications/preferences` (whole document with `version`). */
export type NotificationPreferencesInput = Omit<NotificationPreferences, 'smtpConfigured'>

export const notificationsApi = {
  list: async (params: ListNotificationsParams, signal?: AbortSignal): Promise<Paginated<AppNotification>> => {
    const res = await api.get<Paginated<AppNotification> | null>('/api/notifications', { query: { ...params, unread: params.unread ? true : undefined }, signal })
    return { items: res?.items ?? [], total: res?.total ?? 0, page: res?.page ?? params.page, pageSize: res?.pageSize ?? params.pageSize }
  },
  unreadCount: (signal?: AbortSignal) => api.get<{ count: number }>('/api/notifications/unread-count', { signal }),
  markRead: (ids: string[]) => api.post<unknown>('/api/notifications/read', { ids }),
  markAllRead: () => api.post<unknown>('/api/notifications/read', { all: true }),
  preferences: (signal?: AbortSignal) => api.get<NotificationPreferences>('/api/notifications/preferences', { signal }),
  updatePreferences: (body: NotificationPreferencesInput) => api.put<NotificationPreferences>('/api/notifications/preferences', body),
  /** Sends a 6-digit code to `address` (valid 10 minutes, at most 5 per hour). */
  verifyEmail: (address: string) => api.post<unknown>('/api/notifications/email/verify', { address }),
  /** Verifies the code and makes `address` the notification email. */
  confirmEmail: (address: string, code: string) => api.post<unknown>('/api/notifications/email/confirm', { address, code }),
  testWebhook: () => api.post<WebhookTestResult | null>('/api/notifications/webhook/test'),
  /** Write-only HMAC secret; "" clears it (assumption — the contract only defines setting it). */
  setWebhookSecret: (secret: string) => api.put<unknown>('/api/notifications/webhook/secret', { secret }),
}

export const alertsApi = {
  /** Only channels the current user can manage. */
  summary: (signal?: AbortSignal) => api.get<AlertsSummary>('/api/alerts/summary', { signal }),
}
