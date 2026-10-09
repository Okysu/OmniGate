<script setup lang="ts">
import type { Component } from 'vue'
import { Skeleton } from '@/components/ui/skeleton'

defineProps<{
  label: string
  value: string
  hint?: string
  icon?: Component
  loading?: boolean
  /** Full-precision value shown on hover (e.g. exact money). */
  title?: string
  /** Hover text for the hint line (e.g. its exact money amounts). */
  hintTitle?: string
}>()
</script>

<template>
  <div class="bg-card text-card-foreground ring-foreground/10 flex min-w-0 flex-col gap-1 rounded-xl p-4 ring-1">
    <div class="text-muted-foreground flex items-center gap-1.5 text-xs">
      <component :is="icon" v-if="icon" class="size-3.5 shrink-0" />
      <span class="truncate">{{ label }}</span>
    </div>
    <Skeleton v-if="loading" class="h-7 w-20" />
    <p v-else class="truncate text-xl font-semibold tabular-nums sm:text-2xl" :title="title ?? value">
      {{ value }}
    </p>
    <p v-if="hint" class="text-muted-foreground truncate text-xs" :title="hintTitle ?? hint">
      {{ hint }}
    </p>
  </div>
</template>
