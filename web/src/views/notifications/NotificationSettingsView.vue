<script setup lang="ts">
import type { EligibilityContext, PreferencesForm } from '@/lib/notifications'
import type { DigestMode, NotificationPreferences } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useEventListener } from '@vueuse/core'
import { CircleAlert, Clock, Loader2, RefreshCw, RotateCcw, Save, Undo2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ErrorState from '@/components/ErrorState.vue'
import FormField from '@/components/FormField.vue'
import PageHeader from '@/components/PageHeader.vue'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { fetchAllChannels, notificationsApi } from '@/lib/endpoints'
import { isValidAmount } from '@/lib/money'
import {
  browserTimezone,
  buildPreferencesBody,
  defaultEvents,
  DIGEST_LABELS,
  dirtyParts,
  eligibleEvents,
  formFromPreferences,
  isCategoryEligible,
  normalizePrefsErrorKeys,
  timezoneOptions,
  utcOffsetLabel,
  validatePreferences,
} from '@/lib/notifications'
import { isAbortError } from '@/lib/query'
import { isFullChannel } from '@/lib/types'
import { useAuthStore } from '@/stores/auth'
import EmailChannelCard from './EmailChannelCard.vue'
import EventMatrix from './EventMatrix.vue'
import NotificationTabs from './NotificationTabs.vue'
import WebhookChannelCard from './WebhookChannelCard.vue'

const auth = useAuthStore()
const { currency, money } = useCurrency()

const saved = ref<NotificationPreferences | null>(null)
const form = ref<PreferencesForm | null>(null)
const loadError = ref<unknown>(null)
const loading = ref(false)
const saving = ref(false)
const conflict = ref(false)
const errors = ref<Record<string, string>>({})
const formError = ref<string | null>(null)
/** Inferred from a verify error when the server does not send `smtpConfigured`. */
const smtpInferredUnavailable = ref(false)
const managesChannels = ref(false)
let controller: AbortController | null = null

const ctx = computed<EligibilityContext>(() => ({ permissions: auth.permissions, managesChannels: managesChannels.value || auth.can('channels.manage') }))
const accountEmail = computed(() => auth.user?.email ?? null)

/** channel.* / upstream.* go to channel owners and channel managers (§2). */
async function loadChannelOwnership(signal: AbortSignal): Promise<void> {
  if (auth.can('channels.manage') || !auth.can('channels.read')) {
    managesChannels.value = auth.can('channels.manage')
    return
  }
  try {
    const me = auth.user?.id
    const channels = await fetchAllChannels(signal)
    managesChannels.value = channels.some(c => isFullChannel(c) && c.owner.id === me)
  }
  catch (err) {
    if (isAbortError(err))
      throw err
    managesChannels.value = false
  }
}

function adopt(p: NotificationPreferences) {
  saved.value = p
  form.value = formFromPreferences(p)
  errors.value = {}
  formError.value = null
  conflict.value = false
}

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const [prefs] = await Promise.all([notificationsApi.preferences(ctrl.signal), loadChannelOwnership(ctrl.signal)])
    adopt(prefs)
  }
  catch (err) {
    if (!isAbortError(err))
      loadError.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}
onMounted(load)
onBeforeUnmount(() => controller?.abort())

/**
 * The email confirm / webhook secret endpoints change the stored document: take the
 * server copy (new version, address, secretSet) but keep the user's other unsaved edits.
 */
async function refreshServerSide(kind: 'email' | 'secret') {
  try {
    const fresh = await notificationsApi.preferences()
    saved.value = fresh
    if (form.value && kind === 'email')
      form.value.emailAddress = fresh.email?.address || null
  }
  catch (err) {
    toast.error('无法重新加载通知设置', { description: errorMessage(err) })
  }
}

const smtpUnavailable = computed(() => saved.value?.smtpConfigured === false || smtpInferredUnavailable.value)

const visibleEvents = computed(() => eligibleEvents(ctx.value))
const showWallet = computed(() => isCategoryEligible('wallet', ctx.value))
const dirty = computed(() => (saved.value && form.value ? dirtyParts(form.value, saved.value, ctx.value) : []))
const anyDirty = computed(() => dirty.value.length > 0)
const webhookDirty = computed(() => dirty.value.includes('Webhook'))

// ---------- save ----------
async function save() {
  const s = saved.value
  const f = form.value
  if (!s || !f)
    return
  formError.value = null
  const errs = validatePreferences(f, { ...ctx.value, accountEmail: accountEmail.value })
  errors.value = errs
  if (Object.keys(errs).length) {
    formError.value = '请修正标记的字段后再保存'
    return
  }
  saving.value = true
  try {
    const res = await notificationsApi.updatePreferences(buildPreferencesBody(f, s, ctx.value))
    adopt(res && typeof res.version === 'number' && res.events ? res : await notificationsApi.preferences())
    toast.success('通知设置已保存')
  }
  catch (err) {
    if (isVersionConflict(err)) {
      conflict.value = true
    }
    else if (isApiError(err) && err.status === 422) {
      errors.value = normalizePrefsErrorKeys(fieldErrors(err))
      formError.value = err.message
    }
    else {
      formError.value = errorMessage(err)
    }
  }
  finally {
    saving.value = false
  }
}

