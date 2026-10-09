<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import type { NavGroup } from '@/config/nav'
import { NAV_GROUPS } from '@/config/nav'
import { CONSOLE_BASE } from '@/lib/paths'
import { unreadBadgeText } from '@/lib/notifications'
import { useUnreadPolling } from '@/composables/useUnreadPolling'
import { siteNameOf } from '@/lib/site'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from '@/components/ui/sidebar'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'

const auth = useAuthStore()
const system = useSystemStore()
const siteName = computed(() => siteNameOf(system.info))
const route = useRoute()
const { isMobile, setOpenMobile } = useSidebar()
const { unread } = useUnreadPolling()
const unreadText = computed(() => unreadBadgeText(unread.value))

const groups = computed<NavGroup[]>(() =>
  NAV_GROUPS
    .map(g => ({ ...g, items: g.items.filter(item => auth.can(item.permission)) }))
    .filter(g => g.items.length > 0),
)

function isActive(to: string): boolean {
  if (to === CONSOLE_BASE)
    return route.path === CONSOLE_BASE
  // Longest-prefix match so /console/billing doesn't light up on /console/billing/plans.
  const candidates = NAV_GROUPS.flatMap(g => g.items.map(i => i.to))
    .filter(p => p !== CONSOLE_BASE && (route.path === p || route.path.startsWith(`${p}/`)))
  const best = candidates.sort((a, b) => b.length - a.length)[0]
  return best === to
}

// Close the mobile drawer after navigating.
watch(() => route.fullPath, () => {
  if (isMobile.value)
    setOpenMobile(false)
})
</script>

<template>
  <Sidebar collapsible="icon" class="top-(--header-height) h-[calc(100svh-var(--header-height))]!">
    <SidebarContent class="gap-0 py-1">
      <SidebarGroup v-for="(group, gi) in groups" :key="group.label ?? gi" class="py-1">
        <SidebarGroupLabel v-if="group.label">
          {{ group.label }}
        </SidebarGroupLabel>
        <SidebarMenu>
          <SidebarMenuItem v-for="item in group.items" :key="item.to">
            <SidebarMenuButton as-child :is-active="isActive(item.to)" :tooltip="item.badge === 'unread' && unreadText ? `${item.title}（${unreadText} 条未读）` : item.title">
              <RouterLink :to="item.to" :aria-label="item.badge === 'unread' && unreadText ? `${item.title}，${unreadText} 条未读` : undefined">
                <span class="relative flex shrink-0">
                  <component :is="item.icon" class="size-4" />
                  <!-- Collapsed (icon-only) sidebar: a dot instead of the count. -->
                  <span v-if="item.badge === 'unread' && unreadText" class="bg-destructive ring-sidebar absolute -top-0.5 -right-0.5 hidden size-2 rounded-full ring-2 group-data-[collapsible=icon]:block" aria-hidden="true" />
                </span>
                <span>{{ item.title }}</span>
              </RouterLink>
            </SidebarMenuButton>
            <SidebarMenuBadge v-if="item.badge === 'unread' && unreadText" class="bg-destructive min-w-5 rounded-full px-1.5 text-[10px] font-semibold text-white peer-hover/menu-button:text-white peer-data-active/menu-button:text-white" data-testid="nav-unread-badge">
              {{ unreadText }}
            </SidebarMenuBadge>
            <SidebarMenuBadge v-else-if="item.placeholder" class="text-muted-foreground text-[10px] font-normal">
              占位
            </SidebarMenuBadge>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarGroup>
    </SidebarContent>
    <SidebarFooter class="group-data-[collapsible=icon]:hidden">
      <p class="text-muted-foreground px-2 text-xs">
        {{ siteName }}<template v-if="system.info?.version">
          · {{ system.info.version }}
        </template>
      </p>
    </SidebarFooter>
    <SidebarRail />
  </Sidebar>
</template>
