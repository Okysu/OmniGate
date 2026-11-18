import type { Paginated } from './types'
import { api } from './api'

export interface ControlRule {
  id: string
  name: string
  enabled: boolean
  channelIds: string[]
  models: string[]
  action: 'audit' | 'block' | 'notify' | 'ban'
  notifyAdmins: boolean
  notifyUser: boolean
}
export interface ControlSettings {
  enabled: boolean
  baseUrl: string
  model: string
  threshold: number
  categories: string[]
  failClosed: boolean
  storeExcerpt: boolean
  excerptChars: number
  storeSnapshots: boolean
  snapshotWindowSeconds: number
  keep: number
  minChars: number
  timeoutMs: number
  rules: ControlRule[]
  version: number
  apiKeySet: boolean
}
export interface ControlEvent {
  id: string
  requestId: string
  ruleId: string
  userId: string
  userName: string
  keyId: string
  keyName: string
  channelId: string
  channelName: string
  model: string
  dialect: string
  reason: string
  probability: number | null
  snapshotId: string | null
  threshold: number
  reviewProbability: number | null
  reviewThreshold: number | null
  reviewedAt: string | null
  effectiveHit: boolean
  scores: { label: string, probability: number }[] | null
  excerpt: string
  failure: boolean
  action: ControlRule['action']
  actionError: string
  actionState: 'pending' | 'completed' | 'failed'
  createdAt: string
}
export interface ControlSnapshot {
  id: string
  requestId: string
  model: string
  dialect: string
  payload: unknown
  truncated: boolean
  createdAt: string
}
export interface ControlReview {
  model: string
  result: string
  sourceRequestId: string
  probability: number | null
  threshold: number
  effectiveHit: boolean | null
  applyToken: string
}
export function controlPercent(value: number | null) { return value === null ? '—' : `${(value * 100).toFixed(1)}%` }
export const controlApi = {
  settings: () => api.get<ControlSettings>('/api/admin/control/settings'),
  save: (body: Omit<ControlSettings, 'apiKeySet'> & { apiKey?: string }) => api.put<ControlSettings>('/api/admin/control/settings', body),
  events: (params: { page: number, pageSize: number, userId?: string, channelId?: string, ruleId?: string, model?: string, requestId?: string }) => api.get<Paginated<ControlEvent>>('/api/admin/control/events', { query: { ...params } }),
  remove: (id: string) => api.delete(`/api/admin/control/events/${encodeURIComponent(id)}`),
  clear: () => api.delete('/api/admin/control/events'),
  snapshot: (id: string) => api.get<ControlSnapshot>(`/api/admin/control/events/${encodeURIComponent(id)}/snapshot`),
  reviewModels: () => api.get<{ models: string[] }>('/api/admin/control/review-models'),
  review: (eventId: string, model: string) => api.post<ControlReview>('/api/admin/control/review', { eventId, model }),
  applyReview: (id: string, token: string, threshold?: number) => api.post<Pick<ControlEvent, 'reviewProbability' | 'reviewThreshold' | 'effectiveHit' | 'reviewedAt'>>(`/api/admin/control/events/${encodeURIComponent(id)}/apply-review`, { token, threshold }),
}
export function controlLines(value: string): string[] { return value.split('\n').map(s => s.trim()).filter(Boolean) }
