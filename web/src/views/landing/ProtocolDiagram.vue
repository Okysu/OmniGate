<script setup lang="ts">
import type { Component } from 'vue'
import { ArrowDown, Blocks, Bot, Braces, Cpu, MessageSquareText, Sparkles } from '@lucide/vue'
import AppLogo from '@/components/AppLogo.vue'

interface Node {
  title: string
  detail: string
  icon: Component
}

const clients: Node[] = [
  { title: 'OpenAI Chat Completions', detail: 'POST /v1/chat/completions', icon: MessageSquareText },
  { title: 'OpenAI Responses', detail: 'POST /v1/responses', icon: Braces },
  { title: 'Anthropic Messages', detail: 'POST /v1/messages', icon: Bot },
]

const upstreams: Node[] = [
  { title: 'OpenAI 系模型', detail: 'GPT、DeepSeek、通义等兼容接口', icon: Sparkles },
  { title: 'Claude 系模型', detail: 'Anthropic Messages 原生协议', icon: Cpu },
  { title: '更多模型', detail: '通过插件接入的其他服务商', icon: Blocks },
]

const steps = ['安全鉴权', '额度检查', '直连通道优先', '协议自动转换', '实时计费与明细']

/** Curves from each of `n` evenly spaced rows to the vertical middle (or back). */
function fan(n: number, reverse = false): string[] {
  return Array.from({ length: n }, (_, i) => {
    const y = ((i + 0.5) / n) * 100
    return reverse ? `M0 50 C50 50, 50 ${y}, 100 ${y}` : `M0 ${y} C50 ${y}, 50 50, 100 50`
  })
}
const inPaths = fan(clients.length)
const outPaths = fan(upstreams.length, true)
</script>