function discard() {
  if (saved.value)
    adopt(saved.value)
}

function resetEvents() {
  if (!form.value)
    return
  const defaults = defaultEvents()
  const next = { ...form.value.events }
  for (const e of visibleEvents.value)
    next[e.type] = { ...defaults[e.type]! }
  form.value.events = next
}

// ---------- digest & time zone ----------
const tz = computed(() => timezoneOptions(form.value?.timezone ?? ''))
const browserTz = browserTimezone()
function setDigest(v: unknown) {
  if (form.value && (v === 'off' || v === 'daily'))
    form.value.digest = v as DigestMode
}
function setTimezone(v: unknown) {
  if (form.value && typeof v === 'string')
    form.value.timezone = v
}
const thresholdPreview = computed(() => {
  const v = form.value?.walletBalanceLow.trim() ?? ''
  return isValidAmount(v) ? money(v) : null
})

// ---------- unsaved changes guard ----------
const leaveOpen = ref(false)
let leaveResolve: ((ok: boolean) => void) | null = null
onBeforeRouteLeave(() => {
  if (!anyDirty.value)
    return true
  leaveOpen.value = true
  return new Promise<boolean>((resolve) => {
    leaveResolve = resolve
  })
})
function confirmLeave() {
  const r = leaveResolve
  leaveResolve = null
  leaveOpen.value = false
  r?.(true)
}
watch(leaveOpen, (v) => {
  if (!v && leaveResolve) {
    leaveResolve(false)
    leaveResolve = null
  }
})
useEventListener(window, 'beforeunload', (e: BeforeUnloadEvent) => {
  if (anyDirty.value) {
    e.preventDefault()
    e.returnValue = ''
  }
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="通知中心" description="选择通过哪些方式接收哪些事件。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading || saving" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          重新加载
        </Button>
      </template>
    </PageHeader>

    <NotificationTabs />

    <div v-if="conflict" class="border-destructive/40 bg-destructive/5 flex flex-wrap items-center gap-3 rounded-lg border p-3 text-sm" role="alert" data-testid="prefs-conflict">
      <CircleAlert class="text-destructive size-4 shrink-0" />
      <div class="min-w-0 flex-1">
        <p class="text-destructive font-medium">
          通知设置已在其他地方被修改
        </p>
        <p class="text-muted-foreground text-xs">
          重新加载会获取最新设置并丢弃本页未保存的修改。
        </p>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" />
        重新加载
      </Button>
    </div>

    <ErrorState v-if="loadError && !saved" :error="loadError" @retry="load" />
    <div v-else-if="!saved || !form" class="space-y-4">
      <div class="grid gap-4 lg:grid-cols-2">
        <Skeleton class="h-56 w-full" />
        <Skeleton class="h-56 w-full" />
      </div>
      <Skeleton class="h-96 w-full" />
    </div>

    <template v-else>
      <div class="grid items-start gap-4 lg:grid-cols-2">
        <EmailChannelCard
          v-model:enabled="form.emailEnabled"
          v-model:address="form.emailAddress"
          :saved="saved"
          :account-email="accountEmail"
          :smtp-unavailable="smtpUnavailable"
          :error="errors['email.address'] ?? errors['email.enabled']"
          @confirmed="refreshServerSide('email')"
          @smtp-unavailable="smtpInferredUnavailable = true"
        />
        <WebhookChannelCard
          v-model:enabled="form.webhookEnabled"
          v-model:url="form.webhookUrl"
          v-model:format="form.webhookFormat"
          :saved="saved"
          :dirty="webhookDirty"
          :errors="errors"
          @secret-changed="refreshServerSide('secret')"
        />
      </div>

      <Card data-section="events">
        <CardHeader>
          <CardTitle class="text-base">
            事件
          </CardTitle>
          <CardDescription>
            每个事件可以单独选择接收方式。只列出你的账号会收到的事件。<template v-if="!form.emailEnabled || !form.webhookEnabled">
              {{ [!form.emailEnabled ? '邮件' : '', !form.webhookEnabled ? 'Webhook' : ''].filter(Boolean).join('与') }}未启用，对应的开关暂不生效。
            </template>
          </CardDescription>
          <CardAction>
            <Button variant="ghost" size="sm" data-testid="events-reset" @click="resetEvents">
              <RotateCcw />
              恢复默认
            </Button>
          </CardAction>
        </CardHeader>
        <CardContent>
          <EventMatrix
            v-model="form.events"
            :events="visibleEvents"
            :email-enabled="form.emailEnabled && !smtpUnavailable"
            :webhook-enabled="form.webhookEnabled"
            :digest-daily="form.digest === 'daily'"
            :errors="errors"
          />
        </CardContent>
      </Card>

      <Card data-section="digest">
        <CardHeader>
          <CardTitle class="text-base">
            提醒与摘要
          </CardTitle>
          <CardDescription>余额提醒阈值，以及邮件的发送节奏。</CardDescription>
        </CardHeader>
        <CardContent class="grid gap-4 md:grid-cols-3" data-testid="digest-row">
          <FormField v-if="showWallet" :label="`钱包余额提醒阈值（${currency?.code ?? '结算币种'}）`" for="ntf-threshold" :error="errors['thresholds.walletBalanceLow']">
            <div class="relative">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
              <Input id="ntf-threshold" v-model="form.walletBalanceLow" inputmode="decimal" placeholder="1" class="pl-7 font-mono tabular-nums" :aria-invalid="!!errors['thresholds.walletBalanceLow']" data-testid="wallet-threshold" />
            </div>
            <template #hint>
              可用余额降到 {{ thresholdPreview ?? '阈值' }} 以下时提醒，恢复到阈值以上后才会再次提醒。
            </template>
          </FormField>
          <FormField label="邮件节奏" for="ntf-digest" :error="errors.digest">
            <Select :model-value="form.digest" @update:model-value="setDigest">
              <SelectTrigger id="ntf-digest" class="w-full" data-testid="digest-select">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="off">
                  {{ DIGEST_LABELS.off }}
                </SelectItem>
                <SelectItem value="daily">
                  {{ DIGEST_LABELS.daily }}
                </SelectItem>
              </SelectContent>
            </Select>
            <template #hint>
              <template v-if="form.digest === 'daily'">
                除告警类事件（渠道与上游、额度用完、余额不足）外，每天 09:00（下方时区）合并为一封邮件。
              </template>
              <template v-else>
                每个事件发生时单独发送一封邮件。
              </template>
            </template>
          </FormField>
          <FormField label="时区" for="ntf-tz" :error="errors.timezone">
            <Select :model-value="form.timezone" @update:model-value="setTimezone">
              <SelectTrigger id="ntf-tz" class="w-full" data-testid="timezone-select">
                <SelectValue />
              </SelectTrigger>
              <SelectContent class="max-h-80">
                <SelectGroup>
                  <SelectLabel>常用</SelectLabel>
                  <SelectItem v-for="z in tz.common" :key="z" :value="z">
                    {{ z }} <span class="text-muted-foreground text-xs">{{ utcOffsetLabel(z) }}</span>
                  </SelectItem>
                </SelectGroup>
                <template v-if="tz.others.length">
                  <SelectSeparator />
                  <SelectGroup>
                    <SelectLabel>全部</SelectLabel>
                    <SelectItem v-for="z in tz.others" :key="z" :value="z">
                      {{ z }}
                    </SelectItem>
                  </SelectGroup>
                </template>
              </SelectContent>
            </Select>
            <template #hint>
              <span class="inline-flex flex-wrap items-center gap-x-1">
                <Clock class="size-3" />用于每日摘要的发送时间。
                <Button v-if="browserTz && browserTz !== form.timezone" variant="link" size="xs" class="h-auto p-0 text-xs" @click="setTimezone(browserTz)">
                  使用浏览器时区（{{ browserTz }}）
                </Button>
              </span>
            </template>
          </FormField>
        </CardContent>
      </Card>

      <!-- Sticky save bar -->
      <div
        class="bg-background/95 supports-backdrop-filter:bg-background/80 sticky bottom-0 z-10 -mx-4 flex flex-wrap items-center justify-end gap-2 border-t px-4 py-3 backdrop-blur sm:-mx-6 sm:px-6"
        data-testid="prefs-savebar"
      >
        <p v-if="formError" class="text-destructive mr-auto text-xs" role="alert">
          {{ formError }}
        </p>
        <p v-else-if="anyDirty" class="text-muted-foreground mr-auto text-xs" role="status">
          未保存：{{ dirty.join('、') }}
        </p>
        <p v-else class="text-muted-foreground mr-auto text-xs">
          所有修改已保存
        </p>
        <Button variant="ghost" size="sm" :disabled="!anyDirty || saving" @click="discard">
          <Undo2 />
          放弃修改
        </Button>
        <Button size="sm" :disabled="!anyDirty || saving || conflict" data-testid="prefs-save" @click="save">
          <Loader2 v-if="saving" class="animate-spin" />
          <Save v-else />
          保存
        </Button>
      </div>
    </template>

    <ConfirmDialog
      v-model:open="leaveOpen"
      title="放弃未保存的修改？"
      confirm-text="离开"
      cancel-text="留在本页"
      destructive
      @confirm="confirmLeave"
    >
      <p>以下设置尚未保存：{{ dirty.join('、') }}。离开后这些修改将丢失。</p>
    </ConfirmDialog>
  </div>
</template>
