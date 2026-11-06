import { api } from './api'

export interface LotteryConfig {
  threshold: string
  prizes: { amount: string, percent: number }[]
  version: number
}

export interface LotteryStatus extends LotteryConfig {
  available: number
  earned: number
  todaySpent: string
  todayEarned: boolean
}

export interface DrawRequest {
  count: number
  requestId: string
}

export interface DrawResult {
  rewards: string[]
  total: string
  ledgerId: string
}

export const lotteryApi = {
  status: () => api.get<LotteryStatus>('/api/billing/lottery'),
  draw: (request: DrawRequest) => api.post<DrawResult>('/api/billing/lottery/draw', request),
  config: () => api.get<LotteryConfig>('/api/admin/billing/lottery'),
  save: (config: LotteryConfig) => api.put<LotteryConfig>('/api/admin/billing/lottery', config),
}
