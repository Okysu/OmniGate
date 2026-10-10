<script setup lang="ts">
import { ref } from 'vue'
import CopyButton from '@/components/CopyButton.vue'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { usageSnippets } from '@/lib/snippets'

const all = usageSnippets(window.location.origin)
const baseUrl = all.baseUrl

const tab = ref('curl')
const snippets = {
  curl: all.curl,
  openai: all.openai,
  anthropic: all.anthropic,
  completionsCurl: all.completionsCurl,
  completionsPython: all.completionsPython,
  imagesCurl: all.imagesCurl,
  imagesPython: all.imagesPython,
  imageEditCurl: all.imageEditCurl,
  transcriptionCurl: all.transcriptionCurl,
  transcriptionPython: all.transcriptionPython,
  speechCurl: all.speechCurl,
  speechPython: all.speechPython,
}
const TABS: { key: keyof typeof snippets, label: string }[] = [
  { key: 'curl', label: 'curl' },
  { key: 'openai', label: 'OpenAI SDK' },
  { key: 'anthropic', label: 'Anthropic SDK' },
  { key: 'completionsCurl', label: 'FIM 补全 · curl' },
  { key: 'completionsPython', label: 'FIM 补全 · Python' },
  { key: 'imagesCurl', label: '图片 · curl' },
  { key: 'imagesPython', label: '图片 · Python' },
  { key: 'imageEditCurl', label: '图片编辑 · curl' },
  { key: 'transcriptionCurl', label: '语音转写 · curl' },
  { key: 'transcriptionPython', label: '语音转写 · Python' },
  { key: 'speechCurl', label: '语音合成 · curl' },
  { key: 'speechPython', label: '语音合成 · Python' },
]
</script>

<template>
  <Card>
    <CardHeader>
      <CardTitle class="text-base">
        使用示例
      </CardTitle>
      <CardDescription>
        网关同时提供 OpenAI 与 Anthropic 兼容接口（含 OpenAI Images 图片生成 / 编辑接口、Audio 语音转写 / 语音合成接口与 Completions 文本补全 / FIM 接口），模型名使用「模型」页面中的逻辑模型名。示例中的密钥是占位符，请替换为你自己的 Key。
      </CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <div class="flex items-center gap-2 rounded-lg border p-2 pl-3 text-sm">
        <span class="text-muted-foreground shrink-0 text-xs">Base URL</span>
        <code class="min-w-0 flex-1 truncate font-mono text-xs">{{ baseUrl }}</code>
        <CopyButton :value="baseUrl" label="复制 Base URL" />
      </div>
      <Tabs v-model="tab">
        <div class="-mx-1 overflow-x-auto px-1 pb-1">
          <TabsList data-testid="usage-tabs">
            <TabsTrigger v-for="t in TABS" :key="t.key" :value="t.key" class="flex-none">
              {{ t.label }}
            </TabsTrigger>
          </TabsList>
        </div>
        <TabsContent v-for="(code, key) in snippets" :key="key" :value="key">
          <div class="bg-muted/50 relative rounded-lg border">
            <CopyButton :value="code" class="absolute top-2 right-2" label="复制代码" />
            <pre class="overflow-x-auto p-3 pr-10 font-mono text-xs leading-relaxed"><code>{{ code }}</code></pre>
          </div>
        </TabsContent>
      </Tabs>
    </CardContent>
  </Card>
</template>
