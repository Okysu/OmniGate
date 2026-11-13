import type { CatalogPlan } from './types'
import type { AudienceFilter } from './audience'
import { emptyAudience } from './audience'
import { api } from './api'
export type CampaignConditions = AudienceFilter
export interface CampaignReward { quantity: number, kind?: '5h' | 'weekly' | 'both', validDays?: number, planIds?: string[] }
export interface CampaignRewardItem { type: 'balance' | 'subscription' | 'reset_card' | 'lottery', amount?: string, planId?: string | null, quantity?: number, kind?: '5h' | 'weekly' | 'both', validDays?: number, planIds?: string[], plan?: CatalogPlan }
export interface Campaign {
  id: string
  title: string
  description: string
  status: 'draft' | 'active' | 'archived'
  rewardType: CampaignRewardItem['type'] | 'bundle'
  reward: CampaignReward | null
  rewards: CampaignRewardItem[] | null
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
export interface PublicCampaign extends Pick<Campaign, 'id' | 'title' | 'description' | 'rewardType' | 'reward' | 'rewards' | 'amount' | 'price' | 'stock' | 'perUserLimit' | 'startsAt' | 'endsAt'> {
  requirements: string[]
  plan?: CatalogPlan
}
export function emptyReward(type: CampaignRewardItem['type'] = 'balance'): CampaignRewardItem {
  if (type === 'balance') return { type, amount: '1' }
  if (type === 'subscription') return { type, planId: null }
  if (type === 'lottery') return { type, quantity: 1 }
  return { type, quantity: 1, kind: 'weekly', validDays: 7, planIds: [] }
}
export function emptyCampaign(): Campaign {
  return { id: '', title: '', description: '', status: 'draft', rewardType: 'balance', reward: null, rewards: [emptyReward()], planId: null, amount: '1', price: '0', stock: -1, perUserLimit: 1, startsAt: null, endsAt: null, conditions: emptyAudience(), version: 0 }
}
export function campaignRewards(c: Pick<Campaign, 'rewardType' | 'reward' | 'rewards' | 'amount'> & { planId?: string | null, plan?: CatalogPlan }): CampaignRewardItem[] {
  if (c.rewards?.length) return c.rewards
  if (c.rewardType === 'bundle') return []
  const type = c.rewardType
  if (type === 'balance') return [{ type, amount: c.amount }]
  if (type === 'subscription') return [{ type, planId: c.planId, plan: c.plan }]
  return [{ type, ...c.reward! }]
}
export function rewardTitle(r: CampaignRewardItem): string {
  if (r.type === 'balance') return '$' + r.amount + ' 余额'
  if (r.type === 'subscription') return r.plan?.name ?? '套餐奖励'
  if (r.type === 'lottery') return (r.quantity ?? 0) + ' 次抽奖'
  return (r.quantity ?? 0) + ' 张' + ({ weekly: '周重置卡', '5h': '5 小时重置卡', both: '双重置卡' })[r.kind ?? 'weekly']
}
export function campaignRewardTitle(c: Parameters<typeof campaignRewards>[0]): string { return campaignRewards(c).map(rewardTitle).join(' + ') }
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
    const body = { title: c.title, description: c.description, status: c.status, rewards: campaignRewards(c).map(({ plan: _plan, ...reward }) => reward), price: c.price, stock: c.stock, perUserLimit: c.perUserLimit, startsAt: c.startsAt, endsAt: c.endsAt, conditions: c.conditions, version: c.version }
    return c.id ? api.put<Campaign>(`/api/admin/benefits/campaigns/${c.id}`, body) : api.post<Campaign>('/api/admin/benefits/campaigns', body)
  },
}
