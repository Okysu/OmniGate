<script setup lang="ts">
// Permanent plugin deletion (upload / editor plugins only; managers).
import type { Plugin } from '@/lib/types'
import { ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import { errorMessage, isApiError } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'

const props = defineProps<{ plugin: Plugin | null }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ deleted: [plugin: Plugin] }>()

const busy = ref(false)
/** Server refusal (409 plugin_in_use / plugin_builtin), shown inside the dialog. */
const conflict = ref<string | null>(null)

watch(open, (o) => {
  if (o)
    conflict.value = null
})

async function confirm() {
  const p = props.plugin
  if (!p)
    return
  busy.value = true
  conflict.value = null
  try {
    await pluginsApi.remove(p.id)
    open.value = false
    toast.success(`已删除插件「${p.name}」`)
    emit('deleted', p)
  }
  catch (err) {
    if (isApiError(err) && err.status === 409) {
      conflict.value = err.message
      toast.error('无法删除插件', { description: err.message })
    }
    else {
      toast.error('删除失败', { description: errorMessage(err) })
    }
  }
  finally {
    busy.value = false
  }
}
</script>

<template>
  <ConfirmDialog
    v-model:open="open"
    :title="`删除插件「${plugin?.name ?? ''}」？`"
    confirm-text="永久删除"
    destructive
    :loading="busy"
    @confirm="confirm"
  >
    <p>将永久删除该插件的<strong>全部已发布版本、草稿和插件存储数据</strong>，无法恢复。需要时请先在详情页导出各版本的 ZIP。</p>
    <p>仍被渠道使用（固定在任一版本上）的插件不能删除，请先删除这些渠道或改用其他插件。</p>
    <p v-if="plugin && plugin.channels > 0 && !conflict" class="text-amber-700 dark:text-amber-300">
      当前有 {{ plugin.channels }} 个渠道使用该插件，删除会被拒绝。
    </p>
    <p v-if="conflict" role="alert" class="border-destructive/40 bg-destructive/5 text-destructive rounded-md border px-3 py-2">
      {{ conflict }}
    </p>
  </ConfirmDialog>
</template>
