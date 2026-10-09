<script setup lang="ts">
import type { NotificationPreferences, WebhookFormat, WebhookTestResult } from '@/lib/types'
import { computed, ref } from 'vue'
import { CircleCheck, CircleX, KeyRound, Loader2, Send, Webhook } from '@lucide/vue'
import { toast } from 'vue-sonner'
import FormField from '@/components/FormField.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { errorMessage, fieldErrors } from '@/lib/api'
import { notificationsApi } from '@/lib/endpoints'
import { WEBHOOK_FORMAT_HINTS, WEBHOOK_FORMAT_LABELS, WEBHOOK_FORMATS } from '@/lib/notifications'

const props = defineProps<{
  saved: NotificationPreferences
  /** Webhook fields have unsaved edits (the test uses the saved configuration). */
  dirty: boolean
  errors: Record<string, string>
}>()
const enabled = defineModel<boolean>('enabled', { required: true })
const url = defineModel<string>('url', { required: true })
const format = defineModel<WebhookFormat>('format', { required: true })
const emit = defineEmits<{ secretChanged: [] }>()

function setFormat(v: unknown) {
  if (typeof v === 'string' && (WEBHOOK_FORMATS as string[]).includes(v))
    format.value = v as WebhookFormat
}

// ---------- secret (write-only, own endpoint) ----------
const secretSet = computed(() => !!props.saved.webhook?.secretSet)
const editingSecret = ref(false)
const secret = ref('')
const savingSecret = ref(false)
const secretError = ref<string | null>(null)

async function saveSecret(value: string) {
  secretError.value = null
  savingSecret.value = true
  try {
    await notificationsApi.setWebhookSecret(value)
    editingSecret.value = false
    secret.value = ''
    toast.success(value ? '签名密钥已保存' : '签名密钥已清除')
    emit('secretChanged')
  }
  catch (err) {
    secretError.value = fieldErrors(err).secret ?? errorMessage(err)
  }
  finally {
    savingSecret.value = false
  }
}

// ---------- test ----------
const testing = ref(false)
const testResult = ref<WebhookTestResult | null>(null)
const testError = ref<string | null>(null)
const savedUrl = computed(() => props.saved.webhook?.url ?? '')
const canTest = computed(() => !!savedUrl.value && !props.dirty)

async function test() {
  testing.value = true
  testResult.value = null
  testError.value = null
  try {
    const res = await notificationsApi.testWebhook()
    // A bare 2xx without a body counts as delivered.
    testResult.value = res && typeof res.ok === 'boolean' ? res : { ok: true }
  }
  catch (err) {
    testError.value = errorMessage(err)
  }
  finally {
    testing.value = false
  }
}
</script>

