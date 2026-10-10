import type { LocationQuery, RouteRecordRaw } from 'vue-router'
import type { PlaceholderInfo } from '@/config/nav'
import { createRouter, createWebHistory } from 'vue-router'
import { NAV_ITEMS } from '@/config/nav'
import { CONSOLE_BASE, legacyRedirects, loginLocation, PUBLIC_PATHS, safeRedirect } from '@/lib/paths'
import { saveInviteCode } from '@/lib/referral'
import { landingEnabledOf, pageTitle, siteNameOf } from '@/lib/site'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'

declare module 'vue-router' {
  interface RouteMeta {
    /** Page title (Chinese), used for document.title and breadcrumbs. */
    title?: string
    /** Route is reachable without a session. */
    public?: boolean
    /**
     * Cosmetic permission hint (backend enforces). A function picks the
     * permission from the query (e.g. the editor's read-only version mode).
     */
    permission?: string | ((query: LocationQuery) => string)
    placeholder?: PlaceholderInfo
    /** Render edge to edge (no page padding / max width), e.g. the plugin editor. */
    fullBleed?: boolean
  }
}

const PlaceholderView = () => import('@/views/PlaceholderView.vue')

const placeholderRoutes: RouteRecordRaw[] = NAV_ITEMS
  .filter(item => item.placeholder)
  .map(item => ({
    // Child path of the console layout: "/console/routes" → "routes".
    path: item.to.slice(CONSOLE_BASE.length).replace(/^\//, ''),
    name: `placeholder:${item.to}`,
    component: PlaceholderView,
    meta: { title: item.title, permission: item.permission, placeholder: item.placeholder },
  }))

/** Every authenticated page, relative to {@link CONSOLE_BASE}. */
const consoleRoutes: RouteRecordRaw[] = [
  {
    path: '',
    name: 'overview',
    component: () => import('@/views/OverviewView.vue'),
    meta: { title: '概览' },
  },
  {
    path: 'notifications',
    name: 'notifications',
    component: () => import('@/views/notifications/NotificationsView.vue'),
    meta: { title: '通知中心' },
  },
  {
    path: 'notifications/settings',
    name: 'notification-settings',
    component: () => import('@/views/notifications/NotificationSettingsView.vue'),
    meta: { title: '通知设置' },
  },
  {
    path: 'settings/profile',
    name: 'profile',
    component: () => import('@/views/settings/ProfileView.vue'),
    meta: { title: '个人设置' },
  },
  {
    path: 'channels',
    name: 'channels',
    component: () => import('@/views/channels/ChannelsView.vue'),
    meta: { title: '渠道', permission: 'channels.read' },
  },
  {
    path: 'channels/:id',
    name: 'channel-detail',
    component: () => import('@/views/channels/ChannelDetailView.vue'),
    meta: { title: '渠道详情', permission: 'channels.read' },
  },
  {
    path: 'plugins',
    name: 'plugins',
    component: () => import('@/views/plugins/PluginsView.vue'),
    meta: { title: '插件', permission: 'plugins.read' },
  },
  {
    path: 'plugins/:id',
    name: 'plugin-detail',
    component: () => import('@/views/plugins/PluginDetailView.vue'),
    meta: { title: '插件详情', permission: 'plugins.read' },
  },
  {
    path: 'plugins/:id/edit',
    name: 'plugin-editor',
    // Monaco lives only in this chunk (and its own lazy chunks).
    component: () => import('@/views/plugins/editor/PluginEditorView.vue'),
    // `?version=<vid>` opens a published version read-only (plugins.read);
    // editing the draft requires plugins.manage.
    meta: {
      title: '插件编辑器',
      permission: query => (typeof query.version === 'string' && query.version ? 'plugins.read' : 'plugins.manage'),
      fullBleed: true,
    },
  },
  {
    path: 'plaza',
    name: 'plaza',
    component: () => import('@/views/plaza/PlazaView.vue'),
    meta: { title: '模型广场' },
  },
  {
    path: 'my-models',
    name: 'my-models',
    component: () => import('@/views/plaza/MyModelsView.vue'),
    meta: { title: '我的模型' },
  },
  {
    path: 'models',
    name: 'models',
    component: () => import('@/views/models/ModelsView.vue'),
    meta: { title: '模型管理', permission: 'models.manage' },
    // The old "模型" catalog was open to everyone; it is replaced by 模型广场 / 我的模型.
    // Old bookmarks of users without models.manage land on the plaza instead of a 403.
    beforeEnter: () => {
      const auth = useAuthStore()
      if (auth.isAuthenticated && !auth.can('models.manage'))
        return { name: 'plaza' }
      return true
    },
  },
  {
    path: 'keys',
    name: 'keys',
    component: () => import('@/views/keys/KeysView.vue'),
    meta: { title: 'API Keys', permission: 'keys.own' },
  },
  {
    path: 'logs',
    name: 'logs',
    component: () => import('@/views/request-logs/LogsView.vue'),
    meta: { title: '请求日志', permission: 'stats.own' },
  },
  {
    path: 'stats',
    name: 'stats',
    component: () => import('@/views/stats/StatsView.vue'),
    meta: { title: '统计', permission: 'stats.own' },
  },
  {
    path: 'billing',
    name: 'billing',
    component: () => import('@/views/billing/BillingView.vue'),
    meta: { title: '钱包与订阅', permission: 'billing.own' },
  },
  {
    path: 'store',
    name: 'store',
    component: () => import('@/views/billing/store/StoreView.vue'),
    meta: { title: '购买套餐', permission: 'billing.own' },
  },
  {
    path: 'referral',
    name: 'referral',
    component: () => import('@/views/billing/referral/ReferralView.vue'),
    meta: { title: '邀请返利', permission: 'billing.own' },
  },
  {
    path: 'billing/plans',
    name: 'billing-plans',
    component: () => import('@/views/billing/plans/PlansView.vue'),
    meta: { title: '套餐', permission: 'billing.manage' },
  },
  {
    path: 'billing/subscriptions',
    name: 'billing-subscriptions',
    component: () => import('@/views/billing/subscriptions/SubscriptionsView.vue'),
    meta: { title: '订阅管理', permission: 'billing.manage' },
  },
  {
    path: 'billing/redeem-codes',
    name: 'billing-redeem-codes',
    component: () => import('@/views/billing/RedeemCodesView.vue'),
    meta: { title: '兑换码', permission: 'billing.manage' },
  },
  {
    path: 'billing/reset-cards',
    name: 'billing-reset-cards',
    component: () => import('@/views/billing/reset-cards/ResetCardsView.vue'),
    meta: { title: '重置卡', permission: 'billing.manage' },
  },
  {
    path: 'admin/users',
    name: 'admin-users',
    component: () => import('@/views/admin/UsersView.vue'),
    meta: { title: '用户', permission: 'users.read' },
  },
  {
    path: 'admin/groups',
    name: 'admin-groups',
    component: () => import('@/views/admin/GroupsView.vue'),
    meta: { title: '用户组', permission: 'users.read' },
  },
  {
    path: 'routes',
    name: 'routes',
    component: () => import('@/views/routes/RoutesView.vue'),
    meta: { title: '路由', permission: 'routes.manage' },
  },
  {
    path: 'admin/settings',
    name: 'admin-settings',
    component: () => import('@/views/admin/SettingsView.vue'),
    meta: { title: '系统设置', permission: 'settings.write' },
  },
  {
    path: 'admin/audit',
    name: 'admin-audit',
    component: () => import('@/views/admin/AuditView.vue'),
    meta: { title: '审计日志', permission: 'audit.read' },
  },
  ...placeholderRoutes,
]

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'landing',
    component: () => import('@/views/landing/LandingView.vue'),
    meta: { public: true },
  },
  {
    // Public model plaza (phase5-api.md §3.3); 401 when `site.publicModelPlaza` is off.
    path: '/models',
    name: 'public-plaza',
    component: () => import('@/views/landing/ModelPlazaView.vue'),
    meta: { title: '模型广场', public: true },
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { title: '登录', public: true },
  },
  {
    path: CONSOLE_BASE,
    component: () => import('@/components/layout/AppShell.vue'),
    children: [
      ...consoleRoutes,
      {
        path: ':pathMatch(.*)*',
        name: 'not-found',
        component: () => import('@/views/NotFoundView.vue'),
        meta: { title: '页面不存在' },
      },
    ],
  },
  // Bookmarks from before the /console prefix: /channels/:id → /console/channels/:id.
  // `/models` is the public model plaza now, not a redirect to /console/models.
  ...legacyRedirects(consoleRoutes, PUBLIC_PATHS),
  {
    path: '/:pathMatch(.*)*',
    name: 'public-not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { title: '页面不存在', public: true },
  },
]

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
  scrollBehavior(to, from, saved) {
    if (saved)
      return saved
    // In-page anchors (landing page sections); the sticky header is ~64px tall.
    if (to.hash)
      return { el: to.hash, top: 72, behavior: to.path === from.path ? 'smooth' : 'auto' }
    return { top: 0 }
  },
})

router.beforeEach(async (to) => {
  // phase15 §4.2: in-app links carrying `?invite=` (the initial URL is captured in main.ts).
  if (to.query.invite)
    saveInviteCode(to.query.invite)

  const auth = useAuthStore()
  await auth.ensureLoaded()

  // `site.landingEnabled = false`: "/" goes straight to the console (login first when signed out).
  if (to.name === 'landing') {
    const system = useSystemStore()
    await system.ensureLoaded()
    if (!landingEnabledOf(system.info))
      return auth.isAuthenticated ? CONSOLE_BASE : loginLocation(CONSOLE_BASE)
  }

  if (to.meta.public) {
    // Already signed in: skip the login page.
    if (to.name === 'login' && auth.isAuthenticated)
      return safeRedirect(to.query.redirect)
    return true
  }

  // Backend unreachable / 5xx: let the shell render its connection-error state.
  if (auth.error)
    return true

  if (!auth.isAuthenticated)
    return loginLocation(to.fullPath)

  return true
})

// The landing page sets its own title; App.vue keeps the title in sync once the site name loads.
router.afterEach((to) => {
  if (to.name !== 'landing')
    document.title = pageTitle(to.meta.title, siteNameOf(useSystemStore().info))
})
