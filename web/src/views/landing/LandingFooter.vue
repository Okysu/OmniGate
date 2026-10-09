<script setup lang="ts">
import { computed } from 'vue'
import AppLogo from '@/components/AppLogo.vue'
import { useSite } from '@/composables/useSite'
import { CONSOLE_BASE, loginLocation } from '@/lib/paths'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'
import { LANDING_NAV } from './nav'

/** Footer of the public site (landing page and `/models`). */
const auth = useAuthStore()
const system = useSystemStore()
const { siteName, docsUrl } = useSite()
const user = computed(() => auth.user)
const loginTo = loginLocation(CONSOLE_BASE)
const year = new Date().getFullYear()
</script>

<template>
  <footer class="border-t">
    <div class="text-muted-foreground mx-auto flex w-full max-w-6xl flex-col gap-6 px-4 py-10 text-sm sm:px-6 md:flex-row md:items-center md:justify-between">
      <div class="space-y-2">
        <div class="text-foreground flex items-center gap-2">
          <AppLogo :show-name="false" />
          <span class="font-semibold">{{ siteName }}</span>
        </div>
        <p>一个 Key，接入所有大模型</p>
      </div>
      <nav class="flex flex-wrap gap-x-5 gap-y-2" aria-label="页脚导航">
        <RouterLink v-for="n in LANDING_NAV" :key="n.key" :to="n.to" class="hover:text-foreground">
          {{ n.label }}
        </RouterLink>
        <a v-if="docsUrl" :href="docsUrl" target="_blank" rel="noopener noreferrer" class="hover:text-foreground">文档</a>
        <RouterLink :to="user ? CONSOLE_BASE : loginTo" class="hover:text-foreground">
          {{ user ? '控制台' : '登录' }}
        </RouterLink>
      </nav>
      <p class="text-xs">
        © {{ year }} {{ siteName }}<template v-if="system.info?.version">
          · {{ system.info.version }}
        </template>
      </p>
    </div>
  </footer>
</template>
