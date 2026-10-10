<script setup lang="ts">
import { BookOpen } from '@lucide/vue'
import ThemeToggle from './ThemeToggle.vue'
import UserMenu from './UserMenu.vue'
import AppLogo from '@/components/AppLogo.vue'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { SidebarTrigger } from '@/components/ui/sidebar'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useSite } from '@/composables/useSite'
import { CONSOLE_BASE } from '@/lib/paths'

const { docsUrl, landingEnabled } = useSite()
</script>

<template>
  <header class="bg-background/95 supports-backdrop-filter:bg-background/80 sticky top-0 z-50 flex h-(--header-height) w-full shrink-0 items-center gap-1 border-b px-2 backdrop-blur sm:gap-2 sm:px-4">
    <SidebarTrigger class="-ml-1" />
    <Separator orientation="vertical" class="mr-1 hidden h-5! sm:block" />
    <!-- The logo leads back to the public landing page (or the overview when the landing page is disabled). -->
    <Tooltip>
      <TooltipTrigger as-child>
        <RouterLink :to="landingEnabled ? '/' : CONSOLE_BASE" class="min-w-0 rounded-md outline-none focus-visible:ring-3 focus-visible:ring-ring/50" :aria-label="landingEnabled ? '返回首页' : '返回概览'">
          <AppLogo />
        </RouterLink>
      </TooltipTrigger>
      <TooltipContent>{{ landingEnabled ? '返回首页' : '返回概览' }}</TooltipContent>
    </Tooltip>

    <div class="flex-1" />

    <div class="flex shrink-0 items-center gap-0.5 sm:gap-1">
      <Tooltip>
        <TooltipTrigger as-child>
          <Button v-if="docsUrl" variant="ghost" size="icon-sm" class="hidden sm:inline-flex" as-child>
            <a :href="docsUrl" target="_blank" rel="noopener noreferrer" aria-label="文档">
              <BookOpen />
            </a>
          </Button>
          <Button v-else variant="ghost" size="icon-sm" aria-label="文档" aria-disabled="true" class="hidden opacity-50 sm:inline-flex">
            <BookOpen />
          </Button>
        </TooltipTrigger>
        <TooltipContent>
          {{ docsUrl ? '文档' : '文档地址未配置（在「系统设置」中填写文档链接）' }}
        </TooltipContent>
      </Tooltip>
      <ThemeToggle />
      <UserMenu />
    </div>
  </header>
</template>
