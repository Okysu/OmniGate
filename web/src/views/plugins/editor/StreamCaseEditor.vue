<script setup lang="ts">
// Streaming test case builder (phase9 §2): upstream byte chunks fed to
// parseStream one call each, optional ctx.config and expected events.
import type { ChunkKind, StreamCaseForm } from '@/lib/pluginStream'
import { ArrowDown, ArrowUp, Plus, Trash2 } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { STREAM_CALL_LIMIT_MS, STREAM_TOTAL_LIMIT_MS } from '@/lib/pluginProtocol'
import { chunkRow, MAX_STREAM_CHUNKS } from '@/lib/pluginStream'

const form = defineModel<StreamCaseForm>('form', { required: true })
defineProps<{ errors: Record<string, string> }>()

function setKind(i: number, v: unknown) {
  const row = form.value.chunks[i]
  if (row && (v === 'text' || v === 'json'))
    row.kind = v as ChunkKind
}
function move(i: number, d: -1 | 1) {
  const list = form.value.chunks
  const j = i + d
  if (j < 0 || j >= list.length)
    return
  const [row] = list.splice(i, 1)
  if (row)
    list.splice(j, 0, row)
}
function add() {
  const last = form.value.chunks.at(-1)
  form.value.chunks.push(chunkRow(last?.kind ?? 'json'))
}
</script>

<template>
  <div class="space-y-3" data-testid="stream-case-editor">
    <div class="space-y-1">
      <Label for="stream-name" class="text-xs">用例名称</Label>
      <Input id="stream-name" v-model="form.name" class="h-8 text-xs" maxlength="64" />
    </div>

    <div class="space-y-2">
      <div class="flex items-center justify-between gap-2">
        <p class="text-xs font-medium">
          上游字节块（{{ form.chunks.length }}）
        </p>
        <label class="text-muted-foreground flex items-center gap-1.5 text-xs">
          <Checkbox v-model="form.jsonNewline" aria-label="JSON 块末尾追加换行" />
          JSON 块追加 \n
        </label>
      </div>
      <p class="text-muted-foreground text-xs">
        每个块调用一次 parseStream（单次限时 {{ STREAM_CALL_LIMIT_MS }} ms，整个请求 JS 总耗时上限 {{ STREAM_TOTAL_LIMIT_MS / 1000 }} s），最后调用 endStream（如有）。文本块按原样以 UTF-8 发送，可以把一行 JSON 拆在两个块之间测试缓冲。
      </p>
      <ol class="space-y-2">
        <li v-for="(row, i) in form.chunks" :key="row.uid" class="bg-muted/30 space-y-1.5 rounded-md border p-2" :data-chunk-index="i">
          <div class="flex items-center gap-1">
            <span class="text-muted-foreground w-6 shrink-0 text-xs tabular-nums">#{{ i + 1 }}</span>
            <Select :model-value="row.kind" @update:model-value="(v) => setKind(i, v)">
              <SelectTrigger size="sm" class="w-20 text-xs" :aria-label="`第 ${i + 1} 块类型`">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="json">
                  JSON
                </SelectItem>
                <SelectItem value="text">
                  文本
                </SelectItem>
              </SelectContent>
            </Select>
            <span class="flex-1" />
            <Button type="button" variant="ghost" size="icon-sm" :disabled="i === 0" :aria-label="`上移第 ${i + 1} 块`" @click="move(i, -1)">
              <ArrowUp />
            </Button>
            <Button type="button" variant="ghost" size="icon-sm" :disabled="i === form.chunks.length - 1" :aria-label="`下移第 ${i + 1} 块`" @click="move(i, 1)">
              <ArrowDown />
            </Button>
            <Button type="button" variant="ghost" size="icon-sm" :disabled="form.chunks.length <= 1" :aria-label="`删除第 ${i + 1} 块`" @click="form.chunks.splice(i, 1)">
              <Trash2 />
            </Button>
          </div>
          <Textarea
            v-model="row.text"
            rows="2"
            class="min-h-12 resize-y font-mono text-xs"
            spellcheck="false"
            :placeholder="row.kind === 'json' ? '{&quot;event&quot;:&quot;text&quot;,&quot;text&quot;:&quot;你好&quot;}' : '原始文本（可包含换行）'"
            :aria-label="`第 ${i + 1} 块内容`"
            :aria-invalid="!!errors[`chunks.${i}`]"
          />
          <p v-if="errors[`chunks.${i}`]" class="text-destructive text-xs" role="alert">
            {{ errors[`chunks.${i}`] }}
          </p>
        </li>
      </ol>
      <p v-if="errors.chunks" class="text-destructive text-xs" role="alert">
        {{ errors.chunks }}
      </p>
      <Button type="button" variant="outline" size="sm" class="w-full" :disabled="form.chunks.length >= MAX_STREAM_CHUNKS" @click="add">
        <Plus />
        添加块
      </Button>
    </div>

    <div class="space-y-1">
      <Label for="stream-config" class="text-xs">插件配置 ctx.config（JSON 对象，可选）</Label>
      <Textarea id="stream-config" v-model="form.config" rows="2" class="min-h-12 font-mono text-xs" spellcheck="false" placeholder="{&quot;region&quot;: &quot;cn&quot;}" :aria-invalid="!!errors.config" />
      <p v-if="errors.config" class="text-destructive text-xs" role="alert">
        {{ errors.config }}
      </p>
    </div>
    <div class="space-y-1">
      <Label for="stream-expect" class="text-xs">期望事件（JSON 数组，可选，子集匹配）</Label>
      <Textarea id="stream-expect" v-model="form.expectEvents" rows="2" class="min-h-12 font-mono text-xs" spellcheck="false" placeholder="[{&quot;type&quot;: &quot;delta&quot;, &quot;content&quot;: &quot;你好&quot;}, {&quot;type&quot;: &quot;finish&quot;, &quot;reason&quot;: &quot;stop&quot;}]" :aria-invalid="!!errors.expectEvents" />
      <p v-if="errors.expectEvents" class="text-destructive text-xs" role="alert">
        {{ errors.expectEvents }}
      </p>
    </div>
  </div>
</template>
