<script setup lang="ts">
import type { AuthProvider } from '@/lib/types'
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Ban, CircleAlert, KeyRound, Loader2, LogIn, Settings2 } from '@lucide/vue'
import AppLogo from '@/components/AppLogo.vue'
import GithubIcon from '@/components/GithubIcon.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { errorMessage } from '@/lib/api'
import { authApi } from '@/lib/endpoints'
import { safeRedirect } from '@/lib/paths'
import { siteNameOf } from '@/lib/site'
import { disabledNotice } from '@/lib/userAdmin'
import { useSystemStore } from '@/stores/system'

const route = useRoute()
const system = useSystemStore()
const siteName = computed(() => siteNameOf(system.info))

const providers = ref<AuthProvider[]>([])
const loading = ref(true)
const loadError = ref<string | null>(null)
const pendingProvider = ref<string | null>(null)

const LOGIN_ERRORS: Record<string, string> = {
  registration_closed: '系统当前未开放注册，请联系管理员为你创建账号。',
  not_allowed: '你的账号不在允许登录的名单中，请联系管理员。',
  account_disabled: '账号已被停用，请联系管理员。',
  state_mismatch: '登录流程失败，请重试。',
  oauth_failed: '登录流程失败，请重试。',
  unknown_provider: '该登录方式不存在或已被管理员停用。',
  rate_limited: '登录尝试过于频繁，请稍后再试。',
}

/** `?error=account_disabled` (phase7 §2.1), optionally with `reason` / `until`. */
const disabled = computed(() => disabledNotice(route.query))

const loginError = computed<string | null>(() => {
  const raw = route.query.error
  const code = Array.isArray(raw) ? raw[0] : raw
  if (!code || disabled.value)
    return null
  return LOGIN_ERRORS[code] ?? `登录失败，请稍后重试（错误代码：${code}）。`
})

const redirect = computed(() => safeRedirect(route.query.redirect))

const registrationHint = computed(() => {
  switch (system.info?.registrationMode) {
    case 'open': return '首次登录将自动创建账号。'
    case 'restricted': return '仅允许名单内的账号注册。'
    case 'closed': return '当前未开放注册，仅已有账号可登录。'
    default: return null
  }
})

async function loadProviders() {
  loading.value = true
  loadError.value = null
  try {
    const res = await authApi.providers()
    providers.value = res.providers
  }
  catch (err) {
    loadError.value = errorMessage(err)
  }
  finally {
    loading.value = false
  }
}

function login(p: AuthProvider) {
  pendingProvider.value = p.id
  // Full-page navigation: the backend redirects to the IdP and back.
  window.location.assign(authApi.loginUrl(p.id, redirect.value))
}

onMounted(() => {
  void loadProviders()
  void system.ensureLoaded()
})
</script>

<template>
  <div class="bg-muted/40 relative flex min-h-svh flex-col items-center justify-center p-4">
    <div class="absolute top-4 right-4">
      <ThemeToggle />
    </div>

    <div class="flex w-full max-w-sm flex-col gap-6">
      <div class="flex justify-center">
        <AppLogo class="text-lg" />
      </div>

      <Card>
        <CardHeader class="text-center">
          <CardTitle class="text-xl">
            登录 {{ siteName }}
          </CardTitle>
          <CardDescription>使用已配置的身份提供方登录控制台</CardDescription>
        </CardHeader>

        <CardContent class="space-y-4">
          <div
            v-if="disabled"
            role="alert"
            class="border-destructive/30 bg-destructive/5 flex gap-2 rounded-md border p-3 text-sm"
            data-testid="account-disabled"
          >
            <Ban class="text-destructive mt-0.5 size-4 shrink-0" />
            <div class="min-w-0 space-y-1">
              <p class="text-destructive font-medium">
                账号已被停用，请联系管理员
              </p>
              <p v-if="disabled.reason" class="break-all">
                原因：{{ disabled.reason }}
              </p>
              <p class="text-muted-foreground text-xs">
                {{ disabled.until ? `将于 ${disabled.until} 自动恢复` : '如有疑问，请联系站点管理员。' }}
              </p>
            </div>
          </div>
          <div
            v-else-if="loginError"
            role="alert"
            class="border-destructive/30 bg-destructive/5 text-destructive flex gap-2 rounded-md border p-3 text-sm"
          >
            <CircleAlert class="mt-0.5 size-4 shrink-0" />
            <span>{{ loginError }}</span>
          </div>

          <div v-if="loading" class="space-y-2">
            <Skeleton class="h-10 w-full" />
            <Skeleton class="h-10 w-full" />
          </div>

          <div v-else-if="loadError" class="space-y-3 text-center">
            <p class="text-muted-foreground text-sm">
              无法获取登录方式：{{ loadError }}
            </p>
            <Button variant="outline" size="sm" @click="loadProviders">
              重试
            </Button>
          </div>

          <div v-else-if="providers.length === 0" class="space-y-3 rounded-md border border-dashed p-4 text-sm">
            <div class="flex items-center gap-2 font-medium">
              <Settings2 class="size-4" />
              管理员尚未配置登录方式
            </div>
            <p class="text-muted-foreground">
              OmniGate 仅支持通过外部身份提供方登录。请在服务端设置以下环境变量后重启服务：
            </p>
            <ul class="text-muted-foreground space-y-1 font-mono text-xs break-all">
              <li>OMNIGATE_AUTH_GITHUB_CLIENT_ID</li>
              <li>OMNIGATE_AUTH_GITHUB_CLIENT_SECRET</li>
            </ul>
            <p class="text-muted-foreground">
              或配置 OIDC 提供方（<code class="font-mono text-xs">OMNIGATE_AUTH_OIDC_&lt;ID&gt;_ISSUER</code>、<code class="font-mono text-xs">OMNIGATE_AUTH_OIDC_&lt;ID&gt;_CLIENT_ID</code> 等），详见部署文档。
            </p>
          </div>

          <div v-else class="flex flex-col gap-2">
            <Button
              v-for="p in providers"
              :key="p.id"
              variant="outline"
              size="lg"
              class="w-full"
              :disabled="pendingProvider !== null"
              @click="login(p)"
            >
              <Loader2 v-if="pendingProvider === p.id" class="animate-spin" />
              <GithubIcon v-else-if="p.type === 'github'" class="size-4" />
              <KeyRound v-else-if="p.type === 'oidc'" />
              <LogIn v-else />
              使用 {{ p.displayName }} 登录
            </Button>
          </div>
        </CardContent>

        <CardFooter v-if="registrationHint" class="justify-center">
          <p class="text-muted-foreground text-center text-xs">
            {{ registrationHint }}
          </p>
        </CardFooter>
      </Card>

      <p v-if="system.info" class="text-muted-foreground text-center text-xs">
        {{ system.info.name }} · {{ system.info.version }}
      </p>
    </div>
  </div>
</template>
