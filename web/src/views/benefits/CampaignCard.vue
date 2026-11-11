<script setup lang="ts">
import type { PublicCampaign } from '@/lib/benefits'
import { computed, onBeforeUnmount, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { Gift } from '@lucide/vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { campaignState } from '@/lib/benefits'
const props = defineProps<{ campaign: PublicCampaign }>()
const now = ref(Date.now())
const timer = setInterval(() => { now.value = Date.now() }, 30000)
onBeforeUnmount(() => clearInterval(timer))
const state = computed(() => campaignState(props.campaign, now.value))
</script>

<template>
  <Card class="flex flex-col">
    <CardHeader>
      <div class="flex items-center justify-between gap-3">
        <Gift class="text-primary size-5" /><Badge variant="secondary">
          {{ state }}
        </Badge>
      </div>
      <CardTitle class="mt-3">
        {{ campaign.title }}
      </CardTitle>
    </CardHeader>
    <CardContent class="flex flex-1 flex-col gap-4">
      <p class="text-muted-foreground line-clamp-3 whitespace-pre-wrap text-sm">
        {{ campaign.description }}
      </p>
      <p class="text-xl font-semibold">
        {{ campaign.rewardType === 'balance' ? `$${campaign.amount} 余额` : campaign.plan?.name }}
      </p>
      <div class="text-muted-foreground space-y-1 text-xs">
        <p>{{ Number(campaign.price) > 0 ? `$${campaign.price} / 次` : '免费领取' }} · 每人 {{ campaign.perUserLimit }} 次</p>
        <p v-if="campaign.stock >= 0">
          剩余 {{ campaign.stock }} 份
        </p>
        <p v-if="campaign.endsAt">
          截止 {{ new Date(campaign.endsAt).toLocaleString('zh-CN') }}
        </p>
      </div>
      <Button as-child class="mt-auto">
        <RouterLink :to="`/benefits/${campaign.id}`">
          查看活动
        </RouterLink>
      </Button>
    </CardContent>
  </Card>
</template>