<template>
  <figure class="relative" aria-labelledby="protocol-diagram-caption">
    <figcaption id="protocol-diagram-caption" class="sr-only">
      你的应用用 OpenAI Chat Completions、OpenAI Responses 或 Anthropic Messages 协议请求平台，平台完成鉴权、额度检查、通道选择与协议转换后，转发到对应的模型服务。
    </figcaption>

    <div class="grid items-stretch gap-3 md:grid-cols-[minmax(0,1fr)_3.5rem_minmax(0,1.05fr)_3.5rem_minmax(0,1fr)] md:gap-0 lg:grid-cols-[minmax(0,1fr)_5rem_minmax(0,1fr)_5rem_minmax(0,1fr)]">
      <!-- Clients -->
      <div class="flex min-w-0 flex-col">
        <p class="text-muted-foreground mb-2 text-xs font-medium tracking-wide uppercase">
          客户端 / SDK
        </p>
        <ul class="grid flex-1 grid-cols-1 sm:grid-cols-3 md:flex md:flex-col">
          <li v-for="n in clients" :key="n.title" class="flex min-w-0 flex-1 py-1 sm:px-1 md:px-0 md:py-1.5">
            <div class="lp-node flex w-full min-w-0 items-center gap-3 rounded-lg border px-3 py-2.5">
              <span class="lp-icon flex size-8 shrink-0 items-center justify-center rounded-md">
                <component :is="n.icon" class="size-4" aria-hidden="true" />
              </span>
              <span class="min-w-0">
                <span class="block truncate text-sm font-medium">{{ n.title }}</span>
                <code class="text-muted-foreground block truncate font-mono text-[11px]">{{ n.detail }}</code>
              </span>
            </div>
          </li>
        </ul>
      </div>

      <div class="flex justify-center md:hidden" aria-hidden="true">
        <ArrowDown class="text-muted-foreground size-5" />
      </div>
      <div class="hidden flex-col md:flex" aria-hidden="true">
        <!-- Same height as the column labels so the wires line up with the cards. -->
        <p class="invisible mb-2 text-xs">
          &nbsp;
        </p>
        <svg class="lp-wires w-full flex-1" viewBox="0 0 100 100" preserveAspectRatio="none">
          <path v-for="(d, i) in inPaths" :key="i" :d="d" class="lp-wire" vector-effect="non-scaling-stroke" />
        </svg>
      </div>

      <!-- Gateway -->
      <div class="flex min-w-0 flex-col">
        <p class="text-muted-foreground mb-2 text-xs font-medium tracking-wide uppercase md:text-center">
          平台
        </p>
        <div class="lp-hub relative flex flex-1 flex-col justify-center gap-3 rounded-xl border p-4">
          <AppLogo class="justify-center text-base" />
          <ol class="grid grid-cols-2 gap-1.5 text-xs sm:grid-cols-3 md:grid-cols-1">
            <li v-for="(s, i) in steps" :key="s" class="bg-background/70 flex items-center gap-2 rounded-md border px-2 py-1.5">
              <span class="lp-step flex size-4 shrink-0 items-center justify-center rounded-full font-mono text-[10px] font-semibold">{{ i + 1 }}</span>
              <span class="truncate">{{ s }}</span>
            </li>
          </ol>
        </div>
      </div>

      <div class="flex justify-center md:hidden" aria-hidden="true">
        <ArrowDown class="text-muted-foreground size-5" />
      </div>
      <div class="hidden flex-col md:flex" aria-hidden="true">
        <p class="invisible mb-2 text-xs">
          &nbsp;
        </p>
        <svg class="lp-wires w-full flex-1" viewBox="0 0 100 100" preserveAspectRatio="none">
          <path v-for="(d, i) in outPaths" :key="i" :d="d" class="lp-wire" vector-effect="non-scaling-stroke" />
        </svg>
      </div>

      <!-- Upstreams -->
      <div class="flex min-w-0 flex-col">
        <p class="text-muted-foreground mb-2 text-xs font-medium tracking-wide uppercase md:text-right">
          模型服务
        </p>
        <ul class="grid flex-1 grid-cols-1 sm:grid-cols-3 md:flex md:flex-col">
          <li v-for="n in upstreams" :key="n.title" class="flex min-w-0 flex-1 py-1 sm:px-1 md:px-0 md:py-1.5">
            <div class="lp-node flex w-full min-w-0 items-center gap-3 rounded-lg border px-3 py-2.5">
              <span class="lp-icon flex size-8 shrink-0 items-center justify-center rounded-md">
                <component :is="n.icon" class="size-4" aria-hidden="true" />
              </span>
              <span class="min-w-0">
                <span class="block truncate text-sm font-medium">{{ n.title }}</span>
                <span class="text-muted-foreground block truncate text-[11px]">{{ n.detail }}</span>
              </span>
            </div>
          </li>
        </ul>
      </div>
    </div>
  </figure>
</template>

<style scoped>
.lp-node {
  background: var(--card);
}
.lp-icon {
  color: var(--lp-accent);
  background: color-mix(in oklch, var(--lp-accent) 12%, transparent);
}
.lp-hub {
  background:
    radial-gradient(120% 80% at 50% 0%, color-mix(in oklch, var(--lp-accent) 14%, transparent), transparent 70%),
    var(--card);
  border-color: color-mix(in oklch, var(--lp-accent) 35%, var(--border));
  box-shadow: 0 0 0 4px color-mix(in oklch, var(--lp-accent) 8%, transparent);
}
.lp-step {
  color: var(--lp-accent);
  background: color-mix(in oklch, var(--lp-accent) 14%, transparent);
}
.lp-wires {
  overflow: visible;
}
.lp-wire {
  fill: none;
  stroke: color-mix(in oklch, var(--lp-accent) 55%, var(--border));
  stroke-width: 1.5;
  stroke-dasharray: 5 5;
}
@media (prefers-reduced-motion: no-preference) {
  .lp-wire {
    animation: lp-flow 1.2s linear infinite;
  }
}
@keyframes lp-flow {
  to {
    stroke-dashoffset: -20;
  }
}
</style>
