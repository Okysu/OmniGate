import type { Component } from 'vue'
import {
  BarChart3,
  Bell,
  Blocks,
  Boxes,
  Cable,
  CalendarClock,
  FileClock,
  KeyRound,
  LayoutDashboard,
  Package,
  Route as RouteIcon,
  ScrollText,
  Settings,
  SlidersHorizontal,
  Store,
  Ticket,
  Users,
  UsersRound,
  Wallet,
} from '@lucide/vue'

export interface PlaceholderInfo {
  /** Roadmap phase in which the page is planned, e.g. "Phase 1". */
  phase: string
  /** What the page will contain once implemented. */
  description: string
  /** Optional bullet list of planned capabilities. */
  features?: string[]
}

export interface NavItem {
  title: string
  /** Absolute path under `/console` (see `lib/paths.ts`). */
  to: string
  icon: Component
  /**
   * Permission required to see the entry. Purely cosmetic: the backend enforces
   * authorization on every API call. Strings must match what `/api/me` returns.
   */
  permission?: string
  /** Present when the page is not implemented yet. */
  placeholder?: PlaceholderInfo
  /** Live counter rendered next to the entry (`unread`: unread notifications, polled). */
  badge?: 'unread'
}

export interface NavGroup {
  label: string | null
  items: NavItem[]
}

export const NAV_GROUPS: NavGroup[] = [
  {
    label: null,
    items: [
      { title: '概览', to: '/console', icon: LayoutDashboard },
      // Every signed-in user; the badge shows the unread count (no header bell by design).
      { title: '通知中心', to: '/console/notifications', icon: Bell, badge: 'unread' },
    ],
  },
  {
    label: '网关',
    items: [
      {
        title: '渠道',
        to: '/console/channels',
        icon: Cable,
        permission: 'channels.read',
      },
      {
        title: '模型广场',
        to: '/console/plaza',
        icon: Store,
      },
      {
        title: '我的模型',
        to: '/console/my-models',
        icon: Boxes,
      },
      {
        title: '路由',
        to: '/console/routes',
        icon: RouteIcon,
        permission: 'routes.manage',
      },
      // Kept in 网关 (not 管理) next to 路由: both configure the gateway, and channel
      // admins (models.manage + routes.manage, no users.read) would otherwise get a
      // 管理 group holding only this entry.
      {
        title: '模型管理',
        to: '/console/models',
        icon: SlidersHorizontal,
        permission: 'models.manage',
      },
      {
        title: 'API Keys',
        to: '/console/keys',
        icon: KeyRound,
        permission: 'keys.own',
      },
    ],
  },
  {
    label: '观测',
    items: [
      {
        title: '请求日志',
        to: '/console/logs',
        icon: ScrollText,
        permission: 'stats.own',
      },
      {
        title: '统计',
        to: '/console/stats',
        icon: BarChart3,
        permission: 'stats.own',
      },
    ],
  },
  {
    label: '计费',
    items: [
      {
        title: '钱包与订阅',
        to: '/console/billing',
        icon: Wallet,
        permission: 'billing.own',
      },
      {
        title: '套餐',
        to: '/console/billing/plans',
        icon: Package,
        permission: 'billing.manage',
      },
      {
        title: '订阅管理',
        to: '/console/billing/subscriptions',
        icon: CalendarClock,
        permission: 'billing.manage',
      },
      {
        title: '兑换码',
        to: '/console/billing/redeem-codes',
        icon: Ticket,
        permission: 'billing.manage',
      },
    ],
  },
  {
    label: '扩展',
    items: [
      {
        title: '插件',
        to: '/console/plugins',
        icon: Blocks,
        permission: 'plugins.read',
      },
    ],
  },
  {
    label: '管理',
    items: [
      { title: '用户', to: '/console/admin/users', icon: Users, permission: 'users.read' },
      { title: '用户组', to: '/console/admin/groups', icon: UsersRound, permission: 'users.read' },
      { title: '审计日志', to: '/console/admin/audit', icon: FileClock, permission: 'audit.read' },
      {
        title: '系统设置',
        to: '/console/admin/settings',
        icon: Settings,
        permission: 'settings.write',
      },
    ],
  },
]

export const NAV_ITEMS: NavItem[] = NAV_GROUPS.flatMap(g => g.items)

/**
 * Build-time documentation link (VITE_DOCS_URL). The runtime `site.docsUrl` setting
 * takes precedence — use `useSite().docsUrl` instead of reading this directly.
 */
export const DOCS_URL: string | null = import.meta.env.VITE_DOCS_URL || null
