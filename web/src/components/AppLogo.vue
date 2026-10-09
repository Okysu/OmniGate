<script setup lang="ts">
import { computed } from 'vue'
import { siteNameOf } from '@/lib/site'
import { useSystemStore } from '@/stores/system'

const props = withDefaults(defineProps<{ showName?: boolean, name?: string }>(), { showName: true, name: undefined })
const system = useSystemStore()
const displayName = computed(() => props.name ?? siteNameOf(system.info))
</script>

<template>
  <span class="inline-flex min-w-0 items-center gap-2 font-semibold tracking-tight">
    <span class="bg-primary text-primary-foreground flex size-7 shrink-0 items-center justify-center rounded-md">
      <svg viewBox="0 0 32 32" fill="none" class="size-5" aria-hidden="true">
        <path d="M9 23V13a7 7 0 0 1 14 0v10" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" />
        <path d="M13 23v-8a3 3 0 0 1 6 0v8" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" />
      </svg>
    </span>
    <span v-if="showName" class="max-w-40 truncate sm:max-w-64" :title="displayName">{{ displayName }}</span>
  </span>
</template>
