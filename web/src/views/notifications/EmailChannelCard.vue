<script setup lang="ts">
import type { NotificationPreferences } from '@/lib/types'
import { computed, onBeforeUnmount, ref } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { BadgeCheck, CircleAlert, Loader2, Mail, MailWarning, Send, X } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { errorMessage, fieldErrors, isApiError } from '@/lib/api'
import { notificationsApi } from '@/lib/endpoints'
import { cooldownLeft, isSmtpUnavailableError, isValidEmail, normalizeCode } from '@/lib/notifications'

/** Client-side resend cooldown (the server allows at most 5 codes per hour). */
const RESEND_COOLDOWN_MS = 60_000

const props = defineProps<{
  saved: NotificationPreferences
  accountEmail: string | null
  /** SMTP is not configured (flag from the server, or inferred from a verify error). */
  smtpUnavailable: boolean
  error?: string | null
}>()
const enabled = defineModel<boolean>('enabled', { required: true })
const address = defineModel<string | null>('address', { required: true })
const emit = defineEmits<{
  /** A custom address was verified server-side; the parent reloads the preferences. */
  confirmed: [address: string]
  smtpUnavailable: []
}>()

/** Address shown as the current target (form state, may be unsaved). */
const target = computed(() => address.value ?? props.accountEmail)
const usingAccount = computed(() => address.value === null)
/** The `verified` flag only describes the saved address. */
const verifiedKnown = computed(() => (address.value ?? null) === (props.saved.email?.address ?? null))
const verified = computed(() => verifiedKnown.value && !!props.saved.email?.verified)

// ---------- custom address flow ----------
const editing = ref(false)
const newAddress = ref('')
const code = ref('')
const sentTo = ref<string | null>(null)
const sending = ref(false)
const confirming = ref(false)
const flowError = ref<string | null>(null)
const cooldownUntil = ref<number | null>(null)
const now = ref(Date.now())
const { pause, resume } = useIntervalFn(() => {
  now.value = Date.now()
  if (cooldownLeft(cooldownUntil.value, now.value) === 0)
    pause()
}, 1000, { immediate: false })
onBeforeUnmount(pause)
const cooldown = computed(() => cooldownLeft(cooldownUntil.value, now.value))

const addressValid = computed(() => isValidEmail(newAddress.value))
const addressChangedSinceSend = computed(() => sentTo.value !== null && newAddress.value.trim() !== sentTo.value)

function startEdit() {
  editing.value = true
  newAddress.value = ''
  code.value = ''
  sentTo.value = null
  flowError.value = null
}
function cancelEdit() {
  editing.value = false
  flowError.value = null
}

async function sendCode() {
  flowError.value = null
  const a = newAddress.value.trim()
  if (!isValidEmail(a)) {
    flowError.value = '请输入有效的邮箱地址'
    return
  }
  sending.value = true
  try {
    await notificationsApi.verifyEmail(a)
    sentTo.value = a
    code.value = ''
    cooldownUntil.value = Date.now() + RESEND_COOLDOWN_MS
    now.value = Date.now()
    resume()
    toast.success('验证码已发送', { description: `请查收 ${a} 的邮件，验证码 10 分钟内有效。` })
  }
  catch (err) {
    if (isSmtpUnavailableError(err)) {
      emit('smtpUnavailable')
      flowError.value = '邮件服务（SMTP）尚未配置，暂时无法发送验证码。'
    }
    else if (isApiError(err) && err.status === 429) {
      flowError.value = `${err.message || '发送过于频繁'}（每小时最多发送 5 次验证码）`
    }
    else {
      flowError.value = fieldErrors(err).address ?? errorMessage(err)
    }
  }
  finally {
    sending.value = false
  }
}

async function confirm() {
  flowError.value = null
  if (!sentTo.value)
    return
  if (code.value.length !== 6) {
    flowError.value = '请输入 6 位验证码'
    return
  }
  confirming.value = true
  try {
    await notificationsApi.confirmEmail(sentTo.value, code.value)
    const a = sentTo.value
    editing.value = false
    sentTo.value = null
    code.value = ''
    toast.success('通知邮箱已验证', { description: a })
    emit('confirmed', a)
  }
  catch (err) {
    flowError.value = fieldErrors(err).code ?? errorMessage(err)
  }
  finally {
    confirming.value = false
  }
}

function useAccountEmail() {
  address.value = null
}
</script>

