<script setup lang="ts">
import type { Component } from 'vue'
import { computed, onMounted, ref, watchEffect } from 'vue'
import {
  ArrowLeftRight,
  ArrowRight,
  BarChart3,
  BookOpen,
  Boxes,
  CircleCheck,
  LogIn,
  ShieldCheck,
  Wallet,
  Zap,
} from '@lucide/vue'
import CopyButton from '@/components/CopyButton.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useSite } from '@/composables/useSite'
import { CONSOLE_BASE, loginLocation } from '@/lib/paths'
import { usageSnippets } from '@/lib/snippets'
import { useAuthStore } from '@/stores/auth'
import { useSystemStore } from '@/stores/system'
import LandingFooter from './LandingFooter.vue'
import LandingHeader from './LandingHeader.vue'
import ProtocolDiagram from './ProtocolDiagram.vue'

// Public page: only the session probe (/api/me, done by the router guard; a 401
// just means "logged out") and the public /api/system/info are called here.
const auth = useAuthStore()
const system = useSystemStore()
onMounted(() => {
  void system.ensureLoaded()
})

const { siteName, docsUrl } = useSite()
const user = computed(() => auth.user)
const loginTo = loginLocation(CONSOLE_BASE)

watchEffect(() => {
  document.title = `${siteName.value} · 大模型 API 平台`
})

interface Feature {
  icon: Component
  title: string
  description: string
  points: string[]
}

const FEATURES: Feature[] = [
  {
    icon: Zap,
    title: '低延迟',
    description: '流式响应逐字转发，首字即到；平台自身处理开销仅为毫秒级，请求优先走无需协议转换的直连通道。',
    points: ['流式逐字输出', '毫秒级平台开销'],
  },
  {
    icon: ShieldCheck,
    title: '稳定可用',
    description: '同一模型由多条上游通道提供，某条通道限流或故障时自动切换到下一条，异常通道自动熔断，请求不中断。',
    points: ['故障自动切换', '异常通道自动熔断'],
  },
  {
    icon: Boxes,
    title: '一个 Key，所有模型',
    description: '平台上的所有模型只需一个 API Key；在模型广场查看每个模型的能力、上下文长度与价格。也可以接入你自己的上游密钥，走自己的通道不收费。',
    points: ['模型广场', '自带密钥免费用'],
  },
  {
    icon: ArrowLeftRight,
    title: '兼容现有工具',
    description: '兼容 OpenAI 与 Anthropic 接口，Claude Code、Cherry Studio 和各类 SDK 改个地址即可使用；协议自动转换，工具调用与思考内容完整保留。',
    points: ['OpenAI / Anthropic / Responses', '图片、嵌入接口'],
  },
  {
    icon: Wallet,
    title: '透明计费',
    description: '按量计费，价格公开，每次请求都有明细；支持套餐与兑换码，套餐额度用完后是暂停还是改用余额，由你自己决定。',
    points: ['逐条请求账单', '套餐与兑换码'],
  },
  {
    icon: BarChart3,
    title: '用量一目了然',
    description: '每次请求的模型、耗时、tokens 与费用都能查，按天统计用量与花费；余额不足、额度将尽、价格变动都会提醒你。',
    points: ['请求明细与统计', '邮件与站内提醒'],
  },
]

// Quick start: samples use this deployment's own origin.
const snippets = usageSnippets(window.location.origin)
const SAMPLE_TABS = [
  { key: 'curl', label: 'curl', code: snippets.curl },
  { key: 'python', label: 'Python', code: snippets.openai },
  { key: 'node', label: 'Node.js', code: snippets.openaiNode },
] as const
type SampleKey = typeof SAMPLE_TABS[number]['key']
const sample = ref<SampleKey>('curl')
const sampleCode = computed(() => SAMPLE_TABS.find(t => t.key === sample.value)?.code ?? '')

const STEPS = [
  { title: '登录并充值', description: '登录后在“钱包与订阅”中充值或兑换套餐，也可以先接入自己的上游密钥。' },
  { title: '创建 API Key', description: '可以限制可用的模型和 IP，完整密钥只显示一次。' },
  { title: '把 SDK 指向平台', description: '将 base_url 改为下面的地址，其余代码保持不变。' },
]

