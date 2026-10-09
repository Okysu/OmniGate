<script setup lang="ts">
import { computed } from 'vue'
import { FileCode2, FileJson, FilePlus, FileText, Folder, Pencil, Trash2 } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { buildTree } from './files'

const props = defineProps<{
  files: string[]
  active: string | null
  dirty: ReadonlySet<string>
  /** Error count per file (from the last build). */
  problems: Record<string, number>
  readonly?: boolean
}>()
defineEmits<{
  open: [path: string]
  create: []
  rename: [path: string]
  remove: [path: string]
}>()

const entries = computed(() => buildTree(props.files))

function icon(path: string) {
  if (path.endsWith('.json'))
    return FileJson
  if (/\.(?:ts|js)$/.test(path))
    return FileCode2
  return FileText
}
</script>

<template>
  <div class="flex h-full flex-col">
    <div class="flex h-9 shrink-0 items-center justify-between border-b px-2">
      <span class="text-muted-foreground text-xs font-medium tracking-wide">文件</span>
      <Button v-if="!readonly" variant="ghost" size="icon-xs" aria-label="新建文件" title="新建文件" @click="$emit('create')">
        <FilePlus />
      </Button>
    </div>
    <ul class="flex-1 overflow-y-auto py-1 text-sm" role="tree" aria-label="插件文件">
      <li v-for="e in entries" :key="`${e.kind}:${e.path}`" role="treeitem" :aria-selected="e.kind === 'file' && e.path === active">
        <div
          v-if="e.kind === 'folder'"
          class="text-muted-foreground flex h-7 items-center gap-1.5 pr-2 text-xs"
          :style="{ paddingLeft: `${8 + e.depth * 12}px` }"
        >
          <Folder class="size-3.5 shrink-0" />
          <span class="truncate">{{ e.name }}</span>
        </div>
        <div
          v-else
          class="group flex h-7 items-center gap-1 pr-1"
          :class="e.path === active ? 'bg-muted' : 'hover:bg-muted/50'"
          :style="{ paddingLeft: `${8 + e.depth * 12}px` }"
        >
          <button type="button" class="flex min-w-0 flex-1 items-center gap-1.5 text-left outline-none focus-visible:underline" :title="e.path" @click="$emit('open', e.path)">
            <component :is="icon(e.path)" class="text-muted-foreground size-3.5 shrink-0" />
            <span class="truncate font-mono text-xs" :class="problems[e.path] ? 'text-destructive' : ''">{{ e.name }}</span>
            <span v-if="dirty.has(e.path)" class="bg-foreground/70 size-1.5 shrink-0 rounded-full" title="未保存" aria-label="未保存" />
            <span v-if="problems[e.path]" class="text-destructive ml-auto text-[10px] tabular-nums">{{ problems[e.path] }}</span>
          </button>
          <template v-if="!readonly">
            <Button variant="ghost" size="icon-xs" class="opacity-0 group-hover:opacity-100 focus-visible:opacity-100" :aria-label="`重命名 ${e.path}`" title="重命名" @click="$emit('rename', e.path)">
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              class="opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
              :aria-label="`删除 ${e.path}`"
              title="删除"
              :disabled="e.path === 'manifest.json'"
              @click="$emit('remove', e.path)"
            >
              <Trash2 />
            </Button>
          </template>
        </div>
      </li>
    </ul>
  </div>
</template>
