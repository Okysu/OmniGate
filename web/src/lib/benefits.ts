import type { CatalogPlan } from './types'
import { api } from './api'

export interface CampaignConditions {
  groupIds: string[]
  userIds: string[]
  registeredAfter: string | null
  registeredBefore: string | null
  minSpend: string
  requirePurchase: boolean
}
export interface Campaign {
  id: string
  title: string
  description: string
  status: 'draft' | 'active' | 'archived'
  rewardType: 'balance' | 'subscription'
  planId: string | null
  amount: string
  price: string
  stock: number
  perUserLimit: number
  startsAt: string | null
  endsAt: string | null
  conditions: CampaignConditions
  version: number
}
export interface PublicCampaign extends Pick<Campaign, 'id' | 'title' | 'description' | 'rewardType' | 'amount' | 'price' | 'stock' | 'perUserLimit' | 'startsAt' | 'endsAt'> {
  requirements: string[]
  plan?: CatalogPlan
}
export function emptyCampaign(): Campaign {
  return { id: '', title: '', description: '', status: 'draft', rewardType: 'balance', planId: null, amount: '1', price: '0', stock: -1, perUserLimit: 1, startsAt: null, endsAt: null, conditions: { groupIds: [], userIds: [], registeredAfter: null, registeredBefore: null, minSpend: '0', requirePurchase: false }, version: 0 }
}
export function campaignState(c: PublicCampaign, now = Date.now()): string {
  if (c.startsAt && now < Date.parse(c.startsAt)) return '未开始'
  if (c.endsAt && now >= Date.parse(c.endsAt)) return '已结束'
  if (c.stock === 0) return '已领完'
  return '进行中'
}
export const benefitsApi = {
  list: () => api.get<{ items: PublicCampaign[] }>('/api/public/benefits'),
  get: (id: string) => api.get<PublicCampaign>(`/api/public/benefits/${id}`),
  claims: () => api.get<{ counts: Record<string, number> }>('/api/benefits/claims'),
  claim: (id: string, requestId: string, expectedPrice: string) => api.post(`/api/benefits/campaigns/${id}/claim`, { requestId, expectedPrice }),
  adminList: () => api.get<{ items: Campaign[] }>('/api/admin/benefits/campaigns'),
  save: (c: Campaign) => {
    const body = {
      title: c.title,
      description: c.description,
      status: c.status,
      rewardType: c.rewardType,
      planId: c.planId,
      amount: c.amount,
      price: c.price,
      stock: c.stock,
      perUserLimit: c.perUserLimit,
      startsAt: c.startsAt,
      endsAt: c.endsAt,
      conditions: c.conditions,
      version: c.version,
    }
    return c.id ? api.put<Campaign>(`/api/admin/benefits/campaigns/${c.id}`, body) : api.post<Campaign>('/api/admin/benefits/campaigns', body)
  },
}