<template>
  <Card data-section="webhook">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base">
        <Webhook class="size-4" />
        Webhook
        <Switch v-model="enabled" class="ml-auto" aria-label="启用 Webhook 通知" data-testid="webhook-enabled" />
      </CardTitle>
      <CardDescription>把通知推送到你的服务或群机器人；失败按 1、5、30 分钟重试 3 次，超时 10 秒。</CardDescription>
    </CardHeader>
    <CardContent class="space-y-4">
      <div class="grid gap-4 sm:grid-cols-[minmax(0,1fr)_9rem]">
        <FormField label="地址" for="ntf-wh-url" :error="errors['webhook.url']">
          <Input id="ntf-wh-url" v-model="url" type="url" placeholder="https://example.com/hooks/omnigate" class="font-mono text-xs" :aria-invalid="!!errors['webhook.url']" data-testid="webhook-url" />
        </FormField>
        <FormField label="格式" for="ntf-wh-format" :error="errors['webhook.format']">
          <Select :model-value="format" @update:model-value="setFormat">
            <SelectTrigger id="ntf-wh-format" class="w-full" data-testid="webhook-format">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="f in WEBHOOK_FORMATS" :key="f" :value="f">
                {{ WEBHOOK_FORMAT_LABELS[f] }}
              </SelectItem>
            </SelectContent>
          </Select>
        </FormField>
      </div>
      <p class="text-muted-foreground -mt-2 text-xs">
        {{ WEBHOOK_FORMAT_HINTS[format] }}不能指向内网地址。
      </p>

      <div class="space-y-1.5">
        <Label as="span">签名密钥</Label>
        <div v-if="!editingSecret" class="flex flex-wrap items-center gap-2 rounded-lg border p-3" data-testid="webhook-secret">
          <KeyRound class="text-muted-foreground size-4 shrink-0" />
          <span class="flex-1 text-sm">
            <Badge v-if="secretSet" variant="outline" class="border-emerald-500/40 text-emerald-700 dark:text-emerald-400">已设置</Badge>
            <span v-else class="text-muted-foreground">未设置</span>
          </span>
          <Button variant="outline" size="xs" @click="editingSecret = true">
            {{ secretSet ? '更换' : '设置' }}
          </Button>
          <Button v-if="secretSet" variant="ghost" size="xs" class="text-destructive" :disabled="savingSecret" @click="saveSecret('')">
            清除
          </Button>
        </div>
        <div v-else class="flex flex-col gap-2 sm:flex-row">
          <Input v-model="secret" type="password" autocomplete="new-password" placeholder="新的 HMAC 密钥" class="font-mono text-xs sm:flex-1" aria-label="新的签名密钥" data-testid="webhook-secret-input" />
          <div class="flex gap-2">
            <Button :disabled="savingSecret || !secret" class="flex-1 sm:flex-none" data-testid="webhook-secret-save" @click="saveSecret(secret)">
              <Loader2 v-if="savingSecret" class="animate-spin" />
              保存密钥
            </Button>
            <Button variant="ghost" :disabled="savingSecret" @click="() => { editingSecret = false; secret = ''; secretError = null }">
              取消
            </Button>
          </div>
        </div>
        <p v-if="secretError" class="text-destructive text-xs" role="alert">
          {{ secretError }}
        </p>
        <p v-else class="text-muted-foreground text-xs">
          只写：加密存储，保存后不再显示；立即生效，不需要点击页面底部的保存。用于通用 JSON 的 X-OmniGate-Signature 签名。
        </p>
      </div>

      <div class="space-y-2 border-t pt-4">
        <div class="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" :disabled="testing || !canTest" data-testid="webhook-test" @click="test">
            <Loader2 v-if="testing" class="animate-spin" />
            <Send v-else />
            发送测试
          </Button>
          <span v-if="!savedUrl" class="text-muted-foreground text-xs">保存 Webhook 地址后可以发送测试消息。</span>
          <span v-else-if="dirty" class="text-muted-foreground text-xs">测试使用已保存的配置，请先保存修改。</span>
        </div>
        <div v-if="testResult" class="flex items-start gap-2 rounded-lg border p-2.5 text-xs" :class="testResult.ok ? 'border-emerald-500/40 bg-emerald-500/5' : 'border-destructive/40 bg-destructive/5'" role="status" data-testid="webhook-test-result">
          <CircleCheck v-if="testResult.ok" class="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
          <CircleX v-else class="text-destructive mt-0.5 size-4 shrink-0" />
          <p class="min-w-0 break-words">
            <template v-if="testResult.ok">
              测试消息已送达<template v-if="testResult.statusCode">
                （HTTP {{ testResult.statusCode }}<template v-if="testResult.latencyMs != null">
                  ，{{ testResult.latencyMs }} ms
                </template>）
              </template>。
            </template>
            <template v-else>
              发送失败<template v-if="testResult.statusCode">
                （HTTP {{ testResult.statusCode }}）
              </template>：{{ testResult.error || '未知错误' }}
            </template>
          </p>
        </div>
        <p v-if="testError" class="text-destructive text-xs" role="alert" data-testid="webhook-test-error">
          发送失败：{{ testError }}
        </p>
      </div>
    </CardContent>
  </Card>
</template>
