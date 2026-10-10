<script setup lang="ts">
import { CircleAlert, Loader2, RefreshCw, Users } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { affectedText } from '@/lib/subscriptionBulk'

/** "将影响 N 份订阅" from the dry run (phase7-api.md §3.3). */
const props = defineProps<{
  loading: boolean
  error: string | null
  /** Preview matches the current request. */
  ready: boolean
  affected: number | null
  /** Request invalid / incomplete: nothing to preview. */
  idle?: boolean
  /** Result text (default: "将影响 N 份订阅"); reset cards count recipients. */
  format?: (affected: number) => string
}>()
defineEmits<{ retry: [] }>()
</script>

<template>
  <div class="flex min-h-11 items-center gap-2 rounded-lg border px-3 py-2 text-sm" aria-live="polite" data-testid="dry-run">
    <template v-if="idle">
      <Users class="text-muted-foreground size-4 shrink-0" />
      <span class="text-muted-foreground">填写完整后将预览影响范围</span>
    </template>
    <template v-else-if="error">
      <CircleAlert class="text-destructive size-4 shrink-0" />
      <span class="text-destructive min-w-0 flex-1 break-all">无法预览：{{ error }}</span>
      <Button type="button" variant="ghost" size="icon-xs" aria-label="重新预览" @click="$emit('retry')">
        <RefreshCw />
      </Button>
    </template>
    <template v-else-if="!ready || affected === null">
      <Loader2 class="text-muted-foreground size-4 shrink-0 animate-spin" />
      <span class="text-muted-foreground">正在计算影响范围…</span>
    </template>
    <template v-else>
      <Users class="size-4 shrink-0" :class="affected > 0 ? 'text-primary' : 'text-muted-foreground'" />
      <span :class="affected > 0 ? 'font-medium' : 'text-muted-foreground'" data-testid="dry-run-text">{{ (props.format ?? affectedText)(affected) }}</span>
    </template>
  </div>
</template>