// Illustrative request trace for the hero (not live data).
const TRACE = [
  { label: '鉴权', value: 'API Key og-…3f9a', state: 'ok' },
  { label: '额度', value: '套餐「Pro」· 5 小时会话 37%', state: 'ok' },
  { label: '通道', value: '通道 A 繁忙 → 自动切换通道 B', state: 'warn' },
  { label: '转换', value: 'Anthropic Messages → OpenAI Chat', state: 'ok' },
  { label: '计费', value: '1,284 tokens · 记入请求日志', state: 'ok' },
] as const
</script>

<template>
  <div class="landing bg-background text-foreground flex min-h-svh flex-col">
    <LandingHeader />

    <main class="flex-1">
      <!-- Hero -->
      <section class="relative overflow-hidden border-b">
        <div class="lp-hero-bg pointer-events-none absolute inset-0" aria-hidden="true" />
        <div class="lp-grid pointer-events-none absolute inset-0" aria-hidden="true" />
        <div class="relative mx-auto grid w-full max-w-6xl items-center gap-12 px-4 pt-16 pb-20 sm:px-6 sm:pt-24 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,0.9fr)] lg:pb-28">
          <div class="min-w-0 space-y-7">
            <h1 class="text-4xl leading-[1.1] font-semibold tracking-tight text-balance sm:text-5xl lg:text-6xl">
              一个入口，<br>
              <span class="lp-gradient-text">接入所有大模型</span>
            </h1>
            <p class="text-muted-foreground max-w-xl text-base leading-relaxed text-pretty sm:text-lg">
              用一个 API Key 调用平台上的所有大模型：兼容 OpenAI 与 Anthropic 接口，流式低延迟输出，多条上游通道自动切换保障稳定，按量计费、价格透明。
            </p>
            <div class="flex flex-col gap-3 sm:flex-row">
              <Button v-if="user" size="lg" class="h-11 px-5" as-child data-testid="hero-primary">
                <RouterLink :to="CONSOLE_BASE">
                  进入控制台
                  <ArrowRight />
                </RouterLink>
              </Button>
              <Button v-else size="lg" class="h-11 px-5" as-child data-testid="hero-primary">
                <RouterLink :to="loginTo">
                  <LogIn />
                  登录
                </RouterLink>
              </Button>
              <Button v-if="docsUrl" variant="outline" size="lg" class="bg-background/70 h-11 px-5" as-child>
                <a :href="docsUrl" target="_blank" rel="noopener noreferrer">
                  <BookOpen />
                  阅读文档
                </a>
              </Button>
              <Button v-else variant="outline" size="lg" class="bg-background/70 h-11 px-5" as-child>
                <RouterLink :to="{ hash: '#features' }">
                  查看特性
                </RouterLink>
              </Button>
            </div>
            <p v-if="user" class="text-muted-foreground text-sm">
              已登录为 <span class="text-foreground font-medium">{{ user.displayName }}</span>
            </p>
          </div>

          <!-- Illustrative request trace -->
          <div class="min-w-0" aria-label="请求处理示意">
            <div class="lp-window overflow-hidden rounded-xl border shadow-xl shadow-black/5 dark:shadow-black/30">
              <div class="flex items-center gap-2 border-b px-4 py-2.5">
                <span class="flex gap-1.5" aria-hidden="true">
                  <span class="size-2.5 rounded-full bg-red-400/80" />
                  <span class="size-2.5 rounded-full bg-amber-400/80" />
                  <span class="size-2.5 rounded-full bg-emerald-400/80" />
                </span>
                <span class="text-muted-foreground ml-2 truncate font-mono text-xs">请求处理 · 示意</span>
              </div>
              <div class="space-y-3 p-4 font-mono text-xs sm:p-5 sm:text-[13px]">
                <p class="flex min-w-0 items-center gap-2">
                  <span class="lp-method shrink-0 rounded px-1.5 py-0.5 text-[11px] font-semibold">POST</span>
                  <span class="truncate">/v1/messages</span>
                  <span class="text-muted-foreground ml-auto hidden shrink-0 sm:inline">stream</span>
                </p>
                <ol class="space-y-2 border-l pl-3">
                  <li v-for="t in TRACE" :key="t.label" class="flex min-w-0 items-center gap-2">
                    <CircleCheck class="size-3.5 shrink-0" :class="t.state === 'warn' ? 'text-amber-500' : 'text-emerald-500'" aria-hidden="true" />
                    <span class="text-muted-foreground w-8 shrink-0 font-sans">{{ t.label }}</span>
                    <span class="truncate">{{ t.value }}</span>
                  </li>
                </ol>
                <p class="flex min-w-0 items-center gap-2 border-t pt-3">
                  <span class="shrink-0 rounded bg-emerald-500/15 px-1.5 py-0.5 text-[11px] font-semibold text-emerald-700 dark:text-emerald-400">200</span>
                  <span class="text-muted-foreground truncate font-sans">流式响应已按 Anthropic 格式返回客户端</span>
                </p>
              </div>
            </div>
          </div>
        </div>
      </section>

      <!-- Features -->
      <section id="features" class="scroll-mt-20 py-20 sm:py-24" aria-labelledby="features-title">
        <div class="mx-auto w-full max-w-6xl px-4 sm:px-6">
          <div class="mx-auto max-w-2xl space-y-3 text-center">
            <p class="lp-eyebrow text-sm font-medium">
              为什么选择 {{ siteName }}
            </p>
            <h2 id="features-title" class="text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
              更快、更稳、更省心
            </h2>
            <p class="text-muted-foreground text-pretty">
              为开发者与团队准备的大模型 API 服务：改一个地址，就能用上平台上的所有模型。
            </p>
          </div>
          <ul class="mt-12 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <li v-for="f in FEATURES" :key="f.title" class="lp-card group bg-card flex min-w-0 flex-col gap-3 rounded-xl border p-5 transition-shadow hover:shadow-md">
              <span class="lp-icon flex size-10 items-center justify-center rounded-lg">
                <component :is="f.icon" class="size-5" aria-hidden="true" />
              </span>
              <h3 class="font-semibold">
                {{ f.title }}
              </h3>
              <p class="text-muted-foreground text-sm leading-relaxed">
                {{ f.description }}
              </p>
              <ul class="mt-auto flex flex-wrap gap-1.5 pt-1">
                <li v-for="p in f.points" :key="p">
                  <Badge variant="secondary" class="font-normal">
                    {{ p }}
                  </Badge>
                </li>
              </ul>
            </li>
          </ul>
        </div>
      </section>

      <!-- Protocol conversion -->
      <section id="protocols" class="bg-muted/40 scroll-mt-20 border-y py-20 sm:py-24" aria-labelledby="protocols-title">
        <div class="mx-auto w-full max-w-6xl px-4 sm:px-6">
          <div class="mx-auto mb-12 max-w-2xl space-y-3 text-center">
            <p class="lp-eyebrow text-sm font-medium">
              协议转换
            </p>
            <h2 id="protocols-title" class="text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
              客户端说什么协议都行
            </h2>
            <p class="text-muted-foreground text-pretty">
              无论你用 OpenAI 还是 Anthropic 的 SDK，平台都优先走同协议的直连通道；需要时自动转换请求与流式响应，工具调用和思考内容都不丢。
            </p>
          </div>
          <ProtocolDiagram />
        </div>
      </section>

      <!-- Quick start -->
      <section id="quickstart" class="scroll-mt-20 py-20 sm:py-24" aria-labelledby="quickstart-title">
        <div class="mx-auto grid w-full max-w-6xl gap-10 px-4 sm:px-6 lg:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)] lg:gap-14">
          <div class="min-w-0 space-y-6">
            <div class="space-y-3">
              <p class="lp-eyebrow text-sm font-medium">
                快速开始
              </p>
              <h2 id="quickstart-title" class="text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
                三步接入，<br>代码几乎不用改
              </h2>
            </div>
            <ol class="space-y-4">
              <li v-for="(s, i) in STEPS" :key="s.title" class="flex gap-3">
                <span class="lp-step-num flex size-7 shrink-0 items-center justify-center rounded-full text-sm font-semibold">{{ i + 1 }}</span>
                <div class="min-w-0 space-y-0.5">
                  <p class="font-medium">
                    {{ s.title }}
                  </p>
                  <p class="text-muted-foreground text-sm">
                    {{ s.description }}
                  </p>
                </div>
              </li>
            </ol>
            <div class="bg-card flex min-w-0 items-center gap-2 rounded-lg border p-2 pl-3">
              <span class="text-muted-foreground shrink-0 text-xs">Base URL</span>
              <code class="min-w-0 flex-1 truncate font-mono text-sm" data-testid="landing-base-url">{{ snippets.baseUrl }}</code>
              <CopyButton :value="snippets.baseUrl" label="复制 Base URL" />
            </div>
          </div>

          <div class="lp-code min-w-0 overflow-hidden rounded-xl border">
            <div class="flex items-center gap-1 border-b border-white/10 px-2 py-1.5" role="tablist" aria-label="示例语言">
              <button
                v-for="t in SAMPLE_TABS"
                :key="t.key"
                type="button"
                role="tab"
                :aria-selected="sample === t.key"
                class="rounded-md px-3 py-1.5 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-white/40 focus-visible:outline-none"
                :class="sample === t.key ? 'bg-white/10 text-white' : 'text-zinc-400 hover:text-zinc-200'"
                @click="sample = t.key"
              >
                {{ t.label }}
              </button>
              <CopyButton :value="sampleCode" label="复制代码" class="ml-auto text-zinc-300 hover:bg-white/10 hover:text-white" />
            </div>
            <pre class="overflow-x-auto p-4 font-mono text-xs leading-relaxed text-zinc-100 sm:text-[13px]"><code>{{ sampleCode }}</code></pre>
            <p class="border-t border-white/10 px-4 py-2 text-[11px] text-zinc-400">
              示例中的密钥是占位符，请在控制台「API Keys」中创建并替换；模型名见「模型广场」。
            </p>
          </div>
        </div>
      </section>
    </main>

    <LandingFooter />
  </div>
