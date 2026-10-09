<script setup lang="ts">
import type { PlazaCurrency, PlazaModel } from '@/lib/types'
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowRight, Lock, LogIn } from '@lucide/vue'
import PlazaCatalog from '@/components/plaza/PlazaCatalog.vue'
import { Button } from '@/components/ui/button'
import { useSite } from '@/composables/useSite'
import { isApiError } from '@/lib/api'
import { plazaApi } from '@/lib/endpoints'
import { consolePath, loginLocation } from '@/lib/paths'
import { plazaCurrency } from '@/lib/plaza'
import { publicModelPlazaOf } from '@/lib/site'
import { isAbortError } from '@/lib/query'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'
import LandingFooter from './LandingFooter.vue'
import LandingHeader from './LandingHeader.vue'

// Public model plaza (`/models`). With `site.publicModelPlaza = false` (system info) a
// signed-out visitor sees "登录后查看" without calling the API; a 401 from the API (e.g. the
// setting changed meanwhile, or an older backend without the flag) shows the same state.
const auth = useAuthStore()
const system = useSystemStore()
const { siteName } = useSite()

const items = ref<PlazaModel[]>([])
const resCurrency = ref<PlazaCurrency | null>(null)
const loading = ref(true)
const error = ref<unknown>(null)
const needsLogin = ref(false)
let controller: AbortController | null = null

async function load() {
  controller?.abort()
  const ctrl = new AbortController()
  controller = ctrl
  loading.value = true
  error.value = null
  needsLogin.value = false
  try {
    await system.ensureLoaded()
    if (ctrl.signal.aborted)
      return
    if (!auth.user && !publicModelPlazaOf(system.info)) {
      needsLogin.value = true
      return
    }
    const res = await plazaApi.models({ signal: ctrl.signal, publicPage: true })
    items.value = res.items
    resCurrency.value = res.currency
  }
  catch (err) {
    if (isAbortError(err))
      return
    if (isApiError(err) && err.status === 401)
      needsLogin.value = true
    else
      error.value = err
  }
  finally {
    if (controller === ctrl)
      loading.value = false
  }
}
onMounted(() => {
  void load()
})
onBeforeUnmount(() => controller?.abort())

const currency = computed(() => plazaCurrency(resCurrency.value, system.info?.currency ?? null))
const loginTo = loginLocation('/models')
</script>

<template>
  <div class="bg-background text-foreground flex min-h-svh flex-col">
    <LandingHeader />

    <main class="flex-1">
      <section class="border-b">
        <div class="mx-auto flex w-full max-w-6xl flex-col gap-4 px-4 py-10 sm:px-6 sm:py-14 md:flex-row md:items-end md:justify-between">
          <div class="max-w-2xl space-y-3">
            <p class="text-primary text-sm font-medium">
              模型广场
            </p>
            <h1 class="text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
              平台提供的模型与价格
            </h1>
            <p class="text-muted-foreground text-pretty">
              一个 API Key 调用全部模型，OpenAI 与 Anthropic 协议均可使用。token 价格按每 100 万 tokens 计（部分模型按次、按张或按时长计费），以实际计费为准。
            </p>
          </div>
          <Button v-if="auth.user" variant="outline" as-child class="self-start md:self-auto" data-testid="plaza-my-models">
            <RouterLink :to="consolePath('my-models')">
              我的模型
              <ArrowRight />
            </RouterLink>
          </Button>
        </div>
      </section>

      <section class="mx-auto w-full max-w-6xl px-4 py-8 sm:px-6 sm:py-10">
        <div v-if="needsLogin" class="flex flex-col items-center gap-4 rounded-xl border border-dashed px-4 py-16 text-center" data-testid="plaza-login-required">
          <span class="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-full">
            <Lock class="size-5" />
          </span>
          <div class="space-y-1">
            <p class="text-lg font-semibold">
              登录后查看
            </p>
            <p class="text-muted-foreground max-w-md text-sm">
              {{ siteName }} 的模型广场仅对已登录用户开放。登录后即可查看全部模型、价格与调用示例。
            </p>
          </div>
          <Button as-child data-testid="plaza-login-cta">
            <RouterLink :to="loginTo">
              <LogIn />
              登录查看模型
            </RouterLink>
          </Button>
        </div>
        <PlazaCatalog
          v-else
          :items="items"
          :loading="loading"
          :error="error"
          :currency="currency"
          empty-title="暂无公开模型"
          empty-description="平台还没有对所有用户开放的模型。"
          @retry="load"
        />
      </section>
    </main>

    <LandingFooter />
  </div>
</template>
