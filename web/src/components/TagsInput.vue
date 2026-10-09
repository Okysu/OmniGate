<script setup lang="ts">
import { ref } from 'vue'
import { X } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { normalizeTags, parseTags } from '@/lib/modelInfoForm'

/**
 * Free-text tags: Enter, comma or paste adds; Backspace on an empty input removes the
 * last tag. Duplicates (case-insensitive) are dropped. Over-long tags stay visible and
 * are highlighted so the validation message has something to point at.
 */
const props = withDefaults(defineProps<{
  id?: string
  invalid?: boolean
  placeholder?: string
  /** Highlight tags longer than this. */
  maxLength?: number
}>(), { id: undefined, invalid: false, placeholder: '输入后回车添加', maxLength: undefined })
const model = defineModel<string[]>({ required: true })
const draft = ref('')

function commit() {
  const add = parseTags(draft.value)
  if (add.length)
    model.value = normalizeTags([...model.value, ...add])
  draft.value = ''
}
function onKeydown(e: KeyboardEvent) {
  if (e.isComposing)
    return
  if (e.key === 'Enter' || e.key === ',' || e.key === '，') {
    e.preventDefault()
    commit()
  }
  else if (e.key === 'Backspace' && draft.value === '' && model.value.length) {
    model.value = model.value.slice(0, -1)
  }
}
function onPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text') ?? ''
  if (/[,，;；\n]/.test(text)) {
    e.preventDefault()
    draft.value += text
    commit()
  }
}
function remove(t: string) {
  model.value = model.value.filter(x => x !== t)
}
function tooLong(t: string) {
  return props.maxLength !== undefined && t.length > props.maxLength
}
</script>

<template>
  <div class="border-input focus-within:border-ring focus-within:ring-ring/50 dark:bg-input/30 flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border px-2 py-1.5 shadow-xs focus-within:ring-3" :class="invalid ? 'border-destructive' : ''">
    <Badge
      v-for="t in model"
      :key="t"
      variant="secondary"
      class="h-6 max-w-full gap-1 pr-1"
      :class="tooLong(t) ? 'bg-destructive/10 text-destructive' : ''"
    >
      <span class="truncate">{{ t }}</span>
      <button type="button" class="hover:bg-foreground/10 rounded-sm p-0.5" :aria-label="`移除 ${t}`" @click="remove(t)">
        <X class="size-3" />
      </button>
    </Badge>
    <Input
      :id="id"
      v-model="draft"
      class="h-6 min-w-32 flex-1 border-0 px-1 text-sm shadow-none focus-visible:ring-0 dark:bg-transparent"
      :placeholder="placeholder"
      autocomplete="off"
      :aria-invalid="invalid"
      @keydown="onKeydown"
      @paste="onPaste"
      @blur="commit"
    />
  </div>
</template>
