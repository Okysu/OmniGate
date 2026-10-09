<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { RefreshCw, ServerCrash } from '@lucide/vue'
import AnnouncementBanner from './AnnouncementBanner.vue'
import AppHeader from './AppHeader.vue'
import AppSidebar from './AppSidebar.vue'
import ForbiddenPage from './ForbiddenPage.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import { useAuthStore } from '@/stores/auth'
import { announcementOf } from '@/lib/site'
import { useSystemStore } from '@/stores/system'

const auth = useAuthStore()
const system = useSystemStore()
const router = useRouter()
const route = useRoute()
const requiredPermission = computed(() => {
  const p = route.meta.permission
  return typeof p === 'function' ? p(route.query) : p
})
const retrying = ref(false)
const announcement = computed(() => announcementOf(system.info))

onMounted(() => {
  void system.ensureLoaded()
})

async function retry() {
  retrying.value = true
  try {
    await auth.refresh()
    // Re-run the navigation guard now that we (maybe) have a session answer.
    await router.replace(router.currentRoute.value.fullPath)
  }
  finally {
    retrying.value = false
  }
}
</script>

<template>
  <!-- Backend unreachable while probing the session: no point rendering the console. -->
  <div v-if="!auth.user && auth.error" class="flex min-h-svh items-center justify-center p-4">
    <Card class="w-full max-w-md">
      <CardHeader>
        <div class="flex items-center gap-3">
          <span class="bg-destructive/10 text-destructive flex size-10 items-center justify-center rounded-full">
            <ServerCrash class="size-5" />
          </span>
          <div>
            <CardTitle>无法连接到 OmniGate 服务</CardTitle>
            <CardDescription>{{ auth.error.message }}</CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <p class="text-muted-foreground text-sm">
          请确认后端服务已启动（开发环境默认监听 <code class="font-mono">localhost:8080</code>），然后重试。
        </p>
        <Button class="w-full" :disabled="retrying" @click="retry">
          <RefreshCw :class="retrying ? 'animate-spin' : ''" />
          重试
        </Button>
      </CardContent>
    </Card>
  </div>

  <div v-else class="[--header-height:calc(--spacing(14))]">
    <SidebarProvider class="flex flex-col">
      <AppHeader />
      <div class="flex flex-1">
        <AppSidebar />
        <SidebarInset class="min-w-0">
          <AnnouncementBanner :text="announcement" />
          <div :class="route.meta.fullBleed ? 'flex min-h-0 w-full flex-1 flex-col' : 'mx-auto w-full max-w-7xl flex-1 p-4 sm:p-6'">
            <!-- Cosmetic gate: the backend enforces permissions; this just avoids rendering
                 a page full of 403s when someone opens a URL they cannot use. -->
            <ForbiddenPage
              v-if="requiredPermission && auth.user && !auth.can(requiredPermission)"
              :title="route.meta.title ?? ''"
              :permission="requiredPermission"
            />
            <RouterView v-else />
          </div>
        </SidebarInset>
      </div>
    </SidebarProvider>
  </div>
</template>
