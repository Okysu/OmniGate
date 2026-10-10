<script setup lang="ts">
import type { SettingsForm } from '@/lib/settingsForm'
import type { SettingsResponse, SettingsSection } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useEventListener } from '@vueuse/core'
import { CircleAlert, KeyRound, Loader2, Lock, Mail, RefreshCw, Save, Send, Undo2 } from '@lucide/vue'
import { toast } from 'vue-sonner'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import ErrorState from '@/components/ErrorState.vue'
import PageHeader from '@/components/PageHeader.vue'
import AnnouncementBanner from '@/components/layout/AnnouncementBanner.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { DOCS_URL } from '@/config/nav'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage, fieldErrors, isApiError, isVersionConflict } from '@/lib/api'
import { settingsApi } from '@/lib/endpoints'
import { isAbortError } from '@/lib/query'
import {
  buildResetPatch,
  buildSectionPatch,
  DEFAULT_SETTINGS,
  FIELD_LABELS,
  formFieldOfKey,
  formFromSettings,
  isSectionDirty,
  LIMITS,
  normalizeErrorKeys,
  readonlyEntries,
  REGISTRATION_DESCRIPTIONS,
  REGISTRATION_LABELS,
  REGISTRATION_MODES,
  SECTION_TITLES,
  SECTIONS,
  sectionFromSettings,
  SMTP_DEFAULT_PORTS,
  SMTP_SECURITY,
  SMTP_SECURITY_HINTS,
  SMTP_SECURITY_LABELS,
  sourceOf,
  validateSection,
} from '@/lib/settingsForm'
import { isValidAmount } from '@/lib/money'
import { useSystemStore } from '@/stores/system'
import RetryOnPicker from '@/components/RetryOnPicker.vue'
import DomainTagsInput from './settings/DomainTagsInput.vue'
import SettingField from './settings/SettingField.vue'
import SmtpTestDialog from './settings/SmtpTestDialog.vue'
import AffinityCard from './settings/AffinityCard.vue'
import { useAuthStore } from '@/stores/auth'

const system = useSystemStore()
const auth = useAuthStore()
const { currency, money } = useCurrency()

