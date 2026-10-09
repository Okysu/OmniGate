<script setup lang="ts">
import { ref } from 'vue'
import { X } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { isValidDomain, parseDomains } from '@/lib/settingsForm'

/** Email domains as tags: Enter, comma, space or paste adds; Backspace on empty input removes the last. */
defineProps<{ id?: string, invalid?: boolean }>()
const model = defineModel<string[]>({ required: true })
const draft = ref('')

function commit() {
  const add = parseDomains(draft.value)
  if (add.length)
    model.value = [...new Set([...model.value, ...add])]
  draft.value = ''
}
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ',' || e.key === '，' || e.key === ' ') {
    e.preventDefault()
    commit()
  }
  else if (e.key === 'Backspace' && draft.value === '' && model.value.length) {
    model.value = model.value.slice(0, -1)
  }
}
function onPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text') ?? ''
  if (/[\s,，;；]/.test(text)) {
    e.preventDefault()
    draft.value += text
    commit()
  }
}
function remove(d: string) {
  model.value = model.value.filter(x => x !== d)
}
</script>

<template>
  <div class="border-input focus-within:border-ring focus-within:ring-ring/50 flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border px-2 py-1.5 focus-within:ring-3" :class="invalid ? 'border-destructive' : ''">
    <Badge
      v-for="d in model"
      :key="d"
      variant="secondary"
      class="h-6 gap-1 pr-1 font-mono"
      :class="isValidDomain(d) ? '' : 'bg-destructive/10 text-destructive'"
    >
      {{ d }}
      <button type="button" class="hover:bg-foreground/10 rounded-sm p-0.5" :aria-label="`移除 ${d}`" @click="remove(d)">
        <X class="size-3" />
      </button>
    </Badge>
    <Input
      :id="id"
      v-model="draft"
      class="h-6 min-w-40 flex-1 border-0 px-1 font-mono text-xs shadow-none focus-visible:ring-0 dark:bg-transparent"
      placeholder="example.com，回车添加"
      autocomplete="off"
      :aria-invalid="invalid"
      @keydown="onKeydown"
      @paste="onPaste"
      @blur="commit"
    />
  </div>
</template>
