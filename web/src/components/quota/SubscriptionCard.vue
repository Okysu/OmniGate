<script setup lang="ts">
import type { QuotaOverflow, Subscription } from '@/lib/types'
import { computed } from 'vue'
import { ArrowUpRight, ShieldBan, WalletCards } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { formatDateTime } from '@/lib/format'
import { formatRemaining, modelsSummary, QUOTA_OVERFLOW_SHORT, SUBSCRIPTION_SOURCE_LABELS, SUBSCRIPTION_STATUS_LABELS } from '@/lib/quota'
import RuleUsageItem from './RuleUsageItem.vue'
import SubscriptionStatusBadge from './SubscriptionStatusBadge.vue'

const props = defineProps<{
  subscription: Subscription
  now?: number
  /** The owner's 「套餐额度用完后」 preference; omitted when unknown (e.g. still loading). */
  overflow?: QuotaOverflow | null
  /** Owner's own billing page: link an active subscription to 购买套餐 (renew / upgrade). Never in admin views. */
  storeLink?: boolean
}>()

const live = computed(() => props.subscription.status === 'active')
const exceeded = computed(() => live.value && props.subscription.rules.some(r => r.exceeded))
</script>

<template>
  <article
    class="bg-card text-card-foreground ring-foreground/10 flex min-w-0 flex-col gap-4 rounded-xl p-4 ring-1"
    :class="exceeded ? 'ring-destructive/40' : ''"
    :aria-label="`订阅：${subscription.plan.name}`"
  >
    <header class="flex flex-wrap items-start justify-between gap-2">
      <div class="min-w-0 space-y-1">
        <h3 class="truncate font-medium">
          {{ subscription.plan.name }}
        </h3>
        <p class="text-muted-foreground text-xs tabular-nums">
          {{ formatDateTime(subscription.startsAt) }} 至 {{ formatDateTime(subscription.endsAt) }}
          <template v-if="live">
            （{{ formatRemaining(subscription.endsAt, now) }}）
          </template>
        </p>
      </div>
      <div class="flex shrink-0 items-center gap-1.5">
        <Badge v-if="exceeded" variant="destructive">
          已达上限
        </Badge>
        <SubscriptionStatusBadge :status="subscription.status" />
      </div>
    </header>
    <div class="space-y-4">
      <RuleUsageItem v-for="r in subscription.rules" :key="r.id" :rule="r" :now="now" :live="live" :overflow="overflow" />
    </div>
    <footer class="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 border-t pt-3 text-xs">
      <span v-if="live && overflow" class="text-foreground inline-flex items-center gap-1" data-testid="overflow-note">
        <WalletCards v-if="overflow === 'wallet'" class="size-3.5" aria-hidden="true" />
        <ShieldBan v-else class="size-3.5" aria-hidden="true" />
        额度用完后：{{ QUOTA_OVERFLOW_SHORT[overflow] }}
      </span>
      <span>适用模型：{{ modelsSummary(subscription.models, 3) }}</span>
      <span>来源：{{ SUBSCRIPTION_SOURCE_LABELS[subscription.source] ?? subscription.source }}</span>
      <span v-if="!live">状态：{{ SUBSCRIPTION_STATUS_LABELS[subscription.status] ?? subscription.status }}</span>
      <RouterLink
        v-if="live && storeLink"
        to="/console/store"
        class="text-primary ml-auto inline-flex items-center gap-0.5 font-medium underline-offset-4 hover:underline"
        data-testid="subscription-store-link"
      >
        续费 / 升级
        <ArrowUpRight class="size-3.5" aria-hidden="true" />
      </RouterLink>
    </footer>
  </article>
</template>
