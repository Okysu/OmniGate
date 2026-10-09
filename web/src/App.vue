<script setup lang="ts">
import { watch } from 'vue'
import { useRoute } from 'vue-router'
import { TooltipProvider } from 'reka-ui'
import { Toaster } from '@/components/ui/sonner'
import { useTheme } from '@/composables/useTheme'
import { pageTitle, siteNameOf } from '@/lib/site'
import { useSystemStore } from '@/stores/system'

const { resolved } = useTheme()

// The router sets the title on navigation; this re-applies it once the site name
// arrives (or changes after saving the system settings). The landing page owns its title.
const route = useRoute()
const system = useSystemStore()
watch(() => [siteNameOf(system.info), route.meta.title, route.name] as const, ([site, title, name]) => {
  if (name !== 'landing' && name !== undefined)
    document.title = pageTitle(title, site)
})
</script>

<template>
  <TooltipProvider :delay-duration="300">
    <RouterView />
  </TooltipProvider>
  <Toaster :theme="resolved" position="bottom-right" close-button />
</template>
