<script setup lang="ts">
import type { MyPlazaModel } from '@/lib/types'
import { computed } from 'vue'
import { Badge } from '@/components/ui/badge'
import { freeBillingLabel, sourceEntries } from '@/lib/plaza'

/** "我的模型": channel sources per tier, billing and covering subscription. */
const props = defineProps<{ model: MyPlazaModel }>()
const sources = computed(() => sourceEntries(props.model))

const TIER_CLASSES = {
  own: 'border-emerald-500/40 text-emerald-700 dark:text-emerald-400',
  shared: 'border-sky-500/40 text-sky-700 dark:text-sky-400',
  platform: 'text-muted-foreground',
} as const
const TIER_TITLES = {
  own: '你自己的渠道：优先使用，不计费',
  shared: '其他用户共享给你的渠道：自有渠道之后使用，不计费',
  platform: '平台渠道：自有与共享渠道都不可用时使用，按平台价格计费',
} as const
</script>

<template>
  <div class="flex flex-wrap items-center gap-1.5" data-testid="my-model-badges">
    <Badge
      v-for="s in sources"
      :key="s.tier"
      variant="outline"
      class="font-normal"
      :class="TIER_CLASSES[s.tier]"
      :title="`${TIER_TITLES[s.tier]}（${s.count} 个可用渠道）`"
      :data-source="s.tier"
    >
      {{ s.label }} {{ s.count }}
    </Badge>
    <Badge
      v-if="model.billing === 'free'"
      variant="secondary"
      class="bg-emerald-500/10 font-normal text-emerald-700 dark:text-emerald-300"
      :title="model.sources.platform > 0 ? '自有 / 共享渠道的请求不计费；全部失败后回退到平台渠道时按平台价格计费' : '自有 / 共享渠道的请求不计费'"
      data-testid="billing-free"
    >
      {{ freeBillingLabel(model) }}
    </Badge>
    <Badge
      v-if="model.subscription"
      variant="outline"
      class="max-w-full border-violet-500/40 font-normal text-violet-700 dark:text-violet-300"
      :title="`有效订阅覆盖该模型（平台渠道的请求计入套餐额度）：${model.subscription.planName}`"
      data-testid="subscription-badge"
    >
      <span class="truncate">套餐：{{ model.subscription.planName }}</span>
    </Badge>
  </div>
</template>
