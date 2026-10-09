<script setup lang="ts">
import { Bell, SlidersHorizontal } from '@lucide/vue'
import { useRoute } from 'vue-router'

/** Route-level tabs shared by 通知中心 and 通知设置 (each tab is its own URL). */
const route = useRoute()
const TABS = [
  { name: 'notifications', to: '/console/notifications', label: '通知', icon: Bell },
  { name: 'notification-settings', to: '/console/notifications/settings', label: '通知设置', icon: SlidersHorizontal },
] as const
</script>

<template>
  <nav aria-label="通知中心" class="bg-muted text-muted-foreground inline-flex h-9 w-fit items-center rounded-lg p-0.75">
    <RouterLink
      v-for="t in TABS"
      :key="t.name"
      :to="t.to"
      class="focus-visible:ring-ring/50 dark:text-muted-foreground hover:text-foreground inline-flex h-[calc(100%-1px)] items-center gap-1.5 rounded-md border border-transparent px-3 text-sm font-medium whitespace-nowrap transition-all outline-none focus-visible:ring-3"
      :class="route.name === t.name ? 'bg-background text-foreground! dark:border-input dark:bg-input/30 shadow-sm' : 'text-foreground/60'"
      :aria-current="route.name === t.name ? 'page' : undefined"
    >
      <component :is="t.icon" class="size-4" />
      {{ t.label }}
    </RouterLink>
  </nav>
</template>
