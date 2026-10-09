<script setup lang="ts">
import type { ImportResult } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { CircleCheck, CircleX, FileArchive, Loader2, Upload } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { errorMessage } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { formatBytes } from '@/lib/pluginPermissions'
import DiagnosticsList from './DiagnosticsList.vue'
import RiskList from './RiskList.vue'

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ imported: [] }>()
const router = useRouter()

const MAX_BYTES = 4 << 20
const file = ref<File | null>(null)
const fileError = ref<string | null>(null)
const uploading = ref(false)
const result = ref<ImportResult | null>(null)
const input = ref<HTMLInputElement | null>(null)

watch(open, (v) => {
  if (v) {
    file.value = null
    fileError.value = null
    result.value = null
  }
})

function onPick(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0] ?? null
  fileError.value = null
  result.value = null
  if (f && f.size > MAX_BYTES) {
    fileError.value = '文件不能超过 4 MiB'
    file.value = null
    return
  }
  file.value = f
}

async function upload() {
  if (!file.value) {
    fileError.value = '请选择插件 ZIP 包'
    return
  }
  uploading.value = true
  fileError.value = null
  try {
    result.value = await pluginsApi.importZip(file.value)
    emit('imported')
  }
  catch (err) {
    fileError.value = errorMessage(err)
  }
  finally {
    uploading.value = false
  }
}

const diagnostics = computed(() => result.value?.build.diagnostics ?? [])
const risk = computed(() => result.value?.build.risk ?? [])

function goEditor() {
  if (!result.value)
    return
  open.value = false
  void router.push({ name: 'plugin-editor', params: { id: result.value.plugin.id } })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>导入插件 ZIP</DialogTitle>
        <DialogDescription>
          上传插件包（含 manifest.json，≤ 4 MiB）。新插件会被创建；ID 已存在时替换该插件的草稿，已发布的版本不受影响。导入不会自动发布。
        </DialogDescription>
      </DialogHeader>

      <div v-if="!result" class="space-y-3">
        <button
          type="button"
          class="hover:bg-muted/50 focus-visible:ring-ring/50 flex w-full flex-col items-center gap-2 rounded-lg border border-dashed p-6 text-center outline-none focus-visible:ring-3"
          @click="input?.click()"
        >
          <FileArchive class="text-muted-foreground size-8" />
          <span v-if="file" class="text-sm font-medium">{{ file.name }}（{{ formatBytes(file.size) }}）</span>
          <span v-else class="text-sm">点击选择 .zip 文件</span>
          <span class="text-muted-foreground text-xs">允许外层包一层目录；单文件 ≤ 256 KiB，解压后总计 ≤ 1 MiB，最多 64 个文件</span>
        </button>
        <input ref="input" type="file" accept=".zip,application/zip" class="sr-only" aria-label="选择插件 ZIP 文件" @change="onPick">
        <p v-if="fileError" class="text-destructive text-sm" role="alert">
          {{ fileError }}
        </p>
      </div>

      <div v-else class="space-y-4">
        <div class="flex items-start gap-3 rounded-lg border p-3">
          <CircleCheck v-if="result.build.ok" class="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-400" />
          <CircleX v-else class="text-destructive mt-0.5 size-5 shrink-0" />
          <div class="min-w-0 space-y-0.5 text-sm">
            <p class="font-medium">
              已导入「{{ result.plugin.name }}」<span class="text-muted-foreground font-mono text-xs"> {{ result.plugin.key }}</span>
            </p>
            <p class="text-muted-foreground">
              {{ result.build.ok ? `编译检查通过，产物 ${formatBytes(result.build.bundleBytes)}。可在编辑器中测试并发布。` : '草稿已保存，但编译检查未通过，请在编辑器中修正以下问题。' }}
            </p>
          </div>
        </div>
        <section v-if="diagnostics.length" class="space-y-1.5">
          <h3 class="text-sm font-semibold">
            问题（{{ diagnostics.length }}）
          </h3>
          <div class="max-h-60 overflow-y-auto rounded-lg border">
            <DiagnosticsList :diagnostics="diagnostics" />
          </div>
        </section>
        <section class="space-y-1.5">
          <h3 class="text-sm font-semibold">
            风险提示
          </h3>
          <RiskList :risk="risk" />
        </section>
      </div>

      <DialogFooter>
        <template v-if="!result">
          <Button variant="outline" :disabled="uploading" @click="open = false">
            取消
          </Button>
          <Button :disabled="uploading || !file" @click="upload">
            <Loader2 v-if="uploading" class="animate-spin" />
            <Upload v-else />
            上传并检查
          </Button>
        </template>
        <template v-else>
          <Button variant="outline" @click="open = false">
            关闭
          </Button>
          <Button @click="goEditor">
            打开编辑器
          </Button>
        </template>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