</template>

<style scoped>
.landing {
  --lp-accent: oklch(0.52 0.2 264);
  --lp-accent-2: oklch(0.6 0.15 195);
}
:global(.dark) .landing {
  --lp-accent: oklch(0.74 0.14 264);
  --lp-accent-2: oklch(0.78 0.12 195);
}
.lp-hero-bg {
  background:
    radial-gradient(55% 60% at 15% 0%, color-mix(in oklch, var(--lp-accent) 16%, transparent), transparent 70%),
    radial-gradient(45% 55% at 95% 20%, color-mix(in oklch, var(--lp-accent-2) 14%, transparent), transparent 70%);
}
.lp-grid {
  background-image:
    linear-gradient(to right, color-mix(in oklch, var(--foreground) 6%, transparent) 1px, transparent 1px),
    linear-gradient(to bottom, color-mix(in oklch, var(--foreground) 6%, transparent) 1px, transparent 1px);
  background-size: 48px 48px;
  mask-image: radial-gradient(ellipse 80% 70% at 50% 0%, black 30%, transparent 75%);
}
.lp-gradient-text {
  background-image: linear-gradient(100deg, var(--lp-accent), var(--lp-accent-2));
  background-clip: text;
  -webkit-background-clip: text;
  color: transparent;
}
.lp-dot {
  background: var(--lp-accent);
  box-shadow: 0 0 0 3px color-mix(in oklch, var(--lp-accent) 25%, transparent);
}
.lp-eyebrow {
  color: var(--lp-accent);
}
.lp-icon {
  color: var(--lp-accent);
  background: color-mix(in oklch, var(--lp-accent) 12%, transparent);
}
.lp-card:hover {
  border-color: color-mix(in oklch, var(--lp-accent) 35%, var(--border));
}
.lp-step-num {
  color: var(--lp-accent);
  background: color-mix(in oklch, var(--lp-accent) 14%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in oklch, var(--lp-accent) 30%, transparent);
}
.lp-window {
  background: color-mix(in oklch, var(--card) 88%, transparent);
  backdrop-filter: blur(8px);
}
.lp-method {
  color: var(--lp-accent);
  background: color-mix(in oklch, var(--lp-accent) 14%, transparent);
}
/* Code panels stay dark in both themes. */
.lp-code {
  background: oklch(0.18 0.01 264);
  border-color: oklch(0.3 0.01 264);
}
</style>
