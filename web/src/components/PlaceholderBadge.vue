<script setup lang="ts">
import { Badge } from '@/components/ui/badge'

defineProps<{ phase?: string }>()
</script>

<template>
  <Badge variant="outline" class="border-dashed border-amber-500/60 text-amber-700 dark:text-amber-400">
    占位<template v-if="phase">
      · {{ phase }}
    </template>
  </Badge>
</template>
