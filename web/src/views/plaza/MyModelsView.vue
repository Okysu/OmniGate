<script setup lang="ts">
import { computed } from 'vue'
import { ArrowRight, Plus, RefreshCw, UsersRound } from '@lucide/vue'
import PageHeader from '@/components/PageHeader.vue'
import PlazaCatalog from '@/components/plaza/PlazaCatalog.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { usePlazaData } from '@/composables/usePlazaData'
import { plazaApi } from '@/lib/endpoints'
import { isNonUnitMultiplier, multiplierLabel, MULTIPLIER_TONE_CLASSES, multiplierTone } from '@/lib/groups'
import { consolePath } from '@/lib/paths'
import { useAuthStore } from '@/stores/auth'

// "我的模型": every model the user can call (own + shared + platform channels).
const auth = useAuthStore()
const { items, loading, error, currency, load } = usePlazaData(signal => plazaApi.mine(signal))
const canAddChannel = computed(() => auth.can('channels.write'))
const newChannel = { path: consolePath('channels'), query: { new: '1' } }

/** phase8 §1.1: the group multiplier (from /api/me, else from the items). */
const groupMultiplier = computed(() => {
  const own = auth.group?.priceMultiplier
  if (own)
    return isNonUnitMultiplier(own) ? own : null
  return items.value.find(m => isNonUnitMultiplier(m.priceMultiplier))?.priceMultiplier ?? null
})

const STEPS = [
  { n: 1, title: '自有渠道', text: '你自己添加的渠道最先使用，不计费。', cls: 'text-emerald-700 bg-emerald-500/10 dark:text-emerald-300' },
  { n: 2, title: '共享渠道', text: '其他用户共享给你的渠道其次，同样不计费。', cls: 'text-sky-700 bg-sky-500/10 dark:text-sky-300' },
  { n: 3, title: '平台渠道', text: '前两者都不可用时回退到平台，按平台价格计费；套餐额度只作用于平台渠道。', cls: 'text-muted-foreground bg-muted' },
]
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="我的模型" description="你可以通过 API Key 调用的全部模型，包括自有、共享与平台渠道提供的模型。">
      <template #actions>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button v-if="canAddChannel" size="sm" as-child data-testid="add-channel">
          <RouterLink :to="newChannel">
            <Plus />
            添加我的渠道
          </RouterLink>
        </Button>
      </template>
    </PageHeader>

    <Card class="py-4" data-testid="routing-explainer">
      <CardContent class="space-y-3 px-4">
        <p class="text-sm font-medium">
          请求如何路由与计费
        </p>
        <ol class="grid gap-2 md:grid-cols-[1fr_auto_1fr_auto_1fr] md:items-stretch">
          <template v-for="(s, i) in STEPS" :key="s.n">
            <li class="flex min-w-0 items-start gap-2.5 rounded-lg border p-3">
              <span class="flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold" :class="s.cls">{{ s.n }}</span>
              <div class="min-w-0 space-y-0.5">
                <p class="text-sm font-medium">
                  {{ s.title }}
                </p>
                <p class="text-muted-foreground text-xs leading-relaxed">
                  {{ s.text }}
                </p>
              </div>
            </li>
            <li v-if="i < STEPS.length - 1" class="text-muted-foreground hidden items-center md:flex" aria-hidden="true">
              <ArrowRight class="size-4" />
            </li>
          </template>
        </ol>
        <p class="text-muted-foreground text-xs">
          同一档内按优先级、协议匹配度与权重选择渠道。管理员的路由规则只作用于平台渠道。
        </p>
        <p v-if="groupMultiplier" class="bg-muted/40 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border px-3 py-2 text-sm" data-testid="my-group">
          <UsersRound class="text-muted-foreground size-4" aria-hidden="true" />
          <span>你的分组：<strong class="font-medium">{{ auth.group?.name || '—' }}</strong></span>
          <Badge variant="outline" class="font-normal tabular-nums" :class="MULTIPLIER_TONE_CLASSES[multiplierTone(groupMultiplier)]">
            {{ multiplierLabel(groupMultiplier) }}
          </Badge>
          <span class="text-muted-foreground text-xs">下方平台渠道价格已按分组倍率计算，划线价为原价。</span>
        </p>
      </CardContent>
    </Card>

    <PlazaCatalog
      mode="mine"
      :items="items"
      :loading="loading"
      :error="error"
      :currency="currency"
      empty-title="暂无可用模型"
      empty-description="平台还没有对你开放的模型，你也可以添加自己的渠道。"
      @retry="load"
    >
      <template #empty>
        <Button v-if="canAddChannel" size="sm" as-child>
          <RouterLink :to="newChannel">
            <Plus />
            添加我的渠道
          </RouterLink>
        </Button>
      </template>
    </PlazaCatalog>
  </div>
</template>
