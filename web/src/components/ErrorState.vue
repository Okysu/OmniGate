<script setup lang="ts">
import { computed } from 'vue'
import { CircleAlert, Lock, RefreshCw } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { isApiError } from '@/lib/api'

const props = withDefaults(defineProps<{ error: unknown, title?: string, retryable?: boolean }>(), {
  title: undefined,
  retryable: true,
})
defineEmits<{ retry: [] }>()

const forbidden = computed(() => isApiError(props.error) && props.error.isForbidden)
const heading = computed(() => props.title ?? (forbidden.value ? '没有访问权限' : '加载失败'))
const message = computed(() => {
  if (forbidden.value)
    return '你的账号没有访问此内容的权限。如有需要，请联系系统管理员。'
  if (isApiError(props.error))
    return props.error.message
  if (props.error instanceof Error)
    return props.error.message
  return '发生未知错误'
})
const requestId = computed(() => (isApiError(props.error) ? props.error.requestId : null))
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-3 px-4 py-12 text-center">
    <span class="bg-destructive/10 text-destructive flex size-10 items-center justify-center rounded-full">
      <Lock v-if="forbidden" class="size-5" />
      <CircleAlert v-else class="size-5" />
    </span>
    <div class="space-y-1">
      <p class="font-medium">
        {{ heading }}
      </p>
      <p class="text-muted-foreground max-w-md text-sm">
        {{ message }}
      </p>
      <p v-if="requestId" class="text-muted-foreground font-mono text-xs">
        请求 ID：{{ requestId }}
      </p>
    </div>
    <Button v-if="retryable && !forbidden" variant="outline" size="sm" @click="$emit('retry')">
      <RefreshCw />
      重试
    </Button>
  </div>
</template>
