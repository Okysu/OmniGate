<script setup lang="ts">
import type { SmtpTestResult } from '@/lib/types'
import { ref, watch } from 'vue'
import { CircleCheck, CircleX, Loader2, Send } from '@lucide/vue'
import FormField from '@/components/FormField.vue'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { errorMessage, fieldErrors } from '@/lib/api'
import { settingsApi } from '@/lib/endpoints'
import { isValidEmail } from '@/lib/notifications'

/** "发送测试邮件": asks for a recipient and shows the server's {ok, error}. */
const props = defineProps<{
  defaultTo: string | null
  /** The SMTP card has unsaved edits (the test uses the saved settings). */
  dirty: boolean
}>()
const open = defineModel<boolean>('open', { required: true })

const to = ref('')
const sending = ref(false)
const inputError = ref<string | null>(null)
const result = ref<SmtpTestResult | null>(null)

watch(open, (v) => {
  if (v) {
    to.value = props.defaultTo ?? ''
    inputError.value = null
    result.value = null
  }
})

async function send() {
  inputError.value = null
  result.value = null
  const addr = to.value.trim()
  if (!isValidEmail(addr)) {
    inputError.value = '请输入有效的邮箱地址'
    return
  }
  sending.value = true
  try {
    const res = await settingsApi.smtpTest(addr)
    result.value = res && typeof res.ok === 'boolean' ? res : { ok: true }
  }
  catch (err) {
    const fe = fieldErrors(err).to
    if (fe)
      inputError.value = fe
    else
      result.value = { ok: false, error: errorMessage(err) }
  }
  finally {
    sending.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>发送测试邮件</DialogTitle>
        <DialogDescription>
          使用<strong class="text-foreground">已保存</strong>的 SMTP 设置立即发送一封测试邮件。<template v-if="dirty">
            本页还有未保存的修改，它们不会用于这次测试。
          </template>
        </DialogDescription>
      </DialogHeader>
      <form id="smtp-test-form" class="space-y-3" novalidate @submit.prevent="send">
        <FormField label="收件人" for="smtp-test-to" :error="inputError">
          <Input id="smtp-test-to" v-model="to" type="email" autocomplete="email" placeholder="you@example.com" :aria-invalid="!!inputError" data-testid="smtp-test-to" />
        </FormField>
        <div
          v-if="result"
          class="flex items-start gap-2 rounded-lg border p-2.5 text-xs"
          :class="result.ok ? 'border-emerald-500/40 bg-emerald-500/5' : 'border-destructive/40 bg-destructive/5'"
          role="status"
          data-testid="smtp-test-result"
        >
          <CircleCheck v-if="result.ok" class="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
          <CircleX v-else class="text-destructive mt-0.5 size-4 shrink-0" />
          <p class="min-w-0 break-words">
            <template v-if="result.ok">
              测试邮件已发出，请检查收件箱（可能在垃圾邮件中）。
            </template>
            <template v-else>
              发送失败：{{ result.error || '未知错误' }}
            </template>
          </p>
        </div>
      </form>
      <DialogFooter>
        <Button variant="outline" @click="open = false">
          关闭
        </Button>
        <Button type="submit" form="smtp-test-form" :disabled="sending" data-testid="smtp-test-send">
          <Loader2 v-if="sending" class="animate-spin" />
          <Send v-else />
          发送
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
