<script setup lang="ts">
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from '@lucide/vue'
import { Button } from '@/components/ui/button'

const props = defineProps<{ page: number, pageSize: number, total: number, disabled?: boolean }>()
const emit = defineEmits<{ 'update:page': [page: number] }>()

const pageCount = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
const from = computed(() => (props.total === 0 ? 0 : (props.page - 1) * props.pageSize + 1))
const to = computed(() => Math.min(props.total, props.page * props.pageSize))

function go(p: number) {
  const next = Math.min(Math.max(1, p), pageCount.value)
  if (next !== props.page)
    emit('update:page', next)
}
</script>

<template>
  <div class="flex flex-col items-center justify-between gap-2 text-sm sm:flex-row">
    <p class="text-muted-foreground">
      共 {{ total }} 条<span v-if="total > 0">，第 {{ from }}–{{ to }} 条</span>
    </p>
    <div class="flex items-center gap-2">
      <Button variant="outline" size="sm" :disabled="disabled || page <= 1" @click="go(page - 1)">
        <ChevronLeft />
        上一页
      </Button>
      <span class="text-muted-foreground tabular-nums">{{ page }} / {{ pageCount }}</span>
      <Button variant="outline" size="sm" :disabled="disabled || page >= pageCount" @click="go(page + 1)">
        下一页
        <ChevronRight />
      </Button>
    </div>
  </div>
</template>
