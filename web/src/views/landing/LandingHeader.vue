<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { BookOpen, Boxes, LogIn } from '@lucide/vue'
import AppLogo from '@/components/AppLogo.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { Button } from '@/components/ui/button'
import { useSite } from '@/composables/useSite'
import { CONSOLE_BASE, loginLocation } from '@/lib/paths'
import { useAuthStore } from '@/stores/auth'
import { LANDING_NAV } from './nav'

/** Sticky header of the public site (landing page and `/models`). */
const auth = useAuthStore()
const route = useRoute()
const { siteName, docsUrl } = useSite()
const user = computed(() => auth.user)
/** Log in and come back to the current public page (`/models`) or the console (`/`). */
const loginTo = computed(() => loginLocation(route.path === '/' ? CONSOLE_BASE : route.fullPath))
const onPlaza = computed(() => route.path === '/models')
</script>

<template>
  <header class="bg-background/80 supports-backdrop-filter:bg-background/65 sticky top-0 z-40 border-b backdrop-blur">
    <div class="mx-auto flex h-16 w-full max-w-6xl items-center gap-3 px-4 sm:px-6">
      <RouterLink to="/" class="focus-visible:ring-ring/50 flex min-w-0 items-center gap-2 rounded-md outline-none focus-visible:ring-3" :aria-label="`${siteName} 首页`">
        <AppLogo :show-name="false" />
        <span class="truncate font-semibold tracking-tight">{{ siteName }}</span>
      </RouterLink>
      <nav class="ml-4 hidden items-center gap-1 md:flex" aria-label="页面导航">
        <RouterLink
          v-for="n in LANDING_NAV"
          :key="n.key"
          :to="n.to"
          class="hover:text-foreground hover:bg-muted rounded-md px-3 py-1.5 text-sm transition-colors"
          :class="n.key === 'models' && onPlaza ? 'text-foreground bg-muted font-medium' : 'text-muted-foreground'"
          :aria-current="n.key === 'models' && onPlaza ? 'page' : undefined"
          :data-testid="n.key === 'models' ? 'landing-plaza-link' : undefined"
        >
          {{ n.label }}
        </RouterLink>
      </nav>
      <div class="ml-auto flex shrink-0 items-center gap-1 sm:gap-2">
        <!-- Phones: the section nav is hidden, keep the plaza reachable. -->
        <Button v-if="!onPlaza" variant="ghost" size="icon-sm" class="md:hidden" as-child>
          <RouterLink to="/models" aria-label="模型广场" title="模型广场" data-testid="landing-plaza-icon">
            <Boxes />
          </RouterLink>
        </Button>
        <Button v-if="docsUrl" variant="ghost" size="sm" class="hidden sm:inline-flex" as-child>
          <a :href="docsUrl" target="_blank" rel="noopener noreferrer">
            <BookOpen />
            文档
          </a>
        </Button>
        <ThemeToggle />
        <Button v-if="user" variant="outline" size="sm" class="gap-2 pl-1" as-child data-testid="landing-console">
          <RouterLink :to="CONSOLE_BASE">
            <UserAvatar :name="user.displayName" :src="user.avatarUrl" size="sm" />
            <span class="hidden sm:inline">进入控制台</span>
            <span class="sm:hidden">控制台</span>
          </RouterLink>
        </Button>
        <Button v-else size="sm" as-child data-testid="landing-login">
          <RouterLink :to="loginTo">
            <LogIn />
            登录
          </RouterLink>
        </Button>
      </div>
    </div>
  </header>
</template>
