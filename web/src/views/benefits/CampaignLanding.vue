<script setup lang="ts">
import type { PublicCampaign } from '@/lib/benefits'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { toast } from 'vue-sonner'
import { Gift, Share2 } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { benefitsApi, campaignState } from '@/lib/benefits'
import { useCurrency } from '@/composables/useCurrency'
import { errorMessage } from '@/lib/api'
import { formatDuration, ruleSummary } from '@/lib/quota'
import { useAuthStore } from '@/stores/auth'
const { money } = useCurrency()
const route = useRoute()
const auth = useAuthStore()
const campaign = ref<PublicCampaign | null>(null)
const error = ref('')
const claiming = ref(false)
const count = ref(0)
const key = ref(crypto.randomUUID())
const now = ref(Date.now())
const timer = setInterval(() => { now.value = Date.now() }, 10000)
onBeforeUnmount(() => clearInterval(timer))
const state = computed(() => campaign.value ? campaignState(campaign.value, now.value) : '')
async function load() {
  error.value = ''
  try {
    campaign.value = await benefitsApi.get(String(route.params.id))
    if (auth.isAuthenticated) count.value = (await benefitsApi.claims()).counts[campaign.value.id] ?? 0
  }
  catch (err) { error.value = errorMessage(err) }
}
watch(() => route.params.id, () => { campaign.value = null; count.value = 0; key.value = crypto.randomUUID(); void load() }, { immediate: true })
async function claim() {
  if (!campaign.value || claiming.value) return
  claiming.value = true
  error.value = ''
  try {
    await benefitsApi.claim(campaign.value.id, key.value, campaign.value.price)
    key.value = crypto.randomUUID()
    toast.success(campaign.value.rewardType === 'balance' ? '余额已到账' : '套餐已开通')
    await load()
  }
  catch (err) { error.value = errorMessage(err) }
  finally { claiming.value = false }
}
async function share() {
  try { await navigator.clipboard.writeText(location.href); toast.success('活动链接已复制') }
  catch { toast.error('复制失败，请复制浏览器地址栏链接') }
}
</script>

<template>
  <main class="mx-auto min-h-screen max-w-3xl space-y-8 px-5 py-12 sm:py-20">
    <RouterLink to="/console/benefits" class="text-muted-foreground text-sm">
      ← 福利中心
    </RouterLink>
    <p v-if="error" role="alert" class="text-destructive rounded-lg border p-4">
      {{ error }}
    </p>
    <template v-if="campaign">
      <header class="space-y-5">
        <div class="bg-primary/10 text-primary inline-flex rounded-2xl p-4">
          <Gift class="size-8" />
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <Badge>{{ state }}</Badge><span class="text-muted-foreground text-sm">每人可领取 {{ campaign.perUserLimit }} 次</span>
        </div>
        <h1 class="text-3xl font-semibold tracking-tight sm:text-4xl">
          {{ campaign.title }}
        </h1>
        <p class="text-muted-foreground whitespace-pre-wrap leading-relaxed">
          {{ campaign.description }}
        </p>
      </header>
      <Card>
        <CardHeader><CardTitle>{{ campaign.rewardType === 'balance' ? `$${campaign.amount} 余额奖励` : campaign.plan?.name }}</CardTitle></CardHeader>
        <CardContent class="space-y-4">
          <template v-if="campaign.plan">
            <p>套餐有效期 {{ formatDuration(campaign.plan.duration) }}，从领取时开始计算。</p>
            <ul class="list-inside list-disc text-sm">
              <li v-for="rule in campaign.plan.rules" :key="rule.id">
                {{ ruleSummary(rule, money) }}
              </li>
            </ul>
            <p class="text-muted-foreground break-words text-xs">
              支持模型：{{ campaign.plan.models.join('、') || '全部模型' }}
            </p>
          </template>
          <p v-if="campaign.stock >= 0">
            剩余 {{ campaign.stock }} 份
          </p>
          <p v-if="campaign.startsAt" class="text-muted-foreground text-sm">
            开始：{{ new Date(campaign.startsAt).toLocaleString('zh-CN') }}
          </p>
          <p v-if="campaign.endsAt" class="text-muted-foreground text-sm">
            截止：{{ new Date(campaign.endsAt).toLocaleString('zh-CN') }}
          </p>
          <div class="rounded-lg border p-4 text-sm">
            <p class="mb-2 font-medium">
              领取条件
            </p><ul v-if="campaign.requirements.length" class="list-inside list-disc space-y-1">
              <li v-for="item in campaign.requirements" :key="item">
                {{ item }}
              </li>
            </ul><p v-else>
              登录后即可领取。
            </p>
          </div>
          <p class="text-2xl font-semibold">
            {{ Number(campaign.price) > 0 ? `$${campaign.price}（从钱包余额支付）` : '免费领取' }}
          </p>
          <p v-if="auth.isAuthenticated" class="text-muted-foreground text-sm">
            你已领取 {{ count }} / {{ campaign.perUserLimit }} 次
          </p>
          <div class="flex flex-wrap gap-3">
            <Button v-if="auth.isAuthenticated" :disabled="claiming || state !== '进行中' || count >= campaign.perUserLimit" @click="claim">
              {{ claiming ? '正在发放…' : count >= campaign.perUserLimit ? '已达到领取上限' : Number(campaign.price) > 0 ? '支付并领取' : '立即领取' }}
            </Button>
            <Button v-else as-child>
              <RouterLink :to="{ path: '/login', query: { redirect: route.fullPath } }">
                登录后参与
              </RouterLink>
            </Button>
            <Button variant="outline" @click="share">
              <Share2 class="mr-2 size-4" />分享活动
            </Button>
          </div>
        </CardContent>
      </Card>
    </template>
    <p v-else-if="!error" class="text-muted-foreground">
      正在加载活动…
    </p>
  </main>
</template>