<template>
  <Card data-section="email">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base">
        <Mail class="size-4" />
        邮件
        <Switch v-model="enabled" class="ml-auto" :disabled="smtpUnavailable && !enabled" aria-label="启用邮件通知" data-testid="email-enabled" />
      </CardTitle>
      <CardDescription>每封邮件都带一键退订链接（关闭该事件的邮件通知）与设置页链接。</CardDescription>
    </CardHeader>
    <CardContent class="space-y-4">
      <div v-if="smtpUnavailable" class="flex gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-xs" role="status" data-testid="smtp-unavailable">
        <MailWarning class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
        <div class="space-y-0.5">
          <p class="font-medium text-amber-800 dark:text-amber-300">
            邮件服务尚未配置
          </p>
          <p class="text-muted-foreground">
            系统管理员尚未配置 SMTP，暂时无法发送邮件或验证邮箱。站内通知与 Webhook 不受影响。
          </p>
        </div>
      </div>

      <div class="space-y-1.5">
        <Label as="span">通知邮箱</Label>
        <div class="flex flex-wrap items-center gap-2 rounded-lg border p-3" data-testid="email-target">
          <div class="min-w-0 flex-1">
            <p class="truncate font-mono text-sm" :title="target ?? undefined">
              {{ target ?? '账户未设置邮箱' }}
            </p>
            <p class="text-muted-foreground text-xs">
              {{ usingAccount ? '账户邮箱（来自登录方式）' : '自定义邮箱' }}<template v-if="!verifiedKnown">
                · 保存后生效
              </template>
            </p>
          </div>
          <Badge v-if="target && verifiedKnown && verified" variant="outline" class="border-emerald-500/40 text-emerald-700 dark:text-emerald-400" data-testid="email-verified">
            <BadgeCheck />已验证
          </Badge>
          <Badge v-else-if="target && verifiedKnown" variant="outline" class="border-amber-500/40 text-amber-700 dark:text-amber-400">
            <CircleAlert />未验证
          </Badge>
        </div>
        <p v-if="error" class="text-destructive text-xs" role="alert">
          {{ error }}
        </p>
        <p v-else-if="usingAccount && target && verifiedKnown && !verified" class="text-muted-foreground text-xs">
          登录方式未确认该邮箱已验证，邮件可能不会发送；可以设置一个自定义邮箱并完成验证。
        </p>
        <div v-if="!editing" class="flex flex-wrap gap-2 pt-1">
          <Button variant="outline" size="sm" :disabled="smtpUnavailable" data-testid="email-custom" @click="startEdit">
            {{ usingAccount ? '使用其他邮箱' : '更换邮箱' }}
          </Button>
          <Button v-if="!usingAccount && accountEmail" variant="ghost" size="sm" @click="useAccountEmail">
            改用账户邮箱
          </Button>
        </div>
      </div>

      <div v-if="editing" class="bg-muted/30 space-y-3 rounded-lg border p-3" data-testid="email-verify-flow">
        <div class="flex items-center justify-between gap-2">
          <p class="text-sm font-medium">
            验证新的通知邮箱
          </p>
          <Button variant="ghost" size="icon-xs" aria-label="取消" @click="cancelEdit">
            <X />
          </Button>
        </div>
        <div class="space-y-1.5">
          <Label for="ntf-new-email">邮箱地址</Label>
          <div class="flex flex-col gap-2 sm:flex-row">
            <Input id="ntf-new-email" v-model="newAddress" type="email" autocomplete="email" placeholder="name@example.com" class="sm:flex-1" data-testid="email-new-address" @keydown.enter.prevent="sendCode" />
            <Button
              variant="outline"
              class="sm:w-36"
              :disabled="sending || !addressValid || (cooldown > 0 && !addressChangedSinceSend)"
              data-testid="email-send-code"
              @click="sendCode"
            >
              <Loader2 v-if="sending" class="animate-spin" />
              <Send v-else />
              <template v-if="cooldown > 0 && !addressChangedSinceSend">
                重新发送（{{ cooldown }}s）
              </template>
              <template v-else>
                {{ sentTo ? '重新发送' : '发送验证码' }}
              </template>
            </Button>
          </div>
        </div>
        <div v-if="sentTo" class="space-y-1.5">
          <Label for="ntf-code">验证码</Label>
          <div class="flex flex-col gap-2 sm:flex-row">
            <Input
              id="ntf-code"
              :model-value="code"
              inputmode="numeric"
              autocomplete="one-time-code"
              placeholder="6 位数字"
              class="font-mono tracking-[0.3em] sm:flex-1"
              data-testid="email-code"
              @update:model-value="(v) => (code = normalizeCode(String(v)))"
              @keydown.enter.prevent="confirm"
            />
            <Button class="sm:w-36" :disabled="confirming || code.length !== 6" data-testid="email-confirm" @click="confirm">
              <Loader2 v-if="confirming" class="animate-spin" />
              确认
            </Button>
          </div>
          <p class="text-muted-foreground text-xs">
            已发送到 <span class="font-mono">{{ sentTo }}</span>，10 分钟内有效；每小时最多发送 5 次。
          </p>
        </div>
        <p v-if="flowError" class="text-destructive text-xs" role="alert" data-testid="email-flow-error">
          {{ flowError }}
        </p>
      </div>
    </CardContent>
  </Card>
</template>
