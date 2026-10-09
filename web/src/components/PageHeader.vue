<script setup lang="ts">
defineProps<{ title: string, description?: string }>()
</script>

<template>
  <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
    <div class="min-w-0 space-y-1">
      <div class="flex flex-wrap items-center gap-2">
        <h1 class="text-xl font-semibold tracking-tight sm:text-2xl">
          {{ title }}
        </h1>
        <slot name="badge" />
      </div>
      <p v-if="description" class="text-muted-foreground text-sm">
        {{ description }}
      </p>
    </div>
    <div v-if="$slots.actions" class="flex shrink-0 flex-wrap items-center gap-2">
      <slot name="actions" />
    </div>
  </div>
</template>
