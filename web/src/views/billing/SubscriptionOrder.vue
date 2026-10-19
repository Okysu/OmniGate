<script setup lang="ts">
import type { Subscription } from '@/lib/types'
import { nextTick, ref, watch } from 'vue'
import { ArrowDown, ArrowUp, GripVertical, Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/lib/api'
import { billingApi } from '@/lib/endpoints'
import { formatDateTime } from '@/lib/format'
import { moveItem } from '@/lib/routeForm'

/**
 * 额度使用顺序: the user's active subscriptions in the order requests use them (the
 * first one with quota left serves a request). Drag rows (mouse) or use the arrows
 * (keyboard / touch) to reorder; the order is saved at once.
 */
const props = defineProps<{
  /** Active subscriptions, in the current use order. */
  subscriptions: Subscription[]
}>()
const emit = defineEmits<{ saved: [items: Subscription[]] }>()

const rows = ref<Subscription[]>([...props.subscriptions])
watch(() => props.subscriptions, (v) => {
  rows.value = [...v]
})

const saving = ref(false)
async function apply(next: Subscription[], focusKey?: string) {
  const prev = rows.value
  rows.value = next
  if (focusKey) {
    void nextTick(() => {
      const el = document.querySelector<HTMLButtonElement>(`[data-sub-move="${focusKey}"]`)
      if (el && !el.disabled)
        el.focus()
    })
  }
  saving.value = true
  try {
    const res = await billingApi.reorderSubscriptions(next.map(s => s.id))
    toast.success('使用顺序已保存', { description: '之后的请求按新的顺序使用套餐额度。' })
    emit('saved', res.items)
  }
  catch (err) {
    rows.value = prev
    toast.error('保存顺序失败', { description: errorMessage(err) })
  }
  finally {
    saving.value = false
  }
}
function move(i: number, delta: number) {
  const s = rows.value[i]
  const to = i + delta
  if (!s || saving.value || to < 0 || to >= rows.value.length)
    return
  void apply(moveItem(rows.value, i, to), `${delta < 0 ? 'up' : 'down'}:${s.id}`)
}

const dragFrom = ref<number | null>(null)
const dragOver = ref<number | null>(null)
function onDragStart(i: number, e: DragEvent) {
  if (saving.value) {
    e.preventDefault()
    return
  }
  dragFrom.value = i
  e.dataTransfer?.setData('text/plain', String(i))
  if (e.dataTransfer)
    e.dataTransfer.effectAllowed = 'move'
}
function onDragOver(i: number, e: DragEvent) {
  if (dragFrom.value === null)
    return
  e.preventDefault()
  dragOver.value = i
}
function onDrop(i: number) {
  const from = dragFrom.value
  dragFrom.value = dragOver.value = null
  if (from === null || from === i)
    return
  void apply(moveItem(rows.value, from, i))
}
function onDragEnd() {
  dragFrom.value = dragOver.value = null
}
</script>

<template>
  <div class="bg-card ring-foreground/10 space-y-3 rounded-xl p-4 ring-1" data-testid="subscription-order">
    <div class="flex items-start justify-between gap-2">
      <div class="space-y-1">
        <h3 class="text-sm font-medium">
          额度使用顺序
        </h3>
        <p class="text-muted-foreground text-xs">
          请求从上往下使用覆盖该模型的套餐，前一个的额度用完才用下一个。拖动或用箭头调整；未调整时默认先到期的先用。
        </p>
      </div>
      <Loader2 v-if="saving" class="text-muted-foreground size-4 shrink-0 animate-spin" aria-label="正在保存" />
    </div>
    <ol class="space-y-1.5" :aria-busy="saving">
      <li
        v-for="(s, i) in rows"
        :key="s.id"
        draggable="true"
        class="bg-background flex items-center gap-2 rounded-lg border px-2.5 py-2 text-sm transition-opacity"
        :class="[
          dragOver === i && dragFrom !== null && dragFrom !== i ? (dragFrom > i ? 'shadow-[inset_0_2px_0_var(--primary)]' : 'shadow-[inset_0_-2px_0_var(--primary)]') : '',
          dragFrom === i ? 'opacity-50' : '',
        ]"
        :data-testid="`sub-order-${s.id}`"
        @dragstart="onDragStart(i, $event)"
        @dragover="onDragOver(i, $event)"
        @drop.prevent="onDrop(i)"
        @dragend="onDragEnd"
      >
        <GripVertical class="text-muted-foreground hidden size-4 shrink-0 cursor-grab sm:block" aria-hidden="true" />
        <span class="text-muted-foreground w-5 text-center text-xs tabular-nums">{{ i + 1 }}</span>
        <span class="min-w-0 flex-1 truncate font-medium" :title="s.plan.name">{{ s.plan.name }}</span>
        <span class="text-muted-foreground hidden shrink-0 text-xs tabular-nums sm:inline">至 {{ formatDateTime(s.endsAt) }}</span>
        <Button variant="ghost" size="icon-xs" :data-sub-move="`up:${s.id}`" :disabled="i === 0 || saving" :aria-label="`上移「${s.plan.name}」`" @click="move(i, -1)">
          <ArrowUp />
        </Button>
        <Button variant="ghost" size="icon-xs" :data-sub-move="`down:${s.id}`" :disabled="i === rows.length - 1 || saving" :aria-label="`下移「${s.plan.name}」`" @click="move(i, 1)">
          <ArrowDown />
        </Button>
      </li>
    </ol>
  </div>
</template>
