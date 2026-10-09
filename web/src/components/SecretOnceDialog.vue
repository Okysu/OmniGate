<script setup lang="ts">
import { TriangleAlert } from '@lucide/vue'
import CopyButton from '@/components/CopyButton.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'

/**
 * Shows a secret exactly once. The parent must drop its copy of the secret when
 * the dialog closes (listen to `update:open`).
 */
defineProps<{
  title: string
  description?: string
  secret: string
}>()
const open = defineModel<boolean>('open', { required: true })
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-lg" @interact-outside="(e: Event) => e.preventDefault()">
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription v-if="description">
          {{ description }}
        </DialogDescription>
      </DialogHeader>
      <div class="space-y-3">
        <div class="flex items-center gap-2 rounded-lg border p-2 pl-3">
          <code class="min-w-0 flex-1 font-mono text-xs break-all select-all" data-testid="secret-value">{{ secret }}</code>
          <CopyButton :value="secret" show-label variant="outline" />
        </div>
        <div class="flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-xs">
          <TriangleAlert class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <p>
            这是<strong>唯一一次</strong>显示完整密钥。请立即复制并妥善保存；关闭后无法再次查看，遗失只能轮换或重新创建。
            不要把密钥提交到代码仓库或分享给他人。
          </p>
        </div>
      </div>
      <DialogFooter>
        <Button @click="open = false">
          我已保存
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
