<script setup lang="ts">
import PageHeader from '@/components/PageHeader.vue'
import { Card, CardContent } from '@/components/ui/card'
import { ApiError } from '@/lib/api'
import ErrorState from '@/components/ErrorState.vue'

defineProps<{ title: string, permission: string }>()
const forbidden = new ApiError(403, 'forbidden', '没有权限')
</script>

<template>
  <div class="space-y-6">
    <PageHeader :title="title" />
    <Card>
      <CardContent>
        <ErrorState :error="forbidden" />
        <p class="text-muted-foreground -mt-6 pb-4 text-center text-xs">
          需要权限：<code class="font-mono">{{ permission }}</code>
        </p>
      </CardContent>
    </Card>
  </div>
</template>
