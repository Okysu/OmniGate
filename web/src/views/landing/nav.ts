// Shared navigation of the public landing site (`/` and `/models`).
import type { RouteLocationRaw } from 'vue-router'

export interface LandingNavItem {
  key: string
  label: string
  to: RouteLocationRaw
}

/** Section anchors of the landing page (absolute, so they also work from `/models`). */
export const LANDING_SECTIONS: LandingNavItem[] = [
  { key: 'features', label: '特性', to: { path: '/', hash: '#features' } },
  { key: 'protocols', label: '协议转换', to: { path: '/', hash: '#protocols' } },
  { key: 'quickstart', label: '快速开始', to: { path: '/', hash: '#quickstart' } },
]

/** Public model plaza (phase5-api.md §3.3). */
export const PLAZA_NAV: LandingNavItem = { key: 'models', label: '模型广场', to: '/models' }

export const LANDING_NAV: LandingNavItem[] = [...LANDING_SECTIONS, PLAZA_NAV]
