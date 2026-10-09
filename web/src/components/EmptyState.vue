<script setup lang="ts">
import type { Component } from 'vue'
import { Inbox } from '@lucide/vue'

withDefaults(defineProps<{ title: string, description?: string, icon?: Component }>(), {
  description: undefined,
  icon: () => Inbox,
})
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-3 px-4 py-10 text-center">
    <span class="bg-muted text-muted-foreground flex size-10 items-center justify-center rounded-full">
      <component :is="icon" class="size-5" />
    </span>
    <div class="space-y-1">
      <p class="font-medium">
        {{ title }}
      </p>
      <p v-if="description" class="text-muted-foreground max-w-md text-sm">
        {{ description }}
      </p>
    </div>
    <slot />
  </div>
</template>
