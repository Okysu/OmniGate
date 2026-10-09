<script setup lang="ts">
import type { PluginTemplate } from '@/lib/types'
import { reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Loader2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { pluginsApi } from '@/lib/endpoints'
import { PLUGIN_TEMPLATE_LABELS } from '@/lib/labels'
import { cn } from '@/lib/utils'

const open = defineModel<boolean>('open', { required: true })
const router = useRouter()

const TEMPLATES: PluginTemplate[] = ['openai-compatible', 'anthropic-compatible', 'custom-protocol', 'blank']
/** Same rule as the server (manifest.go pluginIDRe). */
const ID_RE = /^[a-z0-9]+(?:\.[a-z0-9-]+)+$/

const form = reactive({ id: '', name: '', template: 'openai-compatible' as PluginTemplate })
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
const saving = ref(false)

watch(open, (v) => {
  if (v) {
    Object.assign(form, { id: '', name: '', template: 'openai-compatible' })
    errors.value = {}
    formError.value = null
  }
})

function validate(): Record<string, string> {
  const e: Record<string, string> = {}
  const id = form.id.trim()
  if (!ID_RE.test(id) || id.length > 64)
    e.id = '必须形如 vendor.name：小写字母、数字、点与连字符，至少两段，最长 64 个字符'
  else if (id.startsWith('builtin.'))
    e.id = 'builtin. 前缀为内置插件保留'
  const name = form.name.trim()
  if (!name || name.length > 64)
    e.name = '长度应为 1–64 个字符'
  return e
}

async function submit() {
  errors.value = validate()
  formError.value = null
  if (Object.keys(errors.value).length)
    return
  saving.value = true
  try {
    const p = await pluginsApi.create({ id: form.id.trim(), name: form.name.trim(), template: form.template })
    toast.success(`已创建插件「${p.name}」`, { description: '草稿已就绪，可以开始编辑。' })
    open.value = false
    await router.push({ name: 'plugin-editor', params: { id: p.id } })
  }
  catch (err) {
    if (isApiError(err) && err.code === 'plugin_exists')
      errors.value = { id: '该插件 ID 已存在' }
    else if (isApiError(err) && err.status === 422)
      errors.value = fieldErrors(err)
    else
      formError.value = errorMessage(err)
  }
  finally {
    saving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[90svh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>新建插件</DialogTitle>
        <DialogDescription>
          从模板创建一个可编辑的草稿，在在线编辑器中编写、测试并发布。
        </DialogDescription>
      </DialogHeader>
      <form id="create-plugin-form" class="space-y-4" novalidate @submit.prevent="submit">
        <FormField label="插件 ID" for="pl-id" required :error="errors.id" hint="反向域名风格，发布后不可更改，例如 acme.my-gateway。">
          <Input id="pl-id" v-model="form.id" placeholder="vendor.name" class="font-mono text-sm" autocomplete="off" spellcheck="false" :aria-invalid="!!errors.id" />
        </FormField>
        <FormField label="名称" for="pl-name" required :error="errors.name">
          <Input id="pl-name" v-model="form.name" maxlength="64" placeholder="例如：Acme 网关" :aria-invalid="!!errors.name" />
        </FormField>
        <div class="space-y-1.5">
          <p class="text-sm font-medium">
            模板
          </p>
          <div role="radiogroup" aria-label="模板" class="grid gap-2">
            <button
              v-for="t in TEMPLATES"
              :key="t"
              type="button"
              role="radio"
              :aria-checked="form.template === t"
              :class="cn('rounded-lg border p-3 text-left transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50', form.template === t ? 'border-primary bg-primary/5' : 'hover:bg-muted/60')"
              @click="form.template = t"
            >
              <span class="block text-sm font-medium">{{ PLUGIN_TEMPLATE_LABELS[t].label }}</span>
              <span class="text-muted-foreground block text-xs">{{ PLUGIN_TEMPLATE_LABELS[t].description }}</span>
            </button>
          </div>
          <p v-if="errors.template" class="text-destructive text-xs" role="alert">
            {{ errors.template }}
          </p>
        </div>
      </form>
      <DialogFooter class="sm:items-center">
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <Button variant="outline" :disabled="saving" @click="open = false">
          取消
        </Button>
        <Button type="submit" form="create-plugin-form" :disabled="saving">
          <Loader2 v-if="saving" class="animate-spin" />
          创建并打开编辑器
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
