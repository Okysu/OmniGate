<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowLeft, Home, LayoutDashboard } from '@lucide/vue'
import AppLogo from '@/components/AppLogo.vue'
import { Button } from '@/components/ui/button'
import { CONSOLE_BASE, isConsolePath } from '@/lib/paths'

const route = useRoute()
/** Rendered inside the console shell (vs. the public catch-all). */
const inConsole = computed(() => isConsolePath(route.path))

function back() {
  if (window.history.length > 1)
    window.history.back()
}
</script>

<template>
  <div :class="inConsole ? '' : 'bg-muted/40 flex min-h-svh flex-col px-4'">
    <header v-if="!inConsole" class="mx-auto flex h-16 w-full max-w-6xl items-center">
      <RouterLink to="/" class="focus-visible:ring-ring/50 rounded-md outline-none focus-visible:ring-3" aria-label="返回首页">
        <AppLogo />
      </RouterLink>
    </header>
    <div class="flex flex-1 flex-col items-center justify-center gap-4 py-24 text-center">
      <p class="text-muted-foreground font-mono text-6xl font-semibold tracking-tight">
        404
      </p>
      <div class="space-y-1">
        <h1 class="text-xl font-semibold">
          页面不存在
        </h1>
        <p class="text-muted-foreground text-sm break-all">
          找不到 <code class="font-mono">{{ route.path }}</code>，它可能已被移动或从未存在。
        </p>
      </div>
      <div class="flex flex-wrap justify-center gap-2">
        <Button variant="outline" @click="back">
          <ArrowLeft />
          返回上一页
        </Button>
        <Button v-if="inConsole" as-child>
          <RouterLink :to="CONSOLE_BASE">
            <LayoutDashboard />
            回到概览
          </RouterLink>
        </Button>
        <template v-else>
          <Button variant="outline" as-child>
            <RouterLink to="/">
              <Home />
              返回首页
            </RouterLink>
          </Button>
          <Button as-child>
            <RouterLink :to="CONSOLE_BASE">
              <LayoutDashboard />
              进入控制台
            </RouterLink>
          </Button>
        </template>
      </div>
    </div>
  </div>
</template>
