<script setup lang="ts">
import type { AvatarVariants } from '@/components/ui/avatar'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/lib/format'

defineProps<{ name: string, src?: string | null, size?: AvatarVariants['size'] }>()
</script>

<template>
  <Avatar :size="size">
    <AvatarImage v-if="src" :src="src" :alt="name" referrerpolicy="no-referrer" />
    <AvatarFallback class="text-xs">
      {{ initials(name) }}
    </AvatarFallback>
  </Avatar>
</template>