const data = ref<SettingsResponse | null>(null)
const form = ref<SettingsForm>(formFromSettings(DEFAULT_SETTINGS))
const loadError = ref<unknown>(null)
const loading = ref(false)
const conflict = ref(false)
const errors = ref<Record<string, string>>({})
const sectionError = ref<Partial<Record<SettingsSection, string>>>({})
const saving = ref<SettingsSection | null>(null)
const resetting = ref<string | null>(null)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  loadError.value = null
  try {
    const res = await settingsApi.get(ctrl.signal)
    data.value = res
    form.value = formFromSettings(res.settings)
    errors.value = {}
    sectionError.value = {}
    conflict.value = false
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

const busy = computed(() => saving.value !== null || resetting.value !== null)

function src(key: string) {
  return sourceOf(data.value?.sources, key)
}

const dirty = computed<Record<SettingsSection, boolean>>(() => {
  const d = data.value
  const f = form.value
  const out = { site: false, auth: false, billing: false, gateway: false, notifications: false }
  if (!d)
    return out
  for (const s of SECTIONS)
    out[s] = isSectionDirty(s, f[s], d.settings)
  return out
})
/** 「会话亲和」 is saved on its own (AffinityCard). */
const affinityDirty = ref(false)
const anyDirty = computed(() => SECTIONS.some(s => dirty.value[s]) || affinityDirty.value)
async function onAffinitySaved(res: SettingsResponse) {
  await adopt(res)
}

function setSection<S extends SettingsSection>(f: SettingsForm, s: S, v: SettingsForm[S]) {
  f[s] = v
}

function clearSectionErrors(section: SettingsSection) {
  errors.value = Object.fromEntries(Object.entries(errors.value).filter(([k]) => !k.startsWith(`${section}.`)))
  sectionError.value = { ...sectionError.value, [section]: undefined }
}

/** Takes a PATCH response (falls back to a fresh GET when it is not the full document). */
async function adopt(res: SettingsResponse | null | undefined): Promise<SettingsResponse> {
  const full = res && res.settings && typeof res.version === 'number' ? res : await settingsApi.get()
  data.value = full
  // Banner, site name, docs link and landing switch come from /api/system/info.
  void system.load()
  return full
}

function handleError(section: SettingsSection, err: unknown) {
  if (isVersionConflict(err)) {
    conflict.value = true
    sectionError.value = { ...sectionError.value, [section]: '设置已被其他人修改，请重新加载后再保存' }
  }
  else if (isApiError(err) && err.status === 422) {
    errors.value = { ...errors.value, ...normalizeErrorKeys(fieldErrors(err)) }
    sectionError.value = { ...sectionError.value, [section]: err.message }
  }
  else {
    sectionError.value = { ...sectionError.value, [section]: errorMessage(err) }
  }
}

async function save(section: SettingsSection) {
  const d = data.value
  const f = form.value
  if (!d)
    return
  clearSectionErrors(section)
  const errs = validateSection(section, f[section])
  if (Object.keys(errs).length) {
    errors.value = { ...errors.value, ...errs }
    sectionError.value = { ...sectionError.value, [section]: '请修正标记的字段后再保存' }
    return
  }
  saving.value = section
  try {
    const res = await settingsApi.update(buildSectionPatch(section, f[section], d.settings, d.version))
    const full = await adopt(res)
    setSection(f, section, sectionFromSettings(full.settings, section))
    toast.success(`「${SECTION_TITLES[section]}」设置已保存`, { description: '所有实例将在 5 秒内生效。' })
  }
  catch (err) {
    handleError(section, err)
  }
  finally {
    saving.value = null
  }
}

async function resetField(key: string) {
  const d = data.value
  const f = form.value
  if (!d)
    return
  const [section, field] = formFieldOfKey(key)
  resetting.value = key
  try {
    const full = await adopt(await settingsApi.update(buildResetPatch(key, d.version)))
    const fresh = sectionFromSettings(full.settings, section) as Record<string, unknown>
    Object.assign(f[section], { [field]: fresh[field] })
    if (key === 'notifications.smtp.password')
      f.notifications.password = ''
    const { [key]: _drop, ...rest } = errors.value
    errors.value = rest
    toast.success(`「${FIELD_LABELS[key] ?? key}」已恢复为${src(key) === 'env' ? '环境变量中的值' : '默认值'}`)
  }
  catch (err) {
    handleError(section, err)
  }
  finally {
    resetting.value = null
  }
}

function discard(section: SettingsSection) {
  if (!data.value)
    return
  setSection(form.value, section, sectionFromSettings(data.value.settings, section))
  clearSectionErrors(section)
}

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

// ---------- display ----------
const readonly = computed(() => readonlyEntries(data.value?.readonly))
const dirtySections = computed(() => [...SECTIONS.filter(s => dirty.value[s]).map(s => SECTION_TITLES[s]), ...(affinityDirty.value ? ['会话亲和'] : [])])
const creditPreview = computed(() => {
  const v = form.value.billing.signupCredit.trim()
  return isValidAmount(v) ? money(v) : null
})
const enforceTurningOn = computed(() => form.value.billing.enforce && data.value?.settings.billing.enforce === false)
// ---------- 邮件（SMTP） ----------
/** Older backends do not send `settings.notifications`: the card explains and stays read-only. */
const smtpSupported = computed(() => !!data.value?.settings.notifications)
const savedSmtp = computed(() => data.value?.settings.notifications?.smtp ?? null)
const isProduction = computed(() => data.value?.readonly?.env === 'production')
function setSecurity(v: unknown) {
  if (v !== 'starttls' && v !== 'tls' && v !== 'none')
    return
  const n = form.value.notifications
  // Follow the conventional port when it still holds the previous mode's default.
  if (Number(n.port) === SMTP_DEFAULT_PORTS[n.security])
    n.port = SMTP_DEFAULT_PORTS[v]
  n.security = v
}
function cancelPassword() {
  form.value.notifications.passwordAction = 'keep'
  form.value.notifications.password = ''
}
const smtpTestOpen = ref(false)

const restrictedWithoutDomains = computed(() => form.value.auth.registrationMode === 'restricted' && form.value.auth.allowedEmailDomains.length === 0)
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="系统设置" description="（系统管理员）可在运行时修改的全局设置，保存后所有实例在 5 秒内生效。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading || busy" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          重新加载
        </Button>
      </template>
    </PageHeader>

    <div v-if="conflict" class="border-destructive/40 bg-destructive/5 flex flex-wrap items-center gap-3 rounded-lg border p-3 text-sm" role="alert" data-testid="settings-conflict">
      <CircleAlert class="text-destructive size-4 shrink-0" />
      <div class="min-w-0 flex-1">
        <p class="text-destructive font-medium">
          设置已被其他人修改
        </p>
        <p class="text-muted-foreground text-xs">
          重新加载会获取最新设置并丢弃本页所有未保存的修改。
        </p>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" />
        重新加载
      </Button>
    </div>

    <ErrorState v-if="loadError && !data" :error="loadError" @retry="load" />
    <div v-else-if="!data" class="space-y-4">
      <Skeleton v-for="i in 4" :key="i" class="h-48 w-full" />
    </div>

    <template v-else>
      <p v-if="anyDirty" class="text-muted-foreground text-xs" role="status">
        未保存：{{ dirtySections.join('、') }}（每个分组单独保存）
      </p>

      <!-- 站点 -->
      <Card data-section="site">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            站点
            <Badge v-if="dirty.site" variant="outline" class="border-amber-500/50 text-amber-700 dark:text-amber-400">
              未保存
            </Badge>
          </CardTitle>
          <CardDescription>站点名称、控制台公告、公开首页、公开模型广场与文档链接。</CardDescription>
        </CardHeader>
        <CardContent class="grid gap-6 lg:grid-cols-2">
          <div class="space-y-5">
            <SettingField label="站点名称" for="set-site-name" :source="src('site.name')" :error="errors['site.name']" :hint="`显示在控制台顶栏、侧边栏、浏览器标题和首页中，1–${LIMITS.nameMax} 个字符。`" :resetting="resetting === 'site.name'" :busy="busy" @reset="resetField('site.name')">
              <Input id="set-site-name" v-model="form.site.name" :maxlength="LIMITS.nameMax" placeholder="OmniGate" :aria-invalid="!!errors['site.name']" />
            </SettingField>
            <SettingField label="文档链接" for="set-site-docs" :source="src('site.docsUrl')" :error="errors['site.docsUrl']" :resetting="resetting === 'site.docsUrl'" :busy="busy" @reset="resetField('site.docsUrl')">
              <Input id="set-site-docs" v-model="form.site.docsUrl" type="url" placeholder="https://docs.example.com" class="font-mono text-xs" :aria-invalid="!!errors['site.docsUrl']" />
              <template #hint>
                控制台顶栏与首页的「文档」链接；留空时使用构建变量 VITE_DOCS_URL<template v-if="DOCS_URL">
                  （<span class="font-mono">{{ DOCS_URL }}</span>）
                </template><template v-else>
                  （未设置，不显示文档链接）
                </template>。
              </template>
            </SettingField>
            <SettingField label="公开首页" for="set-site-landing" :source="src('site.landingEnabled')" :error="errors['site.landingEnabled']" :resetting="resetting === 'site.landingEnabled'" :busy="busy" @reset="resetField('site.landingEnabled')">
              <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
                <p class="text-muted-foreground text-xs">
                  <template v-if="form.site.landingEnabled">
                    开启：访问 <span class="font-mono">/</span> 显示公开的产品首页（未登录也可访问）。
                  </template>
                  <template v-else>
                    关闭：访问 <span class="font-mono">/</span> 直接进入控制台；未登录时先跳转到登录页。
                  </template>
                </p>
                <Switch id="set-site-landing" v-model="form.site.landingEnabled" />
              </div>
            </SettingField>
            <SettingField label="公开模型广场" for="set-site-plaza" :source="src('site.publicModelPlaza')" :error="errors['site.publicModelPlaza']" :resetting="resetting === 'site.publicModelPlaza'" :busy="busy" @reset="resetField('site.publicModelPlaza')">
              <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
                <p class="text-muted-foreground text-xs">
                  <template v-if="form.site.publicModelPlaza">
                    开启：未登录访客可以在 <span class="font-mono">/models</span> 查看平台模型与价格。
                  </template>
                  <template v-else>
                    关闭：<span class="font-mono">/models</span> 需要登录后才能查看；控制台中的模型广场不受影响。
                  </template>
                </p>
                <Switch id="set-site-plaza" v-model="form.site.publicModelPlaza" data-testid="set-site-plaza" />
              </div>
            </SettingField>
          </div>
          <div class="space-y-3">
            <SettingField label="控制台公告" for="set-site-ann" :source="src('site.announcement')" :error="errors['site.announcement']" :resetting="resetting === 'site.announcement'" :busy="busy" @reset="resetField('site.announcement')">
              <Textarea id="set-site-ann" v-model="form.site.announcement" rows="4" :maxlength="LIMITS.announcementMax" placeholder="例如：本周六 02:00–04:00 例行维护，期间请求可能失败。" :aria-invalid="!!errors['site.announcement']" />
              <template #hint>
                纯文本，显示在所有用户控制台顶部，用户可以关闭（内容变化后会再次显示）；留空则不显示。{{ form.site.announcement.trim().length }} / {{ LIMITS.announcementMax }}
              </template>
            </SettingField>
            <div class="space-y-1.5">
              <p class="text-muted-foreground text-xs">
                预览
              </p>
              <div class="bg-background overflow-hidden rounded-lg border" data-testid="announcement-preview">
                <div class="bg-muted/60 flex h-7 items-center gap-2 border-b px-3">
                  <span class="bg-primary size-3 rounded-sm" />
                  <span class="truncate text-xs font-semibold">{{ form.site.name.trim() || 'OmniGate' }}</span>
                </div>
                <AnnouncementBanner v-if="form.site.announcement.trim()" :text="form.site.announcement" preview />
                <p v-else class="text-muted-foreground px-3 py-2 text-xs">
                  公告为空，不显示横幅。
                </p>
                <div class="space-y-1.5 p-3" aria-hidden="true">
                  <div class="bg-muted h-2 w-1/3 rounded" />
                  <div class="bg-muted h-2 w-2/3 rounded" />
                </div>
              </div>
            </div>
          </div>
        </CardContent>
        <CardFooter class="flex-wrap justify-end gap-2 border-t">
          <p v-if="sectionError.site" class="text-destructive mr-auto text-xs" role="alert">
            {{ sectionError.site }}
          </p>
          <Button variant="ghost" size="sm" :disabled="!dirty.site || busy" @click="discard('site')">
            <Undo2 />
            放弃修改
          </Button>
          <Button size="sm" :disabled="!dirty.site || busy || conflict" data-testid="save-site" @click="save('site')">
            <Loader2 v-if="saving === 'site'" class="animate-spin" />
            <Save v-else />
            保存
          </Button>
        </CardFooter>
      </Card>

      <!-- 登录与注册 -->
      <Card data-section="auth">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            登录与注册
            <Badge v-if="dirty.auth" variant="outline" class="border-amber-500/50 text-amber-700 dark:text-amber-400">
              未保存
            </Badge>
          </CardTitle>
          <CardDescription>新用户首次登录时是否自动创建账号。登录方式本身由环境变量配置（见页面底部）。</CardDescription>
        </CardHeader>
        <CardContent class="space-y-5">
          <SettingField label="注册模式" :source="src('auth.registrationMode')" :error="errors['auth.registrationMode']" :resetting="resetting === 'auth.registrationMode'" :busy="busy" @reset="resetField('auth.registrationMode')">
            <div role="radiogroup" aria-label="注册模式" class="grid gap-2 md:grid-cols-3">
              <label
                v-for="m in REGISTRATION_MODES"
                :key="m"
                class="has-checked:border-primary has-checked:bg-primary/5 has-focus-visible:ring-ring/50 hover:bg-muted/50 flex cursor-pointer flex-col gap-1 rounded-lg border p-3 has-focus-visible:ring-3"
              >
                <span class="flex items-center gap-2">
                  <input v-model="form.auth.registrationMode" type="radio" name="reg-mode" class="accent-primary size-3.5" :value="m">
                  <span class="text-sm font-medium">{{ REGISTRATION_LABELS[m] }}</span>
                </span>
                <span class="text-muted-foreground text-xs leading-relaxed">{{ REGISTRATION_DESCRIPTIONS[m] }}</span>
              </label>
            </div>
          </SettingField>
          <SettingField
            v-if="form.auth.registrationMode === 'restricted'"
            label="允许的邮箱域名"
            for="set-auth-domains"
            :source="src('auth.allowedEmailDomains')"
            :error="errors['auth.allowedEmailDomains']"
            :resetting="resetting === 'auth.allowedEmailDomains'"
            :busy="busy"
            @reset="resetField('auth.allowedEmailDomains')"
          >
            <DomainTagsInput id="set-auth-domains" v-model="form.auth.allowedEmailDomains" :invalid="!!errors['auth.allowedEmailDomains']" />
            <template #hint>
              <span v-if="restrictedWithoutDomains" class="text-amber-700 dark:text-amber-400">列表为空时，只有 OMNIGATE_AUTH_ALLOWED_IDENTITIES 中列出的用户可以自动注册。</span>
              <template v-else>
                邮箱域名与列表中的某一项完全相同（不含子域名，不区分大小写）才会自动创建账号，例如 example.com。可粘贴多个，用逗号或换行分隔。
              </template>
            </template>
          </SettingField>
        </CardContent>
        <CardFooter class="flex-wrap justify-end gap-2 border-t">
          <p v-if="sectionError.auth" class="text-destructive mr-auto text-xs" role="alert">
            {{ sectionError.auth }}
          </p>
          <Button variant="ghost" size="sm" :disabled="!dirty.auth || busy" @click="discard('auth')">
            <Undo2 />
            放弃修改
          </Button>
          <Button size="sm" :disabled="!dirty.auth || busy || conflict" data-testid="save-auth" @click="save('auth')">
            <Loader2 v-if="saving === 'auth'" class="animate-spin" />
            <Save v-else />
            保存
          </Button>
        </CardFooter>
      </Card>

      <!-- 计费 -->
      <Card data-section="billing">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            计费
            <Badge v-if="dirty.billing" variant="outline" class="border-amber-500/50 text-amber-700 dark:text-amber-400">
              未保存
            </Badge>
          </CardTitle>
          <CardDescription>余额强制与新用户赠送余额。</CardDescription>
        </CardHeader>
        <CardContent class="grid gap-6 lg:grid-cols-2">
          <SettingField label="余额强制（billing.enforce）" for="set-bill-enforce" :source="src('billing.enforce')" :error="errors['billing.enforce']" :resetting="resetting === 'billing.enforce'" :busy="busy" @reset="resetField('billing.enforce')">
            <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
              <ul class="text-muted-foreground list-disc space-y-0.5 pl-4 text-xs">
                <li><strong class="text-foreground">关闭</strong>：只记录费用，不拦截请求（适合个人自用）。</li>
                <li><strong class="text-foreground">开启</strong>：预付费模式。可用余额大于 0 时放行请求，并预留「输入估算 + max_tokens（如请求指定）」的金额（不超过可用余额）；可用余额为 0 或负数时拒绝（402）；结束后按实际用量结算。没有售价的模型不受限制。</li>
              </ul>
              <Switch id="set-bill-enforce" v-model="form.billing.enforce" />
            </div>
            <template #hint>
              <span v-if="enforceTurningOn" class="text-amber-700 dark:text-amber-400">保存后立即生效：可用余额为 0 或负数的请求将被拒绝，请确认用户已有足够余额。</span>
              <template v-else>
                与「兑换码」页面中的「余额强制」开关是同一个设置。
              </template>
            </template>
          </SettingField>
          <SettingField :label="`新用户赠送余额（${currency?.code ?? '结算币种'}）`" for="set-bill-credit" :source="src('billing.signupCredit')" :error="errors['billing.signupCredit']" :resetting="resetting === 'billing.signupCredit'" :busy="busy" @reset="resetField('billing.signupCredit')">
            <div class="relative max-w-60">
              <span class="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-sm">{{ currency?.symbol ?? '' }}</span>
              <Input id="set-bill-credit" v-model="form.billing.signupCredit" inputmode="decimal" placeholder="0" class="pl-7 font-mono tabular-nums" :aria-invalid="!!errors['billing.signupCredit']" />
            </div>
            <template #hint>
              新用户首次登录（账号创建）时赠送到钱包的余额；0 表示不赠送。<template v-if="creditPreview">
                当前：{{ creditPreview }}
              </template>
            </template>
          </SettingField>
        </CardContent>
        <CardFooter class="flex-wrap justify-end gap-2 border-t">
          <p v-if="sectionError.billing" class="text-destructive mr-auto text-xs" role="alert">
            {{ sectionError.billing }}
          </p>
          <Button variant="ghost" size="sm" :disabled="!dirty.billing || busy" @click="discard('billing')">
            <Undo2 />
            放弃修改
          </Button>
          <Button size="sm" :disabled="!dirty.billing || busy || conflict" data-testid="save-billing" @click="save('billing')">
            <Loader2 v-if="saving === 'billing'" class="animate-spin" />
            <Save v-else />
            保存
          </Button>
        </CardFooter>
      </Card>

      <!-- 网关 -->
      <Card data-section="gateway">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            网关
            <Badge v-if="dirty.gateway" variant="outline" class="border-amber-500/50 text-amber-700 dark:text-amber-400">
              未保存
            </Badge>
          </CardTitle>
          <CardDescription>未命中路由规则时的默认重试次数与重试条件，以及请求日志保留。</CardDescription>
        </CardHeader>
        <CardContent class="grid gap-6 md:grid-cols-2">
          <SettingField label="默认最多尝试次数" for="set-gw-attempts" :source="src('gateway.maxAttempts')" :error="errors['gateway.maxAttempts']" hint="没有路由规则命中时，一次请求最多尝试的渠道数（含首次），1–5。" :resetting="resetting === 'gateway.maxAttempts'" :busy="busy" @reset="resetField('gateway.maxAttempts')">
            <Input id="set-gw-attempts" v-model="form.gateway.maxAttempts" type="number" min="1" max="5" step="1" class="tabular-nums" :aria-invalid="!!errors['gateway.maxAttempts']" />
          </SettingField>
          <SettingField label="请求日志保留天数" for="set-gw-retention" :source="src('gateway.logRetentionDays')" :error="errors['gateway.logRetentionDays']" :resetting="resetting === 'gateway.logRetentionDays'" :busy="busy" @reset="resetField('gateway.logRetentionDays')">
            <div class="flex items-center gap-2">
              <Input id="set-gw-retention" v-model="form.gateway.logRetentionDays" type="number" min="0" :max="LIMITS.logRetentionDays[1]" step="1" class="tabular-nums" :aria-invalid="!!errors['gateway.logRetentionDays']" />
              <span class="text-muted-foreground shrink-0 text-sm">天</span>
            </div>
            <template #hint>
              超过保留期的请求日志会被定期清理；<strong class="text-foreground">0 = 永久保留</strong>。
            </template>
          </SettingField>
          <SettingField label="默认重试条件（未命中路由规则时）" class="md:col-span-2" :source="src('gateway.retryOn')" :error="errors['gateway.retryOn']" :resetting="resetting === 'gateway.retryOn'" :busy="busy" @reset="resetField('gateway.retryOn')">
            <RetryOnPicker v-model="form.gateway.retryOn" id-prefix="set-gw-retry" class="lg:grid-cols-3" />
            <template #hint>
              勾选的失败类型会换下一个渠道重试（不超过上面的最多尝试次数）；未勾选的直接返回给客户端。全部不勾选 = 不重试。命中路由规则的请求使用规则自己的重试条件。
            </template>
          </SettingField>
        </CardContent>
        <CardFooter class="flex-wrap justify-end gap-2 border-t">
          <p v-if="sectionError.gateway" class="text-destructive mr-auto text-xs" role="alert">
            {{ sectionError.gateway }}
          </p>
          <Button variant="ghost" size="sm" :disabled="!dirty.gateway || busy" @click="discard('gateway')">
            <Undo2 />
            放弃修改
          </Button>
          <Button size="sm" :disabled="!dirty.gateway || busy || conflict" data-testid="save-gateway" @click="save('gateway')">
            <Loader2 v-if="saving === 'gateway'" class="animate-spin" />
            <Save v-else />
            保存
          </Button>
        </CardFooter>
      </Card>

      <AffinityCard v-if="data" :data="data" :busy="busy" :conflict="conflict" @saved="onAffinitySaved" @conflict="conflict = true" @dirty="(v: boolean) => (affinityDirty = v)" />

      <!-- 邮件（SMTP） -->
      <Card data-section="notifications">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            <Mail class="size-4" />
            邮件（SMTP）
            <Badge v-if="dirty.notifications" variant="outline" class="border-amber-500/50 text-amber-700 dark:text-amber-400">
              未保存
            </Badge>
          </CardTitle>
          <CardDescription>
            发送通知邮件与邮箱验证码的 SMTP 服务。服务器为空表示未配置：邮件通知不可用，站内通知照常记录。
          </CardDescription>
        </CardHeader>
        <CardContent v-if="!smtpSupported">
          <p class="text-muted-foreground rounded-lg border border-dashed p-3 text-sm" data-testid="smtp-unsupported">
            当前服务端版本未返回邮件设置（notifications.*），升级服务端后可在此配置 SMTP。
          </p>
        </CardContent>
        <template v-else>
          <CardContent class="space-y-5">
            <div class="grid gap-4 md:grid-cols-[minmax(0,1fr)_8rem_11rem]" data-testid="smtp-row-server">
              <SettingField label="SMTP 服务器" for="set-smtp-host" :source="src('notifications.smtp.host')" :error="errors['notifications.smtp.host']" :resetting="resetting === 'notifications.smtp.host'" :busy="busy" @reset="resetField('notifications.smtp.host')">
                <Input id="set-smtp-host" v-model="form.notifications.host" placeholder="smtp.example.com" class="font-mono text-xs" autocomplete="off" :aria-invalid="!!errors['notifications.smtp.host']" data-testid="smtp-host" />
              </SettingField>
              <SettingField label="端口" for="set-smtp-port" :source="src('notifications.smtp.port')" :error="errors['notifications.smtp.port']" :resetting="resetting === 'notifications.smtp.port'" :busy="busy" @reset="resetField('notifications.smtp.port')">
                <Input id="set-smtp-port" v-model="form.notifications.port" type="number" min="1" max="65535" step="1" class="tabular-nums" :aria-invalid="!!errors['notifications.smtp.port']" data-testid="smtp-port" />
              </SettingField>
              <SettingField label="加密方式" for="set-smtp-security" :source="src('notifications.smtp.security')" :error="errors['notifications.smtp.security']" :resetting="resetting === 'notifications.smtp.security'" :busy="busy" @reset="resetField('notifications.smtp.security')">
                <Select :model-value="form.notifications.security" @update:model-value="setSecurity">
                  <SelectTrigger id="set-smtp-security" class="w-full" data-testid="smtp-security">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="m in SMTP_SECURITY" :key="m" :value="m">
                      {{ SMTP_SECURITY_LABELS[m] }}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </SettingField>
            </div>
            <p class="-mt-3 text-xs" :class="form.notifications.security === 'none' ? 'text-amber-700 dark:text-amber-400' : 'text-muted-foreground'">
              {{ SMTP_SECURITY_HINTS[form.notifications.security] }}<template v-if="form.notifications.security === 'none' && isProduction">
                当前为生产环境，保存会被拒绝。
              </template>
            </p>

            <div class="grid gap-4 md:grid-cols-2" data-testid="smtp-row-auth">
              <SettingField label="用户名" for="set-smtp-user" :source="src('notifications.smtp.username')" :error="errors['notifications.smtp.username']" hint="留空表示不进行 SMTP 认证。" :resetting="resetting === 'notifications.smtp.username'" :busy="busy" @reset="resetField('notifications.smtp.username')">
                <Input id="set-smtp-user" v-model="form.notifications.username" autocomplete="off" class="font-mono text-xs" data-testid="smtp-username" />
              </SettingField>
              <SettingField label="密码" for="set-smtp-pass" :source="src('notifications.smtp.password')" :error="errors['notifications.smtp.password']" :resetting="resetting === 'notifications.smtp.password'" :busy="busy" @reset="resetField('notifications.smtp.password')">
                <div v-if="form.notifications.passwordAction === 'keep'" class="flex h-9 items-center gap-2 rounded-md border px-3" data-testid="smtp-password-state">
                  <KeyRound class="text-muted-foreground size-4 shrink-0" />
                  <span class="flex-1 truncate text-sm" :class="savedSmtp?.passwordSet ? '' : 'text-muted-foreground'">{{ savedSmtp?.passwordSet ? '已设置' : '未设置' }}</span>
                  <Button type="button" variant="outline" size="xs" data-testid="smtp-password-replace" @click="form.notifications.passwordAction = 'replace'">
                    {{ savedSmtp?.passwordSet ? '更换' : '设置' }}
                  </Button>
                  <Button v-if="savedSmtp?.passwordSet" type="button" variant="ghost" size="xs" class="text-destructive" data-testid="smtp-password-clear" @click="form.notifications.passwordAction = 'clear'">
                    清除
                  </Button>
                </div>
                <div v-else-if="form.notifications.passwordAction === 'clear'" class="flex h-9 items-center gap-2 rounded-md border border-dashed px-3">
                  <span class="text-destructive flex-1 truncate text-sm">保存后清除密码</span>
                  <Button type="button" variant="ghost" size="xs" @click="cancelPassword">
                    撤销
                  </Button>
                </div>
                <div v-else class="flex gap-2">
                  <Input id="set-smtp-pass" v-model="form.notifications.password" type="password" autocomplete="new-password" placeholder="新密码" class="font-mono text-xs" :aria-invalid="!!errors['notifications.smtp.password']" data-testid="smtp-password" />
                  <Button type="button" variant="ghost" @click="cancelPassword">
                    取消
                  </Button>
                </div>
                <template #hint>
                  只写：用主密钥加密存储，读取时只显示是否已设置。
                </template>
              </SettingField>
            </div>

            <div class="grid gap-4 md:grid-cols-2" data-testid="smtp-row-from">
              <SettingField label="发件人" for="set-smtp-from" :source="src('notifications.smtp.from')" :error="errors['notifications.smtp.from']" hint="例如 OmniGate <noreply@example.com>；需与 SMTP 账号允许的发件地址一致。" :resetting="resetting === 'notifications.smtp.from'" :busy="busy" @reset="resetField('notifications.smtp.from')">
                <Input id="set-smtp-from" v-model="form.notifications.from" placeholder="OmniGate <noreply@example.com>" class="font-mono text-xs" :aria-invalid="!!errors['notifications.smtp.from']" data-testid="smtp-from" />
              </SettingField>
              <SettingField label="每用户每小时邮件上限" for="set-ntf-rate" :source="src('notifications.emailRateLimitPerHour')" :error="errors['notifications.emailRateLimitPerHour']" :resetting="resetting === 'notifications.emailRateLimitPerHour'" :busy="busy" @reset="resetField('notifications.emailRateLimitPerHour')">
                <div class="flex items-center gap-2">
                  <Input id="set-ntf-rate" v-model="form.notifications.emailRateLimitPerHour" type="number" min="1" :max="LIMITS.emailRateLimitPerHour[1]" step="1" class="tabular-nums" :aria-invalid="!!errors['notifications.emailRateLimitPerHour']" data-testid="smtp-rate" />
                  <span class="text-muted-foreground shrink-0 text-sm">封 / 小时</span>
                </div>
                <template #hint>
                  超出的邮件合并为一封摘要；默认 20。
                </template>
              </SettingField>
            </div>

            <SettingField label="通知总开关" for="set-ntf-enabled" :source="src('notifications.enabled')" :error="errors['notifications.enabled']" :resetting="resetting === 'notifications.enabled'" :busy="busy" @reset="resetField('notifications.enabled')">
              <div class="flex items-start justify-between gap-4 rounded-lg border p-3">
                <p class="text-muted-foreground text-xs">
                  <template v-if="form.notifications.enabled">
                    开启：按用户的通知设置发送站内通知、邮件与 Webhook。
                  </template>
                  <template v-else>
                    关闭：暂停所有通知的产生与发送（站内、邮件、Webhook），用于维护或排查问题。
                  </template>
                </p>
                <Switch id="set-ntf-enabled" v-model="form.notifications.enabled" data-testid="ntf-enabled" />
              </div>
            </SettingField>
          </CardContent>
          <CardFooter class="flex-wrap justify-end gap-2 border-t">
            <p v-if="sectionError.notifications" class="text-destructive w-full text-xs" role="alert">
              {{ sectionError.notifications }}
            </p>
            <Button variant="outline" size="sm" class="mr-auto" :disabled="!savedSmtp?.host" :title="savedSmtp?.host ? undefined : '保存 SMTP 服务器后才能发送测试邮件'" data-testid="smtp-test" @click="smtpTestOpen = true">
              <Send />
              发送测试邮件
            </Button>
            <Button variant="ghost" size="sm" :disabled="!dirty.notifications || busy" @click="discard('notifications')">
              <Undo2 />
              放弃修改
            </Button>
            <Button size="sm" :disabled="!dirty.notifications || busy || conflict" data-testid="save-notifications" @click="save('notifications')">
              <Loader2 v-if="saving === 'notifications'" class="animate-spin" />
              <Save v-else />
              保存
            </Button>
          </CardFooter>
        </template>
      </Card>

      <!-- 只读 -->
      <Card data-section="readonly">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-base">
            <Lock class="size-4" />
            由环境变量控制
          </CardTitle>
          <CardDescription>
            与安全边界相关的配置只能通过环境变量设置。修改需要编辑服务端环境变量并重启服务。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <dl v-if="readonly.length" class="divide-y rounded-lg border">
            <div v-for="e in readonly" :key="e.key" class="grid gap-1 px-3 py-2.5 text-sm sm:grid-cols-[14rem_minmax(0,1fr)] sm:gap-4">
              <dt class="space-y-0.5">
                <Label as="span" class="text-sm">{{ e.label }}</Label>
                <p v-if="e.env" class="text-muted-foreground font-mono text-[11px] break-all">
                  {{ e.env }}
                </p>
              </dt>
              <dd class="min-w-0 self-center font-mono text-xs break-all">
                {{ e.value }}
              </dd>
            </div>
          </dl>
          <p v-else class="text-muted-foreground text-sm">
            服务端未返回只读配置。
          </p>
        </CardContent>
      </Card>
    </template>

    <SmtpTestDialog v-model:open="smtpTestOpen" :default-to="auth.user?.email ?? null" :dirty="dirty.notifications" />

    <ConfirmDialog
      v-model:open="leaveOpen"
      title="放弃未保存的修改？"
      confirm-text="离开"
      cancel-text="留在本页"
      destructive
      @confirm="confirmLeave"
    >
      <p>以下分组有尚未保存的修改：{{ dirtySections.join('、') }}。离开后这些修改将丢失。</p>
    </ConfirmDialog>
  </div>
</template>
