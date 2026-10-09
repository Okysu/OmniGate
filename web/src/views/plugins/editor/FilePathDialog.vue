<script setup lang="ts">
import { ref, watch } from 'vue'
import FormField from '@/components/FormField.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { validatePath } from './files'

const props = defineProps<{
  /** null = create a new file; otherwise the path being renamed. */
  current: string | null
  existing: string[]
}>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ submit: [path: string] }>()

const value = ref('')
const error = ref('')

watch(open, (v) => {
  if (v) {
    value.value = props.current ?? 'src/'
    error.value = ''
  }
})

function submit() {
  const p = value.value.trim()
  error.value = validatePath(p, props.existing, props.current ?? undefined)
  if (error.value)
    return
  if (p !== props.current)
    emit('submit', p)
  open.value = false
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{{ current ? '重命名文件' : '新建文件' }}</DialogTitle>
        <DialogDescription>
          相对插件根目录的路径，例如 src/util.ts 或 tests/balance.json。只允许字母、数字、点、下划线、连字符和 /。
        </DialogDescription>
      </DialogHeader>
      <form id="file-path-form" novalidate @submit.prevent="submit">
        <FormField label="路径" for="file-path" :error="error">
          <Input id="file-path" v-model="value" class="font-mono text-sm" autocomplete="off" spellcheck="false" autofocus :aria-invalid="!!error" />
        </FormField>
      </form>
      <DialogFooter>
        <Button variant="outline" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="file-path-form">
          {{ current ? '重命名' : '创建' }}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
