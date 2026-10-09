<script setup lang="ts">
import { Construction } from '@lucide/vue'
import PageHeader from '@/components/PageHeader.vue'
import PlaceholderBadge from '@/components/PlaceholderBadge.vue'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

defineProps<{
  title: string
  /** Roadmap phase, e.g. "Phase 1". */
  phase: string
  description: string
  features?: string[]
}>()
</script>

<template>
  <div class="space-y-6">
    <PageHeader :title="title">
      <template #badge>
        <PlaceholderBadge :phase="phase" />
      </template>
    </PageHeader>

    <Card class="border-dashed">
      <CardHeader>
        <div class="flex items-center gap-3">
          <span class="bg-muted text-muted-foreground flex size-10 shrink-0 items-center justify-center rounded-lg">
            <Construction class="size-5" />
          </span>
          <div class="min-w-0">
            <CardTitle>此页面尚未实现</CardTitle>
            <CardDescription>计划于 {{ phase }} 交付，当前仅为导航占位，不展示任何数据。</CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <p class="text-sm leading-relaxed">
          {{ description }}
        </p>
        <div v-if="features?.length" class="space-y-2">
          <p class="text-muted-foreground text-xs font-medium">
            规划功能
          </p>
          <ul class="flex flex-wrap gap-2">
            <li v-for="f in features" :key="f">
              <Badge variant="secondary">
                {{ f }}
              </Badge>
            </li>
          </ul>
        </div>
        <slot />
      </CardContent>
    </Card>
  </div>
</template>
