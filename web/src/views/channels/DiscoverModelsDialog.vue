<script setup lang="ts">
import type { DiscoverDiff } from '@/lib/channelForm'
import type { ModelMapping } from '@/lib/types'
import { computed, ref, watch } from 'vue'
import { Loader2, RefreshCw, Search } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/api'
import { appendModels, diffDiscoveredModels } from '@/lib/channelForm'
import { channelsApi } from '@/lib/endpoints'

const props = defineProps<{ channelId: string, models: ModelMapping[] }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ add: [rows: ModelMapping[]] }>()

const loading = ref(false)
const error = ref<string | null>(null)
const diff = ref<DiscoverDiff | null>(null)
const picked = ref<Set<string>>(new Set())
const filter = ref('')

async function load() {
  loading.value = true
  error.value = null
  diff.value = null
  picked.value = new Set()
  try {
    const res = await channelsApi.discoverModels(props.channelId)
    diff.value = diffDiscoveredModels(props.models, res.models)
  }
  catch (err) {
    error.value = errorMessage(err)
  }
  finally {
    loading.value = false
  }
}

watch(open, (v) => {
  if (v) {
    filter.value = ''
    void load()
  }
})

const visibleAdded = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const list = diff.value?.added ?? []
  return q ? list.filter(m => m.toLowerCase().includes(q)) : list
})

function toggle(id: string, v: boolean | 'indeterminate') {
  const next = new Set(picked.value)
  if (v === true)
    next.add(id)
  else
    next.delete(id)
  picked.value = next
}

const allVisiblePicked = computed(() => visibleAdded.value.length > 0 && visibleAdded.value.every(m => picked.value.has(m)))
function toggleAll() {
  const next = new Set(picked.value)
  if (allVisiblePicked.value)
    visibleAdded.value.forEach(m => next.delete(m))
  else
    visibleAdded.value.forEach(m => next.add(m))
  picked.value = next
}

function confirm() {
  emit('add', appendModels(props.models, [...picked.value].sort()))
  open.value = false
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] grid-rows-[auto_1fr_auto] sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>从上游获取模型</DialogTitle>
        <DialogDescription>
          使用已保存的 Base URL 与 API Key 请求上游模型列表。勾选的模型会以相同名称加入映射，<strong>点击保存后才会生效</strong>。
        </DialogDescription>
      </DialogHeader>

      <div class="min-h-0 space-y-3 overflow-y-auto">
        <div v-if="loading" class="text-muted-foreground flex items-center justify-center gap-2 py-10 text-sm">
          <Loader2 class="size-4 animate-spin" />
          正在请求上游…
        </div>
        <div v-else-if="error" class="space-y-3 py-4 text-center">
          <p class="text-destructive text-sm">
            {{ error }}
          </p>
          <Button variant="outline" size="sm" @click="load">
            <RefreshCw />
            重试
          </Button>
        </div>
        <template v-else-if="diff">
          <div class="flex flex-wrap gap-2 text-xs">
            <Badge variant="secondary">
              新模型 {{ diff.added.length }}
            </Badge>
            <Badge variant="outline">
              已映射 {{ diff.existing.length }}
            </Badge>
            <Badge v-if="diff.missing.length" variant="destructive">
              上游未返回 {{ diff.missing.length }}
            </Badge>
          </div>

          <div v-if="diff.added.length" class="space-y-2">
            <div class="flex items-center gap-2">
              <div class="relative flex-1">
                <Search class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input v-model="filter" placeholder="过滤模型" class="h-8 pl-8" />
              </div>
              <Button variant="outline" size="sm" :disabled="visibleAdded.length === 0" @click="toggleAll">
                {{ allVisiblePicked ? '取消全选' : '全选' }}
              </Button>
            </div>
            <ul class="max-h-64 divide-y overflow-y-auto rounded-md border">
              <li v-for="m in visibleAdded" :key="m">
                <label class="hover:bg-muted flex cursor-pointer items-center gap-2 px-2.5 py-1.5">
                  <Checkbox :model-value="picked.has(m)" @update:model-value="(v) => toggle(m, v)" />
                  <span class="truncate font-mono text-xs">{{ m }}</span>
                </label>
              </li>
              <li v-if="visibleAdded.length === 0" class="text-muted-foreground px-2.5 py-3 text-center text-xs">
                没有匹配的模型
              </li>
            </ul>
          </div>
          <p v-else class="text-muted-foreground py-4 text-center text-sm">
            上游返回的模型都已在映射中。
          </p>

          <details v-if="diff.existing.length" class="text-xs">
            <summary class="text-muted-foreground cursor-pointer">
              已映射的模型（{{ diff.existing.length }}）
            </summary>
            <p class="mt-1 font-mono break-all">
              {{ diff.existing.join('、') }}
            </p>
          </details>
          <div v-if="diff.missing.length" class="rounded-md border border-amber-500/40 bg-amber-500/5 p-2.5 text-xs">
            <p class="font-medium text-amber-700 dark:text-amber-400">
              以下已映射的上游模型不在上游列表中：
            </p>
            <p class="mt-1 font-mono break-all">
              {{ diff.missing.join('、') }}
            </p>
            <p class="text-muted-foreground mt-1">
              可能已下线或上游列表不完整；这里不会自动删除，如需移除请在映射中手动删除。
            </p>
          </div>
        </template>
      </div>

      <DialogFooter>
        <Button variant="outline" @click="open = false">
          取消
        </Button>
        <Button :disabled="picked.size === 0" @click="confirm">
          添加 {{ picked.size || '' }} 个模型
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
