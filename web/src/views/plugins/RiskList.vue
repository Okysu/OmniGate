<script setup lang="ts">
// Static risk-scan findings. Advisory only: scanning cannot prove a plugin is safe.
import type { RiskFinding } from '@/lib/types'
import { ShieldAlert, ShieldCheck } from '@lucide/vue'

defineProps<{
  risk: RiskFinding[]
  interactive?: boolean
}>()
defineEmits<{ select: [r: RiskFinding] }>()
</script>

<template>
  <div class="space-y-2">
    <p class="text-muted-foreground text-xs">
      静态风险扫描（如 eval、可疑字符串、未声明的主机）仅作提示，不能作为安全保证；运行时仍由沙箱、网络白名单与资源限制约束。
    </p>
    <p v-if="risk.length === 0" class="text-muted-foreground flex items-center gap-1.5 text-sm">
      <ShieldCheck class="size-4" />
      未发现风险提示
    </p>
    <ul v-else class="divide-y rounded-lg border text-sm">
      <li v-for="(r, i) in risk" :key="i">
        <component
          :is="interactive ? 'button' : 'div'"
          :type="interactive ? 'button' : undefined"
          class="flex w-full items-start gap-2 px-3 py-2 text-left"
          :class="interactive ? 'hover:bg-muted/60 cursor-pointer outline-none focus-visible:bg-muted/60' : ''"
          @click="interactive && $emit('select', r)"
        >
          <ShieldAlert class="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <span class="min-w-0 flex-1 space-y-0.5">
            <span class="block break-words">{{ r.message }}</span>
            <span class="text-muted-foreground block font-mono text-xs">{{ r.rule }}</span>
          </span>
          <span class="text-muted-foreground shrink-0 font-mono text-xs">{{ r.file }}{{ r.line > 0 ? `:${r.line}` : '' }}</span>
        </component>
      </li>
    </ul>
  </div>
</template>
