import { api } from './api'

/** Lists use OR; different fields use AND on the same active subscription. */
export interface AudienceFilter {
  verifiedPurchaseUserIds: string[]
  userIds: string[]
  groupIds: string[]
  planIds: string[]
  excludePlanIds: string[]
  sources: string[]
  requireActiveSubscription: boolean
  excludeHiddenPlans: boolean
  quotaKind: '' | '5h' | 'weekly' | 'both'
  registeredAfter: string | null
  registeredBefore: string | null
  minSpend: string
  requirePurchase: boolean
}
export function emptyAudience(): AudienceFilter {
  return { verifiedPurchaseUserIds: [], userIds: [], groupIds: [], planIds: [], excludePlanIds: [], sources: [], requireActiveSubscription: false, excludeHiddenPlans: false, quotaKind: '', registeredAfter: null, registeredBefore: null, minSpend: '0', requirePurchase: false }
}
export function normalizeAudience(value?: Partial<AudienceFilter>): AudienceFilter {
  const result = { ...emptyAudience(), ...value }
  for (const key of ['userIds', 'groupIds', 'planIds', 'excludePlanIds', 'sources', 'verifiedPurchaseUserIds'] as const) result[key] = value?.[key] ?? []
  return result
}
export interface AudiencePreview { users: number, subscriptions: number, sample: { id: string, name: string }[], truncated: boolean, asOf: string }
export const previewAudience = (filter: AudienceFilter) => api.post<AudiencePreview>('/api/admin/billing/audience/preview', filter)
